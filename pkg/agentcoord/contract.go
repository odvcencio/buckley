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
	SessionID       string            `json:"session_id,omitempty"`
	RunID           string            `json:"run_id,omitempty"`
	ParentRunID     string            `json:"parent_run_id,omitempty"`
	ParentSessionID string            `json:"parent_session_id,omitempty"`
	TurnID          string            `json:"turn_id,omitempty"`
	AttemptID       string            `json:"attempt_id,omitempty"`
	LeaseGeneration int64             `json:"lease_generation,omitempty"`
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
	SessionID       string    `json:"session_id,omitempty"`
	ParentRunID     string    `json:"parent_run_id,omitempty"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	Task            TaskSpec  `json:"task"`
	State           RunState  `json:"state"`
	Adapter         string    `json:"adapter,omitempty"`
	PID             int       `json:"pid,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at,omitempty"`
	HeartbeatAt     time.Time `json:"heartbeat_at,omitempty"`
	AttemptID       string    `json:"attempt_id,omitempty"`
	LeaseGeneration int64     `json:"lease_generation,omitempty"`
	Claims          []string  `json:"claims,omitempty"`
	MailboxCount    int       `json:"mailbox_count,omitempty"`
	Result          Result    `json:"result,omitempty"`
}

// RunFilter narrows a coordinator List request. Empty fields are not filters;
// callers may use it for a session, a parent, or a specific task.
type RunFilter struct {
	SessionID       string `json:"session_id,omitempty"`
	ParentRunID     string `json:"parent_run_id,omitempty"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
	Limit           int    `json:"limit,omitempty"`
}

// Message is a durable mailbox entry. Delivery is explicit: queued means it
// is safely persisted but the active adapter has not claimed live delivery.
// The additional identity and content-reference fields are additive so older
// Coordinator/Message callers can continue to use Content and ID while
// durable adapters carry a complete, bounded envelope.
type Message struct {
	Version         string `json:"version,omitempty"`
	ID              string `json:"id"`
	MessageID       string `json:"message_id,omitempty"`
	IdempotencyKey  string `json:"idempotency_key,omitempty"`
	SessionID       string `json:"session_id,omitempty"`
	RunID           string `json:"run_id"`
	ParentRunID     string `json:"parent_run_id,omitempty"`
	TaskID          string `json:"task_id,omitempty"`
	TurnID          string `json:"turn_id,omitempty"`
	CorrelationID   string `json:"correlation_id,omitempty"`
	CausationID     string `json:"causation_id,omitempty"`
	AttemptID       string `json:"attempt_id,omitempty"`
	LeaseGeneration int64  `json:"lease_generation,omitempty"`
	// SourceAttemptID and SourceLeaseGeneration fence child-to-parent
	// publication independently from the target mailbox's delivery lease.
	// AttemptID/LeaseGeneration remain the target delivery claim identity.
	SourceAttemptID       string    `json:"source_attempt_id,omitempty"`
	SourceLeaseGeneration int64     `json:"source_lease_generation,omitempty"`
	From                  string    `json:"from,omitempty"`
	To                    string    `json:"to"`
	Kind                  string    `json:"kind"`
	Content               string    `json:"content,omitempty"`
	ContentRef            string    `json:"content_ref,omitempty"`
	ContentDigest         string    `json:"content_digest,omitempty"`
	EnvelopeDigest        string    `json:"envelope_digest,omitempty"`
	MediaType             string    `json:"media_type,omitempty"`
	ByteCount             int64     `json:"byte_count,omitempty"`
	Preview               string    `json:"preview,omitempty"`
	Delivery              string    `json:"delivery"`
	State                 string    `json:"state,omitempty"`
	Sequence              int64     `json:"sequence,omitempty"`
	LeaseOwner            string    `json:"lease_owner,omitempty"`
	LeasedUntil           time.Time `json:"leased_until,omitempty"`
	AttemptCount          int       `json:"attempt_count,omitempty"`
	LastError             string    `json:"last_error,omitempty"`
	ProcessedAt           time.Time `json:"processed_at,omitempty"`
	DeadLetteredAt        time.Time `json:"dead_lettered_at,omitempty"`
	EvidenceRefs          []string  `json:"evidence_refs,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

// MessageEnvelope is the versioned wire/storage shape of Message. It is an
// alias rather than a second representation so legacy callers and durable
// callers cannot silently diverge on identity fields.
type MessageEnvelope = Message

const (
	MessageSchemaVersion = "m31.agent.message.v1"
	// OperatorIdentity and OperatorSteerKind are reserved durable provenance.
	// Generic mailbox enqueue paths must never infer authority from these
	// caller-controlled strings; only OperatorMailboxStore may create them.
	OperatorIdentity  = "operator"
	OperatorSteerKind = "steer"

	MessageQueued     = "queued"
	MessageClaimed    = "claimed"
	MessageProcessed  = "processed"
	MessageDeadLetter = "dead_letter"

	DeliveryQueued    = MessageQueued
	DeliveryClaimed   = MessageClaimed
	DeliveryProcessed = MessageProcessed
	DeliveryDead      = MessageDeadLetter
)

// MessageDelivery is the bounded operational state associated with an
// immutable Message envelope. Keeping it separate makes delivery mutation
// explicit while allowing Message to remain a convenient compatibility type.
type MessageDelivery struct {
	State           string    `json:"state"`
	Sequence        int64     `json:"sequence"`
	LeaseOwner      string    `json:"lease_owner,omitempty"`
	LeaseGeneration int64     `json:"lease_generation,omitempty"`
	LeasedUntil     time.Time `json:"leased_until,omitempty"`
	AttemptCount    int       `json:"attempt_count,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	ProcessedAt     time.Time `json:"processed_at,omitempty"`
	DeadLetteredAt  time.Time `json:"dead_lettered_at,omitempty"`
}

