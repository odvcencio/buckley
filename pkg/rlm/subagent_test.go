package rlm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/tool"
	"m31labs.dev/buckley/v2/pkg/tool/builtin"
)

func TestFormatToolResultHonorsAbridgedDisplayData(t *testing.T) {
	result := &builtin.Result{
		Success: true,
		Data: map[string]any{
			"stdout": "FULL_OUTPUT_MUST_NOT_ENTER_MODEL_CONTEXT",
		},
		ShouldAbridge: true,
		DisplayData: map[string]any{
			"status": "PASS",
			"argv":   []string{"go", "test", "."},
		},
	}
	formatted := formatToolResult(result)
	if strings.Contains(formatted, "FULL_OUTPUT_MUST_NOT_ENTER_MODEL_CONTEXT") {
		t.Fatalf("abridged tool result leaked full output: %s", formatted)
	}
	if !strings.Contains(formatted, "PASS") {
		t.Fatalf("abridged tool result omitted trusted status: %s", formatted)
	}
}

func TestNewSubAgentValidation(t *testing.T) {
	registry := tool.NewEmptyRegistry()

	tests := []struct {
		name    string
		cfg     SubAgentConfig
		deps    SubAgentDeps
		wantErr string
	}{
		{
			name:    "empty ID",
			cfg:     SubAgentConfig{ID: "", Model: "test-model"},
			deps:    SubAgentDeps{Models: nil, Registry: registry},
			wantErr: "sub-agent ID required",
		},
		{
			name:    "nil model manager",
			cfg:     SubAgentConfig{ID: "test-agent", Model: "test-model"},
			deps:    SubAgentDeps{Models: nil, Registry: registry},
			wantErr: "model manager required",
		},
		{
			name:    "nil registry",
			cfg:     SubAgentConfig{ID: "test-agent", Model: "test-model"},
			deps:    SubAgentDeps{Models: nil, Registry: nil},
			wantErr: "model manager required", // fails on models first
		},
		{
			name:    "empty model",
			cfg:     SubAgentConfig{ID: "test-agent", Model: ""},
			deps:    SubAgentDeps{Registry: registry},
			wantErr: "model manager required", // fails on models first
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewSubAgent(tt.cfg, tt.deps)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if err.Error() != tt.wantErr {
				t.Errorf("error = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestBudgetedMaxOutputTokensReservesInputAndCapsCompletion(t *testing.T) {
	pricing := model.ModelPricing{Prompt: 0.72, Completion: 3.50}
	got, err := budgetedMaxOutputTokens(pricing, 40_000, 0.10, 0.15)
	if err != nil {
		t.Fatalf("budgetedMaxOutputTokens() error = %v", err)
	}
	if got != 5_935 {
		t.Fatalf("budgetedMaxOutputTokens() = %d, want 5935", got)
	}
}

func TestBudgetedMaxOutputTokensRejectsUnaffordableRequest(t *testing.T) {
	pricing := model.ModelPricing{Prompt: 3, Completion: 15}
	if _, err := budgetedMaxOutputTokens(pricing, 100_000, 0, 0.25); err == nil {
		t.Fatal("budgetedMaxOutputTokens() error = nil, want exhausted input budget")
	}
}

func TestBoundedOutputTokenLimitPreservesSmallerLimit(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		budgeted   int
		want       int
	}{
		{name: "no configured limit", budgeted: 5935, want: 5935},
		{name: "configured limit is smaller", configured: 4096, budgeted: 5935, want: 4096},
		{name: "cost limit is smaller", configured: 4096, budgeted: 2048, want: 2048},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := boundedOutputTokenLimit(tt.configured, tt.budgeted); got != tt.want {
				t.Fatalf("boundedOutputTokenLimit(%d, %d) = %d, want %d", tt.configured, tt.budgeted, got, tt.want)
			}
		})
	}
}

func TestFinalSynthesisMessagesPreservesHistoryAndRequiresAnswer(t *testing.T) {
	history := []model.Message{{Role: "tool", Content: "evidence"}}
	got := finalSynthesisMessages(history)
	if len(got) != 3 || got[1].Content != "evidence" {
		t.Fatalf("final synthesis messages = %#v, want preserved history", got)
	}
	if got[0].Role != "system" || !strings.Contains(fmt.Sprint(got[0].Content), "Tools are unavailable") {
		t.Fatalf("final synthesis system instruction = %#v", got[0])
	}
	if got[2].Role != "user" || !strings.Contains(fmt.Sprint(got[2].Content), "complete final answer now") {
		t.Fatalf("final synthesis instruction = %#v", got[2])
	}
	if len(history) != 1 {
		t.Fatalf("final synthesis mutated history: %#v", history)
	}
}

func TestFinalSynthesisMessagesStrengthensExistingSystemPrompt(t *testing.T) {
	history := []model.Message{
		{Role: "system", Content: "Review the change with tools."},
		{Role: "user", Content: "review this diff"},
	}
	got := finalSynthesisMessages(history)
	if len(got) != 3 {
		t.Fatalf("final synthesis messages = %#v, want one added instruction", got)
	}
	system := fmt.Sprint(got[0].Content)
	if !strings.Contains(system, "Review the change with tools.") || !strings.Contains(system, "Tools are unavailable") {
		t.Fatalf("final synthesis system prompt = %q", system)
	}
	if history[0].Content != "Review the change with tools." {
		t.Fatalf("final synthesis mutated history: %#v", history)
	}
}

func TestSummarizeRejectedToolCallsDoesNotEmitToolMarkup(t *testing.T) {
	got := summarizeRejectedToolCalls([]model.ToolCall{
		{Function: model.FunctionCall{Name: "read_file"}},
		{Function: model.FunctionCall{Name: "search_text"}},
	})
	if !strings.Contains(got, "read_file, search_text") || strings.Contains(got, "<tool_call>") {
		t.Fatalf("rejected tool summary = %q", got)
	}
}

func TestPrepareFinalToolRepairAddsOneStrictSynthesisTurn(t *testing.T) {
	history := []model.Message{{Role: "tool", Content: "verified PASS"}}
	got, iterations, retry := prepareFinalToolRepair(history, 2, false)
	if !retry || iterations != 3 || len(got) != 2 {
		t.Fatalf("repair = retry %v, iterations %d, messages %#v", retry, iterations, got)
	}
	if !strings.Contains(fmt.Sprint(got[1].Content), "completed evidence") {
		t.Fatalf("repair prompt = %#v", got[1])
	}
	if len(history) != 1 {
		t.Fatalf("repair mutated history: %#v", history)
	}

	got, iterations, retry = prepareFinalToolRepair(got, iterations, true)
	if retry || iterations != 3 || len(got) != 2 {
		t.Fatalf("second repair = retry %v, iterations %d, messages %#v", retry, iterations, got)
	}
}

func TestMarshalSubAgentRawPreservesFinishReason(t *testing.T) {
	raw := marshalSubAgentRaw(&SubAgentResult{
		Summary:      "partial review",
		FinishReason: "length",
	})
	if !strings.Contains(string(raw), `"finish_reason":"length"`) {
		t.Fatalf("raw result omitted the finish reason: %s", raw)
	}
}

func TestAssistantToolCallMessagePreservesReasoningContinuity(t *testing.T) {
	source := model.Message{
		Content:   "inspect the selected files",
		ToolCalls: []model.ToolCall{{ID: "call-1"}},
		Reasoning: "the supplied diff leaves one invariant unclear",
		ReasoningDetails: []model.ReasoningDetail{{
			Type: "reasoning.text",
			Text: "inspect the exact invariant before the verdict",
		}},
	}

	got := assistantToolCallMessage(source)
	if got.Role != "assistant" || got.Reasoning != source.Reasoning {
		t.Fatalf("assistant tool message = %#v", got)
	}
	if len(got.ReasoningDetails) != 1 || got.ReasoningDetails[0].Text != source.ReasoningDetails[0].Text {
		t.Fatalf("reasoning details = %#v", got.ReasoningDetails)
	}

	source.ToolCalls[0].ID = "changed"
	source.ReasoningDetails[0].Text = "changed"
	if got.ToolCalls[0].ID != "call-1" || got.ReasoningDetails[0].Text == "changed" {
		t.Fatal("assistant tool message shares mutable slices with the provider response")
	}
}

func TestSubAgentDefaultPrompt(t *testing.T) {
	// Verify the default prompt is set when empty
	if defaultSubAgentPrompt == "" {
		t.Fatal("defaultSubAgentPrompt should not be empty")
	}

	// Check key elements are present
	keywords := []string{
		"sub-agent",
		"summary",
		"tool",
		"Read Before Writing",
	}

	for _, kw := range keywords {
		if !containsString(defaultSubAgentPrompt, kw) {
			t.Errorf("default prompt should contain %q", kw)
		}
	}
}

func TestNewSubAgentBoundsReasoningTokens(t *testing.T) {
	agent, err := NewSubAgent(SubAgentConfig{
		ID:                 "bounded-reasoning",
		Model:              "test-model",
		Reasoning:          "medium",
		ReasoningMaxTokens: 4096,
		MaxOutputTokens:    2048,
	}, SubAgentDeps{
		Models:   &model.Manager{},
		Registry: tool.NewEmptyRegistry(),
	})
	if err != nil {
		t.Fatalf("NewSubAgent() error = %v", err)
	}
	if agent.reasoning != "medium" || agent.reasoningMaxTokens != 4096 {
		t.Fatalf("reasoning policy = %q/%d", agent.reasoning, agent.reasoningMaxTokens)
	}
	if agent.maxOutputTokens != 2048 {
		t.Fatalf("max output tokens = %d, want 2048", agent.maxOutputTokens)
	}
}

func TestSubAgentReasoningConfigUsesOneProviderControl(t *testing.T) {
	api := subAgentReasoningConfig("openrouter", "medium", 4096)
	if api == nil || api.Effort != "" || api.MaxTokens != 4096 {
		t.Fatalf("API reasoning config = %#v", api)
	}

	codex := subAgentReasoningConfig("codex", "xhigh", 2048)
	if codex == nil || codex.Effort != "xhigh" || codex.MaxTokens != 0 {
		t.Fatalf("Codex reasoning config = %#v", codex)
	}

	effortOnly := subAgentReasoningConfig("openrouter", "low", 0)
	if effortOnly == nil || effortOnly.Effort != "low" || effortOnly.MaxTokens != 0 {
		t.Fatalf("effort-only reasoning config = %#v", effortOnly)
	}

	if got := subAgentReasoningConfig("openrouter", "", 0); got != nil {
		t.Fatalf("empty reasoning config = %#v, want nil", got)
	}
}

func TestSubAgentMaxIterationsDefault(t *testing.T) {
	if defaultSubAgentMaxIterations <= 0 {
		t.Errorf("defaultSubAgentMaxIterations should be positive, got %d", defaultSubAgentMaxIterations)
	}
	if defaultSubAgentMaxIterations != 25 {
		t.Errorf("defaultSubAgentMaxIterations = %d, want 25", defaultSubAgentMaxIterations)
	}
}

func TestSubAgentAdaptiveSynthesisUsesDeadline(t *testing.T) {
	agent := &SubAgent{
		adaptive:      true,
		synthesisLead: time.Minute,
	}
	farCtx, farCancel := context.WithDeadline(context.Background(), time.Now().Add(2*time.Minute))
	defer farCancel()
	if agent.shouldSynthesize(farCtx, 0, 0, time.Now()) {
		t.Fatal("adaptive agent synthesized before its deadline reserve")
	}
	nearCtx, nearCancel := context.WithDeadline(context.Background(), time.Now().Add(30*time.Second))
	defer nearCancel()
	if !agent.shouldSynthesize(nearCtx, 0, 0, time.Now().Add(-2*time.Minute)) {
		t.Fatal("adaptive agent did not reserve time for final synthesis")
	}
}

func TestSubAgentToolDeadlinePreservesSynthesisWindow(t *testing.T) {
	agent := &SubAgent{
		adaptive:      true,
		synthesisLead: 90 * time.Second,
	}
	startedAt := time.Now()
	parent, cancelParent := context.WithDeadline(context.Background(), startedAt.Add(6*time.Minute))
	defer cancelParent()

	toolCtx, cancelTools := agent.explorationContext(parent, startedAt)
	defer cancelTools()
	got, ok := toolCtx.Deadline()
	if !ok {
		t.Fatal("tool context has no deadline")
	}
	want := startedAt.Add(4*time.Minute + 30*time.Second)
	if delta := got.Sub(want); delta < -time.Second || delta > time.Second {
		t.Fatalf("tool deadline = %s, want about %s", got, want)
	}
}

func TestSubAgentToolDeadlineHonorsConfiguredReviewReserve(t *testing.T) {
	agent := &SubAgent{
		adaptive:      true,
		synthesisLead: 165 * time.Second,
	}
	startedAt := time.Now()
	parent, cancelParent := context.WithDeadline(context.Background(), startedAt.Add(255*time.Second))
	defer cancelParent()

	toolCtx, cancelTools := agent.explorationContext(parent, startedAt)
	defer cancelTools()
	got, ok := toolCtx.Deadline()
	if !ok {
		t.Fatal("tool context has no deadline")
	}
	want := startedAt.Add(90 * time.Second)
	if delta := got.Sub(want); delta < -time.Second || delta > time.Second {
		t.Fatalf("tool deadline = %s, want about %s", got, want)
	}
}

func TestSubAgentExplorationDeadlineUsesShorterSizeBudget(t *testing.T) {
	agent := &SubAgent{
		adaptive:           true,
		explorationTimeout: 3 * time.Minute,
		synthesisLead:      90 * time.Second,
	}
	startedAt := time.Now()
	parent, cancelParent := context.WithDeadline(context.Background(), startedAt.Add(12*time.Minute))
	defer cancelParent()

	exploreCtx, cancelExplore := agent.explorationContext(parent, startedAt)
	defer cancelExplore()
	got, ok := exploreCtx.Deadline()
	if !ok {
		t.Fatal("exploration context has no deadline")
	}
	want := startedAt.Add(3 * time.Minute)
	if delta := got.Sub(want); delta < -time.Second || delta > time.Second {
		t.Fatalf("exploration deadline = %s, want about %s", got, want)
	}
}

func TestAwaitChatCompletionReturnsWhenProviderIgnoresCancellation(t *testing.T) {
	providerRelease := make(chan struct{})
	defer close(providerRelease)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := awaitChatCompletion(ctx, func() (*model.ChatResponse, error) {
		<-providerRelease
		return &model.ChatResponse{}, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("awaitChatCompletion() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("awaitChatCompletion() returned after %s, want under one second", elapsed)
	}
}

func TestSubAgentToolBudgetForcesSynthesis(t *testing.T) {
	agent := &SubAgent{maxToolCalls: 12}
	if agent.toolBudgetExhausted(&SubAgentResult{ToolCalls: make([]SubAgentToolCall, 11)}) {
		t.Fatal("tool budget exhausted early")
	}
	if !agent.toolBudgetExhausted(&SubAgentResult{ToolCalls: make([]SubAgentToolCall, 12)}) {
		t.Fatal("tool budget did not force synthesis")
	}
}

func TestSubAgentVerificationBudgetCapsExpensiveCalls(t *testing.T) {
	agent := &SubAgent{maxVerificationCalls: 1}
	if agent.verificationBudgetExhausted(&SubAgentResult{
		ToolCalls: []SubAgentToolCall{{Name: "read_file"}},
	}) {
		t.Fatal("source inspection exhausted the verification budget")
	}
	if !agent.verificationBudgetExhausted(&SubAgentResult{
		ToolCalls: []SubAgentToolCall{{Name: "run_verification"}},
	}) {
		t.Fatal("verification budget did not stop a second expensive call")
	}
}

func TestSubAgentExplicitLimitStillForcesFinalTurn(t *testing.T) {
	agent := &SubAgent{maxIterations: 3}
	if agent.shouldSynthesize(context.Background(), 1, 3, time.Now()) {
		t.Fatal("explicit limit synthesized one turn early")
	}
	if !agent.shouldSynthesize(context.Background(), 2, 3, time.Now()) {
		t.Fatal("explicit limit did not force its final turn")
	}
}

func TestSubAgentSingleIterationStartsWithoutTools(t *testing.T) {
	agent := &SubAgent{maxIterations: 1}
	if !agent.shouldSynthesize(context.Background(), 0, 1, time.Now()) {
		t.Fatal("one-turn agent did not start in final response mode")
	}
}

func TestSynthesisBudgetRequiredReservesFinalResponse(t *testing.T) {
	pricing := model.ModelPricing{Prompt: 0.32, Completion: 1.28}
	if synthesisBudgetRequired(pricing, 10_000, 0, 0.15) {
		t.Fatal("synthesis triggered while exploration and final-response reserve still fit")
	}
	if !synthesisBudgetRequired(pricing, 10_000, 0.06, 0.15) {
		t.Fatal("synthesis did not trigger before exploration consumed the final-response reserve")
	}
	if synthesisBudgetRequired(pricing, 10_000, 0.06, 0) {
		t.Fatal("unbudgeted execution should not trigger cost-aware synthesis")
	}
}

func TestBuildToolDefinitionsFiltersToAllowedRegistryNames(t *testing.T) {
	registry := tool.NewRegistry()
	allowed := map[string]struct{}{
		"read_file": {},
		"run_shell": {},
	}

	definitions := buildToolDefinitions(registry, allowed)
	if len(definitions) != len(allowed) {
		t.Fatalf("definitions = %d, want %d", len(definitions), len(allowed))
	}
	seen := make(map[string]bool)
	for _, definition := range definitions {
		function, _ := definition["function"].(map[string]any)
		name, _ := function["name"].(string)
		seen[name] = true
	}
	for name := range allowed {
		if !seen[name] {
			t.Errorf("missing allowed tool %q from definitions: %#v", name, definitions)
		}
	}
	if seen["write_file"] {
		t.Fatal("write_file leaked into read-only review tool definitions")
	}
}

func TestReadOnlyToolSetAndRequestPolicy(t *testing.T) {
	if !isReadOnlyToolSet([]string{"read_file", "find_files", "search_text"}) {
		t.Fatal("review read tools should select read-only execution")
	}
	for _, tools := range [][]string{
		nil,
		{"read_file", "run_shell"},
		{"read_file", "write_file"},
	} {
		if isReadOnlyToolSet(tools) {
			t.Fatalf("tool set should remain writable/unsafe: %v", tools)
		}
	}

	req := model.ChatRequest{}
	applyExecutionPolicy(&req, true, nil)
	if got := req.Metadata[model.RequestMetadataReadOnly]; got != "true" {
		t.Fatalf("read-only metadata = %q, want true", got)
	}

	writable := model.ChatRequest{}
	applyExecutionPolicy(&writable, false, nil)
	if writable.Metadata != nil {
		t.Fatalf("writable request unexpectedly received policy metadata: %v", writable.Metadata)
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStringHelper(s, substr))
}

func containsStringHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
