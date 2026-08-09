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
