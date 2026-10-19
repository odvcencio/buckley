package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/storage"
)

func TestDBCleanup_DryRunThenPruneCompletedHistory(t *testing.T) {
	dir := t.TempDir()
	previousConfig := configPath
	t.Cleanup(func() { configPath = previousConfig })
	configPath = filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("retention:\n  history_ttl: 24h\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "buckley.db")
	store, err := storage.New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	old := time.Now().Add(-48 * time.Hour)
	if err := store.CreateSession(&storage.Session{ID: "expired", Status: storage.SessionStatusCompleted, CreatedAt: old, LastActive: old, CompletedAt: &old}); err != nil {
		t.Fatal(err)
	}
	if err := runDBCommand([]string{"cleanup", "--db", path, "--dry-run"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetSession("expired"); err != nil {
		t.Fatalf("dry run deleted history: %v", err)
	}
	if err := runDBCommand([]string{"cleanup", "--db", path, "--vacuum"}); err != nil {
		t.Fatal(err)
	}
	if session, err := store.GetSession("expired"); err == nil && session != nil {
		t.Fatal("expired session remains")
	}
	if err := runDBCommand([]string{"cleanup", "--db", path, "--dry-run", "--vacuum"}); err == nil {
		t.Fatal("dry run allowed vacuum")
	}
}
