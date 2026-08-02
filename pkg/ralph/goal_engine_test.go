package ralph

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/taskstate"
)

type fakeGoalBackend struct {
	name      string
	available bool
	result    *BackendResult
	err       error
	requests  []BackendRequest
}

func (f *fakeGoalBackend) Name() string    { return f.name }
func (f *fakeGoalBackend) Available() bool { return f.available }
func (f *fakeGoalBackend) Execute(_ context.Context, req BackendRequest) (*BackendResult, error) {
	f.requests = append(f.requests, req)
	return f.result, f.err
}

func newBackendEngineUnderTest(t *testing.T, backend Backend) (*BackendTurnEngine, evidence.Store) {
	t.Helper()
	dir := t.TempDir()
	ev, err := evidence.New(filepath.Join(dir, "ev.db"), evidence.WithBlobRoot(filepath.Join(dir, "blobs")))
	if err != nil {
		t.Fatalf("evidence.New: %v", err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	engine, err := NewBackendTurnEngine(backend, ev, dir)
	if err != nil {
		t.Fatalf("NewBackendTurnEngine: %v", err)
	}
	return engine, ev
}

func goalTask() goalloop.TaskContext {
	return goalloop.TaskContext{
		RunID:  "run-1",
		TaskID: "task-1",
		Goal:   goalloop.Goal{Statement: "migrate the tests"},
		Spec:   goalloop.TaskSpec{Title: "port store tests", AcceptanceCriteria: []string{"suite green"}},
		Phase:  goalloop.PhaseExecute,
	}
}

// TestBackendTurnEngine_CleanRunClaimsCompletion locks the outcome
// mapping: a clean execution stores its output as evidence, claims
// completion referencing it, and carries a passing required check from
// the backend's test counts.
func TestBackendTurnEngine_CleanRunClaimsCompletion(t *testing.T) {
	t.Parallel()
	backend := &fakeGoalBackend{name: "claude", available: true, result: &BackendResult{
		Backend: "claude", TokensIn: 900, TokensOut: 150, Cost: 0.42,
		FilesChanged: []string{"a.go", "b.go"}, TestsPassed: 12, TestsFailed: 0,
		Output: "ported both files, suite green",
	}}
	engine, ev := newBackendEngineUnderTest(t, backend)

	outcome, err := engine.RunTurn(context.Background(), goalTask())
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if !outcome.Completed || outcome.CompletedEvidenceID == "" {
		t.Fatalf("outcome = %+v, want completion with evidence", outcome)
	}
	if !outcome.StateChanged || outcome.SpentUSD != 0.42 || outcome.PromptTokens != 900 {
		t.Fatalf("outcome mapping = %+v", outcome)
	}
	if len(outcome.Checks) != 1 || outcome.Checks[0].Status != taskstate.VerificationPass || !outcome.Checks[0].Required {
		t.Fatalf("checks = %+v, want one required pass", outcome.Checks)
	}
	if outcome.Checks[0].EvidenceID != outcome.CompletedEvidenceID {
		t.Fatal("check and completion must reference the same evidence object")
	}

	obj, err := ev.Get(context.Background(), outcome.CompletedEvidenceID)
	if err != nil {
		t.Fatalf("evidence.Get: %v", err)
	}
	if !strings.Contains(string(obj.InlineBody), "ported both files, suite green") {
		t.Fatalf("evidence body missing backend output:\n%s", obj.InlineBody)
	}

	if len(backend.requests) != 1 || !strings.Contains(backend.requests[0].Prompt, "port store tests") {
		t.Fatalf("backend prompt = %+v", backend.requests)
	}
}

// TestBackendTurnEngine_FailingTestsBlockCompletion locks the gate feed:
// failing tests produce a required fail check and no completion claim,
// so the G7 gate routes to verify instead of completing.
func TestBackendTurnEngine_FailingTestsBlockCompletion(t *testing.T) {
	t.Parallel()
	backend := &fakeGoalBackend{name: "codex", available: true, result: &BackendResult{
		Backend: "codex", TestsPassed: 10, TestsFailed: 2, Output: "two failures remain",
		FilesChanged: []string{"a.go"},
	}}
	engine, _ := newBackendEngineUnderTest(t, backend)

	outcome, err := engine.RunTurn(context.Background(), goalTask())
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if outcome.Completed {
		t.Fatal("failing tests must not claim completion")
	}
	if len(outcome.Checks) != 1 || outcome.Checks[0].Status != taskstate.VerificationFail {
		t.Fatalf("checks = %+v, want one required fail", outcome.Checks)
	}
}

// TestBackendTurnEngine_ErrorParksWithRetry locks the failure contract:
// an execution error parks the task with a retry timer instead of
// failing the goal, so a transient backend outage self-heals through
// the queue's retry-after unparking.
func TestBackendTurnEngine_ErrorParksWithRetry(t *testing.T) {
	t.Parallel()
	backend := &fakeGoalBackend{name: "claude", available: true, err: errors.New("rate limited")}
	engine, _ := newBackendEngineUnderTest(t, backend)

	outcome, err := engine.RunTurn(context.Background(), goalTask())
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if outcome.Blocker == nil || outcome.Blocker.RetryAfter == nil {
		t.Fatalf("outcome = %+v, want blocker with retry timer", outcome)
	}
	if !strings.Contains(outcome.Blocker.Reason, "rate limited") {
		t.Fatalf("blocker reason = %q", outcome.Blocker.Reason)
	}
	if time.Until(*outcome.Blocker.RetryAfter) <= 0 {
		t.Fatal("retry timer must be in the future")
	}
}

// TestBackendTurnEngine_UnavailableParksWithoutExecuting locks quota
// handling: an unavailable backend parks immediately and never runs.
func TestBackendTurnEngine_UnavailableParksWithoutExecuting(t *testing.T) {
	t.Parallel()
	backend := &fakeGoalBackend{name: "claude", available: false}
	engine, _ := newBackendEngineUnderTest(t, backend)

	outcome, err := engine.RunTurn(context.Background(), goalTask())
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if outcome.Blocker == nil || outcome.Blocker.RetryAfter == nil {
		t.Fatalf("outcome = %+v, want blocker with retry timer", outcome)
	}
	if len(backend.requests) != 0 {
		t.Fatal("unavailable backend must not execute")
	}
}

// TestBackendTaskPrompt_VerifyPhase locks the verify instruction for
// whole-task delegation.
func TestBackendTaskPrompt_VerifyPhase(t *testing.T) {
	t.Parallel()
	task := goalTask()
	task.Phase = goalloop.PhaseVerify
	prompt := backendTaskPrompt(task)
	if !strings.Contains(prompt, "VERIFY the prior work") {
		t.Fatalf("verify prompt missing instruction:\n%s", prompt)
	}
	if !strings.Contains(prompt, "suite green") {
		t.Fatalf("prompt missing acceptance criteria:\n%s", prompt)
	}
}
