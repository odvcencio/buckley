package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var ensureBuckleyIgnoreOnce sync.Once

func ensureBuckleyRuntimeIgnored() {
	ensureBuckleyIgnoreOnce.Do(func() {
		gitDir, err := gitOutput("rev-parse", "--absolute-git-dir")
		if err != nil {
			return
		}
		_ = ensureGitExcludeHasBuckleyLogIgnore(strings.TrimSpace(gitDir))
	})
}

func ensureGitExcludeHasBuckleyLogIgnore(gitDir string) error {
	gitDir = strings.TrimSpace(gitDir)
	if gitDir == "" {
		return nil
	}

	path := filepath.Join(gitDir, "info", "exclude")
	const ignoreLine = "**/.buckley/logs/"

	existing, err := os.ReadFile(path)
	if err == nil {
		if gitignoreIgnoresBuckleyLogs(string(existing)) {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return nil
	}

	var b strings.Builder
	if len(existing) > 0 {
		b.Write(existing)
		if existing[len(existing)-1] != '\n' {
			b.WriteByte('\n')
		}
	}

	if b.Len() > 0 {
		b.WriteByte('\n')
	}
	b.WriteString("# Buckley runtime\n")
	b.WriteString(ignoreLine)
	b.WriteByte('\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil
	}

	tmpPath := path + ".buckley.tmp"
	if err := os.WriteFile(tmpPath, []byte(b.String()), 0o644); err != nil {
		return nil
	}
	if err := os.Rename(tmpPath, path); err != nil {
		// Windows cannot replace an existing file with os.Rename. Fall back to a
		// non-atomic write and clean up the temporary file.
		_ = os.WriteFile(path, []byte(b.String()), 0o644)
		_ = os.Remove(tmpPath)
	}
	return nil
}

func gitignoreHasLine(content string, needle string) bool {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return false
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line == needle {
			return true
		}
	}
	return false
}

func gitignoreIgnoresBuckleyLogs(content string) bool {
	for _, candidate := range []string{
		"**/.buckley/logs/",
		".buckley/logs/",
		".buckley/",
		"**/.buckley/",
	} {
		if gitignoreHasLine(content, candidate) {
			return true
		}
	}
	return false
}
