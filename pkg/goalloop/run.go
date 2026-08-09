package goalloop

import (
	"context"
	"fmt"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

// TaskResult is the outcome of driving one task.
type TaskResult struct {
	TaskID string
	// Status is the task's final checkpointed status for this drive:
	// completed, blocked, parked, or in_progress (a checkpoint-and-yield
	// decision such as stop_safety or checkpoint pressure).
	Status string
	// Decision is the controller decision that ended the drive, empty
	// when the engine itself completed or blocked the task.
	Decision agentloop.ProgressDecision
	Turns    int
	SpentUSD float64
}

// driveState is the mutable state one RunTask drive accumulates across
// turns: the latest summary and next actions, the merged verification
// checks, and the deferred questions. It seeds from the resume
// checkpoint so a restarted drive continues the same record.
type driveState struct {
	summary     string
	nextActions []taskstate.NextAction
	checks      []taskstate.VerificationEntry
	questions   []taskstate.Question
	phase       string
	// prematureCompletions counts completion claims the verification
	// gate rejected; the second one parks the task instead of looping.
	prematureCompletions int
}

func newDriveState(spec TaskSpec, resume *taskstate.ResumeContext) *driveState {
	d := &driveState{summary: spec.Title, phase: PhaseExecute}
	if resume != nil {
		if resume.State.Summary != "" {
			d.summary = resume.State.Summary
		}
		d.nextActions = resume.State.NextActions
		d.checks = resume.State.Checks
		d.questions = resume.State.Questions
	}
	return d
}

// absorb folds one turn's outcome into the drive state.
func (d *driveState) absorb(outcome TurnOutcome) {
	if outcome.Summary != "" {
		d.summary = outcome.Summary
	}
	if len(outcome.NextActions) > 0 {
		d.nextActions = outcome.NextActions
	}
	for _, check := range outcome.Checks {
		d.mergeCheck(check)
	}
	d.questions = append(d.questions, outcome.Questions...)
}

// mergeCheck replaces the entry with the same check name, or appends.
func (d *driveState) mergeCheck(check taskstate.VerificationEntry) {
	for i, existing := range d.checks {
		if existing.Check == check.Check {
			d.checks[i] = check
			return
		}
	}
	d.checks = append(d.checks, check)
}

// debt is the drive's current verification debt: unresolved checks.
func (d *driveState) debt() int {
	debt := 0
	for _, check := range d.checks {
		if check.Status != taskstate.VerificationPass {
			debt++
		}
	}
	return debt
}

// RunTask drives turns for one task until the engine completes or blocks
// it, or the progress controller returns a non-continue decision (design
// section 5.2). Completion is gated (design 5.4): a completion claim
// with unmet required checks routes the next turn into the verify phase
// instead of completing, and a second premature claim parks the task.
// Every exit path checkpoints first, so a crash between drives loses at
// most one un-checkpointed turn, and every controller decision lands in
// the ledger with its policy trace.
func (l *Loop) RunTask(ctx context.Context, runID, taskID string, goal Goal, spec TaskSpec) (TaskResult, error) {
	if l.engine == nil {
		return TaskResult{}, errNoEngine
	}

	result := TaskResult{TaskID: taskID, Status: taskstate.StatusInProgress}
	started := time.Now()
	seed, err := l.SeedTask(ctx, taskID, spec)
	if err != nil {
		return TaskResult{}, err
	}
	generation := seed.Generation
	turnIndex := 0
	snapshot := seed.Drive
	counters := agentloop.FuseCounters{}

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		step, err := l.TurnStep(ctx, TurnStepRequest{
			RunID:        runID,
			TaskID:       taskID,
			Goal:         goal,
			Spec:         spec,
			Generation:   generation,
			TurnIndex:    turnIndex,
			Drive:        snapshot,
			Counters:     counters,
			DriveStarted: started,
		})
		if err != nil {
			return result, err
		}
		result.Turns++
		result.SpentUSD += step.TurnSpentUSD
		snapshot = step.Drive
		counters = step.Counters
		turnIndex++
		if step.Decision != "" {
			result.Decision = step.Decision
		}

		switch step.Kind {
		case StepContinue, StepVerify:
			continue
		case StepCheckpoint:
			generation++
			turnIndex = 0
			continue
		case StepCompleted, StepBlocked, StepPark:
			result.Status = step.Status
			return result, nil
		default: // StepYield
			return result, nil
		}
	}
}

