package workspaceevidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WorkspaceStateFingerprint observes ordinary execution workspaces. Git
// repositories retain their Git state identity; other folders use a bounded
// content snapshot that excludes Buckley's runtime directory and Git metadata.
// Admission and review gates should use GitStateFingerprint directly.
func WorkspaceStateFingerprint(ctx context.Context, root string) (string, error) {
	state, err := workspaceStateFingerprint(ctx, root, false)
	return state.Digest, err
}

// WorkspaceStateFingerprintWithFallback permits the existing Git status
// fallback for best-effort execution. Invalid Git repositories still fail.
func WorkspaceStateFingerprintWithFallback(ctx context.Context, root string) (StateFingerprint, error) {
	return workspaceStateFingerprint(ctx, root, true)
}

func workspaceStateFingerprint(ctx context.Context, root string, bestEffort bool) (StateFingerprint, error) {
	if err := ctx.Err(); err != nil {
		return StateFingerprint{}, err
	}
	if strings.TrimSpace(root) == "" {
		return StateFingerprint{}, fmt.Errorf("observe workspace root: path is empty")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return StateFingerprint{}, err
	}
	git, err := workspaceUsesGit(root)
	if err != nil {
		return StateFingerprint{}, err
	}
	if git {
		if bestEffort {
			return GitStateFingerprintWithFallback(ctx, root)
		}
		digest, err := GitStateFingerprint(ctx, root)
		return StateFingerprint{Digest: digest}, err
	}
	digest, err := directoryStateFingerprint(ctx, root)
	return StateFingerprint{Digest: digest}, err
}

func workspaceUsesGit(root string) (bool, error) {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false, fmt.Errorf("observe workspace root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("observe workspace root: not a directory")
	}
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR"} {
		if os.Getenv(name) != "" {
			return true, nil
		}
	}
	for path := resolved; ; path = filepath.Dir(path) {
		if _, err := os.Lstat(filepath.Join(path, ".git")); err == nil {
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("observe workspace repository: %w", err)
		}
		if filepath.Dir(path) == path {
			return false, nil
		}
	}
}

func directoryStateFingerprint(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultFingerprintTimeout)
	defer cancel()
	root, err := os.OpenRoot(path)
	if err != nil {
		return "", err
	}
	defer root.Close()
	before, err := root.Stat(".")
	if err != nil {
		return "", err
	}
	buffer := make([]byte, fingerprintBufferBytes)
	first, err := hashDirectoryState(ctx, root, buffer)
	if err != nil {
		return "", err
	}
	second, err := hashDirectoryState(ctx, root, buffer)
	if err != nil {
		return "", err
	}
	if first != second {
		return "", fmt.Errorf("workspace state changed during observation")
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		return "", fmt.Errorf("workspace root changed during observation")
	}
	git, err := workspaceUsesGit(path)
	if err != nil {
		return "", err
	}
	if git {
		return "", fmt.Errorf("workspace repository changed during observation")
	}
	return "files:" + first, nil
}

type directoryManifest struct {
	files []string
	dirs  []string
}

func readDirectoryManifest(ctx context.Context, root *os.Root, maxEntries int, maxPathBytes int64) (directoryManifest, error) {
	manifest := directoryManifest{dirs: []string{"."}}
	pathBytes := int64(0)
	for index := 0; index < len(manifest.dirs); index++ {
		if err := appendDirectoryEntries(ctx, root, manifest.dirs[index], &manifest, &pathBytes, maxEntries, maxPathBytes); err != nil {
			return directoryManifest{}, err
		}
	}
	sort.Strings(manifest.files)
	sort.Strings(manifest.dirs)
	return manifest, nil
}

func appendDirectoryEntries(ctx context.Context, root *os.Root, path string, manifest *directoryManifest, pathBytes *int64, maxEntries int, maxPathBytes int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	before, err := root.Lstat(path)
	if err != nil {
		return err
	}
	dir, err := root.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	stopClose := context.AfterFunc(ctx, func() { _ = dir.Close() })
	defer stopClose()
	opened, err := dir.Stat()
	if err != nil {
		return err
	}
	if !sameDirectory(before, opened) {
		return fmt.Errorf("workspace directory %q changed before read", path)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := dir.ReadDir(256)
		for _, entry := range entries {
			name := entry.Name()
			if name == ".git" || (path == "." && name == ".buckley") {
				continue
			}
			child := filepath.Join(path, name)
			if len(manifest.files)+len(manifest.dirs) > maxEntries {
				return fmt.Errorf("workspace state exceeds %d-entry observation limit", maxEntries)
			}
			if int64(len(child)) > maxPathBytes-*pathBytes {
				return fmt.Errorf("workspace paths exceed %d-byte observation limit", maxPathBytes)
			}
			*pathBytes += int64(len(child))
			if entry.IsDir() {
				manifest.dirs = append(manifest.dirs, child)
			} else {
				manifest.files = append(manifest.files, child)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	after, err := root.Lstat(path)
	if err != nil {
		return err
	}
	if !sameDirectory(before, after) {
		return fmt.Errorf("workspace directory %q changed during read", path)
	}
	return nil
}

func hashDirectoryState(ctx context.Context, root *os.Root, buffer []byte) (string, error) {
	manifest, err := readDirectoryManifest(ctx, root, maxFingerprintEntries, maxFingerprintPathBytes)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	contentBytes := int64(0)
	for _, path := range manifest.dirs {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		info, err := root.Lstat(path)
		if err != nil {
			return "", err
		}
		if !info.IsDir() {
			return "", fmt.Errorf("workspace directory %q changed before hashing", path)
		}
		writeFingerprintRecord(digest, "directory/path", []byte(filepath.ToSlash(path)))
		writeFingerprintRecord(digest, "directory/mode", []byte(info.Mode().String()))
	}
	for _, path := range manifest.files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		info, err := root.Lstat(path)
		if err != nil {
			return "", err
		}
		writeFingerprintRecord(digest, "file/path", []byte(filepath.ToSlash(path)))
		if err := writeLeafFingerprint(ctx, digest, root, "file", path, info, &contentBytes, buffer); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
