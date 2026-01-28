package storage

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Message represents a conversation message stored for a session.
type Message struct {
	ID          int64     `json:"id"`
	SessionID   string    `json:"sessionId"`
	Role        string    `json:"role"`
	Content     string    `json:"content"`
	ContentJSON string    `json:"contentJson,omitempty"`
	ContentType string    `json:"contentType,omitempty"`
	Reasoning   string    `json:"reasoning,omitempty"` // Reasoning/thinking content for reasoning models
	Embedding   []byte    `json:"embedding,omitempty"`
	Timestamp   time.Time `json:"timestamp"`
	Tokens      int       `json:"tokens"`
	IsSummary   bool      `json:"isSummary"`
	IsTruncated bool      `json:"isTruncated"` // True if message was interrupted/incomplete
}

// Cursor represents a pagination cursor for efficient message retrieval.
// Uses ID and Timestamp for stable pagination even with concurrent writes.
type Cursor struct {
	ID        int64     `json:"id"`
	Timestamp time.Time `json:"timestamp"`
}

// SessionStats contains aggregated statistics for a session without loading all messages.
type SessionStats struct {
	SessionID     string    `json:"sessionId"`
	MessageCount  int       `json:"messageCount"`
	TotalTokens   int       `json:"totalTokens"`
	FirstMessage  time.Time `json:"firstMessage"`
	LastMessage   time.Time `json:"lastMessage"`
	RoleCounts    map[string]int `json:"roleCounts,omitempty"`
}

// stmtCache stores prepared statements for reuse across transactions.
// This reduces parsing overhead and improves query performance.
type stmtCache struct {
	mu    sync.RWMutex
	stmts map[string]*sql.Stmt
}

// getStmt retrieves or prepares a cached statement for the given query.
// Thread-safe: uses read lock for cache hits, write lock for cache misses.
func (s *Store) getStmt(query string) (*sql.Stmt, error) {
	s.stmtCache.mu.RLock()
	if stmt, ok := s.stmtCache.stmts[query]; ok {
		s.stmtCache.mu.RUnlock()
		return stmt, nil
	}
	s.stmtCache.mu.RUnlock()

	s.stmtCache.mu.Lock()
	defer s.stmtCache.mu.Unlock()

	// Double-check after acquiring write lock
	if stmt, ok := s.stmtCache.stmts[query]; ok {
		return stmt, nil
	}

	stmt, err := s.db.Prepare(query)
	if err != nil {
		return nil, fmt.Errorf("preparing statement: %w", err)
	}

	if s.stmtCache.stmts == nil {
		s.stmtCache.stmts = make(map[string]*sql.Stmt)
	}
	s.stmtCache.stmts[query] = stmt
	return stmt, nil
}

// clearStmtCache closes and removes all cached prepared statements.
// Should be called during store cleanup or when statements may become invalid.
func (s *Store) clearStmtCache() {
	s.stmtCache.mu.Lock()
	defer s.stmtCache.mu.Unlock()

	for _, stmt := range s.stmtCache.stmts {
		_ = stmt.Close()
	}
	s.stmtCache.stmts = make(map[string]*sql.Stmt)
}

// EncodeCursor encodes a cursor to a base64 string for API use.
func EncodeCursor(cursor *Cursor) string {
	if cursor == nil {
		return ""
	}
	data, _ := json.Marshal(cursor)
	return base64.URLEncoding.EncodeToString(data)
}

// DecodeCursor decodes a base64 string to a cursor.
func DecodeCursor(encoded string) (*Cursor, error) {
	if encoded == "" {
		return nil, nil
	}
	data, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decoding cursor: %w", err)
	}
	var cursor Cursor
	if err := json.Unmarshal(data, &cursor); err != nil {
		return nil, fmt.Errorf("parsing cursor: %w", err)
	}
	return &cursor, nil
}