// progressState maps a turn outcome onto the controller's signal set.
// Engine-internal signals (repetition, novelty, pressure) are consulted
// by the shared engine's own per-round hook; the goal loop's per-turn
// consultation adds the budget and verification-debt dimensions.
func progressState(outcome TurnOutcome, goal Goal, spentUSD float64, drive *driveState) agentloop.ProgressState {
	state := agentloop.ProgressState{
		StateChanged:     outcome.StateChanged,
		TaskDone:         outcome.Completed,
		VerificationDebt: float64(drive.debt()),
	}
	if goal.BudgetUSD > 0 {
		state.BudgetSet = true
		state.BudgetRemaining = (goal.BudgetUSD - spentUSD) / goal.BudgetUSD
	}
	return state
}

// tryFinishTask attempts an evidence-gated completion. The checkpoint
// schema is the gate (taskstate.Validate): a completed state with an
// unevidenced claim or an unmet required check does not persist. A
// rejected claim is not an error — it increments the premature counter
// and the caller routes to the verify phase; any other failure is real.
func (l *Loop) tryFinishTask(ctx context.Context, runID, taskID string, outcome TurnOutcome, drive *driveState) (bool, error) {
	state := l.driveCheckpoint(taskID, taskstate.StatusCompleted, drive, nil)
	state.Completed = []taskstate.CompletedItem{{
		Text:       drive.summary,
		EvidenceID: outcome.CompletedEvidenceID,
	}}
	if err := state.Validate(); err != nil {
		drive.prematureCompletions++
		l.recordGateRejection(ctx, runID, taskID, err)
		return false, nil
	}

	if _, err := l.checkpoints.Save(ctx, taskstate.SaveInput{
		State:     state,
		Reason:    taskstate.TriggerDecisionRecorded,
		SessionID: l.sessionID,
		RunID:     runID,
	}); err != nil {
		return false, fmt.Errorf("goalloop: completion checkpoint for %s: %w", taskID, err)
	}
	_, err := l.ledger.Append(ctx, runledger.Event{
		Type:      runledger.EventTaskCompleted,
		Timestamp: time.Now().UTC(),
		SessionID: l.sessionID,
		RunID:     runID,
		TaskID:    taskID,
		Payload:   map[string]any{"evidence_id": outcome.CompletedEvidenceID},
	})
	if err != nil {
		return false, fmt.Errorf("goalloop: record completion for %s: %w", taskID, err)
	}
	return true, nil
}

// recordGateRejection puts the verification gate's refusal on the ledger
// so a morning reader can see why a task looped into verify.
func (l *Loop) recordGateRejection(ctx context.Context, runID, taskID string, gateErr error) {
	_, _ = l.ledger.Append(ctx, runledger.Event{
		Type:      runledger.EventControllerDecision,
		Timestamp: time.Now().UTC(),
		SessionID: l.sessionID,
		RunID:     runID,
		TaskID:    taskID,
		Payload: map[string]any{
			"kind":     "goal_loop",
			"decision": "verification_gate_rejected_completion",
			"reason":   gateErr.Error(),
		},
	})
}

func (l *Loop) blockTask(ctx context.Context, runID, taskID string, blocker *taskstate.Blocker, drive *driveState) error {
	if err := l.checkpointTask(ctx, runID, taskID, taskstate.StatusBlocked, drive, blocker, taskstate.TriggerBlocker); err != nil {
		return err
	}
	_, err := l.ledger.Append(ctx, runledger.Event{
		Type:      runledger.EventTaskBlocked,
		Timestamp: time.Now().UTC(),
		SessionID: l.sessionID,
		RunID:     runID,
		TaskID:    taskID,
		Payload:   map[string]any{"reason": blocker.Reason, "needs": blocker.Needs},
	})
	if err != nil {
		return fmt.Errorf("goalloop: record blocker for %s: %w", taskID, err)
	}
	return nil
}

