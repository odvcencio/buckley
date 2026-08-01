package conversation

// ModelVariant is a named reasoning preset. It combines a reasoning effort
// tier with the provider-native continuation flag (decision 0001), so a
// user can cycle both settings together instead of tuning them separately.
type ModelVariant struct {
	// Name identifies the preset (for example "fast", "balanced", "deep").
	Name string
	// ReasoningEffort is the effort tier passed to the model request:
	// "minimal", "low", "medium", "high", or "xhigh".
	ReasoningEffort string
	// ProviderContinuation opts into provider-native continuation state for
	// this variant.
	ProviderContinuation bool
}

// Built-in model variants. These are config-free by design: every runtime
// gets the same four presets without reading a config file.
var (
	VariantFast = ModelVariant{
		Name:                 "fast",
		ReasoningEffort:      "minimal",
		ProviderContinuation: false,
	}
	VariantBalanced = ModelVariant{
		Name:                 "balanced",
		ReasoningEffort:      "medium",
		ProviderContinuation: false,
	}
	VariantDeep = ModelVariant{
		Name:                 "deep",
		ReasoningEffort:      "high",
		ProviderContinuation: false,
	}
	VariantDeepRetained = ModelVariant{
		Name:                 "deep+retained",
		ReasoningEffort:      "high",
		ProviderContinuation: true,
	}
)

// DefaultModelVariants is the built-in cycle order shown in the TUI.
var DefaultModelVariants = []ModelVariant{VariantFast, VariantBalanced, VariantDeep, VariantDeepRetained}

// NextModelVariant returns the variant that follows currentName in variants,
// wrapping to the first entry. When currentName does not match any variant
// (including the empty string, the "no preset chosen yet" state), it
// returns the first variant.
func NextModelVariant(variants []ModelVariant, currentName string) ModelVariant {
	if len(variants) == 0 {
		return ModelVariant{}
	}
	for i, v := range variants {
		if v.Name == currentName {
			return variants[(i+1)%len(variants)]
		}
	}
	return variants[0]
}
