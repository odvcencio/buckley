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

	task := TaskContext{RunID: runID, TaskID: taskID, Goal: goal, Spec: spec, Phase: PhaseExecute}
	if resumed, err := l.checkpoints.Resume(ctx, taskID); err == nil {
		task.Resume = &resumed
	} else if !errors.Is(err, taskstate.ErrNoCheckpoint) {
		return TaskResult{}, fmt.Errorf("goalloop: resume %s: %w", taskID, err)
	}

	result := TaskResult{TaskID: taskID, Status: taskstate.StatusInProgress}
	started := time.Now()
	counters := agentloop.FuseCounters{}
	drive := newDriveState(spec, task.Resume)
	progress := l.progressFor(goal)
	// Budget decisions run on the goal's cumulative spend across every
	// drive and task, read back from the ledger's metric samples — not
	// on this drive's local total.
	goalSpent, err := l.ledger.SumMetric(ctx, runID, costUSDMetric)
	if err != nil {
		return result, fmt.Errorf("goalloop: read spend for %s: %w", runID, err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		task.Phase = drive.phase
		outcome, err := l.engine.RunTurn(ctx, task)
		if err != nil {
			return result, fmt.Errorf("goalloop: turn for %s: %w", taskID, err)
		}
		result.Turns++
		result.SpentUSD += outcome.SpentUSD
		goalSpent, err = l.recordTurnSpend(ctx, runID, taskID, goal, outcome)
		if err != nil {
			return result, err
		}
		counters.ModelRequests += outcome.Rounds
		counters.ToolExecutions += outcome.ToolCalls
		counters.Elapsed = time.Since(started)
		counters.SpentUSD = goalSpent
		drive.absorb(outcome)
		// A verify phase lasts one turn; the outcome's checks decide
		// what happens next.
		drive.phase = PhaseExecute

		if outcome.Completed {
			finished, err := l.tryFinishTask(ctx, runID, taskID, outcome, drive)
			if err != nil {
				return result, err
			}
			if finished {
				result.Status = taskstate.StatusCompleted
				return result, nil
			}
			if drive.prematureCompletions >= 2 {
				result.Status = taskstate.StatusParked
				blocker := &taskstate.Blocker{
					Reason: "completion blocked: required verification is not evidenced",
					Needs:  "evidence for required checks",
				}
				return result, l.parkTask(ctx, runID, taskID, blocker, drive)
			}
			drive.phase = PhaseVerify
			continue
		}
		if outcome.Blocker != nil {
			result.Status = taskstate.StatusBlocked
			return result, l.blockTask(ctx, runID, taskID, outcome.Blocker, drive)
		}

		decision := progress.Decide(progressState(outcome, goal, goalSpent, drive), counters)
		l.recordDecision(ctx, runID, taskID, progress.Mode, decision)
		if decision.Decision == agentloop.DecideContinue || !decision.Apply {
			continue
		}

		result.Decision = decision.Decision
		switch decision.Decision {
		case agentloop.DecideVerify:
			// Route the next turn into the verify phase instead of
			// yielding: cheap verification before more exploration.
			drive.phase = PhaseVerify
			continue
		case agentloop.DecideCheckpoint:
			if err := l.checkpointTask(ctx, runID, taskID, taskstate.StatusInProgress, drive, nil, taskstate.TriggerPressure); err != nil {
				return result, err
			}
			continue
		case agentloop.DecidePark:
			result.Status = taskstate.StatusParked
			blocker := &taskstate.Blocker{Reason: decision.Reason}
			return result, l.parkTask(ctx, runID, taskID, blocker, drive)
		default:
			// replan, synthesize, and stop_safety end this drive: the
			// loop yields to the scheduler with a checkpoint; replan and
			// synthesis passes ride later slices.
			return result, l.checkpointTask(ctx, runID, taskID, taskstate.StatusInProgress, drive, nil, taskstate.TriggerDecisionRecorded)
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
