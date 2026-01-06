package storage

import (
	"path/filepath"
	"testing"
)

func TestGetSchemaVersion(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer store.Close()

	version, err := store.GetSchemaVersion()
	if err != nil {
		t.Fatalf("GetSchemaVersion() error = %v", err)
	}

	// Should be at the latest migration version
	expectedVersion := len(migrations)
	if version != expectedVersion {
		t.Errorf("GetSchemaVersion() = %d, want %d", version, expectedVersion)
	}
}

func TestGetMigrationHistory(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer store.Close()

	history, err := store.GetMigrationHistory()
	if err != nil {
		t.Fatalf("GetMigrationHistory() error = %v", err)
	}

	// Should have all migrations recorded
	if len(history) != len(migrations) {
		t.Errorf("GetMigrationHistory() returned %d migrations, want %d", len(history), len(migrations))
	}

	// Verify migration names match
	for i, h := range history {
		if h.Version != migrations[i].Version {
			t.Errorf("migration %d version = %d, want %d", i, h.Version, migrations[i].Version)
		}
		if h.Name != migrations[i].Name {
			t.Errorf("migration %d name = %q, want %q", i, h.Name, migrations[i].Name)
		}
		if h.AppliedAt == "" {
			t.Errorf("migration %d applied_at is empty", i)
		}
	}
}

func TestMigrationsIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	// Create store (runs migrations)
	store1, err := New(dbPath)
	if err != nil {
		t.Fatalf("first New() error = %v", err)
	}
	version1, _ := store1.GetSchemaVersion()
	store1.Close()

	// Re-open store (should not re-run migrations)
	store2, err := New(dbPath)
	if err != nil {
		t.Fatalf("second New() error = %v", err)
	}
	defer store2.Close()

	version2, _ := store2.GetSchemaVersion()

	if version1 != version2 {
		t.Errorf("version changed after reopen: %d -> %d", version1, version2)
	}

	// Check that no duplicate migrations were recorded
	history, err := store2.GetMigrationHistory()
	if err != nil {
		t.Fatalf("GetMigrationHistory() error = %v", err)
	}

	if len(history) != len(migrations) {
		t.Errorf("duplicate migrations recorded: got %d, want %d", len(history), len(migrations))
	}
}

func TestMigrationsApplyInOrder(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer store.Close()

	history, err := store.GetMigrationHistory()
	if err != nil {
		t.Fatalf("GetMigrationHistory() error = %v", err)
	}

	// Verify migrations are in order
	for i := 1; i < len(history); i++ {
		if history[i].Version <= history[i-1].Version {
			t.Errorf("migrations not in order: version %d came after %d", history[i].Version, history[i-1].Version)
		}
	}
}
