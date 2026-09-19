package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/agentspec"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/subagent"
)

func TestApplyAgentRunChildContract_NarrowsAndPinsResolvedFields(t *testing.T) {
	profile := &agentspec.RuntimeProfile{Spec: &agentspec.Spec{
		Models:   agentspec.ModelSpec{Execution: "project/model"},
		Policies: agentspec.PolicySpec{MaxToolCalls: 4},
		Instructions: agentspec.InstructionSpec{
			Prompt: "Keep changes focused.",
		},
		Tools: agentspec.ToolSpec{Tier: "standard", Allow: []string{"read_file", "write_file"}},
	}}
	contract := subagent.ChildContract{
		Budget:           agentcoord.Budget{MaxToolCalls: 19},
		SchemaVersion:    "buckley.subagent-contract/v1",
		Model:            "child/model",
		Tier:             "execute",
		Effort:           "high",
		SystemPrompt:     "Follow the child review protocol.",
		AllowedTools:     []string{"read_file", "search_text"},
		ToolsConstrained: true,
		StepCap:          4,
		ApprovalPosture:  "safe",
		OutputSchema:     "buckley.artifact/v1",
	}
	if err := applyAgentRunChildContract(profile, contract); err != nil {
		t.Fatalf("applyAgentRunChildContract: %v", err)
	}
	spec := profile.Spec
	if spec.Models.Execution != "child/model" || spec.Models.Chat != "child/model" {
		t.Fatalf("models = %+v", spec.Models)
	}
	if got := strings.Join(spec.Tools.Allow, ","); got != "read_file" {
		t.Fatalf("allowlist = %q, want read_file", got)
	}
	if spec.Tools.Tier != "standard" || spec.Policies.ApprovalMode != "safe" {
		t.Fatalf("policy = %+v tools = %+v", spec.Policies, spec.Tools)
	}
	if spec.Policies.MaxToolCalls != 4 {
		t.Fatalf("resolved MaxToolCalls = %d, want 4 (profile cap must not be loosened by child budget 19)", spec.Policies.MaxToolCalls)
	}
	for key, want := range map[string]string{
		"buckley.resolved_tier":    "execute",
		"buckley.reasoning_effort": "high",
		"buckley.step_cap":         "4",
		"buckley.output_schema":    "buckley.artifact/v1",
	} {
		if got := spec.Metadata[key]; got != want {
			t.Fatalf("metadata[%q] = %q, want %q", key, got, want)
		}
	}
	for _, want := range []string{"Keep changes focused.", "Follow the child review protocol."} {
		if !strings.Contains(spec.Instructions.Prompt, want) {
			t.Fatalf("instructions missing %q:\n%s", want, spec.Instructions.Prompt)
		}
	}
}

func TestACPLoopLimitsFromChildContract_PropagatesLineageAndBudgets(t *testing.T) {
	limits, err := acpLoopLimitsFromChildContract(subagent.ChildContract{
		RunID:           " run-child ",
		ParentRunID:     " run-parent ",
		ParentSessionID: " session-parent ",
		TaskID:          " task-child ",
		StepCap:         11,
		TimeoutSeconds:  80,
		Budget: agentcoord.Budget{
			MaxToolCalls:     19,
			MaxModelRequests: 13,
			MaxElapsedSecond: 50,
			MaxCostUSD:       2.5,
		},
	})
	if err != nil {
		t.Fatalf("acpLoopLimitsFromChildContract: %v", err)
	}
	if !limits.ChildContract || limits.RunID != "run-child" || limits.ParentRunID != "run-parent" || limits.ParentSessionID != "session-parent" || limits.TaskID != "task-child" {
		t.Fatalf("limits lineage = %+v", limits)
	}
	if limits.StepCap != 11 || limits.MaxToolCalls != 19 || limits.MaxModelRequests != 13 || limits.MaxElapsedSeconds != 50 || limits.MaxCostUSD != 2.5 {
		t.Fatalf("limits budget = %+v", limits)
	}

	unbounded, err := acpLoopLimitsFromChildContract(subagent.ChildContract{})
	if err != nil {
		t.Fatalf("unbounded acpLoopLimitsFromChildContract: %v", err)
	}
	if !unbounded.ChildContract || unbounded.StepCap != 0 || unbounded.MaxToolCalls != 0 || unbounded.MaxModelRequests != 0 || unbounded.MaxElapsedSeconds != 0 || unbounded.MaxCostUSD != 0 {
		t.Fatalf("zero contract gained limits: %+v", unbounded)
	}
}

