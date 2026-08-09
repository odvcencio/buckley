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
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/durability"
	"m31labs.dev/buckley/pkg/goalloop"
)

// Runner hosts one goal's activities.
type Runner struct {
	loop  *goalloop.Loop
	goal  goalloop.Goal
	specs map[string]goalloop.TaskSpec
}

// New wires a Runner for one loaded goal.
func New(loop *goalloop.Loop, goal goalloop.Goal, specs map[string]goalloop.TaskSpec) *Runner {
	return &Runner{loop: loop, goal: goal, specs: specs}
}

// ResumeSeed implements durability.TaskRunner.
func (r *Runner) ResumeSeed(ctx context.Context, runID, taskID string) (durability.ResumeSeed, error) {
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

// RunTurn implements durability.TaskRunner over Loop.TurnStep.
func (r *Runner) RunTurn(ctx context.Context, req durability.TurnRequest) (durability.TurnResponse, error) {
	var drive goalloop.DriveSnapshot
	if len(req.Drive) > 0 {
		if err := json.Unmarshal(req.Drive, &drive); err != nil {
			return durability.TurnResponse{}, fmt.Errorf("goalrunner: decode drive snapshot: %w", err)
		}
	}
	step, err := r.loop.TurnStep(ctx, goalloop.TurnStepRequest{
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
	})
	if err != nil {
		return durability.TurnResponse{}, err
	}
	next, err := json.Marshal(step.Drive)
	if err != nil {
		return durability.TurnResponse{}, fmt.Errorf("goalrunner: marshal drive snapshot: %w", err)
	}
	return durability.TurnResponse{
		Kind:         string(step.Kind),
		Decision:     string(step.Decision),
		Status:       step.Status,
		Drive:        next,
		TurnSpentUSD: step.TurnSpentUSD,
		Rounds:       step.Rounds,
		ToolCalls:    step.ToolCalls,
	}, nil
}
