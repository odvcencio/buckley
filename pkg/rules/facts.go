package rules

// TaskFacts carries signals for complexity/planning decisions.
// Tags are flat keys matching the .arb rule variable names directly.
type TaskFacts struct {
	WordCount    int     `arb:"word_count"`
	HasFilePaths bool    `arb:"has_file_paths"`
	HasQuestions bool    `arb:"has_questions"`
	Ambiguity    float64 `arb:"ambiguity"`
}

// CommandFacts carries signals for risk detection.
type CommandFacts struct {
	Command       string   `arb:"command"`
	Args          []string `arb:"args"`
	WorkDir       string   `arb:"work_dir"`
	IsGitOp       bool     `arb:"is_git_op"`
	IsForceOp     bool     `arb:"is_force_op"`
	IsRmRecursive bool     `arb:"is_rm_recursive"`
}

// ContextFacts carries signals for compaction triggers.
type ContextFacts struct {
	TokenCount   int     `arb:"token_count"`
	MaxTokens    int     `arb:"max_tokens"`
	UsageRatio   float64 `arb:"usage_ratio"`
	MessageCount int     `arb:"message_count"`
}

// RetryFacts carries signals for retry/dead-end detection.
type RetryFacts struct {
	Attempt       int    `arb:"attempt"`
	MaxAttempts   int    `arb:"max_attempts"`
	SameError     bool   `arb:"same_error"`
	NoFileChanges bool   `arb:"no_file_changes"`
	ErrorText     string `arb:"error_text"`
}

// GTSFacts carries signals for gts context escalation.
// Uses dot-notation tags for nested map construction.
type GTSFacts struct {
	TaskType   string `arb:"task.type"`
	FileCount  int    `arb:"task.file_count"`
	RepoSizeMB int    `arb:"repo.size_mb"`
	IndexFresh bool   `arb:"index.fresh"`
	LastOOM    bool   `arb:"gts.last_oom"`
}

// ModelFacts carries signals for model routing (used to build strategy maps).
type ModelFacts struct {
	ModelID           string `arb:"model_id"`
	Provider          string `arb:"provider"`
	SupportsReasoning bool   `arb:"supports_reasoning"`
	ContextWindow     int    `arb:"context_window"`
}

// ApprovalFacts carries signals for approval gate (used to build strategy maps).
type ApprovalFacts struct {
	Mode        string `arb:"mode"`
	RiskLevel   string `arb:"risk_level"`
	TrustedPath bool   `arb:"trusted_path"`
	ToolName    string `arb:"tool_name"`
}

// RoutingFacts carries signals for model selection (used to build strategy maps).
type RoutingFacts struct {
	Phase             string `arb:"phase"`
	ModelID           string `arb:"model_id"`
	Provider          string `arb:"provider"`
	SupportsReasoning bool   `arb:"supports_reasoning"`
}

// ReasoningFacts carries signals for reasoning mode (used to build strategy maps).
type ReasoningFacts struct {
	Config              string `arb:"config"`
	Phase               string `arb:"phase"`
	ModelSupportsReason bool   `arb:"model_supports_reasoning"`
}

// OneshotFacts carries signals for oneshot command configuration.
type OneshotFacts struct {
	Command    string `arb:"command"`
	TokenCount int    `arb:"token_count"`
}

// SpawningFacts carries signals for subagent configuration decisions.
type SpawningFacts struct {
	TaskType     string `arb:"task.type"`
	FileCount    int    `arb:"task.file_count"`
	EstTokens    int    `arb:"task.estimated_tokens"`
	SubtaskCount int    `arb:"task.subtask_count"`
	ParentWeight string `arb:"parent.weight"`
	Depth        int    `arb:"spawn.depth"`
}

// CoordinatorFacts carries signals for coordinator budget tuning.
type CoordinatorFacts struct {
	SubtaskCount    int `arb:"task.subtask_count"`
	EstimatedTokens int `arb:"task.estimated_tokens"`
	PlanStepCount   int `arb:"task.plan_step_count"`
}

// ToolBudgetFacts carries signals for tool access governance.
type ToolBudgetFacts struct {
	ToolTier  string `arb:"tool_tier"`
	ToolCalls int    `arb:"agent.tool_calls"`
	MaxCalls  int    `arb:"agent.max_tool_calls"`
}

// RolePermissionFacts carries signals for role-based tool access.
type RolePermissionFacts struct {
	Role string `arb:"role"` // "coordinator", "subagent"
	Tier string `arb:"tier"` // "read_only", "standard", "full" (subagents only)
}
