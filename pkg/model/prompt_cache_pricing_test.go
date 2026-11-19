package model

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

// fablePricing is Claude Fable 5.2's published price per million tokens.
var fablePricing = ModelPricing{Prompt: 8, Completion: 40, CacheRead: 0.20, CacheWrite: 10, CacheWrite1h: 16}

func closeTo(got, want float64) bool { return math.Abs(got-want) < 1e-9 }

func TestConfiguredCachePrices(t *testing.T) {
	o := catalogOverrides{prefix: "openai/", pricing: map[string]config.ModelPricingOverride{
		"gpt-6.2-astra": {InputPerMillion: 8, OutputPerMillion: 40, CacheReadPerMillion: 0.4, CacheWritePerMillion: 10},
	}}
	info := o.withPrice(ModelInfo{ID: "openai/gpt-6.2-astra"})
	if want := (ModelPricing{Prompt: 8, Completion: 40, CacheRead: 0.4, CacheWrite: 10}); info.Pricing != want || !info.PricingKnown {
		t.Fatalf("configured pricing %#v known %v, want %#v", info.Pricing, info.PricingKnown, want)
	}

	// A configured price without cache prices keeps the catalog's.
	o = catalogOverrides{prefix: "anthropic/", pricing: map[string]config.ModelPricingOverride{
		"claude-fable-5-2": {InputPerMillion: 7, OutputPerMillion: 35},
	}}
	info = o.withPrice(ModelInfo{ID: "anthropic/claude-fable-5-2", Pricing: fablePricing})
	if want := (ModelPricing{Prompt: 7, Completion: 35, CacheRead: 0.20, CacheWrite: 10, CacheWrite1h: 16}); info.Pricing != want {
		t.Fatalf("override without cache prices: %#v, want %#v", info.Pricing, want)
	}
}

func TestPromptCacheRetentionAcceptsAnthropicLifetimes(t *testing.T) {
	for _, retention := range []string{"", "in-memory", "24h", "5m", "1h"} {
		cfg := config.DefaultConfig()
		cfg.PromptCache.Retention = retention
		if err := cfg.Validate(); err != nil && strings.Contains(err.Error(), "prompt_cache.retention") {
			t.Errorf("retention %q rejected: %v", retention, err)
		}
	}
	cfg := config.DefaultConfig()
	cfg.PromptCache.Retention = "2h"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "prompt_cache.retention") {
		t.Errorf("retention 2h accepted: %v", err)
	}
}

func TestBudgetedRequestWithCachingIsAdmittedWhenCachePricesAreKnown(t *testing.T) {
	for _, providerID := range []string{"anthropic", "openai"} {
		t.Run(providerID, func(t *testing.T) {
			mgr := newCostBoundedTestManager(providerID, ModelInfo{ID: "vendor/model", Pricing: fablePricing, PricingKnown: true})
			req := ChatRequest{
				Model:       "vendor/model",
				MaxTokens:   100,
				PromptCache: &PromptCache{Enabled: true},
				Messages:    []Message{{Role: "system", Content: "rules"}, {Role: "user", Content: "question"}},
			}
			out, err := mgr.NormalizeCostBoundedRequest(req)
			if err != nil {
				t.Fatalf("budgeted cached request refused: %v", err)
			}
			if out.PromptCache == nil || !out.PromptCache.Enabled {
				t.Fatalf("prompt cache dropped: %#v", out.PromptCache)
			}
			if providerID == "openai" && !strings.HasPrefix(out.PromptCacheKey, "buckley-") {
				t.Fatalf("prompt_cache_key = %q, want a derived key", out.PromptCacheKey)
			}
		})
	}
}

