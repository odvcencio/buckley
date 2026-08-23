package model

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"m31labs.dev/buckley/pkg/secretsafety"
)

// ReviewUntrackedFile is an untracked text file selected for worktree review.
// Patch is a complete Git binary patch that adds Path to an empty tree.
type ReviewUntrackedFile struct {
	Path       string
	Patch      []byte
	Insertions int
}

func cloneReviewUntrackedFiles(files []ReviewUntrackedFile) []ReviewUntrackedFile {
	cloned := make([]ReviewUntrackedFile, len(files))
	for i, file := range files {
		cloned[i] = file
		cloned[i].Patch = append([]byte(nil), file.Patch...)
	}
	return cloned
}

// CaptureReviewUntrackedFiles captures only explicitly allowlisted reviewable
// untracked files without mutating the caller's index. Git's standard excludes
// are authoritative; sensitive-looking paths, binary content, symlinks, and
// agent instruction files cannot be opted into the review boundary.
func CaptureReviewUntrackedFiles(ctx context.Context, root string, allowlistedPaths []string) ([]ReviewUntrackedFile, error) {
	allowed, err := normalizeReviewUntrackedAllowlist(allowlistedPaths)
	if err != nil {
		return nil, err
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("at least one untracked review path must be explicitly allowlisted")
	}

	allPaths, err := enumerateReviewUntrackedPaths(ctx, root)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(allowed))
	for _, path := range allPaths {
		if _, ok := allowed[path]; ok {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	if len(paths) != len(allowed) {
		found := make(map[string]struct{}, len(paths))
		for _, path := range paths {
			found[path] = struct{}{}
		}
		missing := make([]string, 0, len(allowed)-len(paths))
		for path := range allowed {
			if _, ok := found[path]; !ok {
				missing = append(missing, path)
			}
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("allowlisted untracked review paths are not non-ignored untracked files: %s", strings.Join(missing, ", "))
	}
	files, _, err := captureReviewUntrackedPaths(ctx, root, paths, false)
	return files, err
}

// CaptureReviewableUntrackedFiles captures every non-ignored untracked file
// that passes the review boundary. Unsafe names, secrets, binary/control
// content, empty files, and files that exceed the snapshot budget are
// excluded and returned separately so callers can disclose the boundary.
func CaptureReviewableUntrackedFiles(ctx context.Context, root string) ([]ReviewUntrackedFile, []string, error) {
	paths, err := enumerateReviewUntrackedPaths(ctx, root)
	if err != nil {
		return nil, nil, err
	}
	return captureReviewUntrackedPaths(ctx, root, paths, true)
}

func enumerateReviewUntrackedPaths(ctx context.Context, root string) ([]string, error) {
	output, err := reviewSnapshotGitBytes(ctx, root, "ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return nil, fmt.Errorf("enumerate reviewable untracked files: %w", err)
	}
	paths := make([]string, 0)
	seen := make(map[string]struct{})
	for _, raw := range bytes.Split(output, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(string(raw))))
		if path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") {
			return nil, fmt.Errorf("unsafe untracked review path %q", string(raw))
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func captureReviewUntrackedPaths(ctx context.Context, root string, paths []string, skipUnsafe bool) ([]ReviewUntrackedFile, []string, error) {
	sourceRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, nil, fmt.Errorf("open untracked review root: %w", err)
	}
	defer func() { _ = sourceRoot.Close() }()
	capturedRoot, err := os.MkdirTemp("", "buckley-review-untracked-*")
	if err != nil {
		return nil, nil, fmt.Errorf("create untracked review capture: %w", err)
	}
	defer func() { _ = os.RemoveAll(capturedRoot) }()

	files := make([]ReviewUntrackedFile, 0, len(paths))
	excluded := make([]string, 0)
	patchBytes := 0
	for _, path := range paths {
		if excludeReviewUntrackedPath(path) {
			if skipUnsafe {
				excluded = append(excluded, path)
				continue
			}
			return nil, nil, fmt.Errorf("allowlisted untracked review path %q is excluded by the safety policy", path)
		}

		rootPath := filepath.FromSlash(path)
		info, err := sourceRoot.Lstat(rootPath)
		if err != nil {
			if skipUnsafe && os.IsNotExist(err) {
				excluded = append(excluded, path)
				continue
			}
			return nil, nil, fmt.Errorf("inspect untracked review file %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			if skipUnsafe {
				excluded = append(excluded, path)
				continue
			}
			return nil, nil, fmt.Errorf("allowlisted untracked review path %q is not a regular file", path)
		}
		if info.Size() > int64(MaxReviewSnapshotPatchBytes-patchBytes) {
			if skipUnsafe {
				excluded = append(excluded, path)
				continue
			}
			return nil, nil, fmt.Errorf("reviewable untracked files exceed %d-byte snapshot limit", MaxReviewSnapshotPatchBytes)
		}

		content, err := readStableReviewUntrackedFile(sourceRoot, rootPath, info)
		if err != nil {
			if skipUnsafe {
				excluded = append(excluded, path)
				continue
			}
			return nil, nil, fmt.Errorf("read untracked review file %q: %w", path, err)
		}
		if len(content) == 0 || reviewUntrackedBinary(content) || (skipUnsafe && reviewUntrackedSecretContent(content)) {
			if skipUnsafe {
				excluded = append(excluded, path)
				continue
			}
			return nil, nil, fmt.Errorf("allowlisted untracked review path %q is empty, binary, or contains unsafe control bytes", path)
		}
		capturedPath := filepath.Join(capturedRoot, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(capturedPath), 0o700); err != nil {
			return nil, nil, fmt.Errorf("prepare untracked review file %q: %w", path, err)
		}
		mode := os.FileMode(0o600)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o700
		}
		if err := os.WriteFile(capturedPath, content, mode); err != nil {
			return nil, nil, fmt.Errorf("freeze untracked review file %q: %w", path, err)
		}

		patch, err := reviewUntrackedPatch(ctx, capturedRoot, path)
		if err != nil {
			return nil, nil, err
		}
		if len(patch) == 0 {
			return nil, nil, fmt.Errorf("allowlisted untracked review path %q produced no review patch", path)
		}
		if patchBytes+len(patch) > MaxReviewSnapshotPatchBytes {
			if skipUnsafe {
				excluded = append(excluded, path)
				continue
			}
			return nil, nil, fmt.Errorf("reviewable untracked files exceed %d-byte snapshot limit", MaxReviewSnapshotPatchBytes)
		}
		patchBytes += len(patch)

		insertions := bytes.Count(content, []byte{'\n'})
		if content[len(content)-1] != '\n' {
			insertions++
		}
		files = append(files, ReviewUntrackedFile{
			Path:       path,
			Patch:      patch,
			Insertions: insertions,
		})
	}
	return files, excluded, nil
}

