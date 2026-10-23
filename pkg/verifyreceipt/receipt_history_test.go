package verifyreceipt

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/evidence"
)

func TestLoad_FindsReceiptBeyondUnrelatedTreeHistory(t *testing.T) {
	repo, store := newRepo(t), newStore(t)
	want, err := Record(t.Context(), store, RecordOptions{Dir: repo, Argv: []string{"true"}, Paths: []string{"**"}})
	if err != nil {
		t.Fatal(err)
	}
	// This fixture tests lookup, not fsync durability.
	store.DB().SetMaxOpenConns(1)
	store.DB().SetMaxIdleConns(1)
	if _, err := store.DB().Exec("PRAGMA synchronous=OFF"); err != nil {
		t.Fatal(err)
	}
	other := want
	other.Tree = strings.Repeat("b", 40)
	for i := range 5001 {
		other.Name = fmt.Sprintf("unrelated-%d", i)
		body, err := json.Marshal(other)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Put(t.Context(), evidence.Object{Kind: evidence.KindVerificationReceipt, MediaType: "application/json", InlineBody: body, Metadata: map[string]any{"tree": other.Tree}}); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := Load(t.Context(), store, want.Tree)
	if err != nil || len(loaded.Receipts) != 1 || loaded.Receipts[0].ID != want.ID || len(loaded.Rejected) != 0 {
		t.Fatalf("valid tree receipt hidden by unrelated history: %+v err=%v", loaded, err)
	}
}
