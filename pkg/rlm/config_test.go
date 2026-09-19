package rlm

import (
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Coordinator.MaxIterations <= 0 {
		t.Fatalf("expected default MaxIterations > 0")
	}
	if cfg.Coordinator.MaxTokensBudget <= 0 {
		t.Fatalf("expected default MaxTokensBudget > 0")
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

func TestNormalizeScratchpadEvictionPolicyCanonicalizesSupportedValues(t *testing.T) {
	cfg := Config{}
	cfg.Scratchpad.EvictionPolicy = " FIFO "
	cfg.Normalize()
	if cfg.Scratchpad.EvictionPolicy != scratchpadEvictionPolicyFIFO {
		t.Fatalf("EvictionPolicy = %q, want fifo", cfg.Scratchpad.EvictionPolicy)
	}

	cfg.Scratchpad.EvictionPolicy = " LRU "
	cfg.Normalize()
	if cfg.Scratchpad.EvictionPolicy != scratchpadEvictionPolicyLRU {
		t.Fatalf("EvictionPolicy = %q, want lru", cfg.Scratchpad.EvictionPolicy)
	}
}

func TestConfigValidateRejectsUnknownScratchpadEvictionPolicy(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Scratchpad.EvictionPolicy = "lfu"
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "eviction_policy") {
		t.Fatalf("Validate error = %v, want eviction_policy rejection", err)
	}
}

func TestNewRuntimeRejectsUnknownScratchpadEvictionPolicy(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Scratchpad.EvictionPolicy = "lfu"
	_, err := NewRuntime(cfg, RuntimeDeps{})
	if err == nil || !strings.Contains(err.Error(), "eviction_policy") {
		t.Fatalf("NewRuntime error = %v, want eviction_policy rejection before runtime construction", err)
	}
}

func TestConfigValidateRejectsInvalidTierConfigs(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "unknown weight",
			cfg: Config{Tiers: map[Weight]TierConfig{
				Weight("Light"): {},
			}},
			want: "rlm tier",
		},
		{
			name: "negative cost",
			cfg: Config{Tiers: map[Weight]TierConfig{
				WeightLight: {MaxCostPerMillion: -1},
			}},
			want: "max_cost_per_million",
		},
		{
			name: "negative context",
			cfg: Config{Tiers: map[Weight]TierConfig{
				WeightLight: {MinContextWindow: -1},
			}},
			want: "min_context_window",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestNewRuntimeRejectsInvalidTierConfigs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Tiers[Weight("Light")] = TierConfig{}
	_, err := NewRuntime(cfg, RuntimeDeps{})
	if err == nil || !strings.Contains(err.Error(), "rlm tier") {
		t.Fatalf("NewRuntime error = %v, want tier validation before runtime construction", err)
	}
}
