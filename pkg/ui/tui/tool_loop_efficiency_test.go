package tui

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestToolLoopGuard_WarnsThenStopsIdenticalExecution(t *testing.T) {
	state := &toolLoopState{governor: newInteractiveToolLoopGovernor(nil)}
	call := model.ToolCall{Function: model.FunctionCall{Name: "run_tests", Arguments: `{"path":"./..."}`}}
	result := &builtin.Result{Success: false, Error: "failed"}

	if got := applyToolLoopGuard(state, call, result, nil, "failed"); got != "" {
		t.Fatalf("first execution nudge = %q", got)
	}
	if got := applyToolLoopGuard(state, call, result, nil, "failed"); !strings.Contains(got, "exact action") {
		t.Fatalf("second execution nudge = %q, want strategy warning", got)
	}
	if got := applyToolLoopGuard(state, call, result, nil, "failed"); !strings.Contains(got, "stopped further tool execution") {
		t.Fatalf("third execution nudge = %q, want stop notice", got)
	}
	if !strings.Contains(state.guardReason, "same action") || !strings.Contains(state.guardReason, "3 times") {
		t.Fatalf("guard reason = %q", state.guardReason)
	}
}

func TestToolLoopGuard_DifferentResultDoesNotCountAsExactRepeat(t *testing.T) {
	state := &toolLoopState{governor: newInteractiveToolLoopGovernor(nil)}
	call := model.ToolCall{Function: model.FunctionCall{Name: "run_tests", Arguments: `{}`}}
	result := &builtin.Result{Success: false, Error: "failed"}

	_ = applyToolLoopGuard(state, call, result, nil, "failed once")
	_ = applyToolLoopGuard(state, call, result, nil, "failed once")
	if got := applyToolLoopGuard(state, call, &builtin.Result{Success: true}, nil, "passed"); got != "" {
		t.Fatalf("changed result should not trigger guard: %q", got)
	}
	if state.guardReason != "" {
		t.Fatalf("changed result unexpectedly stopped loop: %q", state.guardReason)
	}
}
