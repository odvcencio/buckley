package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// LinkSessionToPlan associates a session with the latest active plan so
// dashboards can hydrate plan state without waiting for telemetry events.
func (s *Store) LinkSessionToPlan(sessionID, planID string) error {
	sessionID = strings.TrimSpace(sessionID)
	planID = strings.TrimSpace(planID)
	if sessionID == "" || planID == "" {
		return fmt.Errorf("session and plan ids are required")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to start feature session tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM feature_sessions WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("failed to clear previous feature session: %w", err)
	}

	if _, err := tx.Exec(`INSERT INTO feature_sessions (session_id, plan_id) VALUES (?, ?)`, sessionID, planID); err != nil {
		return fmt.Errorf("failed to link session to plan: %w", err)
	}

	return tx.Commit()
}

// GetSessionPlanID returns the most recent plan attached to the session, or empty string if none.
func (s *Store) GetSessionPlanID(sessionID string) (string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return "", fmt.Errorf("session id required")
	}

	var planID sql.NullString
	err := s.db.QueryRow(`
		SELECT plan_id FROM feature_sessions
		WHERE session_id = ?
		ORDER BY created_at DESC
		LIMIT 1
	`, sessionID).Scan(&planID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("failed to fetch session plan: %w", err)
	}

	if !planID.Valid {
		return "", nil
	}
	return planID.String, nil
}

func (s *Store) ListPlanIDsForPrincipal(principal string) (map[string]struct{}, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	principal = strings.TrimSpace(principal)
	if principal == "" {
		return map[string]struct{}{}, nil
	}
	rows, err := s.db.Query(`
		SELECT DISTINCT fs.plan_id
		FROM feature_sessions fs
		JOIN sessions s ON s.session_id = fs.session_id
		WHERE LOWER(TRIM(COALESCE(s.principal, ''))) = LOWER(TRIM(?))
	`, principal)
	if err != nil {
		return nil, fmt.Errorf("list plan ids: %w", err)
	}
	defer rows.Close()

	ids := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan plan id: %w", err)
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		ids[id] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list plan ids: %w", err)
	}
	return ids, nil
}

func (s *Store) PrincipalHasPlan(principal, planID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, ErrStoreClosed
	}
	principal = strings.TrimSpace(principal)
	planID = strings.TrimSpace(planID)
	if principal == "" || planID == "" {
		return false, nil
	}
	var exists int
	err := s.db.QueryRow(`
		SELECT 1
		FROM feature_sessions fs
		JOIN sessions s ON s.session_id = fs.session_id
		WHERE fs.plan_id = ?
		  AND LOWER(TRIM(COALESCE(s.principal, ''))) = LOWER(TRIM(?))
		LIMIT 1
	`, planID, principal).Scan(&exists)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("check plan access: %w", err)
	}
	return true, nil
}
