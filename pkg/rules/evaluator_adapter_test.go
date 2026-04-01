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
	if result.String("action") != "use" {
		t.Errorf("action = %q, want use", result.String("action"))
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
