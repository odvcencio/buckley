package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

type stubProvider struct {
	id             string
	catalog        ModelCatalog
	lastRequest    ChatRequest
	requests       []ChatRequest
	errors         []error
	streamRequests []ChatRequest
	streamPlans    []stubStreamPlan
	response       *ChatResponse
	responseErr    error
	nilResponse    bool
}

type stubStreamPlan struct {
	chunks []StreamChunk
	err    error
}

type refreshingStubProvider struct {
	*stubProvider
	refreshed ModelCatalog
}

func (s *refreshingStubProvider) RefreshCatalog() (*ModelCatalog, error) {
	s.catalog = s.refreshed
	return &s.refreshed, nil
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
	s.requests = append(s.requests, req)
	if len(s.errors) > 0 {
		err := s.errors[0]
		s.errors = s.errors[1:]
		if err != nil {
			return nil, err
		}
	}
	if s.nilResponse {
		return nil, nil
	}
	if s.response != nil {
		resp := *s.response
		return &resp, s.responseErr
	}
	return &ChatResponse{
		Model: req.Model,
		Choices: []Choice{{
			Message:      Message{Content: "ok"},
			FinishReason: "stop",
		}},
	}, nil
}

func TestChatCompletionPreservesResponseAlongsideProviderError(t *testing.T) {
	providerErr := errors.New("provider stream ended after output")
	provider := &stubProvider{
		id:      "openrouter",
		catalog: ModelCatalog{Data: []ModelInfo{{ID: "deepseek/deepseek-v4-pro-0813", ContextLength: 128_000}}},
		response: &ChatResponse{
			Model:   "deepseek/deepseek-v4-pro-0813",
			Choices: []Choice{{Message: Message{Content: "partial review"}}},
			Usage:   Usage{TotalTokens: 42},
		},
		responseErr: providerErr,
	}
	mgr := &Manager{
		config:         &config.Config{},
		providers:      map[string]Provider{"openrouter": provider},
		providerOrder:  []string{"openrouter"},
		catalog:        map[string]ModelInfo{"deepseek/deepseek-v4-pro-0813": provider.catalog.Data[0]},
		providerModels: map[string][]string{"openrouter": {"deepseek/deepseek-v4-pro-0813"}},
		modelProviders: map[string]string{"deepseek/deepseek-v4-pro-0813": "openrouter"},
	}

	resp, err := mgr.ChatCompletion(context.Background(), ChatRequest{Model: "deepseek/deepseek-v4-pro-0813"})
	if !errors.Is(err, providerErr) {
		t.Fatalf("ChatCompletion error = %v, want provider error", err)
	}
	if resp == nil || len(resp.Choices) != 1 || resp.Choices[0].Message.Content != "partial review" {
		t.Fatalf("ChatCompletion response = %#v, want preserved partial response", resp)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want no retry after partial response", len(provider.requests))
	}
}

