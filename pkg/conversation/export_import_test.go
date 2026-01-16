package conversation

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/storage"
)

func TestExporterImporter_JSONRoundTrip(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "conv.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	sessionID := "sess-1"
	if err := store.CreateSession(&storage.Session{ID: sessionID, CreatedAt: time.Now(), LastActive: time.Now(), Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := store.SaveMessage(&storage.Message{SessionID: sessionID, Role: "user", Content: "hello", Timestamp: time.Now()}); err != nil {
		t.Fatalf("save message: %v", err)
	}
	if err := store.SaveMessage(&storage.Message{SessionID: sessionID, Role: "assistant", Content: "hi", Timestamp: time.Now().Add(time.Second)}); err != nil {
		t.Fatalf("save message: %v", err)
	}

	exporter := NewExporter(store)
	data, err := exporter.Export(sessionID, ExportOptions{Format: ExportJSON, IncludeMetadata: true})
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	importer := NewImporter(store)
	result, err := importer.Import(data, ExportJSON)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.MessageCount != 2 {
		t.Fatalf("expected 2 imported messages, got %d", result.MessageCount)
	}
	if strings.TrimSpace(result.SessionID) == "" {
		t.Fatalf("expected new session id")
	}
}

func TestExporter_MarkdownOutput(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "conv.db"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	defer store.Close()

	sessionID := "sess-1"
	if err := store.CreateSession(&storage.Session{ID: sessionID, CreatedAt: time.Now(), LastActive: time.Now(), Status: storage.SessionStatusActive}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := store.SaveMessage(&storage.Message{SessionID: sessionID, Role: "user", Content: "hello", Timestamp: time.Now()}); err != nil {
		t.Fatalf("save message: %v", err)
	}

	exporter := NewExporter(store)
	data, err := exporter.Export(sessionID, ExportOptions{Format: ExportMarkdown})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Fatalf("expected markdown to include message content")
	}
}
