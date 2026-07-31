package toolrunner

import (
	"strings"
	"testing"
)

func TestApplyRunnerLoopGuardStopsRepeatedToolResult(t *testing.T) {
	governor := newRunnerLoopGovernor(10)
	record := ToolCallRecord{
		Name:      "read_file",
		Arguments: `{"path":"same.go"}`,
		Result:    "same",
		Success:   true,
	}

	_, first := applyRunnerLoopGuard(governor, record, record.Result)
	if first.Stop || first.Nudge != "" {
		t.Fatalf("first decision = %+v", first)
	}
	content, second := applyRunnerLoopGuard(governor, record, record.Result)
	if second.Stop || !strings.Contains(content, "Harness notice") {
		t.Fatalf("second decision/content = %+v %q", second, content)
	}
	_, third := applyRunnerLoopGuard(governor, record, record.Result)
	if !third.Stop || third.Kind != "exact_repeat" {
		t.Fatalf("third decision = %+v", third)
	}
}

func TestRunnerLoopGuardMessage(t *testing.T) {
	got := runnerLoopGuardMessage("read_file repeated")
	if !strings.Contains(got, "stopped the tool loop") || !strings.Contains(got, "read_file repeated") {
		t.Fatalf("message = %q", got)
	}
}
