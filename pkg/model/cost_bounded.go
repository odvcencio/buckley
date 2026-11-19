package model

import (
	"fmt"
	"math"
	"strings"
)

// NormalizeCostBoundedRequest translates an already-clamped portable output
// allowance into the single wire field understood by the selected provider.
// It fails closed when Buckley cannot prove how the provider enforces the cap.
func (m *Manager) NormalizeCostBoundedRequest(req ChatRequest) (ChatRequest, error) {
	selectedModel, providerID, err := m.costBoundedRoute(req.Model)
	if err != nil {
		return req, err
	}
	req.Model = selectedModel
	if source, active := m.costBoundedPromptCacheSource(req, providerID); active {
		if err := m.costBoundedCachePricing(selectedModel, providerID); err != nil {
			return req, fmt.Errorf("cost-bounded requests cannot use prompt caching (%s): %w", source, err)
		}
	}

	allowance, err := costBoundedOutputAllowance(req)
	if err != nil {
		return req, err
	}

	switch providerID {
	case "openrouter", "openai_compatible", "litellm":
		req.MaxTokens = allowance
		req.MaxCompletionTokens = 0
		if providerID == "openrouter" {
			req.Models = nil
			req.Provider = cloneAnyMap(req.Provider)
			req.Provider["allow_fallbacks"] = false
		}
	case "openai":
		// Both OpenAI Chat Completions and Responses accept the modern
		// completion/output-token field through their respective adapters.
		req.MaxTokens = 0
		req.MaxCompletionTokens = allowance
		// Direct OpenAI chat streams omit usage unless include_usage is
		// requested. Capped callers must receive the authoritative usage-only
		// terminal chunk so the response can be priced before it is accepted.
		req.StreamOptions = &StreamOptions{IncludeUsage: true}
	case "anthropic", "ollama", "google":
		req.MaxTokens = allowance
		req.MaxCompletionTokens = 0
	case "codex":
		return req, fmt.Errorf("cost-bounded requests are unavailable for native Codex: provider pricing is not observable")
	default:
		return req, fmt.Errorf("cost-bounded requests are unavailable for provider %q: output cap semantics are unknown", providerID)
	}

	if req.Reasoning != nil && req.Reasoning.MaxTokens > 0 {
		reasoningLimit := allowance
		minimum := 0
		if providerID == "openrouter" || providerID == "anthropic" {
			reasoningLimit = allowance - 1
			minimum = 1
			if providerID == "anthropic" || strings.HasPrefix(strings.ToLower(selectedModel), "anthropic/") {
				minimum = 1024
			}
			if allowance <= minimum {
				return req, fmt.Errorf("cost-bounded output allowance %d cannot satisfy provider reasoning minimum %d while keeping reasoning.max_tokens strictly below the output allowance", allowance, minimum)
			}
			if req.Reasoning.MaxTokens < minimum {
				return req, fmt.Errorf("reasoning.max_tokens %d is below provider minimum %d", req.Reasoning.MaxTokens, minimum)
			}
		}
		if req.Reasoning.MaxTokens > reasoningLimit {
			reasoning := *req.Reasoning
			reasoning.MaxTokens = reasoningLimit
			req.Reasoning = &reasoning
		}
	}

	// Preflight the same deterministic decorations Manager applies immediately
	// before dispatch. The controller can now include their bytes in its input
	// bound, and the dispatch path's second application remains idempotent.
	req = m.applyFallbackChain(req, selectedModel, providerID)
	req = applyProviderTransformsWithOptions(req, providerID, m.providerTransformOptions(m.providers[providerID], selectedModel))
	req = m.applyPromptCache(req, providerID)
	return req, nil
}

