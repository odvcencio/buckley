package dapr

import (
	"fmt"
	"sort"
	"time"

	"github.com/dapr/durabletask-go/workflow"

	"m31labs.dev/buckley/pkg/durability"
	"m31labs.dev/buckley/pkg/goalloop"
)

// DefaultMaxYields matches the local Drain rule: a task that yields
// in_progress twice defers to a later run.
const DefaultMaxYields = 2

// turnRetryPolicy is the single activity-retry owner for transient
// worker and provider failure (spec retry precedence, step 2). A retried
// turn re-sends the same generation and index, so completed steps replay
// from evidence instead of re-executing.
var turnRetryPolicy = &workflow.RetryPolicy{
	MaxAttempts:          3,
	InitialRetryInterval: 2 * time.Second,
	BackoffCoefficient:   2,
	MaxRetryInterval:     30 * time.Second,
}

// goalWorkflow is GoalWorkflowV1: deterministic goal orchestration. It
// pulls the next runnable task through an activity and runs one child
// task workflow per pull, sequentially — fan-out arrives with workspace
// claims in Phase 2.
func goalWorkflow(ctx *workflow.WorkflowContext) (any, error) {
	var start durability.GoalStart
	if err := ctx.GetInput(&start); err != nil {
		return nil, fmt.Errorf("goal workflow input: %w", err)
	}
	maxYields := start.MaxYields
	if maxYields <= 0 {
		maxYields = DefaultMaxYields
	}

	yields := map[string]int{}
	calls := 0
	result := durability.GoalResult{}
	for {
		var next durability.NextTaskResponse
		if err := ctx.CallActivity(ActivityNextTask,
			workflow.WithActivityInput(durability.NextTaskRequest{RunID: start.RunID, Deferred: deferredTasks(yields, maxYields)}),
		).Await(&next); err != nil {
			return result, fmt.Errorf("next task: %w", err)
		}
		if next.Done || next.TaskID == "" {
			return result, nil
		}

		calls++
		var task durability.TaskOutcome
		if err := ctx.CallChildWorkflow(TaskWorkflowV1,
			workflow.WithChildWorkflowInput(taskStart{RunID: start.RunID, TaskID: next.TaskID}),
			workflow.WithChildWorkflowInstanceID(fmt.Sprintf("%s::%s::%d", ctx.ID(), next.TaskID, calls)),
		).Await(&task); err != nil {
			return result, fmt.Errorf("task workflow %s: %w", next.TaskID, err)
		}
		result.Tasks = append(result.Tasks, task)
		if task.Status == "in_progress" {
			yields[next.TaskID]++
		} else {
			yields[next.TaskID] = 0
		}
	}
}

// goalWorkflowV2 adds bounded fan-out (spec Phase 2): each round pulls
// the largest claim-independent batch and runs one child task workflow
// per task concurrently. Scheduling every child before awaiting any is
// what makes them run in parallel; the awaits are the fan-in barrier.
func goalWorkflowV2(ctx *workflow.WorkflowContext) (any, error) {
	return goalFanout(ctx, TaskWorkflowV1, false)
}

// goalWorkflowV3 keeps the V2 fan-out and schedules approval-aware
// TaskWorkflowV2 children, threading the goal's approval wait through.
func goalWorkflowV3(ctx *workflow.WorkflowContext) (any, error) {
	return goalFanout(ctx, TaskWorkflowV2, true)
}

func goalFanout(ctx *workflow.WorkflowContext, childWorkflow string, forwardApprovalWait bool) (any, error) {
	var start durability.GoalStart
	if err := ctx.GetInput(&start); err != nil {
		return nil, fmt.Errorf("goal workflow input: %w", err)
	}
	maxYields := start.MaxYields
	if maxYields <= 0 {
		maxYields = DefaultMaxYields
	}

	yields := map[string]int{}
	calls := 0
	result := durability.GoalResult{}
	for {
		var batch durability.NextBatchResponse
		if err := ctx.CallActivity(ActivityNextBatch,
			workflow.WithActivityInput(durability.NextBatchRequest{
				RunID:       start.RunID,
				Deferred:    deferredTasks(yields, maxYields),
				MaxParallel: start.MaxParallel,
			}),
		).Await(&batch); err != nil {
			return result, fmt.Errorf("next batch: %w", err)
		}
		if batch.Done || len(batch.Tasks) == 0 {
			return result, nil
		}

		children := make([]workflow.Task, len(batch.Tasks))
		for i, item := range batch.Tasks {
			calls++
			child := taskStart{RunID: start.RunID, TaskID: item.TaskID}
			if forwardApprovalWait {
				child.ApprovalWaitMS = start.ApprovalWaitMS
			}
			children[i] = ctx.CallChildWorkflow(childWorkflow,
				workflow.WithChildWorkflowInput(child),
				workflow.WithChildWorkflowInstanceID(fmt.Sprintf("%s::%s::%d", ctx.ID(), item.TaskID, calls)),
			)
		}
		for i, child := range children {
			var task durability.TaskOutcome
			if err := child.Await(&task); err != nil {
				return result, fmt.Errorf("task workflow %s: %w", batch.Tasks[i].TaskID, err)
			}
			result.Tasks = append(result.Tasks, task)
			if task.Status == "in_progress" {
				yields[task.TaskID]++
			} else {
				yields[task.TaskID] = 0
			}
		}
	}
}

