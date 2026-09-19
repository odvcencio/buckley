package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var ErrApprovalDecisionConflict = errors.New("storage: approval decision conflict")

const (
	maxApprovalDecisionIDBytes        = 128
	maxApprovalDecisionSessionBytes   = 256
	maxApprovalDecisionPrincipalBytes = 128
	maxApprovalDecisionReasonBytes    = 512
)

// ApprovalPolicy represents a stored approval policy
type ApprovalPolicy struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	IsActive  bool      `json:"is_active"`
	Config    string    `json:"config"` // JSON encoded policy config
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PendingApproval represents a tool call awaiting approval
type PendingApproval struct {
	ID             string    `json:"id"`
	SessionID      string    `json:"session_id"`
	ToolName       string    `json:"tool_name"`
	ToolInput      string    `json:"tool_input"` // JSON encoded
	RiskScore      int       `json:"risk_score"`
	RiskReasons    []string  `json:"risk_reasons"`
	Status         string    `json:"status"` // pending, approved, rejected, expired, auto
	DecidedBy      string    `json:"decided_by,omitempty"`
	DecidedAt      time.Time `json:"decided_at,omitempty"`
	DecisionReason string    `json:"decision_reason,omitempty"`
	ExpiresAt      time.Time `json:"expires_at"`
	CreatedAt      time.Time `json:"created_at"`
}

// ApprovalAllowRule represents a persisted rule for auto-approving tool calls.
type ApprovalAllowRule struct {
	ID          int64     `json:"id"`
	ProjectPath string    `json:"project_path"`
	ToolName    string    `json:"tool_name"`
	Operation   string    `json:"operation"`
	Command     string    `json:"command"`
	FilePath    string    `json:"file_path"`
	CreatedAt   time.Time `json:"created_at"`
}

// ToolAuditEntry represents a logged tool execution
type ToolAuditEntry struct {
	ID         int64     `json:"id"`
	SessionID  string    `json:"session_id"`
	ApprovalID string    `json:"approval_id,omitempty"`
	ToolName   string    `json:"tool_name"`
	ToolInput  string    `json:"tool_input"`
	ToolOutput string    `json:"tool_output,omitempty"`
	RiskScore  int       `json:"risk_score"`
	Decision   string    `json:"decision"`
	DecidedBy  string    `json:"decided_by,omitempty"`
	ExecutedAt time.Time `json:"executed_at"`
	DurationMs int64     `json:"duration_ms"`
}

// GetActivePolicy returns the currently active approval policy
func (s *Store) GetActivePolicy() (*ApprovalPolicy, error) {
	if s.db == nil {
		return nil, ErrStoreClosed
	}

	row := s.db.QueryRow(`
		SELECT id, name, is_active, config, created_at, updated_at
		FROM approval_policies
		WHERE is_active = 1
		LIMIT 1
	`)

	var policy ApprovalPolicy
	var updatedAt sql.NullTime
	err := row.Scan(&policy.ID, &policy.Name, &policy.IsActive, &policy.Config, &policy.CreatedAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get active policy: %w", err)
	}

	if updatedAt.Valid {
		policy.UpdatedAt = updatedAt.Time
	}

	return &policy, nil
}

// SavePolicy creates or updates an approval policy
func (s *Store) SavePolicy(policy *ApprovalPolicy) error {
	if s.db == nil {
		return ErrStoreClosed
	}

	now := time.Now()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// If this policy should be active, deactivate all others first
	if policy.IsActive {
		if _, err := tx.Exec(`UPDATE approval_policies SET is_active = 0`); err != nil {
			return fmt.Errorf("deactivate policies: %w", err)
		}
	}

	if policy.ID == 0 {
		// Insert new policy
		result, err := tx.Exec(`
			INSERT INTO approval_policies (name, is_active, config, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?)
		`, policy.Name, policy.IsActive, policy.Config, now, now)
		if err != nil {
			return fmt.Errorf("insert policy: %w", err)
		}
		policy.ID, _ = result.LastInsertId()
		policy.CreatedAt = now
		policy.UpdatedAt = now
	} else {
		// Update existing policy
		_, err := tx.Exec(`
			UPDATE approval_policies
			SET name = ?, is_active = ?, config = ?, updated_at = ?
			WHERE id = ?
		`, policy.Name, policy.IsActive, policy.Config, now, policy.ID)
		if err != nil {
			return fmt.Errorf("update policy: %w", err)
		}
		policy.UpdatedAt = now
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit save policy: %w", err)
	}
	committed = true
	return nil
}

