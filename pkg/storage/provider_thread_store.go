package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

// LoadProviderThread returns the native provider thread associated with a Buckley session.
func (s *Store) LoadProviderThread(sessionID, providerID string) (string, error) {
	var threadID string
	err := s.db.QueryRow(
		`SELECT thread_id FROM provider_threads WHERE session_id = ? AND provider_id = ?`,
		strings.TrimSpace(sessionID), strings.TrimSpace(providerID),
	).Scan(&threadID)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load provider thread: %w", err)
	}
	return threadID, nil
}

// SaveProviderThread records the native provider thread associated with a Buckley session.
func (s *Store) SaveProviderThread(sessionID, providerID, threadID string) error {
	_, err := s.db.Exec(`
		INSERT INTO provider_threads (session_id, provider_id, thread_id, updated_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(session_id, provider_id) DO UPDATE SET
			thread_id = excluded.thread_id,
			updated_at = CURRENT_TIMESTAMP`,
		strings.TrimSpace(sessionID), strings.TrimSpace(providerID), strings.TrimSpace(threadID),
	)
	if err != nil {
		return fmt.Errorf("save provider thread: %w", err)
	}
	return nil
}

// DeleteProviderThread clears a stale native provider thread association.
func (s *Store) DeleteProviderThread(sessionID, providerID string) error {
	_, err := s.db.Exec(
		`DELETE FROM provider_threads WHERE session_id = ? AND provider_id = ?`,
		strings.TrimSpace(sessionID), strings.TrimSpace(providerID),
	)
	if err != nil {
		return fmt.Errorf("delete provider thread: %w", err)
	}
	return nil
}
