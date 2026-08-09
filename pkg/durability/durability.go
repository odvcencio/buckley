// Package durability defines the ports a durable workflow backend uses
// to drive Buckley goals (spec.durable-execution-dapr, Phase 1). The
// package is domain-level: it carries identifiers, compact snapshots,
// and small counters, never provider bodies and never a backend SDK
// type. Adapters live below it (pkg/durability/dapr) and hosts implement
// TaskRunner (pkg/durability/goalrunner).
package durability

import (
	"context"
	"encoding/json"
)

// Backend names for the execution.durable_backend configuration key.
const (
	BackendLocal = "local"
	BackendDapr  = "dapr"
)

// ResumeSeed is the durable starting state of one task drive: the
// checkpoint generation and an opaque drive snapshot owned by the host.
type ResumeSeed struct {
	Generation int             `json:"generation"`
	Drive      json.RawMessage `json:"drive,omitempty"`
}

// NextTaskRequest asks the host's scheduler for the next runnable task.
// Deferred lists tasks the workflow has seen yield twice; the host
// excludes them, mirroring the local Drain rule.
type NextTaskRequest struct {
	RunID    string   `json:"run_id"`
	Deferred []string `json:"deferred,omitempty"`
}

// NextTaskResponse is one queue pull. Done means the queue is empty.
type NextTaskResponse struct {
	TaskID string `json:"task_id,omitempty"`
	Done   bool   `json:"done"`
}

// TaskClaim pairs a runnable task with the workspace paths it claims.
type TaskClaim struct {
	TaskID string   `json:"task_id"`
	Claims []string `json:"claims,omitempty"`
}

// NextBatchRequest asks for the next set of runnable tasks whose
// workspace claims are mutually independent (spec Phase 2 fan-out).
type NextBatchRequest struct {
	RunID       string   `json:"run_id"`
	Deferred    []string `json:"deferred,omitempty"`
	MaxParallel int      `json:"max_parallel,omitempty"`
}

// NextBatchResponse lists tasks that may run concurrently. Done means
// the queue is empty. A task with no claims always arrives alone.
type NextBatchResponse struct {
	Tasks []TaskClaim `json:"tasks,omitempty"`
	Done  bool        `json:"done"`
}

// TurnRequest asks the host to run exactly one turn. Generation and
// TurnIndex form the stable turn identity; a backend retry re-sends the
// same request so completed steps replay from evidence (Phase 0).
type TurnRequest struct {
	RunID      string          `json:"run_id"`
	TaskID     string          `json:"task_id"`
	Generation int             `json:"generation"`
	TurnIndex  int             `json:"turn_index"`
	Drive      json.RawMessage `json:"drive,omitempty"`
	// Drive-scoped counters the workflow accumulates as explicit state.
	ModelRequests  int   `json:"model_requests,omitempty"`
	ToolExecutions int   `json:"tool_executions,omitempty"`
	ElapsedMS      int64 `json:"elapsed_ms,omitempty"`
	// WorkflowInstanceID is projected into the run ledger so both
	// histories reconcile.
	WorkflowInstanceID string `json:"workflow_instance_id,omitempty"`
}

// TurnResponse reports one turn in scheduler terms. Kind uses the
// goalloop step kinds: continue, verify, checkpoint, yield, park,
// completed, blocked.
type TurnResponse struct {
	Kind         string          `json:"kind"`
	Decision     string          `json:"decision,omitempty"`
	Status       string          `json:"status"`
	Drive        json.RawMessage `json:"drive,omitempty"`
	TurnSpentUSD float64         `json:"turn_spent_usd,omitempty"`
	Rounds       int             `json:"rounds,omitempty"`
	ToolCalls    int             `json:"tool_calls,omitempty"`
}

// TaskRunner is the activity host: it owns every Buckley side effect a
// workflow schedules. Implementations must be safe for repeated calls
// with the same identifiers — the step journal makes re-execution a
// replay, not a duplicate.
type TaskRunner interface {
	ResumeSeed(ctx context.Context, runID, taskID string) (ResumeSeed, error)
	NextTask(ctx context.Context, req NextTaskRequest) (NextTaskResponse, error)
	NextBatch(ctx context.Context, req NextBatchRequest) (NextBatchResponse, error)
	RunTurn(ctx context.Context, req TurnRequest) (TurnResponse, error)
}

// GoalStart describes one durable goal execution. MaxYields bounds how
// often one task may yield in_progress before it defers to a later run.
// MaxParallel bounds the fan-out of claim-independent tasks; zero or
// one keeps the sequential V1 behavior.
type GoalStart struct {
	RunID       string `json:"run_id"`
	MaxYields   int    `json:"max_yields,omitempty"`
	MaxParallel int    `json:"max_parallel,omitempty"`
}

// TaskOutcome summarizes one task workflow.
type TaskOutcome struct {
	TaskID   string  `json:"task_id"`
	Status   string  `json:"status"`
	Decision string  `json:"decision,omitempty"`
	Turns    int     `json:"turns"`
	SpentUSD float64 `json:"spent_usd"`
}

// GoalResult is the goal workflow's output.
type GoalResult struct {
	Tasks []TaskOutcome `json:"tasks"`
}

// GoalStatus reports a workflow instance the caller waited on.
type GoalStatus struct {
	InstanceID    string     `json:"instance_id"`
	RuntimeStatus string     `json:"runtime_status"`
	Result        GoalResult `json:"result"`
	Failure       string     `json:"failure,omitempty"`
}

// Backend schedules durable goal workflows and hosts their activities.
// Health must fail fast with an actionable error when the runtime (for
// Dapr, the sidecar) is unreachable.
type Backend interface {
	Health(ctx context.Context) error
	StartWorker(ctx context.Context, runner TaskRunner) error
	StartGoal(ctx context.Context, start GoalStart) (instanceID string, err error)
	WaitForGoal(ctx context.Context, instanceID string) (GoalStatus, error)
	Close() error
}
