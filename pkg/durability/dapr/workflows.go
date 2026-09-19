package dapr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dapr/durabletask-go/task"
	"github.com/dapr/durabletask-go/workflow"

	"m31labs.dev/buckley/pkg/durability"
	"m31labs.dev/buckley/pkg/goalloop"
)

// DefaultMaxYields matches the local Drain rule: a task that yields
// in_progress twice defers to a later run.
const DefaultMaxYields = 2

const (
	defaultMaxRetryWaits = 3
	minimumRetryDelay    = time.Second
	maximumRetryDelay    = 24 * time.Hour
)

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

// retryWaitPolicy governs only the small ledger/wake activities around a
// durable retry timer. Model and tool work remains owned by Buckley's turn
// replay boundary and is never retried by this policy.
var retryWaitPolicy = &workflow.RetryPolicy{
	MaxAttempts:          3,
	InitialRetryInterval: time.Second,
	BackoffCoefficient:   2,
	MaxRetryInterval:     10 * time.Second,
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

// goalWorkflowV4 binds every turn to the goal's recorded workspace and
// finalizes Buckley's canonical run row before the workflow returns. Older
// workflow versions stay registered for in-flight histories.
func goalWorkflowV4(ctx *workflow.WorkflowContext) (any, error) {
	var start durability.GoalStart
	if err := ctx.GetInput(&start); err != nil {
		return nil, fmt.Errorf("goal workflow input: %w", err)
	}

	value, runErr := goalFanoutV4(ctx, TaskWorkflowV3, true)
	result, ok := value.(durability.GoalResult)
	if !ok {
		result = durability.GoalResult{}
		if runErr == nil {
			runErr = fmt.Errorf("goal workflow produced unexpected result %T", value)
		}
	}
	finalization := durability.GoalFinalization{
		RunID:              start.RunID,
		WorkspaceRoot:      start.WorkspaceRoot,
		WorkflowInstanceID: ctx.ID(),
		Incomplete:         result.Status == durability.GoalResultIncomplete,
	}
	if runErr != nil {
		finalization.Failure = runErr.Error()
	}
	finalizeErr := ctx.CallActivity(ActivityFinalizeGoal,
		workflow.WithActivityInput(finalization),
		workflow.WithActivityRetryPolicy(turnRetryPolicy),
	).Await(nil)
	if finalizeErr != nil {
		if runErr != nil {
			return result, fmt.Errorf("%v; finalize goal: %w", runErr, finalizeErr)
		}
		return result, fmt.Errorf("finalize goal: %w", finalizeErr)
	}
	return result, runErr
}

// goalWorkflowV5 preserves V4 finalization and fan-out behavior while
// scheduling TaskWorkflowV4, whose retryable blocked turns stay alive on a
// durable timer instead of returning a permanently blocked child.
func goalWorkflowV5(ctx *workflow.WorkflowContext) (any, error) {
	var start durability.GoalStart
	if err := ctx.GetInput(&start); err != nil {
		return nil, fmt.Errorf("goal workflow input: %w", err)
	}

	value, runErr := goalFanoutV5(ctx)
	result, ok := value.(durability.GoalResult)
	if !ok {
		result = durability.GoalResult{}
		if runErr == nil {
			runErr = fmt.Errorf("goal workflow produced unexpected result %T", value)
		}
	}
	finalization := durability.GoalFinalization{
		RunID:              start.RunID,
		WorkspaceRoot:      start.WorkspaceRoot,
		WorkflowInstanceID: ctx.ID(),
		Incomplete:         result.Status == durability.GoalResultIncomplete,
	}
	if runErr != nil {
		finalization.Failure = runErr.Error()
	}
	finalizeErr := ctx.CallActivity(ActivityFinalizeGoal,
		workflow.WithActivityInput(finalization),
		workflow.WithActivityRetryPolicy(turnRetryPolicy),
	).Await(nil)
	if finalizeErr != nil {
		if runErr != nil {
			return result, fmt.Errorf("%v; finalize goal: %w", runErr, finalizeErr)
		}
		return result, fmt.Errorf("finalize goal: %w", finalizeErr)
	}
	return result, runErr
}

func goalFanout(ctx *workflow.WorkflowContext, childWorkflow string, forwardApprovalWait bool) (any, error) {
	return goalFanoutWithMode(ctx, childWorkflow, forwardApprovalWait, false)
}

// goalFanoutV4 waits for every child scheduled in a batch before returning a
// failure. Earlier workflow versions retain their original fail-fast history;
// V4 needs the stronger barrier so finalization cannot race a sibling child.
func goalFanoutV4(ctx *workflow.WorkflowContext, childWorkflow string, forwardApprovalWait bool) (any, error) {
	return goalFanoutWithMode(ctx, childWorkflow, forwardApprovalWait, true)
}

func goalFanoutV5(ctx *workflow.WorkflowContext) (any, error) {
	var start durability.GoalStart
	if err := ctx.GetInput(&start); err != nil {
		return nil, fmt.Errorf("goal workflow input: %w", err)
	}
	maxYields := start.MaxYields
	if maxYields <= 0 {
		maxYields = DefaultMaxYields
	}

	yields := map[string]int{}
	exhausted := map[string]struct{}{}
	calls := 0
	result := durability.GoalResult{}
	for {
		var batch durability.NextBatchResponse
		if err := ctx.CallActivity(ActivityNextBatchV2,
			workflow.WithActivityInput(durability.NextBatchV2Request{
				RunID:           start.RunID,
				Deferred:        deferredTasks(yields, maxYields),
				ExcludedTaskIDs: sortedTaskSet(exhausted),
				MaxParallel:     start.MaxParallel,
			}),
		).Await(&batch); err != nil {
			return result, fmt.Errorf("next batch v2: %w", err)
		}
		if batch.Done || len(batch.Tasks) == 0 {
			result = markDeferredGoalResult(result, yields, maxYields)
			result = mergeIncompleteTaskIDs(result, batch.IncompleteTaskIDs)
			return markNoncompletedGoalResult(result), nil
		}

		children := make([]scheduledChild, len(batch.Tasks))
		for i, item := range batch.Tasks {
			calls++
			children[i] = scheduledChild{
				taskID: item.TaskID,
				task: ctx.CallChildWorkflow(TaskWorkflowV4,
					workflow.WithChildWorkflowInput(taskStart{
						RunID:          start.RunID,
						TaskID:         item.TaskID,
						WorkspaceRoot:  start.WorkspaceRoot,
						ApprovalWaitMS: start.ApprovalWaitMS,
					}),
					workflow.WithChildWorkflowInstanceID(fmt.Sprintf("%s::%s::%d", ctx.ID(), item.TaskID, calls)),
				),
			}
		}
		outcomes, batchErr := awaitScheduledChildren(children, true)
		for _, taskOutcome := range outcomes {
			result.Tasks = append(result.Tasks, taskOutcome)
			if taskOutcome.RetryExhausted || taskOutcome.GenerationDeferred {
				exhausted[taskOutcome.TaskID] = struct{}{}
			}
			if taskOutcome.Status == "in_progress" {
				yields[taskOutcome.TaskID]++
			} else {
				yields[taskOutcome.TaskID] = 0
			}
		}
		if batchErr != nil {
			return markNoncompletedGoalResult(result), batchErr
		}
	}
}

func goalFanoutWithMode(ctx *workflow.WorkflowContext, childWorkflow string, forwardApprovalWait, waitForAllChildren bool) (any, error) {
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
			if waitForAllChildren {
				result = markDeferredGoalResult(result, yields, maxYields)
			}
			return result, nil
		}

		children := make([]scheduledChild, len(batch.Tasks))
		for i, item := range batch.Tasks {
			calls++
			child := taskStart{RunID: start.RunID, TaskID: item.TaskID, WorkspaceRoot: start.WorkspaceRoot}
			if forwardApprovalWait {
				child.ApprovalWaitMS = start.ApprovalWaitMS
			}
			children[i] = scheduledChild{
				taskID: item.TaskID,
				task: ctx.CallChildWorkflow(childWorkflow,
					workflow.WithChildWorkflowInput(child),
					workflow.WithChildWorkflowInstanceID(fmt.Sprintf("%s::%s::%d", ctx.ID(), item.TaskID, calls)),
				),
			}
		}
		outcomes, batchErr := awaitScheduledChildren(children, waitForAllChildren)
		for _, task := range outcomes {
			result.Tasks = append(result.Tasks, task)
			if task.Status == "in_progress" {
				yields[task.TaskID]++
			} else {
				yields[task.TaskID] = 0
			}
		}
		if batchErr != nil {
			return result, batchErr
		}
	}
}

