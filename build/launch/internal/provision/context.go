package provision

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	ModuleLockSchema    = "buckley.launch.modules.v1"
	ContextManifestType = "buckley.launch.context.v1"
	trustedGitBinary    = "/usr/bin/git"
	maxManifestBytes    = 8 << 20
	maxManifestTotal    = 32 << 20
	maxContextEntries   = 64
	gitOperationTimeout = 30 * time.Second
)

var gitCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type SourceRoots struct {
	GSXMail string
	GoSX    string
	TQWebP  string
}

type ManifestLock struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type RepositoryLock struct {
	Name      string         `json:"name"`
	Commit    string         `json:"commit"`
	Manifests []ManifestLock `json:"manifests"`
}

type ModuleLock struct {
	Schema       string           `json:"schema"`
	Repositories []RepositoryLock `json:"repositories"`
}

type ContextManifest struct {
	Schema           string `json:"schema"`
	ContextSHA256    string `json:"context_sha256"`
	ModuleLockSHA256 string `json:"module_lock_sha256"`
	ToolchainSHA256  string `json:"toolchain_lock_sha256"`
	Entries          int    `json:"entries"`
	Bytes            int64  `json:"bytes"`
}

type repoSpec struct {
	name      string
	root      string
	manifests []string
}

var assetAllowlist = []string{
	"Dockerfile",
	"go.mod",
	"toolchain.lock",
	"licenses/TinyGo-LICENSE",
	"cmd/probe/main_linux.go",
	"cmd/probe/main_other.go",
	"cmd/supervisor/main_linux.go",
	"cmd/supervisor/main_other.go",
}

func repositorySpecs(roots SourceRoots) []repoSpec {
	return []repoSpec{
		{name: "gsxmail", root: roots.GSXMail, manifests: []string{"go.mod", "go.sum", "LICENSE"}},
		{name: "gosx", root: roots.GoSX, manifests: []string{"go.mod", "go.sum", "LICENSE", "editor/go.mod", "editor/go.sum", "cmd/buildbootstrap/go.mod", "cmd/buildbootstrap/go.sum"}},
		{name: "tqwebp", root: roots.TQWebP, manifests: []string{"go.mod", "go.sum", "LICENSE", "bench/deepteams/go.mod", "bench/deepteams/go.sum"}},
	}
}

func CollectModuleLock(ctx context.Context, roots SourceRoots) (ModuleLock, error) {
	if err := validateTrustedExecutable(trustedGitBinary); err != nil {
		return ModuleLock{}, errors.New("trusted git is unavailable")
	}
	lock := ModuleLock{Schema: ModuleLockSchema}
	var total int64
	for _, spec := range repositorySpecs(roots) {
		repo, bytesRead, err := collectRepository(ctx, spec)
		if err != nil {
			return ModuleLock{}, fmt.Errorf("%s module source is invalid", spec.name)
		}
		total += bytesRead
		if total > maxManifestTotal {
			return ModuleLock{}, errors.New("module manifests exceed the bounded input budget")
		}
		lock.Repositories = append(lock.Repositories, repo)
	}
	return lock, nil
}