func TestChatCompletionRetriesAffordableOpenRouterOutputWithoutChangingModel(t *testing.T) {
	provider := &stubProvider{
		id: "openrouter",
		catalog: ModelCatalog{Data: []ModelInfo{{
			ID:            "x-ai/grok-4.6",
			ContextLength: 500_000,
		}}},
		errors: []error{&APIError{
			StatusCode: 402,
			Message:    "This request requires more credits, or fewer max_tokens. You requested up to 32768 tokens, but can only afford 4156.",
		}},
	}
	mgr := &Manager{
		config:         &config.Config{},
		providers:      map[string]Provider{"openrouter": provider},
		providerOrder:  []string{"openrouter"},
		catalog:        map[string]ModelInfo{"x-ai/grok-4.6": provider.catalog.Data[0]},
		providerModels: map[string][]string{"openrouter": {"x-ai/grok-4.6"}},
		modelProviders: map[string]string{"x-ai/grok-4.6": "openrouter"},
	}

	reasoning := &ReasoningConfig{MaxTokens: 3000}
	resp, err := mgr.ChatCompletion(context.Background(), ChatRequest{
		Model:      "x-ai/grok-4.6",
		MaxTokens:  32768,
		Reasoning:  reasoning,
		Messages:   []Message{{Role: "user", Content: "review"}},
		ToolChoice: "none",
	})
	if err != nil || resp == nil {
		t.Fatalf("ChatCompletion() = %#v, %v", resp, err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("requests = %d, want one safe retry", len(provider.requests))
	}
	retry := provider.requests[1]
	if retry.Model != "x-ai/grok-4.6" || len(retry.Models) != 0 {
		t.Fatalf("retry changed exact model routing: model=%q fallbacks=%v", retry.Model, retry.Models)
	}
	if retry.MaxTokens != 3948 {
		t.Fatalf("retry MaxTokens = %d, want 3948", retry.MaxTokens)
	}
	if retry.Reasoning == reasoning || retry.Reasoning.MaxTokens != 1974 {
		t.Fatalf("retry reasoning = %#v, want copied 1974-token allowance", retry.Reasoning)
	}
	if reasoning.MaxTokens != 3000 {
		t.Fatalf("original request reasoning mutated to %d", reasoning.MaxTokens)
	}
}

func TestChatCompletionDoesNotRetryGenericPaymentFailure(t *testing.T) {
	provider := &stubProvider{
		id:     "openrouter",
		errors: []error{&APIError{StatusCode: 402, Message: "insufficient credits"}},
	}
	mgr := &Manager{
		config:         &config.Config{},
		providers:      map[string]Provider{"openrouter": provider},
		providerOrder:  []string{"openrouter"},
		catalog:        map[string]ModelInfo{"x-ai/grok-4.6": {ID: "x-ai/grok-4.6"}},
		providerModels: map[string][]string{"openrouter": {"x-ai/grok-4.6"}},
		modelProviders: map[string]string{"x-ai/grok-4.6": "openrouter"},
	}

	_, err := mgr.ChatCompletion(context.Background(), ChatRequest{Model: "x-ai/grok-4.6", MaxTokens: 32768})
	if err == nil {
		t.Fatal("expected payment failure")
	}
	if len(provider.requests) != 1 {
		t.Fatalf("requests = %d, generic payment failure must not retry", len(provider.requests))
	}
}

func (s *stubProvider) ChatCompletionStream(ctx context.Context, req ChatRequest) (<-chan StreamChunk, <-chan error) {
	s.lastRequest = req
	s.streamRequests = append(s.streamRequests, req)
	var plan stubStreamPlan
	if len(s.streamPlans) > 0 {
		plan = s.streamPlans[0]
		s.streamPlans = s.streamPlans[1:]
	}
	chunks := make(chan StreamChunk, len(plan.chunks))
	errs := make(chan error, 1)
	for _, chunk := range plan.chunks {
		chunks <- chunk
	}
	if plan.err != nil {
		errs <- plan.err
	}
	close(chunks)
	close(errs)
	return chunks, errs
}

func TestChatCompletionStreamRetriesAffordableOutputBeforeFirstChunk(t *testing.T) {
	provider := &stubProvider{
		id: "openrouter",
		streamPlans: []stubStreamPlan{
			{err: &APIError{StatusCode: 402, Message: "You requested up to 32768 tokens, but can only afford 4156."}},
			{chunks: []StreamChunk{{Choices: []StreamChoice{{Delta: MessageDelta{Content: "grounded result"}}}}}},
		},
	}
	mgr := &Manager{
		config:         &config.Config{},
		providers:      map[string]Provider{"openrouter": provider},
		providerOrder:  []string{"openrouter"},
		catalog:        map[string]ModelInfo{"x-ai/grok-4.6": {ID: "x-ai/grok-4.6"}},
		providerModels: map[string][]string{"openrouter": {"x-ai/grok-4.6"}},
		modelProviders: map[string]string{"x-ai/grok-4.6": "openrouter"},
	}

	chunks, errs := mgr.ChatCompletionStream(context.Background(), ChatRequest{
		Model:     "x-ai/grok-4.6",
		MaxTokens: 32768,
	})
	var content string
	for chunk := range chunks {
		for _, choice := range chunk.Choices {
			content += choice.Delta.Content
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
	}
	if content != "grounded result" {
		t.Fatalf("content = %q", content)
	}
	if len(provider.streamRequests) != 2 {
		t.Fatalf("stream requests = %d, want one safe retry", len(provider.streamRequests))
	}
	if retry := provider.streamRequests[1]; retry.Model != "x-ai/grok-4.6" || retry.MaxTokens != 3948 || len(retry.Models) != 0 {
		t.Fatalf("retry changed exact routing or output limit: %+v", retry)
	}
}

func TestChatCompletionStreamNeverRetriesAfterFirstChunk(t *testing.T) {
	provider := &stubProvider{
		id: "openrouter",
		streamPlans: []stubStreamPlan{{
			chunks: []StreamChunk{{Choices: []StreamChoice{{Delta: MessageDelta{Content: "partial"}}}}},
			err:    &APIError{StatusCode: 402, Message: "can only afford 4156"},
		}},
	}
	mgr := &Manager{
		config:         &config.Config{},
		providers:      map[string]Provider{"openrouter": provider},
		providerOrder:  []string{"openrouter"},
		catalog:        map[string]ModelInfo{"x-ai/grok-4.6": {ID: "x-ai/grok-4.6"}},
		providerModels: map[string][]string{"openrouter": {"x-ai/grok-4.6"}},
		modelProviders: map[string]string{"x-ai/grok-4.6": "openrouter"},
	}

	chunks, errs := mgr.ChatCompletionStream(context.Background(), ChatRequest{Model: "x-ai/grok-4.6", MaxTokens: 32768})
	for range chunks {
	}
	var gotErr error
	for err := range errs {
		gotErr = err
	}
	if gotErr == nil || len(provider.streamRequests) != 1 {
		t.Fatalf("error=%v stream requests=%d, want original error with no replay", gotErr, len(provider.streamRequests))
	}
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

func TestRefreshProviderCatalogReplacesOnlyProviderEntries(t *testing.T) {
	provider := &refreshingStubProvider{
		stubProvider: &stubProvider{id: "openrouter"},
		refreshed: ModelCatalog{Data: []ModelInfo{
			{ID: "moonshotai/kimi-k3"},
			{ID: "openai/gpt-5"},
		}},
	}
	mgr := &Manager{
		config:         &config.Config{},
		providers:      map[string]Provider{"openrouter": provider},
		catalog:        map[string]ModelInfo{"old/model": {ID: "old/model"}, "local/model": {ID: "local/model"}},
		providerModels: map[string][]string{"openrouter": {"old/model"}, "ollama": {"local/model"}},
		modelProviders: map[string]string{"old/model": "openrouter", "local/model": "ollama"},
	}

	if err := mgr.RefreshProviderCatalog("openrouter"); err != nil {
		t.Fatalf("RefreshProviderCatalog() error = %v", err)
	}
	catalog := mgr.GetCatalog()
	want := []string{"local/model", "moonshotai/kimi-k3", "openai/gpt-5"}
	if len(catalog.Data) != len(want) {
		t.Fatalf("catalog size = %d, want %d", len(catalog.Data), len(want))
	}
	for index, id := range want {
		if catalog.Data[index].ID != id {
			t.Fatalf("catalog[%d] = %q, want %q", index, catalog.Data[index].ID, id)
		}
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

func TestChatCompletionRejectsEmptyChoices(t *testing.T) {
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
		response: &ChatResponse{ID: "resp-empty", Model: "model-a"},
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
		Model:     "p1/model-a",
		Messages:  []Message{{Role: "user", Content: "hello"}},
		SessionID: "sess-empty",
	})
	if err == nil {
		t.Fatal("expected empty choices error")
	}
	for _, want := range []string{"no response choices", "response_id=resp-empty", "messages=1", "session=sess-empty"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestChatCompletionRejectsNilResponse(t *testing.T) {
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
		nilResponse: true,
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
		Model:     "p1/model-a",
		Messages:  []Message{{Role: "user", Content: "hello"}},
		SessionID: "sess-nil",
	})
	if err == nil {
		t.Fatal("expected nil response error")
	}
	for _, want := range []string{"nil chat response", "messages=1", "session=sess-nil"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestChatCompletionAppliesOpenRouterFallbackChain(t *testing.T) {
	cfg := &config.Config{
		Models: config.ModelConfig{
			Execution:       "z-ai/glm-5.2",
			DefaultProvider: "openrouter",
			FallbackChains: map[string][]string{
				"z-ai/glm-5.2": {
					"moonshotai/kimi-k2.7-code",
					"qwen/qwen3.7-max",
					"qwen/qwen3.7-max",
				},
			},
		},
		Providers: config.ProviderConfig{
			ModelRouting: map[string]string{},
		},
	}
	prov := &stubProvider{
		id: "openrouter",
		catalog: ModelCatalog{
			Data: []ModelInfo{
				{ID: "z-ai/glm-5.2", ContextLength: 128_000},
				{ID: "moonshotai/kimi-k2.7-code", ContextLength: 128_000},
				{ID: "qwen/qwen3.7-max", ContextLength: 128_000},
			},
		},
	}
	mgr := &Manager{
		config:         cfg,
		providers:      map[string]Provider{"openrouter": prov},
		providerOrder:  []string{"openrouter"},
		catalog:        make(map[string]ModelInfo),
		providerModels: make(map[string][]string),
		modelProviders: make(map[string]string),
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	_, err := mgr.ChatCompletion(context.Background(), ChatRequest{
		Model: "z-ai/glm-5.2",
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v", err)
	}

	want := []string{"z-ai/glm-5.2", "moonshotai/kimi-k2.7-code", "qwen/qwen3.7-max"}
	if fmt.Sprint(prov.lastRequest.Models) != fmt.Sprint(want) {
		t.Fatalf("fallback models=%v want %v", prov.lastRequest.Models, want)
	}
	if prov.lastRequest.Provider["allow_fallbacks"] != true {
		t.Fatalf("expected allow_fallbacks=true, got %#v", prov.lastRequest.Provider)
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
	if mgr.SupportsParameter(info.ID, "parallel_tool_calls") {
		t.Fatal("parallel_tool_calls should not be inferred from tool support")
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

func TestGetModelInfoAcceptsUnqualifiedRoutedModelID(t *testing.T) {
	cfg := &config.Config{
		Models: config.ModelConfig{
			DefaultProvider: "anthropic",
			FallbackChains:  map[string][]string{},
		},
		Providers: config.ProviderConfig{
			ModelRouting: map[string]string{},
		},
	}
	prov := &stubProvider{
		id: "anthropic",
		catalog: ModelCatalog{
			Data: []ModelInfo{
				{
					ID:                  "anthropic/claude-3.5-sonnet",
					ContextLength:       200_000,
					SupportedParameters: []string{"tools", "functions"},
				},
			},
		},
	}
	mgr := &Manager{
		config:         cfg,
		providers:      map[string]Provider{"anthropic": prov},
		providerOrder:  []string{"anthropic"},
		catalog:        make(map[string]ModelInfo),
		providerModels: make(map[string][]string),
		modelProviders: make(map[string]string),
	}
	if err := mgr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}

	info, err := mgr.GetModelInfo("claude-3.5-sonnet")
	if err != nil {
		t.Fatalf("GetModelInfo() error = %v", err)
	}
	if info.ID != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("expected anthropic-prefixed model, got %q", info.ID)
	}
	if !mgr.SupportsTools("claude-3.5-sonnet") {
		t.Fatal("expected SupportsTools() to resolve unqualified model IDs")
	}
}
