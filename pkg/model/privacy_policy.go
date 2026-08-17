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

func shouldTryOpenRouterPrivacyFallback(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && openRouterPolicyBlocked(apiErr)
}
