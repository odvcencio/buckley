package reviewledger

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestBackfill_ReadOnlyAndIdempotent(t *testing.T) {
	root := t.TempDir()
	database := filepath.Join(root, "evidence.db")
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE evidence_objects(content_sha256 TEXT, inline_body BLOB, storage_kind TEXT)`); err != nil {
		t.Fatal(err)
	}
	inline := []byte("inline verification")
	if _, err := db.Exec(`INSERT INTO evidence_objects VALUES(?,?,'inline')`, Hash(inline), inline); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(database)
	if err != nil {
		t.Fatal(err)
	}
	evidenceRoot := filepath.Join(root, "evidence")
	body := []byte("compressed evidence")
	hash := Hash(body)
	blobPath := filepath.Join(evidenceRoot, "sha256", hash[:2], hash[2:4], hash+".zst")
	if err := os.MkdirAll(filepath.Dir(blobPath), 0700); err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	compressed := encoder.EncodeAll(body, nil)
	if err := os.WriteFile(blobPath, compressed, 0400); err != nil {
		t.Fatal(err)
	}
	recordsDir := filepath.Join(root, "reviews")
	if err := os.Mkdir(recordsDir, 0700); err != nil {
		t.Fatal(err)
	}
	record := fixtureRecord()
	record.Evidence = []string{hash, Hash(inline)}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recordsDir, "record.json"), data, 0400); err != nil {
		t.Fatal(err)
	}
	objects := &fakeObjects{data: map[string][]byte{}}
	ledger := New(objects, "bucket", "", t.TempDir())
	for range 2 {
		counts, err := ledger.Backfill(context.Background(), evidenceRoot, database, recordsDir)
		if err != nil || counts.Blobs != 2 || counts.Records != 1 {
			t.Fatalf("counts=%+v err=%v", counts, err)
		}
	}
	if len(objects.data) != 3 {
		t.Fatalf("objects=%d", len(objects.data))
	}
	after, err := os.ReadFile(database)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("backfill changed source database")
	}
	after, err = os.ReadFile(blobPath)
	if err != nil || !bytes.Equal(compressed, after) {
		t.Fatal("backfill changed source blob")
	}
}
