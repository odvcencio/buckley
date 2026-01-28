package model

import (
	"context"
	"fmt"
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
)

type stubProvider struct {
	id          string
	catalog     ModelCatalog
	lastRequest ChatRequest
}

func (s *stubProvider) ID() string { return s.id }

func (s *stubProvider) FetchCatalog() (*ModelCatalog, error) {
	return &s.catalog, nil
}

func (s *stubProvider) GetModelInfo(modelID string) (*ModelInfo, error) {
	for _, info := range s.catalog.Data {
		if info.ID == modelID {
			return &info, nil
		}
	}
	return nil, fmt.Errorf("model not found: %s", modelID)
}

func (s *stubProvider) ChatCompletion(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	s.lastRequest = req
	return &ChatResponse{}, nil
}

func (s *stubProvider) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	s.lastRequest = req
	chunks := make(chan StreamChunk)
	errs := make(chan error, 1)
	close(chunks)
	close(errs)
	return chunks, errs
}

func TestInitializeFallsBackWhenModelsMissing(t *testing.T) {
	cfg := &config.Config{
		Models: config.ModelConfig{
			DefaultProvider: "p1",
			FallbackChains:  map[string][]string{},
		},
		Providers: config.ProviderConfig{
			ModelRouting: map[string]string{},
		},
	}
	prov := &stubProvider{
		id: "p1",
		catalog: ModelCatalog{
			Data: []ModelInfo{
				{ID: "p1/model-a", ContextLength: 128_000},
				{ID: "p1/model-b", ContextLength: 64_000},
			},
		},
	}

	mgr := &Manager{
		config:         cfg,
		providers:      map[string]Provider{"p1": prov},
		providerOrder:  []string{"p1"},
		catalog:        make(map[string]ModelInfo),
		providerModels: make(map[string][]string),
		modelProviders: make(map[string]string),
	}

	// Leave planning/execution/review empty to force fallback selection.
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	want := "p1/model-a"
	if cfg.Models.Planning != want || cfg.Models.Execution != want || cfg.Models.Review != want {
		t.Fatalf("fallback models not applied: got planning=%q execution=%q review=%q", cfg.Models.Planning, cfg.Models.Execution, cfg.Models.Review)
	}
}

func TestInitializeReplacesMissingConfiguredModel(t *testing.T) {
	cfg := &config.Config{
		Models: config.ModelConfig{
			Planning:        "p1/missing",
			Execution:       "p1/existing",
			Review:          "p1/model-b",
			DefaultProvider: "p1",
			FallbackChains:  map[string][]string{},
		},
		Providers: config.ProviderConfig{
			ModelRouting: map[string]string{},
		},
	}
	prov := &stubProvider{
		id: "p1",
		catalog: ModelCatalog{
			Data: []ModelInfo{
				{ID: "p1/model-b", ContextLength: 64_000},
				{ID: "p1/existing", ContextLength: 32_000},
			},
		},
	}

	mgr := &Manager{
		config:         cfg,
		providers:      map[string]Provider{"p1": prov},
		providerOrder:  []string{"p1"},
		catalog:        make(map[string]ModelInfo),
		providerModels: make(map[string][]string),
		modelProviders: make(map[string]string),
	}

	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if cfg.Models.Planning != "p1/model-b" {
		t.Fatalf("expected planning model to fall back to p1/model-b, got %q", cfg.Models.Planning)
	}
}

