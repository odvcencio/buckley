package tui

import (
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/fluffyui/backend/sim"
)

func TestApplyToolLoopGuardWarnsThenStops(t *testing.T) {
	state := &toolLoopState{governor: newInteractiveToolLoopGovernor(nil)}
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

func TestInteractiveToolLoopGovernorUsesConfiguredEmergencyFuse(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AgentController.EmergencyFuse.ModelRequests = 40
	governor := newInteractiveToolLoopGovernor(cfg)

	for round := 1; round <= 40; round++ {
		if decision := governor.BeginRound(); decision.Stop {
			t.Fatalf("round %d stopped early: %+v", round, decision)
		}
	}
	decision := governor.BeginRound()
	if !decision.Stop || !strings.Contains(decision.Reason, "40-round") {
		t.Fatalf("round 41 decision = %+v, want configured 40-round stop", decision)
	}
}

func TestInteractiveToolLoopGovernorAllowsTranscriptSizedProgress(t *testing.T) {
	governor := newInteractiveToolLoopGovernor(config.DefaultConfig())
	for round := 1; round <= 40; round++ {
		if decision := governor.BeginRound(); decision.Stop {
			t.Fatalf("productive round %d stopped: %+v", round, decision)
		}
		decision := governor.Observe(
			"read_file",
			fmt.Sprintf(`{"path":"file-%d.go"}`, round),
			fmt.Sprintf("new evidence %d", round),
			true,
		)
		if decision.Stop {
			t.Fatalf("productive tool call %d stopped: %+v", round, decision)
		}
	}
}

func TestInteractiveProgressControllerHonorsRolloutMode(t *testing.T) {
	cfg := config.DefaultConfig()
	if got := newInteractiveProgressController(cfg); got != nil {
		t.Fatalf("legacy progress controller = %+v, want nil", got)
	}
	cfg.AgentController.Mode = "shadow"
	cfg.AgentController.PolicyVersion = "progress-v2"
	cfg.AgentController.EmergencyFuse.ModelRequests = 41
	got := newInteractiveProgressController(cfg)
	if got == nil || got.Mode != "shadow" || got.PolicyVersion != "progress-v2" || got.Fuses.ModelRequests != 41 {
		t.Fatalf("interactive progress controller = %+v", got)
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