// CalculateBoundedCost calculates usage cost only when the selected provider's
// pricing is complete enough to enforce a real dollar bound. Ordinary cost
// reporting remains available through CalculateCost.
//
// Cache reads are charged at the cache-read price and cache writes at the
// write price for their lifetime. A request is refused only when it reports
// cache writes and the model has no write price. Input the provider did not
// report as read or written is charged at the input price, except in two
// cases where it might have been written without a count: an admission bound
// (Usage.AdmissionBound), and a provider that does not report writes (all but
// Anthropic). There it is charged at the costliest known write price.
func (m *Manager) CalculateBoundedCost(modelID string, usage Usage) (float64, error) {
	selectedModel, providerID, err := m.costBoundedRoute(modelID)
	if err != nil {
		return 0, err
	}
	switch providerID {
	case "openrouter", "openai_compatible", "litellm", "openai", "anthropic", "ollama", "google":
	case "codex":
		return 0, fmt.Errorf("cost-bounded requests are unavailable for native Codex: provider pricing is not observable")
	default:
		return 0, fmt.Errorf("cost-bounded requests are unavailable for provider %q: pricing semantics are unknown", providerID)
	}

	info, err := m.GetModelInfo(selectedModel)
	if err != nil {
		return 0, fmt.Errorf("cost-bounded pricing unavailable for %s: %w", modelID, err)
	}
	pricing := boundedPricing(selectedModel, providerID, info.Pricing)
	if !finiteNonNegative(pricing.Prompt) || !finiteNonNegative(pricing.Completion) ||
		!finiteNonNegative(pricing.CacheRead) || !finiteNonNegative(pricing.CacheWrite) || !finiteNonNegative(pricing.CacheWrite1h) {
		return 0, fmt.Errorf("cost-bounded pricing invalid for %s", modelID)
	}
	if pricing.Prompt == 0 || pricing.Completion == 0 {
		// PricingKnown distinguishes an authoritatively free model (the
		// provider's catalog explicitly confirmed $0, or an operator
		// configured a pricing override for an endpoint that does not
		// publish catalog pricing at all, e.g. providers.openai_compatible.
		// pricing) from a zero-value ModelPricing whose price is simply
		// unavailable. It is provider-agnostic: any provider whose pricing
		// semantics are understood by the switch above may assert it.
		if !info.PricingKnown {
			return 0, fmt.Errorf("cost-bounded pricing unavailable for %s: zero price is not authoritative; %s, or drop --budget to run uncapped", modelID, cachePriceHint(providerID, selectedModel, "input_per_million and output_per_million"))
		}
	}
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 {
		return 0, fmt.Errorf("cost-bounded usage invalid for %s: token counts must be non-negative", modelID)
	}
	reads := 0
	if usage.PromptTokensDetails != nil {
		reads = usage.PromptTokensDetails.CachedTokens
	}
	usage.CacheWriteTokens = usage.CacheWrites()
	if reads < 0 || usage.CacheWriteTokens < 0 || usage.CacheWrite1hTokens < 0 || reads > usage.PromptTokens ||
		usage.CacheWriteTokens > usage.PromptTokens-reads || usage.CacheWrite1hTokens > usage.CacheWriteTokens {
		return 0, fmt.Errorf("cost-bounded cache usage is inconsistent for %s", modelID)
	}
	if usage.Estimated {
		return 0, fmt.Errorf("cost-bounded usage unavailable for %s: locally estimated token counts are not authoritative", modelID)
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		return 0, fmt.Errorf("cost-bounded usage unavailable for %s: provider reported no token counts", modelID)
	}
	shortWrites := usage.CacheWriteTokens - usage.CacheWrite1hTokens
	if shortWrites > 0 && pricing.CacheWrite == 0 {
		return 0, fmt.Errorf("cost-bounded usage unavailable for %s: no cache-write price; %s", modelID, cachePriceHint(providerID, selectedModel, "cache_write_per_million"))
	}
	if usage.CacheWrite1hTokens > 0 && pricing.CacheWrite1h == 0 {
		return 0, fmt.Errorf("cost-bounded usage unavailable for %s: no 1-hour cache-write price; %s", modelID, cachePriceHint(providerID, selectedModel, "cache_write_1h_per_million"))
	}

	readPrice := pricing.CacheRead
	if readPrice == 0 {
		// Without a read price a cache read costs at most the input price.
		readPrice = pricing.Prompt
	}
	plainPrice := pricing.Prompt
	if usage.AdmissionBound || providerID != "anthropic" {
		plainPrice = max(plainPrice, pricing.CacheWrite)
		if usage.AdmissionBound && m.promptCacheTTL(nil) == "1h" {
			plainPrice = max(plainPrice, pricing.CacheWrite1h)
		}
	}
	plain := usage.PromptTokens - reads - usage.CacheWriteTokens
	cost := (float64(plain)*plainPrice +
		float64(reads)*readPrice +
		float64(shortWrites)*pricing.CacheWrite +
		float64(usage.CacheWrite1hTokens)*pricing.CacheWrite1h +
		float64(usage.CompletionTokens)*pricing.Completion) / 1_000_000
	if reported, ok := reportedOpenRouterCost(providerID, usage); ok && !usage.AdmissionBound {
		// OpenRouter's own charge is the invoice; the cap keeps the larger
		// figure in case the report leaves something out.
		cost = max(cost, reported)
	}
	return cost, nil
}

