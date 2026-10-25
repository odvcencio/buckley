package shadowgit

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApply_FileTypesAndNames(t *testing.T) {
	for _, scenario := range []string{"file-to-link", "link-to-file", "directory-to-link", "link-to-directory", "file-to-directory", "directory-to-file", "hardlink", "executable", "unicode", "literal-pathspec", "whitespace", "subdirectory"} {
		t.Run(scenario, func(t *testing.T) {
			repo := newTestRepo(t)
			retentionGit(t, repo, "config", "core.filemode", "true")
			outside := t.TempDir()
			writeFile(t, outside, "sentinel", "outside remains unchanged")
			name := "node"
			switch scenario {
			case "unicode":
				name = "café 日本語\ttrack\nname.txt"
			case "literal-pathspec":
				name = "[x]*.txt"
			case "whitespace":
				name = " leading and trailing \n"
			case "subdirectory":
				name = "nested/node"
				writeFile(t, repo, "outside.txt", "unrelated")
			}
			node := filepath.Join(repo, name)
			switch scenario {
			case "link-to-file", "link-to-directory":
				target := outside
				if scenario == "link-to-file" {
					target = filepath.Join(outside, "sentinel")
				}
				if err := os.Symlink(target, node); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "directory-to-link", "directory-to-file":
				writeFile(t, repo, name+"/sentinel", "inside original")
			default:
				writeFile(t, repo, name, "original")
			}
			retentionGit(t, repo, "add", "--all")
			retentionGit(t, repo, "commit", "--quiet", "-m", "fixture")
			index, err := os.ReadFile(filepath.Join(repo, ".git/index"))
			if err != nil {
				t.Fatal(err)
			}
			head := retentionGit(t, repo, "rev-parse", "HEAD")
			working := repo
			if scenario == "subdirectory" {
				working = filepath.Dir(node)
			}
			store, err := New(working, scenario)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			before := restoreTree(t, store)
			if scenario == "directory-to-link" || scenario == "directory-to-file" {
				if err := os.Remove(filepath.Join(node, "sentinel")); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(node); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "file-to-link", "directory-to-link":
				target := outside
				if scenario == "file-to-link" {
					target = filepath.Join(outside, "sentinel")
				}
				if err := os.Symlink(target, node); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "link-to-directory", "file-to-directory":
				writeFile(t, repo, name+"/sentinel", "inside changed")
			case "hardlink":
				if err := os.Link(filepath.Join(outside, "sentinel"), node); err != nil {
					t.Skipf("hardlinks unavailable: %v", err)
				}
			default:
				content := "changed"
				if scenario == "executable" {
					content = "original"
				}
				writeFile(t, repo, name, content)
				if scenario == "executable" {
					if err := os.Chmod(node, 0o755); err != nil {
						t.Fatal(err)
					}
				}
			}
			after := restoreTree(t, store)
			for _, step := range []struct{ source, target string }{{after, before}, {before, after}} {
				if err := store.Apply(context.Background(), step.source, step.target); err != nil {
					t.Fatal(err)
				}
				if got := restoreTree(t, store); got != step.target {
					t.Fatalf("restored tree = %s, want %s", got, step.target)
				}
				if got := readFile(t, outside, "sentinel"); got != "outside remains unchanged" {
					t.Fatalf("restore wrote outside repository: %q", got)
				}
			}
			if got, err := os.ReadFile(filepath.Join(repo, ".git/index")); err != nil || !bytes.Equal(got, index) {
				t.Fatalf("real index changed: %v", err)
			}
			if got := retentionGit(t, repo, "rev-parse", "HEAD"); got != head {
				t.Fatal("HEAD changed")
			}
			if scenario == "subdirectory" && readFile(t, repo, "outside.txt") != "unrelated" {
				t.Fatal("file outside session directory changed")
			}
		})
	}
}

