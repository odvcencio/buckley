package policy

import (
	"time"
)

// Action represents what should happen for a tool call
type Action string

const (
	ActionApprove Action = "approve" // Require explicit approval
	ActionAuto    Action = "auto"    // Auto-approve
	ActionContext Action = "context" // Use time window context
	ActionReject  Action = "reject"  // Always reject
)

// Policy represents a complete approval policy configuration
type Policy struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	IsActive  bool      `json:"is_active"`
	Config    Config    `json:"config"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Config holds the full policy configuration
type Config struct {
	Categories  map[string]CategoryRule `json:"categories" yaml:"categories"`
	RiskRules   []RiskRule              `json:"risk_rules" yaml:"risk_rules"`
	TimeWindows map[string]TimeWindow   `json:"time_windows" yaml:"time_windows"`
	Defaults    Defaults                `json:"defaults" yaml:"defaults"`
}

// CategoryRule defines how a tool category should be handled
type CategoryRule struct {
	Action     Action      `json:"action" yaml:"action"`
	Exceptions []Exception `json:"exceptions,omitempty" yaml:"exceptions,omitempty"`
}

// Exception defines conditions that override the category action
type Exception struct {
	Pattern string `json:"pattern,omitempty" yaml:"pattern,omitempty"` // Glob pattern for file paths
	Command string `json:"command,omitempty" yaml:"command,omitempty"` // Glob pattern for commands
}

// RiskRule defines a condition that adds to the risk score
type RiskRule struct {
	Condition string `json:"condition" yaml:"condition"`
	Score     int    `json:"score" yaml:"score"`
	Action    Action `json:"action,omitempty" yaml:"action,omitempty"`
}

// TimeWindow defines when certain thresholds apply
type TimeWindow struct {
	Hours     string   `json:"hours,omitempty" yaml:"hours,omitempty"`       // "09:00-18:00"
	Days      []string `json:"days,omitempty" yaml:"days,omitempty"`         // ["saturday", "sunday"]
	Timezone  string   `json:"timezone,omitempty" yaml:"timezone,omitempty"` // "America/New_York"
	Threshold int      `json:"threshold" yaml:"threshold"`                   // Risk score threshold
}

// Defaults defines default behavior when no rules match
type Defaults struct {
	Action         Action        `json:"action" yaml:"action"`
	ApprovalExpiry time.Duration `json:"approval_expiry" yaml:"approval_expiry"`
	MaxPending     int           `json:"max_pending" yaml:"max_pending"`
}

// ToolCategory represents known tool categories
type ToolCategory string

const (
	CategoryFileRead  ToolCategory = "file_read"
	CategoryFileWrite ToolCategory = "file_write"
	CategoryShell     ToolCategory = "shell_command"
	CategorySearch    ToolCategory = "search"
	CategoryNetwork   ToolCategory = "network"
	CategoryGit       ToolCategory = "git"
	CategoryUnknown   ToolCategory = "unknown"
)

// RiskCondition represents known risk conditions
type RiskCondition string

const (
	RiskTouchesSecrets  RiskCondition = "touches_secrets"
	RiskDestructive     RiskCondition = "destructive"
	RiskExternalNetwork RiskCondition = "external_network"
	RiskModifiesGit     RiskCondition = "modifies_git"
	RiskWritesConfig    RiskCondition = "writes_config"
	RiskInstallsPackage RiskCondition = "installs_packages"
)

// EvaluationResult contains the result of policy evaluation
type EvaluationResult struct {
	RequiresApproval bool      `json:"requires_approval"`
	RiskScore        int       `json:"risk_score"`
	RiskReasons      []string  `json:"risk_reasons"`
	MatchedRule      string    `json:"matched_rule,omitempty"`
	ExpiresAt        time.Time `json:"expires_at"`
	Decision         Action    `json:"decision"`
}

// ToolCall represents a tool invocation to be evaluated
type ToolCall struct {
	Name      string         `json:"name"`
	Input     map[string]any `json:"input"`
	SessionID string         `json:"session_id"`
	Category  ToolCategory   `json:"category,omitempty"`
}

// PendingApproval represents a tool call awaiting approval
type PendingApproval struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	ToolName    string    `json:"tool_name"`
	ToolInput   string    `json:"tool_input"` // JSON encoded
	RiskScore   int       `json:"risk_score"`
	RiskReasons []string  `json:"risk_reasons"`
	Status      string    `json:"status"` // pending, approved, rejected, expired, auto
	DecidedBy   string    `json:"decided_by,omitempty"`
	DecidedAt   time.Time `json:"decided_at,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// AuditEntry represents a logged tool execution
type AuditEntry struct {
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
