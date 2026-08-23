// Package goalrunner adapts a goalloop.Loop to the durability.TaskRunner
// port: it resolves task specs from the goal intake, converts drive
// snapshots to and from the opaque wire form, and delegates every turn
// to Loop.TurnStep so local and durable execution share one
// implementation.
package goalrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/durability"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/runledger"
)

// Runner hosts one goal's activities.
type Runner struct {
	loop          *goalloop.Loop
	runID         string
	workspaceRoot string
	legacyRoot    bool
	goal          goalloop.Goal
	specs         map[string]goalloop.TaskSpec
}

// New wires a Runner for exactly one loaded goal and worker workspace. A
// missing or mismatched identity fails closed before the worker can register
// with Dapr and receive activities.
func New(loop *goalloop.Loop, runID, workerRoot string, goal goalloop.Goal, specs map[string]goalloop.TaskSpec) (*Runner, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, fmt.Errorf("goalrunner: run ID is required")
	}
	if err := goal.Validate(); err != nil {
		return nil, fmt.Errorf("goalrunner: invalid goal for run %s: %w", runID, err)
	}
	actualRoot, err := goalloop.NormalizeWorkspaceRoot(workerRoot)
	if err != nil {
		return nil, fmt.Errorf("goalrunner: resolve worker workspace for run %s: %w", runID, err)
	}
	if strings.TrimSpace(goal.WorkspaceRoot) == "" {
		return &Runner{loop: loop, runID: runID, workspaceRoot: actualRoot, legacyRoot: true, goal: goal, specs: specs}, nil
	}
	expectedRoot, err := goalloop.NormalizeWorkspaceRoot(goal.WorkspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("goalrunner: load workspace identity for run %s: %w", runID, err)
	}
	if actualRoot != expectedRoot {
		return nil, fmt.Errorf("goalrunner: workspace mismatch for run %s: worker is %s, goal requires %s", runID, actualRoot, expectedRoot)
	}
	return &Runner{loop: loop, runID: runID, workspaceRoot: expectedRoot, goal: goal, specs: specs}, nil
}

// WorkspaceRoot returns the normalized worker root bound to this runner. For
// a legacy goal with no serialized root, this is the narrow trust boundary
// chosen by the worker operator when starting or resuming that goal.
func (r *Runner) WorkspaceRoot() string {
	if r == nil {
		return ""
	}
	return r.workspaceRoot
}

func (r *Runner) validateRun(runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return fmt.Errorf("goalrunner: request carries no run ID")
	}
	if runID != r.runID {
		return fmt.Errorf("goalrunner: runner for run %s rejected foreign run %s", r.runID, runID)
	}
	return nil
}

func (r *Runner) validateWorkspace(root string) error {
	requestRoot, err := goalloop.NormalizeWorkspaceRoot(root)
	if err != nil {
		return fmt.Errorf("goalrunner: request for run %s has no valid workspace identity: %w", r.runID, err)
	}
	if requestRoot != r.workspaceRoot {
		return fmt.Errorf("goalrunner: request workspace mismatch for run %s: got %s, want %s", r.runID, requestRoot, r.workspaceRoot)
	}
	return nil
}

// ResumeSeed implements durability.TaskRunner.
func (r *Runner) ResumeSeed(ctx context.Context, runID, taskID string) (durability.ResumeSeed, error) {
	if err := r.validateRun(runID); err != nil {
		return durability.ResumeSeed{}, err
	}
	seed, err := r.loop.SeedTask(ctx, taskID, r.specs[taskID])
	if err != nil {
		return durability.ResumeSeed{}, err
	}
	drive, err := json.Marshal(seed.Drive)
	if err != nil {
		return durability.ResumeSeed{}, fmt.Errorf("goalrunner: marshal drive snapshot: %w", err)
	}
	return durability.ResumeSeed{Generation: seed.Generation, Drive: drive}, nil
}

