package evidence

import (
	"bytes"
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
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
	noise := make([]byte, 20<<20) // larger than the whole 16 MiB budget, charged once
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

// The budget charges the bytes actually kept for an evidence blob, once. It
// used to charge four times the stored size, so a store with 486 MB on disk
// read as 1.37 GB against a 512 MiB limit.
func TestSQLiteStore_StorageBudgetChargesStoredBytesOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := storage.ConfigureStorageBudget(ctx, s.DB(), 64<<20); err != nil {
		t.Fatal(err)
	}
	before, err := storage.ReadStorageBudget(ctx, s.DB())
	if err != nil {
		t.Fatal(err)
	}
	noise := make([]byte, 2<<20)
	_, _ = rand.New(rand.NewSource(7)).Read(noise)
	obj, err := s.Put(ctx, Object{Kind: KindToolResult, MediaType: "application/octet-stream", InlineBody: noise, Metadata: map[string]any{MetaRunID: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	var stored int64
	if err := s.DB().QueryRow(`SELECT stored_byte_count FROM evidence_objects WHERE evidence_id=?`, obj.ID).Scan(&stored); err != nil || stored < 2<<20 {
		t.Fatalf("stored=%d err=%v", stored, err)
	}
	after, err := storage.ReadStorageBudget(ctx, s.DB())
	if err != nil {
		t.Fatal(err)
	}
	charged := after.UsedBytes - before.UsedBytes
	if charged < stored || charged > stored+64<<10 {
		t.Fatalf("charged %d bytes for %d stored bytes; want the stored size plus small row overhead", charged, stored)
	}
}

func TestSQLiteStore_StorageBudgetError_SaysWhatToDo(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := storage.ConfigureStorageBudget(ctx, s.DB(), 16<<20); err != nil {
		t.Fatal(err)
	}
	noise := make([]byte, 20<<20)
	_, _ = rand.New(rand.NewSource(9)).Read(noise)
	_, err := s.Put(ctx, Object{Kind: KindToolResult, InlineBody: noise})
	if !storage.IsStorageBudgetError(err) {
		t.Fatalf("error = %v", err)
	}
	for _, want := range []string{"buckley db cleanup", "resources.storage_bytes", "16777216"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
}

// A database accounted by the old formula keeps its old triggers. Configuring
// its budget again must re-account it, so the fix reaches existing stores.
func TestConfigureStorageBudget_ReaccountsDatabasesWithOldCostFormula(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := storage.ConfigureStorageBudget(ctx, s.DB(), 64<<20); err != nil {
		t.Fatal(err)
	}
	noise := make([]byte, 2<<20)
	_, _ = rand.New(rand.NewSource(3)).Read(noise)
	if _, err := s.Put(ctx, Object{Kind: KindToolResult, InlineBody: noise, Metadata: map[string]any{MetaRunID: "r"}}); err != nil {
		t.Fatal(err)
	}
	current, err := storage.ReadStorageBudget(ctx, s.DB())
	if err != nil {
		t.Fatal(err)
	}
	// Reinstall the evidence triggers without the version marker and inflate the
	// counter, as a v1 database would be.
	rows, err := s.DB().Query(`SELECT name, sql FROM sqlite_master WHERE type='trigger' AND name LIKE 'storage_budget_evidence_objects_%'`)
	if err != nil {
		t.Fatal(err)
	}
	type trig struct{ name, sql string }
	var triggers []trig
	for rows.Next() {
		var tr trig
		if err := rows.Scan(&tr.name, &tr.sql); err != nil {
			t.Fatal(err)
		}
		triggers = append(triggers, tr)
	}
	rows.Close()
	if len(triggers) != 3 || !strings.Contains(triggers[0].sql, "/*cost:v2*/") {
		t.Fatalf("triggers=%d, versioned=%v", len(triggers), len(triggers) > 0 && strings.Contains(triggers[0].sql, "/*cost:v2*/"))
	}
	for _, tr := range triggers {
		if _, err := s.DB().Exec(`DROP TRIGGER "` + tr.name + `"`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DB().Exec(strings.ReplaceAll(tr.sql, "/*cost:v2*/", "")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DB().Exec(`UPDATE storage_budget SET used_bytes = used_bytes * 4`); err != nil {
		t.Fatal(err)
	}
	if err := storage.ConfigureStorageBudget(ctx, s.DB(), 64<<20); err != nil {
		t.Fatal(err)
	}
	after, err := storage.ReadStorageBudget(ctx, s.DB())
	if err != nil || after.UsedBytes != current.UsedBytes {
		t.Fatalf("re-accounted used=%d want %d err=%v", after.UsedBytes, current.UsedBytes, err)
	}
	var marked int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name LIKE 'storage_budget_evidence_objects_%' AND instr(sql,'/*cost:v2*/')>0`).Scan(&marked); err != nil || marked != 3 {
		t.Fatalf("versioned triggers=%d err=%v", marked, err)
	}
}