// SaveMessage persists a message and updates aggregate session stats.
func (s *Store) SaveMessage(msg *Message) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	now := time.Now()
	insert := `
		INSERT INTO messages (session_id, role, content, content_json, content_type, reasoning, timestamp, tokens, is_summary, is_truncated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	result, err := tx.Exec(insert,
		msg.SessionID,
		msg.Role,
		msg.Content,
		nullIfEmpty(msg.ContentJSON),
		defaultContentType(msg.ContentType),
		nullIfEmpty(msg.Reasoning),
		msg.Timestamp,
		msg.Tokens,
		msg.IsSummary,
		msg.IsTruncated,
	)
	if err != nil {
		_ = tx.Rollback()
		return err
	}

	id, err := result.LastInsertId()
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	msg.ID = id

	update := `
		UPDATE sessions
		SET message_count = message_count + 1,
		    total_tokens = total_tokens + ?,
		    last_active = ?
		WHERE session_id = ?
	`
	if _, err := tx.Exec(update, msg.Tokens, now, msg.SessionID); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return err
	}

	msgCopy := *msg
	s.notify(newEvent(EventMessageCreated, msg.SessionID, msg.ID, msgCopy))
	s.notify(newEvent(EventSessionUpdated, msg.SessionID, msg.SessionID, map[string]any{
		"lastActive":    now,
		"messageDelta":  1,
		"tokensDelta":   msg.Tokens,
		"latestMessage": msgCopy,
	}))

	return nil
}

// ReplaceMessages replaces all messages for a session with the provided set.
func (s *Store) ReplaceMessages(sessionID string, messages []Message) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	if _, err = tx.Exec(`DELETE FROM messages WHERE session_id = ?`, sessionID); err != nil {
		_ = tx.Rollback()
		return err
	}

	stmt, err := tx.Prepare(`
		INSERT INTO messages (session_id, role, content, content_json, content_type, reasoning, timestamp, tokens, is_summary, is_truncated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()

	totalTokens := 0
	latest := time.Time{}

	for _, msg := range messages {
		ts := msg.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		if ts.After(latest) {
			latest = ts
		}
		totalTokens += msg.Tokens

		if _, err := stmt.Exec(
			sessionID,
			msg.Role,
			msg.Content,
			nullIfEmpty(msg.ContentJSON),
			defaultContentType(msg.ContentType),
			nullIfEmpty(msg.Reasoning),
			ts,
			msg.Tokens,
			msg.IsSummary,
			msg.IsTruncated,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}

	if latest.IsZero() {
		latest = time.Now()
	}

	// Update session stats within the same transaction for atomicity
	if _, err := tx.Exec(`
		UPDATE sessions
		SET message_count = ?, total_tokens = ?, last_active = ?
		WHERE session_id = ?
	`, len(messages), totalTokens, latest, sessionID); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	s.notify(newEvent(EventSessionUpdated, sessionID, sessionID, map[string]any{
		"messageCount": len(messages),
		"totalTokens":  totalTokens,
		"lastActive":   latest,
	}))

	return nil
}

// GetMessages retrieves messages for a session using limit/offset pagination.
//
// Recommended indexes for optimal performance:
//   CREATE INDEX idx_messages_session_time ON messages(session_id, timestamp);
//   CREATE INDEX idx_messages_session_role ON messages(session_id, role);
func (s *Store) GetMessages(sessionID string, limit int, offset int) ([]Message, error) {
	query := `
		SELECT id, session_id, role, content, content_json, content_type, reasoning, timestamp, tokens, is_summary, COALESCE(is_truncated, FALSE)
		FROM messages
		WHERE session_id = ?
		ORDER BY timestamp ASC
		LIMIT ? OFFSET ?
	`
	rows, err := s.db.Query(query, sessionID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	capHint := limit
	if capHint < 0 {
		capHint = 0
	}
	messages := make([]Message, 0, capHint)
	for rows.Next() {
		var msg Message
		var contentJSON sql.NullString
		var contentType sql.NullString
		var reasoning sql.NullString
		if err := rows.Scan(
			&msg.ID,
			&msg.SessionID,
			&msg.Role,
			&msg.Content,
			&contentJSON,
			&contentType,
			&reasoning,
			&msg.Timestamp,
			&msg.Tokens,
			&msg.IsSummary,
			&msg.IsTruncated,
		); err != nil {
			return nil, err
		}
		msg.ContentJSON = contentJSON.String
		msg.ContentType = defaultContentType(contentType.String)
		msg.Reasoning = reasoning.String
		messages = append(messages, msg)
	}

	return messages, rows.Err()
}

// GetMessagesWithCursor retrieves messages for a session using cursor-based pagination.
// More efficient than offset pagination for large datasets as it uses index seek.
// Pass nil cursor to get the first page.
//
// Recommended indexes for optimal performance:
//   CREATE INDEX idx_messages_session_time ON messages(session_id, timestamp);
func (s *Store) GetMessagesWithCursor(sessionID string, cursor *Cursor, limit int) ([]Message, *Cursor, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000 // Cap to prevent excessive memory usage
	}

	var query string
	var args []any

	if cursor == nil {
		// First page: no cursor filter
		query = `
			SELECT id, session_id, role, content, content_json, content_type, reasoning, timestamp, tokens, is_summary, COALESCE(is_truncated, FALSE)
			FROM messages
			WHERE session_id = ?
			ORDER BY timestamp ASC, id ASC
			LIMIT ?
		`
		args = []any{sessionID, limit + 1} // Request one extra to detect hasMore
	} else {
		// Subsequent pages: filter by cursor (timestamp, id)
		query = `
			SELECT id, session_id, role, content, content_json, content_type, reasoning, timestamp, tokens, is_summary, COALESCE(is_truncated, FALSE)
			FROM messages
			WHERE session_id = ? AND (timestamp > ? OR (timestamp = ? AND id > ?))
			ORDER BY timestamp ASC, id ASC
			LIMIT ?
		`
		args = []any{sessionID, cursor.Timestamp, cursor.Timestamp, cursor.ID, limit + 1}
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("querying messages with cursor: %w", err)
	}
	defer rows.Close()

	messages := make([]Message, 0, limit)
	var lastMsg Message
	count := 0

	for rows.Next() {
		var msg Message
		var contentJSON sql.NullString
		var contentType sql.NullString
		var reasoning sql.NullString
		if err := rows.Scan(
			&msg.ID,
			&msg.SessionID,
			&msg.Role,
			&msg.Content,
			&contentJSON,
			&contentType,
			&reasoning,
			&msg.Timestamp,
			&msg.Tokens,
			&msg.IsSummary,
			&msg.IsTruncated,
		); err != nil {
			return nil, nil, fmt.Errorf("scanning message: %w", err)
		}
		msg.ContentJSON = contentJSON.String
		msg.ContentType = defaultContentType(contentType.String)
		msg.Reasoning = reasoning.String

		if count < limit {
			messages = append(messages, msg)
		}
		lastMsg = msg
		count++
	}

	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterating messages: %w", err)
	}

	// Generate next cursor if there are more results
	var nextCursor *Cursor
	if count > limit {
		nextCursor = &Cursor{
			ID:        lastMsg.ID,
			Timestamp: lastMsg.Timestamp,
		}
	}

	return messages, nextCursor, nil
}

