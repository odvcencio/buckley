package tui

import (
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/v2/pkg/conversation"
	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/tool/builtin"
	"m31labs.dev/fluffyui/backend/sim"
)

func TestApplyToolLoopGuardWarnsThenStops(t *testing.T) {
	state := &toolLoopState{governor: newInteractiveToolLoopGovernor()}
	call := model.ToolCall{Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"same.go"}`}}
	result := &builtin.Result{Success: true}

	if nudge := applyToolLoopGuard(state, call, result, nil, "same"); nudge != "" {
		t.Fatalf("first nudge = %q", nudge)
	}
	if nudge := applyToolLoopGuard(state, call, result, nil, "same"); !strings.Contains(nudge, "exact action") {
		t.Fatalf("second nudge = %q", nudge)
	}
	_ = applyToolLoopGuard(state, call, result, nil, "same")
	if !strings.Contains(state.guardReason, "same action") {
		t.Fatalf("guard reason = %q", state.guardReason)
	}
}

func TestHandleToolLoopContextErrorRetriesTwice(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatal(err)
	}
	ctrl := &Controller{app: app}
	state := &toolLoopState{contextScale: 1}
	contextErr := fmt.Errorf("maximum context length exceeded")

	if !ctrl.handleToolLoopContextError(contextErr, state) {
		t.Fatal("first context error should be retried")
	}
	firstScale := state.contextScale
	if firstScale >= 1 || state.contextRetries != 1 {
		t.Fatalf("state after first retry = %+v", state)
	}
	if !ctrl.handleToolLoopContextError(contextErr, state) {
		t.Fatal("second context error should be retried")
	}
	if state.contextScale >= firstScale || state.contextRetries != 2 {
		t.Fatalf("state after second retry = %+v", state)
	}
	if ctrl.handleToolLoopContextError(contextErr, state) {
		t.Fatal("third context error must be returned to caller")
	}
	if ctrl.handleToolLoopContextError(fmt.Errorf("provider unavailable"), &toolLoopState{contextScale: 1}) {
		t.Fatal("unrelated provider error must not be consumed")
	}
}

func TestContextProjectionStatus(t *testing.T) {
	if got := contextProjectionStatus(conversation.ContextProjectionStats{}); got != "" {
		t.Fatalf("empty projection status = %q", got)
	}
	got := contextProjectionStatus(conversation.ContextProjectionStats{
		Compacted: true, OriginalTokens: 120_000, ProjectedTokens: 60_000,
	})
	if !strings.Contains(got, "120.0k→60.0k") || !strings.Contains(got, "compacted") {
		t.Fatalf("projection status = %q", got)
	}
}

func TestCompletePendingToolResponsesFillsParallelSuffix(t *testing.T) {
	messages := []model.Message{
		{Role: "user", Content: "inspect both files"},
		{
			Role: "assistant",
			ToolCalls: []model.ToolCall{
				{ID: "call-1", Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}},
				{ID: "call-2", Type: "function", Function: model.FunctionCall{Name: "read_file", Arguments: `{"path":"b.go"}`}},
			},
		},
		{Role: "tool", ToolCallID: "call-1", Name: "read_file", Content: "first result"},
	}

	completed := completePendingToolResponses(messages, "same action repeated")
	if len(completed) != len(messages)+1 {
		t.Fatalf("message count = %d, want %d", len(completed), len(messages)+1)
	}
	last := completed[len(completed)-1]
	if last.Role != "tool" || last.ToolCallID != "call-2" || last.Name != "read_file" {
		t.Fatalf("unexpected synthetic response: %+v", last)
	}
	content, _ := last.Content.(string)
	if !strings.Contains(content, "loop guard") || !strings.Contains(content, "same action repeated") {
		t.Fatalf("synthetic content = %q", content)
	}
	if len(messages) != 3 {
		t.Fatal("input messages were mutated")
	}
}

func TestCompletePendingToolResponsesLeavesCompleteBatchUntouched(t *testing.T) {
	messages := []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{{ID: "call-1", Function: model.FunctionCall{Name: "read_file"}}}},
		{Role: "tool", ToolCallID: "call-1", Name: "read_file", Content: "done"},
	}
	completed := completePendingToolResponses(messages, "guarded")
	if len(completed) != len(messages) {
		t.Fatalf("message count = %d, want %d", len(completed), len(messages))
	}
}
