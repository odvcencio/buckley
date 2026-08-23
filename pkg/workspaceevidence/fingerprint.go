package workspaceevidence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const maxFingerprintBytes int64 = 64 << 20

// GitStateFingerprint returns a content digest of tracked changes relative to
// HEAD and non-ignored untracked files. It emits no workspace contents and is
// stable across repeated observations of the same state.
func GitStateFingerprint(ctx context.Context, root string) (string, error) {
	hash := sha256.New()
	written := int64(0)
	write := func(kind string, value []byte) error {
		written += int64(len(kind) + len(value) + 2)
		if written > maxFingerprintBytes {
			return fmt.Errorf("workspace state exceeds %d-byte observation limit", maxFingerprintBytes)
		}
		_, _ = io.WriteString(hash, kind)
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(value)
		_, _ = hash.Write([]byte{0})
		return nil
	}

	diff, err := gitOutput(ctx, root, "diff", "--binary", "--no-ext-diff", "HEAD", "--")
	if err != nil {
		return "", fmt.Errorf("observe tracked workspace state: %w", err)
	}
	if err := write("tracked", diff); err != nil {
		return "", err
	}

	untrackedRaw, err := gitOutput(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", fmt.Errorf("observe untracked workspace state: %w", err)
	}
	paths := splitNULPaths(untrackedRaw)
	sort.Strings(paths)
	for _, path := range paths {
		clean := filepath.Clean(path)
		if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("observe untracked workspace state: unsafe path %q", path)
		}
		full := filepath.Join(root, clean)
		info, err := os.Lstat(full)
		if err != nil {
			return "", fmt.Errorf("observe untracked workspace state %q: %w", path, err)
		}
		if err := write("path", []byte(filepath.ToSlash(clean))); err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return "", fmt.Errorf("observe untracked symlink %q: %w", path, err)
			}
			if err := write("symlink", []byte(target)); err != nil {
				return "", err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("observe untracked workspace state %q: unsupported file type", path)
		}
		if info.Size() < 0 || written+info.Size() > maxFingerprintBytes {
			return "", fmt.Errorf("workspace state exceeds %d-byte observation limit", maxFingerprintBytes)
		}
		content, err := os.ReadFile(full)
		if err != nil {
			return "", fmt.Errorf("observe untracked workspace state %q: %w", path, err)
		}
		if err := write("content", content); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func gitOutput(ctx context.Context, root string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	stdout := boundedBuffer{limit: maxFingerprintBytes}
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if stdout.exceeded {
		return nil, fmt.Errorf("git %s output exceeds observation limit", args[0])
	}
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(stderr.String()))
		}
		return nil, err
	}
	return stdout.buffer.Bytes(), nil
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