func (l *Loop) parkTask(ctx context.Context, runID, taskID string, blocker *taskstate.Blocker, drive *driveState) error {
	if err := l.checkpointTask(ctx, runID, taskID, taskstate.StatusParked, drive, blocker, taskstate.TriggerBlocker); err != nil {
		return err
	}
	_, err := l.ledger.Append(ctx, runledger.Event{
		Type:      runledger.EventTaskBlocked,
		Timestamp: time.Now().UTC(),
		SessionID: l.sessionID,
		RunID:     runID,
		TaskID:    taskID,
		Payload:   map[string]any{"reason": blocker.Reason, "parked": true},
	})
	if err != nil {
		return fmt.Errorf("goalloop: record park for %s: %w", taskID, err)
	}
	return nil
}

func (l *Loop) checkpointTask(ctx context.Context, runID, taskID, status string, drive *driveState, blocker *taskstate.Blocker, reason taskstate.TriggerReason) error {
	_, err := l.checkpoints.Save(ctx, taskstate.SaveInput{
		State:     l.driveCheckpoint(taskID, status, drive, blocker),
		Reason:    reason,
		SessionID: l.sessionID,
		RunID:     runID,
	})
	if err != nil {
		return fmt.Errorf("goalloop: checkpoint %s: %w", taskID, err)
	}
	return nil
}

// driveCheckpoint builds the checkpoint state a drive persists: the
// running summary and next actions plus the merged verification checks
// and every deferred question, so the durable record always carries the
// full debt and question load for the morning report.
func (l *Loop) driveCheckpoint(taskID, status string, drive *driveState, blocker *taskstate.Blocker) taskstate.CheckpointState {
	return taskstate.CheckpointState{
		Schema:      taskstate.SchemaVersion,
		TaskID:      taskID,
		Status:      status,
		Summary:     drive.summary,
		NextActions: drive.nextActions,
		Checks:      drive.checks,
		Questions:   drive.questions,
		Blocker:     blocker,
	}
}

func (l *Loop) recordDecision(ctx context.Context, runID, taskID, mode string, decision agentloop.ProgressResult) {
	trace := make([]map[string]any, 0, len(decision.Trace))
	for _, step := range decision.Trace {
		trace = append(trace, map[string]any{"rule": step.Rule, "fired": step.Fired})
	}
	_, _ = l.ledger.Append(ctx, runledger.Event{
		Type:      runledger.EventControllerDecision,
		Timestamp: time.Now().UTC(),
		SessionID: l.sessionID,
		RunID:     runID,
		TaskID:    taskID,
		Payload: map[string]any{
			"kind":     "goal_loop",
			"decision": string(decision.Decision),
			"reason":   decision.Reason,
			"mode":     mode,
			"applied":  decision.Apply && decision.Decision != agentloop.DecideContinue,
			"trace":    trace,
		},
	})
}

// Drain runs the queue to exhaustion: it rebuilds the queue, drives the
// first runnable task, and repeats until the queue is empty or the
// context ends. Tasks that finish a drive still in_progress (a
// checkpoint-and-yield) go to the back through the rebuild; blocked,
// parked, and completed tasks leave the queue via their checkpoints.
func (l *Loop) Drain(ctx context.Context, runID string, goal Goal, specs map[string]TaskSpec) ([]TaskResult, error) {
	var results []TaskResult
	yielded := map[string]int{}
	for {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		queue, err := l.BuildQueue(ctx, runID)
		if err != nil {
			return results, err
		}
		next := ""
		for _, item := range queue {
			// A task that yielded twice in this drain without changing
			// status is deferred to the next Drain call, so one spinning
			// task cannot starve the run or loop Drain forever.
			if yielded[item.TaskID] < 2 {
				next = item.TaskID
				break
			}
		}
		if next == "" {
			return results, nil
		}

		result, err := l.RunTask(ctx, runID, next, goal, specs[next])
		if err != nil {
			return results, err
		}
		results = append(results, result)
		if result.Status == taskstate.StatusInProgress {
			yielded[next]++
		} else {
			yielded[next] = 0
		}
	}
}
