package model

import (
	"context"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
)

// Provider defines the behavior required for an LLM backend/provider.
//
//go:generate mockgen -package=model -destination=mock_provider_test.go github.com/odvcencio/buckley/pkg/model Provider
type Provider interface {
	ID() string
	FetchCatalog() (*ModelCatalog, error)
	GetModelInfo(modelID string) (*ModelInfo, error)
	ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error)
}

// TimeoutConfigurer is an optional interface for providers that can adjust request timeouts.
type TimeoutConfigurer interface {
	SetTimeout(timeout time.Duration)
}

// providerFactory builds the configured providers from config.
func providerFactory(cfg *config.Config) (map[string]Provider, error) {
	providers := make(map[string]Provider)
	networkLogsEnabled := cfg.Diagnostics.NetworkLogsEnabled

	if cfg.Providers.OpenRouter.Enabled && cfg.Providers.OpenRouter.APIKey != "" {
		client := NewClientWithOptions(cfg.Providers.OpenRouter.APIKey, cfg.Providers.OpenRouter.BaseURL, ClientOptions{
			NetworkLogsEnabled: networkLogsEnabled,
		})
		providers["openrouter"] = &OpenRouterProvider{client: client}
	}

	// OPENAI_API_KEY makes the direct provider selectable, but it does not make
	// it the implicit transport for openai/* model IDs. When OpenRouter is the
	// selected backend, those model IDs remain on OpenRouter. Selecting the
	// openai default provider (or using OpenAI as the only ready API backend)
	// opts into the native OpenAI API.
	if shouldRegisterOpenAIProvider(cfg) {
		provider := NewOpenAIProvider(cfg.Providers.OpenAI.APIKey, cfg.Providers.OpenAI.BaseURL, networkLogsEnabled)
		providers["openai"] = provider
	}

	if cfg.Providers.Anthropic.Enabled && cfg.Providers.Anthropic.APIKey != "" {
		provider := NewAnthropicProvider(cfg.Providers.Anthropic.APIKey, cfg.Providers.Anthropic.BaseURL, networkLogsEnabled)
		providers["anthropic"] = provider
	}

	if cfg.Providers.Google.Enabled && cfg.Providers.Google.APIKey != "" {
		provider := NewGoogleProvider(cfg.Providers.Google.APIKey, cfg.Providers.Google.BaseURL, networkLogsEnabled)
		providers["google"] = provider
	}

	if cfg.Providers.Ollama.Enabled {
		providers["ollama"] = NewOllamaProvider(cfg.Providers.Ollama.BaseURL, networkLogsEnabled)
	}

	if cfg.Providers.OpenAICompatible.Enabled {
		if strings.TrimSpace(cfg.Providers.OpenAICompatible.BaseURL) == "" {
			return nil, fmt.Errorf("openai_compatible provider requires base_url")
		}
		providers["openai_compatible"] = NewOpenAICompatibleProvider(cfg.Providers.OpenAICompatible, networkLogsEnabled)
	}

	// Keep existing LiteLLM configurations routable while new generic
	// OpenAI-compatible endpoints use the canonical provider name.
	if cfg.Providers.LiteLLM.Enabled {
		providers["litellm"] = NewLiteLLMProvider(cfg.Providers.LiteLLM, networkLogsEnabled)
	}

	if cfg.Providers.Codex.Enabled {
		codexCfg := cfg.Providers.Codex
		codexCfg.Models = append(codexCfg.Models, codexModelsFromConfig(cfg.Models)...)
		providers["codex"] = NewCodexCLIProvider(codexCfg, cfg.Sandbox, cfg.Approval)
	}

	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers configured; set an API key (OPENROUTER_API_KEY, OPENAI_API_KEY, ANTHROPIC_API_KEY, GOOGLE_API_KEY) or enable BUCKLEY_OLLAMA_ENABLED/BUCKLEY_OPENAI_COMPATIBLE_ENABLED/BUCKLEY_CODEX_ENABLED")
	}

	return providers, nil
}

func shouldRegisterOpenAIProvider(cfg *config.Config) bool {
	if cfg == nil || !cfg.Providers.OpenAI.Enabled || strings.TrimSpace(cfg.Providers.OpenAI.APIKey) == "" {
		return false
	}

	defaultProvider := strings.ToLower(strings.TrimSpace(cfg.Models.DefaultProvider))
	if defaultProvider == "" || defaultProvider == "openai" {
		return true
	}

	openRouterReady := cfg.Providers.OpenRouter.Enabled && strings.TrimSpace(cfg.Providers.OpenRouter.APIKey) != ""
	return !openRouterReady
}

// normalizeModelForProvider converts model IDs to the form expected by the
// selected backend. Native APIs receive their provider prefix stripped, while
// OpenRouter receives fully-qualified OpenAI slugs.
func normalizeModelForProvider(modelID, providerID string) string {
	modelID = strings.TrimSpace(modelID)
	providerID = strings.TrimSpace(providerID)

	if strings.EqualFold(providerID, "openrouter") && isUnqualifiedOpenAIModel(modelID) {
		return "openai/" + modelID
	}

	prefix := providerID + "/"
	if strings.HasPrefix(modelID, prefix) {
		return strings.TrimPrefix(modelID, prefix)
	}
	return modelID
}

func isUnqualifiedOpenAIModel(modelID string) bool {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	if modelID == "" || strings.Contains(modelID, "/") {
		return false
	}
	if strings.HasPrefix(modelID, "gpt-") || strings.HasPrefix(modelID, "chatgpt-") {
		return true
	}
	if len(modelID) < 2 || modelID[0] != 'o' || modelID[1] < '1' || modelID[1] > '9' {
		return false
	}
	return len(modelID) == 2 || modelID[2] == '-' || modelID[2] == '.' || modelID[2] == ':'
}

func messageContentToText(content any) string {
	switch v := content.(type) {
	case nil:
		return ""
	case string:
		return v
	case []ContentPart:
		var out []string
		for _, part := range v {
			if part.Type == "text" {
				out = append(out, part.Text)
			}
		}
		return strings.Join(out, "\n")
	case []any:
		parts := make([]ContentPart, 0, len(v))
		for _, val := range v {
			if partMap, ok := val.(map[string]any); ok {
				part := ContentPart{}
				if t, ok := partMap["type"].(string); ok {
					part.Type = t
				}
				if txt, ok := partMap["text"].(string); ok {
					part.Text = txt
				}
				parts = append(parts, part)
			}
		}
		return messageContentToText(parts)
	default:
		return fmt.Sprintf("%v", v)
	}
}
