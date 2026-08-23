package sessionexec

import (
	"context"
	"errors"
	"time"
)

const (
	ForegroundTaskID = "foreground"

	MaxSessionIDBytes                    = 256
	MaxCommandIDBytes                    = 128
	MaxRunIDBytes                        = 128
	MaxTurnIDBytes                       = 192
	MaxCommandTypeBytes                  = 32
	MaxPrincipalBytes                    = 128
	MaxLeaseOwnerBytes                   = 128
	MaxContentBytes                      = 1 << 20
	MaxErrorCodeBytes                    = 64
	MaxErrorTextBytes                    = 512
	MaxOutcomeCodeBytes                  = 64
	MaxOutcomeReferences                 = 64
	MaxOutcomeJSONBytes                  = 32 << 10
	MaxReferenceIDBytes                  = 160
	MaxTranscriptEntries                 = 256
	MaxTranscriptEntryBytes              = 1 << 20
	MaxTranscriptEntryJSONBytes          = 8 << 20
	MaxTranscriptTotalBytes              = 4 << 20
	MaxTranscriptEntryTokens       int64 = 10_000_000
	MaxCompletionTokens            int64 = 100_000_000
	MaxSessionTotalTokens          int64 = 1_000_000_000_000
	MaxSessionMessageCount         int64 = 10_000_000
	MaxTranscriptOrdinal                 = 1_000_000
	MaxCommandSequence             int64 = 1_000_000_000_000
	MaxCommandAttempts                   = 1_000_000
	MaxListLimit                         = 256
	DefaultListLimit                     = 50
	MaxCancelBatch                       = 256
	MaxCancellationSignals               = 256
	MaxEffectIDBytes                     = 512
	MaxActiveEffectPermits               = 64
	MaxEffectPermitsPerSession           = 256
	MaxEffectResolutionReasonBytes       = 512
	MaxLeaseDuration                     = 24 * time.Hour
	DefaultQuiesceDrainTimeout           = 5 * time.Second
)

var (
	ErrValidation           = errors.New("sessionexec: validation failed")
	ErrNotFound             = errors.New("sessionexec: not found")
	ErrIdempotencyConflict  = errors.New("sessionexec: idempotency conflict")
	ErrLeaseStale           = errors.New("sessionexec: stale lease")
	ErrLeaseExpired         = errors.New("sessionexec: expired lease")
	ErrTranscriptConflict   = errors.New("sessionexec: transcript conflict")
	ErrTerminalConflict     = errors.New("sessionexec: terminal conflict")
	ErrSessionQuiesced      = errors.New("sessionexec: session execution quiesced")
	ErrCancellationLimit    = errors.New("sessionexec: cancellation signal limit reached")
	ErrEffectPermitConflict = errors.New("sessionexec: effect permit conflict")
	ErrEffectPermitLimit    = errors.New("sessionexec: effect permit limit reached")
	ErrEffectAmbiguous      = errors.New("sessionexec: effect outcome ambiguous")
	ErrQuiescenceIncomplete = errors.New("sessionexec: quiescence incomplete")
)

type ExecutionMode string

const (
	ExecutionModeHeadless ExecutionMode = "headless"
	ExecutionModeDetached ExecutionMode = "detached"
	ExecutionModeAdopted  ExecutionMode = "adopted"
)

type ExecutionState struct {
	SessionID  string        `json:"sessionId"`
	Mode       ExecutionMode `json:"mode"`
	Generation int64         `json:"generation"`
	ReasonCode string        `json:"reasonCode,omitempty"`
	UpdatedAt  time.Time     `json:"updatedAt"`
}

type QuiesceResult struct {
	State     ExecutionState `json:"state"`
	Cancelled int64          `json:"cancelled"`
	Duplicate bool           `json:"duplicate"`
}

type EffectKind string

const (
	EffectKindModel EffectKind = "model"
	EffectKindTool  EffectKind = "tool"
)

type EffectState string

const (
	EffectStateActive    EffectState = "active"
	EffectStateAmbiguous EffectState = "ambiguous"
	EffectStateEnded     EffectState = "ended"
	EffectStateResolved  EffectState = "resolved"
)

