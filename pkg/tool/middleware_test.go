package tool

import (
	"context"
	"reflect"
	"testing"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func TestChain_Order(t *testing.T) {
	var order []string
	mw := func(label string) Middleware {
		return func(next Executor) Executor {
			return func(ctx *ExecutionContext) (*builtin.Result, error) {
				order = append(order, "pre-"+label)
				res, err := next(ctx)
				order = append(order, "post-"+label)
				return res, err
			}
		}
	}
	base := func(ctx *ExecutionContext) (*builtin.Result, error) {
		order = append(order, "base")
		return &builtin.Result{Success: true}, nil
	}

	exec := Chain(mw("a"), mw("b"), mw("c"))(base)
	_, err := exec(&ExecutionContext{Context: context.Background(), Metadata: map[string]any{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := []string{
		"pre-a",
		"pre-b",
		"pre-c",
		"base",
		"post-c",
		"post-b",
		"post-a",
	}
	if !reflect.DeepEqual(order, expected) {
		t.Errorf("order = %#v, want %#v", order, expected)
	}
}

func TestChain_ContextPropagation(t *testing.T) {
	base := func(ctx *ExecutionContext) (*builtin.Result, error) {
		if ctx.Context == nil {
			t.Error("expected context to be set")
		}
		if ctx.Metadata == nil {
			t.Error("expected metadata to be set")
		}
		if got := ctx.Metadata["key"]; got != "value" {
			t.Errorf("metadata key = %v, want %q", got, "value")
		}
		return &builtin.Result{Success: true}, nil
	}
	mw := func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			if ctx.Metadata == nil {
				ctx.Metadata = map[string]any{}
			}
			ctx.Metadata["key"] = "value"
			return next(ctx)
		}
	}

	exec := Chain(mw)(base)
	_, err := exec(&ExecutionContext{Context: context.Background(), Metadata: map[string]any{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestChain_ShortCircuit(t *testing.T) {
	called := false
	base := func(ctx *ExecutionContext) (*builtin.Result, error) {
		called = true
		return &builtin.Result{Success: true}, nil
	}
	mw := func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			return &builtin.Result{Success: false, Error: "blocked"}, nil
		}
	}

	exec := Chain(mw)(base)
	res, err := exec(&ExecutionContext{Context: context.Background()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Error("expected base executor to be skipped")
	}
	if res == nil || res.Success {
		t.Errorf("expected failure result, got %#v", res)
	}
}
