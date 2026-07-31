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

func TestAnthropicProvider_ToAnthropicRequest_WithTools(t *testing.T) {
	provider := &AnthropicProvider{}

	req := ChatRequest{
		Model: "anthropic/claude-3.5-sonnet",
		Messages: []Message{
			{Role: "system", Content: "system prompt"},
			{Role: "user", Content: "inspect the repo"},
			{
				Role:    "assistant",
				Content: "calling read_file",
				ToolCalls: []ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: FunctionCall{
							Name:      "read_file",
							Arguments: `{"path":"README.md"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call_1", Content: "file contents"},
		},
		Tools: []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name":        "read_file",
					"description": "Read a file",
					"parameters": map[string]any{
						"type": "object",
					},
				},
			},
		},
		ToolChoice: "auto",
	}

	anthReq, err := provider.toAnthropicRequest(req, false)
	if err != nil {
		t.Fatalf("toAnthropicRequest() error = %v", err)
	}

	if anthReq.Model != "claude-3.5-sonnet" {
		t.Fatalf("expected normalized model ID, got %q", anthReq.Model)
	}
	if anthReq.System != "system prompt" {
		t.Fatalf("unexpected system prompt: %q", anthReq.System)
	}
	if len(anthReq.Messages) != 3 {
		t.Fatalf("expected 3 non-system messages, got %d", len(anthReq.Messages))
	}
	if anthReq.Messages[1].Role != "assistant" {
		t.Fatalf("expected assistant tool call message, got %q", anthReq.Messages[1].Role)
	}
	if got := anthReq.Messages[1].Content[1].Type; got != "tool_use" {
		t.Fatalf("expected tool_use content block, got %q", got)
	}
	if got := anthReq.Messages[2].Content[0].Type; got != "tool_result" {
		t.Fatalf("expected tool_result content block, got %q", got)
	}
	if len(anthReq.Tools) != 1 || anthReq.Tools[0].Name != "read_file" {
		t.Fatalf("expected anthropic tool definition for read_file, got %+v", anthReq.Tools)
	}
	if anthReq.ToolChoice == nil || anthReq.ToolChoice.Type != "auto" {
		t.Fatalf("expected auto tool choice, got %+v", anthReq.ToolChoice)
	}
}

func TestAnthropicProvider_ToAnthropicRequest_ToolCallWithoutText(t *testing.T) {
	provider := &AnthropicProvider{}

	req := ChatRequest{
		Model: "anthropic/claude-3.5-sonnet",
		Messages: []Message{
			{Role: "user", Content: "inspect the repo"},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: FunctionCall{
							Name:      "read_file",
							Arguments: `{"path":"README.md"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call_1", Content: "file contents"},
		},
	}

	anthReq, err := provider.toAnthropicRequest(req, false)
	if err != nil {
		t.Fatalf("toAnthropicRequest() error = %v", err)
	}

	if len(anthReq.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(anthReq.Messages))
	}
	assistant := anthReq.Messages[1]
	if len(assistant.Content) != 1 {
		t.Fatalf("expected only tool_use content, got %+v", assistant.Content)
	}
	if got := assistant.Content[0].Type; got != "tool_use" {
		t.Fatalf("expected tool_use content block, got %q", got)
	}
}

func TestAnthropicResponse_ToChatResponse_WithToolUse(t *testing.T) {
	resp, err := anthropicResponse{
		ID:    "msg_1",
		Model: "claude-3.5-sonnet",
		Content: []anthropicContent{
			{Type: "text", Text: "done"},
			{
				Type:  "tool_use",
				ID:    "call_1",
				Name:  "read_file",
				Input: map[string]any{"path": "README.md"},
			},
		},
		StopReason: "tool_use",
		Usage: struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		}{
			InputTokens:  12,
			OutputTokens: 7,
		},
	}.toChatResponse()
	if err != nil {
		t.Fatalf("toChatResponse() error = %v", err)
	}

	if resp.Model != "anthropic/claude-3.5-sonnet" {
		t.Fatalf("unexpected model ID: %q", resp.Model)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	choice := resp.Choices[0]
	if choice.FinishReason != "tool_calls" {
		t.Fatalf("expected finish_reason tool_calls, got %q", choice.FinishReason)
	}
	if choice.Message.Content != "done" {
		t.Fatalf("unexpected content: %#v", choice.Message.Content)
	}
	if len(choice.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(choice.Message.ToolCalls))
	}
	if choice.Message.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("unexpected tool call name: %q", choice.Message.ToolCalls[0].Function.Name)
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(choice.Message.ToolCalls[0].Function.Arguments), &args); err != nil {
		t.Fatalf("unmarshal tool args: %v", err)
	}
	if args["path"] != "README.md" {
		t.Fatalf("unexpected tool args: %+v", args)
	}
	if resp.Usage.TotalTokens != 19 {
		t.Fatalf("expected total tokens 19, got %d", resp.Usage.TotalTokens)
	}
}

// TestAnthropicProvider_ChatCompletionRetriesTransientError proves the
// migration onto the shared ProviderTransport: a transient 429 is retried
// and recovered inside ChatCompletion instead of surfacing to the caller.
func TestAnthropicProvider_ChatCompletionRetriesTransientError(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got != "test-key" {
			t.Fatalf("x-api-key = %q, want test-key", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Fatalf("anthropic-version = %q", got)
		}
		if atomic.AddInt32(&requests, 1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"rate limited","type":"rate_limit_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"msg_1",
			"model":"claude-3.5-sonnet",
			"content":[{"type":"text","text":"recovered"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":3,"output_tokens":2}
		}`)
	}))
	defer server.Close()

	provider := NewAnthropicProvider("test-key", server.URL, false)
	provider.httpClient = server.Client()
	provider.transport.SetRetryConfig(RetryConfig{
		MaxRetries:          3,
		MaxRateLimitRetries: 3,
		InitialInterval:     time.Millisecond,
		MaxInterval:         2 * time.Millisecond,
		Multiplier:          2,
	})

	resp, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:    "anthropic/claude-3.5-sonnet",
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

// TestAnthropicProvider_ChatCompletionSurfacesStructuredError proves a
// non-retryable error status becomes a structured *APIError instead of the
// prior implementation's plain fmt.Errorf(resp.Status).
func TestAnthropicProvider_ChatCompletionSurfacesStructuredError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"missing model"}}`)
	}))
	defer server.Close()

	provider := NewAnthropicProvider("test-key", server.URL, false)
	provider.httpClient = server.Client()

	_, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model:    "anthropic/claude-3.5-sonnet",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v (%T), want *APIError", err, err)
	}
	if apiErr.StatusCode != http.StatusBadRequest || apiErr.Message != "missing model" || apiErr.Retryable {
		t.Fatalf("apiErr = %#v", apiErr)
	}
}
