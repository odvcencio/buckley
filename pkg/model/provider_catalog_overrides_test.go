package model

import (
	"encoding/json"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func TestAnthropicCatalogListsFableAndSonnetWithPrices(t *testing.T) {
	p := &AnthropicProvider{}
	for id, want := range map[string]ModelPricing{
		"anthropic/claude-fable-5-2":  {Prompt: 8, Completion: 40},
		"anthropic/claude-fable-5-1":  {Prompt: 10, Completion: 50},
		"anthropic/claude-sonnet-5-5": {Prompt: 2, Completion: 10},
	} {
		info, err := p.GetModelInfo(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if info.Pricing != want || !info.PricingKnown {
			t.Errorf("%s: pricing %#v known %v, want %#v", id, info.Pricing, info.PricingKnown, want)
		}
		bare, err := p.GetModelInfo(strings.TrimPrefix(id, "anthropic/"))
		if err != nil || bare.ID != id {
			t.Errorf("%s: the bare name did not find it (%v, %v)", id, bare, err)
		}
	}
	if _, err := p.GetModelInfo("openai/claude-fable-5-2"); err == nil {
		t.Error("a model qualified for another provider was found")
	}
}

func TestClaude5RequestRules(t *testing.T) {
	disabled := false
	for _, model := range []string{"claude-fable-5-2", "claude-fable-5-1", "claude-opus-5-5", "claude-sonnet-5-5"} {
		req, err := (&AnthropicProvider{}).toAnthropicRequest(ChatRequest{Model: "anthropic/" + model, Temperature: .7, Reasoning: &ReasoningConfig{Effort: "max"}}, false)
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		if req.Temperature != 0 || req.OutputConfig == nil || req.OutputConfig.Effort != "max" || req.Thinking != nil {
			t.Errorf("%s: temperature %v, output %#v, thinking %#v", model, req.Temperature, req.OutputConfig, req.Thinking)
		}
		if _, err := (&AnthropicProvider{}).toAnthropicRequest(ChatRequest{Model: "anthropic/" + model, Reasoning: &ReasoningConfig{Enabled: &disabled}}, false); err == nil {
			t.Errorf("%s: disabling thinking was not refused", model)
		}
		if _, err := (&AnthropicProvider{}).toAnthropicRequest(ChatRequest{Model: "anthropic/" + model, ToolChoice: "required"}, false); err == nil {
			t.Errorf("%s: a forced tool choice was not refused", model)
		}
	}
	req, err := (&AnthropicProvider{}).toAnthropicRequest(ChatRequest{Model: "anthropic/claude-haiku-5-5", ToolChoice: "required", Reasoning: &ReasoningConfig{Enabled: &disabled}}, false)
	if err != nil || req.Thinking == nil || req.Thinking.Type != "disabled" {
		t.Fatalf("Haiku 5.5 should accept disabled thinking and a forced tool choice: %#v, %v", req, err)
	}
}

func TestConfiguredModelsAndPricesReachTheNativeCatalogs(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers.Anthropic = config.ProviderSettings{Enabled: true, APIKey: "test", Models: []string{"claude-next-6"},
		Pricing: map[string]config.ModelPricingOverride{"claude-next-6": {InputPerMillion: 3, OutputPerMillion: 15}, "anthropic/claude-fable-5-2": {InputPerMillion: 7, OutputPerMillion: 35}}}
	cfg.Providers.OpenAI = config.ProviderSettings{Enabled: true, APIKey: "test", Models: []string{"gpt-6.2-sol"},
		Pricing: map[string]config.ModelPricingOverride{"gpt-6.2-sol": {InputPerMillion: 2, OutputPerMillion: 10}}}
	cfg.Models.DefaultProvider = "openai"
	providers, err := providerFactory(cfg)
	if err != nil {
		t.Fatal(err)
	}
	anth, oai := providers["anthropic"], providers["openai"]
	if anth == nil || oai == nil {
		t.Fatalf("providers: %v", providers)
	}
	for _, c := range []struct {
		p      Provider
		id     string
		prompt float64
	}{
		{anth, "anthropic/claude-next-6", 3},
		{anth, "claude-next-6", 3},
		{anth, "anthropic/claude-fable-5-2", 7},
		{oai, "openai/gpt-6.2-sol", 2},
		{oai, "gpt-6.2-sol", 2},
	} {
		info, err := c.p.GetModelInfo(c.id)
		if err != nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		if info.Pricing.Prompt != c.prompt || !info.PricingKnown {
			t.Errorf("%s: pricing %#v known %v, want prompt %v", c.id, info.Pricing, info.PricingKnown, c.prompt)
		}
	}
	catalog, err := anth.FetchCatalog()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range catalog.Data {
		found = found || m.ID == "anthropic/claude-next-6"
	}
	if !found {
		t.Error("a configured model is missing from the catalog")
	}
	if p, _ := (&AnthropicProvider{}).GetModelInfo("anthropic/claude-fable-5-2"); p.Pricing.Prompt != 8 {
		t.Error("an override changed the built-in catalog")
	}
}

func TestOpenAIChatSendsMaxCompletionTokens(t *testing.T) {
	req := openAIChatLimits(ChatRequest{Model: "gpt-6.2-sol", MaxTokens: 40})
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"max_tokens"`) || !strings.Contains(string(raw), `"max_completion_tokens":40`) {
		t.Fatalf("request %s, want max_completion_tokens 40 and no max_tokens", raw)
	}
	if got := openAIChatLimits(ChatRequest{MaxTokens: 40, MaxCompletionTokens: 10}); got.MaxCompletionTokens != 10 || got.MaxTokens != 0 {
		t.Fatalf("an explicit max_completion_tokens was replaced: %#v", got)
	}
}