func TestBudgetedRequestWithCachingNamesTheMissingPrice(t *testing.T) {
	tests := []struct {
		name      string
		pricing   ModelPricing
		retention string
		want      string
	}{
		{"no read price", ModelPricing{Prompt: 8, Completion: 40, CacheWrite: 10}, "", "cache_read_per_million"},
		{"no write price", ModelPricing{Prompt: 8, Completion: 40, CacheRead: 0.2}, "", "cache_write_per_million"},
		{"no 1-hour price", ModelPricing{Prompt: 8, Completion: 40, CacheRead: 0.2, CacheWrite: 10}, "1h", "cache_write_1h_per_million"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := newCostBoundedTestManager("openai", ModelInfo{ID: "openai/gpt-next", Pricing: tt.pricing, PricingKnown: true})
			mgr.config.PromptCache.Retention = tt.retention
			_, err := mgr.NormalizeCostBoundedRequest(ChatRequest{Model: "openai/gpt-next", MaxTokens: 100, PromptCache: &PromptCache{Enabled: true}})
			if err == nil || !strings.Contains(err.Error(), "providers.openai.pricing.gpt-next."+tt.want) || !strings.Contains(err.Error(), "drop --budget") {
				t.Fatalf("error = %v, want the config key %s and the uncapped option", err, tt.want)
			}
			if mgr.CachePricingKnown("openai/gpt-next") {
				t.Fatal("CachePricingKnown = true with a missing price")
			}
		})
	}
}

func TestCalculateBoundedCostPricesCacheReadsAndWrites(t *testing.T) {
	mgr := newCostBoundedTestManager("anthropic", ModelInfo{ID: "vendor/model", Pricing: fablePricing, PricingKnown: true})
	usage := Usage{
		PromptTokens:        1_000_000,
		CompletionTokens:    100_000,
		PromptTokensDetails: &PromptTokensDetails{CachedTokens: 500_000},
		CacheWriteTokens:    300_000,
		CacheWrite1hTokens:  100_000,
	}
	got, err := mgr.CalculateBoundedCost("vendor/model", usage)
	if err != nil {
		t.Fatal(err)
	}
	// 200k plain at $8, 500k reads at $0.20, 200k 5-minute writes at $10,
	// 100k 1-hour writes at $16, 100k output at $40.
	if want := 1.6 + 0.1 + 2 + 1.6 + 4; !closeTo(got, want) {
		t.Fatalf("bounded cost = %v, want %v", got, want)
	}
	estimate, err := mgr.CalculateCost("vendor/model", usage)
	if err != nil || !closeTo(estimate, got) {
		t.Fatalf("estimate = %v, %v; want %v", estimate, err, got)
	}
}

func TestCalculateBoundedCostAdmissionBoundChargesTheWritePrice(t *testing.T) {
	for _, tt := range []struct {
		retention string
		want      float64
	}{{"", 10 + 40}, {"1h", 16 + 40}} {
		mgr := newCostBoundedTestManager("anthropic", ModelInfo{ID: "vendor/model", Pricing: fablePricing, PricingKnown: true})
		mgr.config.PromptCache.Retention = tt.retention
		got, err := mgr.CalculateBoundedCost("vendor/model", Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, AdmissionBound: true})
		if err != nil || !closeTo(got, tt.want) {
			t.Fatalf("retention %q: admission bound = %v, %v; want %v", tt.retention, got, err, tt.want)
		}
	}
	// An Anthropic invoice with no cache activity is charged at the input price.
	mgr := newCostBoundedTestManager("anthropic", ModelInfo{ID: "vendor/model", Pricing: fablePricing, PricingKnown: true})
	got, err := mgr.CalculateBoundedCost("vendor/model", Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000})
	if err != nil || !closeTo(got, 48) {
		t.Fatalf("plain invoice = %v, %v; want 48", got, err)
	}
}

func TestCalculateBoundedCostOpenAIChargesUnreportedWrites(t *testing.T) {
	// OpenAI reports cache reads but not writes, so input that was not read
	// is charged at the configured write price.
	mgr := newCostBoundedTestManager("openai", ModelInfo{ID: "vendor/model", Pricing: ModelPricing{Prompt: 8, Completion: 40, CacheRead: 0.4, CacheWrite: 10}, PricingKnown: true})
	got, err := mgr.CalculateBoundedCost("vendor/model", Usage{
		PromptTokens:        1_000_000,
		CompletionTokens:    0,
		PromptTokensDetails: &PromptTokensDetails{CachedTokens: 750_000},
	})
	if want := 0.25*10 + 0.75*0.4; err != nil || !closeTo(got, want) {
		t.Fatalf("bounded cost = %v, %v; want %v", got, err, want)
	}
}

