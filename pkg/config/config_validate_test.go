package config

import (
	"math"
	"strings"
	"testing"
	"time"
)

// TestValidateReportsFirstViolationInOriginalOrder locks in the
// decomposition's ordering contract: Validate walks configValidators in
// the same order the original monolithic function checked each section,
// so a config with more than one violation still reports the same first
// error it did before the decomposition.
func TestValidateReportsFirstViolationInOriginalOrder(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Orchestrator.TrustLevel = "chaotic"
	cfg.Execution.Mode = "bad-mode"
	cfg.Approval.Mode = "bad-approval"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error for the invalid trust level")
	}
	const want = "invalid trust level: chaotic (must be conservative, balanced, or autonomous)"
	if err.Error() != want {
		t.Fatalf("expected trust level to be checked before execution mode or approval mode, got: %v", err)
	}
}

// TestValidateSectionValidatorsRunInSequence asserts every section
// validator in configValidators runs, not just the first: clearing the
// trust-level violation from
// TestValidateReportsFirstViolationInOriginalOrder must surface the next
// section's violation, in order.
func TestValidateSectionValidatorsRunInSequence(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Execution.Mode = "bad-mode"
	cfg.Approval.Mode = "bad-approval"

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected an error for the invalid execution mode")
	}
	const want = "invalid execution mode: bad-mode (valid: classic, rlm)"
	if err.Error() != want {
		t.Fatalf("expected execution mode to be checked before approval mode, got: %v", err)
	}
}

// TestValidateBatchNormalizesRemoteBranchNameAsSideEffect asserts the
// decomposed validateBatch still performs the one normalizing mutation
// the original inline check made: defaulting remote_branch.remote_name to
// "origin" when remote branches are enabled but no name was given.
func TestValidateBatchNormalizesRemoteBranchNameAsSideEffect(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Batch.Enabled = true
	cfg.Batch.JobTemplate.Image = "buckley:latest"
	cfg.Batch.JobTemplate.Command = []string{"buckley"}
	cfg.Batch.JobTemplate.Args = []string{"execute-task"}
	cfg.Batch.JobTemplate.WorkspaceMountPath = "/workspace"
	cfg.Batch.RemoteBranch.Enabled = true
	cfg.Batch.RemoteBranch.Prefix = "automation/"
	cfg.Batch.RemoteBranch.RemoteName = ""

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid batch config, got: %v", err)
	}
	if cfg.Batch.RemoteBranch.RemoteName != "origin" {
		t.Fatalf("expected remote_branch.remote_name to default to origin, got %q", cfg.Batch.RemoteBranch.RemoteName)
	}
}

func TestValidateRejectsNonFinitePublicAgentCostLimits(t *testing.T) {
	for _, tt := range []struct {
		name string
		set  func(*Config)
	}{
		{name: "experiment NaN", set: func(c *Config) { c.Experiment.MaxCostPerRun = math.NaN() }},
		{name: "experiment infinity", set: func(c *Config) { c.Experiment.MaxCostPerRun = math.Inf(1) }},
		{name: "buckbot review negative", set: func(c *Config) { c.Buckbot.PerReviewBudgetUSD = -1 }},
		{name: "buckbot review infinity", set: func(c *Config) { c.Buckbot.PerReviewBudgetUSD = math.Inf(1) }},
		{name: "buckbot monthly NaN", set: func(c *Config) { c.Buckbot.MonthlyBudgetUSD = math.NaN() }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.set(cfg)
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "finite and non-negative") {
				t.Fatalf("Validate error = %v", err)
			}
		})
	}
	if err := DefaultConfig().Validate(); err != nil {
		t.Fatalf("zero/default budgets rejected: %v", err)
	}
}

func TestValidateRejectsNegativeOpenAICompatibleStreamIdleTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Providers.OpenAICompatible.StreamIdleTimeout = -time.Second
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "providers.openai_compatible.stream_idle_timeout") {
		t.Fatalf("Validate error = %v, want openai-compatible stream idle timeout error", err)
	}

	cfg = DefaultConfig()
	cfg.Providers.LiteLLM.StreamIdleTimeout = -time.Second
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "providers.litellm.stream_idle_timeout") {
		t.Fatalf("Validate error = %v, want litellm stream idle timeout error", err)
	}
}

func TestValidateRejectsNegativeOpenAICompatibleFirstContentTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Providers.OpenAICompatible.StreamFirstContentTimeout = -time.Second
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "providers.openai_compatible.stream_first_content_timeout") {
		t.Fatalf("Validate error = %v, want openai-compatible first content timeout error", err)
	}

	cfg = DefaultConfig()
	cfg.Providers.LiteLLM.StreamFirstContentTimeout = -time.Second
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "providers.litellm.stream_first_content_timeout") {
		t.Fatalf("Validate error = %v, want litellm first content timeout error", err)
	}
}