// NextTask implements durability.TaskRunner with the Drain selection
// rule: first queue item that the workflow has not deferred.
func (r *Runner) NextTask(ctx context.Context, req durability.NextTaskRequest) (durability.NextTaskResponse, error) {
	if err := r.validateRun(req.RunID); err != nil {
		return durability.NextTaskResponse{}, err
	}
	queue, err := r.loop.BuildQueue(ctx, req.RunID)
	if err != nil {
		return durability.NextTaskResponse{}, err
	}
	deferred := make(map[string]bool, len(req.Deferred))
	for _, taskID := range req.Deferred {
		deferred[taskID] = true
	}
	for _, item := range queue {
		if !deferred[item.TaskID] {
			return durability.NextTaskResponse{TaskID: item.TaskID}, nil
		}
	}
	return durability.NextTaskResponse{Done: true}, nil
}

// NextBatch implements durability.TaskRunner: the next runnable tasks
// whose workspace claims are mutually independent, in queue order.
func (r *Runner) NextBatch(ctx context.Context, req durability.NextBatchRequest) (durability.NextBatchResponse, error) {
	if err := r.validateRun(req.RunID); err != nil {
		return durability.NextBatchResponse{}, err
	}
	queue, err := r.loop.BuildQueue(ctx, req.RunID)
	if err != nil {
		return durability.NextBatchResponse{}, err
	}
	deferred := make(map[string]bool, len(req.Deferred))
	for _, taskID := range req.Deferred {
		deferred[taskID] = true
	}
	candidates := make([]durability.TaskClaim, 0, len(queue))
	for _, item := range queue {
		if deferred[item.TaskID] {
			continue
		}
		candidates = append(candidates, durability.TaskClaim{TaskID: item.TaskID, Claims: r.specs[item.TaskID].Claims})
	}
	batch := partitionIndependent(candidates, req.MaxParallel)
	if len(batch) == 0 {
		return durability.NextBatchResponse{Done: true}, nil
	}
	return durability.NextBatchResponse{Tasks: batch}, nil
}

// NextBatchV2 is the GoalWorkflowV5 terminal-aware scheduler. It keeps
// NextBatch's legacy empty-pull behavior immutable while making deferred,
// exhausted, blocked, and parked task IDs explicit to the new workflow.
func (r *Runner) NextBatchV2(ctx context.Context, req durability.NextBatchV2Request) (durability.NextBatchResponse, error) {
	if err := r.validateRun(req.RunID); err != nil {
		return durability.NextBatchResponse{}, err
	}
	queue, err := r.loop.BuildQueue(ctx, req.RunID)
	if err != nil {
		return durability.NextBatchResponse{}, err
	}
	excluded := make(map[string]bool, len(req.Deferred)+len(req.ExcludedTaskIDs))
	for _, taskID := range req.Deferred {
		excluded[taskID] = true
	}
	for _, taskID := range req.ExcludedTaskIDs {
		excluded[taskID] = true
	}
	candidates := make([]durability.TaskClaim, 0, len(queue))
	for _, item := range queue {
		if excluded[item.TaskID] {
			continue
		}
		candidates = append(candidates, durability.TaskClaim{TaskID: item.TaskID, Claims: r.specs[item.TaskID].Claims})
	}
	batch := partitionIndependent(candidates, req.MaxParallel)
	if len(batch) == 0 {
		incomplete, err := r.incompleteTaskIDsV2(ctx, req)
		if err != nil {
			return durability.NextBatchResponse{}, err
		}
		return durability.NextBatchResponse{Done: true, IncompleteTaskIDs: incomplete}, nil
	}
	return durability.NextBatchResponse{Tasks: batch}, nil
}