// contextRequest is a one-shot request: a system prompt, then the context
// files marked as a stable prefix, then the question.
func contextRequest(question string) ChatRequest {
	return ChatRequest{
		Model: "anthropic/claude-fable-5-2",
		Messages: []Message{
			{Role: "system", Content: "You are Buckley."},
			{Role: "user", Content: []ContentPart{
				{Type: "text", Text: "<context_file path=\"spec.md\">\nlong spec\n</context_file>\n", CacheBreakpoint: true},
				{Type: "text", Text: question},
			}},
		},
		PromptCache: &PromptCache{Enabled: true},
	}
}

func TestAnthropicContextFilesAreACachedPrefix(t *testing.T) {
	mgr := &Manager{config: &config.Config{PromptCache: config.PromptCacheConfig{Retention: "1h"}}}
	provider := NewAnthropicProvider("test", "", false)
	wire := func(question string) []byte {
		req := contextRequest(question)
		original := req.Messages[1].Content.([]ContentPart)
		out := mgr.applyPromptCache(req, "anthropic")
		if original[0].CacheControl != nil {
			t.Fatal("applyPromptCache changed the caller's message")
		}
		anthReq, err := provider.toAnthropicRequest(out, false)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(anthReq)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	first, second := wire("What is the lexer's first rule?"), wire("How are errors reported?")

	var decoded struct {
		System       []map[string]any `json:"system"`
		CacheControl map[string]any   `json:"cache_control"`
		Messages     []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.System) != 1 || decoded.System[0]["cache_control"] == nil {
		t.Fatalf("system = %#v, want one block with cache_control", decoded.System)
	}
	content := decoded.Messages[0].Content
	if len(content) != 2 {
		t.Fatalf("user content = %#v, want the context block and the question", content)
	}
	control, _ := content[0]["cache_control"].(map[string]any)
	if control["type"] != "ephemeral" || control["ttl"] != "1h" || content[1]["cache_control"] != nil {
		t.Fatalf("breakpoints = %#v / %#v, want a 1-hour breakpoint on the context block only", content[0], content[1])
	}
	if decoded.CacheControl == nil {
		t.Fatal("no automatic breakpoint for the conversation tail")
	}

	// Everything up to and including the context block's breakpoint is the
	// same bytes in both requests.
	end := bytes.Index(first, []byte(`"ttl":"1h"}},{"type":"text","text":"What`))
	if end < 0 {
		t.Fatalf("context breakpoint not found in %s", first)
	}
	if !bytes.Equal(first[:end], second[:end]) {
		t.Fatalf("prefixes differ:\n%s\n%s", first[:end], second[:end])
	}
}

func TestOpenAIPromptCacheKeyFollowsTheStablePrefix(t *testing.T) {
	mgr := &Manager{config: &config.Config{PromptCache: config.PromptCacheConfig{Retention: "1h"}}}
	key := func(req ChatRequest) ChatRequest {
		req.Model = "openai/gpt-6.2-astra"
		return mgr.applyPromptCache(req, "openai")
	}

	first, second := key(contextRequest("first question")), key(contextRequest("second question"))
	if first.PromptCacheKey == "" || first.PromptCacheKey != second.PromptCacheKey {
		t.Fatalf("keys %q and %q, want the same derived key", first.PromptCacheKey, second.PromptCacheKey)
	}
	if first.PromptCacheRetention != "" {
		t.Fatalf("prompt_cache_retention = %q, want an Anthropic lifetime kept off the OpenAI wire", first.PromptCacheRetention)
	}
	other := contextRequest("first question")
	other.Messages[1].Content.([]ContentPart)[0].Text = "a different file"
	if key(other).PromptCacheKey == first.PromptCacheKey {
		t.Fatal("different context files share a key")
	}
	data, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("cache_control")) {
		t.Fatalf("OpenAI request carries cache_control: %s", data)
	}
	// GPT-5.6 and later put their own breakpoint after the newest user
	// message, so the context files carry an explicit one.
	parts := first.Messages[1].Content.([]ContentPart)
	if parts[0].PromptCacheBreakpoint == nil || parts[0].PromptCacheBreakpoint.Mode != "explicit" || parts[1].PromptCacheBreakpoint != nil {
		t.Fatalf("parts = %#v, want an explicit breakpoint on the context part only", parts)
	}
	if !bytes.Contains(data, []byte(`"prompt_cache_breakpoint":{"mode":"explicit"}`)) {
		t.Fatalf("breakpoint missing from the wire: %s", data)
	}
	older := contextRequest("first question")
	older.Model = "openai/gpt-4o"
	if out := mgr.applyPromptCache(older, "openai"); out.Messages[1].Content.([]ContentPart)[0].PromptCacheBreakpoint != nil {
		t.Fatal("a model before GPT-5.6 got an explicit breakpoint")
	}
}

