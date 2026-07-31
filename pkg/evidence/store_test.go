package evidence

import (
	"bytes"
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "evidence.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestSQLiteStore_PutGetRoundTrip_Inline(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	obj := Object{
		Kind:       KindToolResult,
		MediaType:  "text/plain",
		InlineBody: []byte("hello evidence"),
		Metadata: map[string]any{
			MetaSessionID: "sess-1",
			MetaTaskID:    "task-1",
		},
	}

	put, err := store.Put(ctx, obj)
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if put.Storage != StorageInline {
		t.Fatalf("Storage = %v, want inline", put.Storage)
	}
	if put.Sensitivity != SensitivityWorkspace {
		t.Fatalf("Sensitivity = %v, want workspace", put.Sensitivity)
	}

	got, err := store.Get(ctx, put.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !bytes.Equal(got.InlineBody, obj.InlineBody) {
		t.Fatalf("round-tripped body = %q, want %q", got.InlineBody, obj.InlineBody)
	}
	if got.Metadata[MetaSessionID] != "sess-1" {
		t.Fatalf("round-tripped metadata missing session_id: %+v", got.Metadata)
	}
	if got.ContentSHA256 != ContentSHA256Hex(obj.InlineBody) {
		t.Fatalf("ContentSHA256 mismatch")
	}
}

