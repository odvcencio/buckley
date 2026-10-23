package builtin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// errSecretFile is the refusal for files that tools never read from a read root.
const errSecretFile = "secret files are not readable through tools"

// SetReadRoots sets extra directories that read-only tools (read_file,
// list_directory, find_files, search_text, file_exists, code_outline) may
// resolve paths in, besides the workdir. Write, edit, delete, move and shell
// tools never use them. Each root is made absolute and cleaned; blank entries
// are dropped.
func (w *workDirAware) SetReadRoots(roots []string) {
	if w == nil {
		return
	}
	w.readRoots = w.readRoots[:0:0]
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if abs, err := filepath.Abs(root); err == nil {
			w.readRoots = append(w.readRoots, filepath.Clean(abs))
		}
	}
}

// ReadRoots returns a copy of the configured read roots.
func (w *workDirAware) ReadRoots() []string {
	if w == nil {
		return nil
	}
	return append([]string(nil), w.readRoots...)
}

// resolveRead resolves a path for a read-only tool: inside the workdir as
// before, or inside a read root. Paths in a read root get the same symlink
// hardening as the workdir, evaluated against that root, and files that look
// like secrets are refused.
func (w *workDirAware) resolveRead(raw string) (string, error) {
	abs, workErr := resolvePath(w.workDir, raw)
	if workErr == nil {
		if err := w.readDeniedError(abs); err != nil {
			return "", err
		}
		return abs, nil
	}
	if len(w.readRoots) == 0 || !filepath.IsAbs(raw) {
		return "", workErr
	}
	candidate := filepath.Clean(raw)
	for _, root := range w.readRoots {
		if !isWithinDir(root, candidate) {
			continue
		}
		if !isWithinDir(evalSymlinksFallback(root), evalSymlinksFallbackForTarget(candidate)) {
			return "", fmt.Errorf("path %q escapes read root %q via symlink", raw, root)
		}
		if err := w.readDeniedError(candidate); err != nil {
			return "", err
		}
		if err := readRootSecretError(candidate); err != nil {
			return "", err
		}
		return candidate, nil
	}
	return "", fmt.Errorf("%w; it is also outside the read roots", workErr)
}

// inReadRoot reports whether abs lies in a read root and not in the workdir.
func (w *workDirAware) inReadRoot(abs string) bool {
	if strings.TrimSpace(w.workDir) != "" {
		if base, err := filepath.Abs(w.workDir); err == nil && isWithinDir(base, abs) {
			return false
		}
	}
	for _, root := range w.readRoots {
		if isWithinDir(root, abs) {
			return true
		}
	}
	return false
}

// readRootSecretError returns the refusal for a path that may hold a secret:
// key and credential file names, anything under .catalog/identities, and any
// file that is not readable by group or others (mode bits 0044), whose owner
// chose to keep it private. The check looks at the path and, when it exists,
// at the file it resolves to.
func readRootSecretError(path string) error {
	for _, p := range []string{path, evalSymlinksFallbackForTarget(path)} {
		if secretPathName(p) {
			return fmt.Errorf("%s: %q", errSecretFile, filepath.Base(path))
		}
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o044 == 0 {
			return fmt.Errorf("%s: %q is a private file (mode %04o)", errSecretFile, filepath.Base(path), info.Mode().Perm())
		}
	}
	return nil
}

func secretPathName(path string) bool {
	slash := filepath.ToSlash(path)
	if strings.Contains(slash+"/", "/.catalog/identities/") {
		return true
	}
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.HasSuffix(base, ".key"), strings.HasSuffix(base, ".pem"),
		base == ".env", strings.HasPrefix(base, ".env."),
		strings.HasPrefix(base, "id_"), strings.HasPrefix(base, "credentials"):
		return true
	}
	return false
}

// readRootEntryHidden reports whether a directory entry in a read root must be
// left out of listings and searches, including explicitly denied workspace entries.
func (w *workDirAware) readRootEntryHidden(abs string) bool {
	if len(w.readRoots) == 0 && len(w.deniedReadPaths) == 0 {
		return false
	}
	_, err := w.resolveRead(abs)
	return err != nil
}

// resolveReadRel is resolveRead for tools that run a command in the workdir: it
// also returns the path relative to the workdir (which starts with ".." for a
// read root outside it).
func (w *workDirAware) resolveReadRel(raw string) (string, string, error) {
	abs, err := w.resolveRead(raw)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(w.workDir) == "" {
		return abs, abs, nil
	}
	base, err := filepath.Abs(w.workDir)
	if err != nil {
		return abs, abs, nil
	}
	rel, err := filepath.Rel(filepath.Clean(base), abs)
	if err != nil {
		return abs, abs, nil
	}
	return abs, rel, nil
}

// searchRecordHidden reports whether a search hit (path as the search command
// printed it, relative to the workdir) is a secret file in a read root.
func (w *workDirAware) searchRecordHidden(path string) bool {
	if (len(w.readRoots) == 0 && len(w.deniedReadPaths) == 0) || path == "" {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(w.workDir, path)
	}
	return w.readRootEntryHidden(filepath.Clean(path))
}

// SetDeniedReadPaths preserves explicit denied paths when additional read roots
// are granted. A nil list clears the previous denials.
func (w *workDirAware) SetDeniedReadPaths(paths []string) {
	if w != nil {
		w.deniedReadPaths = append([]string(nil), paths...)
	}
}

func (w *workDirAware) readDeniedError(abs string) error {
	if len(w.deniedReadPaths) == 0 {
		return nil
	}
	resolvedTarget := evalSymlinksFallbackForTarget(abs)
	for _, denied := range w.deniedReadPaths {
		if denied == "" {
			continue
		}
		if denied == "~" || strings.HasPrefix(denied, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolve denied home path: %w", err)
			}
			denied = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(denied, "~"), "/"))
		}
		if !filepath.IsAbs(denied) {
			denied = filepath.Join(w.workDir, denied)
		}
		denied, err := filepath.Abs(denied)
		if err != nil {
			return fmt.Errorf("invalid denied path: %w", err)
		}
		if isWithinDir(denied, abs) || isWithinDir(evalSymlinksFallback(denied), resolvedTarget) {
			return fmt.Errorf("path %q is denied", abs)
		}
	}
	return nil
}
