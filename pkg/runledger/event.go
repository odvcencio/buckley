// Package runledger implements Buckley's durable run ledger: an append-only,
// per-run sequence of typed events, plus the agent run tree, context
// receipts, task checkpoints, and metric samples that section 14 of the M31
// Context Fabric spec defines as the canonical (non-live) record of what an
// agent run did.
//
// The in-memory telemetry hub (pkg/telemetry, ADR 0008) remains the live
// fan-out mechanism and may drop events; it is not, and must not become, the
// source of truth. This package is that source of truth.
package runledger

import (
	"crypto/sha256"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// SchemaVersion identifies the Event envelope's JSON shape.
const SchemaVersion = "m31.run.event.v1"

// DurableTurnReceiptSchemaV1 identifies the receipt-bearing durable.turn
// payload written by the V3 turn activity. Receipt-less durable.turn events
// predate this schema and remain readable as legacy audit facts.
const DurableTurnReceiptSchemaV1 = "buckley.durable-turn-receipt.v1"

// Event is one durably recorded, immutable fact about an agent run (section
// 14.2). Once appended, an event is never mutated.
type Event struct {
	SchemaVersion string    `json:"schema_version"`
	ID            string    `json:"id"` // ULID
	Sequence      int64     `json:"sequence"`
	Type          string    `json:"type"`
	Timestamp     time.Time `json:"timestamp"`

	SessionID   string `json:"session_id"`
	RunID       string `json:"run_id"`
	ParentRunID string `json:"parent_run_id,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	AgentID     string `json:"agent_id,omitempty"`

	ModelID    string `json:"model_id,omitempty"`
	ProviderID string `json:"provider_id,omitempty"`
	Backend    string `json:"backend,omitempty"`

	SnapshotID  string   `json:"snapshot_id,omitempty"`
	EvidenceIDs []string `json:"evidence_ids,omitempty"`
	ReceiptIDs  []string `json:"receipt_ids,omitempty"`

	Payload   map[string]any `json:"payload,omitempty"`
	Redaction string         `json:"redaction_version"`
}

// NewEventID returns a new ULID string suitable for Event.ID.
func NewEventID() string {
	return ulid.Make().String()
}

// StableEventID derives a deterministic event ID for retryable logical
// operations. Re-appending the same logical event is therefore idempotent,
// while ordinary events should continue to use NewEventID.
func StableEventID(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	var id ulid.ULID
	copy(id[:], digest[:len(id)])
	return id.String()
}

// Required event types (section 14.3). Grouped by subsystem for
// readability; the ledger itself treats Type as an opaque string and, per
// the replay guarantees in section 14.5, tolerates unknown future event
// types without error.
const (
	EventRunStarted   = "run.started"
	EventRunCompleted = "run.completed"
	EventRunFailed    = "run.failed"
	EventRunPaused    = "run.paused"
	EventRunResumed   = "run.resumed"

	EventTaskCreated   = "task.created"
	EventTaskUpdated   = "task.updated"
	EventTaskCompleted = "task.completed"
	EventTaskBlocked   = "task.blocked"

	EventUserMessage  = "user.message"
	EventUserSteering = "user.steering"

	EventControllerDecision        = "controller.decision"
	EventControllerCriticRequested = "controller.critic_requested"
	EventControllerCriticCompleted = "controller.critic_completed"

	EventContextCompileStarted      = "context.compile_started"
	EventContextCompiled            = "context.compiled"
	EventContextReceiptReused       = "context.receipt_reused"
	EventContextReceiptExpanded     = "context.receipt_expanded"
	EventContextProviderRejected    = "context.provider_rejected"
	EventContextEmergencyProjection = "context.emergency_projection"

	EventEvidenceCreated     = "evidence.created"
	EventEvidenceReferenced  = "evidence.referenced"
	EventEvidenceInvalidated = "evidence.invalidated"
	EventEvidencePinned      = "evidence.pinned"

	EventModelRequestPlanned   = "model.request_planned"
	EventModelRequestStarted   = "model.request_started"
	EventModelRequestCompleted = "model.request_completed"
	EventModelRequestFailed    = "model.request_failed"
	EventModelRequestReplayed  = "model.request_replayed"

	// EventDurableTurn correlates one durable-scheduler activity with the
	// turn it drove: workflow instance ID, generation, and turn index.
	EventDurableTurn = "durable.turn"
	// EventDurableGoalGeneration records the immutable terminal fact for one
	// top-level durable goal workflow generation. Incomplete generations leave
	// the canonical run open but remain visible to audit and replay.
	EventDurableGoalGeneration = "durable.goal_generation"
	// EventDurableApprovalWaiting records that a parked task's workflow
	// is holding a durable external-event wait; the payload carries the
	// child workflow instance ID an approval must target.
	EventDurableApprovalWaiting = "durable.approval_waiting"
	// EventDurableApprovalResolved records how the wait ended:
	// approved, denied, or timed_out.
	EventDurableApprovalResolved = "durable.approval_resolved"
	// EventDurableRetryWaiting records one stable retry timer identity. The
	// workflow history owns timer scheduling; the ledger owns this audit fact.
	EventDurableRetryWaiting = "durable.retry_waiting"
	// EventDurableRetryResolved records one stable retry wake/readmission.
	EventDurableRetryResolved = "durable.retry_resolved"

	EventToolRequested         = "tool.requested"
	EventToolStarted           = "tool.started"
	EventToolCompleted         = "tool.completed"
	EventToolFailed            = "tool.failed"
	EventToolReplayed          = "tool.replayed"
	EventToolSkipped           = "tool.skipped"
	EventToolApprovalRequested = "tool.approval_requested"
	EventToolApprovalResolved  = "tool.approval_resolved"

	EventFileRead      = "file.read"
	EventFileProjected = "file.projected"
	EventFileEdited    = "file.edited"

	EventTestStarted   = "test.started"
	EventTestCompleted = "test.completed"

	EventReviewStarted   = "review.started"
	EventReviewCompleted = "review.completed"
	EventReviewFailed    = "review.failed"
	EventReviewPosted    = "review.posted"

	EventCommitPrepared = "commit.prepared"
	EventCommitApproved = "commit.approved"
	EventCommitApplied  = "commit.applied"
	EventCommitFailed   = "commit.failed"
	EventPushApplied    = "push.applied"

	EventCheckpointCreated  = "checkpoint.created"
	EventCheckpointUpdated  = "checkpoint.updated"
	EventCheckpointPromoted = "checkpoint.promoted"

	EventMemoryRecalled = "memory.recalled"
	EventMemoryPromoted = "memory.promoted"
	EventMemoryRejected = "memory.rejected"

	EventSubagentSpawned          = "subagent.spawned"
	EventSubagentUpdated          = "subagent.updated"
	EventSubagentCompleted        = "subagent.completed"
	EventSubagentFailed           = "subagent.failed"
	EventSubagentCancelled        = "subagent.cancelled"
	EventSubagentHandoffCreated   = "subagent.handoff_created"
	EventSubagentMessageSent      = "subagent.message_sent"
	EventSubagentMessageDelivered = "subagent.message_delivered"
	EventSubagentSteered          = "subagent.steered"
	EventSubagentClaimed          = "subagent.claimed"
	EventSubagentReleased         = "subagent.released"

	EventBudgetUpdated   = "budget.updated"
	EventBudgetWarning   = "budget.warning"
	EventBudgetExhausted = "budget.exhausted"
)

// runStatusEvents maps a run-lifecycle event type to the AgentRun.Status it
// implies, in the order materialize.go uses to fold events (last one wins).
var runStatusEvents = map[string]string{
	EventRunStarted:   "running",
	EventRunPaused:    "paused",
	EventRunResumed:   "running",
	EventRunCompleted: "completed",
	EventRunFailed:    "failed",
}

// taskStatusEvents maps a task-lifecycle event type to the TaskState.Status
// it implies.
var taskStatusEvents = map[string]string{
	EventTaskCreated:   "created",
	EventTaskUpdated:   "updated",
	EventTaskCompleted: "completed",
	EventTaskBlocked:   "blocked",
}
