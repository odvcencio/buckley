package model

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/config"
)

func TestParseOpenRouterPrivacyFallback(t *testing.T) {
	tests := []struct {
		value string
		want  OpenRouterPrivacyFallback
		bad   bool
	}{
		{value: "", want: OpenRouterPrivacyFallbackNone},
		{value: "off", want: OpenRouterPrivacyFallbackNone},
		{value: "zdr_then_data_collection_deny", want: OpenRouterPrivacyFallbackZDRThenDataCollection},
		{value: "unknown", bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			got, err := ParseOpenRouterPrivacyFallback(tt.value)
			if tt.bad {
				if err == nil {
					t.Fatalf("ParseOpenRouterPrivacyFallback(%q) error = nil", tt.value)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("ParseOpenRouterPrivacyFallback(%q) = %q, %v; want %q", tt.value, got, err, tt.want)
			}
		})
	}
}

func newOpenRouterPrivacyTestManager(modelID string, provider *stubProvider) *Manager {
	return &Manager{
		config:         &config.Config{},
		providers:      map[string]Provider{"openrouter": provider},
		providerOrder:  []string{"openrouter"},
		catalog:        map[string]ModelInfo{modelID: provider.catalog.Data[0]},
		providerModels: map[string][]string{"openrouter": {modelID}},
		modelProviders: map[string]string{modelID: "openrouter"},
	}
}

func strictZDRTestRequest(req ChatRequest) ChatRequest {
	req.OpenRouterRetention = OpenRouterRetentionZDR
	req.Provider = cloneAnyMap(req.Provider)
	req.Provider["zdr"] = true
	req.Provider["allow_fallbacks"] = false
	return req
}

type privacyContinuationProvider struct {
	*stubProvider
	continuationRequests []ContinuationRequest
	continuationErr      error
}

func (p *privacyContinuationProvider) SupportsContinuation(string) bool {
	return true
}

func (p *privacyContinuationProvider) ChatCompletionWithContinuation(_ context.Context, req ContinuationRequest) (*ContinuationResponse, error) {
	p.continuationRequests = append(p.continuationRequests, req)
	if p.continuationErr != nil {
		return nil, p.continuationErr
	}
	return &ContinuationResponse{Response: &ChatResponse{
		Model: req.Request.Model,
		Choices: []Choice{{
			Message:      Message{Content: "ok"},
			FinishReason: "stop",
		}},
	}}, nil
}

func awaitPrivacyTestStream(chunks <-chan StreamChunk, errs <-chan error) error {
	for range chunks {
	}
	var result error
	for err := range errs {
		if result == nil && err != nil {
			result = err
		}
	}
	return result
}

func TestManagerOpenRouterPrivacyBoundaryBlocksBeforeProvider(t *testing.T) {
	modelID := "deepseek/deepseek-v4-pro-0813"
	tests := []struct {
		name string
		req  ChatRequest
		want error
	}{
		{name: "implicit retention", req: ChatRequest{Model: modelID}, want: ErrOpenRouterRetentionUnspecified},
		{name: "explicit zdr false", req: ChatRequest{Model: modelID, Provider: map[string]any{"zdr": false}}, want: ErrOpenRouterOSSAdmissionRequired},
		{name: "explicit data collection deny", req: ChatRequest{Model: modelID, Provider: map[string]any{"data_collection": "deny"}}, want: ErrOpenRouterOSSAdmissionRequired},
		{name: "explicit non zdr retention", req: ChatRequest{Model: modelID, OpenRouterRetention: OpenRouterRetentionNonZDR}, want: ErrOpenRouterOSSAdmissionRequired},
		{name: "unknown retention", req: ChatRequest{Model: modelID, OpenRouterRetention: OpenRouterRetentionMode("future")}, want: ErrOpenRouterPrivacyContract},
		{name: "unknown retry", req: ChatRequest{Model: modelID, Provider: map[string]any{"zdr": true}, RetryMode: RequestRetryMode("future")}, want: ErrUnsupportedRequestRetryMode},
		{name: "malformed zdr", req: ChatRequest{Model: modelID, Provider: map[string]any{"zdr": "true"}}, want: ErrOpenRouterPrivacyContract},
		{name: "contradictory policy", req: ChatRequest{Model: modelID, Provider: map[string]any{"zdr": true, "data_collection": "deny"}}, want: ErrOpenRouterPrivacyContract},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &stubProvider{id: "openrouter", catalog: ModelCatalog{Data: []ModelInfo{{ID: modelID}}}}
			mgr := newOpenRouterPrivacyTestManager(modelID, provider)
			_, err := mgr.ChatCompletion(context.Background(), tt.req)
			if !errors.Is(err, tt.want) {
				t.Fatalf("ChatCompletion() error = %v, want %v", err, tt.want)
			}
			if len(provider.requests) != 0 {
				t.Fatalf("provider requests = %d, want zero", len(provider.requests))
			}
		})
	}
}

