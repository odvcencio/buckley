package goalrunner

import (
	"context"
	"fmt"
	"sync"

	"m31labs.dev/buckley/pkg/durability"
)

// Resolver serves any goal on the ledger from one worker process: every
// activity request carries its run ID, and the resolver builds (and
// caches) the per-goal Runner on first use. This is what lets a
// standalone `buckley goal worker` host workflows it did not start.
type Resolver struct {
	mu    sync.Mutex
	cache map[string]*Runner
	build func(ctx context.Context, runID string) (*Runner, error)
}

// NewResolver wires a Resolver around a per-run Runner factory.
func NewResolver(build func(ctx context.Context, runID string) (*Runner, error)) *Resolver {
	return &Resolver{cache: map[string]*Runner{}, build: build}
}

func (r *Resolver) runner(ctx context.Context, runID string) (*Runner, error) {
	if runID == "" {
		return nil, fmt.Errorf("goalrunner: request carries no run ID")
	}
	r.mu.Lock()
	cached, ok := r.cache[runID]
	r.mu.Unlock()
	if ok {
		return cached, nil
	}
	built, err := r.build(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("goalrunner: resolve run %s: %w", runID, err)
	}
	r.mu.Lock()
	if cached, ok := r.cache[runID]; ok {
		built = cached
	} else {
		r.cache[runID] = built
	}
	r.mu.Unlock()
	return built, nil
}

// ResumeSeed implements durability.TaskRunner.
func (r *Resolver) ResumeSeed(ctx context.Context, runID, taskID string) (durability.ResumeSeed, error) {
	runner, err := r.runner(ctx, runID)
	if err != nil {
		return durability.ResumeSeed{}, err
	}
	return runner.ResumeSeed(ctx, runID, taskID)
}

// NextTask implements durability.TaskRunner.
func (r *Resolver) NextTask(ctx context.Context, req durability.NextTaskRequest) (durability.NextTaskResponse, error) {
	runner, err := r.runner(ctx, req.RunID)
	if err != nil {
		return durability.NextTaskResponse{}, err
	}
	return runner.NextTask(ctx, req)
}

// NextBatch implements durability.TaskRunner.
func (r *Resolver) NextBatch(ctx context.Context, req durability.NextBatchRequest) (durability.NextBatchResponse, error) {
	runner, err := r.runner(ctx, req.RunID)
	if err != nil {
		return durability.NextBatchResponse{}, err
	}
	return runner.NextBatch(ctx, req)
}

// RunTurn implements durability.TaskRunner.
func (r *Resolver) RunTurn(ctx context.Context, req durability.TurnRequest) (durability.TurnResponse, error) {
	runner, err := r.runner(ctx, req.RunID)
	if err != nil {
		return durability.TurnResponse{}, err
	}
	return runner.RunTurn(ctx, req)
}

// RunLegacyTurn implements durability.LegacyTaskRunner for in-flight workflow
// histories that predate serialized workspace identity.
func (r *Resolver) RunLegacyTurn(ctx context.Context, req durability.TurnRequest) (durability.TurnResponse, error) {
	runner, err := r.runner(ctx, req.RunID)
	if err != nil {
		return durability.TurnResponse{}, err
	}
	return runner.RunLegacyTurn(ctx, req)
}

// RecordApprovalWait implements durability.TaskRunner.
func (r *Resolver) RecordApprovalWait(ctx context.Context, wait durability.ApprovalWait) error {
	runner, err := r.runner(ctx, wait.RunID)
	if err != nil {
		return err
	}
	return runner.RecordApprovalWait(ctx, wait)
}

// ResolveApproval implements durability.TaskRunner.
func (r *Resolver) ResolveApproval(ctx context.Context, resolution durability.ApprovalResolution) error {
	runner, err := r.runner(ctx, resolution.RunID)
	if err != nil {
		return err
	}
	return runner.ResolveApproval(ctx, resolution)
}

// FinalizeGoal implements durability.GoalFinalizer.
func (r *Resolver) FinalizeGoal(ctx context.Context, finalization durability.GoalFinalization) error {
	runner, err := r.runner(ctx, finalization.RunID)
	if err != nil {
		return err
	}
	return runner.FinalizeGoal(ctx, finalization)
}

var _ durability.TaskRunner = (*Resolver)(nil)
var _ durability.LegacyTaskRunner = (*Resolver)(nil)
var _ durability.GoalFinalizer = (*Resolver)(nil)
