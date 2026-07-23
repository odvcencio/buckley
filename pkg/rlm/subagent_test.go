package rlm

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
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

func TestFinalSynthesisMessagesPreservesHistoryAndRequiresAnswer(t *testing.T) {
	history := []model.Message{{Role: "tool", Content: "evidence"}}
	got := finalSynthesisMessages(history)
	if len(got) != 2 || got[0].Content != "evidence" {
		t.Fatalf("final synthesis messages = %#v, want preserved history", got)
	}
	if got[1].Role != "user" || !strings.Contains(fmt.Sprint(got[1].Content), "complete final answer now") {
		t.Fatalf("final synthesis instruction = %#v", got[1])
	}
	if len(history) != 1 {
		t.Fatalf("final synthesis mutated history: %#v", history)
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

func TestSubAgentExplicitLimitStillForcesFinalTurn(t *testing.T) {
	agent := &SubAgent{maxIterations: 3}
	if agent.shouldSynthesize(context.Background(), 1, 3, time.Now()) {
		t.Fatal("explicit limit synthesized one turn early")
	}
	if !agent.shouldSynthesize(context.Background(), 2, 3, time.Now()) {
		t.Fatal("explicit limit did not force its final turn")
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
