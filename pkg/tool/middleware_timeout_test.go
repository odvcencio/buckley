package tool

import (
	"context"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
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