// incompleteTaskIDs reports resumable work that BuildQueue intentionally
// excludes (blocked/parked tasks and bounded-yield IDs). It is only consulted
// at a terminal pull, keeping ordinary batch scheduling cheap.
func (r *Runner) incompleteTaskIDsV2(ctx context.Context, req durability.NextBatchV2Request) ([]string, error) {
	seen := make(map[string]struct{}, len(req.Deferred)+len(req.ExcludedTaskIDs))
	for _, taskID := range req.Deferred {
		if strings.TrimSpace(taskID) != "" {
			seen[taskID] = struct{}{}
		}
	}
	for _, taskID := range req.ExcludedTaskIDs {
		if strings.TrimSpace(taskID) != "" {
			seen[taskID] = struct{}{}
		}
	}
	report, err := r.loop.Report(ctx, req.RunID)
	if err != nil {
		return nil, fmt.Errorf("goalrunner: inspect incomplete tasks: %w", err)
	}
	for _, parked := range report.Parked {
		if parked.TaskID != "" {
			seen[parked.TaskID] = struct{}{}
		}
	}
	for _, action := range report.NextActions {
		if action.TaskID != "" {
			seen[action.TaskID] = struct{}{}
		}
	}
	incomplete := make([]string, 0, len(seen))
	for taskID := range seen {
		incomplete = append(incomplete, taskID)
	}
	sort.Strings(incomplete)
	return incomplete, nil
}

// partitionIndependent greedily takes tasks in queue order whose claims
// do not overlap with claims already taken, bounded by maxParallel. A
// task without claims implicitly claims the whole workspace: it only
// ever runs alone, and only from the front of the queue.
func partitionIndependent(candidates []durability.TaskClaim, maxParallel int) []durability.TaskClaim {
	if maxParallel <= 0 {
		maxParallel = 1
	}
	var batch []durability.TaskClaim
	var taken []string
	for _, candidate := range candidates {
		if len(batch) >= maxParallel {
			break
		}
		if len(candidate.Claims) == 0 {
			// The whole-workspace claim: alone, and never behind a
			// batched task it could conflict with.
			if len(batch) == 0 {
				return []durability.TaskClaim{candidate}
			}
			break
		}
		if claimsOverlap(taken, candidate.Claims) {
			// Queue order is priority order: do not let a later task
			// jump a conflicting earlier one.
			break
		}
		batch = append(batch, candidate)
		taken = append(taken, candidate.Claims...)
	}
	return batch
}

// claimsOverlap reports whether any candidate path and any taken path
// nest inside each other after cleaning.
func claimsOverlap(taken, candidate []string) bool {
	for _, have := range taken {
		for _, want := range candidate {
			if pathsNest(have, want) {
				return true
			}
		}
	}
	return false
}

