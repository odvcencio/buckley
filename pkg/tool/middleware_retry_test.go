package tool

import (
	"context"
	"errors"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/tool/external"
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

	ctx := &ExecutionContext{Context: context.Background(), ToolName: "read_file", Tool: &builtin.ReadFileTool{}}
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

func TestDefaultMiddlewareStack_RetriesTransientRead(t *testing.T) {
	cfg := DefaultRegistryConfig().Middleware
	cfg.RetryConfig.InitialDelay = 0
	cfg.RetryConfig.Jitter = 0
	parent, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ctx := &ExecutionContext{Context: parent, ToolName: "read_file", Tool: &builtin.ReadFileTool{}}
	var attempts []context.Context
	exec := Chain(DefaultMiddlewareStack(cfg)...)(func(ctx *ExecutionContext) (*builtin.Result, error) {
		if err := ctx.Context.Err(); err != nil {
			t.Fatalf("attempt started with canceled context: %v", err)
		}
		parentDeadline, _ := parent.Deadline()
		if deadline, ok := ctx.Context.Deadline(); !ok || !deadline.Equal(parentDeadline) {
			t.Fatalf("attempt did not preserve the earlier parent deadline: %v", deadline)
		}
		attempts = append(attempts, ctx.Context)
		if len(attempts) == 1 {
			return nil, errors.New("connection refused")
		}
		return &builtin.Result{Success: true}, nil
	})
	result, err := exec(ctx)
	if err != nil || result == nil || !result.Success || len(attempts) != 2 || ctx.Attempt != 2 {
		t.Fatalf("result=%+v err=%v attempts=%d recorded=%d", result, err, len(attempts), ctx.Attempt)
	}
	if ctx.Context != parent || parent.Err() != nil {
		t.Fatalf("parent context was replaced or canceled: %v", ctx.Context.Err())
	}
	if attempts[0] == attempts[1] {
		t.Fatal("retry reused the first attempt's context")
	}
	for _, attempt := range attempts {
		if !errors.Is(attempt.Err(), context.Canceled) {
			t.Fatalf("attempt context was not released: %v", attempt.Err())
		}
	}
}

func TestDefaultMiddlewareStack_StopsAtParentCancellation(t *testing.T) {
	for _, scenario := range []string{"before read", "before write", "during backoff"} {
		t.Run(scenario, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := &ExecutionContext{Context: parent, ToolName: "read_file", Tool: &builtin.ReadFileTool{}}
			if scenario == "before write" {
				ctx.ToolName, ctx.Tool = "write_file", namedMetadataTestTool("write_file")
			}
			cfg := DefaultRegistryConfig().Middleware
			cfg.RetryConfig.InitialDelay = time.Minute
			cfg.RetryConfig.RetryableFunc = func(error) bool {
				cancel()
				return true
			}
			wantCalls := 1
			if scenario != "during backoff" {
				cancel()
				wantCalls = 0
			}
			calls := 0
			exec := Chain(DefaultMiddlewareStack(cfg)...)(func(*ExecutionContext) (*builtin.Result, error) {
				calls++
				return nil, errors.New("temporary failure")
			})
			_, err := exec(ctx)
			if !errors.Is(err, context.Canceled) || calls != wantCalls || ctx.Context != parent {
				t.Fatalf("err=%v calls=%d want=%d restored=%v", err, calls, wantCalls, ctx.Context == parent)
			}
		})
	}
}

func TestRetry_UsesRegisteredToolPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		tool Tool
		want int
	}{
		{"read_file", &builtin.ReadFileTool{}, 3},
		{"write_file", namedMetadataTestTool("write_file"), 1},
		{"run_code", &builtin.DynamicCodeTool{}, 1},
		{"commit_changes", namedMetadataTestTool("commit_changes"), 1},
		{"generate_test", &builtin.GenerateTestTool{}, 1},
		{"spawn_subagent", namedMetadataTestTool("spawn_subagent"), 1},
		{"browser", &governedTestTool{name: "browser", metadata: ToolMetadata{Impact: ImpactReadOnly, Category: CategoryBrowser}}, 1},
		{"custom_mutation", &governedTestTool{name: "custom_mutation", metadata: ToolMetadata{Impact: ImpactModifying}}, 1},
		{"external_read_file", external.NewTool(&external.ToolManifest{Name: "read_file"}, "/unused"), 1},
		{"unknown", nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			failure := errors.New("temporary failure")
			partial := &builtin.Result{Success: false, Error: failure.Error()}
			exec := Retry(RetryConfig{MaxAttempts: 3, RetryableFunc: func(error) bool { return true }})(func(*ExecutionContext) (*builtin.Result, error) {
				attempts++
				return partial, failure
			})
			ctx := &ExecutionContext{Context: context.Background(), ToolName: tc.name, Tool: tc.tool}
			result, err := exec(ctx)
			if attempts != tc.want || ctx.Attempt != tc.want || result != partial || !errors.Is(err, failure) {
				t.Fatalf("attempts=%d recorded=%d want=%d result=%+v err=%v", attempts, ctx.Attempt, tc.want, result, err)
			}
		})
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