// GetAllMessages retrieves all messages for a session.
func (s *Store) GetAllMessages(sessionID string) ([]Message, error) {
	return s.GetMessages(sessionID, 999999, 0)
}

// GetMessagesWithSessions retrieves messages for multiple sessions in a single query.
// This eliminates N+1 query problems when loading sessions with their messages.
// Returns a map of sessionID -> messages for that session.
//
// Recommended indexes for optimal performance:
//   CREATE INDEX idx_messages_session_time ON messages(session_id, timestamp);
func (s *Store) GetMessagesWithSessions(sessionIDs []string) (map[string][]Message, error) {
	if len(sessionIDs) == 0 {
		return make(map[string][]Message), nil
	}

	// Build parameterized IN clause
	placeholders := make([]string, len(sessionIDs))
	args := make([]any, len(sessionIDs))
	for i, id := range sessionIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := fmt.Sprintf(`
		SELECT id, session_id, role, content, content_json, content_type, reasoning, timestamp, tokens, is_summary, COALESCE(is_truncated, FALSE)
		FROM messages
		WHERE session_id IN (%s)
		ORDER BY session_id, timestamp ASC
	`, strings.Join(placeholders, ","))

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying messages for sessions: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]Message)
	for _, id := range sessionIDs {
		result[id] = make([]Message, 0)
	}

	for rows.Next() {
		var msg Message
		var contentJSON sql.NullString
		var contentType sql.NullString
		var reasoning sql.NullString
		if err := rows.Scan(
			&msg.ID,
			&msg.SessionID,
			&msg.Role,
			&msg.Content,
			&contentJSON,
			&contentType,
			&reasoning,
			&msg.Timestamp,
			&msg.Tokens,
			&msg.IsSummary,
			&msg.IsTruncated,
		); err != nil {
			return nil, fmt.Errorf("scanning message: %w", err)
		}
		msg.ContentJSON = contentJSON.String
		msg.ContentType = defaultContentType(contentType.String)
		msg.Reasoning = reasoning.String
		result[msg.SessionID] = append(result[msg.SessionID], msg)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating messages: %w", err)
	}

	return result, nil
}

