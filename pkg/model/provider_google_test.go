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

func TestGoogleProvider_RequestEnforcesMaxOutputTokens(t *testing.T) {
	provider := NewGoogleProvider("test-key", "", false)
	payload, err := provider.toGenerateContentRequest(ChatRequest{
		Model:     "google/gemini-2.0-flash",
		MaxTokens: 321,
		Messages:  []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("toGenerateContentRequest: %v", err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if _, ok := wire["generation_config"]; ok {
		t.Fatalf("request used wrong generation_config key: %s", encoded)
	}
	var generationConfig map[string]int
	if err := json.Unmarshal(wire["generationConfig"], &generationConfig); err != nil {
		t.Fatalf("decode generationConfig: %v (request %s)", err, encoded)
	}
	if got := generationConfig["maxOutputTokens"]; got != 321 {
		t.Fatalf("generationConfig.maxOutputTokens = %d, want 321 (request %s)", got, encoded)
	}
}

func TestGoogleResponse_PropagatesMaxTokensFinishReason(t *testing.T) {
	var wire googleResponse
	if err := json.Unmarshal([]byte(`{
		"candidates":[{"content":{"role":"model","parts":[{"text":"partial"}]},"finishReason":"MAX_TOKENS"}],
		"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}
	}`), &wire); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	resp, err := wire.toChatResponse("gemini-2.0-flash")
	if err != nil {
		t.Fatalf("toChatResponse: %v", err)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].FinishReason != "MAX_TOKENS" {
		t.Fatalf("finish reason = %#v, want MAX_TOKENS", resp.Choices)
	}
}

// TestGoogleProvider_ChatCompletionRetriesTransientError proves the
// migration onto the shared ProviderTransport: a transient 429 is retried
// and recovered inside ChatCompletion instead of surfacing to the caller.
func TestGoogleProvider_ChatCompletionRetriesTransientError(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models/gemini-2.0-flash:generateContent" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("key"); got != "test-key" {
			t.Fatalf("key query param = %q, want test-key", got)
		}
		if atomic.AddInt32(&requests, 1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":429,"message":"rate limited","status":"RESOURCE_EXHAUSTED"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"candidates":[{"content":{"role":"model","parts":[{"text":"recovered"}]}}],
			"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}
		}`)
	}))
	defer server.Close()

	provider := NewGoogleProvider("test-key", server.URL, false)
	provider.httpClient = server.Client()
	provider.transport.SetRetryConfig(RetryConfig{
		MaxRetries:          3,
		MaxRateLimitRetries: 3,
		InitialInterval:     time.Millisecond,
		MaxInterval:         2 * time.Millisecond,
		Multiplier:          2,
	})

	resp, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:    "google/gemini-2.0-flash",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v, want the transient 429 retried transparently", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatalf("response = %#v", resp)
	}
	if atomic.LoadInt32(&requests) != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

// TestGoogleProvider_ChatCompletionSurfacesStructuredError proves a
// non-retryable error status becomes a structured *APIError instead of the
// prior implementation's plain fmt.Errorf(status, body).
func TestGoogleProvider_ChatCompletionSurfacesStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"code":400,"message":"invalid model","status":"INVALID_ARGUMENT"}}`)
	}))
	defer server.Close()

	provider := NewGoogleProvider("test-key", server.URL, false)
	provider.httpClient = server.Client()

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:    "google/gemini-2.0-flash",
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