// deferredTasks lists tasks past the yield bound, sorted so replayed
// workflow histories serialize identically.
func deferredTasks(yields map[string]int, maxYields int) []string {
	var deferred []string
	for taskID, count := range yields {
		if count >= maxYields {
			deferred = append(deferred, taskID)
		}
	}
	sort.Strings(deferred)
	return deferred
}

// taskStart is the child workflow input.
type taskStart struct {
	RunID  string `json:"run_id"`
	TaskID string `json:"task_id"`
	// ApprovalWaitMS is consumed by TaskWorkflowV2 only.
	ApprovalWaitMS int64 `json:"approval_wait_ms,omitempty"`
}

// taskWorkflow is TaskWorkflowV1: it seeds the drive from the durable
// checkpoint, then schedules one TurnActivity per turn, carrying only
// the compact drive snapshot and counters as workflow state.
func taskWorkflow(ctx *workflow.WorkflowContext) (any, error) {
	var start taskStart
	if err := ctx.GetInput(&start); err != nil {
		return nil, fmt.Errorf("task workflow input: %w", err)
	}
	return driveTaskTurns(ctx, start)
}

// taskWorkflowV2 wraps the V1 turn drive with a durable approval wait:
// a parked task holds on WaitForExternalEvent instead of ending, and an
// approved decision unparks the task and drives a fresh generation.
func taskWorkflowV2(ctx *workflow.WorkflowContext) (any, error) {
	var start taskStart
	if err := ctx.GetInput(&start); err != nil {
		return nil, fmt.Errorf("task workflow input: %w", err)
	}
	total := durability.TaskOutcome{TaskID: start.TaskID, Status: "in_progress"}
	for {
		round, err := driveTaskTurns(ctx, start)
		total.Turns += round.Turns
		total.SpentUSD += round.SpentUSD
		total.Status = round.Status
		if round.Decision != "" {
			total.Decision = round.Decision
		}
		if err != nil {
			return total, err
		}
		if round.Status != "parked" || start.ApprovalWaitMS <= 0 {
			return total, nil
		}

		if err := ctx.CallActivity(ActivityRecordApprovalWait,
			workflow.WithActivityInput(durability.ApprovalWait{
				RunID:              start.RunID,
				TaskID:             start.TaskID,
				WorkflowInstanceID: ctx.ID(),
				Reason:             round.Decision,
			}),
		).Await(nil); err != nil {
			return total, fmt.Errorf("record approval wait: %w", err)
		}

		var decision durability.ApprovalDecision
		waitErr := ctx.WaitForExternalEvent(durability.ApprovalEventName, time.Duration(start.ApprovalWaitMS)*time.Millisecond).Await(&decision)
		resolution := durability.ApprovalResolution{
			RunID:              start.RunID,
			TaskID:             start.TaskID,
			WorkflowInstanceID: ctx.ID(),
			Reason:             decision.Reason,
		}
		switch {
		case waitErr != nil:
			resolution.Outcome = durability.ApprovalTimedOut
		case decision.Approved:
			resolution.Outcome = durability.ApprovalApproved
		default:
			resolution.Outcome = durability.ApprovalDenied
		}
		if err := ctx.CallActivity(ActivityResolveApproval,
			workflow.WithActivityInput(resolution),
		).Await(nil); err != nil {
			return total, fmt.Errorf("resolve approval: %w", err)
		}
		if resolution.Outcome != durability.ApprovalApproved {
			return total, nil
		}
		// Approved: loop. driveTaskTurns re-seeds from the unparked
		// checkpoint, opening a new generation.
	}
}

