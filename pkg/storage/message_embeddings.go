package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// SaveMessageEmbedding updates a message row with its embedding bytes.
func (s *Store) SaveMessageEmbedding(ctx context.Context, messageID int64, embedding []byte) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	if messageID <= 0 {
		return fmt.Errorf("message id required")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET embedding = ? WHERE id = ?`, embedding, messageID)
	return err
}

// GetMessagesWithEmbeddings returns messages with stored embeddings.
func (s *Store) GetMessagesWithEmbeddings(ctx context.Context, sessionID string) ([]Message, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	query := `
		SELECT id, session_id, role, content, content_json, content_type, reasoning, timestamp, tokens, is_summary, COALESCE(is_truncated, FALSE), embedding
		FROM messages
		WHERE session_id = ? AND embedding IS NOT NULL
		ORDER BY timestamp ASC
	`
	rows, err := s.db.QueryContext(ctx, query, sessionID)
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
		var embedding []byte
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
			&embedding,
		); err != nil {
			return nil, err
		}
		msg.ContentJSON = contentJSON.String
		msg.ContentType = defaultContentType(contentType.String)
		msg.Reasoning = reasoning.String
		msg.Embedding = embedding
		messages = append(messages, msg)
	}

	return messages, rows.Err()
}

// GetMessagesMissingEmbeddings returns messages without embeddings for a session.
func (s *Store) GetMessagesMissingEmbeddings(ctx context.Context, sessionID string, limit int) ([]Message, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}
	query := `
		SELECT id, session_id, role, content, content_json, content_type, reasoning, timestamp, tokens, is_summary, COALESCE(is_truncated, FALSE)
		FROM messages
		WHERE session_id = ? AND embedding IS NULL
		ORDER BY timestamp ASC
		LIMIT ?
	`
	rows, err := s.db.QueryContext(ctx, query, sessionID, limit)
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

// SearchMessagesFTS runs a full-text search query against messages.
func (s *Store) SearchMessagesFTS(ctx context.Context, query, sessionID string, limit int) ([]MessageSearchResult, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	sessionID = strings.TrimSpace(sessionID)

	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.session_id, m.role, m.content, m.content_json, m.content_type, m.reasoning, m.timestamp, m.tokens, m.is_summary, COALESCE(m.is_truncated, FALSE),
			snippet(messages_fts, 0, '', '', '...', 12) AS snippet,
			bm25(messages_fts) AS rank
		FROM messages_fts
		JOIN messages m ON messages_fts.rowid = m.id
		WHERE messages_fts MATCH ?
			AND (? = '' OR m.session_id = ?)
		ORDER BY rank
		LIMIT ?
	`, query, sessionID, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []MessageSearchResult
	for rows.Next() {
		var msg Message
		var contentJSON sql.NullString
		var contentType sql.NullString
		var reasoning sql.NullString
		var snippet sql.NullString
		var rank float64
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
			&snippet,
			&rank,
		); err != nil {
			return nil, err
		}
		msg.ContentJSON = contentJSON.String
		msg.ContentType = defaultContentType(contentType.String)
		msg.Reasoning = reasoning.String
		score := ftsScore(rank)
		results = append(results, MessageSearchResult{
			Message: msg,
			Snippet: strings.TrimSpace(snippet.String),
			Score:   score,
		})
	}

	return results, rows.Err()
}

// MessageSearchResult captures a full-text search hit.
type MessageSearchResult struct {
	Message Message
	Snippet string
	Score   float64
}

func ftsScore(rank float64) float64 {
	if rank <= 0 {
		return 1
	}
	return 1 / (1 + rank)
}