func TestSQLiteStore_PutGetRoundTrip_Blob(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	large := bytes.Repeat([]byte("x"), InlineThreshold+1024)
	put, err := store.Put(ctx, Object{Kind: KindTestOutput, MediaType: "text/plain", InlineBody: large})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if put.Storage != StorageBlob {
		t.Fatalf("Storage = %v, want blob", put.Storage)
	}
	if put.BlobPath == "" {
		t.Fatalf("expected non-empty BlobPath for blob-tier object")
	}

	got, err := store.Get(ctx, put.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !bytes.Equal(got.InlineBody, large) {
		t.Fatalf("blob round-trip content mismatch: got %d bytes, want %d bytes", len(got.InlineBody), len(large))
	}
}

func TestSQLiteStore_ContentAddressDedup(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	content := []byte("duplicate content")

	first, err := store.Put(ctx, Object{Kind: KindSource, MediaType: "text/plain", InlineBody: content})
	if err != nil {
		t.Fatalf("first Put() error = %v", err)
	}
	second, err := store.Put(ctx, Object{Kind: KindSource, MediaType: "text/plain", InlineBody: content})
	if err != nil {
		t.Fatalf("second Put() error = %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected deduplicated ID, got %q and %q", first.ID, second.ID)
	}

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM evidence_objects WHERE kind = ?`, string(KindSource)).Scan(&count); err != nil {
		t.Fatalf("count query error = %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one row after dedup, got %d", count)
	}
}

func TestSQLiteStore_Get_NotFound(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Get(context.Background(), "ev_doesnotexist"); err == nil {
		t.Fatalf("expected error for missing object")
	}
}

func TestSQLiteStore_PinRelease(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	put, err := store.Put(ctx, Object{Kind: KindCheckpoint, MediaType: "text/markdown", InlineBody: []byte("state")})
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	if err := store.Pin(ctx, put.ID, "commit_receipt:rcpt-1"); err != nil {
		t.Fatalf("Pin() error = %v", err)
	}
	pinned, err := store.isPinned(ctx, put.ID)
	if err != nil {
		t.Fatalf("isPinned() error = %v", err)
	}
	if !pinned {
		t.Fatalf("expected object to be pinned")
	}

	// Pinning twice with the same reason must not error.
	if err := store.Pin(ctx, put.ID, "commit_receipt:rcpt-1"); err != nil {
		t.Fatalf("re-Pin() error = %v", err)
	}

	if err := store.Release(ctx, put.ID, "commit_receipt:rcpt-1"); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	pinned, err = store.isPinned(ctx, put.ID)
	if err != nil {
		t.Fatalf("isPinned() after release error = %v", err)
	}
	if pinned {
		t.Fatalf("expected object to be unpinned after Release")
	}
}

func TestSQLiteStore_Query(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if _, err := store.Put(ctx, Object{
		Kind: KindSource, MediaType: "text/x-go", InlineBody: []byte("a"),
		Metadata: map[string]any{MetaSessionID: "sess-a", MetaPath: "pkg/foo.go"},
	}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if _, err := store.Put(ctx, Object{
		Kind: KindDiff, MediaType: "text/x-diff", InlineBody: []byte("b"),
		Metadata: map[string]any{MetaSessionID: "sess-b", MetaPath: "pkg/bar.go"},
	}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	byKind, err := store.Query(ctx, Query{Kinds: []Kind{KindSource}})
	if err != nil {
		t.Fatalf("Query() by kind error = %v", err)
	}
	if len(byKind) != 1 || byKind[0].Kind != KindSource {
		t.Fatalf("Query() by kind = %+v, want one KindSource result", byKind)
	}

	bySession, err := store.Query(ctx, Query{SessionID: "sess-b"})
	if err != nil {
		t.Fatalf("Query() by session error = %v", err)
	}
	if len(bySession) != 1 || bySession[0].Metadata[MetaSessionID] != "sess-b" {
		t.Fatalf("Query() by session = %+v, want one sess-b result", bySession)
	}

	byPath, err := store.Query(ctx, Query{Path: "pkg/foo.go"})
	if err != nil {
		t.Fatalf("Query() by path error = %v", err)
	}
	if len(byPath) != 1 {
		t.Fatalf("Query() by path = %+v, want one result", byPath)
	}

	all, err := store.Query(ctx, Query{})
	if err != nil {
		t.Fatalf("Query() unfiltered error = %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Query() unfiltered = %d results, want 2", len(all))
	}

	limited, err := store.Query(ctx, Query{Limit: 1})
	if err != nil {
		t.Fatalf("Query() limited error = %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("Query() limited = %d results, want 1", len(limited))
	}
}

func TestSQLiteStore_Query_Since(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	old := time.Now().Add(-48 * time.Hour)
	if _, err := store.Put(ctx, Object{Kind: KindSource, MediaType: "text/plain", InlineBody: []byte("old"), CreatedAt: old}); err != nil {
		t.Fatalf("Put() old error = %v", err)
	}
	if _, err := store.Put(ctx, Object{Kind: KindSource, MediaType: "text/plain", InlineBody: []byte("new")}); err != nil {
		t.Fatalf("Put() new error = %v", err)
	}

	recent, err := store.Query(ctx, Query{Since: time.Now().Add(-1 * time.Hour)})
	if err != nil {
		t.Fatalf("Query() since error = %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("Query() since = %d results, want 1", len(recent))
	}
}

// TestSQLiteStore_ConcurrentWriters exercises SQLite WAL concurrent-writer
// safety: many goroutines Put both overlapping (dedup-triggering) and
// distinct content simultaneously. Every call must succeed, dedup must still
// converge on exactly one row per distinct (kind, content), and no data
// race or corruption should occur.
func TestSQLiteStore_ConcurrentWriters(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	const workers = 16
	const dedupContent = "shared content raced by every worker"

	var wg sync.WaitGroup
	errs := make(chan error, workers*2)
	ids := make(chan string, workers*2)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			dedup, err := store.Put(ctx, Object{Kind: KindToolResult, MediaType: "text/plain", InlineBody: []byte(dedupContent)})
			if err != nil {
				errs <- err
				return
			}
			ids <- dedup.ID

			unique, err := store.Put(ctx, Object{Kind: KindToolResult, MediaType: "text/plain", InlineBody: []byte("unique-" + itoa(int64(i)))})
			if err != nil {
				errs <- err
				return
			}
			ids <- unique.ID
		}(i)
	}

	wg.Wait()
	close(errs)
	close(ids)

	for err := range errs {
		t.Fatalf("concurrent Put() error = %v", err)
	}

	seen := map[string]bool{}
	dedupIDs := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != workers+1 {
		t.Fatalf("expected %d distinct IDs (1 deduped + %d unique), got %d", workers+1, workers, len(seen))
	}

	all, err := store.Query(ctx, Query{Kinds: []Kind{KindToolResult}})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	for _, o := range all {
		dedupIDs[o.ID] = true
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM evidence_objects WHERE content_sha256 = ?`, ContentSHA256Hex([]byte(dedupContent))).Scan(&count); err != nil {
		t.Fatalf("count query error = %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one row for raced content after concurrent writes, got %d", count)
	}
}
