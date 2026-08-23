package goalloop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

// StepKind classifies what one TurnStep did, in terms a deterministic
// scheduler (the local RunTask loop or a durable workflow) can act on
// without reading Buckley state.
type StepKind string

const (
	// StepContinue: run the next turn with the same generation.
	StepContinue StepKind = "continue"
	// StepVerify: the next turn runs in the verify phase.
	StepVerify StepKind = "verify"
	// StepCheckpoint: a checkpoint was written; advance the generation and
	// reset the turn index.
	StepCheckpoint StepKind = "checkpoint"
	// StepYield: the drive checkpointed and yields to the scheduler
	// (replan, synthesize, stop_safety).
	StepYield StepKind = "yield"
	// StepPark, StepCompleted, StepBlocked end the task.
	StepPark      StepKind = "park"
	StepCompleted StepKind = "completed"
	StepBlocked   StepKind = "blocked"
)

type RetryWakeResult struct {
	Disposition RetryWakeDisposition
	TaskStatus  string
}

// DriveSnapshot is the serializable drive state one turn hands to the
// next. It is deliberately compact — a summary, actions, checks, and
// questions — so a durable workflow can carry it as explicit state
// without violating the payload rules in spec.durable-execution-dapr.
type DriveSnapshot struct {
	Summary              string                        `json:"summary,omitempty"`
	NextActions          []taskstate.NextAction        `json:"next_actions,omitempty"`
	Checks               []taskstate.VerificationEntry `json:"checks,omitempty"`
	Questions            []taskstate.Question          `json:"questions,omitempty"`
	Phase                string                        `json:"phase,omitempty"`
	PrematureCompletions int                           `json:"premature_completions,omitempty"`
}

func snapshotOf(d *driveState) DriveSnapshot {
	return DriveSnapshot{
		Summary:              d.summary,
		NextActions:          d.nextActions,
		Checks:               d.checks,
		Questions:            d.questions,
		Phase:                d.phase,
		PrematureCompletions: d.prematureCompletions,
	}
}

func (s DriveSnapshot) driveState() *driveState {
	phase := s.Phase
	if phase == "" {
		phase = PhaseExecute
	}
	return &driveState{
		summary:              s.Summary,
		nextActions:          s.NextActions,
		checks:               s.Checks,
		questions:            s.Questions,
		phase:                phase,
		prematureCompletions: s.PrematureCompletions,
	}
}

// TaskSeed is the durable starting state for driving a task: the
// checkpoint generation and the initial drive snapshot.
type TaskSeed struct {
	Generation int           `json:"generation"`
	Drive      DriveSnapshot `json:"drive"`
}

// SeedTask reads the task's latest checkpoint and builds the seed a
// drive starts from. A fresh task seeds generation zero and a snapshot
// carrying only the spec title.
func (l *Loop) SeedTask(ctx context.Context, taskID string, spec TaskSpec) (TaskSeed, error) {
	var resume *taskstate.ResumeContext
	if resumed, err := l.checkpoints.Resume(ctx, taskID); err == nil {
		resume = &resumed
	} else if !errors.Is(err, taskstate.ErrNoCheckpoint) {
		return TaskSeed{}, fmt.Errorf("goalloop: resume %s: %w", taskID, err)
	}
	seed := TaskSeed{Drive: snapshotOf(newDriveState(spec, resume))}
	if resume != nil {
		seed.Generation = resume.Checkpoint.Version
	}
	return seed, nil
}

// TurnStepRequest asks for exactly one turn of one task. The caller owns
// generation, turn index, drive snapshot, and fuse counters; TurnStep
// owns every Buckley side effect the turn produces.
type TurnStepRequest struct {
	RunID  string
	TaskID string
	Goal   Goal
	Spec   TaskSpec
	// Generation and TurnIndex form the turn identity
	// <task>/cp-<generation>/turn-<index>; a retry of an interrupted turn
	// must reuse both so completed steps replay (Phase 0 contract).
	Generation int
	TurnIndex  int
	Drive      DriveSnapshot
	Counters   agentloop.FuseCounters
	// DriveStarted, when set, recomputes Counters.Elapsed locally. A
	// durable caller leaves it zero and supplies Elapsed itself.
	DriveStarted time.Time
	// WorkflowInstanceID and ActivityName, when set, are projected into a
	// durable.turn ledger event so the Dapr history and the run ledger
	// reconcile.
	WorkflowInstanceID string
	ActivityName       string
}

