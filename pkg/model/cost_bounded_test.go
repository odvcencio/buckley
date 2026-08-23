package model

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func TestManagerNormalizeCostBoundedRequest_UsesProviderWireField(t *testing.T) {
	tests := []struct {
		providerID        string
		wantMaxTokens     int
		wantMaxCompletion int
	}{
		{providerID: "openrouter", wantMaxTokens: 2080},
		{providerID: "litellm", wantMaxTokens: 2080},
		{providerID: "openai", wantMaxCompletion: 2080},
		{providerID: "anthropic", wantMaxTokens: 2080},
		{providerID: "ollama", wantMaxTokens: 2080},
		{providerID: "google", wantMaxTokens: 2080},
	}

	for _, tt := range tests {
		t.Run(tt.providerID, func(t *testing.T) {
			mgr := newCostBoundedTestManager(tt.providerID, ModelInfo{
				ID:      "vendor/model",
				Pricing: ModelPricing{Prompt: 1, Completion: 2},
			})
			reasoning := &ReasoningConfig{MaxTokens: 2100}
			original := ChatRequest{
				Model:               "vendor/model",
				MaxTokens:           2120,
				MaxCompletionTokens: 2080,
				Reasoning:           reasoning,
			}

			got, err := mgr.NormalizeCostBoundedRequest(original)
			if err != nil {
				t.Fatalf("NormalizeCostBoundedRequest: %v", err)
			}
			if got.MaxTokens != tt.wantMaxTokens || got.MaxCompletionTokens != tt.wantMaxCompletion {
				t.Fatalf("wire limits = max_tokens:%d max_completion_tokens:%d, want %d/%d", got.MaxTokens, got.MaxCompletionTokens, tt.wantMaxTokens, tt.wantMaxCompletion)
			}
			if tt.providerID == "openai" {
				if got.StreamOptions == nil || !got.StreamOptions.IncludeUsage {
					t.Fatalf("OpenAI bounded stream options = %+v, want include_usage=true", got.StreamOptions)
				}
			} else if got.StreamOptions != nil {
				t.Fatalf("%s received incompatible stream options: %+v", tt.providerID, got.StreamOptions)
			}
			wantReasoning := 2080
			if tt.providerID == "openrouter" || tt.providerID == "anthropic" {
				wantReasoning = 2079
			}
			if got.Reasoning == reasoning || got.Reasoning.MaxTokens != wantReasoning {
				t.Fatalf("normalized reasoning = %#v, want copied %d-token cap", got.Reasoning, wantReasoning)
			}
			if original.MaxTokens != 2120 || original.MaxCompletionTokens != 2080 || reasoning.MaxTokens != 2100 {
				t.Fatalf("original request mutated: %+v reasoning=%+v", original, reasoning)
			}

			if tt.providerID == "openai" {
				wire, err := openAIResponseRequest(got, nil)
				if err != nil {
					t.Fatalf("openAIResponseRequest: %v", err)
				}
				if wire.MaxOutputTokens != 2080 {
					t.Fatalf("Responses max_output_tokens = %d, want 2080", wire.MaxOutputTokens)
				}
			}
		})
	}
}

func TestManagerNormalizeCostBoundedRequest_RejectsTooSmallAnthropicReasoningEnvelope(t *testing.T) {
	for _, providerID := range []string{"anthropic", "openrouter"} {
		t.Run(providerID, func(t *testing.T) {
			modelID := "anthropic/claude-test"
			mgr := newCostBoundedTestManager(providerID, ModelInfo{ID: modelID, Pricing: ModelPricing{Prompt: 1, Completion: 2}})
			_, err := mgr.NormalizeCostBoundedRequest(ChatRequest{
				Model:     modelID,
				MaxTokens: 1024,
				Reasoning: &ReasoningConfig{MaxTokens: 1024},
			})
			if err == nil || !strings.Contains(err.Error(), "cannot satisfy provider reasoning minimum 1024") {
				t.Fatalf("error = %v, want strict reasoning/output minimum rejection", err)
			}
		})
	}
}

