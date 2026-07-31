package storage

import (
	"database/sql"
	"fmt"
)

// Migration is a single, ordered, idempotent schema step.
type Migration struct {
	Version int
	Name    string
	Apply   func(db *sql.DB) error
}

// Migrate creates the given migration-tracking table if it does not already
// exist, then applies every migration whose version is greater than the
// highest recorded version, in order, recording each one as it lands.
//
// It is the shared runner behind pkg/storage's own schema_migrations table
// as well as pkg/evidence and pkg/runledger's per-package migration tables.
func Migrate(db *sql.DB, table string, migrations []Migration) error {
	if _, err := db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`, table)); err != nil {
		return fmt.Errorf("create %s: %w", table, err)
	}

	var currentVersion int
	if err := db.QueryRow(fmt.Sprintf(`SELECT COALESCE(MAX(version), 0) FROM %s`, table)).Scan(&currentVersion); err != nil {
		return fmt.Errorf("get schema version: %w", err)
	}

	for _, m := range migrations {
		if m.Version <= currentVersion {
			continue
		}
		if err := m.Apply(db); err != nil {
			return fmt.Errorf("migration %d (%s): %w", m.Version, m.Name, err)
		}
		if _, err := db.Exec(fmt.Sprintf(`INSERT INTO %s (version, name) VALUES (?, ?)`, table), m.Version, m.Name); err != nil {
			return fmt.Errorf("record migration %d: %w", m.Version, err)
		}
	}
	return nil
}
