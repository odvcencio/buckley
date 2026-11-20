package main

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
)

// TestEveryModelWeUseHasAPricedCostLine checks that each model the owner
// uses prints a cost, not "price unconfirmed", for usage with cache reads,
// cache writes and reasoning tokens.
func TestEveryModelWeUseHasAPricedCostLine(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models.DefaultProvider = "openai"
	cfg.Providers.OpenAI = config.ProviderSettings{Enabled: true, APIKey: "test"}
	cfg.Providers.Anthropic = config.ProviderSettings{Enabled: true, APIKey: "test"}
	cfg.Providers.ModelRouting = map[string]string{"anthropic/": "anthropic"}
	mgr, err := model.NewManager(cfg)
	if err != nil {
		t.Fatal(err)
	}
	reads, reasoning := 2000, 300
	tokens := transparency.TokenUsage{Input: 10000, Output: 500, ReportedCachedInput: &reads, ReportedCacheWrite: 1000, ReportedReasoning: &reasoning}
	usage := model.Usage{PromptTokens: 10000, CompletionTokens: 500, CacheWriteTokens: 1000,
		PromptTokensDetails:    &model.PromptTokensDetails{CachedTokens: reads},
		CompletionTokenDetails: &model.CompletionTokenDetails{ReasoningTokens: reasoning}}
	for _, id := range []string{
		"openai/gpt-6.2-sol", "openai/gpt-6.2-astra", "openai/gpt-6.2-astra-pro",
		"openai/gpt-6.1-terra", "openai/gpt-6.1-terra-pro", "openai/gpt-6.1-sol",
		"openai/gpt-6-sol", "openai/gpt-6-astra", "openai/gpt-6-luna", "openai/gpt-6-luna-pro",
		"anthropic/claude-fable-5-2", "anthropic/claude-opus-5-5", "anthropic/claude-sonnet-5-5", "anthropic/claude-haiku-5-5",
	} {
		route, err := mgr.ResolveModelRoute(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		pricing, unknown := resolveOneshotToolPricing(route, mgr)
		if unknown || pricing.InputPerMillion <= 0 || pricing.CachedInputPerMillion <= 0 || pricing.CacheWritePerMillion <= 0 {
			t.Errorf("%s: pricing %#v unknown %v, want input, cache-read and cache-write prices", id, pricing, unknown)
			continue
		}
		if transparency.CostUnknownForUsage(tokens, pricing) {
			t.Errorf("%s: cost unknown for cached usage", id)
			continue
		}
		line := formatTraceCost(&transparency.Trace{Cost: pricing.Calculate(tokens), Tokens: tokens})
		if !strings.HasPrefix(line, "Cost: $") || line == "Cost: $0.0000" {
			t.Errorf("%s: commit cost line %q", id, line)
		}
		usageLine := oneShotUsageLine(mgr, id, &agentloop.Result{ModelRequests: 1, Usage: usage}, 0)
		if strings.Contains(usageLine, "estimated_cost_usd=unknown") || !strings.Contains(usageLine, "billed_to=") {
			t.Errorf("%s: one-shot usage line %q", id, usageLine)
		}
	}
}
