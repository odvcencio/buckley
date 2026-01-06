package storage

import (
	"fmt"
	"strings"
)

// SaveSessionSummary stores a compacted session summary for cross-instance awareness.
func (s *Store) SaveSessionSummary(sessionID, summary string) error {
	if s == nil {
		return ErrStoreClosed
	}
	key := fmt.Sprintf("session.%s.summary", sessionID)
	return s.SetSetting(key, summary)
}

// GetSessionSummary retrieves the last stored summary for a session.
func (s *Store) GetSessionSummary(sessionID string) (string, error) {
	if s == nil {
		return "", ErrStoreClosed
	}
	key := fmt.Sprintf("session.%s.summary", sessionID)
	settings, err := s.GetSettings([]string{key})
	if err != nil {
		return "", err
	}
	return settings[key], nil
}

// ListSessionSummaries returns summaries for all sessions with stored summaries.
func (s *Store) ListSessionSummaries() (map[string]string, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	rows, err := s.db.Query(`SELECT key, value FROM settings WHERE key LIKE 'session.%.summary'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		parts := strings.Split(key, ".")
		if len(parts) >= 3 {
			sessionID := parts[1]
			result[sessionID] = value
		}
	}
	return result, rows.Err()
}
