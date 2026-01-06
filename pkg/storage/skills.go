package storage

import (
	"database/sql"
	"fmt"
	"time"
)

// SessionSkill represents an active skill in a session
type SessionSkill struct {
	ID            int64      `json:"id"`
	SessionID     string     `json:"sessionId"`
	SkillName     string     `json:"skillName"`
	ActivatedAt   time.Time  `json:"activatedAt"`
	ActivatedBy   string     `json:"activatedBy"` // "model"|"user"|"phase"
	Scope         string     `json:"scope"`
	IsActive      bool       `json:"isActive"`
	DeactivatedAt *time.Time `json:"deactivatedAt,omitempty"`
}

// SaveSessionSkill saves or updates a skill activation in the database
func (s *Store) SaveSessionSkill(sessionID, skillName, activatedBy, scope string) error {
	query := `
		INSERT INTO session_skills (session_id, skill_name, activated_by, scope, is_active)
		VALUES (?, ?, ?, ?, TRUE)
		ON CONFLICT(session_id, skill_name) DO UPDATE SET
			is_active = TRUE,
			activated_at = CURRENT_TIMESTAMP,
			activated_by = excluded.activated_by,
			scope = excluded.scope,
			deactivated_at = NULL
	`

	_, err := s.db.Exec(query, sessionID, skillName, activatedBy, scope)
	if err != nil {
		return fmt.Errorf("failed to save session skill: %w", err)
	}

	s.notify(newEvent(EventSkillActivated, sessionID, skillName, map[string]any{
		"skillName":   skillName,
		"activatedBy": activatedBy,
		"scope":       scope,
		"isActive":    true,
		"activatedAt": time.Now().UTC(),
	}))

	return nil
}

// DeactivateSessionSkill marks a skill as inactive for a session
func (s *Store) DeactivateSessionSkill(sessionID, skillName string) error {
	query := `
		UPDATE session_skills
		SET is_active = FALSE, deactivated_at = CURRENT_TIMESTAMP
		WHERE session_id = ? AND skill_name = ? AND is_active = TRUE
	`

	result, err := s.db.Exec(query, sessionID, skillName)
	if err != nil {
		return fmt.Errorf("failed to deactivate session skill: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to check rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("skill %s not found or already inactive", skillName)
	}

	s.notify(newEvent(EventSkillDeactivated, sessionID, skillName, map[string]any{
		"skillName":     skillName,
		"isActive":      false,
		"deactivatedAt": time.Now().UTC(),
	}))

	return nil
}

// GetActiveSessionSkills retrieves all active skills for a session
func (s *Store) GetActiveSessionSkills(sessionID string) ([]SessionSkill, error) {
	query := `
		SELECT id, session_id, skill_name, activated_at, activated_by, scope, is_active, deactivated_at
		FROM session_skills
		WHERE session_id = ? AND is_active = TRUE
		ORDER BY activated_at ASC
	`

	rows, err := s.db.Query(query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to query active session skills: %w", err)
	}
	defer rows.Close()

	var skills []SessionSkill
	for rows.Next() {
		var skill SessionSkill
		var deactivatedAt sql.NullTime

		err := rows.Scan(
			&skill.ID,
			&skill.SessionID,
			&skill.SkillName,
			&skill.ActivatedAt,
			&skill.ActivatedBy,
			&skill.Scope,
			&skill.IsActive,
			&deactivatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan session skill: %w", err)
		}

		if deactivatedAt.Valid {
			skill.DeactivatedAt = &deactivatedAt.Time
		}

		skills = append(skills, skill)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating session skills: %w", err)
	}

	return skills, nil
}

// GetSessionSkillHistory retrieves all skills (active and inactive) for a session
func (s *Store) GetSessionSkillHistory(sessionID string) ([]SessionSkill, error) {
	query := `
		SELECT id, session_id, skill_name, activated_at, activated_by, scope, is_active, deactivated_at
		FROM session_skills
		WHERE session_id = ?
		ORDER BY activated_at DESC
	`

	rows, err := s.db.Query(query, sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to query session skill history: %w", err)
	}
	defer rows.Close()

	var skills []SessionSkill
	for rows.Next() {
		var skill SessionSkill
		var deactivatedAt sql.NullTime

		err := rows.Scan(
			&skill.ID,
			&skill.SessionID,
			&skill.SkillName,
			&skill.ActivatedAt,
			&skill.ActivatedBy,
			&skill.Scope,
			&skill.IsActive,
			&deactivatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan session skill: %w", err)
		}

		if deactivatedAt.Valid {
			skill.DeactivatedAt = &deactivatedAt.Time
		}

		skills = append(skills, skill)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating session skill history: %w", err)
	}

	return skills, nil
}

// DeactivateAllSessionSkills marks all skills as inactive for a session
func (s *Store) DeactivateAllSessionSkills(sessionID string) error {
	activeSkills, err := s.GetActiveSessionSkills(sessionID)
	if err != nil {
		return err
	}
	if len(activeSkills) == 0 {
		return nil
	}

	query := `
		UPDATE session_skills
		SET is_active = FALSE, deactivated_at = CURRENT_TIMESTAMP
		WHERE session_id = ? AND is_active = TRUE
	`

	if _, err := s.db.Exec(query, sessionID); err != nil {
		return fmt.Errorf("failed to deactivate all session skills: %w", err)
	}

	now := time.Now().UTC()
	for _, skill := range activeSkills {
		s.notify(newEvent(EventSkillDeactivated, sessionID, skill.SkillName, map[string]any{
			"skillName":     skill.SkillName,
			"isActive":      false,
			"deactivatedAt": now,
		}))
	}

	return nil
}
