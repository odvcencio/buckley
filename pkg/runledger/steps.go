package runledger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Execution step statuses are deliberately small. A step is either being
// attempted, has a durable result that can be replayed, is terminally blocked
// after an ambiguous side effect, or failed and may be retried with a higher
// attempt number.
const (
	StepStarted   = "started"
	StepCompleted = "completed"
	StepBlocked   = "blocked"
	StepFailed    = "failed"
)

// Dispatch states distinguish a claim that has not crossed an external-effect
// boundary from one whose provider or tool outcome is ambiguous.
const (
	StepDispatchClaimed    = "claimed"
	StepDispatchDispatched = "dispatched"
)

const (
	StepRecoveryResume = "resume"
	StepRecoveryRerun  = "rerun"
)

// ErrStepNotFound is returned when a step journal lookup has no matching
// logical step.
var ErrStepNotFound = errors.New("runledger: execution step not found")

// ErrStepRecoveryRequired reports a durable started attempt that requires an
// explicit resume or rerun decision. It is never an automatic retry signal.
var ErrStepRecoveryRequired = errors.New("runledger: execution step requires explicit recovery")

// ErrStepInProgress is retained for errors.Is compatibility.
var ErrStepInProgress = ErrStepRecoveryRequired

// ErrStepAttemptRequired reports use of a legacy terminal mutator after the
// logical step advanced beyond attempt one.
var ErrStepAttemptRequired = errors.New("runledger: expected attempt is required for this execution step")

// ErrStepTransitionConflict reports a stale or contradictory terminal write.
// Completed and blocked steps are immutable, and only the active attempt may
// transition out of StepStarted.
var ErrStepTransitionConflict = errors.New("runledger: execution step transition conflicts with durable state")

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
	ClaimGeneration  int
	InputDigest      string
	OutputDigest     string
	OutputEvidenceID string
	Error            string
	DispatchState    string
	StartedAt        time.Time
	CompletedAt      *time.Time
}

// StepRecoveryError tells an operator whether the claim stopped before an
// effect boundary or must be reconciled and intentionally rerun in a new turn
// generation. Legacy rows have an unknown phase and are treated as ambiguous.
type StepRecoveryError struct {
	RunID           string
	TaskID          string
	StepID          string
	Attempt         int
	ClaimGeneration int
	DispatchState   string
	Action          string
	Cause           string
}

func (e *StepRecoveryError) Error() string {
	if e == nil {
		return ErrStepRecoveryRequired.Error()
	}
	if e.Action == StepRecoveryResume {
		return fmt.Sprintf("runledger: step %s attempt %d claim %d stopped before dispatch; resume by atomically transferring ownership", e.StepID, e.Attempt, e.ClaimGeneration)
	}
	message := fmt.Sprintf("runledger: step %s attempt %d may have dispatched an external effect; reconcile its outcome, then rerun with a new turn generation", e.StepID, e.Attempt)
	if strings.TrimSpace(e.Cause) != "" {
		message += ": " + e.Cause
	}
	return message
}

func (e *StepRecoveryError) Unwrap() error { return ErrStepRecoveryRequired }

// RecoveryErrorForStep maps durable phase to the only safe operator action.
// Legacy started rows have no dispatch phase and therefore require rerun.
func RecoveryErrorForStep(step ExecutionStep) *StepRecoveryError {
	action := StepRecoveryRerun
	if step.Status == StepStarted && step.DispatchState == StepDispatchClaimed {
		action = StepRecoveryResume
	}
	return &StepRecoveryError{
		RunID:           step.RunID,
		TaskID:          step.TaskID,
		StepID:          step.StepID,
		Attempt:         step.Attempt,
		ClaimGeneration: step.ClaimGeneration,
		DispatchState:   step.DispatchState,
		Action:          action,
		Cause:           step.Error,
	}
}

// StepJournal is intentionally separate from Store. Existing Store ports and
// test doubles do not need to implement it, while durable backends can opt in
// to logical-step claims and replay.
type StepJournal interface {
	// BeginStep creates a started step or advances a prior failed step to a new
	// attempt. An existing started step returns ErrStepInProgress. The bool is
	// true when an immutable completed or blocked step already exists; callers
	// must inspect its status before replaying.
	BeginStep(ctx context.Context, step ExecutionStep) (ExecutionStep, bool, error)
	// CompleteStep records the durable output before the caller acknowledges
	// successful work to an external scheduler. It is retained for adapter
	// compatibility; side-effecting callers use CompleteStepAttempt below.
	CompleteStep(ctx context.Context, runID, stepID, outputEvidenceID, outputDigest string, completedAt time.Time) error
	// FailStep records an attempt failure while retaining the logical step for
	// a later retry. It is retained for adapter compatibility; side-effecting
	// callers use FailStepAttempt below.
	FailStep(ctx context.Context, runID, stepID, failure string, completedAt time.Time) error
	// GetStep returns the current logical-step record.
	GetStep(ctx context.Context, runID, stepID string) (ExecutionStep, error)
}

// StepEnumerator is the optional read capability used by integrity tooling to
// find durable steps that have no corresponding ledger event. Keeping it
// separate leaves existing StepJournal adapters source-compatible while
// allowing verifiers to report when their view is necessarily partial.
type StepEnumerator interface {
	ListSteps(ctx context.Context, runID string) ([]ExecutionStep, error)
}

// BlockingStepJournal is the optional guarded-transition capability required
// by side-effecting controllers. Keeping it separate preserves source
// compatibility for existing StepJournal adapters while allowing callers to
// fail closed when attempt-aware terminal blocking is unavailable.
type BlockingStepJournal interface {
	StepJournal
	// CompleteStepAttempt completes only the supplied active attempt. Repeating
	// the identical completion is idempotent; stale or contradictory writes fail.
	CompleteStepAttempt(ctx context.Context, step ExecutionStep, outputEvidenceID, outputDigest string, completedAt time.Time) error
	// FailStepAttempt fails only the supplied active attempt. Repeating the
	// identical failure is idempotent; terminal or stale writes fail.
	FailStepAttempt(ctx context.Context, step ExecutionStep, failure string, completedAt time.Time) error
	// BlockStep records an immutable terminal failure for the supplied active
	// attempt, including any output evidence written before the outcome became
	// ambiguous. Repeating the identical block is idempotent.
	BlockStep(ctx context.Context, step ExecutionStep, failure, outputEvidenceID, outputDigest string, completedAt time.Time) error
}

// DispatchStepJournal is the optional phase-marking capability required by
// callers that may cross an external-effect boundary. It is separate from
// BlockingStepJournal so existing guarded-journal adapters remain compatible.
type DispatchStepJournal interface {
	BlockingStepJournal
	// MarkStepDispatched durably crosses the external-effect boundary for the
	// supplied active attempt. Repeating the mark is idempotent.
	MarkStepDispatched(ctx context.Context, step ExecutionStep, dispatchedAt time.Time) error
}

// FencedStepJournal is the optional ownership-transfer capability required by
// controllers that resume pre-dispatch claims. It leaves all lower-level
// journal adapter interfaces source-compatible.
type FencedStepJournal interface {
	DispatchStepJournal
	// ReclaimStep atomically replaces a still-claimed owner's generation.
	// The returned step is the only owner allowed to dispatch or terminate.
	ReclaimStep(ctx context.Context, step ExecutionStep, reclaimedAt time.Time) (ExecutionStep, error)
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
