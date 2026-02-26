package rlm

import "testing"

type mockExecModelProvider struct {
	model string
}

func (m mockExecModelProvider) GetExecutionModel() string {
	return m.model
}

func TestModelSelectorUsesConfiguredModel(t *testing.T) {
	cfg := Config{
		SubAgent: SubAgentConfig{
			Model: "configured-model",
		},
	}
	selector := NewModelSelector(cfg, mockExecModelProvider{model: "fallback"})

	got := selector.Select()
	if got != "configured-model" {
		t.Fatalf("expected configured-model, got %s", got)
	}
}

func TestModelSelectorFallsBackToExecutionModel(t *testing.T) {
	cfg := Config{
		SubAgent: SubAgentConfig{
			Model: "", // Empty = use execution model
		},
	}
	selector := NewModelSelector(cfg, mockExecModelProvider{model: "execution-model"})

	got := selector.Select()
	if got != "execution-model" {
		t.Fatalf("expected execution-model fallback, got %s", got)
	}
}

func TestModelSelectorSetModelOverrides(t *testing.T) {
	cfg := Config{
		SubAgent: SubAgentConfig{
			Model: "initial-model",
		},
	}
	selector := NewModelSelector(cfg, mockExecModelProvider{model: "fallback"})

	selector.SetModel("override-model")

	got := selector.Select()
	if got != "override-model" {
		t.Fatalf("expected override-model, got %s", got)
	}
}

func TestModelSelectorNilSafe(t *testing.T) {
	var selector *ModelSelector
	got := selector.Select()
	if got != "" {
		t.Fatalf("expected empty string for nil selector, got %s", got)
	}

	selector.SetModel("test") // Should not panic
}
