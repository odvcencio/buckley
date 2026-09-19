package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestGoogleProvider_GenerateContentWireUsesSystemInstructionContentAndModelRole(t *testing.T) {
	var requests int32
	handlerErrs := make(chan error, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		if r.URL.Path != "/models/gemini-2.0-flash:generateContent" {
			handlerErrs <- fmt.Errorf("path = %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			handlerErrs <- fmt.Errorf("read body: %w", err)
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		var wire struct {
			SystemInstruction *struct {
				Role  string `json:"role,omitempty"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"systemInstruction"`
			LegacySystemInstruction json.RawMessage `json:"system_instruction"`
			Contents                []struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
			GenerationConfig struct {
				MaxOutputTokens int `json:"maxOutputTokens"`
			} `json:"generationConfig"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			handlerErrs <- fmt.Errorf("decode request: %w\n%s", err, body)
			http.Error(w, "decode request", http.StatusBadRequest)
			return
		}
		if len(wire.LegacySystemInstruction) != 0 {
			handlerErrs <- fmt.Errorf("request used legacy system_instruction key: %s", body)
		}
		if wire.SystemInstruction == nil {
			handlerErrs <- fmt.Errorf("missing systemInstruction content object: %s", body)
			http.Error(w, "missing systemInstruction", http.StatusBadRequest)
			return
		}
		if wire.SystemInstruction.Role != "" {
			handlerErrs <- fmt.Errorf("systemInstruction.role = %q, want omitted/empty", wire.SystemInstruction.Role)
		}
		systemParts := make([]string, 0, len(wire.SystemInstruction.Parts))
		for _, part := range wire.SystemInstruction.Parts {
			systemParts = append(systemParts, part.Text)
		}
		if strings.Join(systemParts, "|") != "first system|second system" {
			handlerErrs <- fmt.Errorf("systemInstruction parts = %v, want ordered system parts (body %s)", systemParts, body)
		}
		if len(wire.Contents) != 3 {
			handlerErrs <- fmt.Errorf("contents length = %d, want 3 (body %s)", len(wire.Contents), body)
			http.Error(w, "bad contents", http.StatusBadRequest)
			return
		}
		if wire.Contents[0].Role != "user" || wire.Contents[1].Role != "model" || wire.Contents[2].Role != "user" {
			handlerErrs <- fmt.Errorf("content roles = %q/%q/%q, want user/model/user (body %s)", wire.Contents[0].Role, wire.Contents[1].Role, wire.Contents[2].Role, body)
		}
		if len(wire.Contents[1].Parts) == 0 {
			handlerErrs <- fmt.Errorf("model content has no parts (body %s)", body)
			http.Error(w, "missing model parts", http.StatusBadRequest)
			return
		}
		if wire.Contents[1].Parts[0].Text != "prior answer" {
			handlerErrs <- fmt.Errorf("model content = %q, want assistant text as model role", wire.Contents[1].Parts[0].Text)
		}
		if wire.GenerationConfig.MaxOutputTokens != 123 {
			handlerErrs <- fmt.Errorf("maxOutputTokens = %d, want 123", wire.GenerationConfig.MaxOutputTokens)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],
			"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}
		}`)
	}))
	defer server.Close()

	provider := NewGoogleProvider("test-key", server.URL, false)
	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:     "google/gemini-2.0-flash",
		MaxTokens: 123,
		Messages: []Message{
			{Role: "system", Content: "first system"},
			{Role: "system", Content: "second system"},
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "prior answer"},
			{Role: "user", Content: "again"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	assertNoGoogleHandlerErrors(t, handlerErrs)
	if atomic.LoadInt32(&requests) != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestGoogleProvider_GenerateContentWireOmitsSystemInstructionWhenAbsent(t *testing.T) {
	handlerErrs := make(chan error, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			handlerErrs <- fmt.Errorf("read body: %w", err)
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		var wire map[string]json.RawMessage
		if err := json.Unmarshal(body, &wire); err != nil {
			handlerErrs <- fmt.Errorf("decode request: %w\n%s", err, body)
			http.Error(w, "decode request", http.StatusBadRequest)
			return
		}
		if _, ok := wire["systemInstruction"]; ok {
			handlerErrs <- fmt.Errorf("systemInstruction present without system messages: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],
			"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}
		}`)
	}))
	defer server.Close()

	provider := NewGoogleProvider("test-key", server.URL, false)
	if _, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:    "google/gemini-2.0-flash",
		Messages: []Message{{Role: "user", Content: "hello"}},
	}); err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	assertNoGoogleHandlerErrors(t, handlerErrs)
}

func TestGoogleProvider_UnsupportedToolConversationFailsBeforeNetwork(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer server.Close()
	provider := NewGoogleProvider("test-key", server.URL, false)

	tests := []struct {
		name string
		req  ChatRequest
	}{
		{
			name: "declared tools",
			req: ChatRequest{
				Model:    "google/gemini-2.0-flash",
				Messages: []Message{{Role: "user", Content: "hi"}},
				Tools:    []map[string]any{{"type": "function"}},
			},
		},
		{
			name: "tool role transcript",
			req: ChatRequest{
				Model: "google/gemini-2.0-flash",
				Messages: []Message{
					{Role: "user", Content: "hi"},
					{Role: "tool", Content: "result", ToolCallID: "call-1"},
				},
			},
		},
		{
			name: "assistant tool calls transcript",
			req: ChatRequest{
				Model: "google/gemini-2.0-flash",
				Messages: []Message{
					{Role: "user", Content: "hi"},
					{Role: "assistant", Content: "", ToolCalls: []ToolCall{{
						ID:   "call-1",
						Type: "function",
						Function: FunctionCall{
							Name:      "lookup",
							Arguments: `{}`,
						},
					}}},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := provider.ChatCompletion(context.Background(), tt.req)
			if err == nil || !strings.Contains(err.Error(), "does not support tool") {
				t.Fatalf("ChatCompletion error = %v, want unsupported tool error", err)
			}
		})
	}
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Fatalf("requests = %d, want zero before-network failures", got)
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

func TestGoogleResponse_ExecutionIdentityUsesOutputOnlyFields(t *testing.T) {
	var wire googleResponse
	if err := json.Unmarshal([]byte(`{
		"responseId":"google-response-123",
		"modelVersion":"gemini-2.0-flash-2026-09-05",
		"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}
	}`), &wire); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	resp, err := wire.toChatResponse("gemini-2.0-flash")
	if err != nil {
		t.Fatalf("toChatResponse: %v", err)
	}
	if resp.ID != "google-response-123" {
		t.Fatalf("response ID = %q", resp.ID)
	}
	if resp.ExecutionIdentity == nil {
		t.Fatal("missing execution identity")
	}
	if got := *resp.ExecutionIdentity; got.ResponseID != "google-response-123" || got.ResponseModel != "gemini-2.0-flash-2026-09-05" || got.RequestedModel != "" || got.ProviderID != "" {
		t.Fatalf("identity = %+v", got)
	}
}

func assertNoGoogleHandlerErrors(t *testing.T, errs <-chan error) {
	t.Helper()
	for {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatal(err)
			}
		default:
			return
		}
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
