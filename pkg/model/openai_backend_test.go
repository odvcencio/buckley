package model

import (
	"context"
	"testing"

	"m31labs.dev/buckley/v2/pkg/config"
)

func TestProviderFactoryKeepsOpenAIModelsOnOpenRouterByDefault(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.APIKey = "openrouter-key"
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "openai-key"

	providers, err := providerFactory(cfg)
	if err != nil {
		t.Fatalf("providerFactory() error = %v", err)
	}
	if _, ok := providers["openrouter"]; !ok {
		t.Fatal("expected OpenRouter provider")
	}
	if _, ok := providers["openai"]; ok {
		t.Fatal("direct OpenAI provider should not be registered while OpenRouter is the selected backend")
	}
}

func TestProviderFactoryRegistersDirectOpenAIWhenExplicitlySelected(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Models.DefaultProvider = "openai"
	cfg.Providers.OpenRouter.APIKey = "openrouter-key"
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "openai-key"

	providers, err := providerFactory(cfg)
	if err != nil {
		t.Fatalf("providerFactory() error = %v", err)
	}
	if _, ok := providers["openai"]; !ok {
		t.Fatal("expected direct OpenAI provider after explicit backend selection")
	}
}

func TestProviderFactoryFallsBackToDirectOpenAIWithoutOpenRouter(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.APIKey = ""
	cfg.Providers.OpenAI.Enabled = true
	cfg.Providers.OpenAI.APIKey = "openai-key"

	providers, err := providerFactory(cfg)
	if err != nil {
		t.Fatalf("providerFactory() error = %v", err)
	}
	if _, ok := providers["openai"]; !ok {
		t.Fatal("expected direct OpenAI provider when OpenRouter is unavailable")
	}
}

func TestOpenAIModelRoutingUsesOpenRouterFallback(t *testing.T) {
	const modelID = "openai/gpt-5.4"
	cfg := &config.Config{
		Models: config.ModelConfig{
			Planning:        modelID,
			Execution:       modelID,
			Review:          modelID,
			DefaultProvider: "openrouter",
			FallbackChains:  map[string][]string{},
		},
		Providers: config.ProviderConfig{
			ModelRouting: map[string]string{
				"openai/": "openai",
				"gpt-":    "openai",
			},
		},
	}
	openRouter := &stubProvider{
		id: "openrouter",
		catalog: ModelCatalog{Data: []ModelInfo{
			{ID: modelID, ContextLength: 128_000},
		}},
	}
	mgr := &Manager{
		config:         cfg,
		providers:      map[string]Provider{"openrouter": openRouter},
		providerOrder:  []string{"openrouter"},
		catalog:        make(map[string]ModelInfo),
		providerModels: make(map[string][]string),
		modelProviders: make(map[string]string),
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if _, err := mgr.ChatCompletion(context.Background(), ChatRequest{Model: modelID}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if got := openRouter.lastRequest.Model; got != modelID {
		t.Fatalf("OpenRouter model = %q, want %q", got, modelID)
	}

	if _, err := mgr.ChatCompletion(context.Background(), ChatRequest{Model: "gpt-5.4"}); err != nil {
		t.Fatalf("ChatCompletion(raw model) error = %v", err)
	}
	if got := openRouter.lastRequest.Model; got != modelID {
		t.Fatalf("qualified OpenRouter model = %q, want %q", got, modelID)
	}
}

func TestOpenAIModelRoutingUsesDirectAPIWhenSelected(t *testing.T) {
	const modelID = "openai/gpt-5.4"
	cfg := &config.Config{
		Models: config.ModelConfig{
			Planning:        modelID,
			Execution:       modelID,
			Review:          modelID,
			DefaultProvider: "openai",
			FallbackChains:  map[string][]string{},
		},
		Providers: config.ProviderConfig{
			ModelRouting: map[string]string{"openai/": "openai"},
		},
	}
	directOpenAI := &stubProvider{
		id: "openai",
		catalog: ModelCatalog{Data: []ModelInfo{
			{ID: modelID, ContextLength: 128_000},
		}},
	}
	mgr := &Manager{
		config:         cfg,
		providers:      map[string]Provider{"openai": directOpenAI},
		providerOrder:  []string{"openai"},
		catalog:        make(map[string]ModelInfo),
		providerModels: make(map[string][]string),
		modelProviders: make(map[string]string),
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if _, err := mgr.ChatCompletion(context.Background(), ChatRequest{Model: modelID}); err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}
	if got := directOpenAI.lastRequest.Model; got != "gpt-5.4" {
		t.Fatalf("direct OpenAI model = %q, want %q", got, "gpt-5.4")
	}
}

func TestNormalizeOpenAIModelForOpenRouter(t *testing.T) {
	tests := map[string]string{
		"gpt-5.4":          "openai/gpt-5.4",
		"chatgpt-5-latest": "openai/chatgpt-5-latest",
		"o3":               "openai/o3",
		"o4-mini":          "openai/o4-mini",
		"openai/gpt-5.4":   "openai/gpt-5.4",
		"anthropic/claude": "anthropic/claude",
	}
	for input, want := range tests {
		if got := normalizeModelForProvider(input, "openrouter"); got != want {
			t.Errorf("normalizeModelForProvider(%q, openrouter) = %q, want %q", input, got, want)
		}
	}
}
