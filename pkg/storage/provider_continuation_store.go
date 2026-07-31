package storage

import (
	"database/sql"
	"fmt"
	"strings"
)

// LoadProviderContinuation returns the persisted provider continuation state
// JSON associated with a Buckley session, provider, and model. It returns an
// empty string when no state has been saved.
func (s *Store) LoadProviderContinuation(sessionID, providerID, modelID string) (string, error) {
	var state string
	err := s.db.QueryRow(
		`SELECT state FROM provider_continuations WHERE session_id = ? AND provider_id = ? AND model_id = ?`,
		strings.TrimSpace(sessionID), strings.TrimSpace(providerID), strings.TrimSpace(modelID),
	).Scan(&state)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load provider continuation: %w", err)
	}
	return state, nil
}

// SaveProviderContinuation records the latest provider continuation state
// JSON for a Buckley session, provider, and model.
func (s *Store) SaveProviderContinuation(sessionID, providerID, modelID, state string) error {
	_, err := s.db.Exec(`
		INSERT INTO provider_continuations (session_id, provider_id, model_id, state, updated_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(session_id, provider_id, model_id) DO UPDATE SET
			state = excluded.state,
			updated_at = CURRENT_TIMESTAMP`,
		strings.TrimSpace(sessionID), strings.TrimSpace(providerID), strings.TrimSpace(modelID), state,
	)
	if err != nil {
		return fmt.Errorf("save provider continuation: %w", err)
	}
	return nil
}

// DeleteProviderContinuation clears persisted continuation state for a
// session and provider, across every model. Callers use this on provider or
// model change so a stale continuation cannot be replayed against the wrong
// window (decision 0001).
func (s *Store) DeleteProviderContinuation(sessionID, providerID string) error {
	_, err := s.db.Exec(
		`DELETE FROM provider_continuations WHERE session_id = ? AND provider_id = ?`,
		strings.TrimSpace(sessionID), strings.TrimSpace(providerID),
	)
	if err != nil {
		return fmt.Errorf("delete provider continuation: %w", err)
	}
	return nil
}
