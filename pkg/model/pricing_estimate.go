package model

import (
	"encoding/json"
	"math"
	"strconv"
)

// EstimateRates are per-million-token USD rates used to estimate a call's cost
// when the provider reports none (for example a BYOK route). They come from the
// model catalog and are approximations, not invoice values.
type EstimateRates struct {
	Prompt     float64
	Completion float64
	// CacheRead and CacheWrite are zero when the catalog lists no cache rate;
	// callers then price those tokens at the prompt rate.
	CacheRead  float64
	CacheWrite float64
}

// EstimateRates returns catalog rates for a call that sent promptTokens input
// tokens. It is more tolerant than PricingKnown: it accepts catalog pricing that
// carries tiered "overrides" (which strict admission rejects) and applies the
// tier whose min_prompt_tokens the call reached. The second result is false when
// the catalog has no usable prompt and completion price.
func (m ModelInfo) EstimateRates(promptTokens int) (EstimateRates, bool) {
	if len(m.RawPricingJSON) == 0 {
		if m.PricingKnown {
			return EstimateRates{Prompt: m.Pricing.Prompt, Completion: m.Pricing.Completion}, validRates(m.Pricing.Prompt, m.Pricing.Completion)
		}
		return EstimateRates{}, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(m.RawPricingJSON, &raw); err != nil {
		return EstimateRates{}, false
	}
	rates, ok := ratesFromFields(raw)
	if !ok {
		return EstimateRates{}, false
	}
	if overridesRaw, present := raw["overrides"]; present {
		var overrides []map[string]json.RawMessage
		if err := json.Unmarshal(overridesRaw, &overrides); err == nil {
			bestMin := -1.0
			for _, override := range overrides {
				threshold, found := priceNumber(override["min_prompt_tokens"])
				if !found || threshold > float64(promptTokens) || threshold <= bestMin {
					continue
				}
				merged := cloneFields(raw)
				delete(merged, "overrides")
				for key, value := range override {
					merged[key] = value
				}
				if tier, tierOK := ratesFromFields(merged); tierOK {
					rates, bestMin = tier, threshold
				}
			}
		}
	}
	return rates, true
}

func ratesFromFields(fields map[string]json.RawMessage) (EstimateRates, bool) {
	prompt, okPrompt := priceNumber(fields["prompt"])
	completion, okCompletion := priceNumber(fields["completion"])
	if !okPrompt || !okCompletion || !validRates(prompt*1e6, completion*1e6) {
		return EstimateRates{}, false
	}
	rates := EstimateRates{Prompt: prompt * 1e6, Completion: completion * 1e6}
	if v, ok := priceNumber(fields["input_cache_read"]); ok && v > 0 {
		rates.CacheRead = v * 1e6
	}
	if v, ok := priceNumber(fields["input_cache_write"]); ok && v > 0 {
		rates.CacheWrite = v * 1e6
	}
	return rates, true
}

func validRates(prompt, completion float64) bool {
	for _, v := range []float64{prompt, completion} {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	// A zero price alone is not evidence of a free model; require one side
	// to be positive so an all-zero placeholder stays unknown.
	return prompt > 0 || completion > 0
}

func cloneFields(in map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// priceNumber reads a catalog number that may be a JSON number or a numeric
// string.
func priceNumber(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		f, err := strconv.ParseFloat(text, 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		return f, !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	return 0, false
}
