package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

func ensureEmbeddingsSchema(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(embeddings)`)
	if err != nil {
		return fmt.Errorf("embeddings pragma: %w", err)
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
			return fmt.Errorf("scan embeddings pragma: %w", err)
		}
		cols[strings.ToLower(name)] = true
	}

	if err := rows.Err(); err != nil {
		return err
	}

	if len(cols) == 0 {
		// Table doesn't exist yet; schema.sql will create it
		return nil
	}

	hasFilePath := cols["file_path"]
	hasSourceMod := cols["source_mod_time"]

	if !hasFilePath || !hasSourceMod {
		if err := migrateLegacyEmbeddings(db, hasFilePath, hasSourceMod); err != nil {
			return err
		}
		hasFilePath = true
		// hasSourceMod is true after migration but not needed further
	}

	if hasFilePath {
		if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_embeddings_file_path ON embeddings(file_path)`); err != nil {
			return fmt.Errorf("ensure idx_embeddings_file_path: %w", err)
		}
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_embeddings_hash ON embeddings(content_hash)`); err != nil {
		return fmt.Errorf("ensure idx_embeddings_hash: %w", err)
	}

	return nil
}

func migrateLegacyEmbeddings(db *sql.DB, hasFilePath, hasSourceMod bool) (err error) {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin embeddings migration: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		} else {
			err = tx.Commit()
		}
	}()

	if !hasFilePath {
		if _, err = tx.Exec(`ALTER TABLE embeddings ADD COLUMN file_path TEXT`); err != nil {
			return fmt.Errorf("add embeddings.file_path: %w", err)
		}
		if _, err = tx.Exec(`UPDATE embeddings SET file_path = 'legacy/' || id WHERE file_path IS NULL OR file_path = ''`); err != nil {
			return fmt.Errorf("backfill embeddings.file_path: %w", err)
		}
	}

	if !hasSourceMod {
		if _, err = tx.Exec(`ALTER TABLE embeddings ADD COLUMN source_mod_time TIMESTAMP`); err != nil {
			return fmt.Errorf("add embeddings.source_mod_time: %w", err)
		}
		if _, err = tx.Exec(`UPDATE embeddings SET source_mod_time = created_at WHERE source_mod_time IS NULL`); err != nil {
			return fmt.Errorf("backfill embeddings.source_mod_time: %w", err)
		}
	}

	return nil
}

func normalizeEmbeddingsPath(path string) string {
	if path == "" {
		return ""
	}
	cleaned := filepath.Clean(path)
	return filepath.ToSlash(cleaned)
}

func extractFilePathFromMetadata(meta string) string {
	if strings.TrimSpace(meta) == "" {
		return ""
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(meta), &data); err != nil {
		return ""
	}
	if value, ok := data["file"].(string); ok {
		return value
	}
	return ""
}
