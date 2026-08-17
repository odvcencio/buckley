package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Migration is the legacy non-transaction-bound migration shape. SQLite
// initializers must use SQLiteMigration with MigrateSQLite instead.
type Migration struct {
	Version int
	Name    string
	Apply   func(db *sql.DB) error
}

// MigrationDB is the connection-bound SQL surface available to an atomic
// SQLite migration. It is deliberately smaller than *sql.DB so an Apply
// function cannot escape the transaction's dedicated connection.
type MigrationDB interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// SQLiteMigration is one schema step applied under a connection-bound
// BEGIN IMMEDIATE transaction.
type SQLiteMigration struct {
	Version int
	Name    string
	Apply   func(db MigrationDB) error
}

// Migrate is retained for adapters that are not SQLite-specific. It does not
// provide connection-bound ownership and must not be used by SQLite startup.
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

// MigrateSQLite serializes migration discovery, DDL, and version recording
// in one SQLite write transaction. BEGIN IMMEDIATE elects one migration
// owner without blocking WAL readers; a crash rolls back both schema and
// tracking changes, so the next opener can safely retry idempotent steps.
func MigrateSQLite(db *sql.DB, table string, migrations []SQLiteMigration) error {
	return MigrateSQLiteWithSetup(db, table, migrations, nil)
}

// MigrateSQLiteWithSetup is MigrateSQLite with an idempotent setup phase
// inside the same ownership transaction. SQLite initializers use setup for
// legacy schema repair and base DDL that must precede versioned migrations.
func MigrateSQLiteWithSetup(db *sql.DB, table string, migrations []SQLiteMigration, setup func(db MigrationDB) error) error {
	if db == nil {
		return fmt.Errorf("migration database is required")
	}
	if !validMigrationTable(table) {
		return fmt.Errorf("invalid migration table %q", table)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return fmt.Errorf("configure migration busy timeout: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	bound := migrationConn{ctx: ctx, conn: conn}
	if setup != nil {
		if err := setup(bound); err != nil {
			return fmt.Errorf("migration setup: %w", err)
		}
	}
	if _, err := bound.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`, table)); err != nil {
		return fmt.Errorf("create %s: %w", table, err)
	}
	var currentVersion int
	if err := bound.QueryRow(fmt.Sprintf(`SELECT COALESCE(MAX(version), 0) FROM %s`, table)).Scan(&currentVersion); err != nil {
		return fmt.Errorf("get schema version: %w", err)
	}
	for _, migration := range migrations {
		if migration.Version <= currentVersion {
			continue
		}
		if migration.Apply == nil {
			return fmt.Errorf("migration %d (%s): apply function is required", migration.Version, migration.Name)
		}
		if err := migration.Apply(bound); err != nil {
			return fmt.Errorf("migration %d (%s): %w", migration.Version, migration.Name, err)
		}
		if _, err := bound.Exec(fmt.Sprintf(`INSERT INTO %s (version, name) VALUES (?, ?)`, table), migration.Version, migration.Name); err != nil {
			return fmt.Errorf("record migration %d: %w", migration.Version, err)
		}
		currentVersion = migration.Version
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	committed = true
	return nil
}

type migrationConn struct {
	ctx  context.Context
	conn *sql.Conn
}

func (db migrationConn) Exec(query string, args ...any) (sql.Result, error) {
	return db.conn.ExecContext(db.ctx, query, args...)
}

func (db migrationConn) Query(query string, args ...any) (*sql.Rows, error) {
	return db.conn.QueryContext(db.ctx, query, args...)
}

func (db migrationConn) QueryRow(query string, args ...any) *sql.Row {
	return db.conn.QueryRowContext(db.ctx, query, args...)
}

func validMigrationTable(table string) bool {
	if strings.TrimSpace(table) != table || table == "" {
		return false
	}
	for _, r := range table {
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
