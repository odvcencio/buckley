package filepicker

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// GitIgnore provides pattern matching based on .gitignore files.
type GitIgnore struct {
	patterns []gitPattern
}

type gitPattern struct {
	pattern  string
	negation bool
	dirOnly  bool
}

// NewGitIgnore loads .gitignore patterns from the project root.
func NewGitIgnore(root string) *GitIgnore {
	gi := &GitIgnore{}

	// Load .gitignore from root
	gi.loadFile(filepath.Join(root, ".gitignore"))

	// Also load global gitignore if exists
	if home, err := os.UserHomeDir(); err == nil {
		gi.loadFile(filepath.Join(home, ".gitignore_global"))
	}

	return gi
}

// loadFile reads patterns from a gitignore file.
func (gi *GitIgnore) loadFile(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		pattern := gitPattern{}

		// Handle negation
		if strings.HasPrefix(line, "!") {
			pattern.negation = true
			line = line[1:]
		}

		// Handle directory-only patterns
		if strings.HasSuffix(line, "/") {
			pattern.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}

		pattern.pattern = line
		gi.patterns = append(gi.patterns, pattern)
	}
}

// Match checks if a path should be ignored.
func (gi *GitIgnore) Match(path string) bool {
	if gi == nil || len(gi.patterns) == 0 {
		return false
	}

	// Normalize path
	path = filepath.ToSlash(path)

	matched := false
	for _, p := range gi.patterns {
		if gi.matchPattern(path, p.pattern, p.dirOnly) {
			matched = !p.negation
		}
	}

	return matched
}

// matchPattern checks if a path matches a gitignore pattern.
func (gi *GitIgnore) matchPattern(path, pattern string, dirOnly bool) bool {
	// Handle patterns starting with /
	if strings.HasPrefix(pattern, "/") {
		pattern = pattern[1:]
		return matchGlob(path, pattern)
	}

	// Handle patterns with /
	if strings.Contains(pattern, "/") {
		return matchGlob(path, pattern) || matchGlob(path, "**/"+pattern)
	}

	// Simple pattern matches anywhere in path
	pathParts := strings.Split(path, "/")
	for _, part := range pathParts {
		if matchGlob(part, pattern) {
			return true
		}
	}

	return matchGlob(path, "**/"+pattern)
}

// matchGlob performs simple glob matching.
func matchGlob(name, pattern string) bool {
	// Handle **
	if strings.Contains(pattern, "**") {
		parts := strings.Split(pattern, "**")
		if len(parts) == 2 {
			// Simple case: prefix**suffix
			prefix := strings.TrimSuffix(parts[0], "/")
			suffix := strings.TrimPrefix(parts[1], "/")

			if prefix != "" && !strings.HasPrefix(name, prefix) {
				return false
			}
			if suffix != "" && !strings.HasSuffix(name, suffix) {
				return false
			}
			return true
		}
	}

	// Use filepath.Match for standard globs
	matched, _ := filepath.Match(pattern, name)
	if matched {
		return true
	}

	// Also try matching just the basename
	base := filepath.Base(name)
	matched, _ = filepath.Match(pattern, base)
	return matched
}

// AddPattern adds a pattern dynamically.
func (gi *GitIgnore) AddPattern(pattern string) {
	p := gitPattern{pattern: pattern}
	if strings.HasPrefix(pattern, "!") {
		p.negation = true
		p.pattern = pattern[1:]
	}
	if strings.HasSuffix(pattern, "/") {
		p.dirOnly = true
		p.pattern = strings.TrimSuffix(p.pattern, "/")
	}
	gi.patterns = append(gi.patterns, p)
}
