package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		Usage: anthropicUsage{
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

func TestAnthropicProvider_ChatCompletion_DropsStaleThinkingBlocks(t *testing.T) {
	// The API names one stale block per rejection, not necessarily the
	// earliest, so the retry must not replay any signed thinking.
	var bodies []anthropicRequest
	var betas []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body anthropicRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		bodies = append(bodies, body)
		betas = append(betas, r.Header.Get("anthropic-beta"))
		w.Header().Set("Content-Type", "application/json")
		for i := len(body.Messages) - 1; i >= 0; i-- {
			for _, block := range body.Messages[i].Content {
				if block.Type == "thinking" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = fmt.Fprintf(w, `{"type":"error","error":{"type":"invalid_request_error","message":"messages.%d.content.0: Invalid signature in thinking block. The block is bound to a different conversation. Remove the block, or set thinking.block_binding.prefix_mismatch_behavior to \"drop_block\"."}}`, i)
					return
				}
			}
		}
		_, _ = io.WriteString(w, `{"id":"msg_2","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()

	signed := func(text string) []ReasoningDetail {
		return []ReasoningDetail{{
			Type:   "reasoning.encrypted",
			Format: anthropicContentFormat,
			Data:   `[{"type":"thinking","thinking":"t","signature":"sig"},{"type":"text","text":"` + text + `"}]`,
		}}
	}
	provider := NewAnthropicProvider("test-key", server.URL, false)
	provider.httpClient = server.Client()

	resp, err := provider.ChatCompletion(context.Background(), ChatRequest{
		Model: "anthropic/claude-opus-5-5",
		Messages: []Message{
			{Role: "user", Content: "one"},
			{Role: "assistant", Content: "first", ReasoningDetails: signed("first")},
			{Role: "user", Content: "two"},
			{Role: "assistant", Content: "second", ReasoningDetails: signed("second")},
			{Role: "user", Content: "three"},
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletion() error = %v, want stale thinking dropped and retried", err)
	}
	if got := messageContentToText(resp.Choices[0].Message.Content); got != "ok" {
		t.Fatalf("content = %q, want ok", got)
	}
	if len(bodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(bodies))
	}
	retry := bodies[1].Messages
	for _, i := range []int{1, 3} {
		if len(retry[i].Content) != 1 || retry[i].Content[0].Type != "text" {
			t.Fatalf("message %d = %#v, want only the text block", i, retry[i].Content)
		}
	}
	if bodies[1].Thinking != nil || betas[1] != "" {
		t.Fatalf("retry thinking = %#v, anthropic-beta = %q; want no binding controls without replayed thinking", bodies[1].Thinking, betas[1])
	}
}

func TestAnthropicProvider_ChatCompletion_ThinkingBindingControls(t *testing.T) {
	signed := []ReasoningDetail{{
		Type:   "reasoning.encrypted",
		Format: anthropicContentFormat,
		Data:   `[{"type":"thinking","thinking":"t","signature":"sig"},{"type":"text","text":"first"}]`,
	}}
	tests := []struct {
		name     string
		messages []Message
		want     bool
	}{
		{name: "replayed thinking", want: true, messages: []Message{
			{Role: "user", Content: "one"},
			{Role: "assistant", Content: "first", ReasoningDetails: signed},
			{Role: "user", Content: "two"},
		}},
		{name: "no thinking", want: false, messages: []Message{{Role: "user", Content: "one"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var beta string
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				beta = r.Header.Get("anthropic-beta")
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"msg","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			}))
			t.Cleanup(server.Close)
			provider := NewAnthropicProvider("test-key", server.URL, false)
			provider.httpClient = server.Client()

			if _, err := provider.ChatCompletion(context.Background(), ChatRequest{
				Model: "anthropic/claude-opus-5-5", Messages: tt.messages,
			}); err != nil {
				t.Fatalf("ChatCompletion() error = %v", err)
			}
			thinking, hasThinking := body["thinking"].(map[string]any)
			if hasThinking != tt.want || (beta == anthropicThinkingBindingBeta) != tt.want {
				t.Fatalf("thinking = %#v, anthropic-beta = %q, want controls %v", body["thinking"], beta, tt.want)
			}
			if tt.want {
				binding, _ := thinking["block_binding"].(map[string]any)
				if binding["prefix_mismatch_behavior"] != "drop_block" {
					t.Fatalf("block_binding = %#v, want drop_block", thinking["block_binding"])
				}
			}
		})
	}
}

func TestIsStaleThinkingError_PrefixMismatchMessage(t *testing.T) {
	err := &APIError{StatusCode: http.StatusBadRequest, Message: `messages.5.content.0: The block is bound to a different conversation. Remove the block, or set thinking.block_binding.prefix_mismatch_behavior to "drop_block". That setting requires the thinking-binding-controls-2026-08-01 value in the anthropic-beta header.`}
	if !isStaleThinkingError(err) {
		t.Fatal("isStaleThinkingError() = false, want true")
	}
}

func TestAnthropicRequest_BindThinking(t *testing.T) {
	thinking := []anthropicMessage{{Role: "assistant", Content: []anthropicContent{{Type: "thinking"}}}}
	plain := []anthropicMessage{{Role: "user", Content: []anthropicContent{{Type: "text", Text: "hi"}}}}
	bound := func() *anthropicThinkingConfig {
		return &anthropicThinkingConfig{BlockBinding: &anthropicBlockBinding{PrefixMismatchBehavior: "drop_block"}}
	}
	tests := []struct {
		name     string
		req      anthropicRequest
		wantBind bool
		wantNil  bool
	}{
		{name: "thinking without config", req: anthropicRequest{Messages: thinking}, wantBind: true},
		{name: "no thinking without config", req: anthropicRequest{Messages: plain}, wantNil: true},
		{name: "binding cleared after drop", req: anthropicRequest{Messages: plain, Thinking: bound()}, wantNil: true},
		{name: "disabled kept after drop", req: anthropicRequest{Messages: plain, Thinking: &anthropicThinkingConfig{Type: "disabled", BlockBinding: &anthropicBlockBinding{}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.req.bindThinking()
			if tt.wantNil != (tt.req.Thinking == nil) {
				t.Fatalf("Thinking = %#v, want nil %v", tt.req.Thinking, tt.wantNil)
			}
			if tt.req.Thinking != nil && (tt.req.Thinking.BlockBinding != nil) != tt.wantBind {
				t.Fatalf("BlockBinding = %#v, want bound %v", tt.req.Thinking.BlockBinding, tt.wantBind)
			}
		})
	}
}
