package model

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
)

func newTestOpenAICompatibleProvider(baseURL string) *OpenAICompatibleProvider {
	return NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{BaseURL: baseURL, APIKey: "test-key"}, false)
}

func TestOpenAICompatibleProvider_ConfiguredSupportedParametersApplyToStaticCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "catalog unavailable", http.StatusUnauthorized)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Models:  []string{"glm-5.3-flash"},
		SupportedParameters: map[string][]string{
			"glm-5.3-flash": {"tools"},
		},
		ContextLengths: map[string]int{"glm-5.3-flash": 204800},
	}, false)
	provider.httpClient = server.Client()

	catalog, err := provider.FetchCatalog()
	if err != nil {
		t.Fatalf("FetchCatalog() error = %v", err)
	}
	if len(catalog.Data) != 1 {
		t.Fatalf("catalog = %+v, want one static model", catalog.Data)
	}
	if catalog.Data[0].ID != "openai_compatible/glm-5.3-flash" {
		t.Fatalf("model ID = %q, want canonical provider prefix", catalog.Data[0].ID)
	}
	if catalog.Data[0].ContextLength != 204800 {
		t.Fatalf("context length = %d, want 204800", catalog.Data[0].ContextLength)
	}
	if got := catalog.Data[0].SupportedParameters; len(got) != 1 || got[0] != "tools" {
		t.Fatalf("supported parameters = %v, want [tools]", got)
	}
}

func TestOpenAICompatibleProvider_FetchModelsPreservesAdvertisedMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[
			{
				"id":"glm-5.3-flash",
				"name":"GLM Flash",
				"description":"fast thinking model",
				"context_length":1048576,
				"max_completion_tokens":32768,
				"created":1790000000,
				"architecture":{"modality":"text+image","tokenizer":"test-tokenizer"},
				"supported_parameters":["tools","reasoning_effort"]
			},
			{
				"id":"openai_compatible/prefixed-model",
				"name":"openai_compatible/Prefixed Model",
				"context_length":512,
				"supported_parameters":["tools"]
			}
		]}`)
	}))
	defer server.Close()

	provider := NewOpenAICompatibleProvider(config.OpenAICompatibleConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		SupportedParameters: map[string][]string{
			"glm-5.3-flash":                    {"reasoning_content", "tools", " "},
			"openai_compatible/prefixed-model": {"parallel_tool_calls"},
		},
		ContextLengths: map[string]int{"openai_compatible/glm-5.3-flash": 2048000},
	}, false)
	provider.httpClient = server.Client()

	catalog, err := provider.FetchCatalog()
	if err != nil {
		t.Fatalf("FetchCatalog() error = %v", err)
	}
	if len(catalog.Data) != 2 {
		t.Fatalf("catalog = %+v, want two fetched models", catalog.Data)
	}

	got := catalog.Data[0]
	if got.ID != "openai_compatible/glm-5.3-flash" {
		t.Fatalf("model ID = %q, want canonical provider prefix", got.ID)
	}
	if got.Name != "GLM Flash" {
		t.Fatalf("model name = %q, want provider-advertised name", got.Name)
	}
	if got.Description != "fast thinking model" || got.Created != 1790000000 {
		t.Fatalf("model metadata = %+v, want description and created preserved", got)
	}
	if got.ContextLength != 2048000 {
		t.Fatalf("context length = %d, want configured override 2048000", got.ContextLength)
	}
	if got.MaxCompletionTokens != 32768 {
		t.Fatalf("max completion tokens = %d, want 32768", got.MaxCompletionTokens)
	}
	if got.Architecture.Modality != "text+image" || got.Architecture.Tokenizer != "test-tokenizer" {
		t.Fatalf("architecture = %+v, want provider-advertised architecture preserved", got.Architecture)
	}
	wantParams := []string{"tools", "reasoning_effort", "reasoning_content"}
	if len(got.SupportedParameters) != len(wantParams) {
		t.Fatalf("supported parameters = %v, want %v", got.SupportedParameters, wantParams)
	}
	for i := range wantParams {
		if got.SupportedParameters[i] != wantParams[i] {
			t.Fatalf("supported parameters = %v, want %v", got.SupportedParameters, wantParams)
		}
	}

	got = catalog.Data[1]
	if got.ID != "openai_compatible/prefixed-model" {
		t.Fatalf("prefixed model ID = %q, want no double prefix", got.ID)
	}
	if got.Name != "Prefixed Model" {
		t.Fatalf("prefixed model name = %q, want display name without provider prefix", got.Name)
	}
	wantParams = []string{"tools", "parallel_tool_calls"}
	if len(got.SupportedParameters) != len(wantParams) {
		t.Fatalf("prefixed supported parameters = %v, want %v", got.SupportedParameters, wantParams)
	}
	for i := range wantParams {
		if got.SupportedParameters[i] != wantParams[i] {
			t.Fatalf("prefixed supported parameters = %v, want %v", got.SupportedParameters, wantParams)
		}
	}
}

func TestOpenAICompatibleProvider_FetchModelsAcceptsMaxModelLenAndMinimalID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[
			{"id":"minimal","architecture":{"tokenizer":"partial-tokenizer"}},
			{"id":"long-context","max_model_len":262144,"top_provider":{"max_completion_tokens":16384}}
		]}`)
	}))
	defer server.Close()

	provider := newTestOpenAICompatibleProvider(server.URL)
	provider.httpClient = server.Client()

	catalog, err := provider.FetchCatalog()
	if err != nil {
		t.Fatalf("FetchCatalog() error = %v", err)
	}
	if len(catalog.Data) != 2 {
		t.Fatalf("catalog = %+v, want two fetched models", catalog.Data)
	}

	minimal := catalog.Data[0]
	if minimal.ID != "openai_compatible/minimal" || minimal.Name != "minimal" {
		t.Fatalf("minimal model = %+v, want prefixed ID and raw name", minimal)
	}
	if minimal.ContextLength != 8192 {
		t.Fatalf("minimal context length = %d, want default 8192", minimal.ContextLength)
	}
	if minimal.Architecture.Modality != "text" {
		t.Fatalf("minimal architecture = %+v, want text default", minimal.Architecture)
	}
	if minimal.Architecture.Tokenizer != "partial-tokenizer" {
		t.Fatalf("minimal tokenizer = %q, want preserved provider metadata", minimal.Architecture.Tokenizer)
	}
	if len(minimal.SupportedParameters) != 0 {
		t.Fatalf("minimal supported parameters = %v, want none", minimal.SupportedParameters)
	}

	longContext := catalog.Data[1]
	if longContext.ID != "openai_compatible/long-context" || longContext.Name != "long-context" {
		t.Fatalf("long context model = %+v, want prefixed ID and raw name", longContext)
	}
	if longContext.ContextLength != 262144 {
		t.Fatalf("long context length = %d, want max_model_len 262144", longContext.ContextLength)
	}
	if longContext.MaxCompletionTokens != 16384 {
		t.Fatalf("long context max completion tokens = %d, want top_provider 16384", longContext.MaxCompletionTokens)
	}
}

