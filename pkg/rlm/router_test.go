package rlm

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/model"
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
		{ID: "cheap-small", ContextLength: 4000, Pricing: model.ModelPricing{Prompt: 0.2, Completion: 0.2}},
		{ID: "expensive-large", ContextLength: 32000, Pricing: model.ModelPricing{Prompt: 12.0, Completion: 12.0}},
		{ID: "good-fit", ContextLength: 16000, Pricing: model.ModelPricing{Prompt: 2.0, Completion: 2.5}},
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
