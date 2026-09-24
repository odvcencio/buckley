package workspaceevidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	fingerprintBufferBytes           = 128 << 10
	maxFingerprintContentBytes       = 2 << 30
	maxFingerprintEntries            = 200000
	maxFingerprintPathBytes    int64 = 8 << 20
	defaultFingerprintTimeout        = 30 * time.Second
)

// GitStateFingerprint returns a content digest of tracked changes relative to
// HEAD and non-ignored untracked files. It emits no workspace contents and is
// stable across repeated observations of the same state.
func GitStateFingerprint(ctx context.Context, root string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, fingerprintTimeout(root))
	defer cancel()
	topRaw, err := gitOutput(ctx, root, maxFingerprintPathBytes, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("observe workspace root: %w", err)
	}
	top := strings.TrimSpace(string(topRaw))
	if top == "" {
		return "", fmt.Errorf("observe workspace root: git top-level is empty")
	}
	root = top
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return "", fmt.Errorf("open workspace root: %w", err)
	}
	defer rootFS.Close()

	manifest, err := gitFingerprintManifest(ctx, root)
	if err != nil {
		return "", err
	}
	first, err := hashGitManifest(ctx, rootFS, manifest)
	if err != nil {
		return "", err
	}
	afterManifest, err := gitFingerprintManifest(ctx, root)
	if err != nil {
		return "", err
	}
	if !manifest.equal(afterManifest) {
		return "", fmt.Errorf("workspace git manifest changed during observation")
	}
	second, err := hashGitManifest(ctx, rootFS, afterManifest)
	if err != nil {
		return "", err
	}
	finalManifest, err := gitFingerprintManifest(ctx, root)
	if err != nil {
		return "", err
	}
	if !afterManifest.equal(finalManifest) || first != second {
		return "", fmt.Errorf("workspace state changed during observation")
	}
	return first, nil
}

type gitManifest struct {
	head           string
	unbornHeadRef  string
	index          []byte
	trackedPaths   []string
	untrackedPaths []string
}

func gitFingerprintManifest(ctx context.Context, root string) (gitManifest, error) {
	head, unbornHeadRef, err := gitFingerprintHead(ctx, root)
	if err != nil {
		return gitManifest{}, err
	}
	indexArgs := []string{"diff", "--cached", "--raw", "-z", "--no-ext-diff"}
	trackedArgs := []string{"diff", "--name-only", "-z", "--no-ext-diff"}
	if unbornHeadRef == "" {
		indexArgs = append(indexArgs, "HEAD")
		trackedArgs = append(trackedArgs, "HEAD")
	}
	indexArgs = append(indexArgs, "--")
	trackedArgs = append(trackedArgs, "--")

	indexRaw, err := gitOutput(ctx, root, maxFingerprintPathBytes, indexArgs...)
	if err != nil {
		return gitManifest{}, fmt.Errorf("observe index state: %w", err)
	}
	trackedRaw, err := gitOutput(ctx, root, maxFingerprintPathBytes, trackedArgs...)
	if err != nil {
		return gitManifest{}, fmt.Errorf("observe tracked workspace state: %w", err)
	}
	if unbornHeadRef != "" {
		indexedRaw, err := gitOutput(ctx, root, maxFingerprintPathBytes, "ls-files", "--cached", "-z")
		if err != nil {
			return gitManifest{}, fmt.Errorf("observe unborn index paths: %w", err)
		}
		if int64(len(indexedRaw)) > maxFingerprintPathBytes-int64(len(trackedRaw)) {
			return gitManifest{}, fmt.Errorf("unborn tracked paths exceed observation limit")
		}
		trackedRaw = append(trackedRaw, indexedRaw...)
	}
	untrackedRaw, err := gitOutput(ctx, root, maxFingerprintPathBytes, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return gitManifest{}, fmt.Errorf("observe untracked workspace state: %w", err)
	}
	trackedPaths := sortedUniquePaths(splitNULPaths(trackedRaw))
	untrackedPaths := splitNULPaths(untrackedRaw)
	sort.Strings(untrackedPaths)
	return gitManifest{
		head:           head,
		unbornHeadRef:  unbornHeadRef,
		index:          indexRaw,
		trackedPaths:   trackedPaths,
		untrackedPaths: untrackedPaths,
	}, nil
}

