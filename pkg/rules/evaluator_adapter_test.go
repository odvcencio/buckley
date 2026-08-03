package rules

import "testing"

func TestEngineAdapter_EvalStrategy(t *testing.T) {
	engine := mustNewTestEngine(t)
	adapter := NewEngineAdapter(engine)

	// Test with existing routing domain
	result, err := adapter.EvalStrategy("routing", "model_select", map[string]any{
		"task.phase": "execution",
	})
	if err != nil {
		t.Fatalf("EvalStrategy: %v", err)
	}
	// routing.arb defers model identity to configuration, so the action
	// is use_configured and the model field is intentionally empty.
	if result.String("action") != "use_configured" {
		t.Errorf("action = %q, want use_configured", result.String("action"))
	}
	if model := result.String("model"); model != "" {
		t.Errorf("model = %q, want empty (deferral to config)", model)
	}
}

func TestEngineAdapter_UnknownDomain(t *testing.T) {
	engine := mustNewTestEngine(t)
	adapter := NewEngineAdapter(engine)

	_, err := adapter.EvalStrategy("nonexistent", "policy", nil)
	if err == nil {
		t.Error("expected error for unknown domain")
	}
}
