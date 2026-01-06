package storage

import (
	"database/sql"
)

// SaveSessionToken stores or replaces the hashed session token for a session ID.
func (s *Store) SaveSessionToken(sessionID, token string) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	hash := hashSecret(token)
	_, err := s.db.Exec(`
        INSERT INTO session_tokens (session_id, token_hash, updated_at)
        VALUES (?, ?, CURRENT_TIMESTAMP)
        ON CONFLICT(session_id) DO UPDATE SET token_hash = excluded.token_hash, updated_at = CURRENT_TIMESTAMP
    `, sessionID, hash)
	return err
}

// ValidateSessionToken compares the provided token against the stored hash.
func (s *Store) ValidateSessionToken(sessionID, token string) (bool, error) {
	if s == nil || s.db == nil {
		return false, ErrStoreClosed
	}
	hash := hashSecret(token)
	var stored string
	err := s.db.QueryRow(`SELECT token_hash FROM session_tokens WHERE session_id = ?`, sessionID).Scan(&stored)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	return stored == hash, nil
}

// DeleteSessionToken removes any stored token for the session.
func (s *Store) DeleteSessionToken(sessionID string) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	_, err := s.db.Exec(`DELETE FROM session_tokens WHERE session_id = ?`, sessionID)
	return err
}
