package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Session status constants.
const (
	SessionStatusActive    = "active"
	SessionStatusPaused    = "paused"
	SessionStatusCompleted = "completed"
)

// Session represents a conversation session persisted in SQLite.
type Session struct {
	ID           string     `json:"id"`
	Principal    string     `json:"principal,omitempty"`
	ProjectPath  string     `json:"projectPath,omitempty"`
	GitRepo      string     `json:"gitRepo,omitempty"`
	GitBranch    string     `json:"gitBranch,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	LastActive   time.Time  `json:"lastActive"`
	MessageCount int        `json:"messageCount"`
	TotalTokens  int        `json:"totalTokens"`
	TotalCost    float64    `json:"totalCost"`
	Status       string     `json:"status"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
	// Pause state for workflow continuity
	PauseReason   string     `json:"pauseReason,omitempty"`
	PauseQuestion string     `json:"pauseQuestion,omitempty"`
	PausedAt      *time.Time `json:"pausedAt,omitempty"`
}

// CreateSession creates a new session with retry logic for database locks.
func (s *Store) CreateSession(session *Session) error {
	status := strings.TrimSpace(strings.ToLower(session.Status))
	if status == "" {
		status = SessionStatusActive
	}

	query := `
		INSERT INTO sessions (session_id, principal, project_path, git_repo, git_branch, created_at, last_active, status, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	principal := strings.TrimSpace(session.Principal)
	var principalArg any
	if principal != "" {
		principalArg = principal
	}

	var completedAt any
	if session.CompletedAt != nil {
		completedAt = *session.CompletedAt
	}

	// Retry logic for handling transient SQLITE_BUSY during concurrent operations (e.g., indexing)
	maxRetries := 3
	baseDelay := 100 * time.Millisecond

	var err error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		_, err = s.db.Exec(query,
			session.ID,
			principalArg,
			session.ProjectPath,
			session.GitRepo,
			session.GitBranch,
			session.CreatedAt,
			session.LastActive,
			status,
			completedAt,
		)

		if err == nil {
			clone := *session
			s.notify(newEvent(EventSessionCreated, session.ID, session.ID, clone))
			return nil
		}

		// Only retry on SQLITE_BUSY/LOCKED errors
		if isBusyError(err) {
			if attempt < maxRetries {
				delay := baseDelay * time.Duration(1<<uint(attempt)) // Exponential backoff
				time.Sleep(delay)
				continue
			}
		}

		// Non-retryable error or max retries exceeded
		return err
	}

	return err
}

// GetSession retrieves a session by ID.
func (s *Store) GetSession(sessionID string) (*Session, error) {
	query := `
		SELECT session_id, principal, project_path, git_repo, git_branch, created_at, last_active,
		       message_count, total_tokens, total_cost, status, completed_at,
		       pause_reason, pause_question, paused_at
		FROM sessions WHERE session_id = ?
	`
	var session Session
	var principal sql.NullString
	var completed sql.NullTime
	var pauseReason, pauseQuestion sql.NullString
	var pausedAt sql.NullTime
	err := s.db.QueryRow(query, sessionID).Scan(
		&session.ID,
		&principal,
		&session.ProjectPath,
		&session.GitRepo,
		&session.GitBranch,
		&session.CreatedAt,
		&session.LastActive,
		&session.MessageCount,
		&session.TotalTokens,
		&session.TotalCost,
		&session.Status,
		&completed,
		&pauseReason,
		&pauseQuestion,
		&pausedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil // Session not found
	}
	if err != nil {
		return nil, err
	}
	if completed.Valid {
		session.CompletedAt = &completed.Time
	}
	if principal.Valid {
		session.Principal = principal.String
	}
	if pauseReason.Valid {
		session.PauseReason = pauseReason.String
	}
	if pauseQuestion.Valid {
		session.PauseQuestion = pauseQuestion.String
	}
	if pausedAt.Valid {
		session.PausedAt = &pausedAt.Time
	}
	return &session, nil
}

// EnsureSession creates a minimal session record if it doesn't exist.
// This is used by the TODO tool to satisfy foreign key constraints.
func (s *Store) EnsureSession(sessionID string) error {
	// Check if session already exists
	existing, err := s.GetSession(sessionID)
	if err != nil {
		return fmt.Errorf("failed to check session: %w", err)
	}
	if existing != nil {
		return nil // Session already exists
	}

	now := time.Now()
	session := &Session{
		ID:         sessionID,
		Principal:  "anonymous",
		CreatedAt:  now,
		LastActive: now,
		Status:     SessionStatusActive,
	}
	return s.CreateSession(session)
}

// ListSessions returns all sessions ordered by last active time.
func (s *Store) ListSessions(limit int) ([]Session, error) {
	query := `
		SELECT session_id, principal, project_path, git_repo, git_branch, created_at, last_active,
		       message_count, total_tokens, total_cost, status, completed_at
		FROM sessions
		ORDER BY last_active DESC
		LIMIT ?
	`
	rows, err := s.db.Query(query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := []Session{}
	for rows.Next() {
		var session Session
		var principal sql.NullString
		var completed sql.NullTime
		if err := rows.Scan(
			&session.ID,
			&principal,
			&session.ProjectPath,
			&session.GitRepo,
			&session.GitBranch,
			&session.CreatedAt,
			&session.LastActive,
			&session.MessageCount,
			&session.TotalTokens,
			&session.TotalCost,
			&session.Status,
			&completed,
		); err != nil {
			return nil, err
		}
		if completed.Valid {
			session.CompletedAt = &completed.Time
		}
		if principal.Valid {
			session.Principal = principal.String
		}
		sessions = append(sessions, session)
	}

	return sessions, rows.Err()
}

// ListSessionsByRepo returns all sessions tied to a specific git repository/project path.
func (s *Store) ListSessionsByRepo(repoPath string) ([]Session, error) {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return []Session{}, nil
	}
	query := `
		SELECT session_id, principal, project_path, git_repo, git_branch, created_at, last_active,
		       message_count, total_tokens, total_cost, status, completed_at
		FROM sessions
		WHERE git_repo = ? OR project_path = ?
		ORDER BY last_active DESC
	`
	rows, err := s.db.Query(query, repoPath, repoPath)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var session Session
		var principal sql.NullString
		var completed sql.NullTime
		if err := rows.Scan(
			&session.ID,
			&principal,
			&session.ProjectPath,
			&session.GitRepo,
			&session.GitBranch,
			&session.CreatedAt,
			&session.LastActive,
			&session.MessageCount,
			&session.TotalTokens,
			&session.TotalCost,
			&session.Status,
			&completed,
		); err != nil {
			return nil, err
		}
		if completed.Valid {
			session.CompletedAt = &completed.Time
		}
		if principal.Valid {
			session.Principal = principal.String
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

// UpdateSessionActivity updates the last active timestamp.
func (s *Store) UpdateSessionActivity(sessionID string) error {
	now := time.Now()
	query := `UPDATE sessions SET last_active = ? WHERE session_id = ?`
	_, err := s.db.Exec(query, now, sessionID)
	if err != nil {
		return err
	}

	s.notify(newEvent(EventSessionUpdated, sessionID, sessionID, map[string]any{
		"lastActive": now,
	}))

	return nil
}

// UpdateSessionProjectPath updates the project path for a session.
func (s *Store) UpdateSessionProjectPath(sessionID, projectPath string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session id required")
	}
	projectPath = strings.TrimSpace(projectPath)
	query := `UPDATE sessions SET project_path = ? WHERE session_id = ?`
	if _, err := s.db.Exec(query, projectPath, sessionID); err != nil {
		return err
	}

	s.notify(newEvent(EventSessionUpdated, sessionID, sessionID, map[string]any{
		"projectPath": projectPath,
	}))
	return nil
}

// UpdateSessionStats updates message count, tokens, and cost.
func (s *Store) UpdateSessionStats(sessionID string, messageCount, totalTokens int, totalCost float64) error {
	query := `
		UPDATE sessions
		SET message_count = ?, total_tokens = ?, total_cost = ?
		WHERE session_id = ?
	`

	// Retry logic for handling transient SQLITE_BUSY during concurrent operations (e.g., indexing)
	maxRetries := 3
	baseDelay := 100 * time.Millisecond

	var err error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		_, err = s.db.Exec(query, messageCount, totalTokens, totalCost, sessionID)
		if err == nil {
			s.notify(newEvent(EventSessionUpdated, sessionID, sessionID, map[string]any{
				"messageCount": messageCount,
				"totalTokens":  totalTokens,
				"totalCost":    totalCost,
			}))
			return nil
		}

		// Only retry on SQLITE_BUSY/LOCKED errors
		if isBusyError(err) {
			if attempt < maxRetries {
				delay := baseDelay * time.Duration(1<<uint(attempt)) // Exponential backoff
				time.Sleep(delay)
				continue
			}
		}

		// Non-retryable error or max retries exceeded
		return err
	}

	return err
}

// SetSessionStatus updates the session status (active/paused/completed) and tracks completion timestamps.
func (s *Store) SetSessionStatus(sessionID string, status string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session id required")
	}

	status = strings.TrimSpace(strings.ToLower(status))
	if status == "" {
		status = SessionStatusActive
	}

	valid := map[string]struct{}{
		SessionStatusActive:    {},
		SessionStatusPaused:    {},
		SessionStatusCompleted: {},
	}
	if _, ok := valid[status]; !ok {
		return fmt.Errorf("invalid session status: %s", status)
	}

	var (
		res     sql.Result
		err     error
		payload map[string]any
	)

	if status == SessionStatusCompleted {
		now := time.Now()
		res, err = s.db.Exec(`UPDATE sessions SET status = ?, completed_at = ? WHERE session_id = ?`, status, now, sessionID)
		payload = map[string]any{
			"status":      status,
			"completedAt": now,
		}
	} else {
		res, err = s.db.Exec(`UPDATE sessions SET status = ?, completed_at = NULL WHERE session_id = ?`, status, sessionID)
		payload = map[string]any{
			"status":      status,
			"completedAt": nil,
		}
	}
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("session %s not found", sessionID)
	}
	s.notify(newEvent(EventSessionUpdated, sessionID, sessionID, payload))
	return nil
}

// UpdateSessionPauseState updates the pause state for a session.
// Pass empty strings for reason/question and nil for pausedAt to clear pause state.
func (s *Store) UpdateSessionPauseState(sessionID string, reason, question string, pausedAt *time.Time) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("session id required")
	}

	var (
		status  string
		payload map[string]any
	)

	if reason != "" || question != "" {
		// Setting pause state
		status = SessionStatusPaused
		payload = map[string]any{
			"status":        status,
			"pauseReason":   reason,
			"pauseQuestion": question,
			"pausedAt":      pausedAt,
		}
	} else {
		// Clearing pause state
		status = SessionStatusActive
		payload = map[string]any{
			"status":        status,
			"pauseReason":   nil,
			"pauseQuestion": nil,
			"pausedAt":      nil,
		}
	}

	query := `
		UPDATE sessions
		SET status = ?, pause_reason = ?, pause_question = ?, paused_at = ?
		WHERE session_id = ?
	`

	var reasonArg, questionArg, pausedAtArg any
	if reason != "" {
		reasonArg = reason
	}
	if question != "" {
		questionArg = question
	}
	if pausedAt != nil {
		pausedAtArg = *pausedAt
	}

	res, err := s.db.Exec(query, status, reasonArg, questionArg, pausedAtArg, sessionID)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("session %s not found", sessionID)
	}
	s.notify(newEvent(EventSessionUpdated, sessionID, sessionID, payload))
	return nil
}

// DeleteSession deletes a session and all related data (cascades to messages, api_calls).
func (s *Store) DeleteSession(sessionID string) error {
	query := `DELETE FROM sessions WHERE session_id = ?`
	_, err := s.db.Exec(query, sessionID)
	if err != nil {
		return err
	}

	s.notify(newEvent(EventSessionDeleted, sessionID, sessionID, nil))
	return nil
}