// MailboxQuery selects one durable run mailbox. Empty identity fields are not
// wildcards for durable callers: at least SessionID and RunID are required by
// operational stores to prevent cross-session reads.
type MailboxQuery struct {
	SessionID string   `json:"session_id"`
	RunID     string   `json:"run_id"`
	MessageID string   `json:"message_id,omitempty"`
	States    []string `json:"states,omitempty"`
	Limit     int      `json:"limit,omitempty"`
}

// MailboxClaimRequest reserves queued messages for one process attachment.
// Durable stores require Owner plus the exact AttemptID/LeaseGeneration pair;
// the fence prevents an expired process from acknowledging a later attempt.
type MailboxClaimRequest struct {
	SessionID       string        `json:"session_id"`
	RunID           string        `json:"run_id"`
	MessageID       string        `json:"message_id,omitempty"`
	Owner           string        `json:"owner"`
	ConsumerID      string        `json:"consumer_id,omitempty"`
	LeaseOwner      string        `json:"lease_owner,omitempty"`
	AttemptID       string        `json:"attempt_id,omitempty"`
	LeaseGeneration int64         `json:"lease_generation,omitempty"`
	LeaseDuration   time.Duration `json:"lease_duration,omitempty"`
	Limit           int           `json:"limit,omitempty"`
	Now             time.Time     `json:"now,omitempty"`
}

// MailboxAckRequest acknowledges one claimed message. The owner, attempt,
// and generation are all part of the compare-and-swap boundary.
type MailboxAckRequest struct {
	SessionID       string `json:"session_id"`
	RunID           string `json:"run_id"`
	MessageID       string `json:"message_id"`
	Owner           string `json:"owner"`
	LeaseOwner      string `json:"lease_owner,omitempty"`
	AttemptID       string `json:"attempt_id,omitempty"`
	LeaseGeneration int64  `json:"lease_generation"`
}

// MailboxNackRequest releases a claim for redelivery, or moves it to the
// dead-letter state when DeadLetter is true.
type MailboxNackRequest struct {
	MailboxAckRequest
	Reason     string `json:"reason,omitempty"`
	DeadLetter bool   `json:"dead_letter,omitempty"`
}

