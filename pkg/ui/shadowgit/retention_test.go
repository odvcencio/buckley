package shadowgit

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func retentionGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func retentionPrivateGit(t *testing.T, store *Store, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"--git-dir=" + store.StorageDir()}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("private git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestRetainTrees_ReleasesDiscardedTreesAndIsolatesHandles(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, "user.txt", "committed")
	retentionGit(t, dir, "add", ".")
	retentionGit(t, dir, "commit", "-qm", "user commit")
	head := retentionGit(t, dir, "rev-parse", "HEAD")
	retentionGit(t, dir, "update-ref", "refs/buckley/undo/legacy", head)
	writeFile(t, dir, "user.txt", "staged")
	retentionGit(t, dir, "add", "user.txt")
	writeFile(t, dir, "user.txt", "unstaged")
	index, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(dir, "same-session")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	b, err := New(dir, "same-session")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if a.RefName() == b.RefName() {
		t.Fatal("concurrent handles share a retention ref")
	}
	ctx := context.Background()
	var trees []string
	for _, content := range []string{"discarded", "other handle", "latest"} {
		writeFile(t, dir, "snapshot.txt", content)
		tree, err := a.WriteTree(ctx)
		if err != nil {
			t.Fatal(err)
		}
		trees = append(trees, tree)
		if content == "other handle" {
			other, err := b.WriteTree(ctx)
			if err != nil || other != tree {
				t.Fatalf("independent snapshot mismatch: %s %s %v", tree, other, err)
			}
		}
	}
	if err := a.RetainTrees(ctx, trees[:1]); err != nil {
		t.Fatal(err)
	}
	if err := b.RetainTrees(ctx, trees[1:2]); err != nil {
		t.Fatal(err)
	}
	// Unretained staging was collected; recapture the current latest worktree.
	latest, err := a.WriteTree(ctx)
	if err != nil || latest != trees[2] {
		t.Fatalf("recaptured latest snapshot: %s %v", latest, err)
	}
	if err := a.RetainTrees(ctx, []string{trees[2], trees[2]}); err != nil {
		t.Fatal(err)
	}
	if got := retentionPrivateGit(t, a, "rev-list", "--count", a.RefName()); got != "1" {
		t.Fatalf("retention root has ancestors: %s", got)
	}
	if got := retentionPrivateGit(t, a, "ls-tree", a.RefName()+"^{tree}"); strings.Count(got, "\n") != 0 {
		t.Fatal("duplicate roots were retained")
	}
	// User Git collection cannot remove either independent handle's snapshots.
	if clean, err := a.Clean(ctx, trees[2]); err != nil || !clean {
		t.Fatalf("clean after retention: %v %v", clean, err)
	}
	retentionGit(t, dir, "gc", "--prune=now")
	if exec.Command("git", "--git-dir="+a.StorageDir(), "cat-file", "-e", trees[0]).Run() == nil {
		t.Fatal("discarded snapshot remains pinned")
	}
	retentionPrivateGit(t, b, "cat-file", "-e", trees[1])
	retentionPrivateGit(t, a, "cat-file", "-e", trees[2])
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	retentionGit(t, dir, "gc", "--prune=now")
	retentionPrivateGit(t, b, "cat-file", "-e", trees[1])
	if got := retentionGit(t, dir, "rev-parse", "refs/buckley/undo/legacy"); got != head {
		t.Fatal("legacy ref changed")
	}
	after, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil || sha256.Sum256(index) != sha256.Sum256(after) || retentionGit(t, dir, "rev-parse", "HEAD") != head {
		t.Fatal("user index or HEAD changed")
	}
	if retentionGit(t, dir, "show", ":user.txt") != "staged" {
		t.Fatal("staged user data was lost")
	}
	data, err := os.ReadFile(filepath.Join(dir, "user.txt"))
	if err != nil || string(data) != "unstaged" {
		t.Fatal("unstaged user data was lost")
	}
}

func TestRetainTrees_FailureKeepsPreviousRootAndReleaseCanReopen(t *testing.T) {
	dir := newTestRepo(t)
	store, err := New(dir, "failure")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	writeFile(t, dir, "notes", "kept")
	ctx := context.Background()
	tree, err := store.WriteTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RetainTrees(ctx, []string{tree}); err != nil {
		t.Fatal(err)
	}
	before, _ := store.CurrentRef(ctx)
	if err := store.RetainTrees(ctx, []string{strings.Repeat("0", 40)}); err == nil {
		t.Fatal("missing tree accepted")
	}
	if after, _ := store.CurrentRef(ctx); after != before {
		t.Fatal("failed update changed existing retention")
	}
	if err := store.RetainTrees(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if ref, _ := store.CurrentRef(ctx); ref != "" {
		t.Fatal("empty retention did not release its ref")
	}
	later, err := store.WriteTree(ctx)
	if err != nil || later != tree {
		t.Fatalf("recapture after release: %s %v", later, err)
	}
	if err := store.RetainTrees(ctx, []string{later}); err != nil {
		t.Fatal("released live handle could not retain a later turn", err)
	}
}

func TestClose_LateRetentionCannotRecreateRef(t *testing.T) {
	dir := newTestRepo(t)
	store, err := New(dir, "close")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "notes", "completed")
	ctx := context.Background()
	tree, err := store.WriteTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			if err := store.RetainTrees(ctx, []string{tree}); err != nil && !errors.Is(err, os.ErrClosed) {
				t.Error(err)
			}
		})
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	workers.Wait()
	if err := store.Close(); err != nil {
		t.Fatal("repeated close failed", err)
	}
	if err := store.RetainTrees(ctx, []string{tree}); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("retention after close: %v", err)
	}
	if ref, _ := store.CurrentRef(ctx); ref != "" {
		t.Fatal("late retention recreated the closed ref")
	}
}
