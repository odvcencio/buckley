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