// AttachmentLease is the process attachment fence for a logical run. PID is
// diagnostic only; identity is the attempt plus monotonically increasing
// LeaseGeneration pair.
type AttachmentLease struct {
	SessionID       string    `json:"session_id"`
	RunID           string    `json:"run_id"`
	ParentRunID     string    `json:"parent_run_id,omitempty"`
	TaskID          string    `json:"task_id,omitempty"`
	TurnID          string    `json:"turn_id,omitempty"`
	AttemptID       string    `json:"attempt_id"`
	LeaseGeneration int64     `json:"lease_generation"`
	PID             int       `json:"pid,omitempty"`
	State           string    `json:"state"`
	AttachedAt      time.Time `json:"attached_at"`
	HeartbeatAt     time.Time `json:"heartbeat_at"`
	LeaseExpiresAt  time.Time `json:"lease_expires_at"`
	DetachedAt      time.Time `json:"detached_at,omitempty"`
}

const (
	AttachmentAttached = "attached"
	AttachmentDetached = "detached"
	AttachmentExpired  = "expired"
)

// AttachmentRequest creates (or idempotently resumes) one process lease.
type AttachmentRequest struct {
	SessionID     string        `json:"session_id"`
	RunID         string        `json:"run_id"`
	ParentRunID   string        `json:"parent_run_id,omitempty"`
	TaskID        string        `json:"task_id,omitempty"`
	TurnID        string        `json:"turn_id,omitempty"`
	AttemptID     string        `json:"attempt_id,omitempty"`
	LeaseDuration time.Duration `json:"lease_duration,omitempty"`
	PID           int           `json:"pid,omitempty"`
}

// AttachmentHeartbeatRequest renews one exact current lease.
type AttachmentHeartbeatRequest struct {
	SessionID       string        `json:"session_id"`
	RunID           string        `json:"run_id"`
	AttemptID       string        `json:"attempt_id"`
	LeaseGeneration int64         `json:"lease_generation"`
	LeaseDuration   time.Duration `json:"lease_duration,omitempty"`
}

// AttachmentDetachRequest detaches one exact current lease.
type AttachmentDetachRequest struct {
	SessionID       string `json:"session_id"`
	RunID           string `json:"run_id"`
	AttemptID       string `json:"attempt_id"`
	LeaseGeneration int64  `json:"lease_generation"`
	Reason          string `json:"reason,omitempty"`
}

// AttachmentStore is the durable process-ownership port. Implementations
// must fence every mutating operation by the exact attempt and generation.
type AttachmentStore interface {
	Attach(ctx context.Context, request AttachmentRequest) (AttachmentLease, error)
	Current(ctx context.Context, sessionID, runID string) (AttachmentLease, error)
	Heartbeat(ctx context.Context, request AttachmentHeartbeatRequest) (AttachmentLease, error)
	Detach(ctx context.Context, request AttachmentDetachRequest) error
}

// MailboxStore is the durable at-least-once operational delivery port. It is
// intentionally separate from Coordinator and the immutable run event log.
type MailboxStore interface {
	Enqueue(ctx context.Context, message Message) (Message, bool, error)
	Claim(ctx context.Context, request MailboxClaimRequest) ([]Message, error)
	Ack(ctx context.Context, request MailboxAckRequest) error
	Nack(ctx context.Context, request MailboxNackRequest) error
	List(ctx context.Context, query MailboxQuery) ([]Message, error)
	Expire(ctx context.Context, sessionID, runID string, now time.Time) (int, error)
}

// OperatorMailboxStore is the explicit trusted injection path for human
// steering. Implementations set reserved operator provenance themselves and
// must not treat Message.From or Message.Kind as proof of authority.
type OperatorMailboxStore interface {
	EnqueueOperatorSteer(ctx context.Context, message Message) (Message, bool, error)
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

// Publisher is the additive child-to-parent publication surface. A durable
// implementation authorizes the source's exact current attachment and its
// recorded parent before enqueueing into the parent's mailbox.
type Publisher interface {
	Publish(ctx context.Context, message Message) (Message, error)
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
