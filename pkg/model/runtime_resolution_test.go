package model

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
)

func TestResolvePhaseModel_UsesExplicitOverride(t *testing.T) {
	cfg := &config.Config{
		Models: config.ModelConfig{
			Execution: "config-execution-model",
		},
	}

	got := ResolvePhaseModel(cfg, nil, nil, "execution", "anthropic/claude-3.5-sonnet")
	if got != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("ResolvePhaseModel() = %q, want explicit override", got)
	}
}

func TestResolveReasoningEffort_UsesArbiterPolicy(t *testing.T) {
	engine := mustNewTestEngine(t)
	checker := &stubReasoningChecker{models: map[string]bool{"reasoning-model": true}}

	got := ResolveReasoningEffort(&config.Config{}, checker, engine, "reasoning-model", "planning")
	if got != "high" {
		t.Fatalf("ResolveReasoningEffort() = %q, want high", got)
	}
}

func TestResolveReasoningEffort_DisablesWhenConfigOff(t *testing.T) {
	engine := mustNewTestEngine(t)
	checker := &stubReasoningChecker{models: map[string]bool{"reasoning-model": true}}
	cfg := &config.Config{
		Models: config.ModelConfig{
			Reasoning: "off",
		},
	}

	got := ResolveReasoningEffort(cfg, checker, engine, "reasoning-model", "planning")
	if got != "" {
		t.Fatalf("ResolveReasoningEffort() = %q, want empty string", got)
	}
}

func TestResolveReasoningEffort_UsesConfiguredXHigh(t *testing.T) {
	engine := mustNewTestEngine(t)
	checker := &stubReasoningChecker{models: map[string]bool{"reasoning-model": true}}
	cfg := &config.Config{
		Models: config.ModelConfig{
			Reasoning: "xhigh",
		},
	}

	got := ResolveReasoningEffort(cfg, checker, engine, "reasoning-model", "execution")
	if got != "xhigh" {
		t.Fatalf("ResolveReasoningEffort() = %q, want xhigh", got)
	}
}

func TestInferModelTier(t *testing.T) {
	tests := []struct {
		modelID string
		want    string
	}{
		{modelID: "openai/gpt-5-mini", want: "fast"},
		{modelID: "anthropic/claude-opus-4", want: "premium"},
		{modelID: "openai/gpt-4o", want: "standard"},
	}

	for _, tt := range tests {
		if got := InferModelTier(tt.modelID); got != tt.want {
			t.Fatalf("InferModelTier(%q) = %q, want %q", tt.modelID, got, tt.want)
		}
	}
}