func pathsNest(a, b string) bool {
	a = path.Clean(strings.TrimPrefix(a, "./"))
	b = path.Clean(strings.TrimPrefix(b, "./"))
	if a == b {
		return true
	}
	return strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

// RecordApprovalWait implements durability.TaskRunner: the wait lands
// on the run ledger before the workflow blocks, so `buckley goal
// approve` can find the child instance to target.
func (r *Runner) RecordApprovalWait(ctx context.Context, wait durability.ApprovalWait) error {
	if err := r.validateRun(wait.RunID); err != nil {
		return err
	}
	_, err := r.loop.Ledger().Append(ctx, runledger.Event{
		Type:      runledger.EventDurableApprovalWaiting,
		Timestamp: time.Now().UTC(),
		RunID:     wait.RunID,
		TaskID:    wait.TaskID,
		Payload: map[string]any{
			"workflow_instance_id": wait.WorkflowInstanceID,
			"reason":               wait.Reason,
		},
	})
	if err != nil {
		return fmt.Errorf("goalrunner: record approval wait: %w", err)
	}
	return nil
}

// ResolveApproval implements durability.TaskRunner. Approved
// resolutions unpark the task before the resolution is recorded, so a
// crash between the two retries the whole activity idempotently.
func (r *Runner) ResolveApproval(ctx context.Context, resolution durability.ApprovalResolution) error {
	if err := r.validateRun(resolution.RunID); err != nil {
		return err
	}
	if resolution.Outcome == durability.ApprovalApproved {
		if err := r.loop.Unpark(ctx, resolution.RunID, resolution.TaskID, resolution.Reason); err != nil {
			return err
		}
	}
	_, err := r.loop.Ledger().Append(ctx, runledger.Event{
		Type:      runledger.EventDurableApprovalResolved,
		Timestamp: time.Now().UTC(),
		RunID:     resolution.RunID,
		TaskID:    resolution.TaskID,
		Payload: map[string]any{
			"workflow_instance_id": resolution.WorkflowInstanceID,
			"outcome":              resolution.Outcome,
			"reason":               resolution.Reason,
		},
	})
	if err != nil {
		return fmt.Errorf("goalrunner: record approval resolution: %w", err)
	}
	return nil
}

// RecordRetryWaiting implements durability.RetryWaiter. The event payload is
// deliberately limited to stable IDs, policy labels, and timer coordinates;
// the blocker explanation remains in the task checkpoint.
func (r *Runner) RecordRetryWaiting(ctx context.Context, wait durability.RetryWait) error {
	var err error
	wait, err = r.canonicalRetryWait(wait)
	if err != nil {
		return err
	}
	_, err = r.loop.Ledger().Append(ctx, runledger.Event{
		ID:        runledger.StableEventID(runledger.EventDurableRetryWaiting, wait.RunID, wait.TaskID, wait.WorkflowInstanceID, wait.WaitID),
		Type:      runledger.EventDurableRetryWaiting,
		Timestamp: time.Now().UTC(),
		RunID:     wait.RunID,
		TaskID:    wait.TaskID,
		Payload: map[string]any{
			"workflow_instance_id":        wait.WorkflowInstanceID,
			"wait_id":                     wait.WaitID,
			"category":                    wait.Category,
			"reason_code":                 wait.ReasonCode,
			"retry_after_unix_ms":         wait.RetryAfterUnixMS,
			"ordinal":                     wait.Ordinal,
			"expected_checkpoint_id":      wait.ExpectedCheckpointID,
			"expected_checkpoint_version": wait.ExpectedCheckpointVersion,
			"blocker_digest":              wait.BlockerDigest,
		},
	})
	if err != nil {
		return fmt.Errorf("goalrunner: record retry wait %s: %w", wait.WaitID, err)
	}
	return nil
}

// WakeRetry implements durability.RetryWaiter. Unpark is intentionally
// idempotent: Dapr may redeliver this activity after the checkpoint save but
// before the activity acknowledgement.
func (r *Runner) WakeRetry(ctx context.Context, wait durability.RetryWait) error {
	_, err := r.WakeRetryV2(ctx, wait)
	return err
}

// WakeRetryV2 returns the checkpoint CAS disposition to TaskWorkflowV4 so a
// stale timer can never emit a resolved event or schedule another turn.
func (r *Runner) WakeRetryV2(ctx context.Context, wait durability.RetryWait) (durability.RetryWakeResult, error) {
	var err error
	wait, err = r.canonicalRetryWait(wait)
	if err != nil {
		return durability.RetryWakeResult{}, err
	}
	wakeResult, err := r.loop.UnparkRetry(ctx, wait.RunID, wait.TaskID, goalloop.RetryWake{
		WaitID:                    wait.WaitID,
		ExpectedCheckpointID:      wait.ExpectedCheckpointID,
		ExpectedCheckpointVersion: wait.ExpectedCheckpointVersion,
		BlockerDigest:             wait.BlockerDigest,
		Category:                  wait.Category,
		ReasonCode:                wait.ReasonCode,
	})
	if err != nil {
		return durability.RetryWakeResult{}, fmt.Errorf("goalrunner: wake retry %s: %w", wait.WaitID, err)
	}
	return durability.RetryWakeResult{Disposition: string(wakeResult.Disposition), TaskStatus: wakeResult.TaskStatus}, nil
}

// ResolveRetry implements durability.RetryWaiter. Stable event identity
// makes a redelivered resolution a no-op in the run ledger.
func (r *Runner) ResolveRetry(ctx context.Context, wait durability.RetryWait) error {
	var err error
	wait, err = r.canonicalRetryWait(wait)
	if err != nil {
		return err
	}
	_, err = r.loop.Ledger().Append(ctx, runledger.Event{
		ID:        runledger.StableEventID(runledger.EventDurableRetryResolved, wait.RunID, wait.TaskID, wait.WorkflowInstanceID, wait.WaitID),
		Type:      runledger.EventDurableRetryResolved,
		Timestamp: time.Now().UTC(),
		RunID:     wait.RunID,
		TaskID:    wait.TaskID,
		Payload: map[string]any{
			"workflow_instance_id":        wait.WorkflowInstanceID,
			"wait_id":                     wait.WaitID,
			"category":                    wait.Category,
			"reason_code":                 wait.ReasonCode,
			"retry_after_unix_ms":         wait.RetryAfterUnixMS,
			"ordinal":                     wait.Ordinal,
			"expected_checkpoint_id":      wait.ExpectedCheckpointID,
			"expected_checkpoint_version": wait.ExpectedCheckpointVersion,
			"blocker_digest":              wait.BlockerDigest,
		},
	})
	if err != nil {
		return fmt.Errorf("goalrunner: resolve retry %s: %w", wait.WaitID, err)
	}
	return nil
}

func (r *Runner) canonicalRetryWait(wait durability.RetryWait) (durability.RetryWait, error) {
	if err := r.validateRun(wait.RunID); err != nil {
		return durability.RetryWait{}, err
	}
	if strings.TrimSpace(wait.TaskID) == "" {
		return durability.RetryWait{}, fmt.Errorf("goalrunner: retry wait task ID is required")
	}
	if strings.TrimSpace(wait.WorkflowInstanceID) == "" {
		return durability.RetryWait{}, fmt.Errorf("goalrunner: retry wait workflow instance ID is required")
	}
	if !validRetryWaitID(wait.WaitID) {
		return durability.RetryWait{}, fmt.Errorf("goalrunner: retry wait ID is invalid")
	}
	if wait.Ordinal <= 0 {
		return durability.RetryWait{}, fmt.Errorf("goalrunner: retry wait %s has invalid ordinal %d", wait.WaitID, wait.Ordinal)
	}
	if wait.RetryAfterUnixMS <= 0 {
		return durability.RetryWait{}, fmt.Errorf("goalrunner: retry wait %s has invalid deadline", wait.WaitID)
	}
	if !validCheckpointID(wait.ExpectedCheckpointID) || wait.ExpectedCheckpointVersion <= 0 {
		return durability.RetryWait{}, fmt.Errorf("goalrunner: retry wait %s has invalid checkpoint identity", wait.WaitID)
	}
	if !validLowerHexDigest(wait.BlockerDigest) {
		return durability.RetryWait{}, fmt.Errorf("goalrunner: retry wait %s has invalid blocker digest", wait.WaitID)
	}
	wait.Category, wait.ReasonCode = canonicalRetryCodes(wait.Category, wait.ReasonCode)
	return wait, nil
}

func canonicalRetryCodes(category, reasonCode string) (string, string) {
	switch category + "/" + reasonCode {
	case "provider/retryable_capacity", "governance/authorization_required", "dependency/external_dependency", "execution/blocked":
		return category, reasonCode
	default:
		return "execution", "blocked"
	}
}

func validRetryWaitID(value string) bool {
	if len(value) < len("retry-1-")+16 || len(value) > 80 || !strings.HasPrefix(value, "retry-") {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func validCheckpointID(value string) bool {
	if len(value) != 29 || !strings.HasPrefix(value, "cp_") {
		return false
	}
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	for _, r := range value[len("cp_"):] {
		if !strings.ContainsRune(alphabet, r) {
			return false
		}
	}
	return true
}

func validLowerHexDigest(value string) bool {
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

// RunTurn implements the workspace-bound V2 activity over Loop.TurnStep.
func (r *Runner) RunTurn(ctx context.Context, req durability.TurnRequest) (durability.TurnResponse, error) {
	if err := r.validateRun(req.RunID); err != nil {
		return durability.TurnResponse{}, err
	}
	if err := r.validateWorkspace(req.WorkspaceRoot); err != nil {
		return durability.TurnResponse{}, err
	}
	return r.runTurn(ctx, req, false)
}

// RunTurnV3 implements the receipt-backed activity without changing the V2
// adapter used by in-flight workflows.
func (r *Runner) RunTurnV3(ctx context.Context, req durability.TurnRequest) (durability.TurnResponse, error) {
	if err := r.validateRun(req.RunID); err != nil {
		return durability.TurnResponse{}, err
	}
	if err := r.validateWorkspace(req.WorkspaceRoot); err != nil {
		return durability.TurnResponse{}, err
	}
	return r.runTurn(ctx, req, true)
}

// RunLegacyTurn serves only in-flight V1/V2 task histories whose serialized
// requests have no workspace field. The run identity remains mandatory; the
// workspace is the one bound when this Runner was constructed.
func (r *Runner) RunLegacyTurn(ctx context.Context, req durability.TurnRequest) (durability.TurnResponse, error) {
	if err := r.validateRun(req.RunID); err != nil {
		return durability.TurnResponse{}, err
	}
	if strings.TrimSpace(req.WorkspaceRoot) != "" {
		if err := r.validateWorkspace(req.WorkspaceRoot); err != nil {
			return durability.TurnResponse{}, err
		}
	}
	return r.runTurn(ctx, req, false)
}

func (r *Runner) runTurn(ctx context.Context, req durability.TurnRequest, receiptBacked bool) (durability.TurnResponse, error) {
	var drive goalloop.DriveSnapshot
	if len(req.Drive) > 0 {
		if err := json.Unmarshal(req.Drive, &drive); err != nil {
			return durability.TurnResponse{}, fmt.Errorf("goalrunner: decode drive snapshot: %w", err)
		}
	}
	stepReq := goalloop.TurnStepRequest{
		RunID:      req.RunID,
		TaskID:     req.TaskID,
		Goal:       r.goal,
		Spec:       r.specs[req.TaskID],
		Generation: req.Generation,
		TurnIndex:  req.TurnIndex,
		Drive:      drive,
		Counters: agentloop.FuseCounters{
			ModelRequests:  req.ModelRequests,
			ToolExecutions: req.ToolExecutions,
			Elapsed:        time.Duration(req.ElapsedMS) * time.Millisecond,
		},
		WorkflowInstanceID: req.WorkflowInstanceID,
		ActivityName:       "run_turn",
	}
	var step goalloop.TurnStepResponse
	var err error
	if receiptBacked {
		stepReq.ActivityName = "run_turn.v3"
		step, err = r.loop.TurnStepV3(ctx, stepReq)
	} else {
		step, err = r.loop.TurnStep(ctx, stepReq)
	}
	if err != nil {
		return durability.TurnResponse{}, err
	}
	next, err := json.Marshal(step.Drive)
	if err != nil {
		return durability.TurnResponse{}, fmt.Errorf("goalrunner: marshal drive snapshot: %w", err)
	}
	return durability.TurnResponse{
		Kind:                      string(step.Kind),
		Decision:                  string(step.Decision),
		Status:                    step.Status,
		Drive:                     next,
		TurnSpentUSD:              step.TurnSpentUSD,
		Rounds:                    step.Rounds,
		ToolCalls:                 step.ToolCalls,
		BlockerCategory:           step.BlockerCategory,
		BlockerReasonCode:         step.BlockerReasonCode,
		RetryAfterUnixMS:          step.RetryAfterUnixMS,
		RetryOrdinal:              step.RetryOrdinal,
		ExpectedCheckpointID:      step.ExpectedCheckpointID,
		ExpectedCheckpointVersion: step.ExpectedCheckpointVersion,
		BlockerDigest:             step.BlockerDigest,
	}, nil
}

// FinalizeGoal implements durability.GoalFinalizer. The report is the canonical
// task roll-up, so copying its status to the run row keeps report and replay
// views consistent. Repeated delivery of the same terminal status is a no-op.
func (r *Runner) FinalizeGoal(ctx context.Context, finalization durability.GoalFinalization) error {
	if err := r.validateRun(finalization.RunID); err != nil {
		return err
	}
	if err := r.validateWorkspace(finalization.WorkspaceRoot); err != nil {
		return err
	}
	failure := strings.TrimSpace(finalization.Failure)
	report, err := r.loop.Report(ctx, r.runID)
	if err != nil {
		return fmt.Errorf("goalrunner: build final report for run %s: %w", r.runID, err)
	}
	if failure == "" && (finalization.Incomplete || report.Status == "pending" || report.Status == "partial") {
		// Pending and partial reports still contain resumable work. V4 marks
		// bounded-yield exits explicitly, while the report guard also protects
		// legacy observer-side reconciliation from sealing the same run.
		return r.recordGoalGeneration(ctx, finalization, false)
	}
	status := report.Status
	if failure != "" {
		status = "failed"
	}
	run, err := r.loop.Ledger().GetRun(ctx, r.runID)
	if err != nil {
		return fmt.Errorf("goalrunner: load run %s for finalization: %w", r.runID, err)
	}
	if run.EndedAt != nil {
		if run.Status != status {
			return fmt.Errorf("goalrunner: run %s already finalized as %s, cannot finalize as %s", r.runID, run.Status, status)
		}
		return r.recordGoalGeneration(ctx, finalization, failure != "")
	}
	outcome := map[string]any{
		"workflow_instance_id": finalization.WorkflowInstanceID,
		"goal_status":          report.Status,
		"spent_usd":            report.SpentUSD,
	}
	if finalization.Failure != "" {
		outcome["failure"] = finalization.Failure
	}
	if err := r.loop.Ledger().EndRun(ctx, r.runID, status, time.Now().UTC(), outcome); err != nil {
		return fmt.Errorf("goalrunner: finalize run %s: %w", r.runID, err)
	}
	return r.recordGoalGeneration(ctx, finalization, failure != "")
}

func (r *Runner) recordGoalGeneration(ctx context.Context, finalization durability.GoalFinalization, failed bool) error {
	instanceID := finalization.WorkflowInstanceID
	if strings.TrimSpace(instanceID) == "" {
		return fmt.Errorf("goalrunner: workflow instance ID is required for run %s finalization", r.runID)
	}
	generation, err := workflowGeneration(r.runID, instanceID)
	if err != nil {
		return err
	}
	_, err = r.loop.Ledger().Append(ctx, runledger.Event{
		ID:        runledger.StableEventID("durable-goal-generation", r.runID, instanceID),
		Type:      runledger.EventDurableGoalGeneration,
		Timestamp: time.Now().UTC(),
		RunID:     r.runID,
		Payload: map[string]any{
			"run_id":               r.runID,
			"workflow_instance_id": instanceID,
			"generation":           generation,
			"incomplete":           finalization.Incomplete,
			"failure":              failed,
		},
	})
	if err != nil {
		return fmt.Errorf("goalrunner: record workflow generation %s: %w", instanceID, err)
	}
	return nil
}

func workflowGeneration(runID, instanceID string) (int, error) {
	root := "goal-" + runID
	if instanceID == root {
		return 0, nil
	}
	prefix := root + "::resume::"
	if !strings.HasPrefix(instanceID, prefix) {
		return 0, fmt.Errorf("goalrunner: workflow instance %s does not belong to run %s", instanceID, runID)
	}
	raw := strings.TrimPrefix(instanceID, prefix)
	generation, err := strconv.Atoi(raw)
	if err != nil || generation <= 0 || strconv.Itoa(generation) != raw {
		return 0, fmt.Errorf("goalrunner: workflow instance %s has an invalid resume generation", instanceID)
	}
	return generation, nil
}