func TestProviderRoutingPrefersExplicitMapping(t *testing.T) {
	cfg := &config.Config{
		Models: config.ModelConfig{
			DefaultProvider: "p1",
			FallbackChains:  map[string][]string{},
		},
		Providers: config.ProviderConfig{
			ModelRouting: map[string]string{"special": "p2"},
		},
	}

	prov1 := &stubProvider{
		id: "p1",
		catalog: ModelCatalog{
			Data: []ModelInfo{{ID: "p1/model-a", ContextLength: 16_000}},
		},
	}
	prov2 := &stubProvider{
		id: "p2",
		catalog: ModelCatalog{
			Data: []ModelInfo{{ID: "special/model-b", ContextLength: 16_000}},
		},
	}

	mgr := &Manager{
		config:         cfg,
		providers:      map[string]Provider{"p1": prov1, "p2": prov2},
		providerOrder:  []string{"p1", "p2"},
		catalog:        make(map[string]ModelInfo),
		providerModels: make(map[string][]string),
		modelProviders: make(map[string]string),
	}

	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	provider := mgr.providerForModel("special-123")
	if provider == nil || provider.ID() != "p2" {
		t.Fatalf("expected provider p2 for special-prefixed model, got %v", provider)
	}
}

func TestProviderIDForModelUsesCatalogAndRouting(t *testing.T) {
	cfg := &config.Config{
		Models: config.ModelConfig{
			DefaultProvider: "p1",
			FallbackChains:  map[string][]string{},
		},
		Providers: config.ProviderConfig{
			ModelRouting: map[string]string{"special": "p2"},
		},
	}

	prov1 := &stubProvider{
		id: "p1",
		catalog: ModelCatalog{
			Data: []ModelInfo{{ID: "p1/model-a", ContextLength: 16_000}},
		},
	}
	prov2 := &stubProvider{
		id: "p2",
		catalog: ModelCatalog{
			Data: []ModelInfo{{ID: "special/model-b", ContextLength: 16_000}},
		},
	}

	mgr := &Manager{
		config:         cfg,
		providers:      map[string]Provider{"p1": prov1, "p2": prov2},
		providerOrder:  []string{"p1", "p2"},
		catalog:        make(map[string]ModelInfo),
		providerModels: make(map[string][]string),
		modelProviders: make(map[string]string),
	}

	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if got := mgr.ProviderIDForModel("p1/model-a"); got != "p1" {
		t.Fatalf("expected provider p1 for catalog model, got %q", got)
	}
	if got := mgr.ProviderIDForModel("special/custom"); got != "p2" {
		t.Fatalf("expected provider p2 for routed model, got %q", got)
	}
	if got := mgr.ProviderIDForModel("unknown-model"); got != "p1" {
		t.Fatalf("expected default provider p1 for unknown model, got %q", got)
	}
}

