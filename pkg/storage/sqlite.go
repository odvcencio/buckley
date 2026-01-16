package storage

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed schema.sql
var schemaSQL string

// Store manages SQLite database operations
type Store struct {
	db         *sql.DB
	observers  []Observer
	observerMu sync.RWMutex
}

// ErrStoreClosed indicates the underlying database connection is unavailable.
var ErrStoreClosed = errors.New("storage: closed")

// New creates a new store and initializes the database
func New(dbPath string) (*Store, error) {
	filePath, onDisk := sqliteFilePathFromDSN(dbPath)
	if onDisk {
		// Ensure parent directory exists for on-disk databases. (SQLite state can
		// include sensitive prompts/artifacts; default to private permissions.)
		if dir := filepath.Dir(filePath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("failed to create database directory: %w", err)
			}
		}
		if err := ensurePrivateSQLiteFile(filePath); err != nil {
			return nil, err
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Configure connection pool for SQLite
	// SQLite supports one writer at a time, but multiple readers with WAL mode
	db.SetMaxOpenConns(10)   // Allow multiple concurrent reads
	db.SetMaxIdleConns(5)    // Keep connections alive for reuse
	db.SetConnMaxLifetime(0) // Connections never expire

	// Enable WAL mode for better concurrent access
	// WAL allows multiple readers and one writer simultaneously
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	// Set busy timeout to 5 seconds - wait instead of immediately returning SQLITE_BUSY
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		return nil, fmt.Errorf("failed to set busy timeout: %w", err)
	}

	// Enable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	// Run migrations
	if err := runMigrations(db); err != nil {
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	return &Store{db: db}, nil
}

func sqliteFilePathFromDSN(dsn string) (string, bool) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" || dsn == ":memory:" {
		return "", false
	}
	if strings.HasPrefix(dsn, "file:") {
		u, err := url.Parse(dsn)
		if err != nil || !strings.EqualFold(strings.TrimSpace(u.Scheme), "file") {
			return "", false
		}
		path := strings.TrimSpace(u.Path)
		if path == "" {
			path = strings.TrimSpace(u.Opaque)
		}
		if path == "" || path == ":memory:" {
			return "", false
		}
		return path, true
	}
	if strings.Contains(dsn, "://") {
		return "", false
	}
	return dsn, true
}

func ensurePrivateSQLiteFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("db path cannot be empty")
	}

	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat db path: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("create db file: %w", err)
	}
	return f.Close()
}

// Close closes the database connection
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying database connection
func (s *Store) DB() *sql.DB {
	return s.db
}

// AddObserver registers a new observer that will receive storage events.
func (s *Store) AddObserver(observer Observer) {
	s.observerMu.Lock()
	s.observers = append(s.observers, observer)
	s.observerMu.Unlock()
}

// notify fans out events to observers without blocking the writer.
func (s *Store) notify(event Event) {
	s.observerMu.RLock()
	observers := append([]Observer(nil), s.observers...)
	s.observerMu.RUnlock()

	for _, observer := range observers {
		observer := observer
		go observer.HandleStorageEvent(event)
	}
}

// Migration represents a database schema migration
type Migration struct {
	Version int
	Name    string
	Apply   func(db *sql.DB) error
}

// migrations is the ordered list of all migrations
var migrations = []Migration{
	{1, "initial_schema", func(db *sql.DB) error { return nil }}, // Base schema from schemaSQL
	{2, "session_columns", ensureSessionSchema},
	{3, "message_columns", ensureMessagesSchema},
	{4, "memories_columns", ensureMemoriesSchema},
	{5, "embeddings_columns", ensureEmbeddingsSchema},
	{6, "pending_approvals_reason", ensurePendingApprovalsSchema},
	{7, "session_principal", ensureSessionsPrincipalSchema},
	{8, "session_principal_backfill", backfillSessionsPrincipal},
	{9, "rlm_scratchpad_entries", ensureScratchpadSchema},
	{10, "messages_search", ensureMessagesSearchSchema},
	{11, "memories_project_path", ensureMemoriesSchema},
}

// runMigrations runs the schema migrations with version tracking
func runMigrations(db *sql.DB) error {
	// First apply the base schema (idempotent via CREATE TABLE IF NOT EXISTS)
	if _, err := db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("apply base schema: %w", err)
	}

	// Get current schema version
	currentVersion, err := getSchemaVersion(db)
	if err != nil {
		return fmt.Errorf("get schema version: %w", err)
	}

	// Apply any pending migrations
	for _, m := range migrations {
		if m.Version <= currentVersion {
			continue
		}

		if err := m.Apply(db); err != nil {
			return fmt.Errorf("migration %d (%s): %w", m.Version, m.Name, err)
		}

		if err := recordMigration(db, m.Version, m.Name); err != nil {
			return fmt.Errorf("record migration %d: %w", m.Version, err)
		}
	}

	return nil
}

