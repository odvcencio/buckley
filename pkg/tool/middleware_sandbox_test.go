package tool

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// mockSandboxExecutor implements SandboxExecutor for testing.
type mockSandboxExecutor struct {
	execFunc func(ctx context.Context, req SandboxRequest) (*SandboxResult, error)
	readyErr error
	closed   bool
}

func (m *mockSandboxExecutor) Execute(ctx context.Context, req SandboxRequest) (*SandboxResult, error) {
	if m.execFunc != nil {
		return m.execFunc(ctx, req)
	}
	return &SandboxResult{ExitCode: 0, Stdout: "ok"}, nil
}

func (m *mockSandboxExecutor) Ready(ctx context.Context) error {
	return m.readyErr
}

func (m *mockSandboxExecutor) Close() error {
	m.closed = true
	return nil
}

func TestDockerSandboxMiddleware_RunShell(t *testing.T) {
	executor := &mockSandboxExecutor{
		execFunc: func(_ context.Context, req SandboxRequest) (*SandboxResult, error) {
			if req.Command != "echo hello" {
				t.Errorf("expected command 'echo hello', got %q", req.Command)
			}
			if req.ToolName != "run_shell" {
				t.Errorf("expected tool name 'run_shell', got %q", req.ToolName)
			}
			return &SandboxResult{
				ExitCode: 0,
				Stdout:   "hello\n",
				Duration: 100 * time.Millisecond,
			}, nil
		},
	}

	nextCalled := false
	next := func(ctx *ExecutionContext) (*builtin.Result, error) {
		nextCalled = true
		return &builtin.Result{Success: true}, nil
	}

	mw := DockerSandboxMiddleware(executor, func(string) string { return "execute" })
	handler := mw(next)

	result, err := handler(&ExecutionContext{
		Context:  context.Background(),
		ToolName: "run_shell",
		Params:   map[string]any{"command": "echo hello"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Data["stdout"] != "hello\n" {
		t.Errorf("expected stdout 'hello\\n', got %v", result.Data["stdout"])
	}
	if nextCalled {
		t.Error("next should not be called when sandbox intercepts")
	}
}

func TestDockerSandboxMiddleware_ReadOnlyPassthrough(t *testing.T) {
	executor := &mockSandboxExecutor{}
	nextCalled := false
	next := func(ctx *ExecutionContext) (*builtin.Result, error) {
		nextCalled = true
		return &builtin.Result{Success: true}, nil
	}

	mw := DockerSandboxMiddleware(executor, func(string) string { return "read" })
	handler := mw(next)

	result, err := handler(&ExecutionContext{
		Context:  context.Background(),
		ToolName: "read_file",
		Params:   map[string]any{"path": "/tmp/test.go"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success")
	}
	if !nextCalled {
		t.Error("next should be called for non-sandboxed tools")
	}
}

func TestDockerSandboxMiddleware_NilSandbox(t *testing.T) {
	nextCalled := false
	next := func(ctx *ExecutionContext) (*builtin.Result, error) {
		nextCalled = true
		return &builtin.Result{Success: true}, nil
	}

	mw := DockerSandboxMiddleware(nil, func(string) string { return "" })
	handler := mw(next)

	_, err := handler(&ExecutionContext{
		Context:  context.Background(),
		ToolName: "run_shell",
		Params:   map[string]any{"command": "echo test"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !nextCalled {
		t.Error("next should be called when sandbox is nil")
	}
}

func TestDockerSandboxMiddleware_NonZeroExit(t *testing.T) {
	executor := &mockSandboxExecutor{
		execFunc: func(_ context.Context, req SandboxRequest) (*SandboxResult, error) {
			return &SandboxResult{
				ExitCode: 1,
				Stderr:   "error: something failed",
			}, nil
		},
	}

	next := func(ctx *ExecutionContext) (*builtin.Result, error) {
		t.Error("next should not be called")
		return nil, nil
	}

	mw := DockerSandboxMiddleware(executor, func(string) string { return "execute" })
	handler := mw(next)

	result, err := handler(&ExecutionContext{
		Context:  context.Background(),
		ToolName: "run_shell",
		Params:   map[string]any{"command": "false"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Error("expected failure for non-zero exit")
	}
	if result.Data["exit_code"] != 1 {
		t.Errorf("expected exit code 1, got %v", result.Data["exit_code"])
	}
}

func TestDockerSandboxMiddleware_ExecuteError(t *testing.T) {
	executor := &mockSandboxExecutor{
		execFunc: func(_ context.Context, req SandboxRequest) (*SandboxResult, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}

	mw := DockerSandboxMiddleware(executor, func(string) string { return "execute" })
	handler := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		return nil, nil
	})

	result, err := handler(&ExecutionContext{
		Context:  context.Background(),
		ToolName: "run_shell",
		Params:   map[string]any{"command": "echo test"},
	})

	if err != nil {
		t.Fatalf("unexpected error (should be in result): %v", err)
	}
	if result.Success {
		t.Error("expected failure")
	}
	if result.Error == "" {
		t.Error("expected error message in result")
	}
}

func TestDockerSandboxMiddleware_RunTests(t *testing.T) {
	executor := &mockSandboxExecutor{
		execFunc: func(_ context.Context, req SandboxRequest) (*SandboxResult, error) {
			if req.ToolName != "run_tests" {
				t.Errorf("expected run_tests, got %s", req.ToolName)
			}
			return &SandboxResult{ExitCode: 0, Stdout: "PASS"}, nil
		},
	}

	mw := DockerSandboxMiddleware(executor, func(string) string { return "execute" })
	handler := mw(func(ctx *ExecutionContext) (*builtin.Result, error) {
		t.Error("next should not be called")
		return nil, nil
	})

	result, err := handler(&ExecutionContext{
		Context:  context.Background(),
		ToolName: "run_tests",
		Params:   map[string]any{"command": "go test ./..."},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Error)
	}
}

func TestDockerSandboxMiddleware_EmptyCommand(t *testing.T) {
	executor := &mockSandboxExecutor{}
	nextCalled := false
	next := func(ctx *ExecutionContext) (*builtin.Result, error) {
		nextCalled = true
		return &builtin.Result{Success: true}, nil
	}

	mw := DockerSandboxMiddleware(executor, func(string) string { return "execute" })
	handler := mw(next)

	_, _ = handler(&ExecutionContext{
		Context:  context.Background(),
		ToolName: "run_shell",
		Params:   map[string]any{"command": ""},
	})

	if !nextCalled {
		t.Error("next should be called when command is empty (passthrough)")
	}
}