func TestManagerOpenRouterPrivacyBoundaryBlocksUnknownStreamContractsBeforeProvider(t *testing.T) {
	modelID := "deepseek/deepseek-v4-pro-0813"
	tests := []struct {
		name string
		req  ChatRequest
		want error
	}{
		{
			name: "retention",
			req: ChatRequest{
				Model:               modelID,
				OpenRouterRetention: OpenRouterRetentionMode("future"),
				Provider:            map[string]any{"zdr": true},
			},
			want: ErrOpenRouterPrivacyContract,
		},
		{
			name: "retry mode",
			req: strictZDRTestRequest(ChatRequest{
				Model:     modelID,
				RetryMode: RequestRetryMode("future"),
			}),
			want: ErrUnsupportedRequestRetryMode,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &stubProvider{id: "openrouter", catalog: ModelCatalog{Data: []ModelInfo{{ID: modelID}}}}
			mgr := newOpenRouterPrivacyTestManager(modelID, provider)

			chunks, errs := mgr.ChatCompletionStream(context.Background(), tt.req)
			if err := awaitPrivacyTestStream(chunks, errs); !errors.Is(err, tt.want) {
				t.Fatalf("ChatCompletionStream() error = %v, want %v", err, tt.want)
			}
			if len(provider.streamRequests) != 0 || len(provider.requests) != 0 {
				t.Fatalf("provider calls = stream:%d completion:%d, want zero", len(provider.streamRequests), len(provider.requests))
			}
		})
	}
}

func TestManagerOpenRouterPrivacyBoundaryBlocksUnknownContinuationContractsBeforeProvider(t *testing.T) {
	modelID := "deepseek/deepseek-v4-pro-0813"
	tests := []struct {
		name string
		req  ChatRequest
		want error
	}{
		{
			name: "retention",
			req: ChatRequest{
				Model:               modelID,
				OpenRouterRetention: OpenRouterRetentionMode("future"),
				Provider:            map[string]any{"zdr": true},
			},
			want: ErrOpenRouterPrivacyContract,
		},
		{
			name: "retry mode",
			req: strictZDRTestRequest(ChatRequest{
				Model:     modelID,
				RetryMode: RequestRetryMode("future"),
			}),
			want: ErrUnsupportedRequestRetryMode,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &privacyContinuationProvider{stubProvider: &stubProvider{
				id:      "openrouter",
				catalog: ModelCatalog{Data: []ModelInfo{{ID: modelID}}},
			}}
			mgr := &Manager{
				config:         &config.Config{},
				providers:      map[string]Provider{"openrouter": provider},
				providerOrder:  []string{"openrouter"},
				catalog:        map[string]ModelInfo{modelID: provider.catalog.Data[0]},
				providerModels: map[string][]string{"openrouter": {modelID}},
				modelProviders: map[string]string{modelID: "openrouter"},
			}

			resp, err := mgr.ChatCompletionWithContinuation(context.Background(), ContinuationRequest{Request: tt.req})
			if resp != nil || !errors.Is(err, tt.want) {
				t.Fatalf("ChatCompletionWithContinuation() = %#v, %v; want nil, %v", resp, err, tt.want)
			}
			if len(provider.continuationRequests) != 0 || len(provider.requests) != 0 || len(provider.streamRequests) != 0 {
				t.Fatalf("provider calls = continuation:%d completion:%d stream:%d, want zero",
					len(provider.continuationRequests), len(provider.requests), len(provider.streamRequests))
			}
		})
	}
}