// GetPolicy returns a policy by ID
func (s *Store) GetPolicy(id int64) (*ApprovalPolicy, error) {
	if s.db == nil {
		return nil, ErrStoreClosed
	}

	row := s.db.QueryRow(`
		SELECT id, name, is_active, config, created_at, updated_at
		FROM approval_policies
		WHERE id = ?
	`, id)

	var policy ApprovalPolicy
	var updatedAt sql.NullTime
	err := row.Scan(&policy.ID, &policy.Name, &policy.IsActive, &policy.Config, &policy.CreatedAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get policy: %w", err)
	}

	if updatedAt.Valid {
		policy.UpdatedAt = updatedAt.Time
	}

	return &policy, nil
}

// ListPolicies returns all approval policies
func (s *Store) ListPolicies() ([]*ApprovalPolicy, error) {
	if s.db == nil {
		return nil, ErrStoreClosed
	}

	rows, err := s.db.Query(`
		SELECT id, name, is_active, config, created_at, updated_at
		FROM approval_policies
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}
	defer rows.Close()

	var policies []*ApprovalPolicy
	for rows.Next() {
		var policy ApprovalPolicy
		var updatedAt sql.NullTime
		if err := rows.Scan(&policy.ID, &policy.Name, &policy.IsActive, &policy.Config, &policy.CreatedAt, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan policy: %w", err)
		}
		if updatedAt.Valid {
			policy.UpdatedAt = updatedAt.Time
		}
		policies = append(policies, &policy)
	}

	return policies, rows.Err()
}

// ListApprovalAllowRules returns persisted allowlist rules for a project.
func (s *Store) ListApprovalAllowRules(projectPath string) ([]*ApprovalAllowRule, error) {
	if s.db == nil {
		return nil, ErrStoreClosed
	}

	projectPath = strings.TrimSpace(projectPath)
	rows, err := s.db.Query(`
		SELECT id, project_path, tool_name, operation, command, file_path, created_at
		FROM approval_allowlist
		WHERE project_path = ?
		ORDER BY created_at DESC
	`, projectPath)
	if err != nil {
		return nil, fmt.Errorf("list approval allow rules: %w", err)
	}
	defer rows.Close()

	var rules []*ApprovalAllowRule
	for rows.Next() {
		var rule ApprovalAllowRule
		if err := rows.Scan(&rule.ID, &rule.ProjectPath, &rule.ToolName, &rule.Operation, &rule.Command, &rule.FilePath, &rule.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan approval allow rule: %w", err)
		}
		rules = append(rules, &rule)
	}

	return rules, rows.Err()
}

// AddApprovalAllowRule persists an auto-approval rule (duplicates are ignored).
func (s *Store) AddApprovalAllowRule(rule *ApprovalAllowRule) error {
	if s.db == nil {
		return ErrStoreClosed
	}
	if rule == nil {
		return fmt.Errorf("approval allow rule is nil")
	}

	projectPath := strings.TrimSpace(rule.ProjectPath)
	toolName := strings.TrimSpace(rule.ToolName)
	operation := strings.TrimSpace(rule.Operation)
	command := strings.TrimSpace(rule.Command)
	filePath := strings.TrimSpace(rule.FilePath)
	if projectPath == "" && toolName == "" && operation == "" && command == "" && filePath == "" {
		return nil
	}

	_, err := s.db.Exec(`
		INSERT OR IGNORE INTO approval_allowlist (project_path, tool_name, operation, command, file_path, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, projectPath, toolName, operation, command, filePath, time.Now())
	if err != nil {
		return fmt.Errorf("insert approval allow rule: %w", err)
	}
	return nil
}

// CreatePendingApproval creates a new pending approval
func (s *Store) CreatePendingApproval(approval *PendingApproval) error {
	if s.db == nil {
		return ErrStoreClosed
	}

	riskReasonsJSON, _ := json.Marshal(approval.RiskReasons)

	_, err := s.db.Exec(`
		INSERT INTO pending_approvals (id, session_id, tool_name, tool_input, risk_score, risk_reasons, status, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, approval.ID, approval.SessionID, approval.ToolName, approval.ToolInput,
		approval.RiskScore, string(riskReasonsJSON), approval.Status,
		approval.ExpiresAt, approval.CreatedAt)
	if err != nil {
		return fmt.Errorf("create pending approval: %w", err)
	}

	// Notify observers
	s.notify(newEvent(EventApprovalCreated, approval.SessionID, approval.ID, map[string]any{
		"tool_name":  approval.ToolName,
		"risk_score": approval.RiskScore,
		"expires_at": approval.ExpiresAt,
	}))

	return nil
}

// GetPendingApproval returns a pending approval by ID
func (s *Store) GetPendingApproval(id string) (*PendingApproval, error) {
	if s.db == nil {
		return nil, ErrStoreClosed
	}

	row := s.db.QueryRow(`
			SELECT id, session_id, tool_name, tool_input, risk_score, risk_reasons,
			       status, decided_by, decided_at, decision_reason, expires_at, created_at
			FROM pending_approvals
			WHERE id = ?
		`, id)

	approval, err := scanPendingApproval(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pending approval: %w", err)
	}
	return approval, nil
}

func scanPendingApproval(row rowScanner) (*PendingApproval, error) {
	var approval PendingApproval
	var riskReasonsJSON string
	var decidedBy sql.NullString
	var decidedAtTime sql.NullTime
	var decisionReason sql.NullString
	err := row.Scan(&approval.ID, &approval.SessionID, &approval.ToolName,
		&approval.ToolInput, &approval.RiskScore, &riskReasonsJSON,
		&approval.Status, &decidedBy, &decidedAtTime, &decisionReason, &approval.ExpiresAt, &approval.CreatedAt)
	if err != nil {
		return nil, err
	}
	if riskReasonsJSON != "" {
		if err := json.Unmarshal([]byte(riskReasonsJSON), &approval.RiskReasons); err != nil {
			return nil, fmt.Errorf("unmarshal risk reasons: %w", err)
		}
	}
	if decidedBy.Valid {
		approval.DecidedBy = decidedBy.String
	}
	if decidedAtTime.Valid {
		approval.DecidedAt = decidedAtTime.Time
	}
	if decisionReason.Valid {
		approval.DecisionReason = decisionReason.String
	}

	return &approval, nil
}

// DecidePendingApproval atomically applies one approved/rejected decision.
// The returned duplicate flag is true only when the same semantic decision was
// already committed; the original authoritative decision timestamp is kept.
func (s *Store) DecidePendingApproval(id, sessionID, status, decidedBy, reason string, decidedAt time.Time) (*PendingApproval, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, ErrStoreClosed
	}
	id = strings.TrimSpace(id)
	sessionID = strings.TrimSpace(sessionID)
	status = strings.TrimSpace(status)
	decidedBy = strings.TrimSpace(decidedBy)
	reason = strings.TrimSpace(reason)
	if status != "approved" && status != "rejected" {
		return nil, false, fmt.Errorf("decide pending approval: status must be approved or rejected")
	}
	for _, field := range []struct {
		name  string
		value string
		max   int
	}{
		{"id", id, maxApprovalDecisionIDBytes},
		{"session id", sessionID, maxApprovalDecisionSessionBytes},
		{"decided by", decidedBy, maxApprovalDecisionPrincipalBytes},
	} {
		if !validApprovalDecisionText(field.value, field.max, false) {
			return nil, false, fmt.Errorf("decide pending approval: invalid %s", field.name)
		}
	}
	if !validApprovalDecisionText(reason, maxApprovalDecisionReasonBytes, true) {
		return nil, false, fmt.Errorf("decide pending approval: invalid decision reason")
	}
	if decidedAt.IsZero() {
		return nil, false, fmt.Errorf("decide pending approval: decision time is required")
	}
	decidedAt = decidedAt.UTC()

	var approval *PendingApproval
	duplicate := false
	err := s.withSessionExecWrite(context.Background(), func(db *sessionExecConn) error {
		result, err := db.exec(`UPDATE pending_approvals
			SET status = ?, decided_by = ?, decided_at = ?, decision_reason = ?
			WHERE id = ? AND session_id = ? AND status = 'pending'`,
			status, decidedBy, decidedAt, nullIfEmpty(reason), id, sessionID)
		if err != nil {
			return fmt.Errorf("decide pending approval: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read pending approval decision result: %w", err)
		}
		approval, err = scanPendingApproval(db.queryRow(`
			SELECT id, session_id, tool_name, tool_input, risk_score, risk_reasons,
			       status, decided_by, decided_at, decision_reason, expires_at, created_at
			FROM pending_approvals WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("decide pending approval: not found")
		}
		if err != nil {
			return fmt.Errorf("read decided pending approval: %w", err)
		}
		if changed == 1 {
			return nil
		}
		if changed != 0 || approval.SessionID != sessionID || approval.Status != status ||
			approval.DecidedBy != decidedBy || strings.TrimSpace(approval.DecisionReason) != reason {
			return ErrApprovalDecisionConflict
		}
		duplicate = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if !duplicate {
		s.notify(newEvent(EventApprovalDecided, approval.SessionID, approval.ID, map[string]any{
			"status":          approval.Status,
			"decided_by":      approval.DecidedBy,
			"decision_reason": approval.DecisionReason,
		}))
	}
	return approval, duplicate, nil
}

// ExpirePendingApproval atomically expires one approval only when the exact
// session-owned row is still pending and its expiry is at or before the
// database's current time. The returned approval is reread under the same
// write lock, so a concurrent approval or rejection is returned as the
// authoritative winner rather than being overwritten.
func (s *Store) ExpirePendingApproval(id, sessionID string) (*PendingApproval, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, ErrStoreClosed
	}
	id = strings.TrimSpace(id)
	sessionID = strings.TrimSpace(sessionID)
	if !validApprovalDecisionText(id, maxApprovalDecisionIDBytes, false) {
		return nil, false, fmt.Errorf("expire pending approval: invalid id")
	}
	if !validApprovalDecisionText(sessionID, maxApprovalDecisionSessionBytes, false) {
		return nil, false, fmt.Errorf("expire pending approval: invalid session id")
	}

	var (
		approval *PendingApproval
		changed  bool
	)
	err := s.withSessionExecWrite(context.Background(), func(db *sessionExecConn) error {
		nowMillis, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		approval, changed, err = expirePendingApprovalTx(db, id, sessionID, sessionExecTime(nowMillis))
		return err
	})
	if err != nil {
		return nil, false, err
	}
	if changed {
		s.notify(newEvent(EventApprovalExpired, approval.SessionID, approval.ID, map[string]any{
			"status":          approval.Status,
			"decision_reason": approval.DecisionReason,
		}))
	}
	return approval, changed, nil
}

