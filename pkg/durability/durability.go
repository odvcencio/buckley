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

// NextBatchV2Request is used only by GoalWorkflowV5. ExcludedTaskIDs carries
// tasks whose child exhausted its generation-wide retry budget, so a terminal
// pull reports them incomplete instead of scheduling a fresh child workflow.
type NextBatchV2Request struct {
	RunID           string   `json:"run_id"`
	Deferred        []string `json:"deferred,omitempty"`
	ExcludedTaskIDs []string `json:"excluded_task_ids,omitempty"`
	MaxParallel     int      `json:"max_parallel,omitempty"`
}

// NextBatchResponse lists tasks that may run concurrently. Done means
// the queue is empty. A task with no claims always arrives alone.
type NextBatchResponse struct {
	Tasks []TaskClaim `json:"tasks,omitempty"`
	Done  bool        `json:"done"`
	// IncompleteTaskIDs is populated on a new-generation terminal pull so
	// blocked, parked, or deferred tasks that were not selected into a child
	// workflow still make incompleteness explicit in the goal result.
	IncompleteTaskIDs []string `json:"incomplete_task_ids,omitempty"`
}

// NextBatchV2Runner is the GoalWorkflowV5 scheduler capability. The original
// NextBatch method remains frozen for V1-V4 workflow histories.
type NextBatchV2Runner interface {
	NextBatchV2(ctx context.Context, req NextBatchV2Request) (NextBatchResponse, error)
}

// TurnRequest asks the host to run exactly one turn. Generation and
// TurnIndex form the stable turn identity; a backend retry re-sends the
// same request so completed steps replay from evidence (Phase 0).
type TurnRequest struct {
	RunID  string `json:"run_id"`
	TaskID string `json:"task_id"`
	// WorkspaceRoot binds the activity to the canonical directory recorded
	// for the goal. Workers reject a request for any other workspace.
	WorkspaceRoot string          `json:"workspace_root"`
	Generation    int             `json:"generation"`
	TurnIndex     int             `json:"turn_index"`
	Drive         json.RawMessage `json:"drive,omitempty"`
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
	// BlockerCategory and BlockerReasonCode are governed, bounded labels.
	// They deliberately exclude the human blocker text and any provider
	// response body from workflow history.
	BlockerCategory   string `json:"blocker_category,omitempty"`
	BlockerReasonCode string `json:"blocker_reason_code,omitempty"`
	// RetryAfterUnixMS is an absolute UTC timestamp. RetryOrdinal is the
	// one-based source-turn ordinal, which lets a workflow derive a stable
	// wait identity without carrying a blocker body.
	RetryAfterUnixMS int64 `json:"retry_after_unix_ms,omitempty"`
	RetryOrdinal     int   `json:"retry_ordinal,omitempty"`
	// WaitID is assigned by the V5 workflow from the workflow/task identity,
	// monotonic wait ordinal, and the checkpoint-bound blocker identity. A
	// TaskRunner-provided value is never trusted or copied into history.
	WaitID string `json:"wait_id,omitempty"`
	// ExpectedCheckpointID, ExpectedCheckpointVersion, and BlockerDigest bind a
	// retry wake to the exact blocked checkpoint it observed. The digest is a
	// SHA-256 of the blocker record; the blocker body itself stays out of
	// workflow history.
	ExpectedCheckpointID      string `json:"expected_checkpoint_id,omitempty"`
	ExpectedCheckpointVersion int    `json:"expected_checkpoint_version,omitempty"`
	BlockerDigest             string `json:"blocker_digest,omitempty"`
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
	// RecordApprovalWait and ResolveApproval bound one durable approval:
	// the wait lands on the ledger before the workflow blocks, and the
	// resolution lands before the workflow acts on it. An approved
	// resolution unparks the task.
	RecordApprovalWait(ctx context.Context, wait ApprovalWait) error
	ResolveApproval(ctx context.Context, resolution ApprovalResolution) error
}

// LegacyTaskRunner optionally narrows compatibility handling for in-flight
// TaskWorkflowV1/V2 histories whose serialized turn input predates
// WorkspaceRoot. Runners that do not implement it receive the same request
// through TaskRunner.RunTurn, preserving the pre-V4 public interface.
type LegacyTaskRunner interface {
	RunLegacyTurn(ctx context.Context, req TurnRequest) (TurnResponse, error)
}

// DurableTurnRunner is the V3 activity capability. It adds a whole-turn
// receipt boundary without changing the V1/V2 activity contract used by
// in-flight workflow histories.
type DurableTurnRunner interface {
	RunTurnV3(ctx context.Context, req TurnRequest) (TurnResponse, error)
}

// RetryWait is the compact, transport-neutral identity of one retry timer.
// Dapr history stores only these bounded fields; Buckley's run ledger owns
// the corresponding audit facts and the checkpoint remains the source of the
// next drive snapshot.
type RetryWait struct {
	RunID                     string `json:"run_id"`
	TaskID                    string `json:"task_id"`
	WorkflowInstanceID        string `json:"workflow_instance_id"`
	WaitID                    string `json:"wait_id"`
	Category                  string `json:"category,omitempty"`
	ReasonCode                string `json:"reason_code,omitempty"`
	RetryAfterUnixMS          int64  `json:"retry_after_unix_ms"`
	Ordinal                   int    `json:"ordinal"`
	ExpectedCheckpointID      string `json:"expected_checkpoint_id"`
	ExpectedCheckpointVersion int    `json:"expected_checkpoint_version"`
	BlockerDigest             string `json:"blocker_digest"`
}

