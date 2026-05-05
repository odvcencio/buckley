package model

import (
	"strings"
	"testing"
)

func TestApplyProviderTransformsRepairsInterruptedToolCall(t *testing.T) {
	req := ChatRequest{
		Model: "openai/gpt-4o",
		Messages: []Message{
			{Role: "user", Content: "inspect the repo"},
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []ToolCall{
					{
						ID: "call-1",
						Function: FunctionCall{
							Name: "read_file",
						},
					},
				},
			},
			{Role: "user", Content: "continue"},
		},
	}

	got := applyProviderTransforms(req, "openai")

	if len(got.Messages) != 4 {
		t.Fatalf("expected repaired history to have 4 messages, got %d: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[1].Role != "assistant" || len(got.Messages[1].ToolCalls) != 1 {
		t.Fatalf("expected assistant tool call at index 1, got %+v", got.Messages[1])
	}
	call := got.Messages[1].ToolCalls[0]
	if call.Type != "function" {
		t.Fatalf("tool call type=%q want function", call.Type)
	}
	if call.Function.Arguments != "{}" {
		t.Fatalf("tool call arguments=%q want {}", call.Function.Arguments)
	}
	if got.Messages[2].Role != "tool" {
		t.Fatalf("expected synthetic tool result at index 2, got %+v", got.Messages[2])
	}
	if got.Messages[2].ToolCallID != "call-1" {
		t.Fatalf("synthetic tool_call_id=%q want call-1", got.Messages[2].ToolCallID)
	}
	if got.Messages[2].Content != missingToolResultContent {
		t.Fatalf("synthetic content=%q want %q", got.Messages[2].Content, missingToolResultContent)
	}
	if got.Messages[3].Role != "user" || got.Messages[3].Content != "continue" {
		t.Fatalf("expected original user message after repair, got %+v", got.Messages[3])
	}
}

func TestApplyProviderTransformsConvertsOrphanToolResult(t *testing.T) {
	req := ChatRequest{
		Model: "openai/gpt-4o",
		Messages: []Message{
			{Role: "user", Content: "start"},
			{Role: "tool", ToolCallID: "missing-call", Name: "read_file", Content: "file contents"},
			{Role: "assistant", Content: "done"},
		},
	}

	got := applyProviderTransforms(req, "openai")

	if len(got.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[1].Role != "user" {
		t.Fatalf("expected orphan tool result to become user context, got %+v", got.Messages[1])
	}
	text, ok := got.Messages[1].Content.(string)
	if !ok || !strings.Contains(text, "without a matching tool call") || !strings.Contains(text, "file contents") {
		t.Fatalf("unexpected orphan tool context: %#v", got.Messages[1].Content)
	}
}

func TestApplyProviderTransformsResolvesPendingBeforeOrphanToolResult(t *testing.T) {
	req := ChatRequest{
		Model: "openai/gpt-4o",
		Messages: []Message{
			{Role: "user", Content: "start"},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call-1",
						Type: "function",
						Function: FunctionCall{
							Name:      "read_file",
							Arguments: `{"path":"README.md"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "missing-call", Name: "read_file", Content: "orphaned contents"},
			{Role: "user", Content: "next"},
		},
	}

	got := applyProviderTransforms(req, "openai")

	if len(got.Messages) != 5 {
		t.Fatalf("expected 5 messages, got %d: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[2].Role != "tool" || got.Messages[2].ToolCallID != "call-1" {
		t.Fatalf("expected missing result before orphan context, got %+v", got.Messages[2])
	}
	if got.Messages[3].Role != "user" {
		t.Fatalf("expected orphan result to become user context after pending repair, got %+v", got.Messages[3])
	}
}

func TestApplyProviderTransformsScrubsAnthropicToolIDs(t *testing.T) {
	req := ChatRequest{
		Model: "anthropic/claude-3.5-sonnet",
		Messages: []Message{
			{Role: "user", Content: "inspect"},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call:bad id!",
						Type: "function",
						Function: FunctionCall{
							Name:      "read_file",
							Arguments: `{"path":"README.md"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call:bad id!", Name: "read_file", Content: "contents"},
		},
	}

	got := applyProviderTransforms(req, "anthropic")

	if len(got.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %+v", len(got.Messages), got.Messages)
	}
	callID := got.Messages[1].ToolCalls[0].ID
	if callID != "call_bad_id_" {
		t.Fatalf("scrubbed call id=%q want call_bad_id_", callID)
	}
	if got.Messages[2].ToolCallID != callID {
		t.Fatalf("tool result id=%q want %q", got.Messages[2].ToolCallID, callID)
	}
}

func TestApplyProviderTransformsAddsLiteLLMNoopToolForToolHistory(t *testing.T) {
	req := ChatRequest{
		Model: "litellm/claude",
		Messages: []Message{
			{Role: "user", Content: "inspect"},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					{
						ID:   "call-1",
						Type: "function",
						Function: FunctionCall{
							Name:      "read_file",
							Arguments: `{"path":"README.md"}`,
						},
					},
				},
			},
			{Role: "tool", ToolCallID: "call-1", Name: "read_file", Content: "contents"},
		},
	}

	got := applyProviderTransforms(req, "litellm")

	if len(got.Tools) != 1 {
		t.Fatalf("expected one noop tool, got %+v", got.Tools)
	}
	fn, ok := got.Tools[0]["function"].(map[string]any)
	if !ok {
		t.Fatalf("noop tool missing function payload: %+v", got.Tools[0])
	}
	if fn["name"] != "_noop" {
		t.Fatalf("noop tool name=%v want _noop", fn["name"])
	}
	if got.ToolChoice != "auto" {
		t.Fatalf("tool choice=%q want auto", got.ToolChoice)
	}
}

func TestApplyProviderTransformsDropsEmptyAssistantMessages(t *testing.T) {
	req := ChatRequest{
		Model: "openai/gpt-4o",
		Messages: []Message{
			{Role: "user", Content: "start"},
			{Role: "assistant", Content: ""},
			{Role: "assistant", Content: nil, Reasoning: "reasoned answer"},
			{Role: "user", Content: "next"},
		},
	}

	got := applyProviderTransforms(req, "openai")

	if len(got.Messages) != 3 {
		t.Fatalf("expected 3 messages after dropping empty assistant turn, got %d: %+v", len(got.Messages), got.Messages)
	}
	if got.Messages[1].Role != "assistant" || got.Messages[1].Content != "reasoned answer" {
		t.Fatalf("expected reasoning-backed assistant message at index 1, got %+v", got.Messages[1])
	}
}