func expirePendingApprovalTx(db *sessionExecConn, id, sessionID string, now time.Time) (*PendingApproval, bool, error) {
	// Read and compare the expiry under BEGIN IMMEDIATE. The SQLite driver
	// applies TIMESTAMP affinity when binding/scanning values, so a SQL text
	// comparison can mistake a future Go-format timestamp for an expired one.
	var rawExpiresAt string
	if err := db.queryRow(`SELECT expires_at FROM pending_approvals WHERE id = ? AND session_id = ?`, id, sessionID).Scan(&rawExpiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, fmt.Errorf("expire pending approval: not found")
		}
		return nil, false, fmt.Errorf("read pending approval expiry: %w", err)
	}
	expiresAt := parseSQLiteTimestamp(rawExpiresAt)
	if expiresAt.IsZero() {
		return nil, false, fmt.Errorf("expire pending approval: invalid expiry timestamp")
	}
	now = now.UTC()
	if expiresAt.After(now) {
		approval, err := scanPendingApproval(db.queryRow(`
			SELECT id, session_id, tool_name, tool_input, risk_score, risk_reasons,
			       status, decided_by, decided_at, decision_reason, expires_at, created_at
			FROM pending_approvals WHERE id = ? AND session_id = ?`, id, sessionID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, fmt.Errorf("expire pending approval: not found")
		}
		if err != nil {
			return nil, false, fmt.Errorf("read pending approval: %w", err)
		}
		return approval, false, nil
	}
	result, err := db.exec(`UPDATE pending_approvals
		SET status = 'expired', decided_by = NULL, decided_at = ?, decision_reason = 'timeout'
		WHERE id = ? AND session_id = ? AND status = 'pending'`,
		sqliteTimestamp(now), id, sessionID)
	if err != nil {
		return nil, false, fmt.Errorf("expire pending approval: %w", err)
	}
	changedRows, err := result.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("read pending approval expiry result: %w", err)
	}
	approval, err := scanPendingApproval(db.queryRow(`
		SELECT id, session_id, tool_name, tool_input, risk_score, risk_reasons,
		       status, decided_by, decided_at, decision_reason, expires_at, created_at
		FROM pending_approvals WHERE id = ? AND session_id = ?`, id, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("expire pending approval: not found")
	}
	if err != nil {
		return nil, false, fmt.Errorf("read expired pending approval: %w", err)
	}
	return approval, changedRows == 1, nil
}