// getSchemaVersion returns the current schema version (0 if no migrations applied)
func getSchemaVersion(db *sql.DB) (int, error) {
	var version int
	err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_migrations").Scan(&version)
	if err != nil {
		// Table might not exist yet (first run before schemaSQL applied)
		if strings.Contains(err.Error(), "no such table") {
			return 0, nil
		}
		return 0, err
	}
	return version, nil
}

// recordMigration records that a migration was applied
func recordMigration(db *sql.DB, version int, name string) error {
	_, err := db.Exec(
		"INSERT INTO schema_migrations (version, name) VALUES (?, ?)",
		version, name,
	)
	return err
}

// GetSchemaVersion returns the current schema version for external use
func (s *Store) GetSchemaVersion() (int, error) {
	return getSchemaVersion(s.db)
}

// GetMigrationHistory returns the list of applied migrations
func (s *Store) GetMigrationHistory() ([]struct {
	Version   int
	Name      string
	AppliedAt string
}, error) {
	rows, err := s.db.Query("SELECT version, name, applied_at FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []struct {
		Version   int
		Name      string
		AppliedAt string
	}
	for rows.Next() {
		var h struct {
			Version   int
			Name      string
			AppliedAt string
		}
		if err := rows.Scan(&h.Version, &h.Name, &h.AppliedAt); err != nil {
			return nil, err
		}
		history = append(history, h)
	}
	return history, rows.Err()
}

func ensureSessionSchema(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(sessions)`)
	if err != nil {
		return fmt.Errorf("session pragma: %w", err)
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan session pragma: %w", err)
		}
		cols[strings.ToLower(name)] = true
	}

	if err := rows.Err(); err != nil {
		return err
	}

	if !cols["status"] {
		if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN status TEXT NOT NULL DEFAULT 'active'`); err != nil {
			return fmt.Errorf("add session status: %w", err)
		}
		if _, err := db.Exec(`UPDATE sessions SET status = 'active' WHERE status IS NULL OR status = ''`); err != nil {
			return fmt.Errorf("backfill session status: %w", err)
		}
	}

	if !cols["completed_at"] {
		if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN completed_at TIMESTAMP`); err != nil {
			return fmt.Errorf("add session completed_at: %w", err)
		}
	}

	return nil
}

func ensureSessionsPrincipalSchema(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(sessions)`)
	if err != nil {
		return fmt.Errorf("session pragma: %w", err)
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan session pragma: %w", err)
		}
		cols[strings.ToLower(name)] = true
	}

	if err := rows.Err(); err != nil {
		return err
	}

	if cols["principal"] {
		return nil
	}

	if _, err := db.Exec(`ALTER TABLE sessions ADD COLUMN principal TEXT`); err != nil {
		return fmt.Errorf("add sessions principal: %w", err)
	}

	return nil
}

func backfillSessionsPrincipal(db *sql.DB) error {
	if _, err := db.Exec(`UPDATE sessions SET principal = 'anonymous' WHERE principal IS NULL OR TRIM(principal) = ''`); err != nil {
		return fmt.Errorf("backfill sessions principal: %w", err)
	}
	return nil
}

func ensureMessagesSchema(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(messages)`)
	if err != nil {
		return fmt.Errorf("messages pragma: %w", err)
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notNull int
			dflt    any
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan messages pragma: %w", err)
		}
		cols[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if !cols["content_json"] {
		if _, err := db.Exec(`ALTER TABLE messages ADD COLUMN content_json TEXT`); err != nil {
			return fmt.Errorf("add messages.content_json: %w", err)
		}
	}
	if !cols["content_type"] {
		if _, err := db.Exec(`ALTER TABLE messages ADD COLUMN content_type TEXT NOT NULL DEFAULT 'text'`); err != nil {
			return fmt.Errorf("add messages.content_type: %w", err)
		}
	}
	if !cols["reasoning"] {
		if _, err := db.Exec(`ALTER TABLE messages ADD COLUMN reasoning TEXT`); err != nil {
			return fmt.Errorf("add messages.reasoning: %w", err)
		}
	}
	if !cols["is_truncated"] {
		if _, err := db.Exec(`ALTER TABLE messages ADD COLUMN is_truncated BOOLEAN DEFAULT FALSE`); err != nil {
			return fmt.Errorf("add messages.is_truncated: %w", err)
		}
	}
	return nil
}

func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code()
		return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
	}
	return false
}
