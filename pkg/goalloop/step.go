package goalloop

import (
	"context"
	"errors"
	"fmt"
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
	EvidenceFingerprint  string                        `json:"evidence_fingerprint,omitempty"`
	NoProgressTurns      int                           `json:"no_progress_turns,omitempty"`
}

func snapshotOf(d *driveState) DriveSnapshot {
	return DriveSnapshot{
		Summary:              d.summary,
		NextActions:          d.nextActions,
		Checks:               d.checks,
		Questions:            d.questions,
		Phase:                d.phase,
		PrematureCompletions: d.prematureCompletions,
		EvidenceFingerprint:  d.lastEvidenceFingerprint,
		NoProgressTurns:      d.noProgressTurns,
	}
}

func (s DriveSnapshot) driveState() *driveState {
	phase := s.Phase
	if phase == "" {
		phase = PhaseExecute
	}
	return &driveState{
		summary:                 s.Summary,
		nextActions:             s.NextActions,
		checks:                  s.Checks,
		questions:               s.Questions,
		phase:                   phase,
		prematureCompletions:    s.PrematureCompletions,
		lastEvidenceFingerprint: s.EvidenceFingerprint,
		noProgressTurns:         s.NoProgressTurns,
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
	Kind StepKind
	// Decision is the progress-controller decision that produced Kind,
	// empty when the engine itself completed, blocked, or verified.
	Decision agentloop.ProgressDecision
	// Status is the task's status after this turn.
	Status       string
	Drive        DriveSnapshot
	Counters     agentloop.FuseCounters
	TurnSpentUSD float64
	Rounds       int
	ToolCalls    int
}

// TurnStep drives exactly one turn: engine call, spend recording, drive
// absorption, completion gating, and the progress decision, with the
// same checkpoint boundaries as RunTask. It exists so a durable workflow
// can schedule turns as retry-safe activities while the local loop keeps
// identical semantics.
func (l *Loop) TurnStep(ctx context.Context, req TurnStepRequest) (TurnStepResponse, error) {
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

	drive.observeHarnessProgress(outcome)
	drive.absorb(outcome)
	// A verify phase lasts one turn; the outcome's checks decide what
	// happens next.
	drive.phase = PhaseExecute

	finish := func(kind StepKind, status string) (TurnStepResponse, error) {
		resp.Kind = kind
		resp.Status = status
		resp.Drive = snapshotOf(drive)
		l.recordDurableTurn(ctx, req, resp)
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

// recordDurableTurn projects a durable scheduler's identity onto the run
// ledger, so the Dapr workflow history and Buckley's audit reconcile per
// spec.durable-execution-dapr. Local drives skip it.
func (l *Loop) recordDurableTurn(ctx context.Context, req TurnStepRequest, resp TurnStepResponse) {
	if req.WorkflowInstanceID == "" {
		return
	}
	_, _ = l.ledger.Append(ctx, runledger.Event{
		ID: runledger.StableEventID(
			runledger.EventDurableTurn,
			req.RunID,
			req.TaskID,
			req.WorkflowInstanceID,
			req.ActivityName,
			fmt.Sprintf("%d", req.Generation),
			fmt.Sprintf("%d", req.TurnIndex),
		),
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
}
