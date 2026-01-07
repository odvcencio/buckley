package rlm

import (
	"fmt"
	"sort"
	"strings"

	"github.com/odvcencio/buckley/pkg/model"
)

// CatalogProvider supplies the model catalog for routing decisions.
type CatalogProvider interface {
	GetCatalog() *model.ModelCatalog
}

// ProviderResolver resolves the provider ID for a model.
type ProviderResolver interface {
	ProviderIDForModel(modelID string) string
}

// CapabilityChecker reports model capability support.
type CapabilityChecker interface {
	SupportsReasoning(modelID string) bool
}

// RouterOptions configures model routing behavior.
type RouterOptions struct {
	Pins              map[Weight]string
	ProviderResolver  ProviderResolver
	CapabilityChecker CapabilityChecker
}

// ModelRouter selects models based on tier configuration and catalog metadata.
type ModelRouter struct {
	catalog           map[string]model.ModelInfo
	catalogList       []model.ModelInfo
	tiers             map[Weight]TierConfig
	pins              map[Weight]string
	providerResolver  ProviderResolver
	capabilityChecker CapabilityChecker
}

// NewModelRouterFromManager constructs a router backed by the model manager catalog.
func NewModelRouterFromManager(mgr *model.Manager, cfg Config) (*ModelRouter, error) {
	if mgr == nil {
		return nil, fmt.Errorf("model manager required")
	}
	return NewModelRouterWithCatalog(mgr.GetCatalog(), cfg, RouterOptions{
		ProviderResolver:  mgr,
		CapabilityChecker: mgr,
	})
}

// NewModelRouterWithCatalog constructs a router from a provided catalog.
func NewModelRouterWithCatalog(catalog *model.ModelCatalog, cfg Config, opts RouterOptions) (*ModelRouter, error) {
	if catalog == nil || len(catalog.Data) == 0 {
		return nil, fmt.Errorf("model catalog is empty")
	}
	cfg.Normalize()

	index := make(map[string]model.ModelInfo, len(catalog.Data))
	for _, info := range catalog.Data {
		index[info.ID] = info
	}

	router := &ModelRouter{
		catalog:           index,
		catalogList:       append([]model.ModelInfo{}, catalog.Data...),
		tiers:             cfg.Tiers,
		pins:              map[Weight]string{},
		providerResolver:  opts.ProviderResolver,
		capabilityChecker: opts.CapabilityChecker,
	}
	for weight, modelID := range opts.Pins {
		if strings.TrimSpace(modelID) != "" {
			router.pins[weight] = modelID
		}
	}
	for weight, tier := range cfg.Tiers {
		if strings.TrimSpace(tier.Model) != "" {
			router.pins[weight] = tier.Model
		}
	}

	return router, nil
}

// SetPin overrides the model choice for a given weight tier.
func (r *ModelRouter) SetPin(weight Weight, modelID string) {
	if r == nil {
		return
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return
	}
	if r.pins == nil {
		r.pins = map[Weight]string{}
	}
	r.pins[weight] = modelID
}

// Select chooses a model for the requested weight tier.
func (r *ModelRouter) Select(weight Weight) (string, error) {
	if r == nil {
		return "", fmt.Errorf("model router is nil")
	}
	weight = Weight(strings.TrimSpace(string(weight)))
	if weight == "" {
		return "", fmt.Errorf("weight required")
	}
	tier, ok := r.tiers[weight]
	if !ok {
		return "", fmt.Errorf("unknown weight tier: %s", weight)
	}

	if pin := strings.TrimSpace(r.pins[weight]); pin != "" {
		if r.modelAvailable(pin) {
			return pin, nil
		}
		// Pin not available, fall through to catalog search instead of failing
		// This allows graceful degradation when configured model isn't accessible
	}

	if len(tier.Models) > 0 {
		for _, candidate := range tier.Models {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			info, ok := r.catalog[candidate]
			if !ok {
				continue
			}
			if r.matchesTier(info, tier) {
				return info.ID, nil
			}
		}
	}

	candidates := r.filterCatalog(tier)
	if len(candidates) == 0 {
		return "", fmt.Errorf("no models available for tier %s", weight)
	}
	candidates = r.rankCandidates(candidates, tier.Prefer)
	return candidates[0].ID, nil
}

func (r *ModelRouter) modelAvailable(modelID string) bool {
	if r == nil {
		return false
	}
	_, ok := r.catalog[modelID]
	return ok
}

func (r *ModelRouter) filterCatalog(tier TierConfig) []model.ModelInfo {
	candidates := make([]model.ModelInfo, 0, len(r.catalogList))
	for _, info := range r.catalogList {
		if r.matchesTier(info, tier) {
			candidates = append(candidates, info)
		}
	}
	return candidates
}

func (r *ModelRouter) matchesTier(info model.ModelInfo, tier TierConfig) bool {
	if tier.MinContextWindow > 0 && info.ContextLength < tier.MinContextWindow {
		return false
	}
	if tier.MaxCostPerMillion > 0 {
		cost := maxCostPerMillion(info)
		if cost > 0 && cost > tier.MaxCostPerMillion {
			return false
		}
	}
	if provider := strings.TrimSpace(tier.Provider); provider != "" {
		if resolved := r.resolveProvider(info.ID); resolved != "" && resolved != provider {
			return false
		}
	}
	if len(tier.Requires) > 0 && r.capabilityChecker != nil {
		for _, req := range tier.Requires {
			switch strings.ToLower(strings.TrimSpace(req)) {
			case "extended_thinking", "reasoning":
				if !r.capabilityChecker.SupportsReasoning(info.ID) {
					return false
				}
			}
		}
	}
	return true
}

func (r *ModelRouter) resolveProvider(modelID string) string {
	if r == nil {
		return ""
	}
	if r.providerResolver != nil {
		return r.providerResolver.ProviderIDForModel(modelID)
	}
	parts := strings.SplitN(modelID, "/", 2)
	if len(parts) > 1 {
		return parts[0]
	}
	return ""
}

func (r *ModelRouter) rankCandidates(candidates []model.ModelInfo, prefer []string) []model.ModelInfo {
	ordered := append([]model.ModelInfo{}, candidates...)
	if len(prefer) == 0 {
		sort.SliceStable(ordered, func(i, j int) bool {
			return ordered[i].ID < ordered[j].ID
		})
		return ordered
	}

	prefs := normalizePrefs(prefer)
	sort.SliceStable(ordered, func(i, j int) bool {
		left := ordered[i]
		right := ordered[j]
		for _, pref := range prefs {
			switch pref {
			case "cost":
				leftCost := maxCostPerMillion(left)
				rightCost := maxCostPerMillion(right)
				if leftCost != rightCost {
					return leftCost < rightCost
				}
			case "quality":
				if left.ContextLength != right.ContextLength {
					return left.ContextLength > right.ContextLength
				}
			case "speed":
				if left.ContextLength != right.ContextLength {
					return left.ContextLength < right.ContextLength
				}
			}
		}
		return left.ID < right.ID
	})
	return ordered
}

func normalizePrefs(prefer []string) []string {
	out := make([]string, 0, len(prefer))
	for _, pref := range prefer {
		pref = strings.ToLower(strings.TrimSpace(pref))
		if pref == "" {
			continue
		}
		switch pref {
		case "cost", "quality", "speed":
			out = append(out, pref)
		}
	}
	return out
}

func maxCostPerMillion(info model.ModelInfo) float64 {
	prompt := info.Pricing.Prompt
	completion := info.Pricing.Completion
	if prompt > completion {
		return prompt
	}
	return completion
}