// TurnStepResponse reports one turn in scheduler terms.
type TurnStepResponse struct {
	Kind StepKind `json:"kind"`
	// Decision is the progress-controller decision that produced Kind,
	// empty when the engine itself completed, blocked, or verified.
	Decision agentloop.ProgressDecision `json:"decision,omitempty"`
	// Status is the task's status after this turn.
	Status       string                 `json:"status"`
	Drive        DriveSnapshot          `json:"drive,omitempty"`
	Counters     agentloop.FuseCounters `json:"counters,omitempty"`
	TurnSpentUSD float64                `json:"turn_spent_usd,omitempty"`
	Rounds       int                    `json:"rounds,omitempty"`
	ToolCalls    int                    `json:"tool_calls,omitempty"`
	// These are compact retry metadata projected from a taskstate.Blocker;
	// the raw blocker text remains in the checkpoint, never in a durable
	// workflow payload.
	BlockerCategory           string `json:"blocker_category,omitempty"`
	BlockerReasonCode         string `json:"blocker_reason_code,omitempty"`
	RetryAfterUnixMS          int64  `json:"retry_after_unix_ms,omitempty"`
	RetryOrdinal              int    `json:"retry_ordinal,omitempty"`
	ExpectedCheckpointID      string `json:"expected_checkpoint_id,omitempty"`
	ExpectedCheckpointVersion int    `json:"expected_checkpoint_version,omitempty"`
	BlockerDigest             string `json:"blocker_digest,omitempty"`
}

// TurnStep drives exactly one turn: engine call, spend recording, drive
// absorption, completion gating, and the progress decision, with the
// same checkpoint boundaries as RunTask. It exists so a durable workflow
// can schedule turns as retry-safe activities while the local loop keeps
// identical semantics.
func (l *Loop) TurnStep(ctx context.Context, req TurnStepRequest) (TurnStepResponse, error) {
	return l.turnStep(ctx, req, true)
}

