package telemetry

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore persists telemetry events to SQLite for replay and analysis.
type SQLiteStore struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewSQLiteStore creates a new SQLite-backed event store.
// Accepts a file path or ":memory:" for in-memory storage.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	if dbPath != ":memory:" {
		if dir := filepath.Dir(dbPath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("creating database directory: %w", err)
			}
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	db.SetMaxOpenConns(1) // SQLite single-writer
	db.SetMaxIdleConns(1)

	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		db.Close()
		return nil, fmt.Errorf("setting busy timeout: %w", err)
	}

	if err := createEventTables(db); err != nil {
		db.Close()
		return nil, err
	}

	return &SQLiteStore{db: db}, nil
}

func createEventTables(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS telemetry_events (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			stream_id  TEXT NOT NULL,
			event_type TEXT NOT NULL,
			version    INTEGER NOT NULL,
			session_id TEXT NOT NULL DEFAULT '',
			plan_id    TEXT NOT NULL DEFAULT '',
			task_id    TEXT NOT NULL DEFAULT '',
			data       TEXT NOT NULL DEFAULT '{}',
			timestamp  TIMESTAMP NOT NULL,
			UNIQUE(stream_id, version)
		);
		CREATE INDEX IF NOT EXISTS idx_telemetry_stream ON telemetry_events(stream_id, version);
		CREATE INDEX IF NOT EXISTS idx_telemetry_type ON telemetry_events(stream_id, event_type);
		CREATE INDEX IF NOT EXISTS idx_telemetry_time ON telemetry_events(timestamp);
	`)
	if err != nil {
		return fmt.Errorf("creating event tables: %w", err)
	}
	return nil
}

// Append writes events to the store. Each event gets an auto-incremented
// version within the stream for ordering and replay.
func (s *SQLiteStore) Append(ctx context.Context, streamID string, events []Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback()

	// Get current max version for this stream
	var maxVersion int64
	row := tx.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM telemetry_events WHERE stream_id = ?",
		streamID,
	)
	if err := row.Scan(&maxVersion); err != nil {
		return fmt.Errorf("reading max version: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO telemetry_events (stream_id, event_type, version, session_id, plan_id, task_id, data, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		return fmt.Errorf("preparing statement: %w", err)
	}
	defer stmt.Close()

	for i, event := range events {
		data, err := json.Marshal(event.Data)
		if err != nil {
			return fmt.Errorf("marshaling event data: %w", err)
		}
		version := maxVersion + int64(i) + 1
		if _, err := stmt.ExecContext(ctx,
			streamID,
			string(event.Type),
			version,
			event.SessionID,
			event.PlanID,
			event.TaskID,
			string(data),
			event.Timestamp,
		); err != nil {
			return fmt.Errorf("inserting event: %w", err)
		}
	}

	return tx.Commit()
}

// Read returns events for a stream starting from the given version (inclusive).
// Pass version=0 to read all events.
func (s *SQLiteStore) Read(ctx context.Context, streamID string, fromVersion int64) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx,
		`SELECT event_type, session_id, plan_id, task_id, data, timestamp
		 FROM telemetry_events
		 WHERE stream_id = ? AND version >= ?
		 ORDER BY version ASC`,
		streamID, fromVersion,
	)
	if err != nil {
		return nil, fmt.Errorf("querying events: %w", err)
	}
	defer rows.Close()

	return scanEvents(rows)
}

// ReadByType returns events for a stream filtered by event type.
func (s *SQLiteStore) ReadByType(ctx context.Context, streamID string, eventType EventType) ([]Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.QueryContext(ctx,
		`SELECT event_type, session_id, plan_id, task_id, data, timestamp
		 FROM telemetry_events
		 WHERE stream_id = ? AND event_type = ?
		 ORDER BY version ASC`,
		streamID, string(eventType),
	)
	if err != nil {
		return nil, fmt.Errorf("querying events by type: %w", err)
	}
	defer rows.Close()

	return scanEvents(rows)
}

func scanEvents(rows *sql.Rows) ([]Event, error) {
	var events []Event
	for rows.Next() {
		var (
			eventType string
			sessionID string
			planID    string
			taskID    string
			dataJSON  string
			ts        time.Time
		)
		if err := rows.Scan(&eventType, &sessionID, &planID, &taskID, &dataJSON, &ts); err != nil {
			return nil, fmt.Errorf("scanning event: %w", err)
		}

		var data map[string]any
		if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
			return nil, fmt.Errorf("unmarshaling event data: %w", err)
		}

		events = append(events, Event{
			Type:      EventType(eventType),
			Timestamp: ts,
			SessionID: sessionID,
			PlanID:    planID,
			TaskID:    taskID,
			Data:      data,
		})
	}
	return events, rows.Err()
}

// Close closes the database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