func normalizeReviewUntrackedAllowlist(paths []string) (map[string]struct{}, error) {
	allowed := make(map[string]struct{}, len(paths))
	for _, raw := range paths {
		raw = strings.TrimSpace(raw)
		path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(raw)))
		if raw == "" || path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") || unsafeReviewUntrackedPath(path) {
			return nil, fmt.Errorf("unsafe untracked review allowlist path %q", raw)
		}
		allowed[path] = struct{}{}
	}
	return allowed, nil
}

func readStableReviewUntrackedFile(root *os.Root, path string, expected os.FileInfo) ([]byte, error) {
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(expected, opened) {
		return nil, fmt.Errorf("file changed identity while being captured")
	}
	content, err := io.ReadAll(io.LimitReader(file, MaxReviewSnapshotPatchBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > MaxReviewSnapshotPatchBytes {
		return nil, fmt.Errorf("file exceeds %d-byte snapshot limit", MaxReviewSnapshotPatchBytes)
	}
	return content, nil
}

func reviewUntrackedPatch(ctx context.Context, root, path string) ([]byte, error) {
	args := []string{
		"--no-pager", "-C", root,
		"diff", "--no-index", "--binary", "--full-index", "--no-ext-diff", "--no-textconv",
		"--", "/dev/null", path,
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err == nil {
		return output, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && len(output) > 0 {
		return output, nil
	}
	return nil, fmt.Errorf("capture untracked review file %q: %w: %s", path, err, strings.TrimSpace(stderr.String()))
}

func reviewUntrackedBinary(content []byte) bool {
	return secretsafety.BinaryContent(content)
}

// reviewUntrackedSecretContent is deliberately conservative and only applies
// to automatic project capture. Explicit branch allowlists retain the prior
// behavior because they represent an operator-selected disclosure decision.
func reviewUntrackedSecretContent(content []byte) bool {
	return secretsafety.AutomaticDisclosureSecretContent(content)
}

func excludeReviewUntrackedPath(path string) bool {
	base := strings.ToLower(filepath.Base(filepath.ToSlash(path)))
	if secretsafety.SensitivePath(path) {
		return true
	}

	// Untracked instruction files are not allowed to change reviewer policy.
	if base == "agents.md" {
		return true
	}

	return false
}

func unsafeReviewUntrackedPath(path string) bool {
	return secretsafety.UnsafePath(path)
}

func reviewUntrackedBinaryPath(base string) bool {
	return secretsafety.BinaryPath(base)
}
