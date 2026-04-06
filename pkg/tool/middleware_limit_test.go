package tool

import (
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func TestResultSizeLimitTruncates(t *testing.T) {
	long := strings.Repeat("a", 200)
	mw := ResultSizeLimit(80, "...[truncated]")
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{
			Success: true,
			Data: map[string]any{
				"content": long,
			},
		}, nil
	})

	ctx := &ExecutionContext{Metadata: map[string]any{}}
	res, err := exec(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content, _ := res.Data["content"].(string)
	if len(content) >= len(long) {
		t.Fatalf("expected truncation, got length %d", len(content))
	}
	if !strings.HasSuffix(content, "...[truncated]") {
		t.Errorf("expected truncation suffix, got %q", content)
	}
	if truncated, ok := ctx.Metadata["result_truncated"].(bool); !ok || !truncated {
		t.Errorf("expected result_truncated metadata, got %v", ctx.Metadata["result_truncated"])
	}
}

func TestResultSizeLimitNoopWhenSmall(t *testing.T) {
	mw := ResultSizeLimit(200, "...[truncated]")
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{
			Success: true,
			Data: map[string]any{
				"content": "ok",
			},
		}, nil
	})

	res, err := exec(&ExecutionContext{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Data["content"] != "ok" {
		t.Errorf("unexpected content: %v", res.Data["content"])
	}
}

func TestResultSizeLimitSkipsAbridged(t *testing.T) {
	long := strings.Repeat("b", 200)
	mw := ResultSizeLimit(80, "...[truncated]")
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return &builtin.Result{
			Success:       true,
			ShouldAbridge: true,
			Data: map[string]any{
				"content": long,
			},
			DisplayData: map[string]any{
				"content": "abridged",
			},
		}, nil
	})

	ctx := &ExecutionContext{Metadata: map[string]any{}}
	res, err := exec(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	content, _ := res.Data["content"].(string)
	if content != long {
		t.Errorf("expected content to remain unchanged, got %q", content)
	}
	if _, ok := ctx.Metadata["result_truncated"]; ok {
		t.Errorf("did not expect result_truncated metadata, got %v", ctx.Metadata["result_truncated"])
	}
}
