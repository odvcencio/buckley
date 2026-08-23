package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type goalStateTestTool struct {
	name     string
	metadata tool.ToolMetadata
	execute  func() error
}

func (t *goalStateTestTool) Name() string        { return t.name }
func (t *goalStateTestTool) Description() string { return t.name }
func (t *goalStateTestTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}
func (t *goalStateTestTool) Execute(map[string]any) (*builtin.Result, error) {
	if t.execute != nil {
		if err := t.execute(); err != nil {
			return nil, err
		}
	}
	return &builtin.Result{Success: true}, nil
}
func (t *goalStateTestTool) Metadata() tool.ToolMetadata { return t.metadata }

// newGoalEngineTestServer scripts non-streaming chat completions: each
// request pops the next response body.
func newGoalEngineTestServer(t *testing.T, responses []string) (*httptest.Server, func() int) {
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
	return server, func() int {
		mu.Lock()
		defer mu.Unlock()
		return call
	}
}

func goalEngineToolCallResponse(name, arguments string) string {
	return fmt.Sprintf(`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"%s","arguments":%q}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":80,"completion_tokens":10,"total_tokens":90}}`, name, arguments)
}

const goalEngineTextResponse = `{"id":"chatcmpl-2","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"wrapped up"},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":5,"total_tokens":45}}`

func readBuckleyLicenseForTest(t *testing.T) []byte {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate goal engine test source")
	}
	license, err := os.ReadFile(filepath.Join(filepath.Dir(sourceFile), "..", "..", "LICENSE"))
	if err != nil {
		t.Fatalf("read test license: %v", err)
	}
	return license
}

type failGoalPolicySink struct{ failed bool }

func (s *failGoalPolicySink) WriteEvent(_ context.Context, event runledger.Event) error {
	if event.Type == runledger.EventControllerDecision && event.Payload["kind"] == "model_data_policy" && !s.failed {
		s.failed = true
		return fmt.Errorf("injected policy audit failure")
	}
	return nil
}

func newGoalEngineUnderTest(t *testing.T, responses []string) (*goalTurnEngine, *evidence.SQLiteStore) {
	t.Helper()
	engine, ev, _ := newGoalEngineUnderTestWithCalls(t, responses)
	return engine, ev
}

func newGoalEngineUnderTestWithCalls(t *testing.T, responses []string) (*goalTurnEngine, *evidence.SQLiteStore, func() int) {
	t.Helper()
	server, calls := newGoalEngineTestServer(t, responses)

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
	license := readBuckleyLicenseForTest(t)
	if err := os.WriteFile(filepath.Join(dir, "LICENSE"), license, 0o644); err != nil {
		t.Fatalf("write test license: %v", err)
	}
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
	return engine, ev, calls
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

func TestGoalLoopGovernorConfig_UsesDurableEmergencyFuse(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.AgentController.EmergencyFuse.ModelRequests = 137
	cfg.AgentController.EmergencyFuse.ToolExecutions = 509

	execute := goalLoopGovernorConfig(cfg, goalloop.PhaseExecute)
	if execute.MaxRounds != 137 || execute.MaxToolCalls != 509 {
		t.Fatalf("execute limits = (%d, %d), want (137, 509)", execute.MaxRounds, execute.MaxToolCalls)
	}
	if execute.ReadOnlyWarningAt != goalReadOnlyWarning || execute.ReadOnlyActionAt != goalReadOnlyAction || execute.MaxReadOnlyCalls != goalReadOnlyLimit {
		t.Fatalf("execute read-only ladder = (%d, %d, %d), want (%d, %d, %d)", execute.ReadOnlyWarningAt, execute.ReadOnlyActionAt, execute.MaxReadOnlyCalls, goalReadOnlyWarning, goalReadOnlyAction, goalReadOnlyLimit)
	}

	verify := goalLoopGovernorConfig(cfg, goalloop.PhaseVerify)
	if verify.MaxRounds != 137 || verify.MaxToolCalls != 509 {
		t.Fatalf("verify limits = (%d, %d), want (137, 509)", verify.MaxRounds, verify.MaxToolCalls)
	}
	if verify.MaxReadOnlyCalls != 0 {
		t.Fatalf("verify read-only limit = %d, want disabled", verify.MaxReadOnlyCalls)
	}
}

func TestGoalTurnEngine_PersistedOxContractEmitsExactPrivateToolRequestAndReplays(t *testing.T) {
	for _, tt := range []struct {
		name          string
		retention     string
		wantZDR       bool
		wantZDRAbsent bool
	}{
		{name: "strict zdr", retention: goalloop.GoalRetentionZDR, wantZDR: true},
		{name: "oss no zdr", retention: goalloop.GoalRetentionNonZDR, wantZDRAbsent: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertPersistedOxContractRequest(t, tt.retention, tt.wantZDR, tt.wantZDRAbsent)
		})
	}
}

