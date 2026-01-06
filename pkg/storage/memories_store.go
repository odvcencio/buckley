package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

func ensureMemoriesSchema(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(memories)`)
	if err != nil {
		return fmt.Errorf("memories pragma: %w", err)
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
			return fmt.Errorf("scan memories pragma: %w", err)
		}
		cols[strings.ToLower(name)] = true
	}

	if err := rows.Err(); err != nil {
		return err
	}

	if len(cols) == 0 {
		// Table doesn't exist yet; schema.sql will create it.
		return nil
	}

	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_memories_session ON memories(session_id)`); err != nil {
		return fmt.Errorf("ensure idx_memories_session: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_memories_created ON memories(created_at)`); err != nil {
		return fmt.Errorf("ensure idx_memories_created: %w", err)
	}

	return nil
}
