package storage

import "testing"

func TestSessionSummaryCRUD(t *testing.T) {
	store, err := New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	if err := store.SaveSessionSummary("session-xyz", "summary text"); err != nil {
		t.Fatalf("SaveSessionSummary: %v", err)
	}
	got, err := store.GetSessionSummary("session-xyz")
	if err != nil {
		t.Fatalf("GetSessionSummary: %v", err)
	}
	if got != "summary text" {
		t.Fatalf("expected summary text, got %q", got)
	}
}

func TestListSessionSummaries(t *testing.T) {
	store, err := New(t.TempDir() + "/buckley.db")
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	_ = store.SaveSessionSummary("a", "first")
	_ = store.SaveSessionSummary("b", "second")

	result, err := store.ListSessionSummaries()
	if err != nil {
		t.Fatalf("ListSessionSummaries: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(result))
	}
	if result["a"] != "first" || result["b"] != "second" {
		t.Fatalf("unexpected summaries: %#v", result)
	}
}
