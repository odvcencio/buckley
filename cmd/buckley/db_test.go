package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/storage"
)

func TestDBBackupAndRestore(t *testing.T) {
	tmpDir := t.TempDir()

	dbPath := filepath.Join(tmpDir, "buckley.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	_ = store.Close()

	backupPath := filepath.Join(tmpDir, "backup", "buckley.backup.db")
	if err := runDBBackup([]string{"--db", dbPath, "--out", backupPath}); err != nil {
		t.Fatalf("runDBBackup: %v", err)
	}
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("expected backup to exist: %v", err)
	}

	restorePath := filepath.Join(tmpDir, "restore", "buckley.db")
	if err := runDBRestore([]string{"--db", restorePath, "--in", backupPath}); err != nil {
		t.Fatalf("runDBRestore: %v", err)
	}
	if _, err := os.Stat(restorePath); err != nil {
		t.Fatalf("expected restored db to exist: %v", err)
	}

	// Destination exists should require --force.
	if err := runDBRestore([]string{"--db", restorePath, "--in", backupPath}); err == nil {
		t.Fatalf("expected restore to fail without --force when destination exists")
	}

	if err := runDBRestore([]string{"--db", restorePath, "--in", backupPath, "--force"}); err != nil {
		t.Fatalf("runDBRestore with --force: %v", err)
	}
	if _, err := os.Stat(restorePath); err != nil {
		t.Fatalf("expected restored db to exist after --force: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(restorePath))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	foundBak := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "buckley.db.bak.") {
			foundBak = true
			break
		}
	}
	if !foundBak {
		t.Fatalf("expected .bak file in %s", filepath.Dir(restorePath))
	}
}
