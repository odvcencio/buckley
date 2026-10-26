package shadowgit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPrivateStorage_PackedCollectionReusesInstalledPackAndRemovesSidecars(t *testing.T) {
	repo := newTestRepo(t)
	retentionGit(t, repo, "config", "core.bigFileThreshold", "1")
	writeFile(t, repo, "file", "completed work")
	store, err := New(repo, "packed-reuse")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	tree := restoreTree(t, store)
	if err := store.RetainTrees(ctx, []string{tree}); err != nil {
		t.Fatal(err)
	}
	packs, err := filepath.Glob(filepath.Join(store.gitDir, "objects", "pack", "*.pack"))
	if err != nil || len(packs) != 1 {
		t.Fatalf("expected one retained pack: %v %v", packs, err)
	}
	original, err := os.Stat(packs[0])
	if err != nil {
		t.Fatal(err)
	}
	keep := packs[0][:len(packs[0])-len(".pack")] + ".keep"
	if err := os.WriteFile(keep, []byte("obsolete private sidecar"), 0600); err != nil {
		t.Fatal(err)
	}
	if clean, err := store.Clean(ctx, tree); err != nil || !clean {
		t.Fatalf("repeated collection: clean=%v err=%v", clean, err)
	}
	after, err := os.Stat(packs[0])
	if err != nil || !os.SameFile(original, after) {
		t.Fatalf("collection replaced an identical installed pack: %v", err)
	}
	if _, err := os.Stat(keep); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("obsolete private sidecar was retained: %v", err)
	}
}

func TestPrivateStorage_PackedCollectionPreservesPendingAndReleasesEmptyHistory(t *testing.T) {
	repo := newTestRepo(t)
	retentionGit(t, repo, "config", "core.bigFileThreshold", "1")
	writeFile(t, repo, "user", "user history")
	retentionGit(t, repo, "add", ".")
	retentionGit(t, repo, "commit", "-qm", "user baseline")
	original := userGitFiles(t, repo)
	store, err := New(repo, "packed-pending")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	before := restoreTree(t, store)
	if err := store.RetainTrees(ctx, []string{before}); err != nil {
		t.Fatal(err)
	}
	var pending []string
	for turn := range 3 {
		if err := os.WriteFile(filepath.Join(repo, "artifact"), budgetPayload(turn, 256<<10), 0600); err != nil {
			t.Fatal(err)
		}
		pending = append(pending, restoreTree(t, store))
	}
	commit, err := store.CommitTree(ctx, pending[0], "", "pending commit")
	if err != nil {
		t.Fatal(err)
	}
	if clean, err := store.Clean(ctx, pending[2]); err != nil || !clean {
		t.Fatalf("collection during clean: clean=%v err=%v", clean, err)
	}
	for _, object := range append(append([]string{before, commit}, pending...), store.pending...) {
		retentionPrivateGit(t, store, "cat-file", "-e", object)
	}
	if err := store.Apply(ctx, pending[2], pending[0]); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(repo, "artifact")); err != nil || !reflect.DeepEqual(got, budgetPayload(0, 256<<10)) {
		t.Fatal("collection lost a pending restore target")
	}
	if err := store.RetainTrees(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if bytes := privateObjectBytes(t, store); bytes != 0 {
		t.Fatalf("empty history retained %d packed object bytes", bytes)
	}
	if current := userGitFiles(t, repo); !reflect.DeepEqual(original, current) {
		logUserGitChanges(t, original, current)
		t.Fatal("packed collection changed user Git data")
	}
}

func TestPrivateStorage_InterruptedPackPreparationIsReaped(t *testing.T) {
	repo := newTestRepo(t)
	store, err := New(repo, "pack-staging-crash")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.lease == nil || store.lease.file == nil {
		t.Skip("filesystem has no ownership locks")
	}
	ctx := context.Background()
	writeFile(t, repo, "file", "completed work")
	tree := restoreTree(t, store)
	pack, err := store.preparePack(ctx, []string{tree}, false)
	if err != nil {
		t.Fatal(err)
	}
	if bytes, err := pack.bytes(); err != nil || bytes == 0 {
		t.Fatalf("expected a staged pack: bytes=%d err=%v", bytes, err)
	}
	dir := store.gitDir
	store.lease.close()
	next, err := New(repo, "after-pack-staging-crash")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abandoned pack staging survived recovery: %v", err)
	}
	if got := readFile(t, repo, "file"); got != "completed work" {
		t.Fatal("recovery changed completed work")
	}
}
