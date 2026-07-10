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
	"unicode"
	"unicode/utf8"
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

	output, err := reviewSnapshotGitBytes(ctx, root, "ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return nil, fmt.Errorf("enumerate reviewable untracked files: %w", err)
	}

	paths := make([]string, 0)
	for _, raw := range bytes.Split(output, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(string(raw))))
		if path == "." || filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") {
			return nil, fmt.Errorf("unsafe untracked review path %q", string(raw))
		}
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
	sourceRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open untracked review root: %w", err)
	}
	defer sourceRoot.Close()
	capturedRoot, err := os.MkdirTemp("", "buckley-review-untracked-*")
	if err != nil {
		return nil, fmt.Errorf("create untracked review capture: %w", err)
	}
	defer os.RemoveAll(capturedRoot)

	files := make([]ReviewUntrackedFile, 0, len(paths))
	patchBytes := 0
	for _, path := range paths {
		if excludeReviewUntrackedPath(path) {
			return nil, fmt.Errorf("allowlisted untracked review path %q is excluded by the safety policy", path)
		}

		rootPath := filepath.FromSlash(path)
		info, err := sourceRoot.Lstat(rootPath)
		if err != nil {
			return nil, fmt.Errorf("inspect untracked review file %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("allowlisted untracked review path %q is not a regular file", path)
		}
		if info.Size() > int64(MaxReviewSnapshotPatchBytes-patchBytes) {
			return nil, fmt.Errorf("reviewable untracked files exceed %d-byte snapshot limit", MaxReviewSnapshotPatchBytes)
		}

		content, err := readStableReviewUntrackedFile(sourceRoot, rootPath, info)
		if err != nil {
			return nil, fmt.Errorf("read untracked review file %q: %w", path, err)
		}
		if len(content) == 0 || reviewUntrackedBinary(content) {
			return nil, fmt.Errorf("allowlisted untracked review path %q is empty, binary, or contains unsafe control bytes", path)
		}
		capturedPath := filepath.Join(capturedRoot, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(capturedPath), 0o700); err != nil {
			return nil, fmt.Errorf("prepare untracked review file %q: %w", path, err)
		}
		mode := os.FileMode(0o600)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o700
		}
		if err := os.WriteFile(capturedPath, content, mode); err != nil {
			return nil, fmt.Errorf("freeze untracked review file %q: %w", path, err)
		}

		patch, err := reviewUntrackedPatch(ctx, capturedRoot, path)
		if err != nil {
			return nil, err
		}
		if len(patch) == 0 {
			return nil, fmt.Errorf("allowlisted untracked review path %q produced no review patch", path)
		}
		patchBytes += len(patch)
		if patchBytes > MaxReviewSnapshotPatchBytes {
			return nil, fmt.Errorf("reviewable untracked files exceed %d-byte snapshot limit", MaxReviewSnapshotPatchBytes)
		}

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
	return files, nil
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
	defer file.Close()

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
	if !utf8.Valid(content) {
		return true
	}
	for _, b := range content {
		if (b < 0x20 && b != '\n' && b != '\r' && b != '\t') || b == 0x7f {
			return true
		}
	}
	return false
}

func excludeReviewUntrackedPath(path string) bool {
	lowerPath := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(lowerPath))
	if unsafeReviewUntrackedPath(path) || reviewUntrackedBinaryPath(base) || reviewUntrackedSecretDirectory(lowerPath) {
		return true
	}

	// Untracked instruction files are not allowed to change reviewer policy.
	if base == "agents.md" {
		return true
	}

	secretFiles := []string{
		".envrc",
		"credentials.json", "credentials.yaml", "credentials.yml",
		"secrets.json", "secrets.yaml", "secrets.yml",
		".secrets",
		"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa",
		".pem", ".key", ".p12", ".pfx",
		".htpasswd", ".netrc", ".npmrc", ".pypirc", ".git-credentials",
		"service-account.json", "serviceaccount.json",
		"kubeconfig", ".kube/config",
	}
	for _, secret := range secretFiles {
		if base == secret || strings.HasSuffix(base, secret) {
			return true
		}
	}
	safeDotenvExample := base == "sample.env" || base == "example.env" || base == ".env.example"
	if !safeDotenvExample && (strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".env") || strings.Contains(base, ".env.")) {
		return true
	}
	if reviewUntrackedSensitiveDataName(base) {
		return true
	}
	if lowerPath == ".kube/config" || strings.HasSuffix(lowerPath, "/.kube/config") {
		return true
	}
	if lowerPath == ".docker/config.json" || strings.HasSuffix(lowerPath, "/.docker/config.json") {
		return true
	}
	return strings.Contains(lowerPath, ".aws/") && (base == "credentials" || base == "config")
}

func unsafeReviewUntrackedPath(path string) bool {
	if !utf8.ValidString(path) {
		return true
	}
	for _, r := range path {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return true
		}
	}
	return false
}

func reviewUntrackedSecretDirectory(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts[:len(parts)-1] {
		switch part {
		case "secret", "secrets", ".secrets", "credential", "credentials", ".credentials", ".aws", ".kube", ".ssh":
			return true
		}
	}
	return false
}

func hasReviewSecretDataSuffix(base string) bool {
	extensions := []string{
		".json", ".yaml", ".yml", ".txt", ".ini", ".conf", ".cfg",
		".toml", ".properties", ".xml", ".csv",
	}
	for _, extension := range extensions {
		if strings.HasSuffix(base, extension) {
			return true
		}
	}
	return base == "secret" || base == "secrets" || base == "credential" || base == "credentials"
}

func reviewUntrackedSensitiveDataName(base string) bool {
	if !hasReviewSecretDataSuffix(base) {
		return false
	}
	markers := []string{
		"secret", "credential", "password", "passwd", "token",
		"api-key", "api_key", "apikey", "private-key", "private_key",
		"service-account", "service_account", "serviceaccount", "oauth",
	}
	for _, marker := range markers {
		if strings.Contains(base, marker) {
			return true
		}
	}
	return false
}

func reviewUntrackedBinaryPath(base string) bool {
	extensions := []string{
		".zip", ".tar", ".gz", ".bz2", ".7z", ".rar",
		".mp4", ".mov", ".avi", ".mkv", ".webm",
		".mp3", ".wav", ".flac", ".aac",
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico",
		".woff", ".woff2", ".ttf", ".otf",
		".psd", ".ai", ".sketch",
		".sqlite", ".sqlite3", ".db", ".wasm",
		".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx",
		".bin", ".o", ".a", ".so", ".dylib", ".dll", ".exe", ".class",
		".jar", ".war", ".apk",
	}
	for _, extension := range extensions {
		if strings.HasSuffix(base, extension) {
			return true
		}
	}
	return false
}
