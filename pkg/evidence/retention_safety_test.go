package evidence

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSweep_PinAfterSelectionProtectsObject(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	obj, err := store.Put(ctx, Object{Kind: KindToolResult, InlineBody: []byte("pin race"), CreatedAt: now.Add(-40 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := store.expiredUnpinnedIDs(ctx, ephemeralKinds, now.Add(-30*24*time.Hour), "")
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	if err := store.Pin(ctx, obj.ID, "user:keep"); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.deleteUnpinnedObjects(ctx, ids, "")
	if err != nil || len(deleted) != 0 {
		t.Fatalf("deleted=%v err=%v", deleted, err)
	}
	if _, err := store.Get(ctx, obj.ID); err != nil {
		t.Fatal(err)
	}
}

func TestSweepCount_CleansCorruptBlobsAndMultipleBatches(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	now := time.Now()
	for i := 0; i < 270; i++ {
		body := []byte(fmt.Sprintf("expired-%d", i))
		if i == 0 {
			body = bytes.Repeat(body, 2048)
		}
		obj, err := store.Put(ctx, Object{Kind: KindToolResult, InlineBody: body, CreatedAt: now.Add(-40 * 24 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := os.WriteFile(obj.BlobPath, []byte("corrupt compressed body"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	count, err := store.SweepCount(ctx, DefaultRetentionPolicy(), now)
	if err != nil || count != 270 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	files, err := store.CleanupOrphanBlobs(ctx)
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
}

func TestSweepCount_PreservesLegacyEventReferencesIncludingEscapedIDs(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.DB().Exec(`CREATE TABLE run_events(payload_json TEXT, evidence_ids_json TEXT)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now()
	var ids []string
	for i := 0; i < 6; i++ {
		obj, err := store.Put(ctx, Object{Kind: KindToolResult, InlineBody: []byte(fmt.Sprintf("legacy-%d", i)), CreatedAt: now.Add(-40 * 24 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, obj.ID)
	}
	payload, err := json.Marshal(map[string]string{
		"request_evidence_id": ids[0], "response_evidence_id": ids[1],
		"output_evidence_id": ids[2], "evidence_id": ids[3],
	})
	if err != nil {
		t.Fatal(err)
	}
	escaped := strings.Replace(string(payload), ids[0], fmt.Sprintf(`\u%04x%s`, ids[0][0], ids[0][1:]), 1)
	linked, err := json.Marshal([]string{ids[4]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`INSERT INTO run_events VALUES(?, ?), ('{}', '[]')`, escaped, string(linked)); err != nil {
		t.Fatal(err)
	}
	count, err := store.SweepCount(ctx, DefaultRetentionPolicy(), now)
	if err != nil || count != 1 {
		t.Fatalf("sweep removed referenced evidence or retained an orphan: count=%d err=%v", count, err)
	}
	for _, id := range ids[:5] {
		if _, err := store.Get(ctx, id); err != nil {
			t.Fatalf("referenced object %s lost: %v", id, err)
		}
	}
}

func TestCleanupOrphanBlobs_IsolatesDatabasesAndKeepsLegacyReferences(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "evidence")
	first, err := New(filepath.Join(dir, "session.db"), WithBlobRoot(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := New(filepath.Join(dir, "ledger.db"), WithBlobRoot(root))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	ctx := context.Background()
	body := bytes.Repeat([]byte("shared-root evidence"), 2048)
	put := func(store *SQLiteStore) Object {
		obj, err := store.Put(ctx, Object{Kind: KindToolResult, InlineBody: body, CreatedAt: time.Now().Add(-40 * 24 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return obj
	}
	a, b := put(first), put(second)
	if a.BlobPath == b.BlobPath {
		t.Fatal("independent databases share writable blob paths")
	}
	if err := second.Pin(ctx, b.ID, "user:keep"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.SweepCount(ctx, DefaultRetentionPolicy(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CleanupOrphanBlobs(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := second.Get(ctx, b.ID); err != nil || !bytes.Equal(got.InlineBody, body) {
		t.Fatalf("second database lost evidence: %v", err)
	}
	legacy, err := NewBlobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	path, err := legacy.Write(ContentSHA256Hex(body), body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.db.Exec(`UPDATE evidence_objects SET blob_path = ? WHERE evidence_id = ?`, path, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CleanupOrphanBlobs(ctx); err != nil {
		t.Fatal(err)
	}
	if got, err := second.Get(ctx, b.ID); err != nil || !bytes.Equal(got.InlineBody, body) {
		t.Fatalf("legacy evidence lost: %v", err)
	}
}

func TestCleanupOrphanBlobs_ConcurrentPutAndSweep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.db")
	first, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 24; i++ {
			body := bytes.Repeat([]byte(fmt.Sprintf("retained-%d", i)), 2048)
			obj, err := first.Put(ctx, Object{Kind: KindToolResult, InlineBody: body, CreatedAt: time.Now().Add(-40 * 24 * time.Hour), Metadata: map[string]any{MetaRunID: "active"}})
			if err != nil {
				t.Error(err)
				return
			}
			got, err := first.Get(ctx, obj.ID)
			if err != nil || !bytes.Equal(got.InlineBody, body) {
				t.Errorf("retained blob lost: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 24; i++ {
			if _, err := second.SweepCount(ctx, DefaultRetentionPolicy(), time.Now()); err != nil {
				t.Error(err)
				return
			}
			if _, err := second.CleanupOrphanBlobs(ctx); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	wg.Wait()
}

func TestCleanupStaleTemps_LeavesRecentWritesAndOtherFiles(t *testing.T) {
	store := newTestStore(t)
	root := store.blobs.writeRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".blob-old.tmp", ".blob-recent.tmp", "other.tmp"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("temporary"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, ".blob-old.tmp"), old, old); err != nil {
		t.Fatal(err)
	}
	count, err := store.CleanupStaleTemps(context.Background(), time.Now().Add(-24*time.Hour))
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	for _, name := range []string{".blob-recent.tmp", "other.tmp"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("removed %s: %v", name, err)
		}
	}
}
