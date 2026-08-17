package model

import (
	"errors"
	"fmt"
	"strings"
)

// OpenRouterPrivacyFallback controls how a Manager handles a policy-filtered
// OpenRouter route. The fallback is deliberately opt-in because it can relax
// a strict zero-data-retention requirement.
type OpenRouterPrivacyFallback string

const (
	OpenRouterPrivacyFallbackNone                  OpenRouterPrivacyFallback = ""
	OpenRouterPrivacyFallbackZDRThenDataCollection OpenRouterPrivacyFallback = "zdr_then_data_collection_deny"
)

// ParseOpenRouterPrivacyFallback normalizes the user-facing configuration
// value and rejects unknown policies instead of silently weakening privacy.
func ParseOpenRouterPrivacyFallback(value string) (OpenRouterPrivacyFallback, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none", "off", "disabled":
		return OpenRouterPrivacyFallbackNone, nil
	case string(OpenRouterPrivacyFallbackZDRThenDataCollection):
		return OpenRouterPrivacyFallbackZDRThenDataCollection, nil
	default:
		return OpenRouterPrivacyFallbackNone, fmt.Errorf("unsupported OpenRouter privacy fallback %q", value)
	}
}

func (p OpenRouterPrivacyFallback) enabled() bool {
	return p == OpenRouterPrivacyFallbackZDRThenDataCollection
}

func openRouterPrivacyRequest(req ChatRequest, policy OpenRouterPrivacyFallback) (ChatRequest, bool) {
	if !policy.enabled() || hasExplicitOpenRouterPrivacyPolicy(req.Provider) {
		return req, false
	}
	provider := cloneAnyMap(req.Provider)
	provider["zdr"] = true
	req.Provider = provider
	return req, true
}

func openRouterDataCollectionDenyRequest(req ChatRequest) (ChatRequest, bool) {
	if !providerBool(req.Provider, "zdr") {
		return req, false
	}
	provider := cloneAnyMap(req.Provider)
	delete(provider, "zdr")
	provider["data_collection"] = "deny"
	req.Provider = provider
	return req, true
}

func hasExplicitOpenRouterPrivacyPolicy(provider map[string]any) bool {
	if provider == nil {
		return false
	}
	_, hasZDR := provider["zdr"]
	_, hasCollection := provider["data_collection"]
	return hasZDR || hasCollection
}

func providerBool(provider map[string]any, key string) bool {
	value, ok := provider[key]
	if !ok {
		return false
	}
	result, ok := value.(bool)
	return ok && result
}

// shouldTryOpenRouterPrivacyFallback reports whether the ZDR leg failed in a way
// the data-collection-deny leg can still serve.
//
// Two conditions qualify. OpenRouter answers 404 with "no endpoints" when its
// guardrails leave no zero-data-retention route at all. It answers 429 when a
// zero-data-retention route exists but is saturated upstream, which is the
// common case for a model whose ZDR endpoint pool is small: the same request
// succeeds without the flag. Treating only the 404 as eligible left the
// configured zdr_then_data_collection_deny policy unable to fire for the second
// case, so the request failed while a permitted route was available.
//
// A transient 429 never reaches this point. The client already retries rate
// limits with Retry-After backoff, so a 429 here is persistent. The check also
// runs only when Buckley injected the ZDR flag under the opt-in policy; a
// caller who pinned zero data retention explicitly never reaches this path and
// is never downgraded.
func shouldTryOpenRouterPrivacyFallback(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return openRouterPolicyBlocked(apiErr) || apiErr.IsRateLimitError()
}
