package shadowgit

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func allocatedPrivateBytes(t *testing.T, store *Store) int64 {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native allocated-storage measurement requires du")
	}
	output, err := exec.Command("du", "-sk", store.StorageDir()).Output()
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseInt(strings.Fields(string(output))[0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n * 1024
}

func budgetPayload(turn, size int) []byte {
	data := make([]byte, size)
	for offset := 0; offset < len(data); offset += sha256.Size {
		digest := sha256.Sum256([]byte(strconv.Itoa(turn) + "/" + strconv.Itoa(offset)))
		copy(data[offset:], digest[:])
	}
	return data
}

func TestRetainTreesWithinLimit_RejectsWithoutLosingPublishedHistory(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo, "user.txt", "committed")
	retentionGit(t, repo, "add", ".")
	retentionGit(t, repo, "commit", "-qm", "user history")
	original := userGitFiles(t, repo)
	store, err := New(repo, "budget")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	before := restoreTree(t, store)
	if err := store.RetainTrees(ctx, []string{before}); err != nil {
		t.Fatal(err)
	}
	ref, _ := store.CurrentRef(ctx)
	data := budgetPayload(1, 2<<20)
	if err := os.WriteFile(filepath.Join(repo, "artifact"), data, 0600); err != nil {
		t.Fatal(err)
	}
	after := restoreTree(t, store)
	committed, err := store.RetainTreesWithinLimit(ctx, []string{before, after}, 1<<20)
	if committed || !errors.Is(err, ErrStorageLimit) {
		t.Fatalf("oversized root: committed=%v err=%v", committed, err)
	}
	if current, _ := store.CurrentRef(ctx); current != ref {
		t.Fatal("budget refusal changed the published history")
	}
	retentionPrivateGit(t, store, "cat-file", "-e", before)
	if err := store.RetainTrees(ctx, []string{before}); err != nil {
		t.Fatal(err)
	}
	if used := allocatedPrivateBytes(t, store); used > 1<<20 {
		t.Fatalf("old-only retention kept oversized staging: %d bytes", used)
	}
	got, err := os.ReadFile(filepath.Join(repo, "artifact"))
	if err != nil || sha256.Sum256(got) != sha256.Sum256(data) {
		t.Fatal("budget refusal changed completed file work")
	}
	if !reflect.DeepEqual(original, userGitFiles(t, repo)) {
		t.Fatal("budget accounting changed user Git data")
	}
}

func TestRetainTreesWithinLimit_BoundsPhysicalStorageAndSupportsRestore(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			t.Setenv("GIT_DEFAULT_HASH", format)
			repo := newTestRepo(t)
			store, err := New(repo, "bounded")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			const limit = 2 << 20
			ctx := context.Background()
			var trees []string
			for turn := range 30 {
				if err := os.WriteFile(filepath.Join(repo, "artifact"), budgetPayload(turn, 256<<10), 0600); err != nil {
					t.Fatal(err)
				}
				trees = append(trees, restoreTree(t, store))
				for {
					committed, err := store.RetainTreesWithinLimit(ctx, trees, limit)
					if errors.Is(err, ErrStorageLimit) && len(trees) > 1 {
						trees = trees[1:]
						continue
					}
					if err != nil || !committed {
						t.Fatalf("turn %d: committed=%v err=%v", turn, committed, err)
					}
					break
				}
				if used := allocatedPrivateBytes(t, store); used > limit {
					t.Fatalf("turn %d: %d allocated bytes exceeds %d", turn, used, limit)
				}
			}
			if len(trees) < 2 || len(trees) >= 20 {
				t.Fatalf("unexpected retained depth %d", len(trees))
			}
			if err := store.Apply(ctx, trees[len(trees)-1], trees[len(trees)-2]); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(repo, "artifact"))
			if err != nil || sha256.Sum256(got) != sha256.Sum256(budgetPayload(28, 256<<10)) {
				t.Fatal("retained snapshot no longer restores after eviction")
			}
		})
	}
}

