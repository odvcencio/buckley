package config

import "strings"

// ReadyProviders returns identifiers for providers that have usable configuration.
func (p *ProviderConfig) ReadyProviders() []string {
	var providers []string
	for _, providerID := range []string{"openrouter", "openai", "anthropic", "google", "ollama", "litellm", "codex"} {
		if p.ready(providerID) {
			providers = append(providers, providerID)
		}
	}
	return providers
}

// HasReadyProvider returns true when at least one provider can be used.
func (p *ProviderConfig) HasReadyProvider() bool {
	return len(p.ReadyProviders()) > 0
}

func (c *Config) applyConfiguredProviderHints() {
	if c == nil {
		return
	}
	if c.usesCodexProvider() {
		c.Providers.Codex.Enabled = true
	}
}

func (p *ProviderConfig) ready(providerID string) bool {
	switch providerID {
	case "openrouter":
		return p.OpenRouter.Enabled && p.OpenRouter.APIKey != ""
	case "openai":
		return p.OpenAI.Enabled && p.OpenAI.APIKey != ""
	case "anthropic":
		return p.Anthropic.Enabled && p.Anthropic.APIKey != ""
	case "google":
		return p.Google.Enabled && p.Google.APIKey != ""
	case "ollama":
		return p.Ollama.Enabled
	case "litellm":
		return p.LiteLLM.Enabled
	case "codex":
		return p.Codex.Enabled
	default:
		return false
	}
}

func (c *Config) alignModelDefaultsWithProviders() {
	if fallbackProvider := strings.ToLower(strings.TrimSpace(c.Models.DefaultProvider)); fallbackProvider != "" &&
		fallbackProvider != "openrouter" &&
		c.Providers.ready(fallbackProvider) {
		c.Models.DefaultProvider = fallbackProvider
		c.applyProviderModelDefaults(fallbackProvider)
		return
	}

	if c.Providers.ready("openrouter") {
		if c.Models.DefaultProvider == "" {
			c.Models.DefaultProvider = "openrouter"
		}
		return
	}

	fallbackProvider := c.preferredReadyProvider()
	if fallbackProvider == "" {
		return
	}

	if c.Models.DefaultProvider == "" || c.Models.DefaultProvider == "openrouter" {
		c.Models.DefaultProvider = fallbackProvider
	}

	c.applyProviderModelDefaults(fallbackProvider)
}

func (c *Config) preferredReadyProvider() string {
	if providerID := strings.ToLower(strings.TrimSpace(c.Models.DefaultProvider)); c.Providers.ready(providerID) {
		return providerID
	}

	for _, providerID := range []string{"openai", "anthropic", "google", "litellm", "ollama", "codex"} {
		if c.Providers.ready(providerID) {
			return providerID
		}
	}

	return ""
}

func (c *Config) replaceModelIfDefault(field *string, fallback string) {
	if *field == "" || *field == defaultOpenRouterModel {
		*field = fallback
	}
}

func (c *Config) applyProviderModelDefaults(providerID string) {
	defaults, ok := providerDefaultModels[providerID]
	if !ok {
		return
	}
	c.replaceModelIfDefault(&c.Models.Planning, defaults.Planning)
	c.replaceModelIfDefault(&c.Models.Execution, defaults.Execution)
	c.replaceModelIfDefault(&c.Models.Review, defaults.Review)
	c.replaceModelIfDefault(&c.Models.Utility.Commit, defaults.UtilityCommit)
	c.replaceModelIfDefault(&c.Models.Utility.PR, defaults.UtilityPR)
	c.replaceModelIfDefault(&c.Models.Utility.Compaction, defaults.UtilityCompaction)
	c.replaceModelIfDefault(&c.Models.Utility.TodoPlan, defaults.UtilityTodoPlan)
}

func (c *Config) usesCodexProvider() bool {
	if c == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(c.Models.DefaultProvider), "codex") {
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
		if strings.HasPrefix(strings.TrimSpace(modelID), "codex/") {
			return true
		}
	}
	return false
}
