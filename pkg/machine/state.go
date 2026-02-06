package machine

// State represents a machine's current lifecycle state.
type State int

const (
	// Shared states (all modalities)
	Idle               State = iota
	CallingModel             // LLM request in flight
	ProcessingResponse       // Parsing model output, identifying tool calls
	AcquiringLocks           // Requesting file locks before tool execution
	ExecutingTools           // Running tools
	WaitingOnLock            // Blocked on file lock held by another agent
	Compacting               // Context summarization in progress

	// RLM states
	Delegating            // Coordinator dispatching tasks to sub-agents
	AwaitingSubAgents     // Waiting for child machines to finish
	Reviewing             // Evaluating sub-agent output
	Rejecting             // Sub-agent output failed review
	Synthesizing          // Combining approved results
	CheckpointingProgress // Persisting iteration state

	// Ralph states
	CommittingWork  // Auto-commit after mutations (uses commit tool)
	Verifying       // Running acceptance criteria
	ResettingContext // Wipe conversation, preserve spec + verification result

	// Terminal states
	Done  // Completed with result
	Error // Failed with error info
)

func (s State) String() string {
	switch s {
	case Idle:
		return "idle"
	case CallingModel:
		return "calling_model"
	case ProcessingResponse:
		return "processing_response"
	case AcquiringLocks:
		return "acquiring_locks"
	case ExecutingTools:
		return "executing_tools"
	case WaitingOnLock:
		return "waiting_on_lock"
	case Compacting:
		return "compacting"
	case Delegating:
		return "delegating"
	case AwaitingSubAgents:
		return "awaiting_sub_agents"
	case Reviewing:
		return "reviewing"
	case Rejecting:
		return "rejecting"
	case Synthesizing:
		return "synthesizing"
	case CheckpointingProgress:
		return "checkpointing_progress"
	case CommittingWork:
		return "committing_work"
	case Verifying:
		return "verifying"
	case ResettingContext:
		return "resetting_context"
	case Done:
		return "done"
	case Error:
		return "error"
	default:
		return "unknown"
	}
}

// IsTerminal returns true if this is a terminal state.
func (s State) IsTerminal() bool {
	return s == Done || s == Error
}

// Modality identifies which execution mode a machine runs in.
type Modality int

const (
	Classic Modality = iota
	RLM
	Ralph
)

func (m Modality) String() string {
	switch m {
	case Classic:
		return "classic"
	case RLM:
		return "rlm"
	case Ralph:
		return "ralph"
	default:
		return "unknown"
	}
}