func TestACPLoopLimitsFromChildContract_RejectsNonFiniteCost(t *testing.T) {
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.01} {
		_, err := acpLoopLimitsFromChildContract(subagent.ChildContract{
			Budget: agentcoord.Budget{MaxCostUSD: value},
		})
		if err == nil || !strings.Contains(err.Error(), "finite and non-negative") {
			t.Fatalf("acpLoopLimitsFromChildContract(%v) error = %v", value, err)
		}
	}
}
func TestResolveAgentRunLoopLimits_ToolBudget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		profileMax    int
		contract      *subagent.ChildContract
		wantMaxTool   int
		wantChild     bool
		wantRunID     string
		wantModelReqs int
	}{
		{name: "no contract profile cap applies", profileMax: 7, wantMaxTool: 7, wantChild: false},
		{
			name:          "child narrows to profile cap",
			profileMax:    4,
			contract:      &subagent.ChildContract{RunID: "run-child", Budget: agentcoord.Budget{MaxToolCalls: 19, MaxModelRequests: 13}},
			wantMaxTool:   4,
			wantChild:     true,
			wantRunID:     "run-child",
			wantModelReqs: 13,
		},
		{
			name:          "child keeps budget when profile uncapped",
			profileMax:    0,
			contract:      &subagent.ChildContract{RunID: "run-child", Budget: agentcoord.Budget{MaxToolCalls: 19, MaxModelRequests: 13}},
			wantMaxTool:   19,
			wantChild:     true,
			wantRunID:     "run-child",
			wantModelReqs: 13,
		},
		{
			name:          "zero child budget keeps profile cap",
			profileMax:    4,
			contract:      &subagent.ChildContract{RunID: "run-child", Budget: agentcoord.Budget{MaxModelRequests: 13}},
			wantMaxTool:   4,
			wantChild:     true,
			wantRunID:     "run-child",
			wantModelReqs: 13,
		},
		{
			name:          "zero child and zero profile stays unbounded",
			profileMax:    0,
			contract:      &subagent.ChildContract{RunID: "run-child", Budget: agentcoord.Budget{MaxModelRequests: 13}},
			wantMaxTool:   0,
			wantChild:     true,
			wantRunID:     "run-child",
			wantModelReqs: 13,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			profile := &agentspec.RuntimeProfile{Spec: &agentspec.Spec{
				Policies: agentspec.PolicySpec{MaxToolCalls: tt.profileMax},
			}}
			contract := subagent.ChildContract{}
			if tt.contract != nil {
				contract = *tt.contract
			}
			limits, err := resolveAgentRunLoopLimits(profile, contract, tt.contract != nil)
			if err != nil {
				t.Fatalf("resolveAgentRunLoopLimits: %v", err)
			}
			if limits.MaxToolCalls != tt.wantMaxTool {
				t.Fatalf("MaxToolCalls = %d, want %d", limits.MaxToolCalls, tt.wantMaxTool)
			}
			if limits.ChildContract != tt.wantChild {
				t.Fatalf("ChildContract = %v, want %v", limits.ChildContract, tt.wantChild)
			}
			if limits.RunID != tt.wantRunID {
				t.Fatalf("RunID = %q, want %q", limits.RunID, tt.wantRunID)
			}
			if limits.MaxModelRequests != tt.wantModelReqs {
				t.Fatalf("MaxModelRequests = %d, want %d", limits.MaxModelRequests, tt.wantModelReqs)
			}
		})
	}
}

