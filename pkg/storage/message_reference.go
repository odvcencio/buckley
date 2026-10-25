package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// WorkingSetReadBytes matches the conservative body cost used before materializing
// a message window or restoring an exact working-set reference.
func (m Message) WorkingSetReadBytes() int {
	return 512 + 2*(len(m.Content)+len(m.ContentJSON)+len(m.ToolCalls)+len(m.Reasoning)+len(m.ReasoningDetails)+len(m.ToolCallID)+len(m.Name))
}

// GetWorkingSetMessages restores at most 256 exact records in the requested order.
// Count and byte admission share the same read snapshot as the bodies.
func (s *Store) GetWorkingSetMessages(ctx context.Context, sessionID string, ids []int64, maxBytes int) ([]Message, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > 256 || maxBytes <= 0 {
		return nil, fmt.Errorf("%w: invalid working-set reference budget", ErrSessionMemoryBudget)
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, sessionID)
	for _, id := range ids {
		args = append(args, id)
	}
	where := ` FROM messages WHERE session_id = ? AND id IN (` + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + `)`
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var count, bytes int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(`+workingSetReadCostSQL+`), 0)`+where, args...).Scan(&count, &bytes); err != nil {
		return nil, err
	}
	if count != len(ids) {
		return nil, fmt.Errorf("working-set records missing or duplicated for session %s", sessionID)
	}
	if bytes > maxBytes {
		return nil, fmt.Errorf("%w: referenced records require %d bytes; budget %d", ErrSessionMemoryBudget, bytes, maxBytes)
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, session_id, role, content, content_json, content_type,
		tool_calls, tool_call_id, name, reasoning, reasoning_details, timestamp, tokens, is_summary, COALESCE(is_truncated, FALSE)`+where, args...)
	if err != nil {
		return nil, err
	}
	messages, err := scanMessages(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	byID := make(map[int64]Message, len(messages))
	for _, msg := range messages {
		byID[msg.ID] = msg
	}
	ordered := make([]Message, len(ids))
	for i, id := range ids {
		ordered[i] = byID[id]
	}
	return ordered, nil
}
