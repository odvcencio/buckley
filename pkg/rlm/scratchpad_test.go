package rlm

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/storage"
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