func collectRepository(ctx context.Context, spec repoSpec) (RepositoryLock, int64, error) {
	root, err := canonicalDirectory(spec.root)
	if err != nil {
		return RepositoryLock{}, 0, err
	}
	operationCtx, cancel := boundedContext(ctx, gitOperationTimeout)
	defer cancel()
	top, err := runGit(operationCtx, root, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(strings.TrimSpace(top)) != root {
		return RepositoryLock{}, 0, errors.New("repository root mismatch")
	}
	head, err := runGit(operationCtx, root, "rev-parse", "--verify", "HEAD")
	if err != nil || !gitCommitPattern.MatchString(strings.TrimSpace(head)) {
		return RepositoryLock{}, 0, errors.New("repository HEAD is invalid")
	}
	head = strings.TrimSpace(head)
	repo := RepositoryLock{Name: spec.name, Commit: head}
	var total int64
	for _, relative := range spec.manifests {
		if !validRelativePath(relative) {
			return RepositoryLock{}, 0, errors.New("manifest path is invalid")
		}
		headData, err := readTrackedHEADBlob(operationCtx, root, relative)
		if err != nil {
			return RepositoryLock{}, 0, err
		}
		data, err := readStableRegular(filepath.Join(root, filepath.FromSlash(relative)), maxManifestBytes)
		if err != nil {
			return RepositoryLock{}, 0, err
		}
		if !bytes.Equal(data, headData) {
			return RepositoryLock{}, 0, errors.New("manifest differs from its tracked HEAD blob")
		}
		digest := sha256.Sum256(data)
		repo.Manifests = append(repo.Manifests, ManifestLock{Path: relative, SHA256: hex.EncodeToString(digest[:]), Bytes: int64(len(data))})
		total += int64(len(data))
	}
	after, err := runGit(operationCtx, root, "rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(after) != head {
		return RepositoryLock{}, 0, errors.New("repository changed during inspection")
	}
	return repo, total, nil
}

func readTrackedHEADBlob(ctx context.Context, root, relative string) ([]byte, error) {
	tree, err := runGitBytes(ctx, root, 4096, "ls-tree", "-z", "HEAD", "--", relative)
	if err != nil || len(tree) < 2 || tree[len(tree)-1] != 0 || bytes.Count(tree, []byte{0}) != 1 {
		return nil, errors.New("manifest is not a unique tracked HEAD entry")
	}
	record := string(tree[:len(tree)-1])
	metadata, path, ok := strings.Cut(record, "\t")
	fields := strings.Fields(metadata)
	if !ok || path != relative || len(fields) != 3 || fields[1] != "blob" || fields[0] != "100644" && fields[0] != "100755" || !gitCommitPattern.MatchString(fields[2]) {
		return nil, errors.New("manifest HEAD entry is invalid")
	}
	data, err := runGitBytes(ctx, root, maxManifestBytes, "cat-file", "blob", "HEAD:"+relative)
	if err != nil {
		return nil, errors.New("manifest HEAD blob is unavailable")
	}
	return data, nil
}

func SynthesizeBuildContext(ctx context.Context, assetsRoot, destination string, roots SourceRoots) (ContextManifest, ModuleLock, error) {
	canonicalAssets, err := canonicalDirectory(assetsRoot)
	if err != nil {
		return ContextManifest{}, ModuleLock{}, errors.New("launch asset root is invalid")
	}
	assetsRoot = canonicalAssets
	toolchain, toolchainDigest, err := LoadToolchainLock(filepath.Join(assetsRoot, "toolchain.lock"))
	if err != nil || toolchain.ValidateRuntime() != nil {
		return ContextManifest{}, ModuleLock{}, errors.New("sealed toolchain is unavailable")
	}
	moduleLock, err := CollectModuleLock(ctx, roots)
	if err != nil {
		return ContextManifest{}, ModuleLock{}, err
	}
	if err := createEmptyDirectory(destination); err != nil {
		return ContextManifest{}, ModuleLock{}, errors.New("build context destination is invalid")
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(destination)
		}
	}()

	for _, relative := range assetAllowlist {
		data, err := readStableRegular(filepath.Join(assetsRoot, filepath.FromSlash(relative)), maxManifestBytes)
		if err != nil {
			return ContextManifest{}, ModuleLock{}, errors.New("launch build asset is unavailable")
		}
		target := relative
		if relative != "Dockerfile" && relative != "toolchain.lock" {
			target = filepath.ToSlash(filepath.Join("launch", relative))
		}
		if err := writeContextFile(destination, target, data); err != nil {
			return ContextManifest{}, ModuleLock{}, err
		}
	}
	for repoIndex, repo := range moduleLock.Repositories {
		spec := repositorySpecs(roots)[repoIndex]
		for _, manifest := range repo.Manifests {
			data, err := readStableRegular(filepath.Join(spec.root, filepath.FromSlash(manifest.Path)), maxManifestBytes)
			if err != nil || digestBytes(data) != manifest.SHA256 || int64(len(data)) != manifest.Bytes {
				return ContextManifest{}, ModuleLock{}, errors.New("module manifest changed while synthesizing context")
			}
			target := filepath.ToSlash(filepath.Join("modules", repo.Name, manifest.Path))
			if err := writeContextFile(destination, target, data); err != nil {
				return ContextManifest{}, ModuleLock{}, err
			}
		}
	}
	moduleBytes, err := canonicalJSON(moduleLock)
	if err != nil {
		return ContextManifest{}, ModuleLock{}, errors.New("module lock serialization failed")
	}
	if err := writeContextFile(destination, "module-lock.json", moduleBytes); err != nil {
		return ContextManifest{}, ModuleLock{}, err
	}
	moduleDigest := digestBytes(moduleBytes)
	if err := normalizeContextTimes(destination, time.Unix(toolchain.SourceDateEpoch, 0).UTC()); err != nil {
		return ContextManifest{}, ModuleLock{}, err
	}

	revalidated, err := CollectModuleLock(ctx, roots)
	if err != nil || !reflect.DeepEqual(moduleLock, revalidated) {
		return ContextManifest{}, ModuleLock{}, errors.New("module sources changed while synthesizing context")
	}
	contextDigest, entries, size, err := digestContext(destination)
	if err != nil {
		return ContextManifest{}, ModuleLock{}, err
	}
	manifest := ContextManifest{
		Schema:           ContextManifestType,
		ContextSHA256:    contextDigest,
		ModuleLockSHA256: moduleDigest,
		ToolchainSHA256:  toolchainDigest,
		Entries:          entries,
		Bytes:            size,
	}
	cleanup = false
	return manifest, moduleLock, nil
}

func canonicalJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func digestContext(root string) (string, int, int64, error) {
	type entry struct {
		path string
		data []byte
	}
	var entries []entry
	var total int64
	err := filepath.WalkDir(root, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if item.Type()&os.ModeSymlink != 0 {
			return errors.New("build context contains a symlink")
		}
		if item.IsDir() {
			return nil
		}
		if !item.Type().IsRegular() || len(entries) >= maxContextEntries {
			return errors.New("build context shape is invalid")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || !validRelativePath(filepath.ToSlash(relative)) {
			return errors.New("build context path is invalid")
		}
		data, err := readStableRegular(path, maxManifestBytes)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > maxManifestTotal {
			return errors.New("build context exceeds the bounded input budget")
		}
		entries = append(entries, entry{path: filepath.ToSlash(relative), data: data})
		return nil
	})
	if err != nil {
		return "", 0, 0, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	hash := sha256.New()
	for _, item := range entries {
		fmt.Fprintf(hash, "%s\x00%d\x00", item.path, len(item.data))
		_, _ = hash.Write(item.data)
	}
	return hex.EncodeToString(hash.Sum(nil)), len(entries), total, nil
}

func writeContextFile(root, relative string, data []byte) error {
	if !validRelativePath(relative) {
		return errors.New("build context path is invalid")
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return errors.New("build context directory creation failed")
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		return errors.New("build context write failed")
	}
	return nil
}

func normalizeContextTimes(root string, timestamp time.Time) error {
	if timestamp.IsZero() || timestamp.Location() != time.UTC {
		return errors.New("build context timestamp is invalid")
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("build context timestamp normalization failed")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("build context contains a symlink")
		}
		if err := os.Chtimes(path, timestamp, timestamp); err != nil {
			return errors.New("build context timestamp normalization failed")
		}
		return nil
	})
}

func createEmptyDirectory(path string) error {
	if path == "" {
		return errors.New("destination is empty")
	}
	if _, err := os.Lstat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("destination already exists")
	}
	return os.Mkdir(path, 0o700)
}

func canonicalDirectory(path string) (string, error) {
	if path == "" {
		return "", errors.New("directory is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || filepath.Clean(absolute) != resolved {
		return "", errors.New("directory is not canonical")
	}
	info, err := os.Lstat(resolved)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("directory is unavailable")
	}
	return resolved, nil
}

func validRelativePath(path string) bool {
	return path != "" && path == filepath.ToSlash(filepath.Clean(path)) && path != "." && !strings.HasPrefix(path, "../") && !strings.HasPrefix(path, "/") && !strings.ContainsRune(path, 0)
}

func validateTrustedExecutable(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("trusted executable is unavailable")
	}
	return nil
}

func runGit(ctx context.Context, root string, args ...string) (string, error) {
	data, err := runGitBytes(ctx, root, 64<<10, args...)
	return string(data), err
}

func runGitBytes(ctx context.Context, root string, limit int, args ...string) ([]byte, error) {
	commandArgs := append([]string{
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.attributesFile=/dev/null",
		"-C", root,
	}, args...)
	cmd := exec.CommandContext(ctx, trustedGitBinary, commandArgs...)
	cmd.Env = []string{"HOME=/nonexistent", "LC_ALL=C", "PATH=/usr/bin:/bin", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_NO_REPLACE_OBJECTS=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat"}
	var output bytes.Buffer
	output.Grow(4096)
	cmd.Stdout = &limitedBuffer{buffer: &output, limit: limit}
	cmd.Stderr = &limitedBuffer{limit: 64 << 10}
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

type limitedBuffer struct {
	buffer *bytes.Buffer
	limit  int
	size   int
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	if b.size+len(data) > b.limit {
		return 0, errors.New("command output exceeded limit")
	}
	b.size += len(data)
	if b.buffer != nil {
		_, _ = b.buffer.Write(data)
	}
	return written, nil
}

func boundedContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(parent, timeout)
}

func digestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func regularLinkCount(info os.FileInfo) uint64 {
	if info == nil {
		return 0
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Nlink)
	}
	return 0
}
