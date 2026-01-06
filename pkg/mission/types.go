package mission

import "time"

const (
	EventMissionSnapshot = "mission.snapshot"
	EventAgentActivity   = "mission.agent.activity"
	EventAgentStatus     = "mission.agent.status"
	EventChangeCreated   = "mission.change.created"
	EventChangeApproved  = "mission.change.approved"
	EventChangeRejected  = "mission.change.rejected"
)

// PendingChange represents a code change awaiting approval
type PendingChange struct {
	ID         string     `json:"id"`
	AgentID    string     `json:"agentId"`
	SessionID  string     `json:"sessionId"`
	FilePath   string     `json:"filePath"`
	Diff       string     `json:"diff"`
	Reason     string     `json:"reason,omitempty"`
	Status     string     `json:"status"` // pending, approved, rejected
	CreatedAt  time.Time  `json:"createdAt"`
	ReviewedAt *time.Time `json:"reviewedAt,omitempty"`
	ReviewedBy string     `json:"reviewedBy,omitempty"`
}

// AgentActivity represents an agent's current activity
type AgentActivity struct {
	ID        int64     `json:"id"`
	AgentID   string    `json:"agentId"`
	SessionID string    `json:"sessionId"`
	AgentType string    `json:"agentType,omitempty"`
	Action    string    `json:"action"`
	Details   string    `json:"details,omitempty"`
	Status    string    `json:"status"` // active, idle, working, waiting, error, stopped
	Timestamp time.Time `json:"timestamp"`
}

// AgentStatus represents the current state of an agent
type AgentStatus struct {
	AgentID        string    `json:"agentId"`
	AgentType      string    `json:"agentType,omitempty"`
	SessionID      string    `json:"sessionId"`
	Status         string    `json:"status"`
	CurrentAction  string    `json:"currentAction,omitempty"`
	LastActivity   time.Time `json:"lastActivity"`
	ActivityCount  int       `json:"activityCount"`
	PendingChanges int       `json:"pendingChanges"`
}

// DiffApprovalRequest represents a request to approve/reject a diff
type DiffApprovalRequest struct {
	ChangeID   string `json:"changeId"`
	Action     string `json:"action"` // approve, reject
	ReviewedBy string `json:"reviewedBy,omitempty"`
	Comment    string `json:"comment,omitempty"`
}

// AgentMessageRequest represents a message to send to an agent
type AgentMessageRequest struct {
	Message   string `json:"message"`
	SessionID string `json:"sessionId,omitempty"`
}
