package storage

import (
	"database/sql"
	"path/filepath"
	"sync"
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

func TestMigrationsSerializeConcurrentStoreOpeners(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concurrent-storage.db")
	const openers = 24
	start := make(chan struct{})
	errs := make(chan error, openers)
	var wg sync.WaitGroup
	for range openers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			store, err := New(dbPath)
			if err == nil {
				err = store.Close()
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent New: %v", err)
		}
	}
	store, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	history, err := store.GetMigrationHistory()
	if err != nil || len(history) != len(migrations) {
		t.Fatalf("history count=%d err=%v, want %d", len(history), err, len(migrations))
	}
	var mode string
	if err := store.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode=%q err=%v, want wal", mode, err)
	}
}

func TestMigrationsRecoverFullyAppliedSchemaWithoutVersionRows(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "partial-storage.db")
	store, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM schema_migrations WHERE version >= 2`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(dbPath)
	if err != nil {
		t.Fatalf("recover partially recorded schema: %v", err)
	}
	defer reopened.Close()
	history, err := reopened.GetMigrationHistory()
	if err != nil || len(history) != len(migrations) {
		t.Fatalf("history count=%d err=%v, want %d", len(history), err, len(migrations))
	}
	rows, err := reopened.db.Query(`PRAGMA table_info(sessions)`)
	if err != nil {
		t.Fatal(err)
	}
	principalColumns := 0
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if name == "principal" {
			principalColumns++
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	var projectIndexes int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = 'idx_memories_project'`).Scan(&projectIndexes); err != nil {
		t.Fatal(err)
	}
	if principalColumns != 1 || projectIndexes != 1 {
		t.Fatalf("principal columns=%d project indexes=%d, want 1/1", principalColumns, projectIndexes)
	}
}