func validApprovalDecisionText(value string, maxBytes int, allowEmpty bool) bool {
	if (!allowEmpty && value == "") || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) && char != '\n' && char != '\t' {
			return false
		}
	}
	return true
}

// UpdatePendingApproval updates a pending approval's status
func (s *Store) UpdatePendingApproval(approval *PendingApproval) error {
	if s.db == nil {
		return ErrStoreClosed
	}
	if approval != nil && strings.TrimSpace(approval.Status) == "expired" {
		_, _, err := s.ExpirePendingApproval(approval.ID, approval.SessionID)
		return err
	}

	var decidedAt any
	if !approval.DecidedAt.IsZero() {
		decidedAt = approval.DecidedAt
	}

	var decisionReason any
	if strings.TrimSpace(approval.DecisionReason) != "" {
		decisionReason = strings.TrimSpace(approval.DecisionReason)
	}

	_, err := s.db.Exec(`
			UPDATE pending_approvals
			SET status = ?, decided_by = ?, decided_at = ?, decision_reason = ?
			WHERE id = ?
		`, approval.Status, approval.DecidedBy, decidedAt, decisionReason, approval.ID)
	if err != nil {
		return fmt.Errorf("update pending approval: %w", err)
	}

	// Notify observers
	s.notify(newEvent(EventApprovalDecided, approval.SessionID, approval.ID, map[string]any{
		"status":          approval.Status,
		"decided_by":      approval.DecidedBy,
		"decision_reason": strings.TrimSpace(approval.DecisionReason),
	}))

	return nil
}