func TestLiteLLMLegacyAlias_ConfiguredSupportedParametersAugmentModelInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model/info" {
			t.Fatalf("path = %q, want /model/info", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"model_name":"glm-5.3-flash","model_info":{"mode":"chat","supports_function_calling":true}}]}`)
	}))
	defer server.Close()

	provider := NewLiteLLMProvider(config.LiteLLMConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
		SupportedParameters: map[string][]string{
			"litellm/glm-5.3-flash": {"reasoning", "tools", " "},
		},
	}, false)
	provider.httpClient = server.Client()

	catalog, err := provider.FetchCatalog()
	if err != nil {
		t.Fatalf("FetchCatalog() error = %v", err)
	}
	got := catalog.Data[0].SupportedParameters
	want := []string{"tools", "functions", "reasoning"}
	if len(got) != len(want) {
		t.Fatalf("supported parameters = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("supported parameters = %v, want %v", got, want)
		}
	}
}

// TestOpenAICompatibleProvider_ChatCompletionRetriesTransientError proves the
// migration onto the shared ProviderTransport: a transient 429 is retried
// and recovered inside ChatCompletion instead of surfacing to the caller.
func TestOpenAICompatibleProvider_ChatCompletionRetriesTransientError(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q, want Bearer test-key", got)
		}
		if atomic.AddInt32(&requests, 1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-1",
			"model":"test-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}]
		}`)
	}))
	defer server.Close()

	provider := newTestOpenAICompatibleProvider(server.URL)
	provider.httpClient = server.Client()
	provider.transport.SetRetryConfig(RetryConfig{
		MaxRetries:          3,
		MaxRateLimitRetries: 3,
		InitialInterval:     time.Millisecond,
		MaxInterval:         2 * time.Millisecond,
		Multiplier:          2,
	})

	resp, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v, want the transient 429 retried transparently", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content != "recovered" {
		t.Fatalf("response = %#v", resp)
	}
	if atomic.LoadInt32(&requests) != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

// TestOpenAICompatibleProvider_ChatCompletionSurfacesStructuredError proves a
// non-retryable error status becomes a structured *APIError instead of the
// prior implementation's plain fmt.Errorf(status, body).
func TestOpenAICompatibleProvider_ChatCompletionSurfacesStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid model","type":"invalid_request_error"}}`)
	}))
	defer server.Close()

	provider := newTestOpenAICompatibleProvider(server.URL)
	provider.httpClient = server.Client()

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest || apiErr.Message != "invalid model" || apiErr.Retryable {
		t.Fatalf("apiErr = %#v", apiErr)
	}
}

// TestOpenAICompatibleProvider_ChatCompletionStreamDetectsMalformedChunk proves the
// migration onto the shared SSE parser: a malformed chunk in the middle of a
// stream is now a hard error instead of being silently skipped by the prior
// hand-rolled parser's `continue` on decode failure.
func TestOpenAICompatibleProvider_ChatCompletionStreamDetectsMalformedChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "data: not-json\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	provider := newTestOpenAICompatibleProvider(server.URL)
	provider.httpClient = server.Client()

	chunkChan, errChan := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})

	var gotChunk bool
	var gotErr error
	timeout := time.After(2 * time.Second)
loop:
	for {
		select {
		case chunk, ok := <-chunkChan:
			if !ok {
				chunkChan = nil
				if errChan == nil {
					break loop
				}
				continue
			}
			if len(chunk.Choices) > 0 {
				gotChunk = true
			}
		case err, ok := <-errChan:
			if !ok {
				errChan = nil
				if chunkChan == nil {
					break loop
				}
				continue
			}
			gotErr = err
		case <-timeout:
			t.Fatal("timeout waiting for stream")
		}
		if chunkChan == nil && errChan == nil {
			break
		}
	}

	if !gotChunk {
		t.Fatal("expected at least one valid chunk before the malformed one")
	}
	if gotErr == nil {
		t.Fatal("expected the malformed chunk to surface as an error, not be silently skipped")
	}
}