func TestChatCompletionNormalizesModelID(t *testing.T) {
	cfg := &config.Config{
		Models: config.ModelConfig{
			Execution:       "p1/model-a",
			DefaultProvider: "p1",
			FallbackChains:  map[string][]string{},
		},
		Providers: config.ProviderConfig{
			ModelRouting: map[string]string{},
		},
	}
	prov := &stubProvider{
		id: "p1",
		catalog: ModelCatalog{
			Data: []ModelInfo{{ID: "p1/model-a", ContextLength: 8_000}},
		},
	}
	mgr := &Manager{
		config:         cfg,
		providers:      map[string]Provider{"p1": prov},
		providerOrder:  []string{"p1"},
		catalog:        make(map[string]ModelInfo),
		providerModels: make(map[string][]string),
		modelProviders: make(map[string]string),
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	_, err := mgr.ChatCompletion(context.Background(), ChatRequest{
		Model: "p1/model-a",
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	if prov.lastRequest.Model != "model-a" {
		t.Fatalf("expected model to be normalized to provider local ID, got %q", prov.lastRequest.Model)
	}
}

func TestVisionFallbackPrefersAvailableModel(t *testing.T) {
	cfg := &config.Config{
		Models: config.ModelConfig{
			VisionFallback: []string{"missing/model", "p1/vision"},
			FallbackChains: map[string][]string{},
		},
		Providers: config.ProviderConfig{
			ModelRouting: map[string]string{},
		},
	}
	prov := &stubProvider{
		id: "p1",
		catalog: ModelCatalog{
			Data: []ModelInfo{
				{ID: "p1/vision", ContextLength: 8_000},
			},
		},
	}
	mgr := &Manager{
		config:         cfg,
		providers:      map[string]Provider{"p1": prov},
		providerOrder:  []string{"p1"},
		catalog:        make(map[string]ModelInfo),
		providerModels: make(map[string][]string),
		modelProviders: make(map[string]string),
	}

	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if got := mgr.GetVisionFallbackModel(); got != "p1/vision" {
		t.Fatalf("expected p1/vision fallback, got %q", got)
	}
}

func TestSupportsHelpersAndCostCalculation(t *testing.T) {
	cfg := &config.Config{
		Models: config.ModelConfig{
			Execution:       "p1/m",
			DefaultProvider: "p1",
			FallbackChains:  map[string][]string{},
		},
		Providers: config.ProviderConfig{
			ModelRouting: map[string]string{},
		},
	}
	info := ModelInfo{
		ID:            "p1/m",
		ContextLength: 16_000,
		Pricing: ModelPricing{
			Prompt:     1.2, // per million tokens
			Completion: 2.4,
		},
		Architecture: Architecture{
			Modality: "text+image",
		},
		SupportedParameters: []string{"tools", "reasoning"},
	}
	prov := &stubProvider{
		id: "p1",
		catalog: ModelCatalog{
			Data: []ModelInfo{info},
		},
	}
	mgr := &Manager{
		config:         cfg,
		providers:      map[string]Provider{"p1": prov},
		providerOrder:  []string{"p1"},
		catalog:        make(map[string]ModelInfo),
		providerModels: make(map[string][]string),
		modelProviders: make(map[string]string),
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	if !mgr.SupportsVision(info.ID) {
		t.Fatalf("expected vision support from modality")
	}
	if !mgr.SupportsTools(info.ID) {
		t.Fatalf("expected tools support from supported parameters")
	}
	if !mgr.SupportsReasoning(info.ID) {
		t.Fatalf("expected reasoning support from supported parameters")
	}

	cost, err := mgr.CalculateCostFromTokens(info.ID, 1_000, 2_000)
	if err != nil {
		t.Fatalf("CalculateCostFromTokens() error = %v", err)
	}
	// Costs are per million tokens; 1k prompt * 1.2 + 2k completion * 2.4 = 0.0012 + 0.0048 = 0.006
	if cost < 0.0059 || cost > 0.0061 {
		t.Fatalf("unexpected cost: %f", cost)
	}
}

func TestChatCompletionAppliesRoutingHooks(t *testing.T) {
	cfg := &config.Config{
		Models: config.ModelConfig{
			DefaultProvider: "p1",
			FallbackChains:  map[string][]string{},
		},
		Providers: config.ProviderConfig{
			ModelRouting: map[string]string{"p2": "p2"},
		},
	}
	prov1 := &stubProvider{
		id: "p1",
		catalog: ModelCatalog{
			Data: []ModelInfo{{ID: "p1/model-a", ContextLength: 8_000}},
		},
	}
	prov2 := &stubProvider{
		id: "p2",
		catalog: ModelCatalog{
			Data: []ModelInfo{{ID: "p2/model-b", ContextLength: 8_000}},
		},
	}
	mgr := &Manager{
		config:         cfg,
		providers:      map[string]Provider{"p1": prov1, "p2": prov2},
		providerOrder:  []string{"p1", "p2"},
		catalog:        make(map[string]ModelInfo),
		providerModels: make(map[string][]string),
		modelProviders: make(map[string]string),
		routingHooks:   NewRoutingHooks(),
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	mgr.RoutingHooks().Register(func(decision *RoutingDecision) *RoutingDecision {
		decision.SelectedModel = "p2/model-b"
		decision.Reason = "hook"
		return decision
	})

	_, err := mgr.ChatCompletion(context.Background(), ChatRequest{
		Model: "p1/model-a",
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	if prov2.lastRequest.Model != "model-b" {
		t.Fatalf("expected routed model to use p2/model-b, got %q", prov2.lastRequest.Model)
	}
}
