package runner

import (
	"context"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/types"
)

func TestDaemonRunner_AcceptsTask(t *testing.T) {
	cfg := &RunnerConfig{Mode: ModeDaemon, Role: "worker", MaxTurns: 10}
	deps := &RuntimeDeps{
		Api: &testApiClient{responses: [][]orchestrator.StreamEvent{
			{{Type: orchestrator.EventTextDelta, Text: "done"}, {Type: orchestrator.EventStop}},
		}},
		Tools:     &testToolExecutor{},
		Escalator: &testGrantEscalator{},
		Sandbox:   &testNoopSandbox{},
	}

	daemon := NewDaemonRunner(cfg, deps)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go daemon.Run(ctx)

	daemon.Submit(Task{ID: "test-1", Prompt: "hello", Priority: 10, Channel: ChannelLocal})

	select {
	case result := <-daemon.Results():
		if result.ID != "test-1" {
			t.Errorf("task ID = %q, want test-1", result.ID)
		}
		if result.Error != "" {
			t.Errorf("unexpected error: %s", result.Error)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for result")
	}
}

func TestDaemonRunner_RejectsOverBudget(t *testing.T) {
	cfg := &RunnerConfig{Mode: ModeDaemon, Role: "worker", MaxTurns: 10}
	deps := &RuntimeDeps{
		Evaluator: &rejectAllEvaluator{},
	}

	daemon := NewDaemonRunner(cfg, deps)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go daemon.Run(ctx)

	daemon.Submit(Task{ID: "over-budget", Prompt: "hello", Priority: 1, Channel: ChannelLocal})

	select {
	case result := <-daemon.Results():
		if result.Error == "" {
			t.Error("expected error for over-budget rejection")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for rejection")
	}
}

type rejectAllEvaluator struct{}

func (r *rejectAllEvaluator) EvalStrategy(domain, name string, facts map[string]any) (types.StrategyResult, error) {
	return types.StrategyResult{Params: map[string]any{
		"action": "reject", "reason": "budget nearly exhausted",
	}}, nil
}
