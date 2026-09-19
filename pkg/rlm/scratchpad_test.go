package rlm

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/storage"
)

func TestScratchpadWriteInspect(t *testing.T) {
	pad := NewScratchpad(nil, func(raw []byte) string {
		return "summary"
	}, ScratchpadConfig{})

	key, err := pad.Write(context.Background(), WriteRequest{
		Type:      EntryTypeAnalysis,
		Raw:       []byte("raw"),
		Metadata:  map[string]any{"foo": "bar"},
		CreatedBy: "agent-1",
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	summary, err := pad.Inspect(context.Background(), key)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if summary == nil {
		t.Fatalf("expected summary")
	}
	if summary.Summary != "summary" {
		t.Fatalf("expected summary, got %q", summary.Summary)
	}
	if summary.Metadata["foo"] != "bar" {
		t.Fatalf("expected metadata foo=bar")
	}

	entry, err := pad.InspectRaw(context.Background(), key)
	if err != nil {
		t.Fatalf("InspectRaw() error = %v", err)
	}
	if entry == nil {
		t.Fatalf("expected entry")
	}
	if string(entry.Raw) != "raw" {
		t.Fatalf("expected raw content")
	}
}

func TestScratchpadEvictsOldestEntry(t *testing.T) {
	pad := NewScratchpad(nil, nil, ScratchpadConfig{
		MaxEntriesMemory: 1,
		EvictionPolicy:   "lru",
	})

	key1, err := pad.Write(context.Background(), WriteRequest{
		Type: EntryTypeAnalysis,
		Raw:  []byte("first"),
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	time.Sleep(2 * time.Millisecond)

	key2, err := pad.Write(context.Background(), WriteRequest{
		Type: EntryTypeAnalysis,
		Raw:  []byte("second"),
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if entry, _ := pad.InspectRaw(context.Background(), key1); entry != nil {
		t.Fatalf("expected oldest entry to be evicted")
	}
	if entry, _ := pad.InspectRaw(context.Background(), key2); entry == nil {
		t.Fatalf("expected newest entry to remain")
	}
}

func TestScratchpadEvictionPolicyLRUUsesLastAccess(t *testing.T) {
	pad := NewScratchpad(nil, nil, ScratchpadConfig{
		MaxEntriesMemory: 2,
		EvictionPolicy:   " LRU ",
		DefaultTTL:       time.Hour,
	})
	base := time.Now().UTC().Add(-time.Minute)

	key1, err := pad.Write(context.Background(), WriteRequest{
		Type:      EntryTypeAnalysis,
		Raw:       []byte("first"),
		CreatedAt: base,
	})
	if err != nil {
		t.Fatalf("Write first error = %v", err)
	}
	key2, err := pad.Write(context.Background(), WriteRequest{
		Type:      EntryTypeAnalysis,
		Raw:       []byte("second"),
		CreatedAt: base.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("Write second error = %v", err)
	}
	if _, err := pad.InspectRaw(context.Background(), key1); err != nil {
		t.Fatalf("InspectRaw first error = %v", err)
	}
	key3, err := pad.Write(context.Background(), WriteRequest{
		Type:      EntryTypeAnalysis,
		Raw:       []byte("third"),
		CreatedAt: base.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("Write third error = %v", err)
	}

	if entry, _ := pad.InspectRaw(context.Background(), key1); entry == nil {
		t.Fatalf("expected recently accessed first entry to remain under lru")
	}
	if entry, _ := pad.InspectRaw(context.Background(), key2); entry != nil {
		t.Fatalf("expected least recently accessed second entry to be evicted under lru")
	}
	if entry, _ := pad.InspectRaw(context.Background(), key3); entry == nil {
		t.Fatalf("expected newest third entry to remain")
	}
}

func TestScratchpadEvictionPolicyFIFOUsesCreationOrder(t *testing.T) {
	pad := NewScratchpad(nil, nil, ScratchpadConfig{
		MaxEntriesMemory: 2,
		EvictionPolicy:   " FIFO ",
		DefaultTTL:       time.Hour,
	})
	base := time.Now().UTC().Add(-time.Minute)

	key1, err := pad.Write(context.Background(), WriteRequest{
		Type:      EntryTypeAnalysis,
		Raw:       []byte("first"),
		CreatedAt: base,
	})
	if err != nil {
		t.Fatalf("Write first error = %v", err)
	}
	key2, err := pad.Write(context.Background(), WriteRequest{
		Type:      EntryTypeAnalysis,
		Raw:       []byte("second"),
		CreatedAt: base.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("Write second error = %v", err)
	}
	if _, err := pad.InspectRaw(context.Background(), key1); err != nil {
		t.Fatalf("InspectRaw first error = %v", err)
	}
	key3, err := pad.Write(context.Background(), WriteRequest{
		Type:      EntryTypeAnalysis,
		Raw:       []byte("third"),
		CreatedAt: base.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("Write third error = %v", err)
	}

	if entry, _ := pad.InspectRaw(context.Background(), key1); entry != nil {
		t.Fatalf("expected oldest first entry to be evicted under fifo despite recent access")
	}
	if entry, _ := pad.InspectRaw(context.Background(), key2); entry == nil {
		t.Fatalf("expected second entry to remain under fifo")
	}
	if entry, _ := pad.InspectRaw(context.Background(), key3); entry == nil {
		t.Fatalf("expected newest third entry to remain")
	}
}

func TestScratchpadExpiresEntries(t *testing.T) {
	pad := NewScratchpad(nil, nil, ScratchpadConfig{
		DefaultTTL: 1 * time.Second,
	})

	key, err := pad.Write(context.Background(), WriteRequest{
		Type:      EntryTypeAnalysis,
		Raw:       []byte("expired"),
		CreatedAt: time.Now().Add(-2 * time.Second),
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if entry, _ := pad.Inspect(context.Background(), key); entry != nil {
		t.Fatalf("expected expired entry to be evicted")
	}
}

func TestScratchpadPersistFlags(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "scratchpad.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	defer store.Close()

	pad := NewScratchpad(store, nil, ScratchpadConfig{
		PersistArtifacts: true,
		PersistDecisions: false,
	})

	ctx := context.Background()
	artifactKey, err := pad.Write(ctx, WriteRequest{
		Type: EntryTypeArtifact,
		Raw:  []byte("artifact"),
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	decisionKey, err := pad.Write(ctx, WriteRequest{
		Type: EntryTypeDecision,
		Raw:  []byte("decision"),
	})
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if entry, err := store.GetScratchpadEntry(ctx, artifactKey); err != nil || entry == nil {
		t.Fatalf("expected artifact to persist, err=%v", err)
	}
	if entry, err := store.GetScratchpadEntry(ctx, decisionKey); err != nil || entry != nil {
		t.Fatalf("expected decision not to persist, err=%v", err)
	}
}

func TestScratchpadWriteDurableOnlyPersistsWithoutActiveMemory(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "scratchpad.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	defer store.Close()

	pad := NewScratchpad(store, nil, ScratchpadConfig{
		MaxEntriesMemory: 1,
		EvictionPolicy:   "lru",
		PersistArtifacts: true,
		PersistDecisions: true,
	})
	ctx := context.Background()
	workerKey, err := pad.Write(ctx, WriteRequest{
		Type:    EntryTypeAnalysis,
		Raw:     []byte("worker evidence must stay live"),
		Summary: "worker evidence",
	})
	if err != nil {
		t.Fatalf("Write() worker evidence error = %v", err)
	}

	decisionKey, err := pad.WriteDurableOnly(ctx, WriteRequest{
		Type:    EntryTypeDecision,
		Raw:     []byte(`{"content":"public decision"}`),
		Summary: "public decision",
	})
	if err != nil {
		t.Fatalf("WriteDurableOnly() decision error = %v", err)
	}
	artifactKey, err := pad.WriteDurableOnly(ctx, WriteRequest{
		Type:    EntryTypeArtifact,
		Raw:     []byte(`{"artifacts":["file.txt"]}`),
		Summary: "file.txt",
	})
	if err != nil {
		t.Fatalf("WriteDurableOnly() artifact error = %v", err)
	}

	entries, err := store.ListScratchpadEntries(ctx, 10)
	if err != nil {
		t.Fatalf("ListScratchpadEntries() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("persisted entries = %d, want durable decision and artifact only", len(entries))
	}
	for _, entry := range entries {
		if !strings.Contains(entry.Metadata, scratchpadDurableOnlyVisibility) {
			t.Fatalf("entry %s metadata %q missing durable-only marker", entry.Key, entry.Metadata)
		}
	}

	summaries, err := pad.ListSummaries(ctx, 10)
	if err != nil {
		t.Fatalf("ListSummaries() error = %v", err)
	}
	if len(summaries) != 1 || summaries[0].Key != workerKey || summaries[0].Summary != "worker evidence" {
		t.Fatalf("active summaries = %+v, want only worker evidence", summaries)
	}
	for _, key := range []string{decisionKey, artifactKey} {
		entry, err := pad.InspectRaw(ctx, key)
		if err != nil {
			t.Fatalf("InspectRaw(%s) error = %v", key, err)
		}
		if entry != nil {
			t.Fatalf("durable-only entry %s became active memory: %+v", key, entry)
		}
	}
}

func TestScratchpadListSummariesLimitNotStarvedByNewerDurableOnlyRows(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "scratchpad.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	defer store.Close()

	cfg := ScratchpadConfig{
		MaxEntriesMemory: 10,
		PersistArtifacts: true,
		PersistDecisions: true,
	}
	ctx := context.Background()
	writer := NewScratchpad(store, nil, cfg)
	if _, err := writer.Write(ctx, WriteRequest{
		Key:       "active-entry",
		Type:      EntryTypeArtifact,
		Raw:       []byte("active persisted evidence"),
		Summary:   "active persisted evidence",
		Metadata:  map[string]any{"model": "m", "agent_id": "a"},
		CreatedAt: time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("Write() active entry error = %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := writer.WriteDurableOnly(ctx, WriteRequest{
			Type:      EntryTypeDecision,
			Raw:       []byte(`{"content":"newer durable-only"}`),
			Summary:   "newer durable-only",
			CreatedAt: time.Now().UTC().Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("WriteDurableOnly() entry %d error = %v", i, err)
		}
	}

	reader := NewScratchpad(store, nil, cfg)
	summaries, err := reader.ListSummaries(ctx, 1)
	if err != nil {
		t.Fatalf("ListSummaries() error = %v", err)
	}
	if len(summaries) != 1 || summaries[0].Key != "active-entry" {
		t.Fatalf("summaries = %+v, want the newest active entry despite newer durable-only rows", summaries)
	}

	entries, err := store.ListScratchpadEntries(ctx, 10)
	if err != nil {
		t.Fatalf("ListScratchpadEntries() error = %v", err)
	}
	for _, entry := range entries {
		if entry.Key == "active-entry" {
			continue
		}
		if raw, err := reader.InspectRaw(ctx, entry.Key); err != nil {
			t.Fatalf("InspectRaw(%s) error = %v", entry.Key, err)
		} else if raw != nil {
			t.Fatalf("durable-only entry %s became inspectable active memory: %+v", entry.Key, raw)
		}
	}
}

func TestScratchpadWriteDurableOnlyHonorsPersistenceFlags(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "scratchpad.db"))
	if err != nil {
		t.Fatalf("storage.New() error = %v", err)
	}
	defer store.Close()

	pad := NewScratchpad(store, nil, ScratchpadConfig{
		MaxEntriesMemory: 10,
		PersistArtifacts: false,
		PersistDecisions: false,
	})
	ctx := context.Background()
	if _, err := pad.WriteDurableOnly(ctx, WriteRequest{
		Type: EntryTypeDecision,
		Raw:  []byte(`{"content":"disabled"}`),
	}); err != nil {
		t.Fatalf("WriteDurableOnly() decision error = %v", err)
	}
	if _, err := pad.WriteDurableOnly(ctx, WriteRequest{
		Type: EntryTypeArtifact,
		Raw:  []byte(`{"artifacts":["disabled"]}`),
	}); err != nil {
		t.Fatalf("WriteDurableOnly() artifact error = %v", err)
	}
	entries, err := store.ListScratchpadEntries(ctx, 10)
	if err != nil {
		t.Fatalf("ListScratchpadEntries() error = %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("persisted entries = %+v, want none when flags disabled", entries)
	}
	summaries, err := pad.ListSummaries(ctx, 10)
	if err != nil {
		t.Fatalf("ListSummaries() error = %v", err)
	}
	if len(summaries) != 0 {
		t.Fatalf("active summaries = %+v, want none", summaries)
	}
}