func TestManagerConfiguredLegacyFallbackDoesNotAuthorizeImplicitRequest(t *testing.T) {
	modelID := "deepseek/deepseek-v4-pro-0813"
	t.Run("completion", func(t *testing.T) {
		provider := &stubProvider{id: "openrouter", catalog: ModelCatalog{Data: []ModelInfo{{ID: modelID}}}}
		mgr := newOpenRouterPrivacyTestManager(modelID, provider)
		if err := mgr.SetOpenRouterPrivacyFallback(OpenRouterPrivacyFallbackZDRThenDataCollection); err != nil {
			t.Fatal(err)
		}

		_, err := mgr.ChatCompletion(context.Background(), ChatRequest{Model: modelID})
		if !errors.Is(err, ErrOpenRouterRetentionUnspecified) {
			t.Fatalf("ChatCompletion() error = %v, want %v", err, ErrOpenRouterRetentionUnspecified)
		}
		if len(provider.requests) != 0 {
			t.Fatalf("provider requests = %d, want zero", len(provider.requests))
		}
	})

	t.Run("stream", func(t *testing.T) {
		provider := &stubProvider{id: "openrouter", catalog: ModelCatalog{Data: []ModelInfo{{ID: modelID}}}}
		mgr := newOpenRouterPrivacyTestManager(modelID, provider)
		if err := mgr.SetOpenRouterPrivacyFallback(OpenRouterPrivacyFallbackZDRThenDataCollection); err != nil {
			t.Fatal(err)
		}

		chunks, errs := mgr.ChatCompletionStream(context.Background(), ChatRequest{Model: modelID})
		if err := awaitPrivacyTestStream(chunks, errs); !errors.Is(err, ErrOpenRouterRetentionUnspecified) {
			t.Fatalf("ChatCompletionStream() error = %v, want %v", err, ErrOpenRouterRetentionUnspecified)
		}
		if len(provider.streamRequests) != 0 {
			t.Fatalf("provider stream requests = %d, want zero", len(provider.streamRequests))
		}
	})
}