// reportedOpenRouterCost returns the total OpenRouter reported for a call:
// its own charge, plus the upstream provider's charge on a BYOK call. A BYOK
// call that does not report the upstream charge has no usable total, because
// usage.cost is then only OpenRouter's fee.
func reportedOpenRouterCost(providerID string, usage Usage) (float64, bool) {
	if providerID != "openrouter" || usage.Cost == nil {
		return 0, false
	}
	if usage.IsBYOK && (usage.CostDetails == nil || usage.CostDetails.UpstreamInferenceCost <= 0) {
		return 0, false
	}
	total, ok := usage.TotalCostUSD()
	if !ok || !finiteNonNegative(total) {
		return 0, false
	}
	return total, true
}

// costBoundedCachePricing returns an error naming the missing price when a
// capped request cannot use prompt caching on this model.
func (m *Manager) costBoundedCachePricing(selectedModel, providerID string) error {
	info, err := m.GetModelInfo(selectedModel)
	if err != nil {
		return fmt.Errorf("model pricing unavailable: %w", err)
	}
	pricing := boundedPricing(selectedModel, providerID, info.Pricing)
	missing := ""
	switch {
	case pricing.CacheRead <= 0:
		missing = "cache_read_per_million"
	case pricing.CacheWrite <= 0:
		missing = "cache_write_per_million"
	case m.promptCacheTTL(nil) == "1h" && pricing.CacheWrite1h <= 0:
		missing = "cache_write_1h_per_million"
	}
	if missing == "" {
		return nil
	}
	return fmt.Errorf("%s has no %s price; %s, or drop --budget to run uncapped", selectedModel, strings.ReplaceAll(strings.TrimSuffix(missing, "_per_million"), "_", " "), cachePriceHint(providerID, selectedModel, missing))
}

// CachePricingKnown reports whether a spend cap can price prompt caching on
// this model: it has cache-read and cache-write prices.
func (m *Manager) CachePricingKnown(modelID string) bool {
	selectedModel, providerID, err := m.costBoundedRoute(modelID)
	if err != nil {
		return false
	}
	return m.costBoundedCachePricing(selectedModel, providerID) == nil
}

// cachePriceHint names the config key that sets a missing cache price.
func cachePriceHint(providerID, selectedModel, key string) string {
	switch providerID {
	case "anthropic", "openai", "openai_compatible", "litellm":
		name := strings.TrimPrefix(selectedModel, providerID+"/")
		return fmt.Sprintf("set providers.%s.pricing.%s.%s in the config", providerID, name, key)
	default:
		return fmt.Sprintf("provider %s publishes no cache price Buckley can read", providerID)
	}
}

