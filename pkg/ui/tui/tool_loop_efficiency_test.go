package tui

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestStagnationNudge_TriggersOnThirdIdenticalExecution(t *testing.T) {
	state := &toolLoopState{executions: make(map[[32]byte]int)}
	call := model.ToolCall{Function: model.FunctionCall{Name: "run_tests", Arguments: `{"path":"./..."}`}}
	if got := stagnationNudge(state, call, "failed"); got != "" {
		t.Fatalf("first execution nudge = %q", got)
	}
	if got := stagnationNudge(state, call, "failed"); got != "" {
		t.Fatalf("second execution nudge = %q", got)
	}
	if got := stagnationNudge(state, call, "failed"); !strings.Contains(got, "repeated 3 times") {
		t.Fatalf("third execution nudge = %q", got)
	}
}

func TestStagnationNudge_DifferentResultResetsFingerprint(t *testing.T) {
	state := &toolLoopState{executions: make(map[[32]byte]int)}
	call := model.ToolCall{Function: model.FunctionCall{Name: "run_tests", Arguments: `{}`}}
	_ = stagnationNudge(state, call, "failed once")
	_ = stagnationNudge(state, call, "failed once")
	if got := stagnationNudge(state, call, "passed"); got != "" {
		t.Fatalf("changed result should not trigger nudge: %q", got)
	}
}
