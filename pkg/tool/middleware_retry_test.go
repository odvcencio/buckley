package tool

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

func TestRetryRetriesUntilSuccess(t *testing.T) {
	attempts := 0
	mw := Retry(RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 1 * time.Millisecond,
		Multiplier:   1,
		Jitter:       0,
		RetryableFunc: func(err error) bool {
			return true
		},
	})

	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("fail")
		}
		return &builtin.Result{Success: true}, nil
	})

	ctx := &ExecutionContext{Context: context.Background(), ToolName: "retry_tool"}
	res, err := exec(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil || !res.Success {
		t.Fatalf("expected success result, got %#v", res)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
	if ctx.Attempt != 3 {
		t.Errorf("expected ctx.Attempt=3, got %d", ctx.Attempt)
	}
}

func TestRetryStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	mw := Retry(RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 1 * time.Millisecond,
		Multiplier:   1,
		Jitter:       0,
	})
	exec := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return nil, errors.New("fail")
	})

	_, err := exec(&ExecutionContext{Context: ctx, ToolName: "retry_tool"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}
