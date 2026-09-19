package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/rlm"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type recordingPlanStore struct {
	mu    sync.Mutex
	saves int
	plan  *orchestrator.Plan
}

func (s *recordingPlanStore) SavePlan(plan *orchestrator.Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	s.plan = plan
	return nil
}

func (s *recordingPlanStore) LoadPlan(string) (*orchestrator.Plan, error) { return s.plan, nil }
func (s *recordingPlanStore) ListPlans() ([]orchestrator.Plan, error)     { return nil, nil }
func (s *recordingPlanStore) ReadLog(string, string, int) ([]string, string, error) {
	return nil, "", nil
}

func newRunnerTaskManager(t *testing.T, server *httptest.Server) *model.Manager {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return mgr
}

func runnerChatResponse(id, content, finish string, prompt, completion int) string {
	return fmt.Sprintf(`{
		"id":%q,"model":"gpt-4o",
		"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":%q}],
		"usage":{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d}
	}`, id, content, finish, prompt, completion, prompt+completion)
}

func runnerSetAnswerResponse(content string) string {
	args, _ := json.Marshal(map[string]any{"content": content, "ready": true, "confidence": 0.99})
	argString, _ := json.Marshal(string(args))
	return fmt.Sprintf(`{
		"id":"chatcmpl-forged","model":"gpt-4o",
		"choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call_set_answer","type":"function","function":{"name":"set_answer","arguments":%s}}]},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
	}`, string(argString))
}

func newRunnerWithHTTP(t *testing.T, handler http.HandlerFunc, store *recordingPlanStore) *Runner {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg := config.DefaultConfig()
	r := New(nil, newRunnerTaskManager(t, server), tool.NewEmptyRegistry(), cfg, nil, store)
	installRunnerTestRuntime(t, r, cfg)
	return r
}