func TestManagerNormalizeCostBoundedRequest_PinsOpenRouterAndPreflightsWireTransforms(t *testing.T) {
	info := ModelInfo{ID: "vendor/model", Pricing: ModelPricing{Prompt: 1, Completion: 2}}
	fallback := ModelInfo{ID: "vendor/fallback", Pricing: ModelPricing{Prompt: 10, Completion: 20}}
	provider := &stubProvider{
		id:      "openrouter",
		catalog: ModelCatalog{Data: []ModelInfo{info, fallback}},
	}
	mgr := &Manager{
		config: &config.Config{
			Models: config.ModelConfig{
				DefaultProvider: "openrouter",
				FallbackChains: map[string][]string{
					info.ID: {fallback.ID},
				},
			},
			Providers: config.ProviderConfig{ModelRouting: map[string]string{}},
		},
		providers:      map[string]Provider{"openrouter": provider},
		providerOrder:  []string{"openrouter"},
		catalog:        map[string]ModelInfo{info.ID: info, fallback.ID: fallback},
		providerModels: map[string][]string{"openrouter": {info.ID, fallback.ID}},
		modelProviders: map[string]string{info.ID: "openrouter", fallback.ID: "openrouter"},
	}
	originalProvider := map[string]any{"allow_fallbacks": true, "data_collection": "deny"}
	original := ChatRequest{
		Model:     info.ID,
		Models:    []string{info.ID, fallback.ID},
		MaxTokens: 200,
		Provider:  originalProvider,
		Messages: []Message{
			{Role: "system", Content: "system policy"},
			{Role: "user", Content: "review this repository"},
		},
	}

	normalized, err := mgr.NormalizeCostBoundedRequest(original)
	if err != nil {
		t.Fatalf("NormalizeCostBoundedRequest: %v", err)
	}
	if len(normalized.Models) != 0 || normalized.Provider["allow_fallbacks"] != false {
		t.Fatalf("normalized routing = models:%v provider:%v, want exact no-fallback route", normalized.Models, normalized.Provider)
	}
	if normalized.Provider["data_collection"] != "deny" {
		t.Fatalf("provider preference was lost: %v", normalized.Provider)
	}
	if originalProvider["allow_fallbacks"] != true || len(original.Models) != 2 {
		t.Fatalf("normalization mutated caller routing: models:%v provider:%v", original.Models, originalProvider)
	}
	if !reflect.DeepEqual(normalized.Transforms, []string{"middle-out"}) {
		t.Fatalf("preflight transforms = %v, want middle-out", normalized.Transforms)
	}
	twice, err := mgr.NormalizeCostBoundedRequest(normalized)
	if err != nil {
		t.Fatalf("second NormalizeCostBoundedRequest: %v", err)
	}
	if !reflect.DeepEqual(twice, normalized) {
		t.Fatalf("normalization is not idempotent:\nfirst:  %#v\nsecond: %#v", normalized, twice)
	}

	if _, err := mgr.ChatCompletion(context.Background(), normalized); !errors.Is(err, ErrOpenRouterOSSAdmissionRequired) {
		t.Fatalf("ChatCompletion error = %v, want OSS admission requirement", err)
	}
	if len(provider.requests) != 0 {
		t.Fatalf("provider requests = %d, non-ZDR preflight must stop before dispatch", len(provider.requests))
	}
}

func TestManagerNormalizeCostBoundedRequest_RejectsPromptCaching(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manager, *ChatRequest)
	}{
		{
			name: "manager configuration",
			mutate: func(mgr *Manager, _ *ChatRequest) {
				mgr.config.PromptCache = config.PromptCacheConfig{
					Enabled:   true,
					Providers: []string{"openrouter"},
				}
			},
		},
		{
			name: "per-request policy",
			mutate: func(_ *Manager, req *ChatRequest) {
				req.PromptCache = &PromptCache{Enabled: true, TailMessages: 1}
			},
		},
		{
			name: "top-level cache control",
			mutate: func(_ *Manager, req *ChatRequest) {
				req.CacheControl = &CacheControl{Type: "ephemeral"}
			},
		},
		{
			name: "OpenAI cache key",
			mutate: func(_ *Manager, req *ChatRequest) {
				req.PromptCacheKey = "stable-prefix"
			},
		},
		{
			name: "typed message decoration",
			mutate: func(_ *Manager, req *ChatRequest) {
				req.Messages[0].Content = []ContentPart{{
					Type:         "text",
					Text:         "review",
					CacheControl: &CacheControl{Type: "ephemeral"},
				}}
			},
		},
		{
			name: "decoded message decoration",
			mutate: func(_ *Manager, req *ChatRequest) {
				req.Messages[0].Content = []any{map[string]any{
					"type":          "text",
					"text":          "review",
					"cache_control": map[string]any{"type": "ephemeral"},
				}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := newCostBoundedTestManager("openrouter", ModelInfo{
				ID:      "vendor/model",
				Pricing: ModelPricing{Prompt: 1, Completion: 2},
			})
			req := ChatRequest{
				Model:     "vendor/model",
				MaxTokens: 100,
				Messages:  []Message{{Role: "user", Content: "review"}},
			}
			tt.mutate(mgr, &req)

			_, err := mgr.NormalizeCostBoundedRequest(req)
			if err == nil || !strings.Contains(err.Error(), "authoritative cache-write pricing is unavailable") {
				t.Fatalf("error = %v, want explicit prompt-cache pricing rejection", err)
			}
		})
	}
}

