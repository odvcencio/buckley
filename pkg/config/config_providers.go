package config

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

	fallbackModel := providerDefaultModels[fallbackProvider]
	if fallbackModel == "" {
		return
	}

	c.replaceModelIfDefault(&c.Models.Planning, fallbackModel)
	c.replaceModelIfDefault(&c.Models.Execution, fallbackModel)
	c.replaceModelIfDefault(&c.Models.Review, fallbackModel)
}

func (c *Config) preferredReadyProvider() string {
	if c.Providers.ready(c.Models.DefaultProvider) {
		return c.Models.DefaultProvider
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