// driveTaskTurns is the shared turn drive: seed once, then one
// TurnActivity per turn until the task ends or yields.
func driveTaskTurns(ctx *workflow.WorkflowContext, start taskStart) (durability.TaskOutcome, error) {
	outcome := durability.TaskOutcome{TaskID: start.TaskID, Status: "in_progress"}

	var seed durability.ResumeSeed
	if err := ctx.CallActivity(ActivityResumeSeed,
		workflow.WithActivityInput(taskStart{RunID: start.RunID, TaskID: start.TaskID}),
	).Await(&seed); err != nil {
		return outcome, fmt.Errorf("resume seed: %w", err)
	}

	generation := seed.Generation
	turnIndex := 0
	drive := seed.Drive
	modelRequests, toolExecutions := 0, 0
	driveStarted := ctx.CurrentTimeUTC()

	for {
		var turn durability.TurnResponse
		if err := ctx.CallActivity(ActivityRunTurn,
			workflow.WithActivityInput(durability.TurnRequest{
				RunID:              start.RunID,
				TaskID:             start.TaskID,
				Generation:         generation,
				TurnIndex:          turnIndex,
				Drive:              drive,
				ModelRequests:      modelRequests,
				ToolExecutions:     toolExecutions,
				ElapsedMS:          ctx.CurrentTimeUTC().Sub(driveStarted).Milliseconds(),
				WorkflowInstanceID: ctx.ID(),
			}),
			workflow.WithActivityRetryPolicy(turnRetryPolicy),
		).Await(&turn); err != nil {
			return outcome, fmt.Errorf("turn %d.%d: %w", generation, turnIndex, err)
		}

		outcome.Turns++
		outcome.SpentUSD += turn.TurnSpentUSD
		if turn.Decision != "" {
			outcome.Decision = turn.Decision
		}
		drive = turn.Drive
		modelRequests += turn.Rounds
		toolExecutions += turn.ToolCalls
		generation, turnIndex = nextTurnIdentity(goalloop.StepKind(turn.Kind), generation, turnIndex)
		if turnDone(goalloop.StepKind(turn.Kind)) {
			outcome.Status = turn.Status
			return outcome, nil
		}
	}
}

// nextTurnIdentity advances the stable turn identity for one step kind:
// a checkpoint opens a new generation, everything else advances the
// index within the current one.
func nextTurnIdentity(kind goalloop.StepKind, generation, turnIndex int) (int, int) {
	if kind == goalloop.StepCheckpoint {
		return generation + 1, 0
	}
	return generation, turnIndex + 1
}

// turnDone reports whether a step kind ends the task workflow.
func turnDone(kind goalloop.StepKind) bool {
	switch kind {
	case goalloop.StepContinue, goalloop.StepVerify, goalloop.StepCheckpoint:
		return false
	default:
		return true
	}
}

// resumeSeedActivity loads the durable seed for one task.
func resumeSeedActivity(runner durability.TaskRunner) workflow.Activity {
	return func(ctx workflow.ActivityContext) (any, error) {
		var start taskStart
		if err := ctx.GetInput(&start); err != nil {
			return nil, err
		}
		return runner.ResumeSeed(ctx.Context(), start.RunID, start.TaskID)
	}
}

// nextTaskActivity pulls the next runnable task from the host queue.
func nextTaskActivity(runner durability.TaskRunner) workflow.Activity {
	return func(ctx workflow.ActivityContext) (any, error) {
		var req durability.NextTaskRequest
		if err := ctx.GetInput(&req); err != nil {
			return nil, err
		}
		return runner.NextTask(ctx.Context(), req)
	}
}

// nextBatchActivity pulls the next claim-independent batch.
func nextBatchActivity(runner durability.TaskRunner) workflow.Activity {
	return func(ctx workflow.ActivityContext) (any, error) {
		var req durability.NextBatchRequest
		if err := ctx.GetInput(&req); err != nil {
			return nil, err
		}
		return runner.NextBatch(ctx.Context(), req)
	}
}

// recordApprovalWaitActivity lands the wait on the ledger before the
// workflow blocks on the external event.
func recordApprovalWaitActivity(runner durability.TaskRunner) workflow.Activity {
	return func(ctx workflow.ActivityContext) (any, error) {
		var wait durability.ApprovalWait
		if err := ctx.GetInput(&wait); err != nil {
			return nil, err
		}
		return nil, runner.RecordApprovalWait(ctx.Context(), wait)
	}
}

// resolveApprovalActivity records how a wait ended; approvals unpark.
func resolveApprovalActivity(runner durability.TaskRunner) workflow.Activity {
	return func(ctx workflow.ActivityContext) (any, error) {
		var resolution durability.ApprovalResolution
		if err := ctx.GetInput(&resolution); err != nil {
			return nil, err
		}
		return nil, runner.ResolveApproval(ctx.Context(), resolution)
	}
}

// runTurnActivity is TurnActivity: one Buckley turn per invocation,
// side effects owned by the host, output recorded durably before the
// workflow runtime sees success.
func runTurnActivity(runner durability.TaskRunner) workflow.Activity {
	return func(ctx workflow.ActivityContext) (any, error) {
		var req durability.TurnRequest
		if err := ctx.GetInput(&req); err != nil {
			return nil, err
		}
		return runner.RunTurn(ctx.Context(), req)
	}
}
