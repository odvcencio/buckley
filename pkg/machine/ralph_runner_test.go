package machine

import (
	"context"
	"fmt"
	"testing"
)

type mockCommitExecutor struct {
	hash    string
	message string
	err     error
}

func (m *mockCommitExecutor) Commit(_ context.Context) (string, string, error) {
	return m.hash, m.message, m.err
}

type mockShellExecutor struct {
	output string
	err    error
}

func (m *mockShellExecutor) Run(_ context.Context, _ string) (string, error) {
	return m.output, m.err
}

func TestExecuteCommit_Success(t *testing.T) {
	rt := NewRuntime(RuntimeConfig{
		CommitExecutor: &mockCommitExecutor{hash: "abc123", message: "fix: tests"},
	})
	m := NewObservable("test", Classic, nil)

	event, err := rt.executeAction(context.Background(), m, CommitChanges{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cc, ok := event.(CommitCompleted)
	if !ok {
		t.Fatalf("expected CommitCompleted, got %T", event)
	}
	if cc.Hash != "abc123" {
		t.Errorf("hash = %q, want %q", cc.Hash, "abc123")
	}
	if cc.Message != "fix: tests" {
		t.Errorf("message = %q, want %q", cc.Message, "fix: tests")
	}
}

func TestExecuteCommit_Error(t *testing.T) {
	rt := NewRuntime(RuntimeConfig{
		CommitExecutor: &mockCommitExecutor{err: fmt.Errorf("nothing to commit")},
	})
	m := NewObservable("test", Classic, nil)

	event, err := rt.executeAction(context.Background(), m, CommitChanges{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cc := event.(CommitCompleted)
	if cc.Hash != "" {
		t.Errorf("hash should be empty on error, got %q", cc.Hash)
	}
}

func TestExecuteCommit_NoExecutor(t *testing.T) {
	rt := NewRuntime(RuntimeConfig{})
	m := NewObservable("test", Classic, nil)

	event, err := rt.executeAction(context.Background(), m, CommitChanges{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cc := event.(CommitCompleted)
	if cc.Hash != "none" {
		t.Errorf("hash = %q, want %q", cc.Hash, "none")
	}
}

func TestExecuteVerification_Pass(t *testing.T) {
	rt := NewRuntime(RuntimeConfig{
		ShellExecutor: &mockShellExecutor{output: "ok\nPASS"},
	})
	m := NewObservable("test", Classic, nil)

	event, err := rt.executeAction(context.Background(), m, RunVerification{Command: "go test ./..."})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	vr := event.(VerificationResult)
	if !vr.Passed {
		t.Error("expected Passed=true")
	}
	if vr.Output != "ok\nPASS" {
		t.Errorf("output = %q, want %q", vr.Output, "ok\nPASS")
	}
	if vr.Command != "go test ./..." {
		t.Errorf("command = %q, want %q", vr.Command, "go test ./...")
	}
}

func TestExecuteVerification_Fail(t *testing.T) {
	rt := NewRuntime(RuntimeConfig{
		ShellExecutor: &mockShellExecutor{output: "FAIL", err: fmt.Errorf("exit 1")},
	})
	m := NewObservable("test", Classic, nil)

	event, err := rt.executeAction(context.Background(), m, RunVerification{Command: "make test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	vr := event.(VerificationResult)
	if vr.Passed {
		t.Error("expected Passed=false")
	}
	if vr.Output != "FAIL" {
		t.Errorf("output = %q, want %q", vr.Output, "FAIL")
	}
}

func TestExecuteVerification_NoExecutor(t *testing.T) {
	rt := NewRuntime(RuntimeConfig{})
	m := NewObservable("test", Classic, nil)

	event, err := rt.executeAction(context.Background(), m, RunVerification{Command: "test"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	vr := event.(VerificationResult)
	if vr.Passed {
		t.Error("expected Passed=false when no executor")
	}
}

func TestExecuteResetContext(t *testing.T) {
	rt := NewRuntime(RuntimeConfig{})
	m := NewObservable("test", Classic, nil)

	event, err := rt.executeAction(context.Background(), m, ResetContext{
		Spec:      "goal: fix tests",
		LastError: "compilation error",
		Iteration: 3,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, ok := event.(ContextResetDone)
	if !ok {
		t.Fatalf("expected ContextResetDone, got %T", event)
	}
}