func mergeIncompleteTaskIDs(result durability.GoalResult, taskIDs []string) durability.GoalResult {
	seen := make(map[string]struct{}, len(result.DeferredTasks)+len(taskIDs))
	for _, taskID := range result.DeferredTasks {
		if taskID != "" {
			seen[taskID] = struct{}{}
		}
	}
	for _, taskID := range taskIDs {
		if taskID != "" {
			seen[taskID] = struct{}{}
		}
	}
	if len(seen) == 0 {
		return result
	}
	result.DeferredTasks = result.DeferredTasks[:0]
	for taskID := range seen {
		result.DeferredTasks = append(result.DeferredTasks, taskID)
	}
	sort.Strings(result.DeferredTasks)
	result.Status = durability.GoalResultIncomplete
	return result
}

func sortedTaskSet(tasks map[string]struct{}) []string {
	result := make([]string, 0, len(tasks))
	for taskID := range tasks {
		if taskID != "" {
			result = append(result, taskID)
		}
	}
	sort.Strings(result)
	return result
}

type childTask interface {
	Await(any) error
}

type scheduledChild struct {
	taskID string
	task   childTask
}

// awaitScheduledChildren is the V4 fan-in barrier. When waitForAll is true,
// an early child failure is remembered while every already-scheduled sibling
// is still awaited; only then is the combined error returned for finalization.
func awaitScheduledChildren(children []scheduledChild, waitForAll bool) ([]durability.TaskOutcome, error) {
	outcomes := make([]durability.TaskOutcome, 0, len(children))
	var childErrors []error
	for _, child := range children {
		var outcome durability.TaskOutcome
		if err := child.task.Await(&outcome); err != nil {
			wrapped := fmt.Errorf("task workflow %s: %w", child.taskID, err)
			if !waitForAll {
				return outcomes, wrapped
			}
			childErrors = append(childErrors, wrapped)
			continue
		}
		if outcome.TaskID == "" {
			outcome.TaskID = child.taskID
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, errors.Join(childErrors...)
}

func markDeferredGoalResult(result durability.GoalResult, yields map[string]int, maxYields int) durability.GoalResult {
	result.DeferredTasks = deferredTasks(yields, maxYields)
	if len(result.DeferredTasks) > 0 {
		result.Status = durability.GoalResultIncomplete
	}
	return result
}

// markNoncompletedGoalResult makes every observed nonterminal task explicit
// in a new generation's output. Bounded yield is one such case, but blocked
// and parked children are also resumable/incomplete and must not be silently
// reported as a completed generation.
func markNoncompletedGoalResult(result durability.GoalResult) durability.GoalResult {
	deferred := make(map[string]struct{}, len(result.DeferredTasks))
	for _, taskID := range result.DeferredTasks {
		deferred[taskID] = struct{}{}
	}
	for _, task := range result.Tasks {
		if task.Status != "completed" && task.TaskID != "" {
			deferred[task.TaskID] = struct{}{}
		}
	}
	if len(deferred) == 0 {
		return result
	}
	result.DeferredTasks = result.DeferredTasks[:0]
	for taskID := range deferred {
		result.DeferredTasks = append(result.DeferredTasks, taskID)
	}
	sort.Strings(result.DeferredTasks)
	result.Status = durability.GoalResultIncomplete
	return result
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
	RunID         string `json:"run_id"`
	TaskID        string `json:"task_id"`
	WorkspaceRoot string `json:"workspace_root,omitempty"`
	// ApprovalWaitMS is consumed by approval-aware task workflows.
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
	return driveTaskTurns(ctx, start, ActivityRunTurn)
}

// taskWorkflowV2 wraps the V1 turn drive with a durable approval wait:
// a parked task holds on WaitForExternalEvent instead of ending, and an
// approved decision unparks the task and drives a fresh generation.
func taskWorkflowV2(ctx *workflow.WorkflowContext) (any, error) {
	return taskWorkflowWithApproval(ctx, ActivityRunTurn)
}

// taskWorkflowV3 preserves durable approval behavior while scheduling the
// workspace-bound turn activity introduced with GoalWorkflowV4.
func taskWorkflowV3(ctx *workflow.WorkflowContext) (any, error) {
	return taskWorkflowWithApproval(ctx, ActivityRunTurnV2)
}

// taskWorkflowV4 keeps retryable blocked work inside the child workflow:
// waiting is durable, wake is idempotent, and the next turn is seeded from
// the checkpoint version created by the wake activity.
func taskWorkflowV4(ctx *workflow.WorkflowContext) (any, error) {
	return taskWorkflowWithApprovalV4(ctx)
}

func taskWorkflowWithApproval(ctx *workflow.WorkflowContext, turnActivity string) (any, error) {
	var start taskStart
	if err := ctx.GetInput(&start); err != nil {
		return nil, fmt.Errorf("task workflow input: %w", err)
	}
	total := durability.TaskOutcome{TaskID: start.TaskID, Status: "in_progress"}
	for {
		round, err := driveTaskTurns(ctx, start, turnActivity)
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

// taskWorkflowWithApprovalV4 carries the V3 approval contract forward while
// using the retry-aware V5 turn drive. The older helper above remains frozen
// for in-flight V2/V3 histories.
func taskWorkflowWithApprovalV4(ctx *workflow.WorkflowContext) (any, error) {
	var start taskStart
	if err := ctx.GetInput(&start); err != nil {
		return nil, fmt.Errorf("task workflow input: %w", err)
	}
	total := durability.TaskOutcome{TaskID: start.TaskID, Status: "in_progress"}
	retryState := taskRetryState{}
	for {
		round, err := driveTaskTurnsV4(ctx, start, ActivityRunTurnV3, &retryState)
		total.Turns += round.Turns
		total.SpentUSD += round.SpentUSD
		total.Status = round.Status
		total.RetryExhausted = total.RetryExhausted || round.RetryExhausted
		total.GenerationDeferred = round.GenerationDeferred
		if round.Decision != "" {
			total.Decision = round.Decision
		}
		if err != nil {
			return total, err
		}
		if round.GenerationDeferred {
			return total, nil
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
	}
}

type taskRetryState struct {
	WaitOrdinal int
	WaitCount   int
}

// driveTaskTurns is the shared turn drive: seed once, then one
// TurnActivity per turn until the task ends or yields.
func driveTaskTurns(ctx *workflow.WorkflowContext, start taskStart, turnActivity string) (durability.TaskOutcome, error) {
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
		if err := ctx.CallActivity(turnActivity,
			workflow.WithActivityInput(durability.TurnRequest{
				RunID:              start.RunID,
				TaskID:             start.TaskID,
				WorkspaceRoot:      start.WorkspaceRoot,
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

// driveTaskTurnsV4 is the immutable V5 task drive. A future retry deadline
// becomes one ledger-backed wait, one durable timer, one idempotent wake, and
// one resolved audit fact before a fresh checkpoint seed starts the next turn.
func driveTaskTurnsV4(ctx *workflow.WorkflowContext, start taskStart, turnActivity string, retryState *taskRetryState) (durability.TaskOutcome, error) {
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
	if retryState == nil {
		retryState = &taskRetryState{}
	}

	for {
		var turn durability.TurnResponse
		if err := ctx.CallActivity(turnActivity,
			workflow.WithActivityInput(durability.TurnRequest{
				RunID:              start.RunID,
				TaskID:             start.TaskID,
				WorkspaceRoot:      start.WorkspaceRoot,
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

		kind := goalloop.StepKind(turn.Kind)
		if kind == goalloop.StepBlocked && turn.RetryAfterUnixMS > 0 && retryWaitIdentityValid(turn) {
			waitOrdinal, reserved := reserveRetryWait(retryState)
			if !reserved {
				outcome.Status = turn.Status
				outcome.RetryExhausted = true
				return outcome, nil
			}
			turn.WaitID = retryWaitID(
				ctx.ID(), start.TaskID, waitOrdinal, turn.ExpectedCheckpointID,
				turn.ExpectedCheckpointVersion, generation, turn.BlockerDigest,
			)
			wait := durability.RetryWait{
				RunID:                     start.RunID,
				TaskID:                    start.TaskID,
				WorkflowInstanceID:        ctx.ID(),
				WaitID:                    turn.WaitID,
				Category:                  turn.BlockerCategory,
				ReasonCode:                turn.BlockerReasonCode,
				RetryAfterUnixMS:          turn.RetryAfterUnixMS,
				Ordinal:                   waitOrdinal,
				ExpectedCheckpointID:      turn.ExpectedCheckpointID,
				ExpectedCheckpointVersion: turn.ExpectedCheckpointVersion,
				BlockerDigest:             turn.BlockerDigest,
			}
			if err := ctx.CallActivity(ActivityRecordRetryWaiting,
				workflow.WithActivityInput(wait),
				workflow.WithActivityRetryPolicy(retryWaitPolicy),
			).Await(nil); err != nil {
				return outcome, fmt.Errorf("record retry wait %s: %w", wait.WaitID, err)
			}

			delay, _ := boundedRetryDelay(ctx.CurrentTimeUTC(), wait.RetryAfterUnixMS)
			if err := ctx.CreateTimer(delay, workflow.WithTimerName(wait.WaitID)).Await(nil); err != nil {
				if errors.Is(err, task.ErrTaskCanceled) {
					outcome.Status = turn.Status
					return outcome, nil
				}
				return outcome, fmt.Errorf("retry timer %s: %w", wait.WaitID, err)
			}
			var wakeResult durability.RetryWakeResult
			if err := ctx.CallActivity(ActivityWakeRetryV2,
				workflow.WithActivityInput(wait),
				workflow.WithActivityRetryPolicy(retryWaitPolicy),
			).Await(&wakeResult); err != nil {
				return outcome, fmt.Errorf("wake retry %s: %w", wait.WaitID, err)
			}
			switch wakeResult.Disposition {
			case durability.RetryWakeApplied, durability.RetryWakeAlreadyApplied:
				// Only an exact transition, including its ack-loss replay, may
				// resolve the wait and schedule another turn.
			case durability.RetryWakeStale:
				outcome.Status = wakeResult.TaskStatus
				if outcome.Status == "" {
					outcome.Status = turn.Status
				}
				outcome.GenerationDeferred = outcome.Status != "completed"
				return outcome, nil
			default:
				return outcome, fmt.Errorf("wake retry %s returned unknown disposition %q", wait.WaitID, wakeResult.Disposition)
			}
			if err := ctx.CallActivity(ActivityResolveRetry,
				workflow.WithActivityInput(wait),
				workflow.WithActivityRetryPolicy(retryWaitPolicy),
			).Await(nil); err != nil {
				return outcome, fmt.Errorf("resolve retry %s: %w", wait.WaitID, err)
			}

			if err := ctx.CallActivity(ActivityResumeSeed,
				workflow.WithActivityInput(taskStart{RunID: start.RunID, TaskID: start.TaskID}),
			).Await(&seed); err != nil {
				return outcome, fmt.Errorf("resume seed after retry %s: %w", wait.WaitID, err)
			}
			generation = seed.Generation
			turnIndex = 0
			drive = seed.Drive
			modelRequests, toolExecutions = 0, 0
			driveStarted = ctx.CurrentTimeUTC()
			continue
		}

		generation, turnIndex = nextTurnIdentity(kind, generation, turnIndex)
		if turnDone(kind) {
			outcome.Status = turn.Status
			return outcome, nil
		}
	}
}

// retryWaitID is stable across replay, compact, and binds the monotonic wait
// ordinal to the exact checkpoint/generation/blocker digest without exposing
// any raw blocker content.
func retryWaitID(workflowInstanceID, taskID string, ordinal int, checkpointID string, checkpointVersion, generation int, blockerDigest string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		workflowInstanceID,
		taskID,
		fmt.Sprintf("%d", ordinal),
		checkpointID,
		fmt.Sprintf("%d", checkpointVersion),
		fmt.Sprintf("%d", generation),
		blockerDigest,
	}, "\x00")))
	return fmt.Sprintf("retry-%d-%s", ordinal, hex.EncodeToString(sum[:12]))
}

func retryWaitIdentityValid(turn durability.TurnResponse) bool {
	return validCheckpointIdentity(turn.ExpectedCheckpointID, turn.ExpectedCheckpointVersion) &&
		validBlockerDigest(turn.BlockerDigest)
}

func reserveRetryWait(state *taskRetryState) (int, bool) {
	if state == nil || state.WaitCount >= defaultMaxRetryWaits {
		return 0, false
	}
	state.WaitCount++
	state.WaitOrdinal++
	return state.WaitOrdinal, true
}

func boundedRetryDelay(now time.Time, retryAfterUnixMS int64) (time.Duration, bool) {
	if retryAfterUnixMS <= 0 {
		return 0, false
	}
	delay := time.UnixMilli(retryAfterUnixMS).UTC().Sub(now.UTC())
	if delay < minimumRetryDelay {
		delay = minimumRetryDelay
	}
	if delay > maximumRetryDelay {
		delay = maximumRetryDelay
	}
	return delay, true
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

// nextBatchActivityV2 is registered separately so GoalWorkflowV1-V4 retain
// the exact next_batch.v1 activity implementation and terminal semantics.
func nextBatchActivityV2(runner durability.TaskRunner) workflow.Activity {
	return func(ctx workflow.ActivityContext) (any, error) {
		var req durability.NextBatchV2Request
		if err := ctx.GetInput(&req); err != nil {
			return nil, err
		}
		versioned, ok := runner.(durability.NextBatchV2Runner)
		if !ok {
			return nil, fmt.Errorf("dapr: task runner does not support next batch v2")
		}
		return versioned.NextBatchV2(ctx.Context(), req)
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

func recordRetryWaitingActivity(runner durability.TaskRunner) workflow.Activity {
	return func(ctx workflow.ActivityContext) (any, error) {
		var wait durability.RetryWait
		if err := ctx.GetInput(&wait); err != nil {
			return nil, err
		}
		waiter, ok := runner.(durability.RetryWaiter)
		if !ok {
			return nil, fmt.Errorf("dapr: task runner does not support durable retry waits")
		}
		return nil, waiter.RecordRetryWaiting(ctx.Context(), wait)
	}
}

func wakeRetryActivity(runner durability.TaskRunner) workflow.Activity {
	return func(ctx workflow.ActivityContext) (any, error) {
		var wait durability.RetryWait
		if err := ctx.GetInput(&wait); err != nil {
			return nil, err
		}
		waiter, ok := runner.(durability.RetryWaiter)
		if !ok {
			return nil, fmt.Errorf("dapr: task runner does not support durable retry wakes")
		}
		return nil, waiter.WakeRetry(ctx.Context(), wait)
	}
}

func wakeRetryActivityV2(runner durability.TaskRunner) workflow.Activity {
	return func(ctx workflow.ActivityContext) (any, error) {
		var wait durability.RetryWait
		if err := ctx.GetInput(&wait); err != nil {
			return nil, err
		}
		resolver, ok := runner.(durability.RetryWakeResolver)
		if !ok {
			return nil, fmt.Errorf("dapr: task runner does not support durable retry wake dispositions")
		}
		result, err := resolver.WakeRetryV2(ctx.Context(), wait)
		if err != nil {
			return nil, err
		}
		return sanitizeRetryWakeResult(result)
	}
}

func sanitizeRetryWakeResult(result durability.RetryWakeResult) (durability.RetryWakeResult, error) {
	switch result.Disposition {
	case durability.RetryWakeApplied, durability.RetryWakeAlreadyApplied:
		result.TaskStatus = "in_progress"
	case durability.RetryWakeStale:
		switch result.TaskStatus {
		case "pending", "in_progress", "blocked", "parked", "completed":
		default:
			result.TaskStatus = "blocked"
		}
	default:
		return durability.RetryWakeResult{}, fmt.Errorf("dapr: retry wake returned an invalid disposition")
	}
	return result, nil
}

func resolveRetryActivity(runner durability.TaskRunner) workflow.Activity {
	return func(ctx workflow.ActivityContext) (any, error) {
		var wait durability.RetryWait
		if err := ctx.GetInput(&wait); err != nil {
			return nil, err
		}
		waiter, ok := runner.(durability.RetryWaiter)
		if !ok {
			return nil, fmt.Errorf("dapr: task runner does not support durable retry resolution")
		}
		return nil, waiter.ResolveRetry(ctx.Context(), wait)
	}
}

// finalizeGoalActivity reconciles workflow completion with the canonical run
// lifecycle before Dapr records the workflow as terminal.
func finalizeGoalActivity(runner durability.TaskRunner) workflow.Activity {
	return func(ctx workflow.ActivityContext) (any, error) {
		var finalization durability.GoalFinalization
		if err := ctx.GetInput(&finalization); err != nil {
			return nil, err
		}
		return nil, finalizeGoal(ctx.Context(), runner, finalization)
	}
}

func finalizeGoal(ctx context.Context, runner durability.TaskRunner, finalization durability.GoalFinalization) error {
	finalizer, ok := runner.(durability.GoalFinalizer)
	if !ok {
		return fmt.Errorf("dapr: task runner does not support V4 goal finalization")
	}
	return finalizer.FinalizeGoal(ctx, finalization)
}

// runTurnActivity is the legacy V1 adapter kept for in-flight workflow
// histories whose serialized input predates workspace identity.
func runTurnActivity(runner durability.TaskRunner) workflow.Activity {
	return turnActivity(selectTurnActivity(runner, true))
}

// runTurnActivityV2 enforces the serialized run/workspace identity before a
// new workflow can execute a turn.
func runTurnActivityV2(runner durability.TaskRunner) workflow.Activity {
	return turnActivity(selectTurnActivity(runner, false))
}

// runTurnActivityV3 is used only by TaskWorkflowV4. It requires the
// receipt-backed runner capability and normalizes every retry field before
// the response can enter workflow history.
func runTurnActivityV3(runner durability.TaskRunner) workflow.Activity {
	run, err := selectTurnActivityV3(runner)
	if err != nil {
		return func(workflow.ActivityContext) (any, error) {
			return nil, err
		}
	}
	return turnActivity(func(ctx context.Context, req durability.TurnRequest) (durability.TurnResponse, error) {
		resp, err := run(ctx, req)
		if err != nil {
			return durability.TurnResponse{}, err
		}
		return sanitizeTurnResponse(req, resp), nil
	})
}

func selectTurnActivityV3(runner durability.TaskRunner) (turnActivityFunc, error) {
	durableRunner, ok := runner.(durability.DurableTurnRunner)
	if !ok {
		return nil, fmt.Errorf("dapr: task runner does not support V3 durable turn receipts")
	}
	return durableRunner.RunTurnV3, nil
}

func sanitizeTurnResponse(req durability.TurnRequest, resp durability.TurnResponse) durability.TurnResponse {
	resp.WaitID = ""
	if resp.Kind != string(goalloop.StepBlocked) {
		resp.BlockerCategory = ""
		resp.BlockerReasonCode = ""
		resp.RetryAfterUnixMS = 0
		resp.RetryOrdinal = 0
		resp.ExpectedCheckpointID = ""
		resp.ExpectedCheckpointVersion = 0
		resp.BlockerDigest = ""
		return resp
	}
	resp.BlockerCategory, resp.BlockerReasonCode = normalizeRetryCodes(resp.BlockerCategory, resp.BlockerReasonCode)
	resp.RetryOrdinal = req.TurnIndex + 1
	if resp.RetryAfterUnixMS <= 0 ||
		resp.ExpectedCheckpointVersion != req.Generation+1 ||
		!validCheckpointIdentity(resp.ExpectedCheckpointID, resp.ExpectedCheckpointVersion) ||
		!validBlockerDigest(resp.BlockerDigest) {
		resp.RetryAfterUnixMS = 0
		resp.RetryOrdinal = 0
		resp.ExpectedCheckpointID = ""
		resp.ExpectedCheckpointVersion = 0
		resp.BlockerDigest = ""
	}
	return resp
}

func normalizeRetryCodes(category, reasonCode string) (string, string) {
	switch category + "/" + reasonCode {
	case "provider/retryable_capacity", "governance/authorization_required", "dependency/external_dependency", "execution/blocked":
		return category, reasonCode
	default:
		return "execution", "blocked"
	}
}

func validCheckpointIdentity(checkpointID string, checkpointVersion int) bool {
	if checkpointVersion <= 0 || len(checkpointID) != 29 || !strings.HasPrefix(checkpointID, "cp_") {
		return false
	}
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for _, r := range checkpointID[len("cp_"):] {
		if !strings.ContainsRune(alphabet, r) {
			return false
		}
	}
	return true
}

func validBlockerDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

type turnActivityFunc func(context.Context, durability.TurnRequest) (durability.TurnResponse, error)

func selectTurnActivity(runner durability.TaskRunner, legacy bool) turnActivityFunc {
	if legacy {
		if legacyRunner, ok := runner.(durability.LegacyTaskRunner); ok {
			return legacyRunner.RunLegacyTurn
		}
	}
	return runner.RunTurn
}

func turnActivity(run turnActivityFunc) workflow.Activity {
	return func(ctx workflow.ActivityContext) (any, error) {
		var req durability.TurnRequest
		if err := ctx.GetInput(&req); err != nil {
			return nil, err
		}
		return run(ctx.Context(), req)
	}
}