func TestRunAgentRun_ChildContractAppearsInDryRunProjection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(`
version: buckley.agent/v1
name: daily
tools:
  allow: [read_file, write_file]
subagents:
  - name: reviewer
    tool_tier: standard
`), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	contract, err := subagent.EncodeChildContract(subagent.ChildContract{
		Budget:           agentcoord.Budget{MaxToolCalls: 6},
		SchemaVersion:    "buckley.subagent-contract/v1",
		Model:            "child/model",
		Tier:             "execute",
		Effort:           "medium",
		SystemPrompt:     "Use evidence before conclusions.",
		AllowedTools:     []string{"read_file", "search_text"},
		ToolsConstrained: true,
		StepCap:          3,
		ApprovalPosture:  "safe",
		OutputSchema:     "buckley.artifact/v1",
	})
	if err != nil {
		t.Fatalf("EncodeChildContract: %v", err)
	}
	t.Setenv(subagent.ChildContractEnv, contract)

	output := captureStdout(t, func() {
		if err := runAgentRun([]string{"--dry-run", "--json", path, "reviewer", "inspect this"}); err != nil {
			t.Fatalf("runAgentRun: %v", err)
		}
	})
	var preview agentRunPreviewSnapshot
	if err := json.Unmarshal([]byte(output), &preview); err != nil {
		t.Fatalf("unmarshal preview: %v\n%s", err, output)
	}
	if preview.Model != "child/model" || preview.ToolTier != "standard" || strings.Join(preview.AllowedTools, ",") != "read_file" {
		t.Fatalf("preview contract projection = %+v", preview)
	}
	if preview.ResolvedTier != "execute" || preview.ReasoningEffort != "medium" || preview.StepCap != 3 || preview.OutputSchema != "buckley.artifact/v1" || preview.ApprovalMode != "safe" || !preview.Instructions {
		t.Fatalf("preview resolved fields = %+v", preview)
	}
	if preview.MaxToolCalls != 6 {
		t.Fatalf("preview MaxToolCalls = %d, want 6", preview.MaxToolCalls)
	}
}

func TestRunAgentRun_AppliesChildModelBeforeDependencyInitialization(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(`
version: buckley.agent/v1
name: exact-child
subagents:
  - name: reviewer
    model: z-ai/glm-5.2
`), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	previousInit := initDependenciesFn
	previousOverride := modelOverrideFlag
	t.Cleanup(func() {
		initDependenciesFn = previousInit
		modelOverrideFlag = previousOverride
	})
	sentinel := errors.New("stop after inspecting startup routing")
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		if modelOverrideFlag != "z-ai/glm-5.2" {
			t.Fatalf("dependency initialization saw model override %q, want exact child pin", modelOverrideFlag)
		}
		cfg := config.DefaultConfig()
		applyStartupModelOverride(cfg, modelOverrideFlag)
		if _, exists := cfg.Models.FallbackChains["z-ai/glm-5.2"]; exists {
			t.Fatal("child pin retained configured OpenRouter fallback chain")
		}
		return nil, nil, nil, sentinel
	}

	err := runAgentRun([]string{path, "reviewer", "inspect this"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("runAgentRun error = %v, want sentinel", err)
	}
	if modelOverrideFlag != previousOverride {
		t.Fatalf("model override was not restored: got %q want %q", modelOverrideFlag, previousOverride)
	}
}

