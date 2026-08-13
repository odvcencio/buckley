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
		return req, fmt.Errorf("cost-bounded requests cannot use prompt caching (%s): authoritative cache-write pricing is unavailable", source)
	}

	allowance, err := costBoundedOutputAllowance(req)
	if err != nil {
		return req, err
	}

	switch providerID {
	case "openrouter", "litellm":
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
	req = applyProviderTransforms(req, providerID)
	req = m.applyPromptCache(req, providerID)
	return req, nil
}

// CalculateBoundedCost calculates usage cost only when the selected provider's
// pricing is complete enough to enforce a real dollar bound. Ordinary cost
// reporting remains available through CalculateCost.
func (m *Manager) CalculateBoundedCost(modelID string, usage Usage) (float64, error) {
	selectedModel, providerID, err := m.costBoundedRoute(modelID)
	if err != nil {
		return 0, err
	}
	switch providerID {
	case "openrouter", "litellm", "openai", "anthropic", "ollama", "google":
	case "codex":
		return 0, fmt.Errorf("cost-bounded requests are unavailable for native Codex: provider pricing is not observable")
	default:
		return 0, fmt.Errorf("cost-bounded requests are unavailable for provider %q: pricing semantics are unknown", providerID)
	}

	info, err := m.GetModelInfo(selectedModel)
	if err != nil {
		return 0, fmt.Errorf("cost-bounded pricing unavailable for %s: %w", modelID, err)
	}
	pricing := info.Pricing
	if !finiteNonNegative(pricing.Prompt) || !finiteNonNegative(pricing.Completion) {
		return 0, fmt.Errorf("cost-bounded pricing invalid for %s", modelID)
	}
	if pricing.Prompt == 0 || pricing.Completion == 0 {
		if (providerID != "openrouter" && providerID != "ollama") || !info.PricingKnown {
			return 0, fmt.Errorf("cost-bounded pricing unavailable for %s: zero price is not authoritative", modelID)
		}
	}
	if usage.PromptTokens < 0 || usage.CompletionTokens < 0 {
		return 0, fmt.Errorf("cost-bounded usage invalid for %s: token counts must be non-negative", modelID)
	}
	if usage.CacheWriteTokens != 0 {
		return 0, fmt.Errorf("cost-bounded usage unavailable for %s: authoritative cache-write pricing is unavailable", modelID)
	}
	if usage.PromptTokens == 0 && usage.CompletionTokens == 0 {
		return 0, fmt.Errorf("cost-bounded usage unavailable for %s: provider reported no token counts", modelID)
	}

	return (float64(usage.PromptTokens)/1_000_000)*pricing.Prompt +
		(float64(usage.CompletionTokens)/1_000_000)*pricing.Completion, nil
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
