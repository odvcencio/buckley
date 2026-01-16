package tool

import (
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func TestHooksMiddlewareOrder(t *testing.T) {
	registry := &HookRegistry{}
	var order []string

	registry.RegisterPreHook("*", func(ctx *ExecutionContext) HookResult {
		order = append(order, "pre-global")
		return HookResult{}
	})
	registry.RegisterPreHook("run_shell", func(ctx *ExecutionContext) HookResult {
		order = append(order, "pre-tool")
		return HookResult{}
	})
	registry.RegisterPostHook("*", func(ctx *ExecutionContext, result *builtin.Result, err error) (*builtin.Result, error) {
		order = append(order, "post-global")
		return result, err
	})
	registry.RegisterPostHook("run_shell", func(ctx *ExecutionContext, result *builtin.Result, err error) (*builtin.Result, error) {
		order = append(order, "post-tool")
		return result, err
	})

	exec := Hooks(registry)(func(ctx *ExecutionContext) (*builtin.Result, error) {
		order = append(order, "exec")
		return &builtin.Result{Success: true}, nil
	})

	if _, err := exec(&ExecutionContext{ToolName: "run_shell"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{"pre-global", "pre-tool", "exec", "post-tool", "post-global"}
	if !sameStringSlice(order, expected) {
		t.Fatalf("order = %#v, want %#v", order, expected)
	}
}

func TestHooksMiddlewareAbort(t *testing.T) {
	registry := &HookRegistry{}
	registry.RegisterPreHook("write_file", func(ctx *ExecutionContext) HookResult {
		return HookResult{Abort: true, AbortReason: "blocked"}
	})

	called := false
	exec := Hooks(registry)(func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	})

	result, err := exec(&ExecutionContext{ToolName: "write_file"})
	if called {
		t.Fatal("expected execution to be aborted")
	}
	if err == nil || !strings.Contains(err.Error(), "aborted by hook") {
		t.Fatalf("expected abort error, got %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("expected abort result, got %#v", result)
	}
	if result.Error != "blocked" {
		t.Fatalf("expected abort reason, got %q", result.Error)
	}
}

func TestHooksMiddlewareModifiedParams(t *testing.T) {
	registry := &HookRegistry{}
	registry.RegisterPreHook("read_file", func(ctx *ExecutionContext) HookResult {
		return HookResult{ModifiedParams: map[string]any{"path": "override"}}
	})

	exec := Hooks(registry)(func(ctx *ExecutionContext) (*builtin.Result, error) {
		if ctx.Params["path"] != "override" {
			return &builtin.Result{Success: false, Error: "params not updated"}, nil
		}
		return &builtin.Result{Success: true}, nil
	})

	result, err := exec(&ExecutionContext{ToolName: "read_file", Params: map[string]any{"path": "original"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected success, got %#v", result)
	}
}