// EffectRequest binds one external invocation to the exact live command
// lease and stable durable step identity that authorized it.
type EffectRequest struct {
	Lease    LeaseRef   `json:"lease"`
	EffectID string     `json:"effectId"`
	Kind     EffectKind `json:"kind"`
}

// EffectPermit is the database-fenced authority to perform one external
// invocation. Callers must close it immediately after the invocation ends.
type EffectPermit struct {
	EffectRequest
	State            EffectState `json:"state"`
	ExpiresAt        time.Time   `json:"expiresAt"`
	CreatedAt        time.Time   `json:"createdAt"`
	AmbiguousAt      *time.Time  `json:"ambiguousAt,omitempty"`
	EndedAt          *time.Time  `json:"endedAt,omitempty"`
	ResolvedAt       *time.Time  `json:"resolvedAt,omitempty"`
	ResolvedBy       string      `json:"resolvedBy,omitempty"`
	ResolutionReason string      `json:"resolutionReason,omitempty"`
	Duplicate        bool        `json:"duplicate"`
}

// EffectResolutionRequest is the privileged identity and audit projection
// used to resolve an expired ambiguous effect after execution is quiesced.
type EffectResolutionRequest struct {
	SessionID  string `json:"sessionId"`
	CommandID  string `json:"commandId"`
	Generation int    `json:"generation"`
	EffectID   string `json:"effectId"`
	Actor      string `json:"actor"`
	Reason     string `json:"reason,omitempty"`
}

type Lane string

const (
	LaneWork    Lane = "work"
	LaneControl Lane = "control"
)

type State string

const (
	StateAccepted    State = "accepted"
	StateRunning     State = "running"
	StateSucceeded   State = "succeeded"
	StateFailed      State = "failed"
	StateBlocked     State = "blocked"
	StateInterrupted State = "interrupted"
	StateCancelled   State = "cancelled"
)

type Identity struct {
	SessionID  string `json:"sessionId"`
	RunID      string `json:"runId"`
	TaskID     string `json:"taskId"`
	CommandID  string `json:"commandId"`
	TurnID     string `json:"turnId"`
	Generation int    `json:"generation"`
	Sequence   int64  `json:"sequence"`
}

type AcceptRequest struct {
	SessionID  string `json:"sessionId"`
	CommandID  string `json:"commandId,omitempty"`
	Type       string `json:"type"`
	Content    string `json:"content,omitempty"`
	AcceptedBy string `json:"acceptedBy"`
}

