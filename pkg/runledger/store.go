package runledger

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned when a requested run, event, receipt, or
// checkpoint does not exist.
var ErrNotFound = errors.New("runledger: not found")

// ErrEvidenceNotFound is returned when a receipt or checkpoint references an
// evidence ID that does not exist in the evidence_objects table. See the
// package doc on SQLiteStore for why this is an application-level check
// rather than a SQL foreign key.
var ErrEvidenceNotFound = errors.New("runledger: referenced evidence not found")

// AgentRun is one row of the agent_runs table (section 14.4): the durable
// record of a single agent run and its lifecycle.
type AgentRun struct {
	RunID       string
	SessionID   string
	ParentRunID string
	TaskID      string
	AgentID     string
	ModelID     string
	ProviderID  string
	Backend     string
	Status      string
	StartedAt   time.Time
	EndedAt     *time.Time
	Budget      map[string]any
	Outcome     map[string]any
}

// Reason is one scoring reason attached to a context receipt item (Appendix
// A: items[].reasons[]).
type Reason struct {
	Kind   string `json:"kind"`
	Weight int    `json:"weight"`
}

// ContextReceipt is one row of the context_receipts table (section 14.4):
// the durable manifest identifying a compiled context bundle.
type ContextReceipt struct {
	ReceiptID          string
	ParentReceiptID    string
	SessionID          string
	RunID              string
	TaskID             string
	SnapshotID         string
	RequestDigest      string
	PolicyVersion      string
	BudgetTokens       int
	EstimatedTokens    int
	CandidateTokens    int
	BundleEvidenceID   string
	ManifestEvidenceID string
	BundleSHA256       string
	CreatedAt          time.Time
}

// ContextReceiptItem is one row of the context_receipt_items table, ordered
// by Ordinal within a receipt. Field names and JSON tags mirror Appendix A's
// canonical receipt item shape; DB column names (in parentheses) differ
// slightly for two fields: Mode maps to the projection_mode column, and
// Reasons maps to the reasons_json column.
type ContextReceiptItem struct {
	Ordinal         int
	ItemID          string
	EntityID        string
	Path            string
	Section         string
	Mode            string // DB column: projection_mode
	StartLine       int
	EndLine         int
	ContentSHA256   string
	RawTokens       int
	ProjectedTokens int
	Score           int
	Reasons         []Reason // DB column: reasons_json
	Required        bool
}

// ContextUsage is one row of the context_usage table (section 14.4):
// observed token accounting for a model request made against a receipt.
type ContextUsage struct {
	ID                    int64
	ReceiptID             string
	ModelID               string
	ProviderID            string
	Backend               string
	EstimatedPromptTokens int
	ActualPromptTokens    int
	CachedPromptTokens    int
	CompletionTokens      int
	CreatedAt             time.Time
}

// TaskCheckpoint is one row of the task_checkpoints table (section 14.4): a
// versioned, durable snapshot of a task's state. StateJSON holds the
// serialized taskstate.CheckpointState (section 15.1); this package treats
// it as an opaque string, since pkg/taskstate owns that schema.
type TaskCheckpoint struct {
	CheckpointID       string
	ParentCheckpointID string
	TaskID             string
	SessionID          string
	RunID              string
	Version            int
	Status             string
	SnapshotID         string
	Reason             string
	StateJSON          string
	MarkdownEvidenceID string
	CreatedAt          time.Time
}

// AgentMetricSample is one row of the agent_metric_samples table (section
// 14.4): a single observed metric value for a run.
type AgentMetricSample struct {
	ID         int64
	RunID      string
	TaskID     string
	MetricName string
	Value      float64
	Unit       string
	Dimensions map[string]any
	Timestamp  time.Time
}

// EventQuery selects events for ListEvents.
type EventQuery struct {
	RunID  string
	TaskID string
	Types  []string
	Since  time.Time
	Limit  int
}

// RunQuery selects runs for ListRuns.
type RunQuery struct {
	SessionID   string
	TaskID      string
	ParentRunID string
	Limit       int
}

// LiveSink receives a best-effort, fire-and-forget copy of every
// successfully appended event, for live fan-out (for example, the
// telemetry hub, ADR 0008). Delivery is not guaranteed: a Store MUST NOT
// let a sink error or panic affect the durable write (section 14.1,
// acceptance: "Dropped live events do not affect durable ledger").
type LiveSink interface {
	Notify(event Event)
}

// RalphSink receives a copy of every successfully appended event for the
// legacy Ralph memory store during migration (section 14.1: "If Ralph
// compatibility is enabled, events are dual-written"). Unlike LiveSink, a
// RalphSink failure is reported back to the caller (see Store.Append), but
// it never rolls back or blocks the canonical write, since Buckley's own
// database is now the canonical run ledger.
type RalphSink interface {
	WriteEvent(ctx context.Context, event Event) error
}

// Store is the run ledger's public port.
type Store interface {
	// StartRun durably creates a new run row and returns it with defaults
	// applied (Status, StartedAt).
	StartRun(ctx context.Context, run AgentRun) (AgentRun, error)
	// EndRun transitions a run to a terminal status and records its
	// outcome.
	EndRun(ctx context.Context, runID, status string, endedAt time.Time, outcome map[string]any) error
	// GetRun returns a single run by ID.
	GetRun(ctx context.Context, runID string) (AgentRun, error)
	// ListRuns lists runs matching q.
	ListRuns(ctx context.Context, q RunQuery) ([]AgentRun, error)

	// Append durably records event, assigning it the next sequence number
	// for its run inside one transaction (append is transactional; section
	// 14.2). Existing events are immutable. If event.ID or
	// event.SchemaVersion are empty, defaults are applied.
	Append(ctx context.Context, event Event) (Event, error)
	// ListEvents lists events matching q, ordered by (run_id, sequence).
	ListEvents(ctx context.Context, q EventQuery) ([]Event, error)

	// CreateContextReceipt durably records receipt and its items in one
	// transaction, validating that BundleEvidenceID and ManifestEvidenceID
	// reference existing evidence objects (see ErrEvidenceNotFound).
	CreateContextReceipt(ctx context.Context, receipt ContextReceipt, items []ContextReceiptItem) (ContextReceipt, error)
	// GetContextReceipt returns a receipt and its items, ordered by
	// ordinal.
	GetContextReceipt(ctx context.Context, receiptID string) (ContextReceipt, []ContextReceiptItem, error)
	// RecordContextUsage appends a usage observation for a receipt.
	RecordContextUsage(ctx context.Context, usage ContextUsage) (ContextUsage, error)

	// CreateTaskCheckpoint durably records a new checkpoint version,
	// validating that MarkdownEvidenceID references an existing evidence
	// object.
	CreateTaskCheckpoint(ctx context.Context, checkpoint TaskCheckpoint) (TaskCheckpoint, error)
	// LatestTaskCheckpoint returns the highest-version checkpoint for
	// taskID.
	LatestTaskCheckpoint(ctx context.Context, taskID string) (TaskCheckpoint, error)

	// RecordMetricSample appends one metric observation for a run.
	RecordMetricSample(ctx context.Context, sample AgentMetricSample) (AgentMetricSample, error)

	// SetLiveSink attaches a best-effort live event sink (see LiveSink).
	SetLiveSink(sink LiveSink)
	// SetRalphSink attaches a Ralph compatibility dual-write sink (see
	// RalphSink).
	SetRalphSink(sink RalphSink)
}
