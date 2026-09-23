package config

import (
	"strings"
	"testing"
	"time"
)

func TestDefaultConfigDecisionsIsOffWithPopulatedDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Decisions.Enabled {
		t.Fatal("decisions.enabled must default to false")
	}
	if cfg.Decisions.Gates.ReviewDepth.Enabled {
		t.Fatal("decisions.gates.review_depth.enabled must default to false")
	}
	if cfg.Decisions.Gates.ReasoningChoice.Enabled {
		t.Fatal("decisions.gates.reasoning_choice.enabled must default to false")
	}
	if cfg.Decisions.Model != "typesafe/jev-1.13" {
		t.Fatalf("unexpected default model: %s", cfg.Decisions.Model)
	}
	if cfg.Decisions.Endpoint != "https://openrouter.ai/api/alpha/decisions" {
		t.Fatalf("unexpected default endpoint: %s", cfg.Decisions.Endpoint)
	}
	if cfg.Decisions.Timeout != 12*time.Second {
		t.Fatalf("unexpected default timeout: %s", cfg.Decisions.Timeout)
	}
	if cfg.Decisions.Pricing.InputPerMillion != 0.042 || cfg.Decisions.Pricing.OutputPerMillion != 0 {
		t.Fatalf("unexpected default pricing: %+v", cfg.Decisions.Pricing)
	}
	if cfg.Decisions.Gates.ReviewDepth.TrivialProbability != 0.85 {
		t.Fatalf("unexpected default trivial probability: %v", cfg.Decisions.Gates.ReviewDepth.TrivialProbability)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config must validate: %v", err)
	}
}

func TestApplyEnvOverridesDecisions(t *testing.T) {
	cfg := DefaultConfig()
	t.Setenv("BUCKLEY_DECISIONS_ENABLED", "true")
	t.Setenv("BUCKLEY_DECISIONS_MODEL", "typesafe/jev-2.0")
	t.Setenv("BUCKLEY_DECISIONS_ENDPOINT", "https://example.test/decisions")
	t.Setenv("BUCKLEY_DECISIONS_TIMEOUT", "5s")
	t.Setenv("BUCKLEY_DECISIONS_PRICING_INPUT_PER_MILLION", "1.5")
	t.Setenv("BUCKLEY_DECISIONS_PRICING_OUTPUT_PER_MILLION", "2.5")
	t.Setenv("BUCKLEY_DECISIONS_LOG_PATH", "/tmp/decisions.jsonl")
	t.Setenv("BUCKLEY_DECISIONS_GATE_REVIEW_DEPTH_ENABLED", "true")
	t.Setenv("BUCKLEY_DECISIONS_GATE_REVIEW_DEPTH_TRIVIAL_PROBABILITY", "0.9")
	t.Setenv("BUCKLEY_DECISIONS_GATE_REASONING_CHOICE_ENABLED", "true")

	ApplyEnvOverridesForTest(cfg)

	if !cfg.Decisions.Enabled {
		t.Error("BUCKLEY_DECISIONS_ENABLED=true did not enable decisions")
	}
	if cfg.Decisions.Model != "typesafe/jev-2.0" {
		t.Errorf("unexpected model: %s", cfg.Decisions.Model)
	}
	if cfg.Decisions.Endpoint != "https://example.test/decisions" {
		t.Errorf("unexpected endpoint: %s", cfg.Decisions.Endpoint)
	}
	if cfg.Decisions.Timeout != 5*time.Second {
		t.Errorf("unexpected timeout: %s", cfg.Decisions.Timeout)
	}
	if cfg.Decisions.Pricing.InputPerMillion != 1.5 || cfg.Decisions.Pricing.OutputPerMillion != 2.5 {
		t.Errorf("unexpected pricing: %+v", cfg.Decisions.Pricing)
	}
	if cfg.Decisions.LogPath != "/tmp/decisions.jsonl" {
		t.Errorf("unexpected log path: %s", cfg.Decisions.LogPath)
	}
	if !cfg.Decisions.Gates.ReviewDepth.Enabled {
		t.Error("BUCKLEY_DECISIONS_GATE_REVIEW_DEPTH_ENABLED=true did not enable the gate")
	}
	if cfg.Decisions.Gates.ReviewDepth.TrivialProbability != 0.9 {
		t.Errorf("unexpected trivial probability: %v", cfg.Decisions.Gates.ReviewDepth.TrivialProbability)
	}
	if !cfg.Decisions.Gates.ReasoningChoice.Enabled {
		t.Error("BUCKLEY_DECISIONS_GATE_REASONING_CHOICE_ENABLED=true did not enable the gate")
	}
}

func TestValidateDecisionsSkipsWhenFullyDisabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Decisions.Model = ""
	cfg.Decisions.Endpoint = ""
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a fully disabled decisions section must not block validation: %v", err)
	}
}

func TestValidateDecisionsRequiresModelWhenEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Decisions.Enabled = true
	cfg.Decisions.Model = ""
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "decisions.model") {
		t.Fatalf("expected a decisions.model error, got %v", err)
	}
}

func TestValidateDecisionsRequiresModelWhenReviewDepthGateEnabled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Decisions.Model = ""
	cfg.Decisions.Gates.ReviewDepth.Enabled = true
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "decisions.model") {
		t.Fatalf("expected a decisions.model error, got %v", err)
	}
}

func TestValidateDecisionsRejectsNegativeTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Decisions.Enabled = true
	cfg.Decisions.Timeout = -1
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "decisions.timeout") {
		t.Fatalf("expected a decisions.timeout error, got %v", err)
	}
}

func TestValidateDecisionsRejectsNegativePricing(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Decisions.Enabled = true
	cfg.Decisions.Pricing.InputPerMillion = -1
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "decisions.pricing") {
		t.Fatalf("expected a decisions.pricing error, got %v", err)
	}
}

func TestValidateDecisionsRejectsOutOfRangeThreshold(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Decisions.Gates.ReviewDepth.Enabled = true
	cfg.Decisions.Gates.ReviewDepth.TrivialProbability = 1.5
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "trivial_probability") {
		t.Fatalf("expected a trivial_probability error, got %v", err)
	}
}
