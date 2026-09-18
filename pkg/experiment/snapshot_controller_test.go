package experiment

import (
	"bytes"
	"context"
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
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

type snapshotMemoryTool struct {
	calls int
}

func (t *snapshotMemoryTool) Name() string { return "memory_echo" }

func (t *snapshotMemoryTool) Description() string { return "returns bounded in-memory evidence" }

func (t *snapshotMemoryTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (t *snapshotMemoryTool) Execute(params map[string]any) (*builtin.Result, error) {
	t.calls++
	return &builtin.Result{Success: true, Data: map[string]any{"evidence": "tool ok"}}, nil
}

func TestExperimentSnapshotControllerRetainedRunsRecompareOffline(t *testing.T) {
	toolRun := runSnapshotConversation(t, []string{
		`{"id":"resp-tool","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":null,"reasoning":"private-reasoning-sentinel","tool_calls":[{"id":"call_1","type":"function","function":{"name":"memory_echo","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		`{"id":"resp-tool-final","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"tool ok final answer","reasoning_details":[{"type":"reasoning.text","text":"private-reasoning-sentinel"}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":4,"total_tokens":12}}`,
	}, true, 0)
	truncatedRun := runSnapshotConversation(t, []string{
		`{"id":"resp-truncated","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"useful ok public draft","reasoning":"private-reasoning-sentinel"},"finish_reason":"length"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`,
	}, false, 0)
	capFailureRun := runSnapshotConversation(t, []string{
		`{"id":"resp-before-cap","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"memory_echo","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		`{"id":"resp-cap-partial","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"partial ok after tool","reasoning_details":[{"type":"reasoning.text","text":"private-reasoning-sentinel"}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":6,"total_tokens":15}}`,
	}, true, 20)

	exp := &Experiment{
		ID:   "exp-controller-snapshot",
		Name: "controller snapshot fixture",
		Task: Task{Prompt: "exercise controller", Timeout: time.Second},
		Variants: []Variant{
			{ID: "variant-tool", Name: "tool", ModelID: "gpt-4o", ProviderID: "openai"},
			{ID: "variant-truncated", Name: "truncated", ModelID: "gpt-4o", ProviderID: "openai"},
			{ID: "variant-failed", Name: "failed", ModelID: "gpt-4o", ProviderID: "openai"},
		},
		Criteria: []SuccessCriterion{
			{ID: 1, Name: "contains ok", Type: CriterionContains, Target: "ok", Weight: 1},
		},
	}
	now := time.Date(2026, 9, 5, 14, 0, 0, 0, time.UTC)
	runs := []Run{
		controllerSnapshotRun(t, exp, exp.Variants[0], "run-tool", RunCompleted, toolRun, now),
		controllerSnapshotRun(t, exp, exp.Variants[1], "run-truncated", RunFailed, truncatedRun, now.Add(time.Second)),
		controllerSnapshotRun(t, exp, exp.Variants[2], "run-cap-failure", RunFailed, capFailureRun, now.Add(2*time.Second)),
	}
	evals := map[string][]CriterionEvaluation{}
	for _, run := range runs {
		evaluated := EvaluateCriteria(context.Background(), t.TempDir(), "", run.Output, exp.Criteria)
		for i := range evaluated {
			evaluated[i].RunID = run.ID
		}
		evals[run.ID] = evaluated
	}

	snapshot, err := NewSnapshot(exp, runs, evals)
	if err != nil {
		t.Fatalf("NewSnapshot: %v", err)
	}
	var encoded bytes.Buffer
	if err := EncodeSnapshot(&encoded, snapshot); err != nil {
		t.Fatalf("EncodeSnapshot: %v", err)
	}
	if strings.Contains(encoded.String(), "private-reasoning-sentinel") {
		t.Fatal("snapshot contains private reasoning sentinel")
	}

	decoded, err := DecodeSnapshot(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("DecodeSnapshot: %v", err)
	}
	report, err := decoded.Compare()
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if toolRun.tool.calls != 1 || capFailureRun.tool.calls != 1 {
		t.Fatalf("tool calls changed after offline compare: tool=%d cap=%d", toolRun.tool.calls, capFailureRun.tool.calls)
	}
	if len(toolRun.requestBodies) < 2 || !strings.Contains(toolRun.requestBodies[1], "tool ok") {
		t.Fatalf("second tool run request did not carry tool evidence: %s", toolRun.requestBodies)
	}
	if len(capFailureRun.requestBodies) < 2 || !strings.Contains(capFailureRun.requestBodies[1], "tool ok") {
		t.Fatalf("second cap failure request did not carry tool evidence: %s", capFailureRun.requestBodies)
	}
	if len(report.Variants) != 3 {
		t.Fatalf("variants = %d, want 3", len(report.Variants))
	}
	byRun := map[string]VariantReport{}
	for _, variant := range report.Variants {
		byRun[variant.RunID] = variant
	}
	if !byRun["run-tool"].Verified || !byRun["run-tool"].RankEligible {
		t.Fatalf("tool run report = %+v, want verified retained evidence", byRun["run-tool"])
	}
	if byRun["run-truncated"].Verified || byRun["run-truncated"].VerificationStatus == "verified" {
		t.Fatalf("truncated run report = %+v, want explicitly unverified partial evidence", byRun["run-truncated"])
	}
	if byRun["run-truncated"].Status != RunFailed || !strings.Contains(decoded.Runs[1].Output, "useful ok public draft") {
		t.Fatalf("truncated run lost public draft/status: run=%+v report=%+v", decoded.Runs[1], byRun["run-truncated"])
	}
	if byRun["run-cap-failure"].Verified || byRun["run-cap-failure"].VerificationStatus == "verified" {
		t.Fatalf("cap failure run report = %+v, want explicitly unverified partial evidence", byRun["run-cap-failure"])
	}
	if byRun["run-cap-failure"].Status != RunFailed || !strings.Contains(decoded.Runs[2].Output, "partial ok after tool") || decoded.Runs[2].Metrics.ToolCalls != 1 {
		t.Fatalf("cap failure run lost public/tool evidence: run=%+v report=%+v", decoded.Runs[2], byRun["run-cap-failure"])
	}
	if decoded.Runs[0].Metrics.PromptTokens != 18 || decoded.Runs[0].Metrics.CompletionTokens != 9 || decoded.Runs[0].Metrics.ToolCalls != 1 {
		t.Fatalf("tool run metrics = %+v, want exact retained usage/tool evidence", decoded.Runs[0].Metrics)
	}
	if len(decoded.Runs[0].ModelExecutions) != 2 || decoded.Runs[0].ModelExecutions[1].ResponseID != "resp-tool-final" {
		t.Fatalf("tool run model executions = %+v, want retained response identities", decoded.Runs[0].ModelExecutions)
	}
}

type snapshotConversationResult struct {
	conversation  runConversationResult
	err           error
	tool          *snapshotMemoryTool
	requestBodies []string
}

func runSnapshotConversation(t *testing.T, responses []string, withTool bool, maxTokens int) snapshotConversationResult {
	t.Helper()
	var requests atomic.Int32
	var bodiesMu sync.Mutex
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read request body", http.StatusInternalServerError)
			return
		}
		bodiesMu.Lock()
		bodies = append(bodies, string(body))
		bodiesMu.Unlock()
		request := int(requests.Add(1))
		if request > len(responses) {
			http.Error(w, fmt.Sprintf("unexpected model request %d", request), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, responses[request-1])
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	cfg.Experiment.MaxCostPerRun = 0
	cfg.Experiment.MaxTokensPerRun = maxTokens
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	executor := &experimentExecutor{config: cfg, modelManager: mgr}
	registry := tool.NewEmptyRegistry()
	memoryTool := &snapshotMemoryTool{}
	if withTool {
		registry.Register(memoryTool)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conversation, runErr := executor.runConversation(ctx, "gpt-4o", registry, "produce retained public evidence", "", "", "")
	if got := int(requests.Load()); got != len(responses) {
		t.Fatalf("requests = %d, want %d", got, len(responses))
	}
	bodiesMu.Lock()
	copiedBodies := append([]string(nil), bodies...)
	bodiesMu.Unlock()
	return snapshotConversationResult{conversation: conversation, err: runErr, tool: memoryTool, requestBodies: copiedBodies}
}

func controllerSnapshotRun(t *testing.T, exp *Experiment, variant Variant, runID string, status RunStatus, result snapshotConversationResult, started time.Time) Run {
	t.Helper()
	if status == RunCompleted && result.err != nil {
		t.Fatalf("%s unexpected error: %v", runID, result.err)
	}
	if status == RunFailed && result.err == nil {
		t.Fatalf("%s expected retained incomplete failure", runID)
	}
	manifest, err := buildRunInputManifest(exp, variant, exp.Task.Timeout)
	if err != nil {
		t.Fatalf("buildRunInputManifest: %v", err)
	}
	completed := started.Add(time.Second)
	errorText := ""
	if result.err != nil {
		errorText = result.err.Error()
	}
	var errorPtr *string
	if errorText != "" {
		errorPtr = &errorText
	}
	return Run{
		ID:           runID,
		ExperimentID: exp.ID,
		VariantID:    variant.ID,
		Status:       status,
		Output:       result.conversation.output,
		Metrics: RunMetrics{
			DurationMs:       1000,
			PromptTokens:     result.conversation.metrics.promptTokens,
			CompletionTokens: result.conversation.metrics.completionTokens,
			ToolCalls:        result.conversation.metrics.toolCalls,
			ToolSuccesses:    result.conversation.metrics.toolSuccesses,
			ToolFailures:     result.conversation.metrics.toolFailures,
		},
		Error:           errorPtr,
		StartedAt:       started,
		CompletedAt:     &completed,
		InputManifest:   manifest,
		ModelExecutions: cloneModelExecutions(result.conversation.modelExecutions),
	}
}
