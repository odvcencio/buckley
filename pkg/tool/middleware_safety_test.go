package tool

import (
	"context"
	"testing"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func TestPanicRecovery(t *testing.T) {
	exec := PanicRecovery()(func(ctx *ExecutionContext) (*builtin.Result, error) {
		panic("boom")
	})

	ctx := &ExecutionContext{
		Context:  context.Background(),
		ToolName: "boom_tool",
	}
	res, err := exec(ctx)
	if err == nil {
		t.Fatal("expected error")
	}
	if res == nil || res.Success {
		t.Fatalf("expected failure result, got %#v", res)
	}
	if ctx.Metadata == nil {
		t.Fatal("expected metadata to be set")
	}
	if stack, ok := ctx.Metadata["panic_stack"].(string); !ok || stack == "" {
		t.Errorf("expected panic_stack to be recorded")
	}
	if val, ok := ctx.Metadata["panic_value"]; !ok || val == "" {
		t.Errorf("expected panic_value to be recorded")
	}
}
