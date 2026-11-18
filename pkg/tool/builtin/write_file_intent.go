package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const maxWriteRecoveryBytes = 16 << 20

type writeFileIntentKey struct{}

// WriteFileIntent identifies the expected content before and after a local
// whole-file write. An empty Before digest represents a missing target.
type WriteFileIntent struct {
	Workspace string `json:"workspace"`
	Path      string `json:"path"`
	Before    string `json:"before"`
	After     string `json:"after"`
}

// WithWriteFileIntent records intent at the actual builtin write boundary,
// after the tool middleware has authorized dispatch.
func WithWriteFileIntent(ctx context.Context, record func(context.Context, WriteFileIntent) error) context.Context {
	return context.WithValue(ctx, writeFileIntentKey{}, record)
}

func prepareWriteFileIntent(workDir, absPath, content string) (WriteFileIntent, bool) {
	if workDir == "" || len(content) > maxWriteRecoveryBytes {
		return WriteFileIntent{}, false
	}
	workspace, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		return WriteFileIntent{}, false
	}
	path, err := filepath.Rel(workDir, absPath)
	if err != nil {
		return WriteFileIntent{}, false
	}
	intent := WriteFileIntent{Workspace: workspace, Path: path}
	before, err := intent.CurrentDigest()
	if err != nil {
		return WriteFileIntent{}, false
	}
	digest := sha256.Sum256([]byte(content))
	intent.Before = before
	intent.After = hex.EncodeToString(digest[:])
	return intent, intent.Before != intent.After
}

// CurrentDigest observes the target within the captured workspace. Recovery
// does not read through a symlink at the target or mutate the filesystem.
func (i WriteFileIntent) CurrentDigest() (string, error) {
	workspace, err := filepath.EvalSymlinks(i.Workspace)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(workspace) || workspace != i.Workspace || !filepath.IsLocal(i.Path) || i.Path == "." {
		return "", fmt.Errorf("invalid write intent workspace or path")
	}
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return "", err
	}
	defer root.Close()
	for path := i.Path; path != "."; path = filepath.Dir(path) {
		info, err := root.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("write recovery path contains a symlink")
		}
	}
	info, err := root.Lstat(i.Path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxWriteRecoveryBytes {
		return "", fmt.Errorf("write recovery requires a regular file up to %d bytes", maxWriteRecoveryBytes)
	}
	file, err := root.Open(i.Path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	size, err := io.Copy(digest, io.LimitReader(file, maxWriteRecoveryBytes+1))
	if err != nil {
		return "", err
	}
	if size > maxWriteRecoveryBytes {
		return "", fmt.Errorf("write recovery file exceeded %d bytes", maxWriteRecoveryBytes)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