func (l *Loop) turnStep(ctx context.Context, req TurnStepRequest, legacyAudit bool) (TurnStepResponse, error) {
	if l.engine == nil {
		return TurnStepResponse{}, errNoEngine
	}

	task := TaskContext{RunID: req.RunID, TaskID: req.TaskID, Goal: req.Goal, Spec: req.Spec}
	if resumed, err := l.checkpoints.Resume(ctx, req.TaskID); err == nil {
		task.Resume = &resumed
	} else if !errors.Is(err, taskstate.ErrNoCheckpoint) {
		return TurnStepResponse{}, fmt.Errorf("goalloop: resume %s: %w", req.TaskID, err)
	}

	drive := req.Drive.driveState()
	task.Phase = drive.phase
	task.TurnID = fmt.Sprintf("%s/cp-%03d/turn-%03d", req.TaskID, req.Generation, req.TurnIndex)

	outcome, err := l.engine.RunTurn(ctx, task)
	if err != nil {
		return TurnStepResponse{}, fmt.Errorf("goalloop: turn for %s: %w", req.TaskID, err)
	}

	resp := TurnStepResponse{
		Status:       taskstate.StatusInProgress,
		TurnSpentUSD: outcome.SpentUSD,
		Rounds:       outcome.Rounds,
		ToolCalls:    outcome.ToolCalls,
	}
	metricKey := fmt.Sprintf("turn:%s:%s:%d:%d", req.RunID, req.TaskID, req.Generation, req.TurnIndex)
	goalSpent, err := l.recordTurnSpend(ctx, req.RunID, req.TaskID, req.Goal, outcome, metricKey)
	if err != nil {
		return TurnStepResponse{}, err
	}
	counters := req.Counters
	counters.ModelRequests += outcome.Rounds
	counters.ToolExecutions += outcome.ToolCalls
	if !req.DriveStarted.IsZero() {
		counters.Elapsed = time.Since(req.DriveStarted)
	}
	counters.SpentUSD = goalSpent
	resp.Counters = counters

	drive.absorb(outcome)
	// A verify phase lasts one turn; the outcome's checks decide what
	// happens next.
	drive.phase = PhaseExecute

	finish := func(kind StepKind, status string) (TurnStepResponse, error) {
		resp.Kind = kind
		resp.Status = status
		resp.Drive = snapshotOf(drive)
		if legacyAudit {
			// V1/V2 activities historically treated this projection as best
			// effort. Keep that behavior frozen; the V3 activity owns the strict
			// receipt boundary below.
			_ = l.recordDurableTurn(ctx, req, resp)
		}
		return resp, nil
	}

	if outcome.Completed {
		finished, err := l.tryFinishTask(ctx, req.RunID, req.TaskID, outcome, drive)
		if err != nil {
			return TurnStepResponse{}, err
		}
		if finished {
			return finish(StepCompleted, taskstate.StatusCompleted)
		}
		if drive.prematureCompletions >= 2 {
			blocker := &taskstate.Blocker{
				Reason: "completion blocked: required verification is not evidenced",
				Needs:  "evidence for required checks",
			}
			if err := l.parkTask(ctx, req.RunID, req.TaskID, blocker, drive); err != nil {
				return TurnStepResponse{}, err
			}
			return finish(StepPark, taskstate.StatusParked)
		}
		drive.phase = PhaseVerify
		return finish(StepVerify, taskstate.StatusInProgress)
	}
	if outcome.Blocker != nil {
		if err := l.blockTask(ctx, req.RunID, req.TaskID, outcome.Blocker, drive); err != nil {
			return TurnStepResponse{}, err
		}
		if legacyAudit {
			// V1/V2 historically returned immediately after the blocked
			// checkpoint. Retry metadata and its post-save read belong only to
			// the V3 receipt path; keeping them out also preserves the legacy
			// save-success/read-failure behavior.
			return finish(StepBlocked, taskstate.StatusBlocked)
		}
		resp.BlockerCategory, resp.BlockerReasonCode = governedBlockerCode(outcome.Blocker)
		if outcome.Blocker.RetryAfter != nil {
			resp.RetryAfterUnixMS = outcome.Blocker.RetryAfter.UTC().UnixMilli()
			resp.RetryOrdinal = req.TurnIndex + 1
		}
		blocked, err := l.checkpoints.Resume(ctx, req.TaskID)
		if err != nil {
			return TurnStepResponse{}, fmt.Errorf("goalloop: read blocked checkpoint %s: %w", req.TaskID, err)
		}
		resp.ExpectedCheckpointID = blocked.Checkpoint.CheckpointID
		resp.ExpectedCheckpointVersion = blocked.Checkpoint.Version
		resp.BlockerDigest = blockerIdentityDigest(blocked.State.Blocker)
		return finish(StepBlocked, taskstate.StatusBlocked)
	}

	progress := l.progressFor(req.Goal)
	decision := progress.Decide(progressState(outcome, req.Goal, goalSpent, drive), counters)
	l.recordDecision(ctx, req.RunID, req.TaskID, progress.Mode, decision)
	if decision.Decision == agentloop.DecideContinue || !decision.Apply {
		return finish(StepContinue, taskstate.StatusInProgress)
	}

	resp.Decision = decision.Decision
	switch decision.Decision {
	case agentloop.DecideVerify:
		drive.phase = PhaseVerify
		return finish(StepVerify, taskstate.StatusInProgress)
	case agentloop.DecideCheckpoint:
		if err := l.checkpointTask(ctx, req.RunID, req.TaskID, taskstate.StatusInProgress, drive, nil, taskstate.TriggerPressure); err != nil {
			return TurnStepResponse{}, err
		}
		return finish(StepCheckpoint, taskstate.StatusInProgress)
	case agentloop.DecidePark:
		blocker := &taskstate.Blocker{Reason: decision.Reason}
		if err := l.parkTask(ctx, req.RunID, req.TaskID, blocker, drive); err != nil {
			return TurnStepResponse{}, err
		}
		return finish(StepPark, taskstate.StatusParked)
	default:
		// replan, synthesize, and stop_safety end this drive: the loop
		// yields to the scheduler with a checkpoint.
		if err := l.checkpointTask(ctx, req.RunID, req.TaskID, taskstate.StatusInProgress, drive, nil, taskstate.TriggerDecisionRecorded); err != nil {
			return TurnStepResponse{}, err
		}
		return finish(StepYield, taskstate.StatusInProgress)
	}
}

