package mission

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Store handles persistence for Mission Control data
type Store struct {
	db *sql.DB
}

// NewStore creates a new Mission Control store
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// CreatePendingChange creates a new pending change record
func (s *Store) CreatePendingChange(change *PendingChange) error {
	query := `
		INSERT INTO pending_changes (id, agent_id, session_id, file_path, diff, reason, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`
	_, err := s.db.Exec(query,
		change.ID,
		change.AgentID,
		change.SessionID,
		change.FilePath,
		change.Diff,
		change.Reason,
		change.Status,
		change.CreatedAt,
	)
	return err
}

// GetPendingChange retrieves a pending change by ID
func (s *Store) GetPendingChange(id string) (*PendingChange, error) {
	query := `
		SELECT id, agent_id, session_id, file_path, diff, reason, status, created_at, reviewed_at, reviewed_by
		FROM pending_changes
		WHERE id = ?
	`

	change := &PendingChange{}
	var reviewedAt sql.NullTime
	var reviewedBy sql.NullString
	err := s.db.QueryRow(query, id).Scan(
		&change.ID,
		&change.AgentID,
		&change.SessionID,
		&change.FilePath,
		&change.Diff,
		&change.Reason,
		&change.Status,
		&change.CreatedAt,
		&reviewedAt,
		&reviewedBy,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("pending change not found: %s", id)
	}
	if err != nil {
		return nil, err
	}

	if reviewedAt.Valid {
		change.ReviewedAt = &reviewedAt.Time
	}
	if reviewedBy.Valid {
		change.ReviewedBy = reviewedBy.String
	}

	return change, nil
}

// ListPendingChanges lists pending changes, optionally filtered by status
func (s *Store) ListPendingChanges(status string, limit int) ([]*PendingChange, error) {
	var query string
	var args []interface{}

	if status != "" {
		query = `
			SELECT id, agent_id, session_id, file_path, diff, reason, status, created_at, reviewed_at, reviewed_by
			FROM pending_changes
			WHERE status = ?
			ORDER BY created_at DESC
			LIMIT ?
		`
		args = []interface{}{status, limit}
	} else {
		query = `
			SELECT id, agent_id, session_id, file_path, diff, reason, status, created_at, reviewed_at, reviewed_by
			FROM pending_changes
			ORDER BY created_at DESC
			LIMIT ?
		`
		args = []interface{}{limit}
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var changes []*PendingChange
	for rows.Next() {
		change := &PendingChange{}
		var reviewedAt sql.NullTime
		var reviewedBy sql.NullString
		err := rows.Scan(
			&change.ID,
			&change.AgentID,
			&change.SessionID,
			&change.FilePath,
			&change.Diff,
			&change.Reason,
			&change.Status,
			&change.CreatedAt,
			&reviewedAt,
			&reviewedBy,
		)
		if err != nil {
			return nil, err
		}
		if reviewedAt.Valid {
			change.ReviewedAt = &reviewedAt.Time
		}
		if reviewedBy.Valid {
			change.ReviewedBy = reviewedBy.String
		}
		changes = append(changes, change)
	}

	return changes, rows.Err()
}

// UpdatePendingChangeStatus updates the status of a pending change
func (s *Store) UpdatePendingChangeStatus(id, status, reviewedBy string) error {
	query := `
		UPDATE pending_changes
		SET status = ?, reviewed_at = ?, reviewed_by = ?
		WHERE id = ?
	`

	result, err := s.db.Exec(query, status, time.Now(), reviewedBy, id)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("pending change not found: %s", id)
	}

	return nil
}

// RecordAgentActivity records an agent activity event
func (s *Store) RecordAgentActivity(activity *AgentActivity) error {
	query := `
		INSERT INTO agent_activity (agent_id, session_id, agent_type, action, details, status, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`

	result, err := s.db.Exec(query,
		activity.AgentID,
		activity.SessionID,
		activity.AgentType,
		activity.Action,
		activity.Details,
		activity.Status,
		activity.Timestamp,
	)
	if err != nil {
		return err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return err
	}

	activity.ID = id
	return nil
}

