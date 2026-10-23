package evidence

import (
	"testing"
	"time"
)

func TestQuery_TreeFilterPrecedesLimit(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	want, err := store.Put(t.Context(), Object{Kind: KindVerificationReceipt, InlineBody: []byte("wanted"), CreatedAt: now.Add(-time.Hour), Metadata: map[string]any{"tree": "wanted-tree"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Put(t.Context(), Object{Kind: KindVerificationReceipt, InlineBody: []byte("newer unrelated"), CreatedAt: now, Metadata: map[string]any{"tree": "other-tree", "repo": "wanted-tree"}})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.Query(t.Context(), Query{Kinds: []Kind{KindVerificationReceipt}, Tree: "wanted-tree", Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].ID != want.ID {
		t.Fatalf("tree filter lost older matching receipt: %+v err=%v", rows, err)
	}
	rows, err = store.Query(t.Context(), Query{Tree: "wanted-tree' OR 1=1 --", Limit: 1})
	if err != nil || len(rows) != 0 {
		t.Fatalf("tree filter was not exact: %+v err=%v", rows, err)
	}
}
