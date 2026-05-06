package config

import "strings"

var reasoningSuffixes = []struct {
	suffix string
	effort string
}{
	{suffix: "-xhigh", effort: "xhigh"},
	{suffix: "-medium", effort: "medium"},
	{suffix: "-high", effort: "high"},
	{suffix: "-low", effort: "low"},
}

// SplitReasoningSuffix separates legacy model IDs like gpt-5.4-mini-xhigh into
// the actual model ID and reasoning effort config.
func SplitReasoningSuffix(modelID string) (string, string) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return "", ""
	}
	lower := strings.ToLower(modelID)
	if !strings.Contains(lower, "gpt-5") {
		return modelID, ""
	}
	for _, candidate := range reasoningSuffixes {
		if strings.HasSuffix(lower, candidate.suffix) {
			return strings.TrimSpace(modelID[:len(modelID)-len(candidate.suffix)]), candidate.effort
		}
	}
	return modelID, ""
}

func normalizeReasoningValue(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return strings.ToLower(strings.TrimSpace(value))
	case "off", "none":
		return "off"
	case "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func (c *Config) normalizeReasoningModelIDs() {
	if c == nil {
		return
	}
	c.Models.Reasoning = normalizeReasoningValue(c.Models.Reasoning)
	c.normalizeModelID(&c.Models.Planning)
	c.normalizeModelID(&c.Models.Execution)
	c.normalizeModelID(&c.Models.Review)
	for i := range c.Models.VisionFallback {
		c.normalizeModelID(&c.Models.VisionFallback[i])
	}
	c.normalizeModelID(&c.Models.Utility.Commit)
	c.normalizeModelID(&c.Models.Utility.PR)
	c.normalizeModelID(&c.Models.Utility.Compaction)
	c.normalizeModelID(&c.Models.Utility.TodoPlan)
	for i := range c.Providers.Codex.Models {
		c.normalizeModelID(&c.Providers.Codex.Models[i])
	}
}

func (c *Config) normalizeModelID(modelID *string) {
	if c == nil || modelID == nil {
		return
	}
	normalized, effort := SplitReasoningSuffix(*modelID)
	*modelID = normalized
	if effort != "" && strings.TrimSpace(c.Models.Reasoning) == "" {
		c.Models.Reasoning = effort
	}
}

func (c *Config) applyProviderReasoningDefaults() {
	if c == nil || strings.TrimSpace(c.Models.Reasoning) != "" {
		return
	}
	switch {
	case c.usesCodexProvider():
		c.Models.Reasoning = providerDefaultReasoning["codex"]
	case c.usesOpenAIReasoningProvider():
		c.Models.Reasoning = providerDefaultReasoning["openai"]
	}
}

func (c *Config) usesOpenAIReasoningProvider() bool {
	if c == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(c.Models.DefaultProvider), "openai") {
		return true
	}
	for _, modelID := range []string{
		c.Models.Planning,
		c.Models.Execution,
		c.Models.Review,
		c.Models.Utility.Commit,
		c.Models.Utility.PR,
		c.Models.Utility.Compaction,
		c.Models.Utility.TodoPlan,
	} {
		modelID = strings.ToLower(strings.TrimSpace(modelID))
		if strings.HasPrefix(modelID, "openai/gpt-5") || strings.HasPrefix(modelID, "gpt-5") {
			return true
		}
	}
	return false
}
