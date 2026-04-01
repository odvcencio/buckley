package rules

import "reflect"

// structToMap converts a facts struct to a map using `arb` struct tags.
// Fields without an arb tag are skipped.
func structToMap(v any) map[string]any {
	rv := reflect.ValueOf(v)
	rt := rv.Type()
	m := make(map[string]any, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("arb")
		if tag == "" {
			continue
		}
		m[tag] = rv.Field(i).Interface()
	}
	return m
}

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

// PermissionEscalationFacts — distinct from existing EscalationFacts (failure escalation).
type PermissionEscalationFacts struct {
	ToolName     string  `arb:"tool"`
	CurrentTier  string  `arb:"current_tier"`
	RequiredTier string  `arb:"required_tier"`
	AgentRole    string  `arb:"role"`
	Reason       string  `arb:"reason"`
	RiskScore    float64 `arb:"risk_score"`
}

func (f PermissionEscalationFacts) ToMap() map[string]any { return structToMap(f) }

type SandboxFacts struct {
	Tool      string  `arb:"tool"`
	Role      string  `arb:"role"`
	RiskScore float64 `arb:"risk_score"`
}

func (f SandboxFacts) ToMap() map[string]any { return structToMap(f) }

type DelegationFacts struct {
	Tool            string `arb:"tool"`
	Role            string `arb:"role"`
	Tier            string `arb:"tier"`
	DelegationDepth int    `arb:"delegation_depth"`
}

func (f DelegationFacts) ToMap() map[string]any { return structToMap(f) }

type CostFacts struct {
	SessionSpendUSD   float64 `arb:"session_spend"`
	DailySpendUSD     float64 `arb:"daily_spend"`
	MonthlySpendUSD   float64 `arb:"monthly_spend"`
	SessionBudgetUSD  float64 `arb:"session_budget"`
	DailyBudgetUSD    float64 `arb:"daily_budget"`
	MonthlyBudgetUSD  float64 `arb:"monthly_budget"`
	BudgetUtilization float64 `arb:"budget_util"`
	CurrentModelCost  float64 `arb:"current_model_cost"`
	TurnCount         int     `arb:"turn_count"`
}

func (f CostFacts) ToMap() map[string]any { return structToMap(f) }

type RateLimitFacts struct {
	Provider          string  `arb:"provider"`
	RequestsPerMin    int     `arb:"rpm"`
	TokensPerMin      int     `arb:"tpm"`
	CurrentRPM        int     `arb:"current_rpm"`
	CurrentTPM        int     `arb:"current_tpm"`
	Utilization       float64 `arb:"utilization"`
	ConsecutiveErrors int     `arb:"consecutive_errors"`
}

func (f RateLimitFacts) ToMap() map[string]any { return structToMap(f) }

type CompactionFacts struct {
	TokenUtilization float64 `arb:"token_util"`
	MessageCount     int     `arb:"message_count"`
	PendingWorkCount int     `arb:"pending_work_count"`
	UniqueFilesCount int     `arb:"unique_files"`
	ToolsUsedCount   int     `arb:"tools_used"`
	TaskType         string  `arb:"task_type"`
	HasUnsavedWork   bool    `arb:"has_unsaved_work"`
}

func (f CompactionFacts) ToMap() map[string]any { return structToMap(f) }

type SessionMemoryFacts struct {
	MessageCount     int     `arb:"message_count"`
	TokenUtilization float64 `arb:"token_util"`
	SessionAgeHours  float64 `arb:"session_age_hours"`
	HasUnsavedWork   bool    `arb:"has_unsaved_work"`
}

func (f SessionMemoryFacts) ToMap() map[string]any { return structToMap(f) }

type PromptAssemblyFacts struct {
	ModelTier        string `arb:"model_tier"`
	TaskType         string `arb:"task_type"`
	GitDiffLines     int    `arb:"git_diff_lines"`
	InstructionChars int    `arb:"instruction_chars"`
	GTSAvailable     bool   `arb:"gts_available"`
}

func (f PromptAssemblyFacts) ToMap() map[string]any { return structToMap(f) }

type PhaseFacts struct {
	Phase          string `arb:"phase"`
	Environment    string `arb:"environment"`
	ExecutionMode  string `arb:"execution_mode"`
	GTSBinary      bool   `arb:"gts_binary"`
	PermissionTier string `arb:"permission_tier"`
}

func (f PhaseFacts) ToMap() map[string]any { return structToMap(f) }

type TimeoutFacts struct {
	Tool string `arb:"tool"`
	Role string `arb:"role"`
	Tier string `arb:"tier"`
}

func (f TimeoutFacts) ToMap() map[string]any { return structToMap(f) }

type PoolFacts struct {
	Role         string  `arb:"role"`
	TaskType     string  `arb:"task_type"`
	BudgetUtil   float64 `arb:"budget_util"`
	SubtaskCount int     `arb:"subtask_count"`
}

func (f PoolFacts) ToMap() map[string]any { return structToMap(f) }

type ConditionFacts struct {
	ConditionType string `arb:"condition_type"`
}

func (f ConditionFacts) ToMap() map[string]any { return structToMap(f) }

type ModeFacts struct {
	RequestedMode string `arb:"requested_mode"`
	HasTTY        bool   `arb:"has_tty"`
	HasPrompt     bool   `arb:"has_prompt"`
	Environment   string `arb:"environment"`
	OutputFormat  string `arb:"output_format"`
}

func (f ModeFacts) ToMap() map[string]any { return structToMap(f) }

type TaskIntakeFacts struct {
	TaskID      string  `arb:"task_id"`
	Priority    int     `arb:"priority"`
	Channel     string  `arb:"channel"`
	ActiveTasks int     `arb:"active_tasks"`
	BudgetUtil  float64 `arb:"budget_util"`
}

func (f TaskIntakeFacts) ToMap() map[string]any { return structToMap(f) }

type ChannelFacts struct {
	Channel     string `arb:"channel"`
	Environment string `arb:"environment"`
}

func (f ChannelFacts) ToMap() map[string]any { return structToMap(f) }
