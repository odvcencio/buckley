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
	if cfg.SubAgent.MaxConcurrent <= 0 {
		t.Fatalf("expected default SubAgent.MaxConcurrent > 0")
	}
	if cfg.SubAgent.Timeout <= 0 {
		t.Fatalf("expected default SubAgent.Timeout > 0")
	}
}

func TestNormalizeFillsDefaults(t *testing.T) {
	cfg := Config{}
	cfg.Normalize()
	if cfg.Coordinator.MaxIterations <= 0 {
		t.Fatalf("expected Normalize to fill coordinator defaults")
	}
	if cfg.SubAgent.MaxConcurrent <= 0 {
		t.Fatalf("expected Normalize to fill subagent defaults")
	}
	if cfg.SubAgent.Timeout <= 0 {
		t.Fatalf("expected Normalize to fill subagent timeout")
	}
	if cfg.Scratchpad.MaxEntriesMemory <= 0 {
		t.Fatalf("expected Normalize to fill scratchpad defaults")
	}
}
