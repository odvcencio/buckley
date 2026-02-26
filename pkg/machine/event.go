package machine

// Event is the interface all machine-internal events implement.
type Event interface {
	eventType() string
}

// --- Model events ---

type ModelCompleted struct {
	Content      string
	FinishReason string // "end_turn", "tool_use", "max_tokens"
	ToolCalls    []ToolCallRequest
	TokensUsed   int
	Reasoning    string
}

func (ModelCompleted) eventType() string { return "model_completed" }

type ModelFailed struct {
	Err       error
	Retryable bool
}

func (ModelFailed) eventType() string { return "model_failed" }

// --- Tool events ---

type ToolsCompleted struct {
	Results []ToolCallResult
}

func (ToolsCompleted) eventType() string { return "tools_completed" }

type ToolCallRequest struct {
	ID     string
	Name   string
	Params map[string]any
	Paths  []string // file paths this tool will touch
	Mode   LockMode // read or write
}

type ToolCallResult struct {
	ID      string
	Name    string
	Result  string
	Success bool
	Err     error
}

// --- Lock events ---

type LockMode int

const (
	LockRead LockMode = iota
	LockWrite
)

type LockAcquired struct {
	Path string
	Mode LockMode
}

func (LockAcquired) eventType() string { return "lock_acquired" }

type LocksAcquired struct{}

func (LocksAcquired) eventType() string { return "locks_acquired" }

type LockWaiting struct {
	Path   string
	HeldBy string
	Mode   LockMode
}

func (LockWaiting) eventType() string { return "lock_waiting" }

type LockReleased struct {
	Path string
}

func (LockReleased) eventType() string { return "lock_released" }

// --- Context events ---

type CompactionCompleted struct {
	TokensSaved int
}

func (CompactionCompleted) eventType() string { return "compaction_completed" }

type ContextPressure struct {
	UsedTokens int
	MaxTokens  int
	Ratio      float64
}

func (ContextPressure) eventType() string { return "context_pressure" }

// --- User events ---

type UserSteering struct {
	Content string
}

func (UserSteering) eventType() string { return "user_steering" }

type UserInput struct {
	Content string
}

func (UserInput) eventType() string { return "user_input" }

// --- RLM events ---

type SubAgentsCompleted struct {
	Results []SubAgentResult
}

func (SubAgentsCompleted) eventType() string { return "sub_agents_completed" }

type SubAgentResult struct {
	AgentID       string
	Summary       string
	ScratchpadKey string
	TokensUsed    int
	Success       bool
}

type ReviewResult struct {
	Passed      bool
	Reason      string
	Attempt     int
	MaxAttempts int
}

func (ReviewResult) eventType() string { return "review_result" }

type SynthesisCompleted struct {
	Content string
}

func (SynthesisCompleted) eventType() string { return "synthesis_completed" }

type CheckpointSaved struct{}

func (CheckpointSaved) eventType() string { return "checkpoint_saved" }

// --- Ralph events ---

type CommitCompleted struct {
	Hash    string
	Message string
}

func (CommitCompleted) eventType() string { return "commit_completed" }

type VerificationResult struct {
	Passed  bool
	Output  string
	Command string
}

func (VerificationResult) eventType() string { return "verification_result" }

type ContextResetDone struct {
	Iteration int
	LastError string
}

func (ContextResetDone) eventType() string { return "context_reset_done" }

// --- Lifecycle events ---

type Cancelled struct{}

func (Cancelled) eventType() string { return "cancelled" }
