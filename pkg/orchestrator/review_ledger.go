package orchestrator

import (
	"context"
	"encoding/json"
	"time"
)

// ReviewRecord is the permanent record of one completed or failed review.
// Empty revisions identify a failure before repository metadata was available.
type ReviewRecord struct {
	SchemaVersion int                  `json:"schema_version"`
	ReviewID      string               `json:"review_id"`
	Repository    string               `json:"repository"`
	PRNumber      int                  `json:"pr_number,omitempty"`
	Ref           string               `json:"ref"`
	BaseSHA       string               `json:"base_sha"`
	HeadSHA       string               `json:"head_sha"`
	Model         string               `json:"model"`
	StartedAt     time.Time            `json:"started_at"`
	EndedAt       time.Time            `json:"ended_at"`
	Verdict       string               `json:"verdict"`
	Findings      json.RawMessage      `json:"findings"`
	Verification  []ReviewVerification `json:"verification"`
	Evidence      []string             `json:"evidence_blob_hashes"`
	Error         string               `json:"error,omitempty"`
}

type ReviewVerification struct {
	Command  string `json:"command"`
	ExitCode *int   `json:"exit_code"`
	Status   string `json:"status"`
	LogHash  string `json:"log_blob_hash"`
}

// ReviewLedger stores records and content addressed evidence. Upload errors
// must not change a review verdict.
type ReviewLedger interface {
	Record(context.Context, ReviewRecord, map[string][]byte) error
	Retry(context.Context) error
	List(context.Context, string, int) ([]ReviewRecord, error)
	Show(context.Context, string) (ReviewRecord, error)
}

// ReviewRecorder starts an archive record and returns its completion callback.
type ReviewRecorder func(modelID string) func(*ReviewResult, string, error)