// ListPendingApprovals returns pending approvals for a session
func (s *Store) ListPendingApprovals(sessionID string) ([]*PendingApproval, error) {
	if s.db == nil {
		return nil, ErrStoreClosed
	}

	now := time.Now()
	query := `
				SELECT id, session_id, tool_name, tool_input, risk_score, risk_reasons,
				       status, decided_by, decided_at, decision_reason, expires_at, created_at
				FROM pending_approvals
				WHERE status = 'pending' AND expires_at >= ?
		`
	args := []any{now}

	if sessionID != "" {
		query += ` AND session_id = ?`
		args = append(args, sessionID)
	}

	query += ` ORDER BY created_at ASC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list pending approvals: %w", err)
	}
	defer rows.Close()

	var approvals []*PendingApproval
	for rows.Next() {
		var approval PendingApproval
		var riskReasonsJSON string
		var decidedBy sql.NullString
		var decidedAtTime sql.NullTime
		var decisionReason sql.NullString

		if err := rows.Scan(&approval.ID, &approval.SessionID, &approval.ToolName,
			&approval.ToolInput, &approval.RiskScore, &riskReasonsJSON,
			&approval.Status, &decidedBy, &decidedAtTime, &decisionReason, &approval.ExpiresAt, &approval.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan pending approval: %w", err)
		}

		if riskReasonsJSON != "" {
			if err := json.Unmarshal([]byte(riskReasonsJSON), &approval.RiskReasons); err != nil {
				return nil, fmt.Errorf("unmarshal risk reasons: %w", err)
			}
		}
		if decidedBy.Valid {
			approval.DecidedBy = decidedBy.String
		}
		if decidedAtTime.Valid {
			approval.DecidedAt = decidedAtTime.Time
		}
		if decisionReason.Valid {
			approval.DecisionReason = decisionReason.String
		}

		approvals = append(approvals, &approval)
	}

	return approvals, rows.Err()
}

// ExpirePendingApprovals marks expired approvals as expired
func (s *Store) ExpirePendingApprovals() (int, error) {
	if s.db == nil {
		return 0, ErrStoreClosed
	}

	var expired []*PendingApproval
	err := s.withSessionExecWrite(context.Background(), func(db *sessionExecConn) error {
		nowMillis, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		now := sessionExecTime(nowMillis)
		rows, err := db.query(`
			SELECT id, session_id FROM pending_approvals
			WHERE status = 'pending'`)
		if err != nil {
			return fmt.Errorf("list pending approvals to expire: %w", err)
		}
		var candidates [][2]string
		for rows.Next() {
			var id, sessionID string
			if err := rows.Scan(&id, &sessionID); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan pending approval to expire: %w", err)
			}
			candidates = append(candidates, [2]string{id, sessionID})
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate pending approvals to expire: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close pending approvals to expire: %w", err)
		}
		for _, candidate := range candidates {
			approval, changed, err := expirePendingApprovalTx(db, candidate[0], candidate[1], now)
			if err != nil {
				return err
			}
			if changed {
				expired = append(expired, approval)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, approval := range expired {
		s.notify(newEvent(EventApprovalExpired, approval.SessionID, approval.ID, map[string]any{
			"status":          approval.Status,
			"decision_reason": approval.DecisionReason,
		}))
	}
	return len(expired), nil
}

// LogToolExecution logs a tool execution to the audit log
func (s *Store) LogToolExecution(entry *ToolAuditEntry) error {
	if s.db == nil {
		return ErrStoreClosed
	}

	var approvalID any
	if entry.ApprovalID != "" {
		approvalID = entry.ApprovalID
	}

	result, err := s.db.Exec(`
		INSERT INTO tool_audit_log (session_id, approval_id, tool_name, tool_input, tool_output,
		                            risk_score, decision, decided_by, executed_at, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, entry.SessionID, approvalID, entry.ToolName, entry.ToolInput, entry.ToolOutput,
		entry.RiskScore, entry.Decision, entry.DecidedBy, entry.ExecutedAt, entry.DurationMs)
	if err != nil {
		return fmt.Errorf("log tool execution: %w", err)
	}

	entry.ID, _ = result.LastInsertId()
	return nil
}

