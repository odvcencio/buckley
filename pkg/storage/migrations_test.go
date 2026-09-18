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

func TestMigrationsCreateModelBehaviorProfilePromotionsAfterCandidates(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "profiles.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer store.Close()

	history, err := store.GetMigrationHistory()
	if err != nil {
		t.Fatalf("GetMigrationHistory: %v", err)
	}
	candidates, promotions := 0, 0
	for _, migration := range history {
		switch migration.Name {
		case "model_behavior_profiles":
			candidates = migration.Version
		case "model_behavior_profile_promotions":
			promotions = migration.Version
		}
	}
	if candidates == 0 || promotions <= candidates {
		t.Fatalf("migration versions candidates=%d promotions=%d, want promotions after candidates", candidates, promotions)
	}
	var tableCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'model_behavior_profile_promotions'`).Scan(&tableCount); err != nil {
		t.Fatalf("query promotions table: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("promotions table count = %d, want 1", tableCount)
	}
}

func TestMigrationAddsExperimentRunInputManifestToPopulatedLegacyRuns(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "legacy-experiment-runs.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := `
CREATE TABLE schema_migrations (
	version INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE experiments (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	task_prompt TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending',
	created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE experiment_variants (
	id TEXT PRIMARY KEY,
	experiment_id TEXT NOT NULL,
	name TEXT NOT NULL,
	model_id TEXT NOT NULL
);
CREATE TABLE experiment_runs (
	id TEXT PRIMARY KEY,
	experiment_id TEXT NOT NULL,
	variant_id TEXT NOT NULL,
	branch TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending',
	output TEXT,
	started_at TIMESTAMP
);
INSERT INTO experiments(id, name, task_prompt, status) VALUES ('exp-legacy', 'legacy', 'prompt', 'completed');
INSERT INTO experiment_variants(id, experiment_id, name, model_id) VALUES ('var-legacy', 'exp-legacy', 'variant', 'model');
INSERT INTO experiment_runs(id, experiment_id, variant_id, branch, status, output) VALUES ('run-legacy', 'exp-legacy', 'var-legacy', 'branch', 'completed', 'legacy output');
`
	if _, err := db.Exec(legacySchema); err != nil {
		_ = db.Close()
		t.Fatalf("create legacy schema: %v", err)
	}
	for _, migration := range migrations[:26] {
		if _, err := db.Exec(`INSERT INTO schema_migrations(version, name) VALUES (?, ?)`, migration.Version, migration.Name); err != nil {
			_ = db.Close()
			t.Fatalf("record legacy migration %d: %v", migration.Version, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := New(dbPath)
	if err != nil {
		t.Fatalf("New legacy db: %v", err)
	}
	defer store.Close()
	var output string
	var manifest sql.NullString
	var executions sql.NullString
	var usage sql.NullString
	var costUnknown int
	if err := store.db.QueryRow(`SELECT output, input_manifest_json, model_executions_json, usage_json, cost_unknown FROM experiment_runs WHERE id = 'run-legacy'`).Scan(&output, &manifest, &executions, &usage, &costUnknown); err != nil {
		t.Fatalf("query migrated run: %v", err)
	}
	if output != "legacy output" {
		t.Fatalf("output = %q, want legacy output", output)
	}
	if manifest.Valid {
		t.Fatalf("legacy run input_manifest_json = %q, want NULL", manifest.String)
	}
	if executions.Valid {
		t.Fatalf("legacy run model_executions_json = %q, want NULL", executions.String)
	}
	if usage.Valid {
		t.Fatalf("legacy run usage_json = %q, want NULL", usage.String)
	}
	if costUnknown != 0 {
		t.Fatalf("legacy run cost_unknown = %d, want 0", costUnknown)
	}
	version, err := store.GetSchemaVersion()
	if err != nil {
		t.Fatalf("GetSchemaVersion: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("schema version = %d, want %d", version, len(migrations))
	}
}
