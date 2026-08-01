package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/v2/pkg/storage"
)

func seedExportSession(t *testing.T, dbPath string) {
	t.Helper()
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	defer store.Close()

	session := &storage.Session{
		ID:          "session-export-1",
		ProjectPath: "/home/user/project",
		GitRepo:     "github.com/example/project",
		GitBranch:   "main",
		Model:       "openai/gpt-4o",
		CreatedAt:   time.Now().Add(-time.Hour),
		LastActive:  time.Now(),
		Status:      storage.SessionStatusActive,
	}
	if err := store.CreateSession(session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	messages := []*storage.Message{
		{
			SessionID: session.ID,
			Role:      "user",
			Content:   "Please set OPENAI_API_KEY=sk-secretvalue1234567890 for me",
			Timestamp: time.Now().Add(-30 * time.Minute),
		},
		{
			SessionID: session.ID,
			Role:      "assistant",
			Content:   "Sure, I updated the config.",
			Timestamp: time.Now().Add(-29 * time.Minute),
		},
	}
	for _, msg := range messages {
		if err := store.SaveMessage(msg); err != nil {
			t.Fatalf("SaveMessage: %v", err)
		}
	}
}

func TestRunSessionExportCommand_JSONRedactsSecrets(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "buckley.db")
	seedExportSession(t, dbPath)

	outPath := filepath.Join(tmpDir, "export.json")
	if err := runSessionExportCommand([]string{"--db", dbPath, "--format", "json", "--out", outPath, "session-export-1"}); err != nil {
		t.Fatalf("runSessionExportCommand: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "sk-secretvalue1234567890") {
		t.Fatalf("export leaked secret content: %s", data)
	}

	var doc SessionExportDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if doc.SchemaVersion != SessionExportSchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", doc.SchemaVersion, SessionExportSchemaVersion)
	}
	if doc.Source != "storage_transcript" {
		t.Fatalf("Source = %q, want storage_transcript", doc.Source)
	}
	if doc.Session.ID != "session-export-1" {
		t.Fatalf("Session.ID = %q, want session-export-1", doc.Session.ID)
	}
	if len(doc.Messages) != 2 {
		t.Fatalf("len(Messages) = %d, want 2", len(doc.Messages))
	}
	if doc.Messages[1].Content != "Sure, I updated the config." {
		t.Fatalf("Messages[1].Content = %q, want unredacted assistant reply", doc.Messages[1].Content)
	}
}

func TestRunSessionExportCommand_Markdown(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "buckley.db")
	seedExportSession(t, dbPath)

	outPath := filepath.Join(tmpDir, "export.md")
	if err := runSessionExportCommand([]string{"--db", dbPath, "--format", "markdown", "--out", outPath, "session-export-1"}); err != nil {
		t.Fatalf("runSessionExportCommand: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "# Session export: session-export-1") {
		t.Fatalf("markdown export missing header: %s", content)
	}
	if strings.Contains(content, "sk-secretvalue1234567890") {
		t.Fatalf("markdown export leaked secret content: %s", content)
	}
	if !strings.Contains(content, "## User") {
		t.Fatalf("markdown export missing user section: %s", content)
	}
}

func TestRunSessionExportCommand_UnknownSession(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "buckley.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	store.Close()

	err = runSessionExportCommand([]string{"--db", dbPath, "does-not-exist"})
	if err == nil {
		t.Fatal("expected an error for an unknown session")
	}
}

func TestRunSessionExportCommand_RejectsBadFormat(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "buckley.db")
	seedExportSession(t, dbPath)

	err := runSessionExportCommand([]string{"--db", dbPath, "--format", "yaml", "session-export-1"})
	if err == nil {
		t.Fatal("expected an error for an unsupported format")
	}
}

func TestRunSessionCommand_RoutesToExport(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "buckley.db")
	seedExportSession(t, dbPath)

	outPath := filepath.Join(tmpDir, "export.json")
	err := runSessionCommand([]string{"export", "--db", dbPath, "--out", outPath, "session-export-1"})
	if err != nil {
		t.Fatalf("runSessionCommand: %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("expected export file to exist: %v", err)
	}
}

func TestRunSessionCommand_UnknownSubcommand(t *testing.T) {
	if err := runSessionCommand([]string{"bogus"}); err == nil {
		t.Fatal("expected an error for an unknown session subcommand")
	}
	if err := runSessionCommand(nil); err == nil {
		t.Fatal("expected an error with no subcommand")
	}
}

func TestRenderSessionExportMarkdownEmptyRoleDoesNotPanic(t *testing.T) {
	doc := SessionExportDocument{Messages: []SessionExportMessage{{Role: "", Content: "orphan"}}}
	out := renderSessionExportMarkdown(doc)
	if !strings.Contains(out, "Message") || !strings.Contains(out, "orphan") {
		t.Fatalf("empty-role message not rendered safely: %q", out)
	}
}
