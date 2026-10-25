package shadowgit

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestApply_RequiresExactSourceEvenWithoutFileChanges(t *testing.T) {
	for _, drift := range []string{"modified unrelated file", "new untracked file", "deleted unrelated file", "changed ignore rules", "unchanged snapshots"} {
		t.Run(drift, func(t *testing.T) {
			repo := newTestRepo(t)
			writeFile(t, repo, "changed.txt", "original")
			writeFile(t, repo, "other.txt", "unrelated")
			writeFile(t, repo, ".gitignore", "cache/\n")
			store, err := New(repo, "drift")
			if err != nil {
				t.Fatal(err)
			}
			before := restoreTree(t, store)
			writeFile(t, repo, "changed.txt", "turn output")
			after := restoreTree(t, store)
			switch drift {
			case "modified unrelated file":
				writeFile(t, repo, "other.txt", "user work")
			case "new untracked file", "unchanged snapshots":
				writeFile(t, repo, "manual.txt", "user work")
			case "deleted unrelated file":
				if err := os.Remove(filepath.Join(repo, "other.txt")); err != nil {
					t.Fatal(err)
				}
			case "changed ignore rules":
				writeFile(t, repo, ".gitignore", "cache/\nother.txt\n")
			}
			if drift == "unchanged snapshots" {
				before = after
			}
			current := restoreTree(t, store)
			if err := store.Apply(context.Background(), after, before); !errors.Is(err, ErrWorktreeChanged) {
				t.Fatalf("Apply error = %v, want ErrWorktreeChanged", err)
			}
			if got := restoreTree(t, store); got != current {
				t.Fatal("refused operation changed the worktree")
			}
			if readFile(t, repo, "changed.txt") != "turn output" {
				t.Fatal("completed turn output changed")
			}
		})
	}
}

func TestApply_EqualCleanSnapshots(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo, "file", "same")
	store, err := New(repo, "same")
	if err != nil {
		t.Fatal(err)
	}
	tree := restoreTree(t, store)
	if err := store.Apply(context.Background(), tree, tree); err != nil {
		t.Fatal(err)
	}
}

func TestWriteTree_PrivateIndexesDoNotLeaveSplitIndexSidecars(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo, "file", "staged user work")
	retentionGit(t, repo, "config", "core.splitIndex", "true")
	retentionGit(t, repo, "add", ".")
	indexPath := filepath.Join(repo, ".git/index")
	index, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	sidecars := func() map[string][32]byte {
		out := map[string][32]byte{}
		paths, err := filepath.Glob(filepath.Join(repo, ".git/sharedindex.*"))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			out[filepath.Base(p)] = sha256.Sum256(data)
		}
		return out
	}
	userSidecars := sidecars()
	if len(userSidecars) == 0 {
		t.Fatal("fixture did not create a real user split index")
	}
	scratch := t.TempDir()
	t.Setenv("TMPDIR", scratch)
	store, err := New(repo, "split")
	if err != nil {
		t.Fatal(err)
	}
	before := restoreTree(t, store)
	for i := range 5 {
		writeFile(t, repo, "file", string(rune('a'+i)))
		restoreTree(t, store)
	}
	after := restoreTree(t, store)
	if err := store.Apply(context.Background(), after, before); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if got := sidecars(); !reflect.DeepEqual(got, userSidecars) {
		t.Fatalf("private snapshots changed user split-index sidecars: before=%v after=%v", userSidecars, got)
	}
	if data, err := os.ReadFile(indexPath); err != nil || sha256.Sum256(data) != sha256.Sum256(index) {
		t.Fatalf("real index changed: %v", err)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("private index files leaked: %v", entries)
	}
}
