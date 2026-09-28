package model

import (
	"strings"
)

// normalizeOpenRouterBYOKPins lowercases and trims configured pins and drops
// empty entries. Config validation already rejects empty entries; this keeps
// the Manager safe when it is built from an unvalidated config.
func normalizeOpenRouterBYOKPins(pins map[string]string) map[string]string {
	if len(pins) == 0 {
		return nil
	}
	out := make(map[string]string, len(pins))
	for prefix, slug := range pins {
		prefix = strings.ToLower(strings.TrimSpace(prefix))
		slug = strings.ToLower(strings.TrimSpace(slug))
		if prefix == "" || slug == "" {
			continue
		}
		out[prefix] = slug
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// openRouterBYOKSlug returns the provider slug pinned for modelID, choosing
// the longest matching prefix so an exact model entry beats a vendor prefix.
func openRouterBYOKSlug(modelID string, pins map[string]string) string {
	modelID = strings.ToLower(strings.TrimSpace(normalizeModelForProvider(modelID, "openrouter")))
	if modelID == "" {
		return ""
	}
	best, bestLen := "", -1
	for prefix, slug := range pins {
		if strings.HasPrefix(modelID, prefix) && len(prefix) > bestLen {
			best, bestLen = slug, len(prefix)
		}
	}
	return best
}

// openRouterBYOKRequest restricts an OpenRouter request to the provider that
// holds the operator's own key.
//
// Without the pin, OpenRouter may pick any endpoint for the model. A request
// that carries provider.zdr=true is the worst case: for openai/* models the
// zero-data-retention endpoints are Azure only, so the operator's OpenAI key is
// never used and every token bills OpenRouter credits (is_byok=false). When
// credits run low OpenRouter then rejects large prompts with HTTP 402 even
// though the operator's own key is funded, and at a zero balance it rejects
// any request whose candidate endpoints include a credit-billed one.
//
// The pin is skipped when:
//   - no pin matches the model, or the fallback chain mixes models that
//     resolve to different pins (provider.only applies to every model);
//   - the caller already chose providers with only or order;
//   - the request is a single-attempt launch request, whose provider
//     contract must stay exact.
//
// Pinning narrows routing; it never relaxes a privacy field. When a pinned
// provider has no ZDR endpoint, OpenRouter answers 404 "no endpoints matching
// your data policy" and the opt-in zdr_then_data_collection_deny fallback
// retries with data_collection=deny, keeping the pin.
func openRouterBYOKRequest(req ChatRequest, pins map[string]string) ChatRequest {
	if len(pins) == 0 || req.RetryMode == RequestRetrySingleAttempt {
		return req
	}
	if _, ok := req.Provider["only"]; ok {
		return req
	}
	if _, ok := req.Provider["order"]; ok {
		return req
	}
	slug := openRouterBYOKSlug(req.Model, pins)
	if slug == "" {
		return req
	}
	for _, modelID := range req.Models {
		if openRouterBYOKSlug(modelID, pins) != slug {
			return req
		}
	}
	provider := cloneAnyMap(req.Provider)
	provider["only"] = []string{slug}
	req.Provider = provider
	return req
}
