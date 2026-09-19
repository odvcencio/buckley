package model

import (
	"math"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func TestManagerGetPricingAvailability(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pricing ModelPricing
		known   bool
		wantErr bool
	}{
		{"unknown", ModelPricing{}, false, true},
		{"known free", ModelPricing{}, true, false},
		{"legacy paid", ModelPricing{Prompt: 2, Completion: 6}, false, false},
		{"known paid", ModelPricing{Prompt: 2, Completion: 6}, true, false},
		{"known free input", ModelPricing{Completion: 6}, true, false},
		{"known free output", ModelPricing{Prompt: 2}, true, false},
		{"unknown input", ModelPricing{Completion: 6}, false, true},
		{"unknown output", ModelPricing{Prompt: 2}, false, true},
		{"negative input", ModelPricing{Prompt: -1, Completion: 6}, true, true},
		{"negative output", ModelPricing{Prompt: 2, Completion: -1}, true, true},
		{"nan input", ModelPricing{Prompt: math.NaN(), Completion: 6}, true, true},
		{"infinite output", ModelPricing{Prompt: 2, Completion: math.Inf(1)}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const id = "openai/pricing-test"
			cfg := config.DefaultConfig()
			cfg.Models.DefaultProvider = "openai"
			manager := &Manager{
				config:         cfg,
				providers:      map[string]Provider{"openai": NewOpenAIProvider("test", "", false)},
				catalog:        map[string]ModelInfo{id: {ID: id, Pricing: tc.pricing, PricingKnown: tc.known}},
				modelProviders: map[string]string{id: "openai"},
			}
			pricing, err := manager.GetPricing(id)
			if tc.wantErr {
				if err == nil || pricing != nil {
					t.Fatalf("GetPricing = %#v, %v; want unavailable", pricing, err)
				}
			} else if err != nil || pricing == nil || *pricing != tc.pricing {
				t.Fatalf("GetPricing = %#v, %v; want %#v", pricing, err, tc.pricing)
			}
		})
	}
}

func TestManagerUnknownModelMetadataDoesNotImplyFreePricing(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models.DefaultProvider = "openai"
	manager := &Manager{config: cfg, providers: map[string]Provider{"openai": NewOpenAIProvider("test", "", false)}}
	const id = "openai/future-model"
	info, err := manager.GetModelInfo(id)
	if err != nil || info == nil || info.ID != id || info.PricingKnown {
		t.Fatalf("metadata = %#v, %v; want available model with unknown pricing", info, err)
	}
	if pricing, err := manager.GetPricing(id); err == nil || pricing != nil {
		t.Fatalf("GetPricing = %#v, %v; want unavailable", pricing, err)
	}
}
