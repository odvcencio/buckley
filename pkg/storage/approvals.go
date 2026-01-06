package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
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

	// If this policy should be active, deactivate all others first
	if policy.IsActive {
		if _, err := s.db.Exec(`UPDATE approval_policies SET is_active = 0`); err != nil {
			return fmt.Errorf("deactivate policies: %w", err)
		}
	}

	if policy.ID == 0 {
		// Insert new policy
		result, err := s.db.Exec(`
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
		_, err := s.db.Exec(`
			UPDATE approval_policies
			SET name = ?, is_active = ?, config = ?, updated_at = ?
			WHERE id = ?
		`, policy.Name, policy.IsActive, policy.Config, now, policy.ID)
		if err != nil {
			return fmt.Errorf("update policy: %w", err)
		}
		policy.UpdatedAt = now
	}

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

	var approval PendingApproval
	var riskReasonsJSON string
	var decidedBy sql.NullString
	var decidedAtTime sql.NullTime
	var decisionReason sql.NullString

	err := row.Scan(&approval.ID, &approval.SessionID, &approval.ToolName,
		&approval.ToolInput, &approval.RiskScore, &riskReasonsJSON,
		&approval.Status, &decidedBy, &decidedAtTime, &decisionReason, &approval.ExpiresAt, &approval.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pending approval: %w", err)
	}

	if riskReasonsJSON != "" {
		json.Unmarshal([]byte(riskReasonsJSON), &approval.RiskReasons)
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

// UpdatePendingApproval updates a pending approval's status
func (s *Store) UpdatePendingApproval(approval *PendingApproval) error {
	if s.db == nil {
		return ErrStoreClosed
	}

	var decidedAt interface{}
	if !approval.DecidedAt.IsZero() {
		decidedAt = approval.DecidedAt
	}

	var decisionReason interface{}
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
	args := []interface{}{now}

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
			json.Unmarshal([]byte(riskReasonsJSON), &approval.RiskReasons)
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

	now := time.Now()
	result, err := s.db.Exec(`
		UPDATE pending_approvals
		SET status = 'expired', decided_at = ?, decision_reason = 'timeout'
		WHERE status = 'pending' AND expires_at < ?
	`, now, now)
	if err != nil {
		return 0, fmt.Errorf("expire pending approvals: %w", err)
	}

	count, _ := result.RowsAffected()
	return int(count), nil
}

// LogToolExecution logs a tool execution to the audit log
func (s *Store) LogToolExecution(entry *ToolAuditEntry) error {
	if s.db == nil {
		return ErrStoreClosed
	}

	var approvalID interface{}
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
	`, sessionID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count pending approvals: %w", err)
	}

	return count, nil
}

func ensurePendingApprovalsSchema(db *sql.DB) error {
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