func TestManagerNonOpenRouterRetryModeRemainsAdapterOwned(t *testing.T) {
	modelID := "openai/gpt-test"
	provider := &stubProvider{id: "openai", catalog: ModelCatalog{Data: []ModelInfo{{ID: modelID}}}}
	mgr := &Manager{
		config:         &config.Config{},
		providers:      map[string]Provider{"openai": provider},
		providerOrder:  []string{"openai"},
		catalog:        map[string]ModelInfo{modelID: provider.catalog.Data[0]},
		providerModels: map[string][]string{"openai": {modelID}},
		modelProviders: map[string]string{modelID: "openai"},
	}

	resp, err := mgr.ChatCompletion(context.Background(), ChatRequest{
		Model:     modelID,
		RetryMode: RequestRetryMode("adapter-owned"),
	})
	if err != nil || resp == nil {
		t.Fatalf("ChatCompletion() = %#v, %v; want unchanged non-OpenRouter dispatch", resp, err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want one", len(provider.requests))
	}
}

func TestManagerOpenRouterStrictZDRNeverDowngrades(t *testing.T) {
	modelID := "deepseek/deepseek-v4-pro-0813"
	tests := []struct {
		name string
		err  error
	}{
		{name: "policy rejection", err: &APIError{StatusCode: 404, Message: "No endpoints found matching your data policy (Zero data retention)."}},
		{name: "rate limit", err: &APIError{StatusCode: 429, Message: "capacity limited", Retryable: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &stubProvider{id: "openrouter", catalog: ModelCatalog{Data: []ModelInfo{{ID: modelID}}}, errors: []error{tt.err}}
			mgr := newOpenRouterPrivacyTestManager(modelID, provider)
			if err := mgr.SetOpenRouterPrivacyFallback(OpenRouterPrivacyFallbackZDRThenDataCollection); err != nil {
				t.Fatal(err)
			}
			_, err := mgr.ChatCompletion(context.Background(), ChatRequest{
				Model:               modelID,
				OpenRouterRetention: OpenRouterRetentionZDR,
				Provider:            map[string]any{"zdr": true, "allow_fallbacks": false},
				RetryMode:           RequestRetrySingleAttempt,
			})
			if err == nil {
				t.Fatal("ChatCompletion() error = nil")
			}
			if len(provider.requests) != 1 {
				t.Fatalf("provider requests = %d, want exactly one strict-ZDR route", len(provider.requests))
			}
			got := provider.requests[0]
			if got.Model != modelID || len(got.Models) != 0 || got.Provider["zdr"] != true {
				t.Fatalf("strict route changed: model=%q fallbacks=%v provider=%#v", got.Model, got.Models, got.Provider)
			}
			if _, downgraded := got.Provider["data_collection"]; downgraded {
				t.Fatalf("strict route was downgraded: %#v", got.Provider)
			}
		})
	}
}

func TestManagerOpenRouterProviderOnlyStrictZDRIsPreserved(t *testing.T) {
	modelID := "deepseek/deepseek-v4-pro-0813"
	provider := &stubProvider{id: "openrouter", catalog: ModelCatalog{Data: []ModelInfo{{ID: modelID}}}}
	mgr := newOpenRouterPrivacyTestManager(modelID, provider)
	resp, err := mgr.ChatCompletion(context.Background(), ChatRequest{
		Model:    modelID,
		Models:   []string{"other/model"},
		Provider: map[string]any{"zdr": true, "allow_fallbacks": true},
	})
	if err != nil || resp == nil {
		t.Fatalf("ChatCompletion() = %#v, %v", resp, err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want one", len(provider.requests))
	}
	got := provider.requests[0]
	if got.Model != modelID || len(got.Models) != 0 || got.Provider["zdr"] != true || got.Provider["allow_fallbacks"] != false {
		t.Fatalf("strict-ZDR route = model:%q fallbacks:%v provider:%#v", got.Model, got.Models, got.Provider)
	}
	if _, downgraded := got.Provider["data_collection"]; downgraded {
		t.Fatalf("strict-ZDR route was downgraded: %#v", got.Provider)
	}
}

func TestManagerOpenRouterStrictZDRStreamNeverDowngrades(t *testing.T) {
	modelID := "deepseek/deepseek-v4-pro-0813"
	provider := &stubProvider{
		id:          "openrouter",
		catalog:     ModelCatalog{Data: []ModelInfo{{ID: modelID}}},
		streamPlans: []stubStreamPlan{{err: &APIError{StatusCode: 429, Message: "capacity limited", Retryable: true}}},
	}
	mgr := newOpenRouterPrivacyTestManager(modelID, provider)
	if err := mgr.SetOpenRouterPrivacyFallback(OpenRouterPrivacyFallbackZDRThenDataCollection); err != nil {
		t.Fatal(err)
	}
	chunks, errs := mgr.ChatCompletionStream(context.Background(), ChatRequest{
		Model:               modelID,
		OpenRouterRetention: OpenRouterRetentionZDR,
		Provider:            map[string]any{"zdr": true, "allow_fallbacks": false},
		RetryMode:           RequestRetrySingleAttempt,
	})
	for range chunks {
	}
	if err := <-errs; err == nil {
		t.Fatal("stream error = nil")
	}
	if len(provider.streamRequests) != 1 {
		t.Fatalf("stream requests = %d, want exactly one", len(provider.streamRequests))
	}
	if got := provider.streamRequests[0].Provider; got["zdr"] != true || got["data_collection"] != nil {
		t.Fatalf("stream privacy policy = %#v, want strict ZDR only", got)
	}
}

func TestManagerOpenRouterSingleAttemptDoesNotRetryAffordableCompletion(t *testing.T) {
	modelID := "x-ai/grok-4.6"
	originalErr := &APIError{
		StatusCode: 402,
		Message:    "This request requires fewer max_tokens. You requested up to 32768 tokens, but can only afford 4156.",
	}
	provider := &stubProvider{
		id:      "openrouter",
		catalog: ModelCatalog{Data: []ModelInfo{{ID: modelID}}},
		errors:  []error{originalErr},
	}
	mgr := newOpenRouterPrivacyTestManager(modelID, provider)
	req := strictZDRTestRequest(ChatRequest{
		Model:      modelID,
		MaxTokens:  32768,
		RetryMode:  RequestRetrySingleAttempt,
		Messages:   []Message{{Role: "user", Content: "hello"}},
		ToolChoice: "none",
	})

	resp, err := mgr.ChatCompletion(context.Background(), req)
	if resp != nil || err != originalErr {
		t.Fatalf("ChatCompletion() = %#v, %v; want original affordability error", resp, err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("provider requests = %d, want one", len(provider.requests))
	}
	if got := provider.requests[0].MaxTokens; got != req.MaxTokens {
		t.Fatalf("first request max tokens = %d, want original %d", got, req.MaxTokens)
	}
}

func TestManagerOpenRouterSingleAttemptDoesNotRetryAffordableStream(t *testing.T) {
	modelID := "x-ai/grok-4.6"
	originalErr := &APIError{
		StatusCode: 402,
		Message:    "This request requires fewer max_tokens. You requested up to 32768 tokens, but can only afford 4156.",
	}
	provider := &stubProvider{
		id:          "openrouter",
		catalog:     ModelCatalog{Data: []ModelInfo{{ID: modelID}}},
		streamPlans: []stubStreamPlan{{err: originalErr}},
	}
	mgr := newOpenRouterPrivacyTestManager(modelID, provider)
	req := strictZDRTestRequest(ChatRequest{
		Model:     modelID,
		MaxTokens: 32768,
		RetryMode: RequestRetrySingleAttempt,
		Messages:  []Message{{Role: "user", Content: "hello"}},
	})

	chunks, errs := mgr.ChatCompletionStream(context.Background(), req)
	if err := awaitPrivacyTestStream(chunks, errs); err != originalErr {
		t.Fatalf("ChatCompletionStream() error = %v, want original affordability error", err)
	}
	if len(provider.streamRequests) != 1 {
		t.Fatalf("provider stream requests = %d, want one", len(provider.streamRequests))
	}
	if got := provider.streamRequests[0].MaxTokens; got != req.MaxTokens {
		t.Fatalf("first stream request max tokens = %d, want original %d", got, req.MaxTokens)
	}
}

func TestManagerOpenRouterSingleAttemptDoesNotRetryAffordableContinuation(t *testing.T) {
	modelID := "x-ai/grok-4.6"
	originalErr := &APIError{
		StatusCode: 402,
		Message:    "This request requires fewer max_tokens. You requested up to 32768 tokens, but can only afford 4156.",
	}
	provider := &privacyContinuationProvider{
		stubProvider: &stubProvider{
			id:      "openrouter",
			catalog: ModelCatalog{Data: []ModelInfo{{ID: modelID}}},
		},
		continuationErr: originalErr,
	}
	mgr := &Manager{
		config:         &config.Config{},
		providers:      map[string]Provider{"openrouter": provider},
		providerOrder:  []string{"openrouter"},
		catalog:        map[string]ModelInfo{modelID: provider.catalog.Data[0]},
		providerModels: map[string][]string{"openrouter": {modelID}},
		modelProviders: map[string]string{modelID: "openrouter"},
	}
	req := strictZDRTestRequest(ChatRequest{
		Model:     modelID,
		MaxTokens: 32768,
		RetryMode: RequestRetrySingleAttempt,
		Messages:  []Message{{Role: "user", Content: "hello"}},
	})

	resp, err := mgr.ChatCompletionWithContinuation(context.Background(), ContinuationRequest{Request: req})
	if resp != nil || err != originalErr {
		t.Fatalf("ChatCompletionWithContinuation() = %#v, %v; want original affordability error", resp, err)
	}
	if len(provider.continuationRequests) != 1 {
		t.Fatalf("provider continuation requests = %d, want one", len(provider.continuationRequests))
	}
	if got := provider.continuationRequests[0].Request.MaxTokens; got != req.MaxTokens {
		t.Fatalf("first continuation request max tokens = %d, want original %d", got, req.MaxTokens)
	}
}

func TestManagerOpenRouterStrictZDRSingleAttemptMakesOneHTTPPost(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"capacity limited","code":429}}`))
	}))
	defer server.Close()

	cfg := config.DefaultConfig()
	cfg.Providers.OpenRouter.Enabled = true
	cfg.Providers.OpenRouter.APIKey = "test-key"
	cfg.Providers.OpenRouter.BaseURL = server.URL
	cfg.Models.DefaultProvider = "openrouter"
	mgr, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := mgr.SetOpenRouterPrivacyFallback(OpenRouterPrivacyFallbackZDRThenDataCollection); err != nil {
		t.Fatal(err)
	}

	_, err = mgr.ChatCompletion(context.Background(), ChatRequest{
		Model:               "stealth/ox-alpha",
		Messages:            []Message{{Role: "user", Content: "hello"}},
		OpenRouterRetention: OpenRouterRetentionZDR,
		Provider:            map[string]any{"zdr": true, "allow_fallbacks": false},
		RetryMode:           RequestRetrySingleAttempt,
	})
	if err == nil {
		t.Fatal("ChatCompletion() error = nil")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP posts = %d, want exactly one", got)
	}
}

func TestOpenRouterClientSingleAttemptStreamMakesOneHTTPPost(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"capacity limited","code":429}}`))
	}))
	defer server.Close()

	client := NewClient("test-key", server.URL)
	t.Cleanup(func() { _ = client.Close() })
	chunks, errs := client.ChatCompletionStream(context.Background(), strictZDRTestRequest(ChatRequest{
		Model:     "stealth/ox-alpha",
		Messages:  []Message{{Role: "user", Content: "hello"}},
		RetryMode: RequestRetrySingleAttempt,
	}))
	if err := awaitPrivacyTestStream(chunks, errs); err == nil {
		t.Fatal("ChatCompletionStream() error = nil")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP posts = %d, want exactly one", got)
	}
}

