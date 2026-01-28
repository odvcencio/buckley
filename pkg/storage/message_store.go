package storage

import (
	"database/sql"
	"strings"
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

// GetAllMessages retrieves all messages for a session.
func (s *Store) GetAllMessages(sessionID string) ([]Message, error) {
	return s.GetMessages(sessionID, 999999, 0)
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
