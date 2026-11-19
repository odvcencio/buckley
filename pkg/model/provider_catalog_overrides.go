package model

import (
	"strings"

	"m31labs.dev/buckley/pkg/config"
)

// catalogOverrides adds operator-configured models and prices to a native
// provider's static catalog (providers.openai and providers.anthropic in the
// config). Native providers ship a curated list that lags new model releases;
// an operator can name a new model and assert its price without a release of
// Buckley. A configured price is authoritative, so cost-bounded requests
// (--budget) admit the model.
type catalogOverrides struct {
	prefix  string // "openai/" or "anthropic/"
	models  []string
	pricing map[string]config.ModelPricingOverride
}

func newCatalogOverrides(providerID string, settings config.ProviderSettings) catalogOverrides {
	return catalogOverrides{prefix: providerID + "/", models: settings.Models, pricing: settings.Pricing}
}

// qualify returns the catalog ID for a configured or requested name.
func (o catalogOverrides) qualify(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || strings.HasPrefix(id, o.prefix) {
		return id
	}
	return o.prefix + id
}

// price returns the configured price for a model, by bare or qualified name.
func (o catalogOverrides) price(id string) (config.ModelPricingOverride, bool) {
	if len(o.pricing) == 0 {
		return config.ModelPricingOverride{}, false
	}
	if p, ok := o.pricing[id]; ok {
		return p, true
	}
	if p, ok := o.pricing[strings.TrimPrefix(id, o.prefix)]; ok {
		return p, true
	}
	p, ok := o.pricing[o.qualify(id)]
	return p, ok
}

// withPrice applies a configured price to one entry. A configured price with
// no cache prices keeps the catalog's cache prices; one that sets any cache
// price replaces all three.
func (o catalogOverrides) withPrice(info ModelInfo) ModelInfo {
	if p, ok := o.price(info.ID); ok {
		pricing := pricingFromOverride(p)
		if p.CacheReadPerMillion == 0 && p.CacheWritePerMillion == 0 && p.CacheWrite1hPerMillion == 0 {
			pricing.CacheRead = info.Pricing.CacheRead
			pricing.CacheWrite = info.Pricing.CacheWrite
			pricing.CacheWrite1h = info.Pricing.CacheWrite1h
		}
		info.Pricing = pricing
		info.PricingKnown = true
	}
	return info
}

// pricingFromOverride converts an operator-configured price to ModelPricing.
func pricingFromOverride(p config.ModelPricingOverride) ModelPricing {
	return ModelPricing{
		Prompt:       p.InputPerMillion,
		Completion:   p.OutputPerMillion,
		CacheRead:    p.CacheReadPerMillion,
		CacheWrite:   p.CacheWritePerMillion,
		CacheWrite1h: p.CacheWrite1hPerMillion,
	}
}

// apply returns the static catalog with configured prices and the configured
// models it does not list. It does not modify static.
func (o catalogOverrides) apply(static []ModelInfo) []ModelInfo {
	if len(o.models) == 0 && len(o.pricing) == 0 {
		return static
	}
	out := make([]ModelInfo, 0, len(static)+len(o.models))
	seen := make(map[string]bool, len(static)+len(o.models))
	for _, m := range static {
		out = append(out, o.withPrice(m))
		seen[m.ID] = true
	}
	for _, name := range o.models {
		id := o.qualify(name)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, o.withPrice(ModelInfo{ID: id, Name: strings.TrimPrefix(id, o.prefix)}))
	}
	return out
}

// lookup finds a model in the catalog with overrides applied, by bare or
// qualified name.
func (o catalogOverrides) lookup(static []ModelInfo, index map[string]ModelInfo, id string) (ModelInfo, bool) {
	qualified := o.qualify(id)
	if info, ok := index[qualified]; ok {
		return o.withPrice(info), true
	}
	for _, name := range o.models {
		if o.qualify(name) == qualified {
			return o.withPrice(ModelInfo{ID: qualified, Name: strings.TrimPrefix(qualified, o.prefix)}), true
		}
	}
	return ModelInfo{}, false
}