// GetSessionStats retrieves aggregated statistics for a session without loading all messages.
// This is useful for displaying session metadata in list views.
//
// Recommended indexes for optimal performance:
//   CREATE INDEX idx_messages_session_time ON messages(session_id, timestamp);
//   CREATE INDEX idx_messages_session_role ON messages(session_id, role);
func (s *Store) GetSessionStats(sessionID string) (*SessionStats, error) {
	query := `
		SELECT 
			COUNT(*) as message_count,
			COALESCE(SUM(tokens), 0) as total_tokens,
			MIN(timestamp) as first_message,
			MAX(timestamp) as last_message
		FROM messages
		WHERE session_id = ?
	`

	var stats SessionStats
	stats.SessionID = sessionID
	stats.RoleCounts = make(map[string]int)

	var firstMsgStr, lastMsgStr sql.NullString
	err := s.db.QueryRow(query, sessionID).Scan(
		&stats.MessageCount,
		&stats.TotalTokens,
		&firstMsgStr,
		&lastMsgStr,
	)
	if err != nil {
		return nil, fmt.Errorf("querying session stats: %w", err)
	}

	if firstMsgStr.Valid && firstMsgStr.String != "" {
		stats.FirstMessage = parseSQLiteTimestamp(firstMsgStr.String)
	}
	if lastMsgStr.Valid && lastMsgStr.String != "" {
		stats.LastMessage = parseSQLiteTimestamp(lastMsgStr.String)
	}

	// Get role distribution
	roleQuery := `
		SELECT role, COUNT(*) as count
		FROM messages
		WHERE session_id = ?
		GROUP BY role
	`
	roleRows, err := s.db.Query(roleQuery, sessionID)
	if err != nil {
		return nil, fmt.Errorf("querying role stats: %w", err)
	}
	defer roleRows.Close()

	for roleRows.Next() {
		var role string
		var count int
		if err := roleRows.Scan(&role, &count); err != nil {
			return nil, fmt.Errorf("scanning role stats: %w", err)
		}
		stats.RoleCounts[role] = count
	}

	if err := roleRows.Err(); err != nil {
		return nil, fmt.Errorf("iterating role stats: %w", err)
	}

	return &stats, nil
}

