package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ScratchpadEntry represents a persisted RLM scratchpad entry.
type ScratchpadEntry struct {
	Key       string
	EntryType string
	Raw       []byte
	Summary   string
	Metadata  string
	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// UpsertScratchpadEntry writes or updates a scratchpad entry.
func (s *Store) UpsertScratchpadEntry(ctx context.Context, entry ScratchpadEntry) (ScratchpadEntry, error) {
	if s == nil || s.db == nil {
		return ScratchpadEntry{}, ErrStoreClosed
	}
	if entry.Key == "" {
		return ScratchpadEntry{}, fmt.Errorf("scratchpad key required")
	}
	if entry.EntryType == "" {
		return ScratchpadEntry{}, fmt.Errorf("scratchpad entry_type required")
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now().UTC()
	}
	entry.UpdatedAt = time.Now().UTC()

	query := `
		INSERT INTO rlm_scratchpad_entries (key, entry_type, raw, summary, metadata, created_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			entry_type = excluded.entry_type,
			raw = excluded.raw,
			summary = excluded.summary,
			metadata = excluded.metadata,
			created_by = excluded.created_by,
			updated_at = excluded.updated_at
	`

	_, err := s.db.ExecContext(ctx, query,
		entry.Key,
		entry.EntryType,
		entry.Raw,
		nullIfEmpty(entry.Summary),
		nullIfEmpty(entry.Metadata),
		nullIfEmpty(entry.CreatedBy),
		entry.CreatedAt,
		entry.UpdatedAt,
	)
	if err != nil {
		return ScratchpadEntry{}, err
	}
	return entry, nil
}

// GetScratchpadEntry retrieves a scratchpad entry by key.
func (s *Store) GetScratchpadEntry(ctx context.Context, key string) (*ScratchpadEntry, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	if key == "" {
		return nil, fmt.Errorf("scratchpad key required")
	}

	query := `
		SELECT key, entry_type, raw, summary, metadata, created_by, created_at, updated_at
		FROM rlm_scratchpad_entries
		WHERE key = ?
	`

	var entry ScratchpadEntry
	var summary sql.NullString
	var metadata sql.NullString
	var createdBy sql.NullString
	row := s.db.QueryRowContext(ctx, query, key)
	if err := row.Scan(
		&entry.Key,
		&entry.EntryType,
		&entry.Raw,
		&summary,
		&metadata,
		&createdBy,
		&entry.CreatedAt,
		&entry.UpdatedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	entry.Summary = summary.String
	entry.Metadata = metadata.String
	entry.CreatedBy = createdBy.String
	return &entry, nil
}

// ListScratchpadEntries returns scratchpad entries ordered by creation time (descending).
func (s *Store) ListScratchpadEntries(ctx context.Context, limit int) ([]ScratchpadEntry, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}

	query := `
		SELECT key, entry_type, raw, summary, metadata, created_by, created_at, updated_at
		FROM rlm_scratchpad_entries
		ORDER BY created_at DESC
	`
	args := []any{}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := []ScratchpadEntry{}
	for rows.Next() {
		var entry ScratchpadEntry
		var summary sql.NullString
		var metadata sql.NullString
		var createdBy sql.NullString
		if err := rows.Scan(
			&entry.Key,
			&entry.EntryType,
			&entry.Raw,
			&summary,
			&metadata,
			&createdBy,
			&entry.CreatedAt,
			&entry.UpdatedAt,
		); err != nil {
			return nil, err
		}
		entry.Summary = summary.String
		entry.Metadata = metadata.String
		entry.CreatedBy = createdBy.String
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func ensureScratchpadSchema(db *sql.DB) error {
	queries := []string{
		`CREATE TABLE IF NOT EXISTS rlm_scratchpad_entries (
			key TEXT PRIMARY KEY,
			entry_type TEXT NOT NULL,
			raw BLOB,
			summary TEXT,
			metadata TEXT,
			created_by TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_rlm_scratchpad_type ON rlm_scratchpad_entries(entry_type);`,
		`CREATE INDEX IF NOT EXISTS idx_rlm_scratchpad_created ON rlm_scratchpad_entries(created_at);`,
	}
	for _, query := range queries {
		if _, err := db.Exec(query); err != nil {
			return err
		}
	}
	return nil
}