func TestOpenRouterClientUnknownRetryModeStopsBeforeHTTP(t *testing.T) {
	operations := []struct {
		name string
		call func(*Client, ChatRequest) error
	}{
		{
			name: "completion",
			call: func(client *Client, req ChatRequest) error {
				_, err := client.ChatCompletion(context.Background(), req)
				return err
			},
		},
		{
			name: "stream",
			call: func(client *Client, req ChatRequest) error {
				chunks, errs := client.ChatCompletionStream(context.Background(), req)
				return awaitPrivacyTestStream(chunks, errs)
			},
		},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()

			client := NewClient("test-key", server.URL)
			t.Cleanup(func() { _ = client.Close() })
			err := operation.call(client, ChatRequest{
				Model:     "stealth/ox-alpha",
				RetryMode: RequestRetryMode("future"),
			})
			if !errors.Is(err, ErrUnsupportedRequestRetryMode) {
				t.Fatalf("request error = %v, want %v", err, ErrUnsupportedRequestRetryMode)
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("HTTP requests = %d, want zero", got)
			}
		})
	}
}

func TestOpenRouterProviderDirectPrivacyBoundary(t *testing.T) {
	modelID := "stealth/ox-alpha"
	tests := []struct {
		name      string
		req       ChatRequest
		wantErr   error
		wantPosts int32
	}{
		{
			name:    "implicit",
			req:     ChatRequest{Model: modelID},
			wantErr: ErrOpenRouterRetentionUnspecified,
		},
		{
			name: "malformed",
			req: ChatRequest{
				Model:               modelID,
				OpenRouterRetention: OpenRouterRetentionZDR,
				Provider:            map[string]any{"zdr": "true", "allow_fallbacks": false},
			},
			wantErr: ErrOpenRouterPrivacyContract,
		},
		{
			name: "non-zdr",
			req: ChatRequest{
				Model:               modelID,
				OpenRouterRetention: OpenRouterRetentionNonZDR,
				Provider: map[string]any{
					"zdr":             false,
					"data_collection": "deny",
					"allow_fallbacks": false,
				},
			},
			wantErr: ErrOpenRouterOSSAdmissionRequired,
		},
		{
			name: "unknown retention",
			req: ChatRequest{
				Model:               modelID,
				OpenRouterRetention: OpenRouterRetentionMode("future"),
				Provider:            map[string]any{"zdr": true, "allow_fallbacks": false},
			},
			wantErr: ErrOpenRouterPrivacyContract,
		},
		{
			name: "unknown retry",
			req: ChatRequest{
				Model:               modelID,
				OpenRouterRetention: OpenRouterRetentionZDR,
				Provider:            map[string]any{"zdr": true, "allow_fallbacks": false},
				RetryMode:           RequestRetryMode("future"),
			},
			wantErr: ErrUnsupportedRequestRetryMode,
		},
		{
			name: "non-exact strict",
			req: ChatRequest{
				Model:               modelID,
				OpenRouterRetention: OpenRouterRetentionZDR,
				Models:              []string{"other/model"},
				Provider:            map[string]any{"zdr": true, "allow_fallbacks": true},
			},
			wantErr: ErrOpenRouterPrivacyContract,
		},
		{
			name: "exact strict",
			req: ChatRequest{
				Model:               modelID,
				OpenRouterRetention: OpenRouterRetentionZDR,
				Provider:            map[string]any{"zdr": true, "allow_fallbacks": false},
			},
			wantPosts: 1,
		},
	}
	operations := []struct {
		name string
		call func(*OpenRouterProvider, ChatRequest) error
	}{
		{
			name: "completion",
			call: func(provider *OpenRouterProvider, req ChatRequest) error {
				_, err := provider.ChatCompletion(context.Background(), req)
				return err
			},
		},
		{
			name: "stream",
			call: func(provider *OpenRouterProvider, req ChatRequest) error {
				chunks, errs := provider.ChatCompletionStream(context.Background(), req)
				return awaitPrivacyTestStream(chunks, errs)
			},
		},
	}

	for _, operation := range operations {
		for _, tt := range tests {
			t.Run(operation.name+"/"+tt.name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.Header.Get("Accept") == "text/event-stream" {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = w.Write([]byte("data: [DONE]\n\n"))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"response-1","model":"stealth/ox-alpha","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
				}))
				defer server.Close()

				client := NewClient("test-key", server.URL)
				t.Cleanup(func() { _ = client.Close() })
				provider := &OpenRouterProvider{client: client}
				err := operation.call(provider, tt.req)
				if tt.wantErr == nil {
					if err != nil {
						t.Fatalf("request error = %v, want nil", err)
					}
				} else if !errors.Is(err, tt.wantErr) {
					t.Fatalf("request error = %v, want %v", err, tt.wantErr)
				}
				if got := requests.Load(); got != tt.wantPosts {
					t.Fatalf("HTTP requests = %d, want %d", got, tt.wantPosts)
				}
			})
		}
	}
}

func TestValidateBuckbotPrivacyFallback(t *testing.T) {
	valid := config.DefaultConfig()
	valid.Buckbot.OpenRouterPrivacyFallback = "zdr_then_data_collection_deny"
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid privacy fallback rejected: %v", err)
	}
	invalid := config.DefaultConfig()
	invalid.Buckbot.OpenRouterPrivacyFallback = "weaken-it"
	if err := invalid.Validate(); err == nil || !strings.Contains(err.Error(), "openrouter_privacy_fallback") {
		t.Fatalf("invalid privacy fallback error = %v", err)
	}
}