// GetLatestMessageByRole returns the most recent message for a role in a session.
func (s *Store) GetLatestMessageByRole(sessionID, role string) (*Message, error) {
	query := `
		SELECT id, session_id, role, content, content_json, content_type, reasoning, timestamp, tokens, is_summary, COALESCE(is_truncated, FALSE)
		FROM messages
		WHERE session_id = ? AND role = ?
		ORDER BY id DESC
		LIMIT 1
	`
	row := s.db.QueryRow(query, sessionID, role)

	var msg Message
	var contentJSON sql.NullString
	var contentType sql.NullString
	var reasoning sql.NullString
	if err := row.Scan(
		&msg.ID,
		&msg.SessionID,
		&msg.Role,
		&msg.Content,
		&contentJSON,
		&contentType,
		&reasoning,
		&msg.Timestamp,
		&msg.Tokens,
		&msg.IsSummary,
		&msg.IsTruncated,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	msg.ContentJSON = contentJSON.String
	msg.ContentType = defaultContentType(contentType.String)
	msg.Reasoning = reasoning.String

	return &msg, nil
}

// GetRecentMessagesByRole returns the most recent messages for a role across sessions.
func (s *Store) GetRecentMessagesByRole(role string, limit int) ([]Message, error) {
	query := `
		SELECT id, session_id, role, content, content_json, content_type, reasoning, timestamp, tokens, is_summary, COALESCE(is_truncated, FALSE)
		FROM messages
		WHERE role = ?
		ORDER BY timestamp DESC
		LIMIT ?
	`
	rows, err := s.db.Query(query, role, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		var contentJSON sql.NullString
		var contentType sql.NullString
		var reasoning sql.NullString
		if err := rows.Scan(
			&msg.ID,
			&msg.SessionID,
			&msg.Role,
			&msg.Content,
			&contentJSON,
			&contentType,
			&reasoning,
			&msg.Timestamp,
			&msg.Tokens,
			&msg.IsSummary,
			&msg.IsTruncated,
		); err != nil {
			return nil, err
		}
		msg.ContentJSON = contentJSON.String
		msg.ContentType = defaultContentType(contentType.String)
		msg.Reasoning = reasoning.String
		messages = append(messages, msg)
	}

	return messages, rows.Err()
}

// parseSQLiteTimestamp parses timestamps from SQLite which may be in various formats.
// SQLite stores timestamps in UTC by default but may return them in different formats.
// MIN/MAX aggregates can return Go's time.String() format when the column contains time.Time values.
func parseSQLiteTimestamp(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}

	// Try common SQLite timestamp formats first
	formats := []string{
		time.RFC3339,                    // 2006-01-02T15:04:05Z
		time.RFC3339Nano,                // 2006-01-02T15:04:05.999999999Z
		"2006-01-02 15:04:05",           // Basic SQLite format
		"2006-01-02 15:04:05.999999999", // With nanoseconds
		"2006-01-02T15:04:05",           // Date-only with T separator
	}

	for _, format := range formats {
		if ts, err := time.ParseInLocation(format, value, time.UTC); err == nil {
			return ts
		}
	}

	// Handle Go's time.String() format which may include monotonic clock reading
	// Format: "2006-01-02 15:04:05.999999999 -0700 MST m=+0.000000000"
	// Strip the monotonic clock reading (" m=+...") if present
	if idx := strings.Index(value, " m="); idx != -1 {
		value = value[:idx]
	}

	// Try parsing the time.String() format without monotonic reading
	if ts, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", value); err == nil {
		return ts
	}

	// Try without nanoseconds
	if ts, err := time.Parse("2006-01-02 15:04:05 -0700 MST", value); err == nil {
		return ts
	}

	return time.Time{}
}

func defaultContentType(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return "text"
	}
	return value
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// SaveMessagesBatch efficiently saves multiple messages in a single transaction.
// This reduces database round-trips compared to calling SaveMessage for each message.
func (s *Store) SaveMessagesBatch(messages []*Message) error {
	if len(messages) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin batch transaction: %w", err)
	}

	stmt, err := tx.Prepare(`
		INSERT INTO messages (session_id, role, content, content_json, content_type, reasoning, timestamp, tokens, is_summary, is_truncated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare batch insert: %w", err)
	}
	defer stmt.Close()

	now := time.Now()
	totalTokens := 0
	var sessionID string
	var latest time.Time

	for _, msg := range messages {
		if msg == nil {
			continue
		}
		if sessionID == "" {
			sessionID = msg.SessionID
		}
		if msg.Timestamp.IsZero() {
			msg.Timestamp = now
		}
		if msg.Timestamp.After(latest) {
			latest = msg.Timestamp
		}
		totalTokens += msg.Tokens

		result, err := stmt.Exec(
			msg.SessionID,
			msg.Role,
			msg.Content,
			nullIfEmpty(msg.ContentJSON),
			defaultContentType(msg.ContentType),
			nullIfEmpty(msg.Reasoning),
			msg.Timestamp,
			msg.Tokens,
			msg.IsSummary,
			msg.IsTruncated,
		)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("batch insert message: %w", err)
		}

		id, err := result.LastInsertId()
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("get last insert id: %w", err)
		}
		msg.ID = id
	}

	// Update session stats in a single query
	if sessionID != "" {
		update := `
			UPDATE sessions
			SET message_count = message_count + ?,
			    total_tokens = total_tokens + ?,
			    last_active = ?
			WHERE session_id = ?
		`
		if _, err := tx.Exec(update, len(messages), totalTokens, latest, sessionID); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("update session stats: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit batch transaction: %w", err)
	}

	// Notify after successful commit
	for _, msg := range messages {
		if msg == nil {
			continue
		}
		msgCopy := *msg
		s.notify(newEvent(EventMessageCreated, msg.SessionID, msg.ID, msgCopy))
	}

	if sessionID != "" {
		s.notify(newEvent(EventSessionUpdated, sessionID, sessionID, map[string]any{
			"lastActive":    now,
			"messageDelta":  len(messages),
			"tokensDelta":   totalTokens,
			"latestMessage": latest,
		}))
	}

	return nil
}