// RetryWaiter is an optional extension implemented by durable activity
// hosts. Keeping it separate from TaskRunner preserves V1–V4 adapter source
// compatibility while V5 can require explicit audit and wake semantics.
type RetryWaiter interface {
	RecordRetryWaiting(ctx context.Context, wait RetryWait) error
	WakeRetry(ctx context.Context, wait RetryWait) error
	ResolveRetry(ctx context.Context, wait RetryWait) error
}

// Retry wake dispositions are deliberately small workflow-history values.
const (
	RetryWakeApplied        = "applied"
	RetryWakeAlreadyApplied = "already_applied"
	RetryWakeStale          = "stale"
)

// RetryWakeResult reports whether the checkpoint-bound wake was applied,
// replayed after activity-ack loss, or rejected as stale.
type RetryWakeResult struct {
	Disposition string `json:"disposition"`
	TaskStatus  string `json:"task_status,omitempty"`
}

// RetryWakeResolver is the disposition-bearing wake capability used only by
// TaskWorkflowV4. RetryWaiter.WakeRetry remains frozen for old registrations.
type RetryWakeResolver interface {
	WakeRetryV2(ctx context.Context, wait RetryWait) (RetryWakeResult, error)
}

// GoalFinalizer is the optional V4 lifecycle capability. Keeping it separate
// preserves source compatibility for pre-V4 TaskRunner adapters; V4 execution
// fails explicitly when a registered runner cannot reconcile terminal state.
type GoalFinalizer interface {
	FinalizeGoal(ctx context.Context, finalization GoalFinalization) error
}

// GoalStart describes one durable goal execution. MaxYields bounds how
// often one task may yield in_progress before it defers to a later run.
// MaxParallel bounds the fan-out of claim-independent tasks; zero or
// one keeps the sequential V1 behavior. ApprovalWaitMS, when positive,
// holds a parked task on a durable external-event wait for that many
// milliseconds before parking it for good.
type GoalStart struct {
	RunID          string `json:"run_id"`
	WorkspaceRoot  string `json:"workspace_root"`
	MaxYields      int    `json:"max_yields,omitempty"`
	MaxParallel    int    `json:"max_parallel,omitempty"`
	ApprovalWaitMS int64  `json:"approval_wait_ms,omitempty"`
	// ResumeAfterWorkflowInstanceID is the immutable incomplete-generation
	// fence read from Buckley's canonical run ledger. When set, the backend may
	// schedule only the immediately following generation. It is scheduler
	// control state; resumed workflows inherit execution settings from
	// generation zero instead of serializing this field into their input.
	ResumeAfterWorkflowInstanceID string `json:"-"`
}

// GoalFinalization asks the activity host to reconcile a terminal workflow
// with Buckley's canonical run lifecycle. Failure is empty for a normal
// workflow completion.
type GoalFinalization struct {
	RunID              string `json:"run_id"`
	WorkspaceRoot      string `json:"workspace_root"`
	WorkflowInstanceID string `json:"workflow_instance_id"`
	// Incomplete means the workflow intentionally stopped after bounded
	// yields with resumable tasks still open. The activity must not seal the
	// canonical run in that case.
	Incomplete bool   `json:"incomplete,omitempty"`
	Failure    string `json:"failure,omitempty"`
}

// ApprovalEventName is the durable external event a parked task waits
// on. Approvals target the task's child workflow instance ID, recorded
// in the durable.approval_waiting ledger event.
const ApprovalEventName = "approval"

// ApprovalDecision resolves one durable approval wait.
type ApprovalDecision struct {
	Approved bool   `json:"approved"`
	Reason   string `json:"reason,omitempty"`
}

// ApprovalWait records that a task workflow began a durable approval
// wait; Resolution records how it ended.
type ApprovalWait struct {
	RunID              string `json:"run_id"`
	TaskID             string `json:"task_id"`
	WorkflowInstanceID string `json:"workflow_instance_id"`
	Reason             string `json:"reason,omitempty"`
}

// ApprovalResolution outcomes.
const (
	ApprovalApproved = "approved"
	ApprovalDenied   = "denied"
	ApprovalTimedOut = "timed_out"
)

// ApprovalResolution closes one approval wait. Outcome is approved,
// denied, or timed_out. An approved resolution also unparks the task.
type ApprovalResolution struct {
	RunID              string `json:"run_id"`
	TaskID             string `json:"task_id"`
	WorkflowInstanceID string `json:"workflow_instance_id"`
	Outcome            string `json:"outcome"`
	Reason             string `json:"reason,omitempty"`
}

// TaskOutcome summarizes one task workflow.
type TaskOutcome struct {
	TaskID         string  `json:"task_id"`
	Status         string  `json:"status"`
	Decision       string  `json:"decision,omitempty"`
	Turns          int     `json:"turns"`
	SpentUSD       float64 `json:"spent_usd"`
	RetryExhausted bool    `json:"retry_exhausted,omitempty"`
	// GenerationDeferred means a checkpoint-bound retry wake was superseded
	// by newer nonterminal state. GoalWorkflowV5 excludes the task for the
	// rest of this generation so a new child cannot reset its retry budget.
	GenerationDeferred bool `json:"generation_deferred,omitempty"`
}

// GoalResult is the goal workflow's output.
type GoalResult struct {
	Tasks []TaskOutcome `json:"tasks"`
	// Status is set to incomplete when bounded yields defer one or more
	// resumable tasks. It stays empty for legacy and ordinary terminal
	// results so existing workflow output remains wire-compatible.
	Status        string   `json:"status,omitempty"`
	DeferredTasks []string `json:"deferred_tasks,omitempty"`
}

// Goal result statuses.
const GoalResultIncomplete = "incomplete"

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
	// RaiseApproval delivers an approval decision to the task workflow
	// instance recorded in the durable.approval_waiting event.
	RaiseApproval(ctx context.Context, workflowInstanceID string, decision ApprovalDecision) error
	Close() error
}
