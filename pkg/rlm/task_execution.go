package rlm

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ExecuteTasks runs host-supplied tasks directly through the dispatcher.
// The caller owns task identity and prompt construction; this path does not
// ask the coordinator model to rewrite task IDs, prompts, or completion state.
func (r *Runtime) ExecuteTasks(ctx context.Context, req BatchRequest) ([]BatchResult, error) {
	if r == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	if r.dispatcher == nil {
		return nil, fmt.Errorf("task dispatcher unavailable")
	}
	req = cloneBatchRequest(req)
	if err := validateHostTaskBatch(req); err != nil {
		return nil, err
	}
	var cancel context.CancelFunc
	ctx, cancel = r.taskExecutionContext(ctx, time.Now())
	defer cancel()
	results, err := r.dispatcher.Execute(ctx, req)
	return cloneBatchResults(results), err
}

func (r *Runtime) taskExecutionContext(ctx context.Context, start time.Time) (context.Context, context.CancelFunc) {
	maxWallTime := r.config.Coordinator.MaxWallTime
	if maxWallTime <= 0 {
		maxWallTime = DefaultConfig().Coordinator.MaxWallTime
	}
	if maxWallTime <= 0 {
		return ctx, func() {}
	}
	desired := start.Add(maxWallTime)
	if deadline, ok := ctx.Deadline(); ok && !deadline.After(desired) {
		return ctx, func() {}
	}
	return context.WithDeadline(ctx, desired)
}

func cloneBatchRequest(req BatchRequest) BatchRequest {
	out := req
	if req.Tasks == nil {
		return out
	}
	out.Tasks = make([]SubTask, len(req.Tasks))
	for i, task := range req.Tasks {
		out.Tasks[i] = task
		out.Tasks[i].AllowedTools = append([]string(nil), task.AllowedTools...)
	}
	return out
}

func validateHostTaskBatch(req BatchRequest) error {
	if len(req.Tasks) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(req.Tasks))
	for i, task := range req.Tasks {
		id := strings.TrimSpace(task.ID)
		if id == "" {
			return fmt.Errorf("task %d id required", i+1)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate task id: %s", id)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(task.Prompt) == "" {
			return fmt.Errorf("task %s prompt required", id)
		}
	}
	return nil
}
