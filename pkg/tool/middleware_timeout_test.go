package tool

import (
	"context"
	"errors"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestTimeoutAppliesDeadline(t *testing.T) {
	mw := Timeout(25*time.Millisecond, nil)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		deadline, ok := ctx.Context.Deadline()
		if !ok {
			t.Fatal("expected deadline to be set")
		}
		if time.Until(deadline) <= 0 {
			t.Fatal("expected deadline in the future")
		}
		return &builtin.Result{Success: true}, nil
	})

	ctx := &ExecutionContext{Context: context.Background()}
	if _, err := exec(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTimeout_RestoresParentContext(t *testing.T) {
	for _, scenario := range []string{"success", "error", "panic", "nil parent", "nested"} {
		t.Run(scenario, func(t *testing.T) {
			parent := context.Background()
			if scenario == "nil parent" {
				parent = nil
			}
			ctx := &ExecutionContext{Context: parent}
			var child context.Context
			failure := errors.New("tool failed")
			exec := Timeout(time.Minute, nil)(func(ctx *ExecutionContext) (*builtin.Result, error) {
				child = ctx.Context
				if scenario == "panic" {
					panic(failure)
				}
				if scenario == "nested" {
					inner := Timeout(time.Second, nil)(func(ctx *ExecutionContext) (*builtin.Result, error) {
						if ctx.Context == child {
							t.Fatal("nested timeout reused the outer context")
						}
						return &builtin.Result{Success: true}, nil
					})
					if _, err := inner(ctx); err != nil {
						t.Fatal(err)
					}
					if ctx.Context != child || child.Err() != nil {
						t.Fatal("nested timeout did not restore the live outer context")
					}
				}
				if scenario == "error" {
					return nil, failure
				}
				return &builtin.Result{Success: true}, nil
			})
			if scenario == "panic" {
				func() {
					defer func() {
						if got := recover(); got != failure {
							t.Fatalf("panic=%v want=%v", got, failure)
						}
					}()
					_, _ = exec(ctx)
				}()
			} else {
				_, err := exec(ctx)
				if (scenario == "error" && !errors.Is(err, failure)) || (scenario != "error" && err != nil) {
					t.Fatalf("unexpected error: %v", err)
				}
			}
			if ctx.Context != parent {
				t.Fatal("timeout did not restore the original context")
			}
			if child == nil || !errors.Is(child.Err(), context.Canceled) {
				t.Fatalf("attempt context was not canceled: %v", child)
			}
		})
	}
}

func TestTimeoutSkipsWhenZero(t *testing.T) {
	mw := Timeout(0, nil)
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		if _, ok := ctx.Context.Deadline(); ok {
			t.Fatal("expected no deadline")
		}
		return &builtin.Result{Success: true}, nil
	})

	ctx := &ExecutionContext{Context: context.Background()}
	if _, err := exec(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