func gitFingerprintHead(ctx context.Context, root string) (head, unbornRef string, err error) {
	headRaw, headErr := gitOutput(ctx, root, maxFingerprintPathBytes, "rev-parse", "--verify", "HEAD")
	if headErr == nil {
		head = strings.TrimSpace(string(headRaw))
		if head == "" {
			return "", "", fmt.Errorf("observe HEAD state: resolved HEAD is empty")
		}
		return head, "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", "", fmt.Errorf("observe HEAD state: %w", err)
	}

	refRaw, symbolicErr := gitOutput(ctx, root, maxFingerprintPathBytes, "symbolic-ref", "--quiet", "--no-recurse", "HEAD")
	if symbolicErr != nil {
		return "", "", fmt.Errorf("observe HEAD state: %w", headErr)
	}
	ref := strings.TrimSpace(string(refRaw))
	if !strings.HasPrefix(ref, "refs/heads/") {
		return "", "", fmt.Errorf("observe HEAD state: unresolved HEAD does not name a branch ref")
	}
	if _, err := gitOutput(ctx, root, maxFingerprintPathBytes, "check-ref-format", ref); err != nil {
		return "", "", fmt.Errorf("observe HEAD state: invalid symbolic branch %q: %w", ref, err)
	}
	absent, err := gitBranchRefAbsent(ctx, root, ref)
	if err != nil {
		return "", "", fmt.Errorf("observe HEAD state: %w", err)
	}
	if !absent {
		return "", "", fmt.Errorf("observe HEAD state: symbolic branch %q exists but HEAD did not resolve", ref)
	}
	return "", ref, nil
}

func gitBranchRefAbsent(ctx context.Context, root, ref string) (bool, error) {
	result, err := runGitCommand(ctx, root, maxFingerprintPathBytes, "show-ref", "--verify", "--quiet", "--", ref)
	if err != nil {
		return false, err
	}
	switch result.exitCode {
	case 0:
		return false, nil
	case 1:
		symbolic, err := runGitCommand(ctx, root, maxFingerprintPathBytes, "symbolic-ref", "--quiet", "--no-recurse", ref)
		if err != nil {
			return false, err
		}
		switch symbolic.exitCode {
		case 0:
			return false, nil
		case 1:
			// Continue: a missing direct ref is the expected unborn state.
		default:
			return false, gitCommandExitError(symbolic, "symbolic-ref")
		}
		// Older Git versions report both a missing ref and some malformed refs
		// as status 1 under --quiet. for-each-ref preserves corruption warnings.
		diagnostic, err := runGitCommand(ctx, root, maxFingerprintPathBytes, "for-each-ref", "--format=%(refname)", "--", ref)
		if err != nil {
			return false, err
		}
		if diagnostic.exitCode != 0 {
			return false, gitCommandExitError(diagnostic, "for-each-ref")
		}
		if diagnostic.stderrExceeded {
			return false, fmt.Errorf("git for-each-ref diagnostics exceed observation limit")
		}
		if message := strings.TrimSpace(string(diagnostic.stderr)); message != "" {
			return false, fmt.Errorf("git for-each-ref: %s", message)
		}
		for _, candidate := range strings.Split(string(diagnostic.stdout), "\n") {
			if strings.TrimSpace(candidate) == ref {
				return false, nil
			}
		}
		return true, nil
	default:
		return false, gitCommandExitError(result, "show-ref")
	}
}