type Receipt struct {
	Identity
	Lane            Lane       `json:"lane"`
	State           State      `json:"state"`
	Duplicate       bool       `json:"duplicate"`
	Attempt         int        `json:"attempt"`
	TargetCommandID string     `json:"targetCommandId,omitempty"`
	AcceptedAt      time.Time  `json:"acceptedAt"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	FinishedAt      *time.Time `json:"finishedAt,omitempty"`
	ErrorCode       string     `json:"errorCode,omitempty"`
	Error           string     `json:"error,omitempty"`
}

type Command struct {
	Identity
	Lane                  Lane       `json:"lane"`
	Type                  string     `json:"type"`
	Content               string     `json:"content"`
	InputDigest           string     `json:"inputDigest"`
	AcceptedBy            string     `json:"acceptedBy"`
	TargetCommandID       string     `json:"targetCommandId,omitempty"`
	State                 State      `json:"state"`
	Attempt               int        `json:"attempt"`
	Lease                 LeaseRef   `json:"lease"`
	AcceptedAt            time.Time  `json:"acceptedAt"`
	StartedAt             *time.Time `json:"startedAt,omitempty"`
	NextTranscriptOrdinal int        `json:"nextTranscriptOrdinal"`
}

type ClaimRequest struct {
	SessionID     string        `json:"sessionId"`
	Lane          Lane          `json:"lane"`
	Owner         string        `json:"owner"`
	LeaseDuration time.Duration `json:"leaseDuration"`
}

type LeaseRef struct {
	SessionID       string    `json:"sessionId"`
	CommandID       string    `json:"commandId"`
	Generation      int       `json:"generation"`
	Owner           string    `json:"owner"`
	LeaseGeneration int64     `json:"leaseGeneration"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

type TranscriptEntry struct {
	Ordinal          int    `json:"ordinal"`
	Role             string `json:"role"`
	Content          string `json:"content"`
	ContentJSON      string `json:"contentJson,omitempty"`
	ContentType      string `json:"contentType,omitempty"`
	ToolCalls        string `json:"toolCalls,omitempty"`
	ToolCallID       string `json:"toolCallId,omitempty"`
	Name             string `json:"name,omitempty"`
	Reasoning        string `json:"reasoning,omitempty"`
	ReasoningDetails string `json:"reasoningDetails,omitempty"`
	Tokens           int64  `json:"tokens"`
	IsSummary        bool   `json:"isSummary"`
	IsTruncated      bool   `json:"isTruncated"`
}

// Outcome is a bounded projection. It deliberately carries only stable codes
// and opaque references, never provider responses, tool bodies, or reasoning.
type Outcome struct {
	Code        string   `json:"code,omitempty"`
	EvidenceIDs []string `json:"evidenceIds,omitempty"`
	ArtifactIDs []string `json:"artifactIds,omitempty"`
	Retryable   bool     `json:"retryable,omitempty"`
}

type Completion struct {
	State     State   `json:"state"`
	ErrorCode string  `json:"errorCode,omitempty"`
	Error     string  `json:"error,omitempty"`
	Outcome   Outcome `json:"outcome,omitempty"`
}

type Query struct {
	SessionID     string  `json:"sessionId"`
	States        []State `json:"states,omitempty"`
	AfterSequence int64   `json:"afterSequence,omitempty"`
	Limit         int     `json:"limit,omitempty"`
}

type Summary struct {
	SessionID      string `json:"sessionId"`
	Total          int    `json:"total"`
	Accepted       int    `json:"accepted"`
	Running        int    `json:"running"`
	Succeeded      int    `json:"succeeded"`
	Failed         int    `json:"failed"`
	Blocked        int    `json:"blocked"`
	Interrupted    int    `json:"interrupted"`
	Cancelled      int    `json:"cancelled"`
	WorkPending    int    `json:"workPending"`
	ControlPending int    `json:"controlPending"`
	LastSequence   int64  `json:"lastSequence"`
}

type Acceptor interface {
	Accept(context.Context, AcceptRequest) (Receipt, error)
}

type LeaseJournal interface {
	ClaimNext(context.Context, ClaimRequest) (Command, error)
	Heartbeat(context.Context, LeaseRef, time.Duration) (LeaseRef, error)
	Release(context.Context, LeaseRef) (Receipt, error)
	RecoverExpired(context.Context, string) (int, error)
}

type CompletionJournal interface {
	Complete(context.Context, LeaseRef, Completion, []TranscriptEntry) (Receipt, error)
	CancelPending(context.Context, string, string) (int, error)
}

type Reader interface {
	Get(context.Context, string, string) (Receipt, error)
	List(context.Context, Query) ([]Receipt, error)
	Summary(context.Context, string) (Summary, error)
}

// CancellationReader reports committed steer/interrupt intent for one exact
// work command. Signal command state is intentionally irrelevant: acceptance
// is the durable cancellation boundary.
type CancellationReader interface {
	CancellationRequested(context.Context, string, string) (bool, error)
}

// ExecutionGate is the durable ownership boundary for foreground execution.
// QuiesceSession is monotonic: once a session leaves headless mode it cannot
// be re-enabled through this interface.
type ExecutionGate interface {
	GetExecutionState(context.Context, string) (ExecutionState, error)
	QuiesceSession(context.Context, string, ExecutionMode, string) (QuiesceResult, error)
}

type EffectJournal interface {
	BeginEffect(context.Context, EffectRequest) (EffectPermit, error)
	EndEffect(context.Context, EffectPermit) error
}

// EffectResolver is a privileged operator capability. It is intentionally
// separate from Journal so ordinary foreground workers cannot resolve their
// own ambiguous external effects.
type EffectResolver interface {
	ResolveAmbiguousEffect(context.Context, EffectResolutionRequest) (EffectPermit, error)
}

type Journal interface {
	Acceptor
	LeaseJournal
	CompletionJournal
	Reader
	CancellationReader
	ExecutionGate
	EffectJournal
}
