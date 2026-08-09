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
}

// taskWorkflow is TaskWorkflowV1: it seeds the drive from the durable
// checkpoint, then schedules one TurnActivity per turn, carrying only
// the compact drive snapshot and counters as workflow state.
func taskWorkflow(ctx *workflow.WorkflowContext) (any, error) {
	var start taskStart
	if err := ctx.GetInput(&start); err != nil {
		return nil, fmt.Errorf("task workflow input: %w", err)
	}
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
