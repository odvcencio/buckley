package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureGitExcludeHasBuckleyLogIgnoreCreatesFile(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "info"), 0o755); err != nil {
		t.Fatalf("mkdir git dir: %v", err)
	}

	if err := ensureGitExcludeHasBuckleyLogIgnore(gitDir); err != nil {
		t.Fatalf("ensureGitExcludeHasBuckleyLogIgnore: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(gitDir, "info", "exclude"))
	if err != nil {
		t.Fatalf("read exclude: %v", err)
	}

	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d", len(lines))
	}
	if lines[0] != "# Buckley runtime" {
		t.Fatalf("unexpected first line: %q", lines[0])
	}
	if lines[1] != "**/.buckley/logs/" {
		t.Fatalf("unexpected ignore line: %q", lines[1])
	}
}

func TestEnsureGitExcludeHasBuckleyLogIgnoreAppendsOnce(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	path := filepath.Join(gitDir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir git dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("# existing\n"), 0o644); err != nil {
		t.Fatalf("write exclude: %v", err)
	}

	if err := ensureGitExcludeHasBuckleyLogIgnore(gitDir); err != nil {
		t.Fatalf("ensureGitExcludeHasBuckleyLogIgnore: %v", err)
	}
	if err := ensureGitExcludeHasBuckleyLogIgnore(gitDir); err != nil {
		t.Fatalf("ensureGitExcludeHasBuckleyLogIgnore (second): %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read exclude: %v", err)
	}
	if strings.Count(string(content), "**/.buckley/logs/") != 1 {
		t.Fatalf("expected ignore line once, got:\n%s", content)
	}
}

func TestEnsureGitExcludeHasBuckleyLogIgnoreSkipsWhenAlreadyIgnored(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	path := filepath.Join(gitDir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir git dir: %v", err)
	}
	initial := "# existing\n.buckley/\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("write exclude: %v", err)
	}

	if err := ensureGitExcludeHasBuckleyLogIgnore(gitDir); err != nil {
		t.Fatalf("ensureGitExcludeHasBuckleyLogIgnore: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read exclude: %v", err)
	}
	if string(content) != initial {
		t.Fatalf("expected file unchanged, got:\n%s", content)
	}
}
