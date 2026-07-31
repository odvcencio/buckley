package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAIProvider_ChatCompletionWithContinuationTranslatesResponsesItems(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %s, want /responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("authorization = %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"resp_123",
			"model":"gpt-5.4",
			"status":"completed",
			"output":[
				{
					"id":"rs_1",
					"type":"reasoning",
					"encrypted_content":"opaque-ciphertext",
					"summary":[{"type":"summary_text","text":"Checked the repository state."}]
				},
				{
					"id":"fc_1",
					"type":"function_call",
					"call_id":"call_next",
					"name":"read_file",
					"arguments":"{\"path\":\"next.go\"}"
				},
				{
					"id":"msg_1",
					"type":"message",
					"role":"assistant",
					"status":"completed",
					"content":[{"type":"output_text","text":"I need one more file."}]
				}
			],
			"usage":{
				"input_tokens":30,
				"output_tokens":12,
				"total_tokens":42,
				"input_tokens_details":{"cached_tokens":7},
				"output_tokens_details":{"reasoning_tokens":5}
			}
		}`)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL, false)
	provider.httpClient = server.Client()
	includeReasoning := true
	parallel := true
	result, err := provider.ChatCompletionWithContinuation(context.Background(), ContinuationRequest{
		Request: ChatRequest{
			Model: "gpt-5.4",
			Messages: []Message{
				{Role: "system", Content: "Follow repository instructions."},
				{Role: "user", Content: []ContentPart{
					{Type: "text", Text: "Inspect this image."},
					{Type: "image_url", ImageURL: &ImageURL{URL: "https://example.test/image.png", Detail: "high"}},
				}},
				{Role: "assistant", ToolCalls: []ToolCall{{
					ID: "call_old", Type: "function",
					Function: FunctionCall{Name: "read_file", Arguments: `{"path":"old.go"}`},
				}}},
				{Role: "tool", ToolCallID: "call_old", Content: "package old"},
			},
			Tools: []map[string]any{{
				"type": "function",
				"function": map[string]any{
					"name":        "read_file",
					"description": "Read a file",
					"parameters":  map[string]any{"type": "object"},
					"strict":      true,
				},
			}},
			ToolChoice:          "read_file",
			ParallelToolCalls:   &parallel,
			Reasoning:           &ReasoningConfig{Effort: "high"},
			IncludeReasoning:    &includeReasoning,
			MaxCompletionTokens: 4096,
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletionWithContinuation: %v", err)
	}

	if captured["model"] != "gpt-5.4" || captured["store"] != false {
		t.Fatalf("request model/store = %v/%v", captured["model"], captured["store"])
	}
	if captured["max_output_tokens"] != float64(4096) {
		t.Fatalf("max_output_tokens = %v", captured["max_output_tokens"])
	}
	reasoning := captured["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" || reasoning["summary"] != "auto" {
		t.Fatalf("reasoning request = %#v", reasoning)
	}
	toolChoice := captured["tool_choice"].(map[string]any)
	if toolChoice["type"] != "function" || toolChoice["name"] != "read_file" {
		t.Fatalf("tool_choice = %#v", toolChoice)
	}
	tools := captured["tools"].([]any)
	tool := tools[0].(map[string]any)
	if tool["name"] != "read_file" || tool["function"] != nil || tool["strict"] != true {
		t.Fatalf("responses tool was not flattened: %#v", tool)
	}
	input := captured["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("input item count = %d, want 4", len(input))
	}
	userContent := input[1].(map[string]any)["content"].([]any)
	if userContent[0].(map[string]any)["type"] != "input_text" ||
		userContent[1].(map[string]any)["type"] != "input_image" {
		t.Fatalf("multimodal content = %#v", userContent)
	}
	if input[2].(map[string]any)["type"] != "function_call" ||
		input[3].(map[string]any)["type"] != "function_call_output" {
		t.Fatalf("tool input items = %#v / %#v", input[2], input[3])
	}

	chat := result.Response
	if chat.ID != "resp_123" || len(chat.Choices) != 1 {
		t.Fatalf("chat response = %#v", chat)
	}
	message := chat.Choices[0].Message
	if message.Content != "I need one more file." || message.Reasoning != "Checked the repository state." {
		t.Fatalf("translated message = %#v", message)
	}
	if strings.Contains(message.Reasoning, "opaque-ciphertext") {
		t.Fatal("encrypted reasoning leaked into the portable response")
	}
	if len(message.ToolCalls) != 1 || message.ToolCalls[0].ID != "call_next" {
		t.Fatalf("translated tool calls = %#v", message.ToolCalls)
	}
	if chat.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q", chat.Choices[0].FinishReason)
	}
	if chat.Usage.PromptTokens != 30 ||
		chat.Usage.PromptTokensDetails.CachedTokens != 7 ||
		chat.Usage.CompletionTokenDetails.ReasoningTokens != 5 {
		t.Fatalf("translated usage = %#v", chat.Usage)
	}

	if result.Continuation.ProviderID != "openai" || result.Continuation.ModelID != "openai/gpt-5.4" {
		t.Fatalf("continuation scope = %#v", result.Continuation)
	}
	state, err := decodeOpenAIContinuation(result.Continuation.State)
	if err != nil {
		t.Fatalf("decode continuation: %v", err)
	}
	if len(state.Items) != 7 {
		t.Fatalf("continuation item count = %d, want 7", len(state.Items))
	}
	if !strings.Contains(string(state.Items[4]), "opaque-ciphertext") {
		t.Fatal("raw encrypted reasoning item was not preserved")
	}
}

func TestOpenAIProvider_ChatCompletionWithContinuationReplaysOpaqueItems(t *testing.T) {
	priorState, err := json.Marshal(openAIContinuationState{
		Version: openAIContinuationVersion,
		Items: []json.RawMessage{
			json.RawMessage(`{"id":"rs_1","type":"reasoning","encrypted_content":"ciphertext","future":{"x":1}}`),
			json.RawMessage(`{"id":"fc_1","type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"a.go\"}"}`),
		},
	})
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"resp_2",
			"model":"gpt-5.4",
			"status":"completed",
			"output":[{
				"id":"msg_2",
				"type":"message",
				"role":"assistant",
				"status":"completed",
				"content":[{"type":"output_text","text":"Done."}]
			}]
		}`)
	}))
	defer server.Close()

	provider := NewOpenAIProvider("test-key", server.URL, false)
	provider.httpClient = server.Client()
	result, err := provider.ChatCompletionWithContinuation(context.Background(), ContinuationRequest{
		Request: ChatRequest{
			Model: "gpt-5.4",
			Messages: []Message{
				{Role: "tool", ToolCallID: "call_1", Content: "package a"},
				{Role: "user", Content: "Finish the task."},
			},
		},
		Continuation: &ProviderContinuation{
			ProviderID: "openai",
			ModelID:    "openai/gpt-5.4",
			State:      priorState,
		},
	})
	if err != nil {
		t.Fatalf("ChatCompletionWithContinuation: %v", err)
	}

	input := captured["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("input item count = %d, want 4", len(input))
	}
	if input[0].(map[string]any)["encrypted_content"] != "ciphertext" {
		t.Fatalf("reasoning item changed: %#v", input[0])
	}
	if input[2].(map[string]any)["type"] != "function_call_output" ||
		input[2].(map[string]any)["call_id"] != "call_1" {
		t.Fatalf("function result = %#v", input[2])
	}

	nextState, err := decodeOpenAIContinuation(result.Continuation.State)
	if err != nil {
		t.Fatalf("decode next continuation: %v", err)
	}
	if len(nextState.Items) != 5 {
		t.Fatalf("next continuation item count = %d, want 5", len(nextState.Items))
	}
	if !strings.Contains(string(nextState.Items[0]), `"future":{"x":1}`) {
		t.Fatal("unknown provider fields were not preserved")
	}
}

func TestOpenAIProvider_ChatCompletionWithContinuationRejectsForeignState(t *testing.T) {
	provider := NewOpenAIProvider("test-key", "https://example.test", false)
	_, err := provider.ChatCompletionWithContinuation(context.Background(), ContinuationRequest{
		Request: ChatRequest{Model: "gpt-5.4"},
		Continuation: &ProviderContinuation{
			ProviderID: "openrouter",
			ModelID:    "openai/gpt-5.4",
			State:      json.RawMessage(`{"version":1,"items":[]}`),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "belongs to") {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAIProvider_SupportsContinuationOnlyForReasoningModels(t *testing.T) {
	provider := NewOpenAIProvider("test-key", "", false)
	if !provider.SupportsContinuation("gpt-5.4") ||
		!provider.SupportsContinuation("openai/gpt-5.4-mini") {
		t.Fatal("reasoning models should support native continuation")
	}
	if provider.SupportsContinuation("gpt-4o") || provider.SupportsContinuation("unknown") {
		t.Fatal("non-reasoning or unknown models should not support native continuation")
	}
}
