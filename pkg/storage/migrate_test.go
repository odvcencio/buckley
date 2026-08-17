package storage

import (
	"database/sql"
	"errors"
	"testing"
)

func TestMigrateSQLite_RollsBackSchemaWhenVersionCannotLand(t *testing.T) {
	db, err := sql.Open("sqlite", "file:atomic-migration-rollback?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	failure := errors.New("interrupted before version record")
	err = MigrateSQLite(db, "atomic_schema_migrations", []SQLiteMigration{{
		Version: 1,
		Name:    "atomic_table",
		Apply: func(db MigrationDB) error {
			if _, err := db.Exec(`CREATE TABLE atomic_table (id INTEGER PRIMARY KEY)`); err != nil {
				return err
			}
			return failure
		},
	}})
	if !errors.Is(err, failure) {
		t.Fatalf("MigrateSQLite error=%v, want interruption", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'atomic_table'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("migration DDL survived a rolled-back version record")
	}

	if err := MigrateSQLite(db, "atomic_schema_migrations", []SQLiteMigration{{
		Version: 1,
		Name:    "atomic_table",
		Apply: func(db MigrationDB) error {
			_, err := db.Exec(`CREATE TABLE atomic_table (id INTEGER PRIMARY KEY)`)
			return err
		},
	}}); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM atomic_schema_migrations WHERE version = 1`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("recorded version count=%d err=%v", count, err)
	}
}
