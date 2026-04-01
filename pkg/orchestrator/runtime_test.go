package orchestrator

import (
	"context"
	"testing"

	"github.com/odvcencio/buckley/pkg/types"
)

// --- Static test doubles ---

type staticApiClient struct {
	responses [][]StreamEvent
	callIdx   int
}

func (s *staticApiClient) Stream(ctx context.Context, req ChatRequest) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent, len(s.responses[s.callIdx]))
	for _, e := range s.responses[s.callIdx] {
		ch <- e
	}
	close(ch)
	s.callIdx++
	return ch, nil
}

type staticToolExecutor struct {
	handlers map[string]func(map[string]any) (*ToolResult, error)
}

func (s *staticToolExecutor) Execute(ctx context.Context, name string, input map[string]any) (*ToolResult, error) {
	if h, ok := s.handlers[name]; ok {
		return h(input)
	}
	return &ToolResult{Output: "unknown tool"}, nil
}

func (s *staticToolExecutor) ExecuteWithSandbox(ctx context.Context, call ToolCall, level types.SandboxLevel) (*ToolResult, error) {
	return s.Execute(ctx, call.Name, call.Input)
}

func (s *staticToolExecutor) RequiredTier(name string) types.PermissionTier {
	return types.TierWorkspaceWrite
}

func (s *staticToolExecutor) Available(role string, tier types.PermissionTier) []ToolSpec {
	return nil
}

type alwaysGrantEscalator struct{}

func (a *alwaysGrantEscalator) Decide(ctx context.Context, req types.EscalationRequest) (types.EscalationOutcome, error) {
	return types.EscalationOutcome{Granted: true}, nil
}

type alwaysDenyEscalator struct{}

func (a *alwaysDenyEscalator) Decide(ctx context.Context, req types.EscalationRequest) (types.EscalationOutcome, error) {
	return types.EscalationOutcome{Granted: false, AuditNote: "always deny"}, nil
}

type noopSandboxResolver struct{}

func (n *noopSandboxResolver) ForTool(string, string, float64) types.SandboxLevel {
	return types.SandboxNone
}

// --- Tests ---

func TestRuntimeLoop_SimpleTextResponse(t *testing.T) {
	api := &staticApiClient{
		responses: [][]StreamEvent{
			{
				{Type: EventTextDelta, Text: "Hello "},
				{Type: EventTextDelta, Text: "world"},
				{Type: EventStop},
			},
		},
	}
	loop := NewRuntimeLoop(api, &staticToolExecutor{}, nil, nil, nil)

	summary, err := loop.RunTurn(context.Background(), "Hi")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if summary.FinalText() != "Hello world" {
		t.Errorf("text = %q, want 'Hello world'", summary.FinalText())
	}
	if summary.Iterations != 0 {
		t.Errorf("iterations = %d, want 0", summary.Iterations)
	}
}

func TestRuntimeLoop_ToolExecution(t *testing.T) {
	api := &staticApiClient{
		responses: [][]StreamEvent{
			// Turn 1: model requests tool
			{
				{Type: EventToolUse, ToolCall: &ToolCall{ID: "1", Name: "read_file", Input: map[string]any{"path": "main.go"}}},
				{Type: EventStop},
			},
			// Turn 2: model responds with text
			{
				{Type: EventTextDelta, Text: "File contents received"},
				{Type: EventStop},
			},
		},
	}
	tools := &staticToolExecutor{
		handlers: map[string]func(map[string]any) (*ToolResult, error){
			"read_file": func(input map[string]any) (*ToolResult, error) {
				return &ToolResult{Output: "package main"}, nil
			},
		},
	}
	loop := NewRuntimeLoop(api, tools, &alwaysGrantEscalator{}, &noopSandboxResolver{}, nil)

	summary, err := loop.RunTurn(context.Background(), "Read main.go")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if len(summary.ToolUses) != 1 {
		t.Fatalf("tool uses = %d, want 1", len(summary.ToolUses))
	}
	if summary.ToolUses[0].Name != "read_file" {
		t.Errorf("tool name = %q, want read_file", summary.ToolUses[0].Name)
	}
	if summary.ToolUses[0].Output != "package main" {
		t.Errorf("tool output = %q, want 'package main'", summary.ToolUses[0].Output)
	}
}

func TestRuntimeLoop_PermissionDenied(t *testing.T) {
	api := &staticApiClient{
		responses: [][]StreamEvent{
			{
				{Type: EventToolUse, ToolCall: &ToolCall{ID: "1", Name: "bash", Input: map[string]any{"cmd": "rm -rf /"}}},
				{Type: EventStop},
			},
			{
				{Type: EventTextDelta, Text: "Permission was denied"},
				{Type: EventStop},
			},
		},
	}
	tools := &staticToolExecutor{handlers: map[string]func(map[string]any) (*ToolResult, error){}}
	loop := NewRuntimeLoop(api, tools, &alwaysDenyEscalator{}, &noopSandboxResolver{}, nil)

	summary, err := loop.RunTurn(context.Background(), "delete everything")
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if len(summary.ToolUses) != 1 {
		t.Fatalf("tool uses = %d, want 1", len(summary.ToolUses))
	}
	if summary.ToolUses[0].Error == "" {
		t.Error("expected error in tool use record for denied tool")
	}
	if len(summary.AuditTrail) != 1 {
		t.Fatalf("audit trail = %d, want 1", len(summary.AuditTrail))
	}
}

func TestRuntimeLoop_MaxIterations(t *testing.T) {
	// API always returns a tool call — should hit max iterations
	api := &staticApiClient{
		responses: make([][]StreamEvent, 5),
	}
	for i := range api.responses {
		api.responses[i] = []StreamEvent{
			{Type: EventToolUse, ToolCall: &ToolCall{ID: "1", Name: "bash", Input: map[string]any{}}},
			{Type: EventStop},
		}
	}
	tools := &staticToolExecutor{
		handlers: map[string]func(map[string]any) (*ToolResult, error){
			"bash": func(input map[string]any) (*ToolResult, error) {
				return &ToolResult{Output: "ok"}, nil
			},
		},
	}
	loop := NewRuntimeLoop(api, tools, &alwaysGrantEscalator{}, &noopSandboxResolver{}, nil)
	loop.SetMaxIterations(3)

	_, err := loop.RunTurn(context.Background(), "loop forever")
	if err != ErrMaxIterations {
		t.Errorf("error = %v, want ErrMaxIterations", err)
	}
}
