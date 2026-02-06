package machine

// Action represents a side effect the runtime must perform.
// The Machine produces Actions; the Runtime executes them.
type Action interface {
	actionType() string
}

type CallModel struct {
	Messages        []any  // model.Message - kept as any to avoid import
	Tools           []any  // tool definitions
	Steering        string // injected user steering, if any
	EnableReasoning bool
}

func (CallModel) actionType() string { return "call_model" }

type ExecuteToolBatch struct {
	Calls []ToolCallRequest
}

func (ExecuteToolBatch) actionType() string { return "execute_tools" }

type AcquireLockBatch struct {
	Locks []LockRequest
}

type LockRequest struct {
	Path string
	Mode LockMode
}

func (AcquireLockBatch) actionType() string { return "acquire_locks" }

type ReleaseLocks struct{}

func (ReleaseLocks) actionType() string { return "release_locks" }

type Compact struct {
	Messages []any
}

func (Compact) actionType() string { return "compact" }

type SpawnSubAgent struct {
	Task     string
	Modality Modality
	Model    string
	Tools    []string // allowed tool names, nil = all
	Spec     string   // for Ralph: verification spec
	Budget   Budget
}

func (SpawnSubAgent) actionType() string { return "spawn_sub_agent" }

type Budget struct {
	MaxIterations int
	MaxCost       float64
	MaxWallTime   int // seconds
}

type CommitChanges struct{}

func (CommitChanges) actionType() string { return "commit_changes" }

type RunVerification struct {
	Command string
}

func (RunVerification) actionType() string { return "run_verification" }

type ResetContext struct {
	Spec      string
	LastError string
	Iteration int
}

func (ResetContext) actionType() string { return "reset_context" }

type EmitResult struct {
	Content    string
	TokensUsed int
}

func (EmitResult) actionType() string { return "emit_result" }

type EmitError struct {
	Err           error
	RetryStrategy string // "none", "retry", "escalate"
}

func (EmitError) actionType() string { return "emit_error" }

type DelegateToSubAgents struct {
	Tasks []SubAgentTask
}

type SubAgentTask struct {
	Task     string
	Tools    []string
	Model    string
	Modality Modality
	Spec     string
	Budget   Budget
}

func (DelegateToSubAgents) actionType() string { return "delegate_to_sub_agents" }

type ReviewSubAgentOutput struct {
	Results  []SubAgentResult
	Criteria string
}

func (ReviewSubAgentOutput) actionType() string { return "review_output" }

type SaveCheckpoint struct {
	Iteration int
}

func (SaveCheckpoint) actionType() string { return "save_checkpoint" }