func TestParseAgentRunArgs_MaxOutputTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "legacy default", args: []string{"agent.yaml", "reviewer", "inspect this"}, want: 0},
		{name: "explicit zero", args: []string{"--max-output-tokens", "0", "agent.yaml", "reviewer", "inspect this"}, want: 0},
		{name: "positive", args: []string{"--max-output-tokens", "321", "agent.yaml", "reviewer", "inspect this"}, want: 321},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, err := parseAgentRunArgs(tt.args)
			if err != nil {
				t.Fatalf("parseAgentRunArgs: %v", err)
			}
			if opts.maxOutputTokens != tt.want {
				t.Fatalf("maxOutputTokens = %d, want %d", opts.maxOutputTokens, tt.want)
			}
		})
	}
}

func TestRunAgentRun_RejectsNegativeMaxOutputTokensBeforeProfileLoad(t *testing.T) {
	_, err := parseAgentRunArgs([]string{"--max-output-tokens=-1", "/path/that/does/not/exist/agent.yaml", "reviewer", "inspect this"})
	if err == nil || err.Error() != "--max-output-tokens must be zero or greater" {
		t.Fatalf("parseAgentRunArgs error = %v, want deterministic negative-limit error", err)
	}
}

func TestRunAgentRun_MaxOutputTokensAppearsInDryRunPreview(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	if err := os.WriteFile(path, []byte(`
version: buckley.agent/v1
name: daily
subagents:
  - name: reviewer
`), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	jsonOutput := captureStdout(t, func() {
		if err := runAgentRun([]string{"--dry-run", "--json", "--max-output-tokens", "321", path, "reviewer", "inspect this"}); err != nil {
			t.Fatalf("runAgentRun json preview: %v", err)
		}
	})
	var preview agentRunPreviewSnapshot
	if err := json.Unmarshal([]byte(jsonOutput), &preview); err != nil {
		t.Fatalf("unmarshal preview: %v\n%s", err, jsonOutput)
	}
	if preview.MaxOutputTokens != 321 {
		t.Fatalf("preview MaxOutputTokens = %d, want 321", preview.MaxOutputTokens)
	}

	textOutput := captureStdout(t, func() {
		if err := runAgentRun([]string{"--dry-run", "--max-output-tokens", "321", path, "reviewer", "inspect this"}); err != nil {
			t.Fatalf("runAgentRun text preview: %v", err)
		}
	})
	if !strings.Contains(textOutput, "Max output tokens: 321") {
		t.Fatalf("text preview = %q, want max output token value", textOutput)
	}
}

func TestBuildACPChatRequest_MaxOutputTokensZeroPreservesLegacyRequest(t *testing.T) {
	conv := conversation.New("session-output-cap")
	conv.AddUserMessage("hello")

	legacy := buildACPChatRequest(nil, nil, nil, conv, "test/model", acpToolTurn{}, acpLoopLimits{})
	if legacy.MaxTokens != 0 {
		t.Fatalf("legacy MaxTokens = %d, want 0", legacy.MaxTokens)
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy request: %v", err)
	}
	if strings.Contains(string(encoded), "max_tokens") {
		t.Fatalf("legacy request unexpectedly exposed max_tokens: %s", encoded)
	}
}

func TestRunACPLoopWithLimits_PropagatesMaxOutputTokensAcrossRounds(t *testing.T) {
	t.Parallel()

	const maxOutputTokens = 321
	var mu sync.Mutex
	var requestMaxTokens []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		mu.Lock()
		round := len(requestMaxTokens) + 1
		var request struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err != nil || json.Unmarshal(body, &request) != nil {
			requestMaxTokens = append(requestMaxTokens, -1)
		} else {
			requestMaxTokens = append(requestMaxTokens, request.MaxTokens)
		}
		mu.Unlock()

		content := "final answer"
		if round == 1 {
			content = "<tool_call>read_file|path=/tmp/unused</tool_call>"
		}
		payload, _ := json.Marshal(map[string]any{
			"id":    fmt.Sprintf("chatcmpl-%d", round),
			"model": "gpt-4o",
			"choices": []map[string]any{{
				"index":         0,
				"delta":         map[string]any{"content": content},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 40, "completion_tokens": 10, "total_tokens": 50},
		})
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", payload)
	}))
	t.Cleanup(server.Close)

	cfg := config.DefaultConfig()
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "test-key"
	cfg.Providers.OpenAI.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openai"
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	conv := conversation.New("session-output-cap")
	conv.AddUserMessage("answer from context only")
	text, err := runACPLoopWithLimits(
		context.Background(), cfg, mgr, conv, nil, nil, nil,
		"gpt-4o", "", "session-output-cap", nil,
		func(string, ...interface{}) {}, nil,
		acpLoopLimits{MaxOutputTokens: maxOutputTokens},
	)
	if err != nil {
		t.Fatalf("runACPLoopWithLimits: %v", err)
	}
	if text != "final answer" {
		t.Fatalf("text = %q, want final answer", text)
	}

	mu.Lock()
	got := append([]int(nil), requestMaxTokens...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("model requests = %d, want initial and later request: %v", len(got), got)
	}
	for round, value := range got {
		if value != maxOutputTokens {
			t.Fatalf("request %d MaxTokens = %d, want %d", round+1, value, maxOutputTokens)
		}
	}
}
func TestParseAgentRunArgs_ExplicitRunLimits(t *testing.T) {
	opts, err := parseAgentRunArgs([]string{"--max-tool-calls", "6", "--max-elapsed-seconds", "45", "agent.yaml", "reviewer", "inspect this"})
	if err != nil {
		t.Fatalf("parseAgentRunArgs: %v", err)
	}
	if opts.maxToolCalls != 6 || opts.maxElapsedSeconds != 45 {
		t.Fatalf("run limits = (tools=%d, elapsed=%d), want (tools=6, elapsed=45)", opts.maxToolCalls, opts.maxElapsedSeconds)
	}
}

