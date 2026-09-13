package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
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

type routeDriftContinuationProvider struct {
	*stubProvider
	continuationRequests []ContinuationRequest
}

func (p *routeDriftContinuationProvider) SupportsContinuation(string) bool { return true }

func (p *routeDriftContinuationProvider) ChatCompletionWithContinuation(_ context.Context, req ContinuationRequest) (*ContinuationResponse, error) {
	p.continuationRequests = append(p.continuationRequests, req)
	return &ContinuationResponse{
		Response: &ChatResponse{
			Model: req.Request.Model,
			Choices: []Choice{{
				Message:      Message{Role: "assistant", Content: "ok"},
				FinishReason: "stop",
			}},
		},
		Continuation: &ProviderContinuation{ProviderID: p.id, ModelID: req.Request.Model},
	}, nil
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

func TestRefreshProviderCatalogDoesNotDeleteCollisionOwnedByAnotherProvider(t *testing.T) {
	const sharedID = "vendor/shared-model"
	provider := &refreshingStubProvider{
		stubProvider: &stubProvider{id: "selected"},
		refreshed:    ModelCatalog{},
	}
	otherInfo := ModelInfo{ID: sharedID, Description: "other provider metadata"}
	mgr := &Manager{
		config:         &config.Config{},
		providers:      map[string]Provider{"selected": provider, "other": &stubProvider{id: "other"}},
		catalog:        map[string]ModelInfo{sharedID: otherInfo},
		providerModels: map[string][]string{"selected": {sharedID}, "other": {sharedID}},
		modelProviders: map[string]string{sharedID: "other"},
	}

	if err := mgr.RefreshProviderCatalog("selected"); err != nil {
		t.Fatalf("RefreshProviderCatalog() error = %v", err)
	}
	if got, ok := mgr.catalog[sharedID]; !ok || got.Description != otherInfo.Description || mgr.modelProviders[sharedID] != "other" {
		t.Fatalf("refresh deleted another provider's collision entry: info=%+v ok=%v owner=%q", got, ok, mgr.modelProviders[sharedID])
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

func TestProviderQualifiedMetadataLookupUsesCanonicalUpstreamAlias(t *testing.T) {
	for _, tt := range []struct {
		name       string
		providerID string
		upstreamID string
		requestID  string
	}{
		{
			name:       "OpenRouter two-segment upstream ID",
			providerID: "openrouter",
			upstreamID: "google/gemini-3.8-flash",
			requestID:  "openrouter/google/gemini-3.8-flash",
		},
		{
			name:       "OpenAI-compatible one-segment upstream ID",
			providerID: "openai_compatible",
			upstreamID: "glm-5.3-flash",
			requestID:  "openai_compatible/glm-5.3-flash",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			info := ModelInfo{ID: tt.upstreamID, SupportedParameters: []string{"tools", "reasoning_effort"}}
			selected := &stubProvider{id: tt.providerID, catalog: ModelCatalog{Data: []ModelInfo{info}}}
			fallback := &stubProvider{id: "fallback"}
			mgr := &Manager{
				config: &config.Config{
					Models:    config.ModelConfig{DefaultProvider: "fallback", FallbackChains: map[string][]string{}},
					Providers: config.ProviderConfig{ModelRouting: map[string]string{}},
				},
				providers:      map[string]Provider{tt.providerID: selected, "fallback": fallback},
				providerOrder:  []string{"fallback", tt.providerID},
				catalog:        map[string]ModelInfo{tt.upstreamID: info},
				providerModels: map[string][]string{tt.providerID: {tt.upstreamID}},
				modelProviders: map[string]string{tt.upstreamID: tt.providerID},
			}

			route, err := mgr.ResolveModelRoute(tt.requestID)
			if err != nil {
				t.Fatalf("ResolveModelRoute: %v", err)
			}
			if route.ProviderID != tt.providerID || route.SelectedModel != tt.requestID {
				t.Fatalf("provider-qualified route = %+v", route)
			}
			got, err := mgr.GetModelInfo(tt.requestID)
			if err != nil || got.ID != tt.upstreamID {
				t.Fatalf("GetModelInfo(%q) = %+v, %v", tt.requestID, got, err)
			}
			if !mgr.SupportsTools(tt.requestID) || !mgr.SupportsReasoning(tt.requestID) || !mgr.modelAvailable(tt.requestID) {
				t.Fatalf("canonical capabilities unavailable for provider-qualified model %q", tt.requestID)
			}
		})
	}
}

func TestProviderQualifiedMetadataLookupRejectsCrossProviderCatalogCollision(t *testing.T) {
	const upstreamID = "google/gemini-3.8-flash"
	openRouterInfo := ModelInfo{ID: upstreamID, Description: "openrouter metadata", SupportedParameters: []string{"reasoning_effort"}}
	googleInfo := ModelInfo{ID: upstreamID, Description: "direct metadata", SupportedParameters: []string{"tools"}}
	openRouter := &stubProvider{id: "openrouter", catalog: ModelCatalog{Data: []ModelInfo{openRouterInfo}}}
	direct := &stubProvider{id: "google", catalog: ModelCatalog{Data: []ModelInfo{googleInfo}}}
	mgr := &Manager{
		config: &config.Config{
			Models:    config.ModelConfig{DefaultProvider: "google", FallbackChains: map[string][]string{}},
			Providers: config.ProviderConfig{ModelRouting: map[string]string{}},
		},
		providers:      map[string]Provider{"openrouter": openRouter, "google": direct},
		providerOrder:  []string{"google", "openrouter"},
		catalog:        map[string]ModelInfo{upstreamID: googleInfo},
		providerModels: map[string][]string{"openrouter": {upstreamID}, "google": {upstreamID}},
		modelProviders: map[string]string{upstreamID: "google"},
	}

	requestID := "openrouter/" + upstreamID
	info, err := mgr.GetModelInfo(requestID)
	if err != nil {
		t.Fatalf("GetModelInfo: %v", err)
	}
	if info.Description != "openrouter metadata" || !mgr.SupportsReasoning(requestID) || mgr.SupportsTools(requestID) {
		t.Fatalf("provider lock used cross-provider metadata: %+v", info)
	}
	if !mgr.modelAvailable(requestID) {
		t.Fatalf("provider-specific catalog entry should remain available despite canonical ID collision")
	}
}

func TestCanonicalMetadataLookupRejectsCrossProviderCatalogCollision(t *testing.T) {
	const modelID = "vendor/shared-model"
	selectedInfo := ModelInfo{ID: modelID, Description: "selected provider metadata", SupportedParameters: []string{"reasoning_effort"}}
	collisionInfo := ModelInfo{ID: modelID, Description: "other provider metadata", SupportedParameters: []string{"tools"}}
	selected := &stubProvider{id: "selected", catalog: ModelCatalog{Data: []ModelInfo{selectedInfo}}}
	other := &stubProvider{id: "other", catalog: ModelCatalog{Data: []ModelInfo{collisionInfo}}}
	mgr := &Manager{
		config: &config.Config{
			Models:    config.ModelConfig{DefaultProvider: "selected", FallbackChains: map[string][]string{}},
			Providers: config.ProviderConfig{ModelRouting: map[string]string{}},
		},
		providers:      map[string]Provider{"selected": selected, "other": other},
		providerOrder:  []string{"selected", "other"},
		catalog:        map[string]ModelInfo{modelID: collisionInfo},
		providerModels: map[string][]string{"selected": {modelID}, "other": {modelID}},
		modelProviders: map[string]string{modelID: "other"},
	}

	info, err := mgr.GetModelInfo(modelID)
	if err != nil {
		t.Fatalf("GetModelInfo: %v", err)
	}
	if info.Description != "selected provider metadata" || !mgr.SupportsReasoning(modelID) || mgr.SupportsTools(modelID) {
		t.Fatalf("canonical lookup used cross-provider metadata: %+v", info)
	}
	if !mgr.modelAvailable(modelID) {
		t.Fatal("selected provider's canonical catalog entry should remain available after a collision")
	}
}

func TestGetContextLengthForRouteUsesSelectedProviderMetadata(t *testing.T) {
	const modelID = "vendor/shared-model"
	selectedInfo := ModelInfo{ID: modelID, ContextLength: 64_000}
	otherInfo := ModelInfo{ID: modelID, ContextLength: 8_000}
	selected := &stubProvider{id: "selected", catalog: ModelCatalog{Data: []ModelInfo{selectedInfo}}}
	other := &stubProvider{id: "other", catalog: ModelCatalog{Data: []ModelInfo{otherInfo}}}
	mgr := &Manager{
		config: &config.Config{
			Models:    config.ModelConfig{DefaultProvider: "other", FallbackChains: map[string][]string{}},
			Providers: config.ProviderConfig{ModelRouting: map[string]string{}},
		},
		providers:      map[string]Provider{"selected": selected, "other": other},
		providerOrder:  []string{"other", "selected"},
		catalog:        map[string]ModelInfo{modelID: otherInfo},
		providerModels: map[string][]string{"selected": {modelID}, "other": {modelID}},
		modelProviders: map[string]string{modelID: "other"},
	}

	legacy, err := mgr.GetContextLength(modelID)
	if err != nil {
		t.Fatalf("GetContextLength: %v", err)
	}
	if legacy != 8_000 {
		t.Fatalf("legacy context length = %d, want other provider fixture", legacy)
	}

	got, err := mgr.GetContextLengthForRoute(ModelRoute{
		RequestedModel: "alias/model",
		SelectedModel:  modelID,
		ProviderID:     "selected",
	})
	if err != nil {
		t.Fatalf("GetContextLengthForRoute: %v", err)
	}
	if got != 64_000 {
		t.Fatalf("route context length = %d, want selected provider metadata", got)
	}
}

func TestRouteMetadataLookupUsesUnqualifiedSelectedModelWithoutRoutingHooks(t *testing.T) {
	const modelID = "gpt-4o"
	selectedInfo := ModelInfo{
		ID:                  "selected/" + modelID,
		ContextLength:       64_000,
		SupportedParameters: []string{"tools"},
		Pricing:             ModelPricing{Prompt: 3, Completion: 15},
		PricingKnown:        true,
	}
	selectedInfo.markSupportedParametersComplete()
	otherInfo := ModelInfo{ID: "other/" + modelID, ContextLength: 8_000, SupportedParameters: []string{}}
	otherInfo.markSupportedParametersComplete()
	selected := &stubProvider{id: "selected", catalog: ModelCatalog{Data: []ModelInfo{selectedInfo}}}
	other := &stubProvider{id: "other", catalog: ModelCatalog{Data: []ModelInfo{otherInfo}}}
	mgr := &Manager{
		config: &config.Config{
			Models:    config.ModelConfig{DefaultProvider: "other", FallbackChains: map[string][]string{}},
			Providers: config.ProviderConfig{ModelRouting: map[string]string{}},
		},
		providers:      map[string]Provider{"selected": selected, "other": other},
		providerOrder:  []string{"other", "selected"},
		catalog:        map[string]ModelInfo{selectedInfo.ID: selectedInfo, otherInfo.ID: otherInfo},
		providerModels: map[string][]string{"selected": {selectedInfo.ID}, "other": {otherInfo.ID}},
		modelProviders: map[string]string{selectedInfo.ID: "selected", otherInfo.ID: "other"},
		routingHooks:   NewRoutingHooks(),
	}
	var hookCalls atomic.Int64
	mgr.routingHooks.Register(func(decision *RoutingDecision) *RoutingDecision {
		hookCalls.Add(1)
		decision.SelectedModel = "other/" + modelID
		return decision
	})
	route := ModelRoute{RequestedModel: "alias/model", SelectedModel: modelID, ProviderID: "selected"}

	gotContext, err := mgr.GetContextLengthForRoute(route)
	if err != nil {
		t.Fatalf("GetContextLengthForRoute: %v", err)
	}
	if gotContext != 64_000 {
		t.Fatalf("route context length = %d, want selected provider metadata", gotContext)
	}
	gotInfo, err := mgr.GetModelInfoForRoute(route)
	if err != nil {
		t.Fatalf("GetModelInfoForRoute: %v", err)
	}
	if gotInfo.ID != selectedInfo.ID || gotInfo.Pricing != selectedInfo.Pricing || !gotInfo.PricingKnown {
		t.Fatalf("route metadata = %+v, want selected provider metadata %+v", gotInfo, selectedInfo)
	}
	if got := mgr.ResolveParameterCapabilityForRoute(route, "tools"); got.State != CapabilitySupported || got.ProviderID != "selected" || got.Model != selectedInfo.ID {
		t.Fatalf("route tools capability = %+v, want selected provider support", got)
	}
	if got := hookCalls.Load(); got != 0 {
		t.Fatalf("routing hook calls = %d, want 0", got)
	}
}

func TestGetContextLengthForRouteRejectsMalformedOrUnavailableRoute(t *testing.T) {
	mgr := &Manager{
		config:         &config.Config{},
		providers:      map[string]Provider{},
		catalog:        map[string]ModelInfo{},
		providerModels: map[string][]string{},
		modelProviders: map[string]string{},
	}

	if _, err := mgr.GetContextLengthForRoute(ModelRoute{SelectedModel: "model"}); err == nil || !strings.Contains(err.Error(), "malformed model route") {
		t.Fatalf("malformed route error = %v", err)
	}
	if _, err := mgr.GetContextLengthForRoute(ModelRoute{SelectedModel: "model", ProviderID: "missing"}); err == nil || !strings.Contains(err.Error(), "context length unavailable") {
		t.Fatalf("unavailable route error = %v", err)
	}
}

func TestResolveModelRoute_ConfigRoutingOutranksCatalogAndRouteHandoffFailsClosed(t *testing.T) {
	openRouter := &stubProvider{id: "openrouter", catalog: ModelCatalog{Data: []ModelInfo{{ID: "stealth/ox-alpha", ContextLength: 1_048_576}}}}
	direct := &stubProvider{id: "openai", catalog: ModelCatalog{Data: []ModelInfo{{ID: "direct/model", ContextLength: 16_000}}}}
	mgr := &Manager{
		config: &config.Config{
			Models: config.ModelConfig{DefaultProvider: "openrouter", FallbackChains: map[string][]string{}},
			Providers: config.ProviderConfig{ModelRouting: map[string]string{
				"stealth/": "openai",
				"direct/":  "openai",
			}},
		},
		providers:      map[string]Provider{"openrouter": openRouter, "openai": direct},
		providerOrder:  []string{"openai", "openrouter"},
		catalog:        map[string]ModelInfo{"stealth/ox-alpha": openRouter.catalog.Data[0]},
		providerModels: map[string][]string{"openrouter": {"stealth/ox-alpha"}, "openai": {"direct/model"}},
		modelProviders: map[string]string{"stealth/ox-alpha": "openrouter", "direct/model": "openai"},
		routingHooks:   NewRoutingHooks(),
	}

	if legacy := mgr.ProviderIDForModel("stealth/ox-alpha"); legacy != "openrouter" {
		t.Fatalf("legacy catalog projection = %q, want disagreement fixture", legacy)
	}
	route, err := mgr.ResolveModelRoute("stealth/ox-alpha")
	if err != nil {
		t.Fatalf("ResolveModelRoute: %v", err)
	}
	if route.ProviderID != "openai" || route.SelectedModel != "stealth/ox-alpha" {
		t.Fatalf("authoritative route = %+v", route)
	}

	// Remove the static conflicting prefix for the handoff race. The first
	// resolution is OpenRouter; a stateful hook changes the second resolution
	// to a direct-provider model. ChatCompletionForRoute must stop before either
	// provider receives a request.
	delete(mgr.config.Providers.ModelRouting, "stealth/")
	hookCalls := 0
	mgr.routingHooks.Register(func(decision *RoutingDecision) *RoutingDecision {
		hookCalls++
		if hookCalls >= 2 {
			decision.SelectedModel = "direct/model"
		}
		return decision
	})
	route, err = mgr.ResolveModelRoute("stealth/ox-alpha")
	if err != nil || route.ProviderID != "openrouter" {
		t.Fatalf("initial governed route = %+v, %v", route, err)
	}
	if _, err := mgr.ChatCompletionForRoute(context.Background(), ChatRequest{Model: "stealth/ox-alpha"}, route); err == nil || !strings.Contains(err.Error(), "route changed") {
		t.Fatalf("ChatCompletionForRoute error = %v", err)
	}
	if len(openRouter.requests) != 0 || len(direct.requests) != 0 {
		t.Fatalf("provider requests openrouter=%d direct=%d, want zero", len(openRouter.requests), len(direct.requests))
	}
}

func TestChatCompletionStreamForRouteFailsClosedOnRouteDrift(t *testing.T) {
	first := &stubProvider{id: "first", catalog: ModelCatalog{Data: []ModelInfo{{ID: "first/model", ContextLength: 16_000}}}}
	second := &stubProvider{id: "second", catalog: ModelCatalog{Data: []ModelInfo{{ID: "second/model", ContextLength: 16_000}}}}
	mgr := &Manager{
		config:         &config.Config{Models: config.ModelConfig{DefaultProvider: "first", FallbackChains: map[string][]string{}}},
		providers:      map[string]Provider{"first": first, "second": second},
		providerOrder:  []string{"first", "second"},
		catalog:        map[string]ModelInfo{"first/model": first.catalog.Data[0], "second/model": second.catalog.Data[0]},
		providerModels: map[string][]string{"first": {"first/model"}, "second": {"second/model"}},
		modelProviders: map[string]string{"first/model": "first", "second/model": "second"},
		routingHooks:   NewRoutingHooks(),
	}
	hookCalls := 0
	mgr.routingHooks.Register(func(decision *RoutingDecision) *RoutingDecision {
		hookCalls++
		if hookCalls == 1 {
			decision.SelectedModel = "first/model"
		} else {
			decision.SelectedModel = "second/model"
		}
		return decision
	})

	route, err := mgr.ResolveModelRoute("alias-model")
	if err != nil {
		t.Fatalf("ResolveModelRoute: %v", err)
	}
	chunks, errs := mgr.ChatCompletionStreamForRoute(context.Background(), ChatRequest{Model: "alias-model"}, route)
	if chunk, ok := <-chunks; ok {
		t.Fatalf("unexpected stream chunk before route mismatch: %+v", chunk)
	}
	err = <-errs
	if err == nil || !strings.Contains(err.Error(), "route changed") {
		t.Fatalf("stream route mismatch error = %v", err)
	}
	if _, ok := <-errs; ok {
		t.Fatalf("stream error channel did not close")
	}
	if len(first.streamRequests) != 0 || len(second.streamRequests) != 0 {
		t.Fatalf("stream provider requests first=%d second=%d, want zero", len(first.streamRequests), len(second.streamRequests))
	}
}

func TestChatCompletionWithContinuationForRouteFailsClosedOnRouteDrift(t *testing.T) {
	first := &routeDriftContinuationProvider{stubProvider: &stubProvider{id: "first", catalog: ModelCatalog{Data: []ModelInfo{{ID: "first/model", ContextLength: 16_000}}}}}
	second := &stubProvider{id: "second", catalog: ModelCatalog{Data: []ModelInfo{{ID: "second/model", ContextLength: 16_000}}}}
	mgr := &Manager{
		config:         &config.Config{Models: config.ModelConfig{DefaultProvider: "first", FallbackChains: map[string][]string{}}},
		providers:      map[string]Provider{"first": first, "second": second},
		providerOrder:  []string{"first", "second"},
		catalog:        map[string]ModelInfo{"first/model": first.catalog.Data[0], "second/model": second.catalog.Data[0]},
		providerModels: map[string][]string{"first": {"first/model"}, "second": {"second/model"}},
		modelProviders: map[string]string{"first/model": "first", "second/model": "second"},
		routingHooks:   NewRoutingHooks(),
	}
	hookCalls := 0
	mgr.routingHooks.Register(func(decision *RoutingDecision) *RoutingDecision {
		hookCalls++
		if hookCalls == 1 {
			decision.SelectedModel = "first/model"
		} else {
			decision.SelectedModel = "second/model"
		}
		return decision
	})

	route, err := mgr.ResolveModelRoute("alias-model")
	if err != nil {
		t.Fatalf("ResolveModelRoute: %v", err)
	}
	_, err = mgr.ChatCompletionWithContinuationForRoute(context.Background(), ContinuationRequest{Request: ChatRequest{Model: "alias-model"}}, route)
	if err == nil || !strings.Contains(err.Error(), "route changed") {
		t.Fatalf("continuation route mismatch error = %v", err)
	}
	if len(first.continuationRequests) != 0 || len(second.requests) != 0 {
		t.Fatalf("provider requests first=%d second=%d, want zero", len(first.continuationRequests), len(second.requests))
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
		SupportedParameters: []string{"tools", "reasoning_effort"},
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
