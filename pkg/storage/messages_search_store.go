package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

func ensureMessagesSearchSchema(db *sql.DB) error {
	cols, err := messageColumns(db)
	if err != nil {
		return err
	}
	if len(cols) == 0 {
		// Table doesn't exist yet; schema.sql will create it.
		return nil
	}

	if !cols["embedding"] {
		if _, err := db.Exec(`ALTER TABLE messages ADD COLUMN embedding BLOB`); err != nil {
			return fmt.Errorf("add messages.embedding: %w", err)
		}
	}

	if err := ensureMessagesFTS(db); err != nil {
		return err
	}

	return nil
}

func messageColumns(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(messages)`)
	if err != nil {
		return nil, fmt.Errorf("messages pragma: %w", err)
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
			return nil, fmt.Errorf("scan messages pragma: %w", err)
		}
		cols[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return cols, nil
}

func ensureMessagesFTS(db *sql.DB) error {
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
		content,
		content='messages',
		content_rowid='id'
	)`); err != nil {
		return fmt.Errorf("create messages_fts: %w", err)
	}

	if _, err := db.Exec(`CREATE TRIGGER IF NOT EXISTS messages_ai AFTER INSERT ON messages BEGIN
		INSERT INTO messages_fts(rowid, content) VALUES (new.id, COALESCE(new.content, ''));
	END;`); err != nil {
		return fmt.Errorf("create messages_ai trigger: %w", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER IF NOT EXISTS messages_ad AFTER DELETE ON messages BEGIN
		INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, COALESCE(old.content, ''));
	END;`); err != nil {
		return fmt.Errorf("create messages_ad trigger: %w", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER IF NOT EXISTS messages_au AFTER UPDATE ON messages BEGIN
		INSERT INTO messages_fts(messages_fts, rowid, content) VALUES ('delete', old.id, COALESCE(old.content, ''));
		INSERT INTO messages_fts(rowid, content) VALUES (new.id, COALESCE(new.content, ''));
	END;`); err != nil {
		return fmt.Errorf("create messages_au trigger: %w", err)
	}

	if _, err := db.Exec(`INSERT INTO messages_fts(messages_fts) VALUES('rebuild')`); err != nil {
		return fmt.Errorf("rebuild messages_fts: %w", err)
	}

	return nil
}
