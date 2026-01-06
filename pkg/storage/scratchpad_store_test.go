package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestScratchpadEntryRoundTrip(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "scratchpad.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	entry := ScratchpadEntry{
		Key:       "entry-1",
		EntryType: "analysis",
		Raw:       []byte("raw"),
		Summary:   "summary",
		Metadata:  "{\"foo\":\"bar\"}",
		CreatedBy: "agent-1",
		CreatedAt: time.Now().UTC(),
	}

	if _, err := store.UpsertScratchpadEntry(ctx, entry); err != nil {
		t.Fatalf("UpsertScratchpadEntry() error = %v", err)
	}

	loaded, err := store.GetScratchpadEntry(ctx, entry.Key)
	if err != nil {
		t.Fatalf("GetScratchpadEntry() error = %v", err)
	}
	if loaded == nil {
		t.Fatalf("expected entry")
	}
	if loaded.Key != entry.Key {
		t.Fatalf("expected key %s, got %s", entry.Key, loaded.Key)
	}
	if loaded.EntryType != entry.EntryType {
		t.Fatalf("expected entry_type %s, got %s", entry.EntryType, loaded.EntryType)
	}
	if string(loaded.Raw) != string(entry.Raw) {
		t.Fatalf("expected raw %q, got %q", entry.Raw, loaded.Raw)
	}
	if loaded.Summary != entry.Summary {
		t.Fatalf("expected summary %q, got %q", entry.Summary, loaded.Summary)
	}
}

func TestScratchpadEntryList(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "scratchpad-list.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)
	entries := []ScratchpadEntry{
		{Key: "entry-1", EntryType: "analysis", Summary: "one", CreatedAt: base},
		{Key: "entry-2", EntryType: "analysis", Summary: "two", CreatedAt: base.Add(10 * time.Second)},
	}
	for _, entry := range entries {
		if _, err := store.UpsertScratchpadEntry(ctx, entry); err != nil {
			t.Fatalf("UpsertScratchpadEntry() error = %v", err)
		}
	}

	listed, err := store.ListScratchpadEntries(ctx, 1)
	if err != nil {
		t.Fatalf("ListScratchpadEntries() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(listed))
	}
	if listed[0].Key != "entry-2" {
		t.Fatalf("expected newest entry, got %s", listed[0].Key)
	}
}