func assertPersistedOxContractRequest(t *testing.T, retention string, wantZDR, wantZDRAbsent bool) {
	t.Helper()
	const modelID = "stealth/ox-alpha"
	var (
		mu        sync.Mutex
		requests  []model.ChatRequest
		sawAuth   bool
		chatCalls int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"stealth/ox-alpha","name":"Ox Alpha","context_length":1048576,"pricing":{"prompt":"0","completion":"0"},"supported_parameters":["reasoning","tools","tool_choice"]}]}`))
		case "/chat/completions":
			var req model.ChatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decode request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			requests = append(requests, req)
			chatCalls++
			sawAuth = sawAuth || strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
			mu.Unlock()
			_, _ = w.Write([]byte(`{"id":"chatcmpl-ox","model":"stealth/ox-alpha","choices":[{"index":0,"message":{"role":"assistant","content":"probe complete"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.Enabled = true
	cfg.Providers.OpenRouter.APIKey = "redacted-test-key"
	cfg.Providers.OpenRouter.BaseURL = server.URL
	cfg.Providers.OpenAI.Enabled = false
	cfg.Models.DefaultProvider = "openrouter"
	cfg.Models.Planning = modelID
	cfg.Models.Execution = modelID
	cfg.Models.Review = modelID
	cfg.Models.FallbackChains = map[string][]string{modelID: {"other/model"}}
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	dir := t.TempDir()
	license := readBuckleyLicenseForTest(t)
	if err := os.WriteFile(filepath.Join(dir, "LICENSE"), license, 0o644); err != nil {
		t.Fatalf("write test license: %v", err)
	}
	ev, err := evidence.New(filepath.Join(dir, "ev.db"), evidence.WithBlobRoot(filepath.Join(dir, "blobs")))
	if err != nil {
		t.Fatalf("evidence.New: %v", err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	if _, err := ledger.StartRun(context.Background(), runledger.AgentRun{RunID: "run-ox", SessionID: "goal-test"}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	engine, err := newGoalTurnEngine(cfg, mgr, tool.NewEmptyRegistry(), ledger, ev, dir, "goal-test")
	if err != nil {
		t.Fatalf("newGoalTurnEngine: %v", err)
	}
	contract, err := bindGoalModelPolicy(dir, "openrouter", goalloop.GoalModelRequest{
		Model:                    modelID,
		ReasoningEffort:          "max",
		RetentionMode:            retention,
		OpenRouterZDR:            wantZDR,
		OpenRouterDataCollection: "deny",
	})
	if err != nil {
		t.Fatalf("bindGoalModelPolicy: %v", err)
	}
	task := goalloop.TaskContext{
		RunID: "run-ox", TaskID: "task-ox", TurnID: "task-ox/cp-000/turn-000",
		Goal: goalloop.Goal{
			Statement:     "harmless request-shape probe",
			WorkspaceRoot: dir,
			ModelRequest:  contract,
		},
		Spec: goalloop.TaskSpec{Title: "probe"},
	}
	if _, err := engine.RunTurn(context.Background(), task); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if _, err := engine.RunTurn(context.Background(), task); err != nil {
		t.Fatalf("replayed RunTurn: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if chatCalls != 1 || len(requests) != 1 {
		t.Fatalf("chat calls=%d requests=%d, want one provider effect across replay", chatCalls, len(requests))
	}
	req := requests[0]
	if req.Model != modelID || req.Reasoning == nil || req.Reasoning.Effort != "max" {
		t.Fatalf("model/reasoning = %q/%+v", req.Model, req.Reasoning)
	}
	if req.Provider["data_collection"] != "deny" || req.Provider["allow_fallbacks"] != false {
		t.Fatalf("provider policy = %#v", req.Provider)
	}
	if wantZDR && req.Provider["zdr"] != true {
		t.Fatalf("strict ZDR provider policy = %#v", req.Provider)
	}
	if _, present := req.Provider["zdr"]; wantZDRAbsent && present {
		t.Fatalf("non-ZDR request unexpectedly carried zdr: %#v", req.Provider)
	}
	if req.ToolChoice != "auto" || len(req.Tools) < 2 {
		t.Fatalf("tool request = choice:%q tools:%d", req.ToolChoice, len(req.Tools))
	}
	if !sawAuth {
		t.Fatal("OpenRouter request did not carry provider authentication")
	}
}

func TestGoalTurnEngine_OpenRouterPrivacyFailsClosedForOtherProvider(t *testing.T) {
	engine, _ := newGoalEngineUnderTest(t, []string{goalEngineTextResponse})
	req := model.ChatRequest{Model: "openai/gpt-4o"}
	err := engine.applyGoalModelRequest(goalloop.GoalModelRequest{
		Model:                    "openai/gpt-4o",
		ReasoningEffort:          "high",
		OpenRouterZDR:            true,
		OpenRouterDataCollection: "deny",
	}, model.ModelRoute{RequestedModel: "openai/gpt-4o", SelectedModel: "openai/gpt-4o", ProviderID: "openai"}, &req)
	if err == nil || !strings.Contains(err.Error(), "cannot be applied") {
		t.Fatalf("applyGoalModelRequest error = %v, want fail-closed provider mismatch", err)
	}
}

func TestGoalTurnEngine_BoundLicenseChangeBlocksBeforeModel(t *testing.T) {
	engine, _, calls := newGoalEngineUnderTestWithCalls(t, []string{goalEngineTextResponse})
	inspection, err := goalloop.InspectWorkspaceLicense(engine.workDir)
	if err != nil || inspection.Status != goalloop.LicenseStatusRecognizedOSS {
		t.Fatalf("InspectWorkspaceLicense = %+v, %v", inspection, err)
	}
	contract := goalloop.GoalModelRequest{
		PolicyVersion:    goalloop.GoalModelPolicyVersionV1,
		Policy:           "oss_legacy",
		PolicyAction:     "allow",
		PolicyReasonCode: "oss_license_verified",
		RetentionMode:    goalloop.GoalRetentionLegacy,
		WorkspaceLicense: inspection.Evidence,
	}
	if err := os.WriteFile(filepath.Join(engine.workDir, "LICENSE"), []byte("Proprietary and confidential. All rights reserved.\n"), 0o644); err != nil {
		t.Fatalf("replace license: %v", err)
	}
	_, err = engine.RunTurn(context.Background(), goalloop.TaskContext{
		RunID: "run-1", TaskID: "task-policy", TurnID: "turn-policy",
		Goal: goalloop.Goal{Statement: "must not dispatch", WorkspaceRoot: engine.workDir, ModelRequest: contract},
		Spec: goalloop.TaskSpec{Title: "must not dispatch"},
	})
	if err == nil || !strings.Contains(err.Error(), "license_changed") {
		t.Fatalf("RunTurn error = %v, want changed-license block", err)
	}
	if got := calls(); got != 0 {
		t.Fatalf("provider calls = %d, want zero", got)
	}
	events, listErr := engine.ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: "run-1", TaskID: "task-policy"})
	if listErr != nil {
		t.Fatalf("ListEvents: %v", listErr)
	}
	if len(events) != 1 || events[0].Type != runledger.EventControllerDecision || events[0].Payload["action"] != "block" {
		t.Fatalf("policy events = %+v", events)
	}
}

func TestGoalTurnEngine_PolicyAuditFailureBlocksThenReconcilesWithoutDuplicateEffect(t *testing.T) {
	engine, _, calls := newGoalEngineUnderTestWithCalls(t, []string{goalEngineTextResponse})
	sink := &failGoalPolicySink{}
	engine.ledger.SetRalphSink(sink)
	task := goalloop.TaskContext{
		RunID: "run-1", TaskID: "task-audit", TurnID: "turn-audit",
		Goal: goalloop.Goal{Statement: "audit before dispatch", WorkspaceRoot: engine.workDir},
		Spec: goalloop.TaskSpec{Title: "audit before dispatch"},
	}
	if _, err := engine.RunTurn(context.Background(), task); err == nil || !strings.Contains(err.Error(), "goal model policy audit") {
		t.Fatalf("first RunTurn error = %v, want policy audit failure", err)
	}
	if got := calls(); got != 0 {
		t.Fatalf("provider calls after audit failure = %d, want zero", got)
	}
	if _, err := engine.RunTurn(context.Background(), task); err != nil {
		t.Fatalf("retry RunTurn: %v", err)
	}
	if got := calls(); got != 1 {
		t.Fatalf("provider calls after retry = %d, want one", got)
	}
	events, err := engine.ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: "run-1", TaskID: "task-audit"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	policyEvents := 0
	for _, event := range events {
		if event.Type == runledger.EventControllerDecision && event.Payload["kind"] == "model_data_policy" {
			policyEvents++
		}
	}
	if policyEvents != 1 {
		t.Fatalf("policy event count = %d, want one stable event", policyEvents)
	}
}

func TestGoalTurnEngine_ResumeIgnoresPermissiveUserPolicyOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	overrideDir := filepath.Join(home, ".buckley", "rules", "runtime")
	if err := os.MkdirAll(overrideDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(overrideDir, "model_data_policy.arb"), []byte(permissiveModelDataPolicyOverride), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
	engine, _, calls := newGoalEngineUnderTestWithCalls(t, []string{goalEngineTextResponse})
	if err := os.Remove(filepath.Join(engine.workDir, "LICENSE")); err != nil {
		t.Fatalf("remove license: %v", err)
	}
	_, err := engine.RunTurn(context.Background(), goalloop.TaskContext{
		RunID: "run-1", TaskID: "task-override", TurnID: "turn-override",
		Goal: goalloop.Goal{Statement: "legacy resumed goal", WorkspaceRoot: engine.workDir},
		Spec: goalloop.TaskSpec{Title: "must remain blocked"},
	})
	if err == nil || !strings.Contains(err.Error(), "license_missing") {
		t.Fatalf("RunTurn error = %v, embedded denial must beat override", err)
	}
	if got := calls(); got != 0 {
		t.Fatalf("provider calls = %d, want zero", got)
	}
}

func TestGoalTurnEngine_AuthoritativeRouteBlocksPrefixAndHookProviderDrift(t *testing.T) {
	for _, retention := range []string{goalloop.GoalRetentionZDR, goalloop.GoalRetentionNonZDR} {
		for _, attack := range []string{"catalog_prefix_disagreement", "hook_changes_after_audit"} {
			t.Run(retention+"/"+attack, func(t *testing.T) {
				openRouterCalls, directCalls, err := runGoalRouteAttack(t, retention, attack)
				if err == nil {
					t.Fatal("RunTurn unexpectedly succeeded")
				}
				if attack == "catalog_prefix_disagreement" {
					want := "provider_privacy_unsupported"
					if retention == goalloop.GoalRetentionZDR {
						want = "zdr_unenforceable"
					}
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("RunTurn error = %v, want %q", err, want)
					}
				} else if !strings.Contains(err.Error(), "route changed") {
					t.Fatalf("RunTurn error = %v, want route-change failure", err)
				}
				if openRouterCalls != 0 || directCalls != 0 {
					t.Fatalf("provider calls openrouter=%d direct=%d, want zero", openRouterCalls, directCalls)
				}
			})
		}
	}
}

func TestGoalTurnEngine_RejectsUnqualifiedOpenRouterContractBeforeProviderCall(t *testing.T) {
	engine, _, calls := newGoalEngineUnderTestWithCalls(t, []string{goalEngineTextResponse})
	_, err := engine.RunTurn(context.Background(), goalloop.TaskContext{
		RunID: "run-unqualified", TaskID: "task-unqualified", TurnID: "turn-unqualified",
		Goal: goalloop.Goal{
			Statement:     "reject unqualified OpenRouter model",
			WorkspaceRoot: engine.workDir,
			ModelRequest: goalloop.GoalModelRequest{
				PolicyVersion: goalloop.GoalModelPolicyVersionV1,
				Policy:        "strict_zdr", PolicyAction: "allow", PolicyReasonCode: "zdr_enforced",
				Model: "ox-alpha", RetentionMode: goalloop.GoalRetentionZDR, OpenRouterZDR: true,
			},
		},
		Spec: goalloop.TaskSpec{Title: "reject unqualified model"},
	})
	if err == nil || !strings.Contains(err.Error(), "canonical provider/model") {
		t.Fatalf("RunTurn error = %v", err)
	}
	if got := calls(); got != 0 {
		t.Fatalf("provider calls = %d, want zero", got)
	}
}

func runGoalRouteAttack(t *testing.T, retention, attack string) (int, int, error) {
	t.Helper()
	openRouterCalls, directCalls := 0, 0
	providerServer := func(calls *int) *httptest.Server {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			(*calls)++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(goalEngineTextResponse))
		}))
		t.Cleanup(server.Close)
		return server
	}
	openRouterServer := providerServer(&openRouterCalls)
	directServer := providerServer(&directCalls)
	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.Enabled = true
	cfg.Providers.OpenRouter.APIKey = "redacted-openrouter-key"
	cfg.Providers.OpenRouter.BaseURL = openRouterServer.URL
	cfg.Providers.Anthropic.Enabled = true
	cfg.Providers.Anthropic.APIKey = "redacted-anthropic-key"
	cfg.Providers.Anthropic.BaseURL = directServer.URL
	cfg.Models.DefaultProvider = "openrouter"
	cfg.Models.Execution = "stealth/ox-alpha"
	cfg.Providers.ModelRouting = map[string]string{"direct/": "anthropic"}
	if attack == "catalog_prefix_disagreement" {
		cfg.Providers.ModelRouting["stealth/"] = "anthropic"
	}
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if attack == "catalog_prefix_disagreement" {
		route, err := mgr.ResolveModelRoute("stealth/ox-alpha")
		if err != nil || route.ProviderID != "anthropic" {
			t.Fatalf("configured prefix route = %+v, %v", route, err)
		}
	}
	if attack == "hook_changes_after_audit" {
		hookCalls := 0
		mgr.RoutingHooks().Register(func(decision *model.RoutingDecision) *model.RoutingDecision {
			hookCalls++
			if hookCalls >= 2 {
				decision.SelectedModel = "direct/model"
			}
			return decision
		})
	}
	dir := t.TempDir()
	license := readBuckleyLicenseForTest(t)
	if err := os.WriteFile(filepath.Join(dir, "LICENSE"), license, 0o644); err != nil {
		t.Fatalf("write test license: %v", err)
	}
	ev, err := evidence.New(filepath.Join(dir, "ev.db"), evidence.WithBlobRoot(filepath.Join(dir, "blobs")))
	if err != nil {
		t.Fatalf("evidence.New: %v", err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	if _, err := ledger.StartRun(context.Background(), runledger.AgentRun{RunID: "run-route", SessionID: "goal-test"}); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	engine, err := newGoalTurnEngine(cfg, mgr, tool.NewEmptyRegistry(), ledger, ev, dir, "goal-test")
	if err != nil {
		t.Fatalf("newGoalTurnEngine: %v", err)
	}
	contract, err := bindGoalModelPolicy(dir, "openrouter", goalloop.GoalModelRequest{
		Model: "stealth/ox-alpha", ReasoningEffort: "max", RetentionMode: retention,
		OpenRouterZDR: retention == goalloop.GoalRetentionZDR, OpenRouterDataCollection: "deny",
	})
	if err != nil {
		t.Fatalf("bindGoalModelPolicy: %v", err)
	}
	_, runErr := engine.RunTurn(context.Background(), goalloop.TaskContext{
		RunID: "run-route", TaskID: "task-route", TurnID: "turn-route",
		Goal: goalloop.Goal{Statement: "route attack", WorkspaceRoot: dir, ModelRequest: contract},
		Spec: goalloop.TaskSpec{Title: "route attack"},
	})
	return openRouterCalls, directCalls, runErr
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

func TestGoalTurnEngine_RepetitionWithoutStateChangeParks(t *testing.T) {
	t.Parallel()
	repeated := goalEngineToolCallResponse("missing_probe", `{}`)
	engine, _ := newGoalEngineUnderTest(t, []string{repeated, repeated, repeated, goalEngineTextResponse})

	outcome, err := engine.RunTurn(context.Background(), goalloop.TaskContext{
		RunID: "run-1", TaskID: "task-stalled", TurnID: "turn-stalled",
		Goal: goalloop.Goal{Statement: "inspect repository"}, Spec: goalloop.TaskSpec{Title: "inspect repository"},
	})
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if outcome.Blocker == nil {
		t.Fatal("repetition guard did not produce a governed park blocker")
	}
	if !strings.Contains(outcome.Blocker.Reason, "without changing workspace state") {
		t.Fatalf("blocker = %+v, want convergence reason", outcome.Blocker)
	}

	events, err := engine.ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: "run-1", TaskID: "task-stalled"})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	found := false
	for _, event := range events {
		if event.Type == runledger.EventControllerDecision && event.Payload["kind"] == "goal_convergence_policy" {
			found = event.Payload["action"] == "park" && event.Payload["reason_code"] == "exact_repeat_without_state_change"
		}
	}
	if !found {
		t.Fatalf("missing audited convergence park decision in %+v", events)
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

func TestGoalActionToolNames_RemovesDiscoveryWithoutChoosingMutation(t *testing.T) {
	registry := tool.NewEmptyRegistry()
	for _, candidate := range []*goalStateTestTool{
		{name: "exec_program", metadata: tool.ToolMetadata{Impact: tool.ImpactReadOnly}},
		{name: "read_file", metadata: tool.ToolMetadata{Impact: tool.ImpactReadOnly}},
		{name: "run_shell", metadata: tool.ToolMetadata{Impact: tool.ImpactDestructive}},
		{name: "edit_file", metadata: tool.ToolMetadata{Impact: tool.ImpactModifying}},
		{name: "write_file", metadata: tool.ToolMetadata{Impact: tool.ImpactModifying}},
		{name: "commit_changes", metadata: tool.ToolMetadata{Impact: tool.ImpactModifying}},
	} {
		registry.Register(candidate)
	}

	got := goalActionToolNames(registry, codeModeTools)
	gotSet := make(map[string]bool, len(got))
	for _, name := range got {
		gotSet[name] = true
	}
	for _, want := range []string{"edit_file", "write_file", "commit_changes"} {
		if !gotSet[want] {
			t.Fatalf("action tools %v missing %s", got, want)
		}
	}
	for _, forbidden := range []string{"exec_program", "read_file", "run_shell"} {
		if gotSet[forbidden] {
			t.Fatalf("action tools %v retain discovery tool %s", got, forbidden)
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

func TestGoalTurnEngine_ObservesActualWorkspaceChangeSeparatelyFromToolImpact(t *testing.T) {
	engine, _ := newGoalEngineUnderTest(t, []string{goalEngineTextResponse})
	runGoalEngineGit(t, engine.workDir, "init", "-q")
	runGoalEngineGit(t, engine.workDir, "config", "user.name", "Buckley Test")
	runGoalEngineGit(t, engine.workDir, "config", "user.email", "buckley@example.invalid")
	runGoalEngineGit(t, engine.workDir, "add", "LICENSE")
	runGoalEngineGit(t, engine.workDir, "commit", "-qm", "base")

	engine.registry.Register(&goalStateTestTool{
		name: "run_shell",
		metadata: tool.ToolMetadata{
			Impact: tool.ImpactDestructive,
		},
	})
	state := &goalTurnState{}
	noOp := engine.dispatchGoalTool(context.Background(), goalloop.TaskContext{Phase: goalloop.PhaseExecute},
		model.ToolCall{ID: "call-noop", Function: model.FunctionCall{Name: "run_shell", Arguments: `{}`}}, state)
	if !noOp.Success || !noOp.StateObserved || noOp.StateChanged || state.stateChanged {
		t.Fatalf("no-op destructive tool = %+v state=%+v, want observed without change", noOp, state)
	}

	engine.registry.Register(&goalStateTestTool{
		name: "write_file",
		metadata: tool.ToolMetadata{
			Impact: tool.ImpactModifying,
		},
		execute: func() error {
			return os.WriteFile(filepath.Join(engine.workDir, "changed.txt"), []byte("real change\n"), 0o644)
		},
	})
	changed := engine.dispatchGoalTool(context.Background(), goalloop.TaskContext{Phase: goalloop.PhaseExecute},
		model.ToolCall{ID: "call-change", Function: model.FunctionCall{Name: "write_file", Arguments: `{}`}}, state)
	if !changed.Success || !changed.StateObserved || !changed.StateChanged || !state.stateChanged {
		t.Fatalf("modifying tool = %+v state=%+v, want observed change", changed, state)
	}
}

func runGoalEngineGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
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

func TestGoalTurnSystemPrompt_ProjectsDurableGoalContract(t *testing.T) {
	t.Parallel()
	prompt := goalTurnSystemPrompt(goalloop.TaskContext{
		Goal: goalloop.Goal{
			Statement:          "build a playable world",
			AcceptanceCriteria: []string{"served route works", "shared criterion", "  "},
			Constraints:        []string{"no remote assets", " no publishing ", "no remote assets"},
		},
		Spec: goalloop.TaskSpec{
			Title:              "implement the bounded slice",
			AcceptanceCriteria: []string{"shared criterion", "focused tests pass"},
			Claims:             []string{"examples/gosx-docs", " game ", "examples/gosx-docs"},
		},
	}, false)

	for _, want := range []string{
		"Acceptance criteria:\n- served route works\n- shared criterion\n- focused tests pass\n",
		"Constraints:\n- no remote assets\n- no publishing\n",
		"Workspace claims:\n- examples/gosx-docs\n- game\n",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing durable contract section %q:\n%s", want, prompt)
		}
	}
	if got := strings.Count(prompt, "- shared criterion\n"); got != 1 {
		t.Fatalf("shared criterion rendered %d times, want once:\n%s", got, prompt)
	}
}

func TestGoalTurnSystemPrompt_ProjectsOrientationWithoutConstrainingDesign(t *testing.T) {
	t.Parallel()
	prompt := goalTurnSystemPrompt(goalloop.TaskContext{
		Goal: goalloop.Goal{Statement: "make it real"},
		Spec: goalloop.TaskSpec{Title: "build it"},
	}, false, "Workspace orientation (harness-observed evidence; sha256=abc):\n- game/runtime.go")
	for _, want := range []string{"sha256=abc", "game/runtime.go", "choose the design and implementation freely", "must converge on an actionable change"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestToolCallParams_RejectsDuplicateJSONFieldsWithRecovery(t *testing.T) {
	t.Parallel()
	call := model.ToolCall{Function: model.FunctionCall{
		Name:      "list_directory",
		Arguments: `{"path":"game","path":"scene"}`,
	}}
	params, err := toolCallParams(call)
	if err == nil || params != nil {
		t.Fatalf("toolCallParams = %#v, %v; want duplicate rejection", params, err)
	}
	for _, want := range []string{"duplicate JSON fields", "parallel tool calls", "exec_program"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

// TestIntersectToolNames locks the code-mode narrowing: an unfiltered
// base yields the narrow pool outright, and a phase-filtered base keeps
// only the overlap (verify keeps exec_program and run_shell, drops the
// editors and governed commit runtime).
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
	if allowed["edit_file"] || allowed["write_file"] || allowed["commit_changes"] {
		t.Fatalf("verify code-mode pool includes a mutating tool: %v", got)
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