// GetAgentStatus retrieves the current status of an agent
func (s *Store) GetAgentStatus(agentID string) (*AgentStatus, error) {
	// Get latest activity
	activityQuery := `
		SELECT session_id, agent_type, action, status, timestamp
		FROM agent_activity
		WHERE agent_id = ?
		ORDER BY timestamp DESC
		LIMIT 1
	`

	status := &AgentStatus{AgentID: agentID}
	err := s.db.QueryRow(activityQuery, agentID).Scan(
		&status.SessionID,
		&status.AgentType,
		&status.CurrentAction,
		&status.Status,
		&status.LastActivity,
	)

	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("agent not found: %s", agentID)
	}
	if err != nil {
		return nil, err
	}

	// Count activities
	countQuery := `SELECT COUNT(*) FROM agent_activity WHERE agent_id = ?`
	err = s.db.QueryRow(countQuery, agentID).Scan(&status.ActivityCount)
	if err != nil {
		return nil, err
	}

	// Count pending changes
	pendingQuery := `SELECT COUNT(*) FROM pending_changes WHERE agent_id = ? AND status = 'pending'`
	err = s.db.QueryRow(pendingQuery, agentID).Scan(&status.PendingChanges)
	if err != nil {
		return nil, err
	}

	return status, nil
}

// ListActiveAgents lists all agents with recent activity
func (s *Store) ListActiveAgents(since time.Duration) ([]*AgentStatus, error) {
	cutoff := time.Now().Add(-since)

	query := `
		SELECT DISTINCT agent_id
		FROM agent_activity
		WHERE timestamp > ?
		ORDER BY timestamp DESC
	`

	rows, err := s.db.Query(query, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var statuses []*AgentStatus
	for rows.Next() {
		var agentID string
		if err := rows.Scan(&agentID); err != nil {
			return nil, err
		}

		status, err := s.GetAgentStatus(agentID)
		if err != nil {
			continue // Skip agents with errors
		}

		statuses = append(statuses, status)
	}

	return statuses, rows.Err()
}

// GetAgentActivity retrieves recent activity for an agent
func (s *Store) GetAgentActivity(agentID string, limit int) ([]*AgentActivity, error) {
	query := `
		SELECT id, agent_id, session_id, agent_type, action, details, status, timestamp
		FROM agent_activity
		WHERE agent_id = ?
		ORDER BY timestamp DESC
		LIMIT ?
	`

	rows, err := s.db.Query(query, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var activities []*AgentActivity
	for rows.Next() {
		activity := &AgentActivity{}
		err := rows.Scan(
			&activity.ID,
			&activity.AgentID,
			&activity.SessionID,
			&activity.AgentType,
			&activity.Action,
			&activity.Details,
			&activity.Status,
			&activity.Timestamp,
		)
		if err != nil {
			return nil, err
		}
		activities = append(activities, activity)
	}

	return activities, rows.Err()
}

// ListSessionActivity returns recent activity rows for a session.
func (s *Store) ListSessionActivity(sessionID string, limit int) ([]*AgentActivity, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT id, agent_id, session_id, agent_type, action, details, status, timestamp
		FROM agent_activity
		WHERE session_id = ?
		ORDER BY timestamp DESC
		LIMIT ?
	`

	rows, err := s.db.Query(query, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var activities []*AgentActivity
	for rows.Next() {
		activity := &AgentActivity{}
		if err := rows.Scan(
			&activity.ID,
			&activity.AgentID,
			&activity.SessionID,
			&activity.AgentType,
			&activity.Action,
			&activity.Details,
			&activity.Status,
			&activity.Timestamp,
		); err != nil {
			return nil, err
		}
		activities = append(activities, activity)
	}
	return activities, rows.Err()
}

// WaitForDecision blocks until a pending change is approved or rejected.
// The poll interval controls how frequently the database is checked while waiting.
func (s *Store) WaitForDecision(ctx context.Context, changeID string, pollInterval time.Duration) (*PendingChange, error) {
	if pollInterval <= 0 {
		pollInterval = 500 * time.Millisecond
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		change, err := s.GetPendingChange(changeID)
		if err != nil {
			return nil, err
		}
		if change.Status != "pending" {
			return change, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
