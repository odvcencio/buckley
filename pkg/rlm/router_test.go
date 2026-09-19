package rlm

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

type stubResolver struct {
	providers        map[string]string
	supportsReasoner map[string]bool
}

func (s stubResolver) ProviderIDForModel(modelID string) string {
	return s.providers[modelID]
}

func (s stubResolver) SupportsReasoning(modelID string) bool {
	return s.supportsReasoner[modelID]
}

func TestModelRouterSelectHonorsPin(t *testing.T) {
	catalog := &model.ModelCatalog{Data: []model.ModelInfo{
		{ID: "a", ContextLength: 8000},
		{ID: "b", ContextLength: 16000},
	}}

	cfg := Config{Tiers: map[Weight]TierConfig{
		WeightLight: {Prefer: []string{"cost"}},
	}}

	router, err := NewModelRouterWithCatalog(catalog, cfg, RouterOptions{
		Pins: map[Weight]string{WeightLight: "b"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	modelID, err := router.Select(WeightLight)
	if err != nil {
		t.Fatalf("unexpected select error: %v", err)
	}
	if modelID != "b" {
		t.Fatalf("expected pinned model b, got %s", modelID)
	}
}

func TestModelRouterSelectFiltersByTierConstraints(t *testing.T) {
	catalog := &model.ModelCatalog{Data: []model.ModelInfo{
		{ID: "cheap-small", ContextLength: 4000, Pricing: model.ModelPricing{Prompt: 0.2, Completion: 0.2}, PricingKnown: true},
		{ID: "expensive-large", ContextLength: 32000, Pricing: model.ModelPricing{Prompt: 12.0, Completion: 12.0}, PricingKnown: true},
		{ID: "good-fit", ContextLength: 16000, Pricing: model.ModelPricing{Prompt: 2.0, Completion: 2.5}, PricingKnown: true},
	}}

	cfg := Config{Tiers: map[Weight]TierConfig{
		WeightMedium: {
			MinContextWindow:  8000,
			MaxCostPerMillion: 10.0,
			Prefer:            []string{"cost"},
		},
	}}

	router, err := NewModelRouterWithCatalog(catalog, cfg, RouterOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	modelID, err := router.Select(WeightMedium)
	if err != nil {
		t.Fatalf("unexpected select error: %v", err)
	}
	if modelID != "good-fit" {
		t.Fatalf("expected good-fit, got %s", modelID)
	}
}

func TestModelRouterConstructorsValidateTierConfigBeforeNormalize(t *testing.T) {
	catalog := &model.ModelCatalog{Data: []model.ModelInfo{
		{ID: "known-free", ContextLength: 32000, Pricing: model.ModelPricing{}, PricingKnown: true},
	}}
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	mgr := newCoordinatorTestManager(t, server)

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "unknown tier key",
			cfg: Config{Tiers: map[Weight]TierConfig{
				Weight("Light"): {},
			}},
			want: "must be one of",
		},
		{
			name: "negative max cost",
			cfg: Config{Tiers: map[Weight]TierConfig{
				WeightLight: {MaxCostPerMillion: -0.01},
			}},
			want: "max_cost_per_million must be >= 0",
		},
		{
			name: "negative minimum context",
			cfg: Config{Tiers: map[Weight]TierConfig{
				WeightLight: {MinContextWindow: -1},
			}},
			want: "min_context_window must be >= 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+"/catalog", func(t *testing.T) {
			if _, err := NewModelRouterWithCatalog(catalog, tt.cfg, RouterOptions{}); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewModelRouterWithCatalog error = %v, want %q", err, tt.want)
			}
		})
		t.Run(tt.name+"/manager", func(t *testing.T) {
			if _, err := NewModelRouterFromManager(mgr, tt.cfg); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewModelRouterFromManager error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestModelRouterConstructorsAcceptZeroAndDefaultConfig(t *testing.T) {
	catalog := &model.ModelCatalog{Data: []model.ModelInfo{
		{ID: "known-free", ContextLength: 200000, Pricing: model.ModelPricing{}, PricingKnown: true},
	}}

	router, err := NewModelRouterWithCatalog(catalog, Config{}, RouterOptions{})
	if err != nil {
		t.Fatalf("NewModelRouterWithCatalog zero config error = %v", err)
	}
	if modelID, err := router.Select(WeightLight); err != nil || modelID != "known-free" {
		t.Fatalf("Select with zero config = %q, %v; want known-free", modelID, err)
	}

	router, err = NewModelRouterWithCatalog(catalog, DefaultConfig(), RouterOptions{})
	if err != nil {
		t.Fatalf("NewModelRouterWithCatalog default config error = %v", err)
	}
	if modelID, err := router.Select(WeightLight); err != nil || modelID != "known-free" {
		t.Fatalf("Select with default config = %q, %v; want known-free", modelID, err)
	}

	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	mgr := newCoordinatorTestManager(t, server)
	if _, err := NewModelRouterFromManager(mgr, Config{}); err != nil {
		t.Fatalf("NewModelRouterFromManager zero config error = %v", err)
	}
	if _, err := NewModelRouterFromManager(mgr, DefaultConfig()); err != nil {
		t.Fatalf("NewModelRouterFromManager default config error = %v", err)
	}
}

func TestModelRouterSelectExcludesUnknownPricingWhenTierHasPositiveCostCap(t *testing.T) {
	catalog := &model.ModelCatalog{Data: []model.ModelInfo{
		{ID: "unknown-price", ContextLength: 32000},
		{ID: "known-expensive", ContextLength: 32000, Pricing: model.ModelPricing{Prompt: 4, Completion: 4}, PricingKnown: true},
	}}
	cfg := Config{Tiers: map[Weight]TierConfig{
		WeightLight: {
			MaxCostPerMillion: 3,
			Prefer:            []string{"cost"},
		},
	}}
	router, err := NewModelRouterWithCatalog(catalog, cfg, RouterOptions{})
	if err != nil {
		t.Fatalf("unexpected router error: %v", err)
	}

	if modelID, err := router.Select(WeightLight); err == nil {
		t.Fatalf("Select returned %q, want no model because unknown/unusable prices cannot satisfy a positive cap", modelID)
	}
}

func TestModelRouterSelectAllowsAuthoritativeKnownFreeUnderCostCap(t *testing.T) {
	catalog := &model.ModelCatalog{Data: []model.ModelInfo{
		{ID: "known-free", ContextLength: 32000, Pricing: model.ModelPricing{}, PricingKnown: true},
		{ID: "unknown-zero", ContextLength: 32000},
	}}
	cfg := Config{Tiers: map[Weight]TierConfig{
		WeightLight: {
			MaxCostPerMillion: 0.01,
			Prefer:            []string{"cost"},
		},
	}}
	router, err := NewModelRouterWithCatalog(catalog, cfg, RouterOptions{})
	if err != nil {
		t.Fatalf("unexpected router error: %v", err)
	}

	modelID, err := router.Select(WeightLight)
	if err != nil {
		t.Fatalf("Select error = %v, want known-free model admitted", err)
	}
	if modelID != "known-free" {
		t.Fatalf("Select = %q, want known-free", modelID)
	}
}

func TestModelRouterCostPreferenceRanksKnownPriceAheadOfUnknown(t *testing.T) {
	catalog := &model.ModelCatalog{Data: []model.ModelInfo{
		{ID: "unknown-price", ContextLength: 32000},
		{ID: "known-cheap", ContextLength: 32000, Pricing: model.ModelPricing{Prompt: 1, Completion: 2}, PricingKnown: true},
		{ID: "known-expensive", ContextLength: 32000, Pricing: model.ModelPricing{Prompt: 4, Completion: 5}, PricingKnown: true},
	}}
	cfg := Config{Tiers: map[Weight]TierConfig{
		WeightLight: {Prefer: []string{"cost"}},
	}}
	router, err := NewModelRouterWithCatalog(catalog, cfg, RouterOptions{})
	if err != nil {
		t.Fatalf("unexpected router error: %v", err)
	}

	modelID, err := router.Select(WeightLight)
	if err != nil {
		t.Fatalf("Select error = %v", err)
	}
	if modelID != "known-cheap" {
		t.Fatalf("Select = %q, want known cheap before unknown", modelID)
	}
}

func TestModelRouterSelectRejectsPinsThatViolateTierConstraints(t *testing.T) {
	catalog := &model.ModelCatalog{Data: []model.ModelInfo{
		{ID: "unknown-price", ContextLength: 32000},
		{ID: "too-expensive", ContextLength: 32000, Pricing: model.ModelPricing{Prompt: 9, Completion: 9}, PricingKnown: true},
		{ID: "wrong-provider", ContextLength: 32000, Pricing: model.ModelPricing{Prompt: 1, Completion: 1}, PricingKnown: true},
		{ID: "too-small", ContextLength: 4000, Pricing: model.ModelPricing{Prompt: 1, Completion: 1}, PricingKnown: true},
		{ID: "non-reasoning", ContextLength: 32000, Pricing: model.ModelPricing{Prompt: 1, Completion: 1}, PricingKnown: true},
	}}
	resolver := stubResolver{
		providers: map[string]string{
			"unknown-price":  "openrouter",
			"too-expensive":  "openrouter",
			"wrong-provider": "openai",
			"too-small":      "openrouter",
			"non-reasoning":  "openrouter",
		},
		supportsReasoner: map[string]bool{},
	}

	tests := []struct {
		name  string
		model string
		tier  TierConfig
	}{
		{
			name:  "unknown price violates positive cost cap",
			model: "unknown-price",
			tier:  TierConfig{MaxCostPerMillion: 3},
		},
		{
			name:  "known price violates positive cost cap",
			model: "too-expensive",
			tier:  TierConfig{MaxCostPerMillion: 3},
		},
		{
			name:  "provider mismatch",
			model: "wrong-provider",
			tier:  TierConfig{Provider: "openrouter"},
		},
		{
			name:  "context below minimum",
			model: "too-small",
			tier:  TierConfig{MinContextWindow: 16000},
		},
		{
			name:  "required capability missing",
			model: "non-reasoning",
			tier:  TierConfig{Requires: []string{"extended_thinking"}},
		},
	}

	for _, tt := range tests {
		for _, pinSource := range []string{"tier model", "router option"} {
			t.Run(tt.name+"/"+pinSource, func(t *testing.T) {
				tier := tt.tier
				opts := RouterOptions{ProviderResolver: resolver, CapabilityChecker: resolver}
				if pinSource == "tier model" {
					tier.Model = tt.model
				} else {
					opts.Pins = map[Weight]string{WeightLight: tt.model}
				}

				router, err := NewModelRouterWithCatalog(catalog, Config{Tiers: map[Weight]TierConfig{
					WeightLight: tier,
				}}, opts)
				if err != nil {
					t.Fatalf("NewModelRouterWithCatalog: %v", err)
				}
				if modelID, err := router.Select(WeightLight); err == nil {
					t.Fatalf("Select returned %q, want constrained pin rejection", modelID)
				}
			})
		}
	}
}

func TestModelRouterSelectAppliesProviderAndReasoning(t *testing.T) {
	catalog := &model.ModelCatalog{Data: []model.ModelInfo{
		{ID: "openrouter/reasoner", ContextLength: 100000},
		{ID: "openai/fast", ContextLength: 32000},
	}}

	resolver := stubResolver{
		providers: map[string]string{
			"openrouter/reasoner": "openrouter",
			"openai/fast":         "openai",
		},
		supportsReasoner: map[string]bool{
			"openrouter/reasoner": true,
			"openai/fast":         false,
		},
	}

	cfg := Config{Tiers: map[Weight]TierConfig{
		WeightReasoning: {
			Provider: "openrouter",
			Requires: []string{"extended_thinking"},
			Prefer:   []string{"quality"},
		},
	}}

	router, err := NewModelRouterWithCatalog(catalog, cfg, RouterOptions{
		ProviderResolver:  resolver,
		CapabilityChecker: resolver,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	modelID, err := router.Select(WeightReasoning)
	if err != nil {
		t.Fatalf("unexpected select error: %v", err)
	}
	if modelID != "openrouter/reasoner" {
		t.Fatalf("expected openrouter/reasoner, got %s", modelID)
	}
}
