package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
)

// newGoalEngineTestServer scripts non-streaming chat completions: each
// request pops the next response body.
func newGoalEngineTestServer(t *testing.T, responses []string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	call := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		idx := call
		if idx >= len(responses) {
			idx = len(responses) - 1
		}
		call++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responses[idx]))
	}))
	t.Cleanup(server.Close)
	return server
}

func goalEngineToolCallResponse(name, arguments string) string {
	return fmt.Sprintf(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"%s","arguments":%q}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":80,"completion_tokens":10,"total_tokens":90}}`, name, arguments)
}

const goalEngineTextResponse = `{"id":"chatcmpl-2","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"wrapped up"},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":5,"total_tokens":45}}`

func newGoalEngineUnderTest(t *testing.T, responses []string) (*goalTurnEngine, *evidence.SQLiteStore) {
	t.Helper()
	server := newGoalEngineTestServer(t, responses)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	cfg.Models.Execution = "gpt-4o"

	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	dir := t.TempDir()
	ev, err := evidence.New(filepath.Join(dir, "ev.db"), evidence.WithBlobRoot(filepath.Join(dir, "blobs")))
	if err != nil {
		t.Fatalf("evidence.New: %v", err)
	}
	t.Cleanup(func() { _ = ev.Close() })

	return newGoalTurnEngine(cfg, mgr, tool.NewEmptyRegistry(), ev, dir), ev
}

// TestGoalTurnEngine_CompletionStoresEvidence locks the completion
// contract: the model calls goal_task_complete, the engine reports
// Completed with a stored evidence object, and usage rolls into tokens
// and rounds.
func TestGoalTurnEngine_CompletionStoresEvidence(t *testing.T) {
	t.Parallel()
	args, _ := json.Marshal(map[string]string{"summary": "ported both files; tests green"})
	engine, ev := newGoalEngineUnderTest(t, []string{
		goalEngineToolCallResponse(goalCompleteToolName, string(args)),
		goalEngineTextResponse,
	})

	outcome, err := engine.RunTurn(context.Background(), goalloop.TaskContext{
		RunID:  "run-1",
		TaskID: "task-1",
		Goal:   goalloop.Goal{Statement: "port files"},
		Spec:   goalloop.TaskSpec{Title: "port files"},
		Phase:  goalloop.PhaseExecute,
	})
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if !outcome.Completed || outcome.CompletedEvidenceID == "" {
		t.Fatalf("outcome = %+v, want completion with evidence", outcome)
	}
	if outcome.Summary != "ported both files; tests green" {
		t.Fatalf("summary = %q, want the tool's summary", outcome.Summary)
	}
	if outcome.Rounds != 2 || outcome.PromptTokens != 120 {
		t.Fatalf("outcome = %+v, want 2 rounds and 120 prompt tokens accumulated", outcome)
	}

	obj, err := ev.Get(context.Background(), outcome.CompletedEvidenceID)
	if err != nil {
		t.Fatalf("evidence.Get: %v", err)
	}
	if !strings.Contains(string(obj.InlineBody), "ported both files; tests green") {
		t.Fatalf("evidence body missing summary:\n%s", obj.InlineBody)
	}
}

// TestGoalTurnEngine_BlockedParksWithReason locks the park contract: the
// model calls goal_task_blocked and the outcome carries the blocker.
func TestGoalTurnEngine_BlockedParksWithReason(t *testing.T) {
	t.Parallel()
	args, _ := json.Marshal(map[string]string{"reason": "needs DATABASE_URL", "needs": "integration env"})
	engine, _ := newGoalEngineUnderTest(t, []string{
		goalEngineToolCallResponse(goalBlockedToolName, string(args)),
		goalEngineTextResponse,
	})

	outcome, err := engine.RunTurn(context.Background(), goalloop.TaskContext{
		RunID:  "run-1",
		TaskID: "task-1",
		Goal:   goalloop.Goal{Statement: "integration"},
		Spec:   goalloop.TaskSpec{Title: "integration"},
	})
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if outcome.Completed {
		t.Fatal("blocked turn reported completion")
	}
	if outcome.Blocker == nil || outcome.Blocker.Reason != "needs DATABASE_URL" || outcome.Blocker.Needs != "integration env" {
		t.Fatalf("blocker = %+v, want the tool's reason and needs", outcome.Blocker)
	}
}

// TestGoalTurnAllowedTools locks the verify-phase pool: read, search,
// and run tools only — no editors — while execute turns are unfiltered.
func TestGoalTurnAllowedTools(t *testing.T) {
	t.Parallel()
	if goalTurnAllowedTools(goalloop.PhaseExecute) != nil {
		t.Fatal("execute phase must not filter the tool pool")
	}
	verify := goalTurnAllowedTools(goalloop.PhaseVerify)
	allowed := map[string]bool{}
	for _, name := range verify {
		allowed[name] = true
	}
	for _, want := range []string{"run_tests", "run_shell", "read_file", "git_diff"} {
		if !allowed[want] {
			t.Fatalf("verify pool missing %s: %v", want, verify)
		}
	}
	for _, forbidden := range []string{"write_file", "edit_file", "apply_patch"} {
		if allowed[forbidden] {
			t.Fatalf("verify pool includes editor %s", forbidden)
		}
	}
}

// TestGoalTurnEngine_VerifyPhaseRejectsEditors locks dispatch-side
// enforcement: a hallucinated editor call in a verify turn fails
// instead of executing.
func TestGoalTurnEngine_VerifyPhaseRejectsEditors(t *testing.T) {
	t.Parallel()
	engine, _ := newGoalEngineUnderTest(t, []string{goalEngineTextResponse})
	outcome := engine.dispatchGoalTool(context.Background(), goalloop.TaskContext{Phase: goalloop.PhaseVerify},
		model.ToolCall{ID: "call-1", Function: model.FunctionCall{Name: "write_file", Arguments: "{}"}},
		&goalTurnState{})
	if outcome.Success || !strings.Contains(outcome.Content, "not available in the verify phase") {
		t.Fatalf("outcome = %+v, want a phase rejection", outcome)
	}
}

// TestGoalTurnEngine_VerifyPhasePrompt locks the phase contract: a
// verify-phase turn tells the model to verify, not explore.
func TestGoalTurnEngine_VerifyPhasePrompt(t *testing.T) {
	t.Parallel()
	prompt := goalTurnSystemPrompt(goalloop.TaskContext{
		Goal:  goalloop.Goal{Statement: "g"},
		Spec:  goalloop.TaskSpec{Title: "t"},
		Phase: goalloop.PhaseVerify,
	})
	if !strings.Contains(prompt, "VERIFY turn") {
		t.Fatalf("verify prompt missing instruction:\n%s", prompt)
	}
	if !strings.Contains(prompt, goalCompleteToolName) || !strings.Contains(prompt, goalBlockedToolName) {
		t.Fatalf("prompt missing goal tool guidance:\n%s", prompt)
	}
}