func TestRetainTreesWithinLimit_RefFailurePreservesOldRoot(t *testing.T) {
	repo := newTestRepo(t)
	store, err := New(repo, "publication")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	writeFile(t, repo, "file", "before")
	before := restoreTree(t, store)
	if err := store.RetainTrees(ctx, []string{before}); err != nil {
		t.Fatal(err)
	}
	ref, _ := store.CurrentRef(ctx)
	writeFile(t, repo, "file", "after")
	after := restoreTree(t, store)
	lock := filepath.Join(store.StorageDir(), store.RefName()+".lock")
	if err := os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	committed, err := store.RetainTreesWithinLimit(ctx, []string{after}, 1<<20)
	if committed || err == nil {
		t.Fatalf("blocked ref update: committed=%v err=%v", committed, err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if current, _ := store.CurrentRef(ctx); current != ref {
		t.Fatal("failed publication changed old root")
	}
	// Failed publication discarded staging, so recapture the unchanged worktree
	// before asking Apply to compare it with the still-published old snapshot.
	after = restoreTree(t, store)
	if err := store.Apply(ctx, after, before); err != nil {
		t.Fatalf("failed publication destroyed old undo objects: %v", err)
	}
}

func TestRetainTreesWithinLimit_RepeatedRefFailuresDoNotAccumulateStaging(t *testing.T) {
	repo := newTestRepo(t)
	store, err := New(repo, "publication-loop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	writeFile(t, repo, "file", "published snapshot")
	before := restoreTree(t, store)
	if err := store.RetainTrees(ctx, []string{before}); err != nil {
		t.Fatal(err)
	}
	ref, _ := store.CurrentRef(ctx)
	lock := filepath.Join(store.StorageDir(), store.RefName()+".lock")
	if err := os.WriteFile(lock, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for turn := range 8 {
		if err := os.WriteFile(filepath.Join(repo, "artifact"), budgetPayload(turn, 256<<10), 0600); err != nil {
			t.Fatal(err)
		}
		after := restoreTree(t, store)
		committed, err := store.RetainTreesWithinLimit(ctx, []string{before, after}, 1<<20)
		if committed || err == nil || errors.Is(err, ErrStorageLimit) {
			t.Fatalf("turn %d: committed=%v err=%v", turn, committed, err)
		}
		if current, _ := store.CurrentRef(ctx); current != ref {
			t.Fatal("ref failure moved published root")
		}
		if len(store.pending) != 0 || privateObjectBytes(t, store) > 1024 {
			t.Fatalf("turn %d retained failed staging", turn)
		}
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	retentionPrivateGit(t, store, "cat-file", "-e", before)
}

func TestRetainTreesWithinLimit_CleanupFailureBlocksFurtherCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fault fixture uses a POSIX shell")
	}
	repo := newTestRepo(t)
	store, err := New(repo, "cleanup")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	writeFile(t, repo, "file", "before")
	before := restoreTree(t, store)
	if err := store.RetainTrees(ctx, []string{before}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "file", "after")
	after := restoreTree(t, store)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	block := filepath.Join(shim, "block-prune")
	if err := os.WriteFile(block, nil, 0600); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nfor arg do\n if [ \"$arg\" = prune ] && [ -e \"$BUCKLEY_TEST_PRUNE_BLOCK\" ]; then echo 'fixture cleanup failure' >&2; exit 55; fi\ndone\nexec '" + strings.ReplaceAll(realGit, "'", "'\\''") + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BUCKLEY_TEST_PRUNE_BLOCK", block)
	committed, err := store.RetainTreesWithinLimit(ctx, []string{after}, 1<<20)
	if !committed || err == nil {
		t.Fatalf("cleanup failure: committed=%v err=%v", committed, err)
	}
	root, _ := store.CurrentRef(ctx)
	retentionPrivateGit(t, store, "cat-file", "-e", root)
	used := privateObjectBytes(t, store)
	writeFile(t, repo, "file", "must not be captured while cleanup is blocked")
	if _, err := store.WriteTree(ctx); err == nil || privateObjectBytes(t, store) != used {
		t.Fatal("cleanup failure allowed more snapshot writes")
	}
	if err := os.Remove(block); err != nil {
		t.Fatal(err)
	}
	if _, err := store.WriteTree(ctx); err != nil {
		t.Fatalf("capture did not recover after cleanup succeeded: %v", err)
	}
}