func TestManagerNormalizeCostBoundedRequest_IgnoresCacheConfigForOtherProvider(t *testing.T) {
	mgr := newCostBoundedTestManager("openrouter", ModelInfo{
		ID:      "vendor/model",
		Pricing: ModelPricing{Prompt: 1, Completion: 2},
	})
	mgr.config.PromptCache = config.PromptCacheConfig{Enabled: true, Providers: []string{"anthropic"}}
	if _, err := mgr.NormalizeCostBoundedRequest(ChatRequest{Model: "vendor/model", MaxTokens: 100}); err != nil {
		t.Fatalf("unrelated provider cache config rejected request: %v", err)
	}
}

func TestManagerNormalizeCostBoundedRequest_FailsClosed(t *testing.T) {
	tests := []struct {
		name    string
		mgr     *Manager
		req     ChatRequest
		wantErr string
	}{
		{
			name:    "native Codex",
			mgr:     newCostBoundedTestManager("codex", ModelInfo{ID: "openai/gpt-5.6-sol"}),
			req:     ChatRequest{Model: "openai/gpt-5.6-sol", MaxTokens: 100},
			wantErr: "native Codex",
		},
		{
			name:    "unknown provider",
			mgr:     newCostBoundedTestManager("mystery", ModelInfo{ID: "vendor/model"}),
			req:     ChatRequest{Model: "vendor/model", MaxTokens: 100},
			wantErr: "semantics are unknown",
		},
		{
			name:    "missing allowance",
			mgr:     newCostBoundedTestManager("openrouter", ModelInfo{ID: "vendor/model"}),
			req:     ChatRequest{Model: "vendor/model"},
			wantErr: "no positive output allowance",
		},
		{
			name:    "missing manager",
			mgr:     nil,
			req:     ChatRequest{Model: "vendor/model", MaxTokens: 100},
			wantErr: "require a model manager",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.mgr.NormalizeCostBoundedRequest(tt.req)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestManagerCalculateBoundedCost_RequiresAuthoritativePricingAndUsage(t *testing.T) {
	positive := ModelInfo{ID: "vendor/model", Pricing: ModelPricing{Prompt: 2, Completion: 3}}
	tests := []struct {
		name       string
		providerID string
		info       ModelInfo
		usage      Usage
		want       float64
		wantErr    string
	}{
		{
			name:       "positive pricing",
			providerID: "openai",
			info:       positive,
			usage:      Usage{PromptTokens: 1_000_000, CompletionTokens: 2_000_000},
			want:       8,
		},
		{
			name:       "missing pricing",
			providerID: "anthropic",
			info:       ModelInfo{ID: "vendor/model"},
			usage:      Usage{PromptTokens: 1, CompletionTokens: 1},
			wantErr:    "not authoritative",
		},
		{
			name:       "all-zero usage",
			providerID: "openai",
			info:       positive,
			usage:      Usage{},
			wantErr:    "reported no token counts",
		},
		{
			name:       "cache-write usage",
			providerID: "openai",
			info:       positive,
			usage:      Usage{PromptTokens: 10, CompletionTokens: 2, CacheWriteTokens: 5},
			wantErr:    "cache-write pricing is unavailable",
		},
		{
			name:       "invalid pricing",
			providerID: "openai",
			info:       ModelInfo{ID: "vendor/model", Pricing: ModelPricing{Prompt: math.NaN(), Completion: 1}},
			usage:      Usage{PromptTokens: 1, CompletionTokens: 1},
			wantErr:    "pricing invalid",
		},
		{
			name:       "unknown provider",
			providerID: "mystery",
			info:       positive,
			usage:      Usage{PromptTokens: 1, CompletionTokens: 1},
			wantErr:    "semantics are unknown",
		},
		{
			name:       "native Codex",
			providerID: "codex",
			info:       positive,
			usage:      Usage{PromptTokens: 1, CompletionTokens: 1},
			wantErr:    "native Codex",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := newCostBoundedTestManager(tt.providerID, tt.info)
			got, err := mgr.CalculateBoundedCost(tt.info.ID, tt.usage)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("CalculateBoundedCost = %v, %v; want %v, nil", got, err, tt.want)
			}
		})
	}
}

func TestManagerCalculateBoundedCost_AllowsAuthoritativeFreeOpenRouterModel(t *testing.T) {
	var info ModelInfo
	if err := json.Unmarshal([]byte(`{"id":"vendor/free","pricing":{"prompt":"0","completion":0}}`), &info); err != nil {
		t.Fatalf("decode model info: %v", err)
	}
	if !info.PricingKnown {
		t.Fatal("explicit OpenRouter pricing was not marked authoritative")
	}

	mgr := newCostBoundedTestManager("openrouter", info)
	got, err := mgr.CalculateBoundedCost(info.ID, Usage{PromptTokens: 50, CompletionTokens: 10})
	if err != nil || got != 0 {
		t.Fatalf("CalculateBoundedCost = %v, %v; want authoritative zero cost", got, err)
	}
}

func TestManagerCalculateBoundedCost_AllowsAuthoritativeLocalOllamaModel(t *testing.T) {
	info := ModelInfo{
		ID:           "ollama/qwen3:8b",
		Pricing:      ModelPricing{},
		PricingKnown: true,
	}
	mgr := newCostBoundedTestManager("ollama", info)
	got, err := mgr.CalculateBoundedCost(info.ID, Usage{PromptTokens: 50, CompletionTokens: 10})
	if err != nil || got != 0 {
		t.Fatalf("CalculateBoundedCost = %v, %v; want authoritative local zero cost", got, err)
	}
}

func TestOllamaCatalogMarksLocalPricingAuthoritative(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"name":"qwen3:8b"}]}`))
	}))
	t.Cleanup(server.Close)

	provider := NewOllamaProvider(server.URL, false)
	catalog, err := provider.FetchCatalog()
	if err != nil {
		t.Fatalf("FetchCatalog: %v", err)
	}
	if len(catalog.Data) != 1 || !catalog.Data[0].PricingKnown || catalog.Data[0].Pricing != (ModelPricing{}) {
		t.Fatalf("Ollama catalog pricing = %+v, want authoritative local zero", catalog.Data)
	}
}

func TestManagerCalculateBoundedCost_RejectsUnknownZeroLiteLLMPricing(t *testing.T) {
	info := ModelInfo{ID: "proxy/model"}
	mgr := newCostBoundedTestManager("litellm", info)
	_, err := mgr.CalculateBoundedCost(info.ID, Usage{PromptTokens: 50, CompletionTokens: 10})
	if err == nil || !strings.Contains(err.Error(), "zero price is not authoritative") {
		t.Fatalf("error = %v, want unknown zero LiteLLM pricing rejected", err)
	}
}

func TestModelInfoPricingKnown_RejectsNullPricing(t *testing.T) {
	var info ModelInfo
	if err := json.Unmarshal([]byte(`{"id":"vendor/unknown","pricing":{"prompt":null,"completion":"0"}}`), &info); err != nil {
		t.Fatalf("decode model info: %v", err)
	}
	if info.PricingKnown {
		t.Fatal("null prompt price must not be authoritative")
	}
}

func newCostBoundedTestManager(providerID string, info ModelInfo) *Manager {
	provider := &stubProvider{id: providerID, catalog: ModelCatalog{Data: []ModelInfo{info}}}
	return &Manager{
		config: &config.Config{
			Models:    config.ModelConfig{DefaultProvider: providerID},
			Providers: config.ProviderConfig{ModelRouting: map[string]string{}},
		},
		providers:      map[string]Provider{providerID: provider},
		providerOrder:  []string{providerID},
		catalog:        map[string]ModelInfo{info.ID: info},
		providerModels: map[string][]string{providerID: {info.ID}},
		modelProviders: map[string]string{info.ID: providerID},
	}
}