func TestApply_RefusesObstructingIgnoredFilesBeforeEdits(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "directory"}[directory], func(t *testing.T) {
			repo := newTestRepo(t)
			writeFile(t, repo, ".gitignore", "node\n")
			writeFile(t, repo, "a.txt", "original")
			writeFile(t, repo, "node", "original")
			store, err := New(repo, "ignored")
			if err != nil {
				t.Fatal(err)
			}
			// Make a target in which node is tracked, then a source in which it
			// is ignored. The real index remains unrelated to either snapshot.
			index, cleanup, err := temporaryIndex()
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			env := []string{"GIT_INDEX_FILE=" + index}
			if _, err := store.run(context.Background(), env, "add", "-f", "node"); err != nil {
				t.Fatal(err)
			}
			target, err := store.run(context.Background(), env, "write-tree")
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, repo, "a.txt", "changed")
			if directory {
				if err := os.Remove(filepath.Join(repo, "node")); err != nil {
					t.Fatal(err)
				}
				writeFile(t, repo, "node/precious", "keep ignored work")
			} else {
				writeFile(t, repo, "node", "keep ignored work")
			}
			source := restoreTree(t, store)
			if err := store.RetainTrees(context.Background(), []string{source, target}); err != nil {
				t.Fatal(err)
			}
			err = store.Apply(context.Background(), source, target)
			if err == nil || !strings.Contains(err.Error(), "untracked path") {
				t.Fatalf("expected obstruction error, got %v", err)
			}
			if readFile(t, repo, "a.txt") != "changed" {
				t.Fatal("earlier file partially restored")
			}
			name := "node"
			if directory {
				name += "/precious"
			}
			if readFile(t, repo, name) != "keep ignored work" {
				t.Fatal("ignored work changed")
			}
		})
	}
}

func TestApply_RefusesChangedSourceBeforeEdits(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo, "a.txt", "original")
	writeFile(t, repo, "z.txt", "original")
	store, err := New(repo, "dirty")
	if err != nil {
		t.Fatal(err)
	}
	before := restoreTree(t, store)
	writeFile(t, repo, "a.txt", "changed")
	writeFile(t, repo, "z.txt", "changed")
	after := restoreTree(t, store)
	writeFile(t, repo, "z.txt", "new user work")
	if err := store.Apply(context.Background(), after, before); err == nil {
		t.Fatal("accepted stale source")
	}
	if readFile(t, repo, "a.txt") != "changed" || readFile(t, repo, "z.txt") != "new user work" {
		t.Fatal("partially restored dirty worktree")
	}
}

func TestApply_PreservesUnrelatedIgnoredFiles(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo, ".gitignore", "cache/\nnested/ignored\n")
	writeFile(t, repo, "cache/large-artifact", "keep cache")
	writeFile(t, repo, "nested/ignored", "keep ignored")
	writeFile(t, repo, "nested/node", "original")
	store, err := New(repo, "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	before := restoreTree(t, store)
	writeFile(t, repo, "nested/node", "changed")
	after := restoreTree(t, store)
	if err := store.Apply(context.Background(), after, before); err != nil {
		t.Fatal(err)
	}
	if readFile(t, repo, "cache/large-artifact") != "keep cache" || readFile(t, repo, "nested/ignored") != "keep ignored" {
		t.Fatal("unrelated ignored files changed")
	}
}

func TestApply_RejectsChangedSubmoduleBeforeEdits(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo, "a.txt", "original")
	retentionGit(t, repo, "add", ".")
	retentionGit(t, repo, "commit", "-qm", "fixture")
	commit := retentionGit(t, repo, "rev-parse", "HEAD")
	store, err := New(repo, "submodule")
	if err != nil {
		t.Fatal(err)
	}
	before := restoreTree(t, store)
	writeFile(t, repo, "a.txt", "changed")
	index, cleanup, err := temporaryIndex()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := store.run(context.Background(), env, "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := store.run(context.Background(), env, "update-index", "--add", "--cacheinfo", "160000", commit, "module"); err != nil {
		t.Fatal(err)
	}
	after, err := store.run(context.Background(), env, "write-tree")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RetainTrees(context.Background(), []string{before, after}); err != nil {
		t.Fatal(err)
	}
	err = store.Apply(context.Background(), after, before)
	if err == nil || !strings.Contains(err.Error(), "submodule") {
		t.Fatalf("expected explicit submodule refusal, got %v", err)
	}
	if readFile(t, repo, "a.txt") != "changed" {
		t.Fatal("partially restored unsupported turn")
	}
}

func restoreTree(t *testing.T, store *Store) string {
	t.Helper()
	tree, err := store.WriteTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
