package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScopedCommitSnapshot_StagedContentOnly(t *testing.T) {
	for _, mode := range []string{"normal", "split-index", "alternate-index", "linked-worktree", "unborn"} {
		t.Run(mode, func(t *testing.T) {
			repo := initTempGitRepo(t)
			if mode == "linked-worktree" {
				linked := filepath.Join(t.TempDir(), "linked")
				runGit(t, repo, "worktree", "add", "--detach", linked, "HEAD")
				repo = linked
			}
			if mode == "unborn" {
				runGit(t, repo, "checkout", "--orphan", "first")
				runGit(t, repo, "read-tree", "--empty")
			}
			t.Chdir(repo)
			if err := os.MkdirAll("a", 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("a/file.go", []byte("package staged\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("outside.go", []byte("package outside\n"), 0644); err != nil {
				t.Fatal(err)
			}
			runGit(t, repo, "add", "a/file.go", "outside.go")
			if mode == "split-index" {
				runGit(t, repo, "update-index", "--split-index")
			}
			originalIndex := gitOutputInDir(t, repo, "rev-parse", "--git-path", "index")
			if mode == "alternate-index" {
				data, err := os.ReadFile(originalIndex)
				if err != nil {
					t.Fatal(err)
				}
				alternate := filepath.Join(t.TempDir(), "alternate index")
				if err := os.WriteFile(alternate, data, 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("GIT_INDEX_FILE", alternate)
			}
			indexPath := gitOutputInDir(t, repo, "rev-parse", "--git-path", "index")
			metadata, err := collectStagedChangeMetadata([]string{"a"})
			if err != nil {
				t.Fatal(err)
			}
			indexBefore, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			outsideBefore := gitOutputInDir(t, repo, "ls-files", "--stage", "--", "outside.go")
			work := []byte("package unstaged\n// never approved\n")
			if err := os.WriteFile("a/file.go", work, 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile("a/untracked.go", []byte("package private\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if err := createCommitWithMetadata("fix: scoped staged snapshot", true, false, []string{"a"}, metadata, true); err != nil {
				t.Fatal(err)
			}
			if got := gitOutputInDir(t, repo, "show", "HEAD:a/file.go"); got != "package staged" {
				t.Fatalf("committed working content: %q", got)
			}
			got, err := os.ReadFile("a/file.go")
			if err != nil || !bytes.Equal(got, work) {
				t.Fatalf("working copy changed: %q err=%v", got, err)
			}
			indexAfter, err := os.ReadFile(indexPath)
			if err != nil || !bytes.Equal(indexBefore, indexAfter) {
				t.Fatalf("original index changed: err=%v", err)
			}
			if got := gitOutputInDir(t, repo, "ls-files", "--stage", "--", "outside.go"); got != outsideBefore {
				t.Fatalf("outside entry changed: %q", got)
			}
			if got := gitOutputInDir(t, repo, "diff", "--cached", "--name-only"); got != "outside.go" {
				t.Fatalf("remaining staged = %q", got)
			}
			tree := gitOutputInDir(t, repo, "ls-tree", "-r", "--name-only", "HEAD")
			if strings.Contains(tree, "untracked.go") || strings.Contains(tree, "outside.go") {
				t.Fatalf("outside content committed: %s", tree)
			}
			if _, err := os.Stat(indexPath + ".lock"); !os.IsNotExist(err) {
				t.Fatalf("index lock left behind: %v", err)
			}
		})
	}
}

func TestScopedCommitSnapshot_FailurePreservesStaging(t *testing.T) {
	for _, failure := range []string{"hook", "existing-lock"} {
		t.Run(failure, func(t *testing.T) {
			repo := setupTwoAreaRepo(t)
			t.Chdir(repo)
			metadata, err := collectStagedChangeMetadata([]string{"a"})
			if err != nil {
				t.Fatal(err)
			}
			indexPath := gitOutputInDir(t, repo, "rev-parse", "--git-path", "index")
			before, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			head := gitOutputInDir(t, repo, "rev-parse", "HEAD")
			if failure == "existing-lock" {
				if err := os.WriteFile(indexPath+".lock", []byte("user-owned-lock"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				hooks := t.TempDir()
				if err := os.WriteFile(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
					t.Fatal(err)
				}
				runGit(t, repo, "config", "core.hooksPath", hooks)
			}
			if err := createCommitWithMetadata("fix: rejected scoped commit", true, false, []string{"a"}, metadata, true); err == nil {
				t.Fatal("expected failure")
			}
			after, err := os.ReadFile(indexPath)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("index changed on failure: %v", err)
			}
			if got := gitOutputInDir(t, repo, "rev-parse", "HEAD"); got != head {
				t.Fatal("HEAD changed on failure")
			}
			if failure == "existing-lock" {
				got, err := os.ReadFile(indexPath + ".lock")
				if err != nil || string(got) != "user-owned-lock" {
					t.Fatalf("removed another operation's lock: %q %v", got, err)
				}
			} else if _, err := os.Stat(indexPath + ".lock"); !os.IsNotExist(err) {
				t.Fatalf("index lock left behind: %v", err)
			}
		})
	}
}

func TestScopedCommitSnapshot_ExactTreeAndLiteralPaths(t *testing.T) {
	repo := initTempGitRepo(t)
	t.Chdir(repo)
	for _, dir := range []string{"a", ":(glob)a"} {
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile("a/deleted", []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("outside-deleted", []byte("keep in HEAD"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "base")
	if err := os.Remove("a/deleted"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove("outside-deleted"); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"a/binary":          {0, 1, 2, 13, 10, 255},
		"a/odd\tname\nfile": []byte("exact\r\npage\n"),
		"a/foreign":         []byte("selected staged content"),
		":(glob)a/foreign":  []byte("outside literal magic path"),
	} {
		if err := os.WriteFile(name, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile("a/executable", []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("binary", "a/link"); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "--literal-pathspecs", "add", "--all")
	if err := os.WriteFile("outside-intent", []byte("not staged"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-N", "outside-intent")
	expectedTree := gitOutputInDir(t, repo, "write-tree", "--prefix=a/")
	metadata, err := collectStagedChangeMetadata([]string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	indexPath := gitOutputInDir(t, repo, "rev-parse", "--git-path", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("a/binary", []byte("unstaged replacement"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := createCommitWithMetadata("fix: exact staged tree", true, false, []string{"a"}, metadata, true); err != nil {
		t.Fatal(err)
	}
	if got := gitOutputInDir(t, repo, "rev-parse", "HEAD:a"); got != expectedTree {
		t.Fatalf("selected subtree changed: got %s want %s", got, expectedTree)
	}
	if got := gitOutputInDir(t, repo, "show", "HEAD:outside-deleted"); got != "keep in HEAD" {
		t.Fatalf("outside deletion committed: %q", got)
	}
	tree := gitOutputInDir(t, repo, "ls-tree", "-r", "--name-only", "HEAD")
	if strings.Contains(tree, "outside-intent") || strings.Contains(tree, "(glob)") {
		t.Fatalf("outside additions leaked: %s", tree)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("original index changed: %v", err)
	}
}

func TestScopedCommitSnapshot_HoldsOriginalIndexLock(t *testing.T) {
	repo := setupTwoAreaRepo(t)
	t.Chdir(repo)
	indexPath := gitOutputInDir(t, repo, "rev-parse", "--git-path", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	env, cleanup, err := prepareScopedCommitIndex(context.Background(), []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	if err := os.WriteFile("a/file.go", []byte("package concurrent_staging\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "a/file.go")
	if output, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(output), "index.lock") {
		t.Fatalf("concurrent staging not rejected: %q %v", output, err)
	}
	after, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("original index changed: %v", err)
	}
	cmd = exec.Command("git", "show", ":a/file.go")
	cmd.Env = env
	output, err := cmd.Output()
	if err != nil || string(output) != "package a\n" {
		t.Fatalf("snapshot changed: %q %v", output, err)
	}
}
