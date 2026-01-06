package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type workDirAware struct {
	workDir          string
	env              map[string]string
	maxFileSizeBytes int64
	maxOutputBytes   int
	maxExecTime      time.Duration
}

func (w *workDirAware) SetWorkDir(dir string) {
	if w == nil {
		return
	}
	w.workDir = strings.TrimSpace(dir)
}

func (w *workDirAware) SetEnv(env map[string]string) {
	if w == nil {
		return
	}
	w.env = sanitizeEnvMap(env)
}

func (w *workDirAware) SetMaxFileSizeBytes(max int64) {
	if w == nil {
		return
	}
	if max <= 0 {
		w.maxFileSizeBytes = 0
		return
	}
	w.maxFileSizeBytes = max
}

func (w *workDirAware) SetMaxExecTimeSeconds(seconds int32) {
	if w == nil {
		return
	}
	if seconds <= 0 {
		w.maxExecTime = 0
		return
	}
	w.maxExecTime = time.Duration(seconds) * time.Second
}

func (w *workDirAware) SetMaxOutputBytes(max int) {
	if w == nil {
		return
	}
	if max <= 0 {
		w.maxOutputBytes = 0
		return
	}
	w.maxOutputBytes = max
}

func (w *workDirAware) execContext() (context.Context, context.CancelFunc) {
	if w == nil || w.maxExecTime <= 0 {
		return context.Background(), func() {}
	}
	return context.WithTimeout(context.Background(), w.maxExecTime)
}

func resolvePath(workDir, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("path cannot be empty")
	}

	// Default behavior for local (non-hosted) usage.
	if strings.TrimSpace(workDir) == "" {
		abs, err := filepath.Abs(raw)
		if err != nil {
			return "", fmt.Errorf("invalid path: %w", err)
		}
		return abs, nil
	}

	base, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("invalid workdir: %w", err)
	}
	base = filepath.Clean(base)

	var candidate string
	if filepath.IsAbs(raw) {
		candidate = filepath.Clean(raw)
	} else {
		candidate = filepath.Clean(filepath.Join(base, raw))
	}

	if !isWithinDir(base, candidate) {
		return "", fmt.Errorf("path %q escapes workdir", raw)
	}

	// Harden against symlink escapes.
	resolvedBase := evalSymlinksFallback(base)
	resolvedCandidate := evalSymlinksFallbackForTarget(candidate)
	if !isWithinDir(resolvedBase, resolvedCandidate) {
		return "", fmt.Errorf("path %q escapes workdir via symlink", raw)
	}

	return candidate, nil
}

func resolveRelPath(workDir, raw string) (string, string, error) {
	abs, err := resolvePath(workDir, raw)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(workDir) == "" {
		return abs, abs, nil
	}
	base, err := filepath.Abs(workDir)
	if err != nil {
		return abs, abs, nil
	}
	rel, err := filepath.Rel(filepath.Clean(base), abs)
	if err != nil {
		return abs, abs, nil
	}
	return abs, rel, nil
}

func isWithinDir(base, target string) bool {
	base = filepath.Clean(strings.TrimSpace(base))
	target = filepath.Clean(strings.TrimSpace(target))
	if base == "" || target == "" {
		return false
	}
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func evalSymlinksFallback(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil && strings.TrimSpace(resolved) != "" {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

func evalSymlinksFallbackForTarget(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && strings.TrimSpace(resolved) != "" {
		return filepath.Clean(resolved)
	}

	dir := filepath.Dir(path)
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err == nil && strings.TrimSpace(resolvedDir) != "" {
		return filepath.Clean(filepath.Join(resolvedDir, filepath.Base(path)))
	}

	// If the parent directory doesn't exist yet, fall back to the cleaned target.
	if _, statErr := os.Stat(dir); statErr != nil {
		return filepath.Clean(path)
	}

	return filepath.Clean(path)
}
