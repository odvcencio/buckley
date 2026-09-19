package orchestrator

import (
	"encoding/json"
	"time"
)

const TaskExecutionRecordSchemaV1 = "buckley.task_execution.v1"

// TaskVerificationCheck describes a bounded, host-executed check. It is
// intentionally not a shell command or argv escape hatch.
type TaskVerificationCheck struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Language       string `json:"language,omitempty"`
	Path           string `json:"path,omitempty"`
	Pattern        string `json:"pattern,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// TaskVerificationResult records host-produced verification evidence for one
// structured check. Model-authored pass/fail claims never populate this type.
type TaskVerificationResult struct {
	CheckID    string          `json:"check_id"`
	Status     string          `json:"status"`
	EvidenceID string          `json:"evidence_id,omitempty"`
	SnapshotID string          `json:"snapshot_id,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
}

// TaskExecutionRecord is a neutral durable receipt for one task execution
// attempt. RuntimeResult is versioned public JSON produced at the runner
// boundary so orchestrator does not import runtime-specific packages.
type TaskExecutionRecord struct {
	Schema              string                   `json:"schema"`
	TaskID              string                   `json:"task_id"`
	ExecutionStatus     string                   `json:"execution_status"`
	VerificationStatus  string                   `json:"verification_status"`
	Summary             string                   `json:"summary,omitempty"`
	Error               string                   `json:"error,omitempty"`
	ScratchpadKey       string                   `json:"scratchpad_key,omitempty"`
	Model               string                   `json:"model,omitempty"`
	TokensUsed          int                      `json:"tokens_used,omitempty"`
	RuntimeResult       json.RawMessage          `json:"runtime_result,omitempty"`
	VerificationResults []TaskVerificationResult `json:"verification_results,omitempty"`
	CreatedAt           time.Time                `json:"created_at"`
}
