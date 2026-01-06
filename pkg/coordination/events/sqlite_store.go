package events

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteEventStore implements EventStore using SQLite
type SQLiteEventStore struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewSQLiteEventStore creates a new SQLite-backed event store
func NewSQLiteEventStore(dbPath string) (*SQLiteEventStore, error) {
	if filePath, onDisk := sqliteFilePathFromDSN(dbPath); onDisk {
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

	// Configure connection pool
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(0)

	// Enable WAL mode for better concurrent access
	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	// Set busy timeout to 5 seconds
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to set busy timeout: %w", err)
	}

	// Enable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable foreign keys: %w", err)
	}

	store := &SQLiteEventStore{db: db}

	// Initialize schema
	if err := store.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	return store, nil
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

// initSchema creates the necessary tables
func (s *SQLiteEventStore) initSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		stream_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		version INTEGER NOT NULL,
		data TEXT NOT NULL,
		metadata TEXT NOT NULL,
		timestamp TIMESTAMP NOT NULL,
		UNIQUE(stream_id, version)
	);

	CREATE INDEX IF NOT EXISTS idx_events_stream ON events(stream_id);
	CREATE INDEX IF NOT EXISTS idx_events_stream_version ON events(stream_id, version);
	CREATE INDEX IF NOT EXISTS idx_events_timestamp ON events(timestamp);

	CREATE TABLE IF NOT EXISTS snapshots (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		stream_id TEXT NOT NULL,
		version INTEGER NOT NULL,
		state TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(stream_id, version)
	);

	CREATE INDEX IF NOT EXISTS idx_snapshots_stream ON snapshots(stream_id);
	CREATE INDEX IF NOT EXISTS idx_snapshots_stream_version ON snapshots(stream_id, version DESC);
	`

	_, err := s.db.Exec(schema)
	return err
}

// Append adds events to a stream
func (s *SQLiteEventStore) Append(ctx context.Context, streamID string, events []Event) error {
	if len(events) == 0 {
		return nil
	}

	// Check context before starting
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO events (stream_id, event_type, version, data, metadata, timestamp)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, event := range events {
		// Marshal data to JSON
		dataJSON, err := json.Marshal(event.Data)
		if err != nil {
			return fmt.Errorf("failed to marshal event data: %w", err)
		}

		// Marshal metadata to JSON
		metadataJSON, err := json.Marshal(event.Metadata)
		if err != nil {
			return fmt.Errorf("failed to marshal event metadata: %w", err)
		}

		_, err = stmt.ExecContext(ctx,
			streamID,
			event.Type,
			event.Version,
			string(dataJSON),
			string(metadataJSON),
			event.Timestamp,
		)
		if err != nil {
			return fmt.Errorf("failed to insert event: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// Read retrieves events from a stream starting at a version
func (s *SQLiteEventStore) Read(ctx context.Context, streamID string, fromVersion int64) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT stream_id, event_type, version, data, metadata, timestamp
		FROM events
		WHERE stream_id = ? AND version >= ?
		ORDER BY version ASC
	`

	rows, err := s.db.QueryContext(ctx, query, streamID, fromVersion)
	if err != nil {
		return nil, fmt.Errorf("failed to query events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		var dataJSON, metadataJSON string

		err := rows.Scan(
			&event.StreamID,
			&event.Type,
			&event.Version,
			&dataJSON,
			&metadataJSON,
			&event.Timestamp,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan event: %w", err)
		}

		// Unmarshal data
		if err := json.Unmarshal([]byte(dataJSON), &event.Data); err != nil {
			return nil, fmt.Errorf("failed to unmarshal event data: %w", err)
		}

		// Unmarshal metadata
		if err := json.Unmarshal([]byte(metadataJSON), &event.Metadata); err != nil {
			return nil, fmt.Errorf("failed to unmarshal event metadata: %w", err)
		}

		events = append(events, event)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	return events, nil
}

// Subscribe to events in a stream
// Note: This is a basic implementation that returns a no-op subscription.
// A production implementation would use triggers, polling, or a notification mechanism.
func (s *SQLiteEventStore) Subscribe(ctx context.Context, streamID string, handler EventHandler) (Subscription, error) {
	// Basic implementation: return a no-op subscription
	// Real implementation would require:
	// - Polling mechanism to check for new events
	// - Or SQLite triggers with notification channels
	// - Or external message queue integration
	return &noopSubscription{}, nil
}

// Snapshot saves a state snapshot
func (s *SQLiteEventStore) Snapshot(ctx context.Context, streamID string, version int64, state interface{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Marshal state to JSON
	stateJSON, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal snapshot state: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	query := `
		INSERT INTO snapshots (stream_id, version, state, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(stream_id, version) DO UPDATE SET
			state = excluded.state,
			created_at = excluded.created_at
	`

	_, err = s.db.ExecContext(ctx, query, streamID, version, string(stateJSON), time.Now().UTC())
	if err != nil {
		return fmt.Errorf("failed to save snapshot: %w", err)
	}

	return nil
}

// LoadSnapshot retrieves the latest snapshot
func (s *SQLiteEventStore) LoadSnapshot(ctx context.Context, streamID string) (state interface{}, version int64, err error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	query := `
		SELECT version, state
		FROM snapshots
		WHERE stream_id = ?
		ORDER BY version DESC
		LIMIT 1
	`

	var stateJSON string
	err = s.db.QueryRowContext(ctx, query, streamID).Scan(&version, &stateJSON)
	if err != nil {
		return nil, 0, err
	}

	// Unmarshal state
	var result interface{}
	if err := json.Unmarshal([]byte(stateJSON), &result); err != nil {
		return nil, 0, fmt.Errorf("failed to unmarshal snapshot state: %w", err)
	}

	return result, version, nil
}

// Close closes the database connection
func (s *SQLiteEventStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.db.Close()
}

// DB returns the underlying database connection for testing or advanced usage
func (s *SQLiteEventStore) DB() *sql.DB {
	return s.db
}
