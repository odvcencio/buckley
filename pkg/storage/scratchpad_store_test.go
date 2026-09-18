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

func TestScratchpadEntryListExcludingMetadataStringAppliesLimitAfterFilter(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "scratchpad-list-filtered.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)
	entries := []ScratchpadEntry{
		{Key: "active-old", EntryType: "analysis", Summary: "active", Metadata: `{"buckley_visibility":"active"}`, CreatedAt: base},
		{Key: "worker-valid", EntryType: "analysis", Summary: "worker", Metadata: `{"model":"m","agent_id":"a"}`, CreatedAt: base.Add(10 * time.Second)},
		{Key: "malformed-new", EntryType: "analysis", Summary: "malformed", Metadata: `{not-json`, CreatedAt: base.Add(20 * time.Second)},
		{Key: "durable-newer-1", EntryType: "decision", Summary: "durable", Metadata: `{"buckley_visibility":"durable_only"}`, CreatedAt: base.Add(30 * time.Second)},
		{Key: "durable-newer-2", EntryType: "artifact", Summary: "durable", Metadata: `{"buckley_visibility":"durable_only"}`, CreatedAt: base.Add(40 * time.Second)},
	}
	for _, entry := range entries {
		if _, err := store.UpsertScratchpadEntry(ctx, entry); err != nil {
			t.Fatalf("UpsertScratchpadEntry() error = %v", err)
		}
	}

	listed, err := store.ListScratchpadEntriesExcludingMetadataString(ctx, "buckley_visibility", "durable_only", 1)
	if err != nil {
		t.Fatalf("ListScratchpadEntriesExcludingMetadataString() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(listed))
	}
	if listed[0].Key != "malformed-new" {
		t.Fatalf("expected newest non-excluded entry, got %s", listed[0].Key)
	}

	listed, err = store.ListScratchpadEntriesExcludingMetadataString(ctx, "buckley_visibility", "durable_only", 10)
	if err != nil {
		t.Fatalf("ListScratchpadEntriesExcludingMetadataString() error = %v", err)
	}
	if len(listed) != 3 || listed[0].Key != "malformed-new" || listed[1].Key != "worker-valid" || listed[2].Key != "active-old" {
		t.Fatalf("filtered entries = %+v, want malformed legacy row then valid worker row then active row", listed)
	}
}

func TestScratchpadEntryListByType(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "scratchpad-type.db"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Minute)
	entries := []ScratchpadEntry{
		{Key: "entry-1", EntryType: "analysis", Summary: "one", CreatedAt: base},
		{Key: "entry-2", EntryType: "decision", Summary: "two", CreatedAt: base.Add(5 * time.Second)},
		{Key: "entry-3", EntryType: "analysis", Summary: "three", CreatedAt: base.Add(10 * time.Second)},
	}
	for _, entry := range entries {
		if _, err := store.UpsertScratchpadEntry(ctx, entry); err != nil {
			t.Fatalf("UpsertScratchpadEntry() error = %v", err)
		}
	}

	listed, err := store.ListScratchpadEntriesByType(ctx, "analysis", 10)
	if err != nil {
		t.Fatalf("ListScratchpadEntriesByType() error = %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(listed))
	}
	if listed[0].Key != "entry-3" || listed[1].Key != "entry-1" {
		t.Fatalf("expected newest analysis first, got %s then %s", listed[0].Key, listed[1].Key)
	}
}
