package model

import (
	"encoding/json"
	"math"
	"testing"
)

func TestAnthropicProvider_HaikuEffortAndDisabledThinking(t *testing.T) {
	disabled := false
	for _, reasoning := range []*ReasoningConfig{{Effort: "low"}, {Enabled: &disabled}} {
		req, err := (&AnthropicProvider{}).toAnthropicRequest(ChatRequest{Model: "anthropic/claude-haiku-5-5", Temperature: 0.7, MaxTokens: 2048, Reasoning: reasoning}, false)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(req)
		var wire map[string]any
		_ = json.Unmarshal(data, &wire)
		if _, ok := wire["temperature"]; ok {
			t.Fatal("Haiku rejects sampling parameters")
		}
		if reasoning.Effort != "" && req.OutputConfig.Effort != "low" {
			t.Fatal("lost low-effort request")
		}
		if reasoning.Enabled != nil && (req.Thinking == nil || req.Thinking.Type != "disabled") {
			t.Fatal("lost disabled-thinking request")
		}
		if req.MaxTokens != 2048 {
			t.Fatal("lost output-token cap")
		}
	}
}

func TestManagerCalculateBoundedCost_HaikuUsesHigherTierCeiling(t *testing.T) {
	id := "anthropic/claude-haiku-5-5"
	mgr := newCostBoundedTestManager("anthropic", ModelInfo{ID: id, Pricing: ModelPricing{Prompt: 0.1, Completion: 0.5}})
	got, err := mgr.CalculateBoundedCost(id, Usage{PromptTokens: 100000, CompletionTokens: 2000})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got-0.105) > 1e-12 {
		t.Fatalf("bounded cost = %v, want cache-write/higher-tier ceiling $0.105", got)
	}
}

func TestAnthropicProvider_CacheUsageAndBoundedPricing(t *testing.T) {
	var response anthropicResponse
	if err := json.Unmarshal([]byte(`{"id":"cached","model":"claude-haiku-5-5","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":100,"cache_read_input_tokens":2000,"cache_creation_input_tokens":400,"cache_creation":{"ephemeral_1h_input_tokens":100},"output_tokens":50,"output_tokens_details":{"thinking_tokens":10}}}`), &response); err != nil {
		t.Fatal(err)
	}
	got, err := response.toChatResponse()
	if err != nil {
		t.Fatal(err)
	}
	u := got.Usage
	if u.PromptTokens != 2500 || u.TotalTokens != 2550 || u.PromptTokensDetails.CachedTokens != 2000 || u.CacheWriteTokens != 400 || u.CacheWrite1hTokens != 100 || u.CompletionTokenDetails.ReasoningTokens != 10 {
		t.Fatalf("lost cache/thinking accounting: %#v", u)
	}
	id := "anthropic/claude-haiku-5-5"
	mgr := newCostBoundedTestManager("anthropic", ModelInfo{ID: id, Pricing: ModelPricing{Prompt: 0.1, Completion: 0.5}})
	price, err := mgr.CalculateCost(id, u)
	if err != nil || math.Abs(price-0.0001125) > 1e-12 {
		t.Fatalf("cache cost = %.10f, %v", price, err)
	}
	bounded, err := mgr.CalculateBoundedCost(id, u)
	if err != nil || bounded < price {
		t.Fatalf("cache ceiling = %v, %v; billed estimate=%v", bounded, err, price)
	}
	req := ChatRequest{Model: id, MaxTokens: 2048, CacheControl: &CacheControl{Type: "ephemeral"}, Messages: []Message{{Role: "user", Content: "hello"}}}
	normalized, err := mgr.NormalizeCostBoundedRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := (&AnthropicProvider{}).toAnthropicRequest(normalized, false)
	if err != nil || wire.CacheControl == nil || wire.CacheControl.Type != "ephemeral" {
		t.Fatalf("cache control lost at native boundary: %#v, %v", wire, err)
	}
	u.CacheWrite1hTokens = 401
	if _, err := mgr.CalculateBoundedCost(id, u); err == nil {
		t.Fatal("admitted inconsistent cache usage")
	}
}
