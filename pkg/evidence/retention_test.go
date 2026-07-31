package evidence

import (
	"context"
	"testing"
	"time"
)

func TestSweep_RemovesExpiredUnpinned(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	old, err := store.Put(ctx, Object{
		Kind: KindToolResult, MediaType: "text/plain", InlineBody: []byte("stale result"),
		CreatedAt: now.Add(-40 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Put() old error = %v", err)
	}
	fresh, err := store.Put(ctx, Object{
		Kind: KindToolResult, MediaType: "text/plain", InlineBody: []byte("recent result"),
		CreatedAt: now.Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Put() fresh error = %v", err)
	}

	removed, err := store.Sweep(ctx, DefaultRetentionPolicy(), now)
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(removed) != 1 || removed[0] != old.ID {
		t.Fatalf("Sweep() removed = %v, want [%s]", removed, old.ID)
	}

	if _, err := store.Get(ctx, old.ID); err == nil {
		t.Fatalf("expected stale object to be deleted")
	}
	if _, err := store.Get(ctx, fresh.ID); err != nil {
		t.Fatalf("expected fresh object to survive Sweep, got error: %v", err)
	}
}

func TestSweep_KeepsPinnedEvenIfExpired(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	old, err := store.Put(ctx, Object{
		Kind: KindCommitProposal, MediaType: "text/markdown", InlineBody: []byte("proposal"),
		CreatedAt: now.Add(-200 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := store.Pin(ctx, old.ID, "user_pin"); err != nil {
		t.Fatalf("Pin() error = %v", err)
	}

	removed, err := store.Sweep(ctx, DefaultRetentionPolicy(), now)
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Sweep() removed pinned object: %v", removed)
	}
	if _, err := store.Get(ctx, old.ID); err != nil {
		t.Fatalf("expected pinned object to survive Sweep, got error: %v", err)
	}
}

func TestSweep_LeavesUnclassifiedKindsAlone(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	// KindSource has no default TTL: retention for it is driven by task
	// activity and pinning at the runledger/taskstate layer, not by Sweep.
	old, err := store.Put(ctx, Object{
		Kind: KindSource, MediaType: "text/plain", InlineBody: []byte("ancient source"),
		CreatedAt: now.Add(-400 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	removed, err := store.Sweep(ctx, DefaultRetentionPolicy(), now)
	if err != nil {
		t.Fatalf("Sweep() error = %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Sweep() unexpectedly removed unclassified kind: %v", removed)
	}
	if _, err := store.Get(ctx, old.ID); err != nil {
		t.Fatalf("expected KindSource object to survive Sweep, got error: %v", err)
	}
}

func TestCleanupOrphanBlobs(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	large := make([]byte, InlineThreshold+1)
	for i := range large {
		large[i] = byte(i)
	}
	referenced, err := store.Put(ctx, Object{Kind: KindTestOutput, MediaType: "text/plain", InlineBody: large})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	orphanContent := []byte("this blob is written directly and never referenced by a row")
	orphanSha := ContentSHA256Hex(orphanContent)
	orphanPath, err := store.blobs.Write(orphanSha, orphanContent)
	if err != nil {
		t.Fatalf("write orphan blob error = %v", err)
	}

	removed, err := store.CleanupOrphanBlobs(ctx)
	if err != nil {
		t.Fatalf("CleanupOrphanBlobs() error = %v", err)
	}
	if len(removed) != 1 || removed[0] != orphanPath {
		t.Fatalf("CleanupOrphanBlobs() removed = %v, want [%s]", removed, orphanPath)
	}

	// The referenced blob must still be readable.
	got, err := store.Get(ctx, referenced.ID)
	if err != nil {
		t.Fatalf("Get() referenced object after cleanup error = %v", err)
	}
	if len(got.InlineBody) != len(large) {
		t.Fatalf("referenced blob content length changed after cleanup: got %d, want %d", len(got.InlineBody), len(large))
	}

	// Running cleanup again must be safe (resumable / idempotent).
	removedAgain, err := store.CleanupOrphanBlobs(ctx)
	if err != nil {
		t.Fatalf("second CleanupOrphanBlobs() error = %v", err)
	}
	if len(removedAgain) != 0 {
		t.Fatalf("second CleanupOrphanBlobs() removed = %v, want none", removedAgain)
	}
}

func TestDeleteObject_SharedBlobSurvivesUntilLastReferenceGone(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	content := make([]byte, InlineThreshold+1)
	for i := range content {
		content[i] = byte(i % 251)
	}

	// Two different kinds, identical content: they dedupe at the blob
	// layer (content-addressed by sha256 alone) but not at the row layer
	// (evidence_dedup is keyed by kind + sha256), so both rows exist.
	a, err := store.Put(ctx, Object{Kind: KindToolResult, MediaType: "text/plain", InlineBody: content})
	if err != nil {
		t.Fatalf("Put() a error = %v", err)
	}
	b, err := store.Put(ctx, Object{Kind: KindCommandOutput, MediaType: "text/plain", InlineBody: content})
	if err != nil {
		t.Fatalf("Put() b error = %v", err)
	}
	if a.BlobPath != b.BlobPath {
		t.Fatalf("expected shared blob path for identical content, got %q and %q", a.BlobPath, b.BlobPath)
	}

	if err := store.deleteObject(ctx, a.ID); err != nil {
		t.Fatalf("deleteObject(a) error = %v", err)
	}

	// b's row still references the shared blob; it must still be readable.
	got, err := store.Get(ctx, b.ID)
	if err != nil {
		t.Fatalf("Get(b) after deleting a error = %v", err)
	}
	if len(got.InlineBody) != len(content) {
		t.Fatalf("shared blob content length changed: got %d, want %d", len(got.InlineBody), len(content))
	}
}
