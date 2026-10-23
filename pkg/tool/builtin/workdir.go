package builtin

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type workDirAware struct {
	previewMutation  bool
	workDir          string
	readRoots        []string
	deniedReadPaths  []string
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
	return w.execContextWithParent(context.Background())
}

func (w *workDirAware) execContextWithParent(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if w == nil || w.maxExecTime <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, w.maxExecTime)
}

// FileMutationLimit reports the effective whole-file mutation budget.
func (w *workDirAware) FileMutationLimit() int64 {
	return w.mutationFileLimit()
}

// ResolvePath applies the same workspace fence used during execution.
func (w *workDirAware) ResolvePath(raw string) (string, error) {
	return resolvePath(w.workDir, raw)
}

func resolvePath(workDir, raw string) (string, error) {
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
	if base == "" || target == "" {
		return false
	}
	base = filepath.Clean(base)
	target = filepath.Clean(target)
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
	if path == "" {
		return ""
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil && resolved != "" {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(path)
}

func evalSymlinksFallbackForTarget(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != "" {
		return filepath.Clean(resolved)
	}

	return resolveMissingTarget(path)
}

// Keep new-file resolution stable even when parent directories do not exist.
// filepath.EvalSymlinks alone cannot resolve such a target.
func resolveMissingTarget(path string) string {
	parent := filepath.Dir(path)
	if parent == path {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(parent); err == nil {
		return filepath.Join(resolved, filepath.Base(path))
	}
	return filepath.Join(resolveMissingTarget(parent), filepath.Base(path))
}
