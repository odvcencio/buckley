package runner

import (
	"context"
	"testing"

	"github.com/odvcencio/buckley/pkg/orchestrator"
	"github.com/odvcencio/buckley/pkg/types"
)

// --- test doubles ---

type testApiClient struct {
	responses [][]orchestrator.StreamEvent
	callIdx   int
}

func (s *testApiClient) Stream(ctx context.Context, req orchestrator.ChatRequest) (<-chan orchestrator.StreamEvent, error) {
	ch := make(chan orchestrator.StreamEvent, len(s.responses[s.callIdx]))
	for _, e := range s.responses[s.callIdx] {
		ch <- e
	}
	close(ch)
	s.callIdx++
	return ch, nil
}

type testToolExecutor struct{}

func (t *testToolExecutor) Execute(ctx context.Context, name string, input map[string]any) (*orchestrator.ToolResult, error) {
	return &orchestrator.ToolResult{Output: "ok"}, nil
}
func (t *testToolExecutor) ExecuteWithSandbox(ctx context.Context, call orchestrator.ToolCall, level types.SandboxLevel) (*orchestrator.ToolResult, error) {
	return &orchestrator.ToolResult{Output: "ok"}, nil
}
func (t *testToolExecutor) RequiredTier(name string) types.PermissionTier {
	return types.TierWorkspaceWrite
}
func (t *testToolExecutor) Available(role string, tier types.PermissionTier) []orchestrator.ToolSpec {
	return nil
}

type testGrantEscalator struct{}

func (g *testGrantEscalator) Decide(ctx context.Context, req types.EscalationRequest) (types.EscalationOutcome, error) {
	return types.EscalationOutcome{Granted: true}, nil
}

type testNoopSandbox struct{}

func (n *testNoopSandbox) ForTool(string, string, float64) types.SandboxLevel {
	return types.SandboxNone
}

func TestRunOneShot_ReturnsResult(t *testing.T) {
	cfg := &RunnerConfig{Mode: ModeOneShot, Role: "worker", MaxTurns: 5}
	deps := &RuntimeDeps{
		Api: &testApiClient{responses: [][]orchestrator.StreamEvent{
			{{Type: orchestrator.EventTextDelta, Text: "4"}, {Type: orchestrator.EventStop}},
		}},
		Tools:     &testToolExecutor{},
		Escalator: &testGrantEscalator{},
		Sandbox:   &testNoopSandbox{},
	}

	result, err := RunOneShot(context.Background(), cfg, "what is 2+2?", deps)
	if err != nil {
		t.Fatalf("RunOneShot: %v", err)
	}
	if result.Message != "4" {
		t.Errorf("message = %q, want '4'", result.Message)
	}
	if result.Iterations != 0 {
		t.Errorf("iterations = %d, want 0", result.Iterations)
	}
}
