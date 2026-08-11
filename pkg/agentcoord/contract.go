// Package agentcoord defines the dependency-free child-agent coordination
// domain contract. It deliberately has no dependency on the existing
// orchestrator package, which currently reaches tool adapters that include the
// local subagent implementation.
package agentcoord

import (
	"context"
	"time"
)

// RunState is the provider-neutral lifecycle of a coordinated child agent.
// Resumable is deliberately distinct from failed: it means the durable record
// survived but no live adapter is currently attached to continue it.
type RunState string

const (
	RunQueued    RunState = "queued"
	RunRunning   RunState = "running"
	RunCompleted RunState = "completed"
	RunFailed    RunState = "failed"
	RunCancelled RunState = "cancelled"
	RunBlocked   RunState = "blocked"
	RunResumable RunState = "resumable"
)

// Terminal reports whether a run can no longer accept work from its current
// attempt. A resumable run is intentionally non-terminal.
func (s RunState) Terminal() bool {
	switch s {
	case RunCompleted, RunFailed, RunCancelled, RunBlocked:
		return true
	default:
		return false
	}
}

// Budget declares the bounded resources available to one child. Zero values
// mean that the corresponding bound is not specified by this contract. Policy
// adapters may narrow these values; they must not silently broaden them.
type Budget struct {
	MaxToolCalls     int     `json:"max_tool_calls,omitempty"`
	MaxModelRequests int     `json:"max_model_requests,omitempty"`
	MaxElapsedSecond int     `json:"max_elapsed_seconds,omitempty"`
	MaxCostUSD       float64 `json:"max_cost_usd,omitempty"`
}

// TaskSpec is the fully resolved input for a child run. It carries the
// execution contract rather than a provider-specific command line so the same
// request can be served by a local process, ACP peer, durable workflow, or
// provider-native agent adapter.
type TaskSpec struct {
	// ID is the stable task identity; RunID, when supplied, requests a stable
	// child-run identity for a retry or a durable scheduler.
	ID              string            `json:"id,omitempty"`
	RunID           string            `json:"run_id,omitempty"`
	ParentRunID     string            `json:"parent_run_id,omitempty"`
	ParentSessionID string            `json:"parent_session_id,omitempty"`
	Dependencies    []string          `json:"dependencies,omitempty"`
	Agent           string            `json:"agent,omitempty"`
	Spec            string            `json:"spec,omitempty"`
	Task            string            `json:"task"`
	Persona         string            `json:"persona,omitempty"`
	Model           string            `json:"model,omitempty"`
	Tier            string            `json:"tier,omitempty"`
	Effort          string            `json:"effort,omitempty"`
	SystemPrompt    string            `json:"system_prompt,omitempty"`
	AllowedTools    []string          `json:"allowed_tools,omitempty"`
	StepCap         int               `json:"step_cap,omitempty"`
	TimeoutSeconds  int               `json:"timeout_seconds,omitempty"`
	Budget          Budget            `json:"budget,omitempty"`
	WorkspaceClaims []string          `json:"workspace_claims,omitempty"`
	Isolation       string            `json:"isolation,omitempty"`
	OutputSchema    string            `json:"output_schema,omitempty"`
	ApprovalPosture string            `json:"approval_posture,omitempty"`
	DelegationDepth int               `json:"delegation_depth,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

// Result is the structured terminal result of one child attempt. Output stays
// bounded; full replayable bodies belong in EvidenceRefs.
type Result struct {
	Summary      string   `json:"summary,omitempty"`
	Error        string   `json:"error,omitempty"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
}

// Run is the projection shared by every child-agent adapter.
type Run struct {
	ID              string    `json:"id"`
	ParentRunID     string    `json:"parent_run_id,omitempty"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	Task            TaskSpec  `json:"task"`
	State           RunState  `json:"state"`
	Adapter         string    `json:"adapter,omitempty"`
	PID             int       `json:"pid,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
	HeartbeatAt     time.Time `json:"heartbeat_at,omitempty"`
	Claims          []string  `json:"claims,omitempty"`
	MailboxCount    int       `json:"mailbox_count,omitempty"`
	Result          Result    `json:"result,omitempty"`
}

// RunFilter narrows a coordinator List request. Empty fields are not filters;
// callers may use it for a session, a parent, or a specific task.
type RunFilter struct {
	ParentRunID     string `json:"parent_run_id,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
	Limit           int    `json:"limit,omitempty"`
}

// Message is a durable mailbox entry. Delivery is explicit: queued means it
// is safely persisted but the active adapter has not claimed live delivery.
type Message struct {
	ID           string    `json:"id"`
	RunID        string    `json:"run_id"`
	From         string    `json:"from,omitempty"`
	To           string    `json:"to"`
	Kind         string    `json:"kind"`
	Content      string    `json:"content,omitempty"`
	Delivery     string    `json:"delivery"`
	EvidenceRefs []string  `json:"evidence_refs,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// ClaimRequest asks to reserve workspace resources for a child before it
// starts mutating. Resources use adapter-defined workspace-relative names.
type ClaimRequest struct {
	RunID     string   `json:"run_id"`
	Resources []string `json:"resources"`
}

// ClaimResult reports the resources that are now exclusively held.
type ClaimResult struct {
	RunID     string   `json:"run_id"`
	Resources []string `json:"resources"`
}

// Coordinator is the domain port for all child-agent surfaces. Every
// implementation must retain durable run identity independently of a worker
// process, and must expose a queued delivery honestly when it cannot deliver a
// message into a live adapter.
type Coordinator interface {
	Spawn(ctx context.Context, spec TaskSpec) (Run, error)
	List(ctx context.Context, filter RunFilter) ([]Run, error)
	Status(ctx context.Context, id string) (Run, error)
	Wait(ctx context.Context, id string) (Run, error)
	Steer(ctx context.Context, id, content string) (Message, error)
	Send(ctx context.Context, message Message) (Message, error)
	Messages(ctx context.Context, id string) ([]Message, error)
	Cancel(ctx context.Context, id, reason string) (Run, error)
	Claim(ctx context.Context, request ClaimRequest) (ClaimResult, error)
	Release(ctx context.Context, request ClaimRequest, reason string) error
}

// Agent-prefixed aliases keep the contract self-describing at call sites that
// coordinate several kinds of runs while preserving concise core type names.
type (
	AgentRunState     = RunState
	AgentBudget       = Budget
	AgentTaskSpec     = TaskSpec
	AgentResult       = Result
	AgentRun          = Run
	AgentRunFilter    = RunFilter
	AgentMessage      = Message
	AgentClaimRequest = ClaimRequest
	AgentClaimResult  = ClaimResult
	AgentCoordinator  = Coordinator
)

const (
	AgentRunQueued    = RunQueued
	AgentRunRunning   = RunRunning
	AgentRunCompleted = RunCompleted
	AgentRunFailed    = RunFailed
	AgentRunCancelled = RunCancelled
	AgentRunBlocked   = RunBlocked
	AgentRunResumable = RunResumable
)