func (m gitManifest) equal(other gitManifest) bool {
	return m.head == other.head &&
		m.unbornHeadRef == other.unbornHeadRef &&
		bytes.Equal(m.index, other.index) &&
		stringSlicesEqual(m.trackedPaths, other.trackedPaths) &&
		stringSlicesEqual(m.untrackedPaths, other.untrackedPaths)
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func hashGitManifest(ctx context.Context, rootFS *os.Root, manifest gitManifest) (string, error) {
	hash := sha256.New()
	contentBytes := int64(0)
	if manifest.unbornHeadRef != "" {
		writeFingerprintRecord(hash, "git/head/unborn-ref", []byte(manifest.unbornHeadRef))
	} else {
		writeFingerprintRecord(hash, "git/head", []byte(manifest.head))
	}
	writeFingerprintRecord(hash, "git/index", manifest.index)

	entries := 0
	for _, path := range manifest.trackedPaths {
		if err := writePathFingerprint(ctx, hash, rootFS, "tracked", path, &contentBytes, &entries); err != nil {
			return "", err
		}
	}
	for _, path := range manifest.untrackedPaths {
		if err := writePathFingerprint(ctx, hash, rootFS, "untracked", path, &contentBytes, &entries); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writePathFingerprint(ctx context.Context, h hash.Hash, root *os.Root, kind, path string, contentBytes *int64, entries *int) error {
	clean := filepath.Clean(path)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("observe workspace state: unsafe path %q", path)
	}
	slashPath := filepath.ToSlash(clean)
	if entries != nil {
		*entries++
		if *entries > maxFingerprintEntries {
			return fmt.Errorf("workspace state exceeds %d-entry observation limit", maxFingerprintEntries)
		}
	}
	writeFingerprintRecord(h, kind+"/path", []byte(slashPath))

	info, err := root.Lstat(clean)
	if err != nil {
		if os.IsNotExist(err) {
			writeFingerprintRecord(h, kind+"/missing", []byte(slashPath))
			return nil
		}
		return fmt.Errorf("observe workspace state %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := root.Readlink(clean)
		if err != nil {
			return fmt.Errorf("observe workspace symlink %q: %w", path, err)
		}
		after, err := root.Lstat(clean)
		if err != nil {
			return fmt.Errorf("observe workspace symlink %q: %w", path, err)
		}
		if after.Mode()&os.ModeSymlink == 0 || !os.SameFile(info, after) || !sameLicenseMetadata(info, after) {
			return fmt.Errorf("observe workspace symlink %q: symlink changed during read", path)
		}
		writeFingerprintRecord(h, kind+"/symlink", []byte(target))
		return nil
	}
	if info.IsDir() {
		writeFingerprintRecord(h, kind+"/dir", []byte(slashPath))
		return writeDirectoryFingerprint(ctx, h, root, kind, clean, info, contentBytes, entries)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("observe workspace state %q: unsupported file type", path)
	}
	writeFingerprintRecord(h, kind+"/stat", []byte(fmt.Sprintf("%s %d", info.Mode().String(), info.Size())))
	if err := writeFileContentFingerprint(ctx, h, root, kind, clean, info, contentBytes); err != nil {
		return fmt.Errorf("observe workspace state %q: %w", path, err)
	}
	return nil
}

func writeDirectoryFingerprint(ctx context.Context, h hash.Hash, root *os.Root, kind, path string, before os.FileInfo, contentBytes *int64, entries *int) error {
	dir, err := root.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	opened, err := dir.Stat()
	if err != nil {
		return err
	}
	if !sameDirectory(before, opened) {
		return fmt.Errorf("directory changed before read")
	}
	dirEntries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	sort.Slice(dirEntries, func(i, j int) bool {
		return dirEntries[i].Name() < dirEntries[j].Name()
	})
	for _, entry := range dirEntries {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := entry.Name()
		if name == ".git" && entry.IsDir() {
			continue
		}
		child := filepath.Join(path, name)
		if err := writePathFingerprint(ctx, h, root, kind, child, contentBytes, entries); err != nil {
			return err
		}
	}
	afterRead, err := dir.Stat()
	if err != nil {
		return err
	}
	after, err := root.Lstat(path)
	if err != nil {
		return err
	}
	if !sameDirectory(before, afterRead) || !sameDirectory(before, after) {
		return fmt.Errorf("directory changed during read")
	}
	return nil
}

func writeFingerprintRecord(h hash.Hash, label string, value []byte) {
	_, _ = io.WriteString(h, label)
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(value)
	_, _ = h.Write([]byte{0})
}

func writeFileContentFingerprint(ctx context.Context, h hash.Hash, root *os.Root, kind, path string, before os.FileInfo, contentBytes *int64) error {
	if before.Size() < 0 {
		return fmt.Errorf("negative file size")
	}
	if contentBytes != nil {
		if before.Size() > maxFingerprintContentBytes-*contentBytes {
			return fmt.Errorf("workspace state exceeds %d-byte content observation limit", maxFingerprintContentBytes)
		}
	}
	file, err := root.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !sameRegularFile(before, opened) {
		return fmt.Errorf("file changed before read")
	}
	stopClose := context.AfterFunc(ctx, func() {
		_ = file.Close()
	})
	defer stopClose()

	contentHash := sha256.New()
	buf := make([]byte, fingerprintBufferBytes)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := file.Read(buf)
		if n > 0 {
			if contentBytes != nil {
				if int64(n) > maxFingerprintContentBytes-*contentBytes {
					return fmt.Errorf("workspace state exceeds %d-byte content observation limit", maxFingerprintContentBytes)
				}
				*contentBytes += int64(n)
			}
			_, _ = contentHash.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			if err := ctx.Err(); err != nil {
				return err
			}
			return readErr
		}
	}

	afterRead, err := file.Stat()
	if err != nil {
		return err
	}
	after, err := root.Lstat(path)
	if err != nil {
		return err
	}
	if !sameRegularFile(before, afterRead) || !sameRegularFile(before, after) {
		return fmt.Errorf("file changed during read")
	}
	writeFingerprintRecord(h, kind+"/sha256", []byte(hex.EncodeToString(contentHash.Sum(nil))))
	return nil
}

func sameDirectory(a, b os.FileInfo) bool {
	return a != nil &&
		b != nil &&
		a.IsDir() &&
		b.IsDir() &&
		os.SameFile(a, b) &&
		sameLicenseMetadata(a, b)
}

func sameRegularFile(a, b os.FileInfo) bool {
	return a != nil &&
		b != nil &&
		a.Mode().IsRegular() &&
		b.Mode().IsRegular() &&
		os.SameFile(a, b) &&
		a.Size() == b.Size() &&
		sameLicenseMetadata(a, b)
}

var gitOutput = gitOutputImpl

func gitOutputImpl(ctx context.Context, root string, limit int64, args ...string) ([]byte, error) {
	result, err := runGitCommand(ctx, root, limit, args...)
	if err != nil {
		return nil, err
	}
	if result.exitCode != 0 {
		return nil, gitCommandExitError(result, args[0])
	}
	return result.stdout, nil
}

type gitCommandResult struct {
	stdout         []byte
	stderr         []byte
	stderrExceeded bool
	exitCode       int
}

func runGitCommand(ctx context.Context, root string, limit int64, args ...string) (gitCommandResult, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	stdout := boundedBuffer{limit: limit}
	stderr := boundedBuffer{limit: limit}
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return gitCommandResult{}, fmt.Errorf("git %s: %w", args[0], ctx.Err())
	}
	if stdout.exceeded {
		return gitCommandResult{}, fmt.Errorf("git %s output exceeds observation limit", args[0])
	}
	result := gitCommandResult{
		stdout:         append([]byte(nil), stdout.buffer.Bytes()...),
		stderr:         append([]byte(nil), stderr.buffer.Bytes()...),
		stderrExceeded: stderr.exceeded,
	}
	if err == nil {
		return result, nil
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return gitCommandResult{}, err
	}
	result.exitCode = exitErr.ExitCode()
	return result, nil
}

func gitCommandExitError(result gitCommandResult, command string) error {
	message := strings.TrimSpace(string(result.stderr))
	if result.stderrExceeded {
		message += " (truncated)"
	}
	if message == "" {
		message = fmt.Sprintf("exit status %d", result.exitCode)
	}
	return fmt.Errorf("git %s: %s", command, message)
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	exceeded bool
}

func (w *boundedBuffer) Write(value []byte) (int, error) {
	remaining := w.limit - int64(w.buffer.Len())
	if remaining <= 0 {
		w.exceeded = true
		return len(value), nil
	}
	keep := int64(len(value))
	if keep > remaining {
		keep = remaining
		w.exceeded = true
	}
	_, _ = w.buffer.Write(value[:keep])
	return len(value), nil
}

func splitNULPaths(raw []byte) []string {
	parts := strings.Split(string(raw), "\x00")
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			paths = append(paths, part)
		}
	}
	return paths
}

func sortedUniquePaths(paths []string) []string {
	sort.Strings(paths)
	if len(paths) < 2 {
		return paths
	}
	unique := paths[:1]
	for _, path := range paths[1:] {
		if path != unique[len(unique)-1] {
			unique = append(unique, path)
		}
	}
	return unique
}