func installRunnerTestRuntime(t *testing.T, r *Runner, cfg *config.Config) {
	t.Helper()
	rlmCfg := runnerRLMConfig(cfg)
	clearRunnerTestTierCostCaps(&rlmCfg)
	rt, err := rlm.NewRuntime(rlmCfg, rlm.RuntimeDeps{
		Models:    r.models,
		Registry:  r.registry,
		Store:     r.store,
		Telemetry: r.telemetry,
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	r.runtime = rt
}

func clearRunnerTestTierCostCaps(cfg *rlm.Config) {
	if cfg == nil {
		return
	}
	if cfg.Tiers == nil {
		cfg.Tiers = rlm.DefaultTiers()
	}
	for weight, tier := range cfg.Tiers {
		tier.MaxCostPerMillion = 0
		cfg.Tiers[weight] = tier
	}
}

func TestRunnerHostBoundBatchAppliesRulesEngineSpawningPolicy(t *testing.T) {
	for _, tc := range []struct {
		name          string
		engine        *rules.Engine
		wantWriteTool bool
	}{
		{name: "without engine preserves unfiltered tools", wantWriteTool: true},
		{name: "with engine restricts review worker to read-only tools", engine: newRunnerPolicyEngine(t), wantWriteTool: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var request string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Method == http.MethodGet && req.URL.Path == "/models" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"data":[{"id":"policy-model","context_length":128000,"pricing":{"prompt":"0.000001","completion":"0.000002"},"supported_parameters":["tools"]}]}`)
					return
				}
				body, _ := io.ReadAll(req.Body)
				request = string(body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, runnerChatResponse("chatcmpl-policy", "review complete", "stop", 3, 2))
			}))
			t.Cleanup(server.Close)

			registry := tool.NewEmptyRegistry()
			registry.Register(runnerPolicyTool{name: "read_file"})
			registry.Register(runnerPolicyTool{name: "write_file"})
			planStore := &recordingPlanStore{}
			runner := New(nil, newRunnerPolicyManager(t, server), registry, runnerPolicyConfig(), nil, planStore, WithRulesEngine(tc.engine))
			if runner.runtime == nil {
				t.Fatal("runner runtime was not initialized")
			}

			plan := &orchestrator.Plan{ID: "policy-plan", Tasks: []orchestrator.Task{{
				ID:          "review-task",
				Title:       "Review",
				Description: "review the implementation without changing files",
				Type:        orchestrator.TaskTypeAnalysis,
				Status:      orchestrator.TaskPending,
			}}}
			if err := runner.executeTaskBatch(plan, plan.Tasks); err != nil {
				t.Fatalf("executeTaskBatch: %v", err)
			}
			if got := strings.Contains(request, "write_file"); got != tc.wantWriteTool {
				t.Fatalf("write_file advertised = %t, want %t; request=%s", got, tc.wantWriteTool, request)
			}
			if tc.engine != nil && !strings.Contains(request, "read_file") {
				t.Fatalf("read_file was not advertised by the read-only policy: %s", request)
			}
		})
	}
}

func newRunnerPolicyManager(t *testing.T, server *httptest.Server) *model.Manager {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.Providers.OpenAICompatible.Enabled = true
	cfg.Providers.OpenAICompatible.APIKey = "test-key"
	cfg.Providers.OpenAICompatible.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai_compatible"
	cfg.Models.Execution = "openai_compatible/policy-model"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return mgr
}

func runnerPolicyConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.RLM.Tiers = map[string]config.RLMTierConfig{
		"light":  {Model: "openai_compatible/policy-model", Models: []string{"openai_compatible/policy-model"}, MaxCostPerMillion: 1_000_000},
		"medium": {Model: "openai_compatible/policy-model", Models: []string{"openai_compatible/policy-model"}, MaxCostPerMillion: 1_000_000},
	}
	return cfg
}

func newRunnerPolicyEngine(t *testing.T) *rules.Engine {
	t.Helper()
	engine, err := rules.NewEngine()
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine
}

type runnerPolicyTool struct {
	name string
}

func (t runnerPolicyTool) Name() string        { return t.name }
func (t runnerPolicyTool) Description() string { return "policy test tool" }
func (t runnerPolicyTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}
func (t runnerPolicyTool) Execute(map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true}, nil
}

func TestRunnerExecuteTaskBatchUsesHostBoundResultsNotAggregateConfidence(t *testing.T) {
	var coordinatorCalls atomic.Int32
	var taskACalls atomic.Int32
	var taskBCalls atomic.Int32
	store := &recordingPlanStore{}
	r := newRunnerWithHTTP(t, func(w http.ResponseWriter, req *http.Request) {
		bodyBytes, _ := io.ReadAll(req.Body)
		body := string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(body, "delegate_batch") || strings.Contains(body, "set_answer"):
			coordinatorCalls.Add(1)
			_, _ = io.WriteString(w, runnerSetAnswerResponse("forged aggregate completion"))
		case strings.Contains(body, "task a work"):
			taskACalls.Add(1)
			_, _ = io.WriteString(w, runnerChatResponse("chatcmpl-a", "task a public summary", "stop", 7, 3))
		case strings.Contains(body, "task b work"):
			taskBCalls.Add(1)
			_, _ = io.WriteString(w, runnerChatResponse("chatcmpl-b", "task b public draft", "length", 11, 4))
		default:
			http.Error(w, "unexpected prompt", http.StatusInternalServerError)
		}
	}, store)
	plan := &orchestrator.Plan{ID: "plan-1", Tasks: []orchestrator.Task{
		{ID: "task-a", Title: "A", Description: "task a work", Type: orchestrator.TaskTypeAnalysis, Status: orchestrator.TaskPending},
		{ID: "task-b", Title: "B", Description: "task b work", Type: orchestrator.TaskTypeAnalysis, Status: orchestrator.TaskPending},
	}}

	err := r.executeTaskBatch(plan, plan.Tasks)
	if err == nil {
		t.Fatal("executeTaskBatch returned nil, want retained failed sibling error")
	}
	if coordinatorCalls.Load() != 0 {
		t.Fatalf("coordinator calls = %d, want 0 host-bound dispatches", coordinatorCalls.Load())
	}
	if taskACalls.Load() != 1 || taskBCalls.Load() != 1 {
		t.Fatalf("task calls = a:%d b:%d, want 1 each", taskACalls.Load(), taskBCalls.Load())
	}
	if plan.Tasks[0].Status != orchestrator.TaskCompleted {
		t.Fatalf("task A status = %v, want completed", plan.Tasks[0].Status)
	}
	if plan.Tasks[1].Status != orchestrator.TaskFailed {
		t.Fatalf("task B status = %v, want failed", plan.Tasks[1].Status)
	}
	if len(plan.Tasks[0].ExecutionRecords) != 1 || plan.Tasks[0].ExecutionRecords[0].VerificationStatus != "not_requested" {
		t.Fatalf("task A record = %#v", plan.Tasks[0].ExecutionRecords)
	}
	if len(plan.Tasks[1].ExecutionRecords) != 1 || !strings.Contains(plan.Tasks[1].ExecutionRecords[0].Summary, "task b public draft") {
		t.Fatalf("task B record = %#v", plan.Tasks[1].ExecutionRecords)
	}
	if store.saves == 0 {
		t.Fatal("plan was not saved after mixed sibling results")
	}
}

func TestRunnerExecutePlanProcessesDependencyReadyWaves(t *testing.T) {
	var calls sync.Map
	store := &recordingPlanStore{}
	r := newRunnerWithHTTP(t, func(w http.ResponseWriter, req *http.Request) {
		bodyBytes, _ := io.ReadAll(req.Body)
		body := string(bodyBytes)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(body, "task a fails"):
			calls.Store("a", true)
			_, _ = io.WriteString(w, runnerChatResponse("chatcmpl-a", "task a draft", "length", 3, 2))
		case strings.Contains(body, "task b succeeds"):
			calls.Store("b", true)
			_, _ = io.WriteString(w, runnerChatResponse("chatcmpl-b", "task b summary", "stop", 4, 2))
		case strings.Contains(body, "task c succeeds"):
			calls.Store("c", true)
			_, _ = io.WriteString(w, runnerChatResponse("chatcmpl-c", "task c summary", "stop", 5, 2))
		default:
			http.Error(w, "unexpected prompt", http.StatusInternalServerError)
		}
	}, store)
	plan := &orchestrator.Plan{ID: "plan-waves", Tasks: []orchestrator.Task{
		{ID: "task-c", Title: "C", Description: "task c succeeds", Type: orchestrator.TaskTypeAnalysis, Dependencies: []string{"task-b"}, Status: orchestrator.TaskPending},
		{ID: "task-a", Title: "A", Description: "task a fails", Type: orchestrator.TaskTypeAnalysis, Status: orchestrator.TaskPending},
		{ID: "task-b", Title: "B", Description: "task b succeeds", Type: orchestrator.TaskTypeAnalysis, Status: orchestrator.TaskPending},
	}}
	r.currentPlan = plan

	err := r.ExecutePlan()
	if err == nil {
		t.Fatal("ExecutePlan returned nil, want failed sibling error")
	}
	if plan.Tasks[0].Status != orchestrator.TaskCompleted {
		t.Fatalf("task C status = %v, want completed after task B wave", plan.Tasks[0].Status)
	}
	if plan.Tasks[1].Status != orchestrator.TaskFailed {
		t.Fatalf("task A status = %v, want failed", plan.Tasks[1].Status)
	}
	if plan.Tasks[2].Status != orchestrator.TaskCompleted {
		t.Fatalf("task B status = %v, want completed", plan.Tasks[2].Status)
	}
	for _, key := range []string{"a", "b", "c"} {
		if _, ok := calls.Load(key); !ok {
			t.Fatalf("task %s was not executed", key)
		}
	}
	if store.saves == 0 {
		t.Fatal("plan was not saved after ExecutePlan error")
	}
}

func TestRunnerExecutePlanDoesNotLaunchBlockedMissingDependency(t *testing.T) {
	var calls atomic.Int32
	store := &recordingPlanStore{}
	r := newRunnerWithHTTP(t, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		http.Error(w, "should not dispatch", http.StatusInternalServerError)
	}, store)
	plan := &orchestrator.Plan{ID: "plan-missing", Tasks: []orchestrator.Task{
		{ID: "task-blocked", Title: "Blocked", Description: "needs absent dependency", Type: orchestrator.TaskTypeAnalysis, Dependencies: []string{"missing"}, Status: orchestrator.TaskPending},
	}}
	r.currentPlan = plan

	err := r.ExecutePlan()
	if err == nil {
		t.Fatal("ExecutePlan returned nil, want missing dependency error")
	}
	if calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0", calls.Load())
	}
	if plan.Tasks[0].Status != orchestrator.TaskFailed {
		t.Fatalf("task status = %v, want failed", plan.Tasks[0].Status)
	}
	if len(plan.Tasks[0].ExecutionRecords) != 0 {
		t.Fatalf("blocked task records = %#v, want none because no execution occurred", plan.Tasks[0].ExecutionRecords)
	}
}

func TestRunnerExecutePlanReportsExistingIncompleteStateAfterSuccessfulWave(t *testing.T) {
	var calls atomic.Int32
	store := &recordingPlanStore{}
	r := newRunnerWithHTTP(t, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, runnerChatResponse("chatcmpl-new", "new task summary", "stop", 3, 2))
	}, store)
	plan := &orchestrator.Plan{ID: "plan-existing", Tasks: []orchestrator.Task{
		{ID: "task-old-failed", Title: "Old failed", Status: orchestrator.TaskFailed},
		{ID: "task-old-running", Title: "Old running", Status: orchestrator.TaskInProgress},
		{ID: "task-old-skipped", Title: "Old skipped", Status: orchestrator.TaskSkipped},
		{ID: "task-new", Title: "New", Description: "new pending work", Type: orchestrator.TaskTypeAnalysis, Status: orchestrator.TaskPending},
	}}
	r.currentPlan = plan

	err := r.ExecutePlan()
	if err == nil {
		t.Fatal("ExecutePlan returned nil, want honest incomplete plan error")
	}
	errText := err.Error()
	for _, want := range []string{"task task-old-failed already failed", "task task-old-running already in progress", "task task-old-skipped skipped"} {
		if !strings.Contains(errText, want) {
			t.Fatalf("error %q missing %q", errText, want)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want one new pending task only", calls.Load())
	}
	if plan.Tasks[3].Status != orchestrator.TaskCompleted {
		t.Fatalf("new task status = %v, want completed", plan.Tasks[3].Status)
	}
	if len(plan.Tasks[0].ExecutionRecords) != 0 || len(plan.Tasks[1].ExecutionRecords) != 0 || len(plan.Tasks[2].ExecutionRecords) != 0 {
		t.Fatalf("old incomplete tasks gained records: failed=%#v running=%#v skipped=%#v", plan.Tasks[0].ExecutionRecords, plan.Tasks[1].ExecutionRecords, plan.Tasks[2].ExecutionRecords)
	}
	if store.saves == 0 {
		t.Fatal("plan was not saved after terminal incomplete state")
	}
}

func TestRunnerExecuteTaskRequiresDependenciesCompleted(t *testing.T) {
	var calls atomic.Int32
	store := &recordingPlanStore{}
	r := newRunnerWithHTTP(t, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		http.Error(w, "should not dispatch", http.StatusInternalServerError)
	}, store)
	plan := &orchestrator.Plan{ID: "plan-single-dep", Tasks: []orchestrator.Task{
		{ID: "task-dep", Title: "Dep", Status: orchestrator.TaskFailed},
		{ID: "task-target", Title: "Target", Description: "blocked target", Type: orchestrator.TaskTypeAnalysis, Dependencies: []string{"task-dep"}, Status: orchestrator.TaskPending},
	}}
	r.currentPlan = plan

	err := r.ExecuteTask("task-target")
	if err == nil {
		t.Fatal("ExecuteTask returned nil, want dependency error")
	}
	if !strings.Contains(err.Error(), "dependencies are not completed") {
		t.Fatalf("error = %v, want dependency diagnostic", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("provider calls = %d, want 0", calls.Load())
	}
	if plan.Tasks[1].Status != orchestrator.TaskPending {
		t.Fatalf("blocked task status = %v, want still pending/no replay", plan.Tasks[1].Status)
	}
}

func TestRunnerStructuredVerificationRequiresHostEvidence(t *testing.T) {
	repo := initRunnerVerificationRepo(t)
	verificationTool := &fakeTaskVerificationTool{result: &builtin.Result{
		Success: true,
		Data:    map[string]any{"status": "PASS"},
	}}
	store := &recordingPlanStore{}
	r := newRunnerWithHTTP(t, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, runnerChatResponse("chatcmpl-ok", "verified summary", "stop", 9, 3))
	}, store)
	r.verificationHost = taskVerificationHost{makeTool: func(string, string, time.Duration) (taskVerificationTool, func(), error) {
		return verificationTool, nil, nil
	}}
	plan := &orchestrator.Plan{ID: "plan-check", Context: orchestrator.PlanContext{RepoRoot: repo}, Tasks: []orchestrator.Task{{
		ID:          "task-check",
		Title:       "Checked",
		Description: "run checked work",
		Type:        orchestrator.TaskTypeValidation,
		Status:      orchestrator.TaskPending,
		VerificationChecks: []orchestrator.TaskVerificationCheck{{
			ID:       "go-tests",
			Kind:     "test",
			Language: "go",
			Path:     "./pkg/rlm",
			Pattern:  "^TestExample$",
		}},
	}}}

	err := r.executeSingleTask(plan, &plan.Tasks[0])
	if err != nil {
		t.Fatalf("executeSingleTask: %v", err)
	}
	if plan.Tasks[0].Status != orchestrator.TaskCompleted {
		t.Fatalf("status = %v, want completed", plan.Tasks[0].Status)
	}
	records := plan.Tasks[0].ExecutionRecords
	if len(records) != 1 {
		t.Fatalf("records length = %d, want 1", len(records))
	}
	if records[0].VerificationStatus != "pass" || len(records[0].VerificationResults) != 1 {
		t.Fatalf("verification record = %#v", records[0])
	}
	if records[0].VerificationResults[0].Status != "PASS" || records[0].VerificationResults[0].EvidenceID == "" {
		t.Fatalf("verification result = %#v", records[0].VerificationResults[0])
	}
	if !strings.Contains(string(records[0].RuntimeResult), "verified summary") {
		t.Fatalf("runtime result JSON missing public summary: %s", records[0].RuntimeResult)
	}
}

func TestRunnerMixedLegacyAndStructuredVerificationRemainsUnverifiedWithEvidence(t *testing.T) {
	repo := initRunnerVerificationRepo(t)
	verificationTool := &fakeTaskVerificationTool{result: &builtin.Result{
		Success: true,
		Data:    map[string]any{"status": "PASS"},
	}}
	store := &recordingPlanStore{}
	r := newRunnerWithHTTP(t, func(w http.ResponseWriter, req *http.Request) {
		bodyBytes, _ := io.ReadAll(req.Body)
		if !strings.Contains(string(bodyBytes), "Structured Verification Checks") {
			t.Fatalf("worker prompt omitted structured check definitions: %s", bodyBytes)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, runnerChatResponse("chatcmpl-mixed", "mixed summary", "stop", 6, 3))
	}, store)
	r.verificationHost = taskVerificationHost{makeTool: func(string, string, time.Duration) (taskVerificationTool, func(), error) {
		return verificationTool, nil, nil
	}}
	plan := &orchestrator.Plan{ID: "plan-mixed", Context: orchestrator.PlanContext{RepoRoot: repo}, Tasks: []orchestrator.Task{{
		ID:           "task-mixed",
		Title:        "Mixed",
		Description:  "mixed verification work",
		Type:         orchestrator.TaskTypeValidation,
		Status:       orchestrator.TaskPending,
		Verification: []string{"also inspect manually"},
		VerificationChecks: []orchestrator.TaskVerificationCheck{{
			ID: "go-tests", Kind: "test", Language: "go", Path: ".",
		}},
	}}}

	err := r.executeSingleTask(plan, &plan.Tasks[0])
	if err == nil {
		t.Fatal("executeSingleTask returned nil, want legacy prose unverified error")
	}
	if plan.Tasks[0].Status != orchestrator.TaskFailed {
		t.Fatalf("status = %v, want failed", plan.Tasks[0].Status)
	}
	records := plan.Tasks[0].ExecutionRecords
	if len(records) != 1 {
		t.Fatalf("records length = %d, want 1", len(records))
	}
	if records[0].VerificationStatus != "unverified" || len(records[0].VerificationResults) != 1 {
		t.Fatalf("record = %#v, want unverified with retained structured result", records[0])
	}
}

func TestRunnerLegacyVerificationRemainsUnverified(t *testing.T) {
	store := &recordingPlanStore{}
	r := newRunnerWithHTTP(t, func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, runnerChatResponse("chatcmpl-legacy", "legacy summary", "stop", 5, 3))
	}, store)
	plan := &orchestrator.Plan{ID: "plan-legacy", Tasks: []orchestrator.Task{{
		ID:           "task-legacy",
		Title:        "Legacy",
		Description:  "legacy verified work",
		Type:         orchestrator.TaskTypeValidation,
		Status:       orchestrator.TaskPending,
		Verification: []string{"run the tests"},
	}}}

	err := r.executeSingleTask(plan, &plan.Tasks[0])
	if err == nil {
		t.Fatal("executeSingleTask returned nil, want legacy verification unverified error")
	}
	if plan.Tasks[0].Status != orchestrator.TaskFailed {
		t.Fatalf("status = %v, want failed", plan.Tasks[0].Status)
	}
	records := plan.Tasks[0].ExecutionRecords
	if len(records) != 1 {
		t.Fatalf("records length = %d, want 1", len(records))
	}
	if records[0].ExecutionStatus != "completed" || records[0].VerificationStatus != "unverified" {
		t.Fatalf("record = %#v", records[0])
	}
}

func TestRunnerApplyTaskResultsFailClosedForMissingDuplicateUnknownIDs(t *testing.T) {
	r := &Runner{}
	plan := &orchestrator.Plan{ID: "plan-ambiguous", Tasks: []orchestrator.Task{
		{
			ID:     "task-a",
			Title:  "A",
			Status: orchestrator.TaskPending,
			ExecutionRecords: []orchestrator.TaskExecutionRecord{{
				Schema:             orchestrator.TaskExecutionRecordSchemaV1,
				TaskID:             "task-a",
				ExecutionStatus:    "completed",
				VerificationStatus: "not_requested",
				Summary:            "prior retained work",
			}},
		},
		{ID: "task-b", Title: "B", Status: orchestrator.TaskPending},
	}}

	err := r.applyTaskResults(context.Background(), plan, plan.Tasks, []rlm.BatchResult{
		{TaskID: "task-a", Summary: "first row"},
		{TaskID: "task-a", Summary: "duplicate row"},
		{TaskID: "unknown", Summary: "intruder row"},
	})
	if err == nil {
		t.Fatal("applyTaskResults returned nil, want ambiguity errors")
	}
	errText := err.Error()
	for _, want := range []string{"unexpected task result id: unknown", "task task-a returned duplicate execution evidence", "task task-b did not return execution evidence"} {
		if !strings.Contains(errText, want) {
			t.Fatalf("error %q missing %q", errText, want)
		}
	}
	if plan.Tasks[0].Status != orchestrator.TaskFailed || plan.Tasks[1].Status != orchestrator.TaskFailed {
		t.Fatalf("statuses = %v/%v, want both failed", plan.Tasks[0].Status, plan.Tasks[1].Status)
	}
	if len(plan.Tasks[0].ExecutionRecords) != 3 {
		t.Fatalf("task A records length = %d, want prior + two duplicate rows", len(plan.Tasks[0].ExecutionRecords))
	}
	if plan.Tasks[0].ExecutionRecords[0].Summary != "prior retained work" {
		t.Fatalf("prior record was overwritten: %#v", plan.Tasks[0].ExecutionRecords)
	}
	if len(plan.Tasks[1].ExecutionRecords) != 1 || plan.Tasks[1].ExecutionRecords[0].Error != "execution evidence missing" {
		t.Fatalf("task B record = %#v", plan.Tasks[1].ExecutionRecords)
	}
}
