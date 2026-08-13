package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAIProvider_CostBoundedStreamRequestsUsage(t *testing.T) {
	requestBody := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBody <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+
			`{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+
			"\n\ndata: "+
			`{"id":"chatcmpl-1","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`+
			"\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL, false)
	provider.httpClient = server.Client()
	chunks, errs := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:         "openai/gpt-4o",
		Messages:      []Message{{Role: "user", Content: "hi"}},
		StreamOptions: &StreamOptions{IncludeUsage: true},
	})

	var usage *Usage
	for chunks != nil || errs != nil {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			if chunk.Usage != nil {
				usage = chunk.Usage
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				t.Fatalf("stream error: %v", err)
			}
		}
	}

	var wire struct {
		StreamOptions *StreamOptions `json:"stream_options"`
	}
	if err := json.Unmarshal(<-requestBody, &wire); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if wire.StreamOptions == nil || !wire.StreamOptions.IncludeUsage {
		t.Fatalf("stream_options = %+v, want include_usage=true", wire.StreamOptions)
	}
	if usage == nil || usage.TotalTokens != 4 {
		t.Fatalf("usage-only terminal chunk = %+v, want total_tokens=4", usage)
	}
}

func TestOpenAIProvider_UnboundedStreamOmitsUsageOption(t *testing.T) {
	requestBody := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBody <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL, false)
	provider.httpClient = server.Client()
	chunks, errs := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model: "openai/gpt-4o", Messages: []Message{{Role: "user", Content: "hi"}},
	})
	for range chunks {
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(<-requestBody, &wire); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if _, ok := wire["stream_options"]; ok {
		t.Fatalf("unbounded stream unexpectedly changed wire shape: %s", wire["stream_options"])
	}
}

func TestOpenAIProvider_NonStreamingRequestOmitsStreamOptions(t *testing.T) {
	requestBody := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBody <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL, false)
	provider.httpClient = server.Client()
	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:         "openai/gpt-4o",
		Messages:      []Message{{Role: "user", Content: "hi"}},
		StreamOptions: &StreamOptions{IncludeUsage: true},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(<-requestBody, &wire); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if _, ok := wire["stream_options"]; ok {
		t.Fatalf("non-streaming request carried incompatible stream_options: %s", wire["stream_options"])
	}
}

// TestOpenAIProvider_ChatCompletionRetriesTransientError proves the
// migration onto the shared ProviderTransport: a transient 429 is retried
// and recovered inside ChatCompletion instead of surfacing to the caller.
func TestOpenAIProvider_ChatCompletionRetriesTransientError(t *testing.T) {
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
			"model":"gpt-4o",
			"choices":[{"index":0,"message":{"role":"assistant","content":"recovered"},"finish_reason":"stop"}]
		}`)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL, false)
	provider.httpClient = server.Client()
	provider.transport.SetRetryConfig(RetryConfig{
		MaxRetries:          3,
		MaxRateLimitRetries: 3,
		InitialInterval:     time.Millisecond,
		MaxInterval:         2 * time.Millisecond,
		Multiplier:          2,
	})

	resp, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:    "openai/gpt-4o",
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

// TestOpenAIProvider_ChatCompletionSurfacesStructuredError proves a
// non-retryable error status becomes a structured *APIError instead of the
// prior implementation's plain fmt.Errorf(status, body).
func TestOpenAIProvider_ChatCompletionSurfacesStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid model","type":"invalid_request_error"}}`)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL, false)
	provider.httpClient = server.Client()

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:    "openai/gpt-4o",
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

// TestOpenAIProvider_ChatCompletionStreamDetectsMalformedChunk proves the
// migration onto the shared SSE parser: a malformed chunk in the middle of a
// stream is now a hard error instead of being silently skipped by the prior
// hand-rolled parser's `continue` on decode failure.
func TestOpenAIProvider_ChatCompletionStreamDetectsMalformedChunk(t *testing.T) {
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

	provider := NewOpenAIProvider("test-key", server.URL, false)
	provider.httpClient = server.Client()

	chunkChan, errChan := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model:    "openai/gpt-4o",
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

func TestOpenAIProvider_ChatCompletionStreamRetriesInterruptedStreamBeforeEvents(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if atomic.AddInt32(&requests, 1) == 1 {
			return
		}
		_, _ = io.WriteString(w, "data: {\"id\":\"1\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"recovered\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL, false)
	provider.httpClient = server.Client()
	provider.transport.SetRetryConfig(RetryConfig{
		MaxRetries:          1,
		MaxRateLimitRetries: 1,
		InitialInterval:     time.Millisecond,
		MaxInterval:         time.Millisecond,
		Multiplier:          1,
	})
	chunks, errs := provider.ChatCompletionStream(context.Background(), ChatRequest{
		Model: "openai/gpt-4o", Messages: []Message{{Role: "user", Content: "hi"}},
	})

	var received int
	var streamErr error
	for chunks != nil || errs != nil {
		select {
		case _, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			received++
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			streamErr = err
		}
	}
	if streamErr != nil {
		t.Fatalf("stream error = %v", streamErr)
	}
	if requests != 2 || received != 1 {
		t.Fatalf("requests=%d chunks=%d, want 2 and 1", requests, received)
	}
}
