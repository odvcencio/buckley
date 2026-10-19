package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
)

var ErrSessionMemoryBudget = errors.New("session working history exceeds memory budget")

// MessageWindow is a bounded view of canonical history. BeforeID identifies the
// stored prefix that a working-view edit must preserve.
type MessageWindow struct {
	Messages     []Message
	BeforeID     int64
	SummaryCount int
}

// GetMessageWindow selects sizes before fetching bodies, retaining the original
// prompt/task, the latest summary and a recent tail. Historical bodies are never
// materialized just to discard them. A small message-count ceiling also bounds
// decoded-object overhead for histories containing millions of tiny messages.
func (s *Store) GetMessageWindow(ctx context.Context, sessionID string, maxBytes int) (MessageWindow, error) {
	if maxBytes <= 0 {
		messages, err := s.GetAllMessages(sessionID)
		window := MessageWindow{Messages: messages}
		for _, msg := range messages {
			if msg.IsSummary {
				window.SummaryCount++
			}
		}
		return window, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return MessageWindow{}, err
	}
	defer tx.Rollback()
	var summaryCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE session_id = ? AND is_summary = 1`, sessionID).Scan(&summaryCount); err != nil {
		return MessageWindow{}, err
	}
	const cost = `512 + 2 * (length(CAST(content AS BLOB)) +
		length(CAST(COALESCE(content_json, '') AS BLOB)) + length(CAST(COALESCE(tool_calls, '') AS BLOB)) +
		length(CAST(COALESCE(reasoning, '') AS BLOB)) + length(CAST(COALESCE(reasoning_details, '') AS BLOB)) +
		length(CAST(COALESCE(tool_call_id, '') AS BLOB)) + length(CAST(COALESCE(name, '') AS BLOB)))`
	rows, err := tx.QueryContext(ctx, `SELECT id, `+cost+` FROM messages WHERE session_id = ? AND ((role = 'system' AND is_summary = 0) OR id IN (
		(SELECT MIN(id) FROM messages WHERE session_id = ? AND role = 'user'),
		(SELECT MAX(id) FROM messages WHERE session_id = ? AND is_summary = 1))) ORDER BY id LIMIT 257`,
		sessionID, sessionID, sessionID)
	if err != nil {
		return MessageWindow{}, err
	}
	ids := make([]int64, 0, 256)
	used := 2048 // the omission marker and its decoded representation
	for rows.Next() {
		var id int64
		var bytes int
		if err := rows.Scan(&id, &bytes); err != nil {
			rows.Close()
			return MessageWindow{}, err
		}
		if len(ids) >= 256 || bytes > maxBytes-used {
			rows.Close()
			return MessageWindow{}, fmt.Errorf("%w: prompt/task/summary requires more than %d bytes", ErrSessionMemoryBudget, maxBytes)
		}
		ids = append(ids, id)
		used += bytes
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return MessageWindow{}, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id, `+cost+` FROM messages WHERE session_id = ? ORDER BY id DESC LIMIT 257`, sessionID)
	if err != nil {
		return MessageWindow{}, err
	}
	var beforeID int64
	var firstTailID int64
	var latestID int64
	for rows.Next() {
		var id int64
		var bytes int
		if err := rows.Scan(&id, &bytes); err != nil {
			rows.Close()
			return MessageWindow{}, err
		}
		if latestID == 0 {
			latestID = id
		}
		if slices.Contains(ids, id) {
			continue
		}
		if len(ids) >= 256 || bytes > maxBytes-used {
			if firstTailID == 0 {
				if slices.Contains(ids, latestID) {
					beforeID = latestID
					break
				}
				rows.Close()
				return MessageWindow{}, fmt.Errorf("%w: latest message requires %d bytes; budget %d", ErrSessionMemoryBudget, bytes, maxBytes)
			}
			beforeID = firstTailID - 1
			break
		}
		ids = append(ids, id)
		firstTailID = id
		used += bytes
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return MessageWindow{}, err
	}
	if len(ids) == 0 {
		return MessageWindow{}, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, sessionID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err = tx.QueryContext(ctx, `SELECT id, session_id, role, content, content_json, content_type,
		tool_calls, tool_call_id, name, reasoning, reasoning_details, timestamp, tokens, is_summary, COALESCE(is_truncated, FALSE)
		FROM messages WHERE session_id = ? AND id IN (`+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) ORDER BY id`, args...)
	if err != nil {
		return MessageWindow{}, err
	}
	messages, err := scanMessages(rows)
	rows.Close()
	if err != nil {
		return MessageWindow{}, err
	}
	if err := tx.Commit(); err != nil {
		return MessageWindow{}, err
	}
	return MessageWindow{Messages: messages, BeforeID: beforeID, SummaryCount: summaryCount}, nil
}
