package runledger

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Execution step statuses are deliberately small. A step is either being
// attempted, has a durable result that can be replayed, or failed and may be
// retried with a higher attempt number.
const (
	StepStarted   = "started"
	StepCompleted = "completed"
	StepFailed    = "failed"
)

// ErrStepNotFound is returned when a step journal lookup has no matching
// logical step.
var ErrStepNotFound = errors.New("runledger: execution step not found")

// ExecutionStep is the durable idempotency record for one logical piece of
// work. StepID and IdempotencyKey identify the operation; Attempt identifies
// an execution attempt of that operation. A completed step's output evidence
// is the replay source and must not be replaced by a later attempt.
type ExecutionStep struct {
	RunID            string
	TaskID           string
	StepID           string
	Kind             string
	IdempotencyKey   string
	Status           string
	Attempt          int
	InputDigest      string
	OutputDigest     string
	OutputEvidenceID string
	Error            string
	StartedAt        time.Time
	CompletedAt      *time.Time
}

// StepJournal is intentionally separate from Store. Existing Store ports and
// test doubles do not need to implement it, while durable backends can opt in
// to logical-step claims and replay.
type StepJournal interface {
	// BeginStep creates a started step or advances a prior failed/started step
	// to a new attempt. The bool is true only when a completed step already
	// exists and its recorded output should be replayed.
	BeginStep(ctx context.Context, step ExecutionStep) (ExecutionStep, bool, error)
	// CompleteStep records the durable output before the caller acknowledges
	// successful work to an external scheduler.
	CompleteStep(ctx context.Context, runID, stepID, outputEvidenceID, outputDigest string, completedAt time.Time) error
	// FailStep records an attempt failure while retaining the logical step for
	// a later retry.
	FailStep(ctx context.Context, runID, stepID, failure string, completedAt time.Time) error
	// GetStep returns the current logical-step record.
	GetStep(ctx context.Context, runID, stepID string) (ExecutionStep, error)
}

func validateExecutionStep(step ExecutionStep) error {
	if step.RunID == "" {
		return fmt.Errorf("runledger: execution step run_id is required")
	}
	if step.StepID == "" {
		return fmt.Errorf("runledger: execution step step_id is required")
	}
	if step.Kind == "" {
		return fmt.Errorf("runledger: execution step kind is required")
	}
	if step.IdempotencyKey == "" {
		step.IdempotencyKey = step.StepID
	}
	return nil
}
