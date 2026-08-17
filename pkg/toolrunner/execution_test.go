package toolrunner

import (
	"context"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
)

func TestRunner_ExecuteToolCallsParallel_ReleasesSemaphoreAfterPanic(t *testing.T) {
	runner := &Runner{config: Config{
		EnableParallelTools: true,
		MaxParallelTools:    1,
		ToolExecutor: func(_ context.Context, call model.ToolCall, _ map[string]any, _ map[string]tool.Tool) (ToolExecutionResult, error) {
			if call.ID == "panic" {
				panic("boom")
			}
			return ToolExecutionResult{Result: "ok", Success: true}, nil
		},
	}}

	calls := []model.ToolCall{
		{ID: "panic", Function: model.FunctionCall{Name: "panic_tool", Arguments: `{}`}},
		{ID: "success", Function: model.FunctionCall{Name: "success_tool", Arguments: `{}`}},
	}

	type executionResult struct {
		records []ToolCallRecord
		err     error
	}
	result := &Result{}
	done := make(chan executionResult, 1)
	go func() {
		records, err := runner.executeToolCalls(context.Background(), calls, nil, result)
		done <- executionResult{records: records, err: err}
	}()

	var got executionResult
	select {
	case got = <-done:
	case <-time.After(time.Second):
		t.Fatal("parallel tool execution hung after a tool panic")
	}

	if got.err != nil {
		t.Fatalf("executeToolCalls returned error: %v", got.err)
	}
	if len(got.records) != len(calls) {
		t.Fatalf("got %d records, want %d", len(got.records), len(calls))
	}
	if len(result.ToolCalls) != len(calls) {
		t.Fatalf("result contains %d tool calls, want %d", len(result.ToolCalls), len(calls))
	}
	if got.records[0].Success {
		t.Fatal("panicking tool reported success")
	}
	if got.records[0].Result != "tool panicked: boom" {
		t.Errorf("panic result = %q, want %q", got.records[0].Result, "tool panicked: boom")
	}
	if !strings.Contains(got.records[0].Error, "tool panicked: boom") {
		t.Errorf("panic error = %q, want panic details", got.records[0].Error)
	}
	if !got.records[1].Success || got.records[1].Result != "ok" {
		t.Errorf("successful tool record = %+v, want success with result %q", got.records[1], "ok")
	}
}