// TurnStepV3 adds the whole-turn boundary used only by the V3 Dapr activity.
// The journal claim is durable before the engine runs, the dispatch mark makes
// an interrupted effect explicit, and a completed durable.turn receipt is the
// replay source after activity redelivery.
func (l *Loop) TurnStepV3(ctx context.Context, req TurnStepRequest) (TurnStepResponse, error) {
	if strings.TrimSpace(req.WorkflowInstanceID) == "" {
		return TurnStepResponse{}, fmt.Errorf("goalloop: V3 durable turn requires a workflow instance ID")
	}
	journal, ok := l.ledger.(runledger.FencedStepJournal)
	if !ok {
		return TurnStepResponse{}, fmt.Errorf("goalloop: V3 durable turn requires a fenced step journal")
	}
	inputDigest, err := turnStepInputDigest(req)
	if err != nil {
		return TurnStepResponse{}, err
	}
	stepID := durableTurnStepID(req)
	claimed, replay, err := journal.BeginStep(ctx, runledger.ExecutionStep{
		RunID:          req.RunID,
		TaskID:         req.TaskID,
		StepID:         stepID,
		Kind:           "durable_turn",
		IdempotencyKey: stepID,
		InputDigest:    inputDigest,
		StartedAt:      time.Now().UTC(),
	})
	if err != nil {
		var recovery *runledger.StepRecoveryError
		if !errors.As(err, &recovery) {
			return TurnStepResponse{}, fmt.Errorf("goalloop: begin V3 durable turn %s: %w", stepID, err)
		}
		if recovery.Action == runledger.StepRecoveryResume {
			claimed, err = journal.ReclaimStep(ctx, claimed, time.Now().UTC())
			if err != nil {
				return TurnStepResponse{}, fmt.Errorf("goalloop: reclaim V3 durable turn %s: %w", stepID, err)
			}
		} else {
			return l.recoverDurableTurn(ctx, journal, claimed, req, inputDigest)
		}
	} else if replay {
		return l.replayDurableTurn(ctx, claimed, req, inputDigest)
	}

	if err := journal.MarkStepDispatched(ctx, claimed, time.Now().UTC()); err != nil {
		return TurnStepResponse{}, fmt.Errorf("goalloop: dispatch V3 durable turn %s: %w", stepID, err)
	}
	resp, err := l.turnStep(ctx, req, false)
	if err != nil {
		return TurnStepResponse{}, err
	}
	eventID, outputDigest, err := l.recordDurableTurnReceipt(ctx, req, resp, inputDigest, claimed)
	if err != nil {
		return TurnStepResponse{}, err
	}
	if err := journal.CompleteStepAttempt(ctx, claimed, eventID, outputDigest, time.Now().UTC()); err != nil {
		return TurnStepResponse{}, fmt.Errorf("goalloop: complete V3 durable turn %s: %w", stepID, err)
	}
	return resp, nil
}

func (l *Loop) recoverDurableTurn(ctx context.Context, journal runledger.FencedStepJournal, step runledger.ExecutionStep, req TurnStepRequest, inputDigest string) (TurnStepResponse, error) {
	resp, found, err := l.loadDurableTurnReceipt(ctx, req, inputDigest, step)
	if err != nil {
		return TurnStepResponse{}, err
	}
	if !found {
		return TurnStepResponse{}, fmt.Errorf("goalloop: V3 durable turn %s was dispatched without a durable receipt; external outcome is ambiguous", step.StepID)
	}
	// Re-append even when the canonical receipt already exists. SQLite's
	// secondary-sink outbox uses that idempotent append to reconcile a prior
	// canonical-success/secondary-failure delivery before the activity is
	// acknowledged.
	if _, _, err := l.recordDurableTurnReceipt(ctx, req, resp, inputDigest, step); err != nil {
		return TurnStepResponse{}, err
	}
	eventID := durableTurnEventID(req)
	outputDigest, err := turnStepResponseDigest(resp)
	if err != nil {
		return TurnStepResponse{}, err
	}
	if err := journal.CompleteStepAttempt(ctx, step, eventID, outputDigest, time.Now().UTC()); err != nil {
		return TurnStepResponse{}, fmt.Errorf("goalloop: reconcile V3 durable turn %s: %w", step.StepID, err)
	}
	return resp, nil
}

func (l *Loop) replayDurableTurn(ctx context.Context, step runledger.ExecutionStep, req TurnStepRequest, inputDigest string) (TurnStepResponse, error) {
	resp, found, err := l.loadDurableTurnReceipt(ctx, req, inputDigest, step)
	if err != nil {
		return TurnStepResponse{}, err
	}
	if !found {
		return TurnStepResponse{}, fmt.Errorf("goalloop: completed V3 durable turn %s has no receipt", step.StepID)
	}
	return resp, nil
}