func TestOpenAIExplicitCacheBreakpointModels(t *testing.T) {
	for model, want := range map[string]bool{
		"openai/gpt-6.2-astra": true, "gpt-6": true, "gpt-5.6-sol": true, "gpt-5.10": true,
		"gpt-5.5": false, "gpt-4o": false, "o3-mini": false, "openai/gpt-4.1": false,
	} {
		if got := openAIExplicitCacheBreakpoints(model); got != want {
			t.Errorf("%s: %v, want %v", model, got, want)
		}
	}
}

func TestOpenAICacheWritesArePriced(t *testing.T) {
	// GPT-5.6 and later report writes in prompt_tokens_details.
	var usage Usage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":1000000,"completion_tokens":0,"prompt_tokens_details":{"cached_tokens":500000,"cache_write_tokens":400000}}`), &usage); err != nil {
		t.Fatal(err)
	}
	if usage.CacheWrites() != 400000 {
		t.Fatalf("CacheWrites = %d, want 400000", usage.CacheWrites())
	}
	mgr := newCostBoundedTestManager("openai", ModelInfo{ID: "vendor/model", Pricing: ModelPricing{Prompt: 8, Completion: 40, CacheRead: 0.4, CacheWrite: 10}, PricingKnown: true})
	// 100k plain at $8, 500k reads at $0.40, 400k writes at $10.
	estimate, err := mgr.CalculateCost("vendor/model", usage)
	if want := 0.8 + 0.2 + 4; err != nil || !closeTo(estimate, want) {
		t.Fatalf("estimate = %v, %v; want %v", estimate, err, want)
	}
	// The cap charges the plain input at the write price as well.
	bounded, err := mgr.CalculateBoundedCost("vendor/model", usage)
	if want := 1.0 + 0.2 + 4; err != nil || !closeTo(bounded, want) {
		t.Fatalf("bounded = %v, %v; want %v", bounded, err, want)
	}
}

