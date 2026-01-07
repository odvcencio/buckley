package rlm

import "testing"

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Coordinator.MaxIterations <= 0 {
		t.Fatalf("expected default MaxIterations > 0")
	}
	// MaxTokensBudget 0 = unlimited (no artificial limit)
	if cfg.Coordinator.MaxTokensBudget != 0 {
		t.Fatalf("expected default MaxTokensBudget = 0 (unlimited), got %d", cfg.Coordinator.MaxTokensBudget)
	}
	if len(cfg.Tiers) == 0 {
		t.Fatalf("expected default tiers")
	}
	if _, ok := cfg.Tiers[WeightReasoning]; !ok {
		t.Fatalf("expected reasoning tier")
	}
}

func TestNormalizeFillsDefaults(t *testing.T) {
	cfg := Config{
		Tiers: map[Weight]TierConfig{
			WeightTrivial: {MaxCostPerMillion: 1.0},
		},
	}
	cfg.Normalize()
	if _, ok := cfg.Tiers[WeightLight]; !ok {
		t.Fatalf("expected Normalize to fill missing tiers")
	}
	if cfg.Coordinator.MaxIterations <= 0 {
		t.Fatalf("expected Normalize to fill coordinator defaults")
	}
	if cfg.Scratchpad.MaxEntriesMemory <= 0 {
		t.Fatalf("expected Normalize to fill scratchpad defaults")
	}
}