func turnStepInputDigest(req TurnStepRequest) (string, error) {
	encoded, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("goalloop: marshal V3 durable turn input: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func turnStepResponseDigest(resp TurnStepResponse) (string, error) {
	encoded, err := json.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("goalloop: marshal V3 durable turn response: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func durableTurnStepID(req TurnStepRequest) string {
	return "turn_" + runledger.StableEventID(
		"durable-turn-step-v3", req.RunID, req.TaskID, req.WorkflowInstanceID,
		req.ActivityName, fmt.Sprintf("%d", req.Generation), fmt.Sprintf("%d", req.TurnIndex),
	)
}

func durableTurnEventID(req TurnStepRequest) string {
	return runledger.StableEventID(
		runledger.EventDurableTurn, req.RunID, req.TaskID, req.WorkflowInstanceID,
		req.ActivityName, fmt.Sprintf("%d", req.Generation), fmt.Sprintf("%d", req.TurnIndex),
	)
}

// Unpark readmits a parked task after a durable approval: the latest
// checkpoint is rewritten in_progress with the blocker cleared, so the
// queue offers the task again and the next drive seeds a fresh
// generation from the new checkpoint version.
func (l *Loop) Unpark(ctx context.Context, runID, taskID, reason string) error {
	resumed, err := l.checkpoints.Resume(ctx, taskID)
	if err != nil {
		return fmt.Errorf("goalloop: unpark %s: %w", taskID, err)
	}
	state := resumed.State
	state.Status = taskstate.StatusInProgress
	state.Blocker = nil
	if _, err := l.checkpoints.Save(ctx, taskstate.SaveInput{
		State:     state,
		Reason:    taskstate.TriggerDecisionRecorded,
		SessionID: l.sessionID,
		RunID:     runID,
	}); err != nil {
		return fmt.Errorf("goalloop: unpark checkpoint %s: %w", taskID, err)
	}
	_, err = l.ledger.Append(ctx, runledger.Event{
		Type:      runledger.EventControllerDecision,
		Timestamp: time.Now().UTC(),
		SessionID: l.sessionID,
		RunID:     runID,
		TaskID:    taskID,
		Payload: map[string]any{
			"kind":     "goal_loop",
			"decision": "unparked_by_approval",
			"reason":   reason,
		},
	})
	if err != nil {
		return fmt.Errorf("goalloop: record unpark for %s: %w", taskID, err)
	}
	return nil
}

const retryWakeCheckpointReasonPrefix = "durable_retry_wake:"

// RetryWake binds a durable timer to the exact blocked checkpoint that
// created it. The raw blocker is represented only by its digest.
type RetryWake struct {
	WaitID                    string
	ExpectedCheckpointID      string
	ExpectedCheckpointVersion int
	BlockerDigest             string
	Category                  string
	ReasonCode                string
}

// RetryWakeDisposition tells a durable workflow whether the checkpoint CAS
// was applied, replayed after acknowledgement loss, or rejected as stale.
type RetryWakeDisposition string

const (
	RetryWakeApplied        RetryWakeDisposition = "applied"
	RetryWakeAlreadyApplied RetryWakeDisposition = "already_applied"
	RetryWakeStale          RetryWakeDisposition = "stale"
)

// UnparkRetry conditionally readmits only the checkpoint named by wake. A
// stale wake is a safe no-op. Redelivery after the successor checkpoint save
// replays the stable audit append instead of writing another checkpoint. The
// save is an adapter-backed expected-parent compare-and-save, so concurrent
// workers cannot both extend the same blocked checkpoint.
func (l *Loop) UnparkRetry(ctx context.Context, runID, taskID string, wake RetryWake) (RetryWakeResult, error) {
	if strings.TrimSpace(taskID) == "" {
		return RetryWakeResult{}, fmt.Errorf("goalloop: retry wake task ID is required")
	}
	if strings.TrimSpace(runID) == "" {
		return RetryWakeResult{}, fmt.Errorf("goalloop: retry wake run ID is required")
	}
	resumed, err := l.checkpoints.Resume(ctx, taskID)
	if err != nil {
		return RetryWakeResult{}, fmt.Errorf("goalloop: retry wake %s: %w", taskID, err)
	}
	if err := l.validateRetryWakeOwnership(resumed, runID, taskID); err != nil {
		return RetryWakeResult{}, err
	}
	if retryWakeSuccessor(resumed, wake) {
		if err := l.recordRetryWake(ctx, runID, taskID, wake); err != nil {
			return RetryWakeResult{}, err
		}
		return RetryWakeResult{Disposition: RetryWakeAlreadyApplied, TaskStatus: resumed.State.Status}, nil
	}
	if !retryWakeMatchesBlocked(resumed, wake) {
		return RetryWakeResult{Disposition: RetryWakeStale, TaskStatus: resumed.State.Status}, nil
	}
	state := resumed.State
	state.Status = taskstate.StatusInProgress
	state.Blocker = nil
	saved, err := l.checkpoints.SaveIfLatest(ctx, taskstate.SaveInput{
		State:     state,
		Reason:    retryWakeCheckpointReason(wake),
		SessionID: l.sessionID,
		RunID:     runID,
	}, taskstate.CheckpointExpectation{
		CheckpointID: wake.ExpectedCheckpointID,
		Version:      wake.ExpectedCheckpointVersion,
	})
	if errors.Is(err, taskstate.ErrCheckpointConflict) {
		latest, resumeErr := l.checkpoints.Resume(ctx, taskID)
		if resumeErr != nil {
			return RetryWakeResult{}, fmt.Errorf("goalloop: classify retry wake conflict %s: %w", taskID, resumeErr)
		}
		if ownershipErr := l.validateRetryWakeOwnership(latest, runID, taskID); ownershipErr != nil {
			return RetryWakeResult{}, ownershipErr
		}
		if retryWakeSuccessor(latest, wake) {
			if err := l.recordRetryWake(ctx, runID, taskID, wake); err != nil {
				return RetryWakeResult{}, err
			}
			return RetryWakeResult{Disposition: RetryWakeAlreadyApplied, TaskStatus: latest.State.Status}, nil
		}
		if retryWakeMatchesBlocked(latest, wake) {
			return RetryWakeResult{}, fmt.Errorf("goalloop: retry wake %s compare-and-save conflicted without a newer checkpoint", wake.WaitID)
		}
		return RetryWakeResult{Disposition: RetryWakeStale, TaskStatus: latest.State.Status}, nil
	}
	if err != nil {
		return RetryWakeResult{}, fmt.Errorf("goalloop: save retry wake checkpoint %s: %w", taskID, err)
	}
	if saved.ParentCheckpointID != wake.ExpectedCheckpointID || saved.Version != wake.ExpectedCheckpointVersion+1 {
		return RetryWakeResult{}, fmt.Errorf("goalloop: retry wake %s lost checkpoint compare-and-swap", wake.WaitID)
	}
	if err := l.recordRetryWake(ctx, runID, taskID, wake); err != nil {
		return RetryWakeResult{}, err
	}
	return RetryWakeResult{Disposition: RetryWakeApplied, TaskStatus: saved.Status}, nil
}

func (l *Loop) validateRetryWakeOwnership(resumed taskstate.ResumeContext, runID, taskID string) error {
	if resumed.Checkpoint.TaskID != taskID || resumed.State.TaskID != taskID {
		return fmt.Errorf("goalloop: retry wake checkpoint task ownership changed")
	}
	if resumed.Checkpoint.RunID != runID {
		return fmt.Errorf("goalloop: retry wake checkpoint run ownership changed")
	}
	if resumed.Checkpoint.SessionID != l.sessionID {
		return fmt.Errorf("goalloop: retry wake checkpoint session ownership changed")
	}
	return nil
}

func retryWakeMatchesBlocked(resumed taskstate.ResumeContext, wake RetryWake) bool {
	return resumed.Checkpoint.CheckpointID == wake.ExpectedCheckpointID &&
		resumed.Checkpoint.Version == wake.ExpectedCheckpointVersion &&
		resumed.State.Status == taskstate.StatusBlocked &&
		blockerIdentityDigest(resumed.State.Blocker) == wake.BlockerDigest
}

func retryWakeSuccessor(resumed taskstate.ResumeContext, wake RetryWake) bool {
	return resumed.Checkpoint.ParentCheckpointID == wake.ExpectedCheckpointID &&
		resumed.Checkpoint.Version == wake.ExpectedCheckpointVersion+1 &&
		resumed.Checkpoint.Reason == string(retryWakeCheckpointReason(wake)) &&
		resumed.State.Status == taskstate.StatusInProgress && resumed.State.Blocker == nil
}

func retryWakeCheckpointReason(wake RetryWake) taskstate.TriggerReason {
	return taskstate.TriggerReason(retryWakeCheckpointReasonPrefix + wake.WaitID)
}

func (l *Loop) recordRetryWake(ctx context.Context, runID, taskID string, wake RetryWake) error {
	_, err := l.ledger.Append(ctx, runledger.Event{
		ID: runledger.StableEventID(
			runledger.EventControllerDecision, runID, taskID, wake.WaitID,
			wake.ExpectedCheckpointID, fmt.Sprintf("%d", wake.ExpectedCheckpointVersion), wake.BlockerDigest,
		),
		Type:      runledger.EventControllerDecision,
		Timestamp: time.Now().UTC(),
		SessionID: l.sessionID,
		RunID:     runID,
		TaskID:    taskID,
		Payload: map[string]any{
			"kind":                        "goal_loop",
			"decision":                    "unparked_by_retry",
			"wait_id":                     wake.WaitID,
			"expected_checkpoint_id":      wake.ExpectedCheckpointID,
			"expected_checkpoint_version": wake.ExpectedCheckpointVersion,
			"blocker_digest":              wake.BlockerDigest,
			"category":                    wake.Category,
			"reason_code":                 wake.ReasonCode,
		},
	})
	if err != nil {
		return fmt.Errorf("goalloop: record retry wake %s: %w", wake.WaitID, err)
	}
	return nil
}

// governedBlockerCode maps untrusted blocker text to bounded policy labels.
// The labels are intentionally coarse: the checkpoint retains the human
// explanation, while durable workflow history carries only these codes.
func governedBlockerCode(blocker *taskstate.Blocker) (category, reasonCode string) {
	if blocker == nil {
		return "", ""
	}
	reason := strings.ToLower(strings.TrimSpace(blocker.Reason))
	switch {
	case strings.Contains(reason, "rate limit"), strings.Contains(reason, "rate-limit"), strings.Contains(reason, "quota"), strings.Contains(reason, "capacity"), strings.Contains(reason, "unavailable"):
		return "provider", "retryable_capacity"
	case strings.Contains(reason, "permission"), strings.Contains(reason, "approval"):
		return "governance", "authorization_required"
	case strings.Contains(reason, "dependency"), strings.Contains(reason, "database"), strings.Contains(reason, "credential"):
		return "dependency", "external_dependency"
	default:
		return "execution", "blocked"
	}
}

func blockerIdentityDigest(blocker *taskstate.Blocker) string {
	if blocker == nil {
		return ""
	}
	retryAfterUnixMS := int64(0)
	if blocker.RetryAfter != nil {
		retryAfterUnixMS = blocker.RetryAfter.UTC().UnixMilli()
	}
	encoded, _ := json.Marshal(struct {
		Reason           string `json:"reason"`
		Needs            string `json:"needs,omitempty"`
		RetryAfterUnixMS int64  `json:"retry_after_unix_ms,omitempty"`
	}{
		Reason:           blocker.Reason,
		Needs:            blocker.Needs,
		RetryAfterUnixMS: retryAfterUnixMS,
	})
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// recordDurableTurn projects a durable scheduler's identity onto the run
// ledger, so the Dapr workflow history and Buckley's audit reconcile per
// spec.durable-execution-dapr. Local drives skip it.
func (l *Loop) recordDurableTurn(ctx context.Context, req TurnStepRequest, resp TurnStepResponse) error {
	if req.WorkflowInstanceID == "" {
		return nil
	}
	_, err := l.ledger.Append(ctx, runledger.Event{
		ID:        durableTurnEventID(req),
		Type:      runledger.EventDurableTurn,
		Timestamp: time.Now().UTC(),
		SessionID: l.sessionID,
		RunID:     req.RunID,
		TaskID:    req.TaskID,
		Payload: map[string]any{
			"workflow_instance_id": req.WorkflowInstanceID,
			"activity":             req.ActivityName,
			"generation":           req.Generation,
			"turn_index":           req.TurnIndex,
			"kind":                 string(resp.Kind),
			"decision":             string(resp.Decision),
		},
	})
	if err != nil {
		return fmt.Errorf("goalloop: record durable turn %s/%d.%d: %w", req.TaskID, req.Generation, req.TurnIndex, err)
	}
	return nil
}

func (l *Loop) recordDurableTurnReceipt(ctx context.Context, req TurnStepRequest, resp TurnStepResponse, inputDigest string, step runledger.ExecutionStep) (string, string, error) {
	responseJSON, err := json.Marshal(resp)
	if err != nil {
		return "", "", fmt.Errorf("goalloop: marshal V3 durable turn receipt: %w", err)
	}
	outputDigest := sha256.Sum256(responseJSON)
	eventID := durableTurnEventID(req)
	_, err = l.ledger.Append(ctx, runledger.Event{
		ID:        eventID,
		Type:      runledger.EventDurableTurn,
		Timestamp: time.Now().UTC(),
		SessionID: l.sessionID,
		RunID:     req.RunID,
		TaskID:    req.TaskID,
		Payload: map[string]any{
			"step_id":              step.StepID,
			"attempt":              step.Attempt,
			"workflow_instance_id": req.WorkflowInstanceID,
			"activity":             req.ActivityName,
			"generation":           req.Generation,
			"turn_index":           req.TurnIndex,
			"kind":                 string(resp.Kind),
			"decision":             string(resp.Decision),
			"receipt_schema":       runledger.DurableTurnReceiptSchemaV1,
			"input_digest":         inputDigest,
			"response_json":        string(responseJSON),
			"output_digest":        hex.EncodeToString(outputDigest[:]),
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("goalloop: record V3 durable turn %s/%d.%d: %w", req.TaskID, req.Generation, req.TurnIndex, err)
	}
	return eventID, hex.EncodeToString(outputDigest[:]), nil
}

func (l *Loop) loadDurableTurnReceipt(ctx context.Context, req TurnStepRequest, inputDigest string, step runledger.ExecutionStep) (TurnStepResponse, bool, error) {
	events, err := l.ledger.ListEvents(ctx, runledger.EventQuery{
		RunID:  req.RunID,
		TaskID: req.TaskID,
		Types:  []string{runledger.EventDurableTurn},
	})
	if err != nil {
		return TurnStepResponse{}, false, fmt.Errorf("goalloop: load V3 durable turn receipt: %w", err)
	}
	eventID := durableTurnEventID(req)
	for _, event := range events {
		if event.ID != eventID {
			continue
		}
		if schema, _ := event.Payload["receipt_schema"].(string); schema != runledger.DurableTurnReceiptSchemaV1 {
			return TurnStepResponse{}, false, fmt.Errorf("goalloop: durable turn %s has unsupported receipt schema %q", eventID, schema)
		}
		if got, _ := event.Payload["step_id"].(string); got != step.StepID {
			return TurnStepResponse{}, false, fmt.Errorf("goalloop: durable turn %s receipt step changed", eventID)
		}
		attempt, ok := receiptInteger(event.Payload["attempt"])
		if !ok || attempt <= 0 || attempt != step.Attempt {
			return TurnStepResponse{}, false, fmt.Errorf("goalloop: durable turn %s receipt attempt changed", eventID)
		}
		if got, _ := event.Payload["input_digest"].(string); got != inputDigest {
			return TurnStepResponse{}, false, fmt.Errorf("goalloop: durable turn %s receipt input changed", eventID)
		}
		encoded, _ := event.Payload["response_json"].(string)
		if strings.TrimSpace(encoded) == "" {
			return TurnStepResponse{}, false, fmt.Errorf("goalloop: durable turn %s receipt is empty", eventID)
		}
		sum := sha256.Sum256([]byte(encoded))
		outputDigest := hex.EncodeToString(sum[:])
		if got, _ := event.Payload["output_digest"].(string); got != outputDigest {
			return TurnStepResponse{}, false, fmt.Errorf("goalloop: durable turn %s receipt output digest changed", eventID)
		}
		if step.Status == runledger.StepCompleted {
			if step.OutputEvidenceID != event.ID || step.OutputDigest != outputDigest {
				return TurnStepResponse{}, false, fmt.Errorf("goalloop: durable turn %s receipt is not the completed step output", eventID)
			}
		}
		var resp TurnStepResponse
		if err := json.Unmarshal([]byte(encoded), &resp); err != nil {
			return TurnStepResponse{}, false, fmt.Errorf("goalloop: decode durable turn %s receipt: %w", eventID, err)
		}
		return resp, true, nil
	}
	return TurnStepResponse{}, false, nil
}

func receiptInteger(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), int64(int(typed)) == typed
	case float64:
		integer := int(typed)
		return integer, float64(integer) == typed
	default:
		return 0, false
	}
}