// GetAuditLog returns the audit log for a session
func (s *Store) GetAuditLog(sessionID string, limit int) ([]*ToolAuditEntry, error) {
	if s.db == nil {
		return nil, ErrStoreClosed
	}

	if limit <= 0 {
		limit = 100
	}

	rows, err := s.db.Query(`
		SELECT id, session_id, approval_id, tool_name, tool_input, tool_output,
		       risk_score, decision, decided_by, executed_at, duration_ms
		FROM tool_audit_log
		WHERE session_id = ?
		ORDER BY executed_at DESC
		LIMIT ?
	`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("get audit log: %w", err)
	}
	defer rows.Close()

	var entries []*ToolAuditEntry
	for rows.Next() {
		var entry ToolAuditEntry
		var approvalID, toolOutput, decidedBy sql.NullString

		if err := rows.Scan(&entry.ID, &entry.SessionID, &approvalID, &entry.ToolName,
			&entry.ToolInput, &toolOutput, &entry.RiskScore, &entry.Decision,
			&decidedBy, &entry.ExecutedAt, &entry.DurationMs); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", err)
		}

		if approvalID.Valid {
			entry.ApprovalID = approvalID.String
		}
		if toolOutput.Valid {
			entry.ToolOutput = toolOutput.String
		}
		if decidedBy.Valid {
			entry.DecidedBy = decidedBy.String
		}

		entries = append(entries, &entry)
	}

	return entries, rows.Err()
}

// CountPendingApprovals returns the count of pending approvals for a session
func (s *Store) CountPendingApprovals(sessionID string) (int, error) {
	if s.db == nil {
		return 0, ErrStoreClosed
	}

	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM pending_approvals
		WHERE session_id = ? AND status = 'pending'
		AND (expires_at IS NULL OR expires_at > ?)
	`, sessionID, time.Now()).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count pending approvals: %w", err)
	}

	return count, nil
}

func ensurePendingApprovalsSchema(db MigrationDB) error {
	rows, err := db.Query(`PRAGMA table_info(pending_approvals)`)
	if err != nil {
		return fmt.Errorf("pending approvals pragma: %w", err)
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan pending approvals pragma: %w", err)
		}
		cols[strings.ToLower(name)] = true
	}

	if err := rows.Err(); err != nil {
		return err
	}

	if !cols["decision_reason"] {
		if _, err := db.Exec(`ALTER TABLE pending_approvals ADD COLUMN decision_reason TEXT`); err != nil {
			return fmt.Errorf("add pending_approvals.decision_reason: %w", err)
		}
	}

	return nil
}
