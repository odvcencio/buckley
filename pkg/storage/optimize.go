package storage

import (
	"database/sql"
	"fmt"
)

// PreparedStatements holds pre-compiled SQL statements for performance
type PreparedStatements struct {
	insertMessage    *sql.Stmt
	getMessage       *sql.Stmt
	listMessages     *sql.Stmt
	insertAPICall    *sql.Stmt
	updateSession    *sql.Stmt
	getSession       *sql.Stmt
	insertEmbedding  *sql.Stmt
	searchEmbeddings *sql.Stmt
}

// PrepareStatements pre-compiles common SQL statements
func (s *Store) PrepareStatements() (*PreparedStatements, error) {
	ps := &PreparedStatements{}

	var err error

	// Insert message (most common operation)
	ps.insertMessage, err = s.db.Prepare(`
		INSERT INTO messages (session_id, role, content, timestamp, tokens, is_summary)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare insertMessage: %w", err)
	}

	// Get single message
	ps.getMessage, err = s.db.Prepare(`
		SELECT id, session_id, role, content, timestamp, tokens, is_summary
		FROM messages
		WHERE id = ?
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare getMessage: %w", err)
	}

	// List messages for session
	ps.listMessages, err = s.db.Prepare(`
		SELECT id, session_id, role, content, timestamp, tokens, is_summary
		FROM messages
		WHERE session_id = ?
		ORDER BY timestamp ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare listMessages: %w", err)
	}

	// Insert API call
	ps.insertAPICall, err = s.db.Prepare(`
		INSERT INTO api_calls (session_id, model, prompt_tokens, completion_tokens, cost, timestamp)
		VALUES (?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare insertAPICall: %w", err)
	}

	// Update session activity
	ps.updateSession, err = s.db.Prepare(`
		UPDATE sessions
		SET last_active = ?, message_count = message_count + 1
		WHERE session_id = ?
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare updateSession: %w", err)
	}

	// Get session
	ps.getSession, err = s.db.Prepare(`
		SELECT session_id, project_path, git_repo, git_branch, created_at, last_active
		FROM sessions
		WHERE session_id = ?
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare getSession: %w", err)
	}

	// Insert embedding (upsert by file path)
	ps.insertEmbedding, err = s.db.Prepare(`
		INSERT INTO embeddings (file_path, content_hash, content, embedding, metadata, source_mod_time, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(file_path) DO UPDATE SET
			content_hash=excluded.content_hash,
			content=excluded.content,
			embedding=excluded.embedding,
			metadata=excluded.metadata,
			source_mod_time=excluded.source_mod_time,
			created_at=excluded.created_at
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare insertEmbedding: %w", err)
	}

	// Search embeddings
	ps.searchEmbeddings, err = s.db.Prepare(`
		SELECT file_path, content_hash, content, embedding, metadata, source_mod_time
		FROM embeddings
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare searchEmbeddings: %w", err)
	}

	return ps, nil
}

// Close closes all prepared statements
func (ps *PreparedStatements) Close() error {
	statements := []*sql.Stmt{
		ps.insertMessage,
		ps.getMessage,
		ps.listMessages,
		ps.insertAPICall,
		ps.updateSession,
		ps.getSession,
		ps.insertEmbedding,
		ps.searchEmbeddings,
	}

	for _, stmt := range statements {
		if stmt != nil {
			if err := stmt.Close(); err != nil {
				return err
			}
		}
	}

	return nil
}

// OptimizeDatabase adds indices and optimizes database settings
func (s *Store) OptimizeDatabase() error {
	optimizations := []string{
		// Indices for common queries
		`CREATE INDEX IF NOT EXISTS idx_messages_session_timestamp
		 ON messages(session_id, timestamp)`,

		`CREATE INDEX IF NOT EXISTS idx_api_calls_session_timestamp
		 ON api_calls(session_id, timestamp)`,

		`CREATE INDEX IF NOT EXISTS idx_sessions_last_active
		 ON sessions(last_active DESC)`,

		`CREATE INDEX IF NOT EXISTS idx_embeddings_hash
		 ON embeddings(content_hash)`,

		// SQLite performance optimizations
		`PRAGMA journal_mode = WAL`,    // Write-Ahead Logging for better concurrency
		`PRAGMA synchronous = NORMAL`,  // Faster writes, still safe
		`PRAGMA cache_size = -64000`,   // 64MB cache
		`PRAGMA temp_store = MEMORY`,   // Use memory for temp tables
		`PRAGMA mmap_size = 268435456`, // 256MB memory-mapped I/O
	}

	for _, sql := range optimizations {
		if _, err := s.db.Exec(sql); err != nil {
			return fmt.Errorf("failed to apply optimization %q: %w", sql, err)
		}
	}

	return nil
}

// VacuumDatabase performs maintenance to reclaim space and optimize
func (s *Store) VacuumDatabase() error {
	_, err := s.db.Exec("VACUUM")
	return err
}

// AnalyzeDatabase updates query planner statistics
func (s *Store) AnalyzeDatabase() error {
	_, err := s.db.Exec("ANALYZE")
	return err
}

// GetDatabaseStats returns database statistics
func (s *Store) GetDatabaseStats() (map[string]any, error) {
	stats := make(map[string]any)

	// Count tables
	queries := map[string]string{
		"sessions":   "SELECT COUNT(*) FROM sessions",
		"messages":   "SELECT COUNT(*) FROM messages",
		"api_calls":  "SELECT COUNT(*) FROM api_calls",
		"embeddings": "SELECT COUNT(*) FROM embeddings",
	}

	for name, query := range queries {
		var count int
		if err := s.db.QueryRow(query).Scan(&count); err != nil {
			return nil, err
		}
		stats[name+"_count"] = count
	}

	// Database size
	var pageCount, pageSize int
	if err := s.db.QueryRow("PRAGMA page_count").Scan(&pageCount); err != nil {
		return nil, err
	}
	if err := s.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		return nil, err
	}

	stats["size_bytes"] = pageCount * pageSize
	stats["size_mb"] = float64(pageCount*pageSize) / (1024 * 1024)

	return stats, nil
}