func TestValidateRejectsNegativeOpenAICompatibleFirstContentReasoningChunkLimit(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Providers.OpenAICompatible.StreamFirstContentMaxReasoningChunks = -1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "providers.openai_compatible.stream_first_content_max_reasoning_chunks") {
		t.Fatalf("Validate error = %v, want openai-compatible first content reasoning chunk limit error", err)
	}

	cfg = DefaultConfig()
	cfg.Providers.LiteLLM.StreamFirstContentMaxReasoningChunks = -1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "providers.litellm.stream_first_content_max_reasoning_chunks") {
		t.Fatalf("Validate error = %v, want litellm first content reasoning chunk limit error", err)
	}
}

func TestValidateRLMScratchpadEvictionPolicy(t *testing.T) {
	for _, policy := range []string{"", "lru", " LRU ", "fifo", " FIFO "} {
		cfg := DefaultConfig()
		cfg.RLM.Scratchpad.EvictionPolicy = policy
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate rejected valid rlm.scratchpad.eviction_policy %q: %v", policy, err)
		}
	}

	cfg := DefaultConfig()
	cfg.RLM.Scratchpad.EvictionPolicy = "lfu"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "rlm.scratchpad.eviction_policy") {
		t.Fatalf("Validate error = %v, want rlm.scratchpad.eviction_policy rejection", err)
	}
}

func TestValidateRLMScratchpadEvictionPolicyRunsBeforeBuckbotBudgetValidation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RLM.Scratchpad.EvictionPolicy = "typo"
	cfg.Buckbot.PerReviewBudgetUSD = -1
	err := cfg.Validate()
	if err == nil {
		t.Fatalf("expected validation error")
	}
	if !strings.Contains(err.Error(), "rlm.scratchpad.eviction_policy") {
		t.Fatalf("Validate error = %v, want rlm scratchpad validation before buckbot budget validation", err)
	}
}

func TestValidateRLMTiers(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RLM.Tiers = map[string]RLMTierConfig{
		"trivial":   {},
		"light":     {},
		"medium":    {},
		"heavy":     {},
		"reasoning": {},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate rejected canonical rlm.tiers keys: %v", err)
	}

	for name, tiers := range map[string]map[string]RLMTierConfig{
		"case mismatch": {"Light": {}},
		"typo":          {"ligth": {}},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.RLM.Tiers = tiers
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "rlm.tiers") {
				t.Fatalf("Validate error = %v, want rlm.tiers rejection", err)
			}
		})
	}

	cfg = DefaultConfig()
	cfg.RLM.Tiers = map[string]RLMTierConfig{"light": {MaxCostPerMillion: -0.1}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "max_cost_per_million") {
		t.Fatalf("Validate error = %v, want max_cost_per_million rejection", err)
	}

	cfg = DefaultConfig()
	cfg.RLM.Tiers = map[string]RLMTierConfig{"light": {MinContextWindow: -1}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "min_context_window") {
		t.Fatalf("Validate error = %v, want min_context_window rejection", err)
	}
}

// TestOneshotDataPolicyDefaultsToNone locks the 2026-09-04 owner decision:
// a fresh config carries the permissive default, and the zero-value
// OneshotModeConfig (e.g. a config loaded before this field existed)
// resolves to the same default via OneshotDataPolicy's nil/empty handling.
func TestOneshotDataPolicyDefaultsToNone(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Oneshot.DataPolicy != "none" {
		t.Fatalf("DefaultConfig().Oneshot.DataPolicy = %q, want %q", cfg.Oneshot.DataPolicy, "none")
	}
	if got := cfg.OneshotDataPolicy(); got != DefaultOneshotDataPolicy {
		t.Fatalf("OneshotDataPolicy() = %q, want %q", got, DefaultOneshotDataPolicy)
	}

	var zero Config
	if got := zero.OneshotDataPolicy(); got != DefaultOneshotDataPolicy {
		t.Fatalf("zero-value Config.OneshotDataPolicy() = %q, want %q", got, DefaultOneshotDataPolicy)
	}
	var nilCfg *Config
	if got := nilCfg.OneshotDataPolicy(); got != DefaultOneshotDataPolicy {
		t.Fatalf("nil Config.OneshotDataPolicy() = %q, want %q", got, DefaultOneshotDataPolicy)
	}
}

func TestValidateOneshotDataPolicy(t *testing.T) {
	for _, value := range []string{"", "none", "NONE", "zdr", "ZDR", "deny", "  deny  "} {
		cfg := DefaultConfig()
		cfg.Oneshot.DataPolicy = value
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() rejected valid oneshot.data_policy %q: %v", value, err)
		}
	}

	cfg := DefaultConfig()
	cfg.Oneshot.DataPolicy = "strict"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "oneshot.data_policy") {
		t.Fatalf("Validate error = %v, want oneshot.data_policy error", err)
	}
}
