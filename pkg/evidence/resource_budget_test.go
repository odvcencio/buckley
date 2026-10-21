package evidence

import (
	"bytes"
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"m31labs.dev/buckley/pkg/storage"
)

func TestSQLiteStore_StorageBudgetChargesCompressedBytesAndRejectsBeforeWrite(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := storage.ConfigureStorageBudget(ctx, s.DB(), 16<<20); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("compressible evidence\n"), 100000)
	obj, err := s.Put(ctx, Object{Kind: KindToolResult, MediaType: "text/plain", InlineBody: body, Metadata: map[string]any{MetaRunID: "retained-run"}})
	if err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(obj.BlobPath)
	if err != nil {
		t.Fatal(err)
	}
	var storedBytes int64
	if err := s.DB().QueryRow(`SELECT stored_byte_count FROM evidence_objects WHERE evidence_id=?`, obj.ID).Scan(&storedBytes); err != nil || storedBytes != stat.Size() {
		t.Fatalf("stored bytes=%d file=%d err=%v", storedBytes, stat.Size(), err)
	}
	budget, err := storage.ReadStorageBudget(ctx, s.DB())
	if err != nil || budget.UsedBytes >= int64(len(body)) {
		t.Fatalf("compressed accounting=%+v raw=%d err=%v", budget, len(body), err)
	}
	got, err := s.Get(ctx, obj.ID)
	if err != nil || !bytes.Equal(body, got.InlineBody) {
		t.Fatalf("retained replay evidence: %v", err)
	}
	noise := make([]byte, 5<<20)
	_, _ = rand.New(rand.NewSource(42)).Read(noise)
	_, err = s.Put(ctx, Object{Kind: KindToolResult, InlineBody: noise})
	if !storage.IsStorageBudgetError(err) {
		t.Fatalf("oversized evidence admission=%v", err)
	}
	path, _ := s.Blobs().PathForHash(ContentSHA256Hex(noise))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("rejected evidence created file: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("rejected evidence created shard: %v", err)
	}
	duplicate, err := s.Put(ctx, Object{Kind: obj.Kind, InlineBody: body})
	if err != nil || duplicate.ID != obj.ID {
		t.Fatalf("dedup under quota: %+v err=%v", duplicate, err)
	}
}

func TestSQLiteStore_StorageBudgetRollbackRemovesUnreferencedBlob(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := storage.ConfigureStorageBudget(ctx, s.DB(), 16<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`CREATE TRIGGER reject_evidence_pin BEFORE INSERT ON evidence_pins BEGIN SELECT RAISE(ABORT,'storage byte budget exceeded'); END`); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("rollback evidence"), 1000)
	_, err := s.Put(ctx, Object{Kind: KindToolResult, InlineBody: body, Metadata: map[string]any{MetaRunID: "run"}})
	if !storage.IsStorageBudgetError(err) {
		t.Fatalf("pin rejection=%v", err)
	}
	path, _ := s.Blobs().PathForHash(ContentSHA256Hex(body))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("failed transaction left blob: %v", err)
	}
	budget, err := storage.ReadStorageBudget(ctx, s.DB())
	if err != nil || budget.UsedBytes != 0 {
		t.Fatalf("rollback accounting=%+v err=%v", budget, err)
	}
}

func TestSQLiteStore_StorageByteMigrationMeasuresLegacyCompressedBlobs(t *testing.T) {
	s := newTestStore(t)
	body := bytes.Repeat([]byte("legacy compressed body\n"), 10000)
	obj, err := s.Put(context.Background(), Object{Kind: KindToolResult, InlineBody: body})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`DELETE FROM evidence_schema_migrations WHERE version>=3; ALTER TABLE evidence_objects DROP COLUMN stored_byte_count`); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWithDB(s.DB(), s.Blobs().root); err != nil {
		t.Fatal(err)
	}
	var physical int64
	if err := s.DB().QueryRow(`SELECT stored_byte_count FROM evidence_objects WHERE evidence_id=?`, obj.ID).Scan(&physical); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(obj.BlobPath)
	if err != nil || physical != info.Size() || physical >= int64(len(body)) {
		t.Fatalf("legacy size=%d err=%v", physical, err)
	}
}
