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

func TestWriteTree_CaptureLimitPreservesPublishedHistoryAndCompletedWork(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo, "note", "original")
	original := userGitFiles(t, repo)
	store, err := New(repo, "capture-input", WithCaptureLimit(1<<20))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	before := restoreTree(t, store)
	writeFile(t, repo, "note", "changed")
	after := restoreTree(t, store)
	if err := store.RetainTrees(ctx, []string{before, after}); err != nil {
		t.Fatal(err)
	}
	root, _ := store.CurrentRef(ctx)
	used := privateObjectBytes(t, store)
	data := budgetPayload(1, 2<<20)
	if err := os.WriteFile(filepath.Join(repo, "artifact"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for attempt := range 4 {
		if _, err := store.WriteTree(ctx); !errors.Is(err, ErrCaptureLimit) {
			t.Fatalf("attempt %d: expected capture refusal, got %v", attempt, err)
		}
		if privateObjectBytes(t, store) != used {
			t.Fatal("refused capture wrote private objects")
		}
	}
	if _, err := store.Clean(ctx, after); !errors.Is(err, ErrCaptureLimit) {
		t.Fatalf("worktree check bypassed capture limit: %v", err)
	}
	if err := store.Apply(ctx, after, before); !errors.Is(err, ErrCaptureLimit) {
		t.Fatalf("restore staging bypassed capture limit: %v", err)
	}
	current, _ := store.CurrentRef(ctx)
	got, err := os.ReadFile(filepath.Join(repo, "artifact"))
	if err != nil || sha256.Sum256(got) != sha256.Sum256(data) || current != root || readFile(t, repo, "note") != "changed" {
		t.Fatal("capture refusal changed completed work or published history")
	}
	if !reflect.DeepEqual(original, userGitFiles(t, repo)) {
		t.Fatal("preflight changed user Git data")
	}
	if err := os.Remove(filepath.Join(repo, "artifact")); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(ctx, after, before); err != nil {
		t.Fatalf("undo did not recover after oversized input was removed: %v", err)
	}
	if got := readFile(t, repo, "note"); got != "original" {
		t.Fatal("published undo data was lost")
	}
}

func TestWriteTree_CaptureInputBoundariesAndOptOut(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  int
		limit int64
		fail  bool
	}{
		{"at limit", (1 << 20) - len("file"), 1 << 20, false},
		{"past limit", (1 << 20) - len("file") + 1, 1 << 20, true},
		{"disabled", 2 << 20, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := newTestRepo(t)
			if err := os.WriteFile(filepath.Join(repo, "file"), make([]byte, tc.size), 0600); err != nil {
				t.Fatal(err)
			}
			store, err := New(repo, tc.name, WithCaptureLimit(tc.limit))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			_, err = store.WriteTree(context.Background())
			if errors.Is(err, ErrCaptureLimit) != tc.fail || err != nil && !tc.fail {
				t.Fatalf("capture error = %v", err)
			}
		})
	}
	if _, err := New(t.TempDir(), "negative", WithCaptureLimit(-1)); err == nil {
		t.Fatal("negative capture limit accepted")
	}
}

func TestWriteTree_CaptureLimitHonorsIgnoredPathsAndSubdirectoryScope(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, repo, ".gitignore", "*.ignored\n")
	writeFile(t, repo, "sub/café_日本語.txt", "kept")
	for _, name := range []string{"outside", "sub/cache.ignored"} {
		if err := os.WriteFile(filepath.Join(repo, name), make([]byte, 2<<20), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := New(filepath.Join(repo, "sub"), "scope", WithCaptureLimit(1<<20))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tree := restoreTree(t, store)
	if got := retentionPrivateGit(t, store, "show", tree+":sub/café_日本語.txt"); got != "kept" {
		t.Fatal("valid scoped input was omitted")
	}
}

func TestWriteTree_CaptureInputLimitAllowsCompressedRetention(t *testing.T) {
	repo := newTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "compressible"), make([]byte, 2<<20), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := New(repo, "compressed", WithCaptureLimit(4<<20))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tree := restoreTree(t, store)
	committed, err := store.RetainTreesWithinLimit(context.Background(), []string{tree}, 1<<20)
	if err != nil || !committed {
		t.Fatalf("compressed snapshot that fits retention was rejected: committed=%v err=%v", committed, err)
	}
}