func TestParseAgentRunArgs_RejectsNegativeRunLimits(t *testing.T) {
	for _, tt := range []struct {
		flag string
		want string
	}{
		{"--max-tool-calls=-1", "--max-tool-calls must be zero or greater"},
		{"--max-elapsed-seconds=-1", "--max-elapsed-seconds must be zero or greater"},
	} {
		_, err := parseAgentRunArgs([]string{tt.flag, "agent.yaml", "reviewer", "inspect"})
		if err == nil || err.Error() != tt.want {
			t.Fatalf("%s error = %v, want %q", tt.flag, err, tt.want)
		}
	}
}

func TestApplyAgentRunExplicitLimitsNarrowsBudgets(t *testing.T) {
	base := acpLoopLimits{MaxToolCalls: 12, MaxElapsedSeconds: 90}
	got := applyAgentRunExplicitLimits(base, agentRunOptions{maxToolCalls: 5, maxElapsedSeconds: 45})
	if got.MaxToolCalls != 5 || got.MaxElapsedSeconds != 45 {
		t.Fatalf("narrowed limits = (tools=%d, elapsed=%d), want (tools=5, elapsed=45)", got.MaxToolCalls, got.MaxElapsedSeconds)
	}
	wider := applyAgentRunExplicitLimits(base, agentRunOptions{maxToolCalls: 20, maxElapsedSeconds: 120})
	if wider.MaxToolCalls != 12 || wider.MaxElapsedSeconds != 90 {
		t.Fatalf("wider flags loosened limits = (tools=%d, elapsed=%d), want existing (tools=12, elapsed=90)", wider.MaxToolCalls, wider.MaxElapsedSeconds)
	}
	unbounded := applyAgentRunExplicitLimits(acpLoopLimits{}, agentRunOptions{maxToolCalls: 5, maxElapsedSeconds: 45})
	if unbounded.MaxToolCalls != 5 || unbounded.MaxElapsedSeconds != 45 {
		t.Fatalf("explicit limits on unbounded run = (tools=%d, elapsed=%d), want (tools=5, elapsed=45)", unbounded.MaxToolCalls, unbounded.MaxElapsedSeconds)
	}
}
