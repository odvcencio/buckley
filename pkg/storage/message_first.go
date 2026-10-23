package storage

import "fmt"

// GetFirstMessageByRole returns the first appended message for a role in a session.
// It returns nil when the session has no message with that role.
func (s *Store) GetFirstMessageByRole(sessionID, role string) (*Message, error) {
	rows, err := s.db.Query(`
		SELECT id, session_id, role, content, content_json, content_type, tool_calls, tool_call_id, name, reasoning, reasoning_details, timestamp, tokens, is_summary, COALESCE(is_truncated, FALSE)
		FROM messages
		WHERE session_id = ? AND role = ?
		ORDER BY id
		LIMIT 1
	`, sessionID, role)
	if err != nil {
		return nil, fmt.Errorf("querying first message by role: %w", err)
	}
	defer rows.Close()

	messages, err := scanMessages(rows)
	if err != nil {
		return nil, fmt.Errorf("reading first message by role: %w", err)
	}
	if len(messages) == 0 {
		return nil, nil
	}
	return &messages[0], nil
}
