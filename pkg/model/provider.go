package model

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
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

	if cfg.Providers.OpenAI.Enabled && cfg.Providers.OpenAI.APIKey != "" {
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

	if cfg.Providers.LiteLLM.Enabled {
		providers["litellm"] = NewLiteLLMProvider(cfg.Providers.LiteLLM, networkLogsEnabled)
	}

	if len(providers) == 0 {
		return nil, fmt.Errorf("no providers configured; set an API key (OPENROUTER_API_KEY, OPENAI_API_KEY, ANTHROPIC_API_KEY, GOOGLE_API_KEY) or enable BUCKLEY_OLLAMA_ENABLED/BUCKLEY_LITELLM_ENABLED")
	}

	return providers, nil
}

// normalizeModelForProvider strips provider prefixes (openai/, anthropic/, etc.)
// before sending requests to the underlying APIs.
func normalizeModelForProvider(modelID, providerID string) string {
	prefix := providerID + "/"
	if strings.HasPrefix(modelID, prefix) {
		return strings.TrimPrefix(modelID, prefix)
	}
	return modelID
}

func messageContentToText(content any) string {
	switch v := content.(type) {
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
