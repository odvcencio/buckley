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
	"m31labs.dev/buckley/pkg/runledger"
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

	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	_, err = ledger.StartRun(context.Background(), runledger.AgentRun{RunID: "run-1", SessionID: "goal-test"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	engine, err := newGoalTurnEngine(cfg, mgr, tool.NewEmptyRegistry(), ledger, ev, dir, "goal-test")
	if err != nil {
		t.Fatalf("newGoalTurnEngine: %v", err)
	}
	return engine, ev
}

func TestNewGoalTurnEngine_RejectsNilDurableLedgerAtWiring(t *testing.T) {
	var typedNil *runledger.SQLiteStore
	for _, tt := range []struct {
		name   string
		ledger goalLedger
	}{
		{name: "nil interface"},
		{name: "typed nil", ledger: typedNil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			engine, err := newGoalTurnEngine(nil, nil, nil, tt.ledger, nil, "", "")
			if err == nil || !strings.Contains(err.Error(), "durable ledger is required") || engine != nil {
				t.Fatalf("engine=%v error=%v, want wiring rejection before dispatch", engine, err)
			}
		})
	}
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

	events, err := engine.ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: "run-1"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var sawPlanned, sawCompleted bool
	for _, event := range events {
		switch event.Type {
		case runledger.EventModelRequestPlanned:
			sawPlanned = event.Payload["step_id"] != nil && event.Payload["input_digest"] != nil
		case runledger.EventModelRequestCompleted:
			sawCompleted = event.Payload["response_evidence_id"] != nil && len(event.EvidenceIDs) == 1
		}
	}
	if !sawPlanned || !sawCompleted {
		t.Fatalf("controller durability events missing planned=%v completed=%v", sawPlanned, sawCompleted)
	}
}

func TestGoalTurnEngine_ReplaysControlToolState(t *testing.T) {
	t.Parallel()
	args, _ := json.Marshal(map[string]string{"summary": "replayed completion"})
	engine, _ := newGoalEngineUnderTest(t, []string{
		goalEngineToolCallResponse(goalCompleteToolName, string(args)),
		goalEngineTextResponse,
	})
	task := goalloop.TaskContext{
		RunID:  "run-1",
		TaskID: "task-1",
		Goal:   goalloop.Goal{Statement: "replay control state"},
		Spec:   goalloop.TaskSpec{Title: "replay control state"},
		Phase:  goalloop.PhaseExecute,
		TurnID: "task-1/cp-000/turn-000",
	}
	if _, err := engine.RunTurn(context.Background(), task); err != nil {
		t.Fatalf("first RunTurn: %v", err)
	}
	replayed, err := engine.RunTurn(context.Background(), task)
	if err != nil {
		t.Fatalf("replay RunTurn: %v", err)
	}
	if !replayed.Completed || replayed.Summary != "replayed completion" {
		t.Fatalf("replayed outcome = %+v, want restored completion state", replayed)
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

func TestGoalTurnEngine_GuardStopReturnsFinalSynthesis(t *testing.T) {
	t.Parallel()
	args, _ := json.Marshal(map[string]string{"reason": "waiting on service", "needs": "service access"})
	blocked := goalEngineToolCallResponse(goalBlockedToolName, string(args))
	engine, _ := newGoalEngineUnderTest(t, []string{blocked, blocked, blocked, goalEngineTextResponse})

	outcome, err := engine.RunTurn(context.Background(), goalloop.TaskContext{
		RunID: "run-1", TaskID: "task-guard", TurnID: "turn-guard",
		Goal: goalloop.Goal{Statement: "inspect service"}, Spec: goalloop.TaskSpec{Title: "inspect service"},
	})
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if outcome.Summary != "wrapped up" {
		t.Fatalf("summary = %q, want guard-stop synthesis", outcome.Summary)
	}
	if outcome.Rounds != 3 || outcome.ToolCalls != 3 {
		t.Fatalf("outcome = %+v, want three guarded tool rounds", outcome)
	}
	if outcome.PromptTokens != 280 || outcome.CompletionTokens != 35 {
		t.Fatalf("usage = %d/%d, want finalization usage included", outcome.PromptTokens, outcome.CompletionTokens)
	}

	events, err := engine.ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: "run-1", TaskID: "task-guard"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	wantKinds := map[string]bool{"exact_repeat": false, "finalization_started": false, "finalization_completed": false}
	for _, event := range events {
		if event.Type != runledger.EventControllerDecision {
			continue
		}
		if kind, _ := event.Payload["kind"].(string); kind != "" {
			if _, ok := wantKinds[kind]; ok {
				wantKinds[kind] = true
			}
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Errorf("missing controller decision %q in %+v", kind, events)
		}
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
	if outcome.Success || !strings.Contains(outcome.Content, "not available in this turn") {
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
	}, false)
	if !strings.Contains(prompt, "VERIFY turn") {
		t.Fatalf("verify prompt missing instruction:\n%s", prompt)
	}
	if !strings.Contains(prompt, goalCompleteToolName) || !strings.Contains(prompt, goalBlockedToolName) {
		t.Fatalf("prompt missing goal tool guidance:\n%s", prompt)
	}
}

// TestIntersectToolNames locks the code-mode narrowing: an unfiltered
// base yields the narrow pool outright, and a phase-filtered base keeps
// only the overlap (verify keeps exec_program and run_shell, drops the
// editors).
func TestIntersectToolNames(t *testing.T) {
	t.Parallel()
	if got := intersectToolNames(nil, codeModeTools); len(got) != len(codeModeTools) {
		t.Fatalf("nil base = %v, want the full narrow pool", got)
	}
	got := intersectToolNames(goalTurnAllowedTools(goalloop.PhaseVerify), codeModeTools)
	allowed := map[string]bool{}
	for _, name := range got {
		allowed[name] = true
	}
	if !allowed["exec_program"] || !allowed["run_shell"] {
		t.Fatalf("verify code-mode pool = %v, want exec_program and run_shell", got)
	}
	if allowed["edit_file"] || allowed["write_file"] {
		t.Fatalf("verify code-mode pool includes an editor: %v", got)
	}
}

// TestGoalTurnSystemPrompt_CodeMode locks the in-turn iteration
// instruction: without it the model ends its turn to retry a failed
// program, which is what made the first live run cost seven turns.
func TestGoalTurnSystemPrompt_CodeMode(t *testing.T) {
	t.Parallel()
	task := goalloop.TaskContext{Goal: goalloop.Goal{Statement: "g"}, Spec: goalloop.TaskSpec{Title: "t"}}
	plain := goalTurnSystemPrompt(task, false)
	if strings.Contains(plain, "CODE MODE") {
		t.Fatal("code-mode guidance leaked into a normal turn")
	}
	coded := goalTurnSystemPrompt(task, true)
	for _, want := range []string{"CODE MODE", "Iterate inside this turn", "Never end the turn to retry"} {
		if !strings.Contains(coded, want) {
			t.Fatalf("code-mode prompt missing %q:\n%s", want, coded)
		}
	}
}
