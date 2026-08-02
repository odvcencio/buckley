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

// RunTask drives turns for one task until the engine completes or blocks
// it, or the progress controller returns a non-continue decision (design
// section 5.2). Every exit path checkpoints first, so a crash between
// drives loses at most one un-checkpointed turn, and every controller
// decision lands in the ledger with its policy trace.
func (l *Loop) RunTask(ctx context.Context, runID, taskID string, goal Goal, spec TaskSpec) (TaskResult, error) {
	if l.engine == nil {
		return TaskResult{}, errNoEngine
	}

	task := TaskContext{RunID: runID, TaskID: taskID, Goal: goal, Spec: spec}
	if resumed, err := l.checkpoints.Resume(ctx, taskID); err == nil {
		task.Resume = &resumed
	} else if !errors.Is(err, taskstate.ErrNoCheckpoint) {
		return TaskResult{}, fmt.Errorf("goalloop: resume %s: %w", taskID, err)
	}

	result := TaskResult{TaskID: taskID, Status: taskstate.StatusInProgress}
	started := time.Now()
	counters := agentloop.FuseCounters{}
	summary := spec.Title
	var nextActions []taskstate.NextAction
	if task.Resume != nil {
		summary = task.Resume.State.Summary
		nextActions = task.Resume.State.NextActions
	}

	for {
		if err := ctx.Err(); err != nil {
			return result, err
		}

		outcome, err := l.engine.RunTurn(ctx, task)
		if err != nil {
			return result, fmt.Errorf("goalloop: turn for %s: %w", taskID, err)
		}
		result.Turns++
		result.SpentUSD += outcome.SpentUSD
		counters.ModelRequests += outcome.Rounds
		counters.ToolExecutions += outcome.ToolCalls
		counters.Elapsed = time.Since(started)
		counters.SpentUSD = result.SpentUSD
		if outcome.Summary != "" {
			summary = outcome.Summary
		}
		if len(outcome.NextActions) > 0 {
			nextActions = outcome.NextActions
		}

		if outcome.Completed {
			result.Status = taskstate.StatusCompleted
			return result, l.finishTask(ctx, runID, taskID, goal, outcome, summary)
		}
		if outcome.Blocker != nil {
			result.Status = taskstate.StatusBlocked
			return result, l.blockTask(ctx, runID, taskID, outcome.Blocker, summary, nextActions)
		}

		decision := l.progress.Decide(progressState(outcome, goal, result.SpentUSD), counters)
		l.recordDecision(ctx, runID, taskID, decision)
		if decision.Decision == agentloop.DecideContinue || !decision.Apply {
			continue
		}

		result.Decision = decision.Decision
		switch decision.Decision {
		case agentloop.DecideCheckpoint:
			if err := l.checkpointTask(ctx, runID, taskID, taskstate.StatusInProgress, summary, nextActions, nil, taskstate.TriggerPressure); err != nil {
				return result, err
			}
			continue
		case agentloop.DecidePark:
			result.Status = taskstate.StatusParked
			blocker := &taskstate.Blocker{Reason: decision.Reason}
			return result, l.parkTask(ctx, runID, taskID, blocker, summary, nextActions)
		default:
			// verify, replan, synthesize, and stop_safety all end this
			// drive: G6 yields to the scheduler with a checkpoint; the
			// richer routing (a verify turn, a replan pass) is G7 scope.
			return result, l.checkpointTask(ctx, runID, taskID, taskstate.StatusInProgress, summary, nextActions, nil, taskstate.TriggerDecisionRecorded)
		}
	}
}

// progressState maps a turn outcome onto the controller's signal set.
// Engine-internal signals (repetition, novelty, pressure) are consulted
// by the shared engine's own per-round hook; the goal loop's per-turn
// consultation adds the budget dimension.
func progressState(outcome TurnOutcome, goal Goal, spentUSD float64) agentloop.ProgressState {
	state := agentloop.ProgressState{
		StateChanged: outcome.StateChanged,
		TaskDone:     outcome.Completed,
	}
	if goal.BudgetUSD > 0 {
		state.BudgetSet = true
		state.BudgetRemaining = (goal.BudgetUSD - spentUSD) / goal.BudgetUSD
	}
	return state
}

func (l *Loop) finishTask(ctx context.Context, runID, taskID string, goal Goal, outcome TurnOutcome, summary string) error {
	state := taskstate.CheckpointState{
		Schema:  taskstate.SchemaVersion,
		TaskID:  taskID,
		Status:  taskstate.StatusCompleted,
		Summary: summary,
		Completed: []taskstate.CompletedItem{{
			Text:       summary,
			EvidenceID: outcome.CompletedEvidenceID,
		}},
	}
	if _, err := l.checkpoints.Save(ctx, taskstate.SaveInput{
		State:     state,
		Reason:    taskstate.TriggerDecisionRecorded,
		SessionID: l.sessionID,
		RunID:     runID,
	}); err != nil {
		return fmt.Errorf("goalloop: completion checkpoint for %s: %w", taskID, err)
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
		return fmt.Errorf("goalloop: record completion for %s: %w", taskID, err)
	}
	return nil
}

func (l *Loop) blockTask(ctx context.Context, runID, taskID string, blocker *taskstate.Blocker, summary string, nextActions []taskstate.NextAction) error {
	if err := l.checkpointTask(ctx, runID, taskID, taskstate.StatusBlocked, summary, nextActions, blocker, taskstate.TriggerBlocker); err != nil {
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

func (l *Loop) parkTask(ctx context.Context, runID, taskID string, blocker *taskstate.Blocker, summary string, nextActions []taskstate.NextAction) error {
	if err := l.checkpointTask(ctx, runID, taskID, taskstate.StatusParked, summary, nextActions, blocker, taskstate.TriggerBlocker); err != nil {
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

func (l *Loop) checkpointTask(ctx context.Context, runID, taskID, status, summary string, nextActions []taskstate.NextAction, blocker *taskstate.Blocker, reason taskstate.TriggerReason) error {
	_, err := l.checkpoints.Save(ctx, taskstate.SaveInput{
		State: taskstate.CheckpointState{
			Schema:      taskstate.SchemaVersion,
			TaskID:      taskID,
			Status:      status,
			Summary:     summary,
			NextActions: nextActions,
			Blocker:     blocker,
		},
		Reason:    reason,
		SessionID: l.sessionID,
		RunID:     runID,
	})
	if err != nil {
		return fmt.Errorf("goalloop: checkpoint %s: %w", taskID, err)
	}
	return nil
}

func (l *Loop) recordDecision(ctx context.Context, runID, taskID string, decision agentloop.ProgressResult) {
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
			"mode":     l.progress.Mode,
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