// boundedPricing returns the prices a spend cap uses. Anthropic models with a
// long-prompt price tier use the higher tier.
func boundedPricing(selectedModel, providerID string, catalog ModelPricing) ModelPricing {
	if providerID == "anthropic" {
		if tier, ok := nativeAnthropicPricing(selectedModel, 1000000); ok && tier.Prompt >= catalog.Prompt {
			return tier
		}
	}
	return catalog
}

// nativeAnthropicPricing returns the price tier for Anthropic models whose
// price depends on prompt length, with cache prices: reads at the model's
// read multiplier, 5-minute writes at 1.25x and 1-hour writes at 2x input.
func nativeAnthropicPricing(modelID string, promptTokens int) (ModelPricing, bool) {
	var pricing ModelPricing
	read := 0.1
	switch normalizeModelForProvider(modelID, "anthropic") {
	case "claude-haiku-5-5":
		pricing = ModelPricing{Prompt: 0.10, Completion: 0.50}
		if promptTokens > 100000 {
			pricing = ModelPricing{Prompt: 0.50, Completion: 2.50}
		}
	case "claude-opus-5-5":
		pricing, read = ModelPricing{Prompt: 4, Completion: 20}, 0.05
	default:
		return ModelPricing{}, false
	}
	pricing.CacheRead = pricing.Prompt * read
	pricing.CacheWrite = pricing.Prompt * 1.25
	pricing.CacheWrite1h = pricing.Prompt * 2
	return pricing, true
}

func (m *Manager) costBoundedPromptCacheSource(req ChatRequest, providerID string) (string, bool) {
	if req.PromptCache != nil && req.PromptCache.Enabled {
		return "request prompt-cache policy", true
	}
	if req.CacheControl != nil {
		return "top-level cache_control", true
	}
	if strings.TrimSpace(req.PromptCacheKey) != "" || strings.TrimSpace(req.PromptCacheRetention) != "" {
		return "OpenAI prompt-cache controls", true
	}
	for _, message := range req.Messages {
		if contentHasCacheControl(message.Content) {
			return "message cache_control", true
		}
	}
	if m != nil && m.config != nil {
		cfg := m.config.PromptCache
		if cfg.Enabled && (len(cfg.Providers) == 0 || containsString(cfg.Providers, providerID)) {
			return "manager prompt-cache configuration", true
		}
	}
	return "", false
}

func contentHasCacheControl(content any) bool {
	switch value := content.(type) {
	case ContentPart:
		return value.CacheControl != nil
	case []ContentPart:
		for _, part := range value {
			if part.CacheControl != nil {
				return true
			}
		}
	case []any:
		for _, part := range value {
			if contentHasCacheControl(part) {
				return true
			}
		}
	case map[string]any:
		if cacheControl, ok := value["cache_control"]; ok && cacheControl != nil {
			return true
		}
		for _, nested := range value {
			if contentHasCacheControl(nested) {
				return true
			}
		}
	}
	return false
}

func (m *Manager) costBoundedRoute(modelID string) (string, string, error) {
	if m == nil {
		return "", "", fmt.Errorf("cost-bounded requests require a model manager")
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return "", "", fmt.Errorf("cost-bounded requests require a model")
	}
	selectedModel, provider := m.resolveModel(modelID)
	if provider == nil {
		return "", "", fmt.Errorf("cost-bounded requests have no provider for model %s", modelID)
	}
	providerID := strings.TrimSpace(provider.ID())
	if providerID == "" {
		return "", "", fmt.Errorf("cost-bounded requests resolved an unnamed provider for model %s", modelID)
	}
	return selectedModel, providerID, nil
}

func costBoundedOutputAllowance(req ChatRequest) (int, error) {
	allowance := 0
	for _, candidate := range []int{req.MaxTokens, req.MaxCompletionTokens} {
		if candidate <= 0 {
			continue
		}
		if allowance == 0 || candidate < allowance {
			allowance = candidate
		}
	}
	if allowance == 0 {
		return 0, fmt.Errorf("cost-bounded request for %s has no positive output allowance", req.Model)
	}
	return allowance, nil
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