// imageContextRequest is a one-shot request with two screenshots in the
// cached prefix, as --context-file builds it.
func imageContextRequest(model, question string) ChatRequest {
	return ChatRequest{
		Model: model,
		Messages: []Message{
			{Role: "system", Content: "You are Buckley."},
			{Role: "user", Content: []ContentPart{
				{Type: "text", Text: "Context files.\n<context_image path=\"a.png\" type=\"image/png\">\n"},
				{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,QUFBQQ=="}},
				{Type: "text", Text: "</context_image>\n<context_image path=\"b.webp\" type=\"image/webp\">\n"},
				{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/webp;base64,QkJCQg=="}},
				{Type: "text", Text: "</context_image>\nEnd of the context files.\n", CacheBreakpoint: true},
				{Type: "text", Text: question},
			}},
		},
		PromptCache: &PromptCache{Enabled: true},
	}
}

func TestAnthropicContextImagesAreBase64BlocksUnderTheBreakpoint(t *testing.T) {
	mgr := &Manager{config: &config.Config{}}
	provider := NewAnthropicProvider("test", "", false)
	wire := func(question string) []byte {
		anthReq, err := provider.toAnthropicRequest(mgr.applyPromptCache(imageContextRequest("anthropic/claude-fable-5-2", question), "anthropic"), false)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(anthReq)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	first, second := wire("Describe each screen."), wire("Which screen is darker?")
	var decoded struct {
		Messages []struct {
			Content []struct {
				Type         string          `json:"type"`
				Text         string          `json:"text"`
				Source       json.RawMessage `json:"source"`
				CacheControl json.RawMessage `json:"cache_control"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, block := range decoded.Messages[0].Content {
		types = append(types, block.Type)
	}
	if strings.Join(types, ",") != "text,image,text,image,text,text" {
		t.Fatalf("block types = %v", types)
	}
	content := decoded.Messages[0].Content
	if string(content[1].Source) != `{"type":"base64","media_type":"image/png","data":"QUFBQQ=="}` ||
		string(content[3].Source) != `{"type":"base64","media_type":"image/webp","data":"QkJCQg=="}` {
		t.Fatalf("image sources = %s / %s", content[1].Source, content[3].Source)
	}
	if content[4].CacheControl == nil || content[5].CacheControl != nil || content[1].CacheControl != nil {
		t.Fatal("want the breakpoint after the images, on the closing text only")
	}
	end := bytes.Index(first, []byte(`"cache_control":{"type":"ephemeral"}},{"type":"text","text":"Describe`))
	if end < 0 || !bytes.Equal(first[:end], second[:end]) {
		t.Fatalf("the prefix through the images differs between questions")
	}
}

func TestOpenAIContextImagesAreDataURLsUnderTheBreakpoint(t *testing.T) {
	mgr := &Manager{config: &config.Config{}}
	first := mgr.applyPromptCache(imageContextRequest("openai/gpt-6.2-astra", "Describe each screen."), "openai")
	second := mgr.applyPromptCache(imageContextRequest("openai/gpt-6.2-astra", "Which screen is darker?"), "openai")
	if first.PromptCacheKey != second.PromptCacheKey {
		t.Fatal("the same images got different cache keys")
	}
	changed := imageContextRequest("openai/gpt-6.2-astra", "Describe each screen.")
	changed.Messages[1].Content.([]ContentPart)[1].ImageURL = &ImageURL{URL: "data:image/png;base64,Q0NDQw=="}
	if mgr.applyPromptCache(changed, "openai").PromptCacheKey == first.PromptCacheKey {
		t.Fatal("a different image kept the same cache key")
	}
	data, err := json.Marshal(first.Messages[1])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,QUFBQQ=="}}`,
		`{"type":"image_url","image_url":{"url":"data:image/webp;base64,QkJCQg=="}}`,
		`"text":"\u003c/context_image\u003e\nEnd of the context files.\n","prompt_cache_breakpoint":{"mode":"explicit"}}`,
	} {
		if !bytes.Contains(data, []byte(want)) {
			t.Fatalf("OpenAI message lacks %s:\n%s", want, data)
		}
	}
	if bytes.Count(data, []byte("prompt_cache_breakpoint")) != 1 {
		t.Fatalf("want one breakpoint, after the images:\n%s", data)
	}
}

func TestOpenRouterCatalogCachePrices(t *testing.T) {
	var gemini, claude ModelPricing
	// google/gemini-4-neon on the public models endpoint, 19 Nov 2026: the
	// write price is Gemini's storage charge, billed on top of input.
	if err := json.Unmarshal([]byte(`{"prompt":"0.000003","completion":"0.000015","input_cache_read":"0.00000015","input_cache_write":"0.0000000833333333333333"}`), &gemini); err != nil {
		t.Fatal(err)
	}
	if !closeTo(gemini.Prompt, 3) || !closeTo(gemini.Completion, 15) || !closeTo(gemini.CacheRead, 0.15) || math.Abs(gemini.CacheWrite-3.0833333) > 1e-6 {
		t.Fatalf("gemini pricing = %#v", gemini)
	}
	// An Anthropic model lists the full 5-minute write price.
	if err := json.Unmarshal([]byte(`{"prompt":"0.000008","completion":"0.00004","input_cache_read":"0.0000002","input_cache_write":"0.00001"}`), &claude); err != nil {
		t.Fatal(err)
	}
	if !closeTo(claude.CacheRead, 0.2) || !closeTo(claude.CacheWrite, 10) {
		t.Fatalf("claude pricing = %#v", claude)
	}
}

func TestOpenRouterBreakpointsFollowTheUpstreamModel(t *testing.T) {
	mgr := &Manager{config: &config.Config{PromptCache: config.PromptCacheConfig{Retention: "1h"}}}
	count := func(req ChatRequest) (int, []string) {
		data, err := json.Marshal(req.Messages)
		if err != nil {
			t.Fatal(err)
		}
		var ttls []string
		for _, msg := range req.Messages {
			parts, _ := msg.Content.([]ContentPart)
			for _, part := range parts {
				if part.CacheControl != nil {
					ttls = append(ttls, part.CacheControl.TTL)
				}
			}
		}
		return bytes.Count(data, []byte(`"cache_control"`)), ttls
	}

	// Gemini: OpenRouter uses only the last breakpoint, with a fixed TTL.
	gemini := mgr.applyPromptCache(imageContextRequest("google/gemini-4-neon", "q"), "openrouter")
	if n, ttls := count(gemini); n != 1 || ttls[0] != "" {
		t.Fatalf("gemini breakpoints = %d %v, want one with no ttl", n, ttls)
	}
	if parts := gemini.Messages[1].Content.([]ContentPart); parts[4].CacheControl == nil {
		t.Fatal("gemini breakpoint is not after the images")
	}
	if _, ok := gemini.Messages[0].Content.(string); !ok {
		t.Fatal("gemini system prompt was rewritten")
	}

	// Claude through OpenRouter: the system prompt and the context, 1-hour.
	claude := mgr.applyPromptCache(imageContextRequest("anthropic/claude-fable-5-2", "q"), "openrouter")
	if n, ttls := count(claude); n != 2 || ttls[0] != "1h" || ttls[1] != "1h" {
		t.Fatalf("claude breakpoints = %d %v, want two with ttl 1h", n, ttls)
	}
	original := imageContextRequest("anthropic/claude-fable-5-2", "q")
	mgr.applyPromptCache(original, "openrouter")
	if _, ok := original.Messages[0].Content.(string); !ok {
		t.Fatal("applyPromptCache changed the caller's system message")
	}

	// The same files give the same bytes whatever the question.
	a, _ := json.Marshal(mgr.applyPromptCache(imageContextRequest("google/gemini-4-neon", "first"), "openrouter").Messages[1].Content.([]ContentPart)[:5])
	b, _ := json.Marshal(mgr.applyPromptCache(imageContextRequest("google/gemini-4-neon", "second"), "openrouter").Messages[1].Content.([]ContentPart)[:5])
	if !bytes.Equal(a, b) {
		t.Fatal("the cached prefix differs between questions")
	}
}

func TestAddUsageSumsReportedCost(t *testing.T) {
	cost := func(v float64) *float64 { return &v }
	total := AddUsage(Usage{}, Usage{PromptTokens: 1, Cost: cost(0.01), CostDetails: &UsageCostDetails{UpstreamInferenceCost: 0.02}, PromptTokensDetails: &PromptTokensDetails{CacheWriteTokens: 5}})
	total = AddUsage(total, Usage{PromptTokens: 1, Cost: cost(0.03), IsBYOK: true, CostDetails: &UsageCostDetails{UpstreamInferenceCost: 0.04}, PromptTokensDetails: &PromptTokensDetails{CacheWriteTokens: 7}})
	if total.Cost == nil || !closeTo(*total.Cost, 0.04) || !closeTo(total.CostDetails.UpstreamInferenceCost, 0.06) || !total.IsBYOK || total.CacheWrites() != 12 {
		t.Fatalf("total = %#v", total)
	}
}

func TestCalculateBoundedCostKeepsOpenRouterReportedCost(t *testing.T) {
	cost := func(v float64) *float64 { return &v }
	mgr := newCostBoundedTestManager("openrouter", ModelInfo{ID: "google/gemini-4-neon", Pricing: ModelPricing{Prompt: 3, Completion: 15}, PricingKnown: true})
	usage := Usage{PromptTokens: 1_000_000, CompletionTokens: 0}
	// A report above Buckley's figure wins; one below does not lower the cap.
	usage.Cost = cost(4)
	if got, err := mgr.CalculateBoundedCost("google/gemini-4-neon", usage); err != nil || !closeTo(got, 4) {
		t.Fatalf("reported higher: %v, %v", got, err)
	}
	usage.Cost = cost(1)
	if got, err := mgr.CalculateBoundedCost("google/gemini-4-neon", usage); err != nil || !closeTo(got, 3) {
		t.Fatalf("reported lower: %v, %v", got, err)
	}
	// A BYOK report without the upstream charge is only OpenRouter's fee.
	usage.Cost, usage.IsBYOK = cost(0.0001), true
	if _, ok := reportedOpenRouterCost("openrouter", usage); ok {
		t.Fatal("a BYOK fee alone was taken as the total")
	}
}
