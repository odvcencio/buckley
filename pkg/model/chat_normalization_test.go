package model

import (
	"encoding/json"
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

func TestApplyProviderTransformsAddsCompatibleNoopToolForToolHistory(t *testing.T) {
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

	for _, providerID := range []string{"openai_compatible", "litellm"} {
		t.Run(providerID, func(t *testing.T) {
			got := applyProviderTransforms(req, providerID)
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
		})
	}
}

func TestApplyProviderTransformsPreservesCompatibleNoToolFinalizationIntent(t *testing.T) {
	req := ChatRequest{
		Model:      "litellm/claude",
		ToolChoice: "none",
		Messages: []Message{
			{Role: "user", Content: "inspect"},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:   "call-1",
					Type: "function",
					Function: FunctionCall{
						Name:      "read_file",
						Arguments: `{"path":"README.md"}`,
					},
				}},
			},
			{Role: "tool", ToolCallID: "call-1", Name: "read_file", Content: "contents"},
		},
	}

	for _, providerID := range []string{"openai_compatible", "litellm"} {
		t.Run(providerID, func(t *testing.T) {
			got := applyProviderTransforms(req, providerID)
			if len(got.Tools) != 1 {
				t.Fatalf("expected compatibility noop tool, got %+v", got.Tools)
			}
			if got.ToolChoice != "none" {
				t.Fatalf("tool choice=%q want explicit no-tool finalization intent", got.ToolChoice)
			}
		})
	}
}

func TestApplyProviderTransformsSuppressesCompatibleNoopForCatalogConfirmedToollessRequest(t *testing.T) {
	req := ChatRequest{
		Model:                            "litellm/o1-mini",
		ToolChoice:                       "auto",
		ToolsCatalogConfirmedUnavailable: true,
		Messages: []Message{
			{Role: "user", Content: "inspect"},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:   "call-1",
					Type: "function",
					Function: FunctionCall{
						Name:      "read_file",
						Arguments: `{"path":"README.md"}`,
					},
				}},
			},
			{Role: "tool", ToolCallID: "call-1", Name: "read_file", Content: "rejected"},
		},
	}

	for _, providerID := range []string{"openai_compatible", "litellm"} {
		t.Run(providerID, func(t *testing.T) {
			got := applyProviderTransforms(req, providerID)
			if len(got.Tools) != 0 {
				t.Fatalf("tools = %+v, want none", got.Tools)
			}
			if got.ToolChoice != "" {
				t.Fatalf("tool choice = %q, want omitted", got.ToolChoice)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal request: %v", err)
			}
			if strings.Contains(string(encoded), "ToolsCatalogConfirmedUnavailable") ||
				strings.Contains(string(encoded), "tools_catalog") {
				t.Fatalf("internal marker leaked into JSON: %s", encoded)
			}
		})
	}
}

func TestApplyProviderTransformsDropsToolChoiceWithoutTools(t *testing.T) {
	req := ChatRequest{
		Model:      "x-ai/grok-4.5",
		ToolChoice: "none",
		Messages: []Message{
			{Role: "user", Content: "return the final answer"},
		},
	}

	got := applyProviderTransforms(req, "openrouter")

	if got.ToolChoice != "" {
		t.Fatalf("tool choice=%q want omitted when tools are absent", got.ToolChoice)
	}
}

func TestApplyProviderTransformsReasoningEffortWinsOverTokenBudget(t *testing.T) {
	originalReasoning := &ReasoningConfig{Effort: " Medium ", MaxTokens: 2048}
	req := ChatRequest{
		Model:     "google/gemini-3.8-flash",
		Reasoning: originalReasoning,
		Messages:  []Message{{Role: "user", Content: "review this"}},
	}

	got := applyProviderTransforms(req, "openrouter")

	if got.Reasoning == nil {
		t.Fatal("Reasoning = nil, want effort-only reasoning")
	}
	if got.Reasoning == originalReasoning {
		t.Fatalf("Reasoning reused caller pointer, want cloned config")
	}
	if got.Reasoning.Effort != "medium" || got.Reasoning.MaxTokens != 0 {
		t.Fatalf("Reasoning = %+v, want effort medium without max_tokens", got.Reasoning)
	}
	if originalReasoning.Effort != " Medium " || originalReasoning.MaxTokens != 2048 {
		t.Fatalf("original reasoning mutated: %+v", originalReasoning)
	}
}

func TestApplyProviderTransformsPreservesReasoningTokenBudgetOnly(t *testing.T) {
	req := ChatRequest{
		Model:     "anthropic/claude-test",
		Reasoning: &ReasoningConfig{MaxTokens: 2048},
		Messages:  []Message{{Role: "user", Content: "review this"}},
	}

	got := applyProviderTransforms(req, "openrouter")

	if got.Reasoning == nil || got.Reasoning.Effort != "" || got.Reasoning.MaxTokens != 2048 {
		t.Fatalf("Reasoning = %+v, want token-budget-only reasoning preserved", got.Reasoning)
	}
}

func TestNormalizeReasoningConfigIsNilSafeAndIdempotent(t *testing.T) {
	if got := NormalizeReasoningConfig(nil); got != nil {
		t.Fatalf("NormalizeReasoningConfig(nil) = %+v, want nil", got)
	}

	enabled := true
	original := &ReasoningConfig{Effort: " Medium ", MaxTokens: 2048, Enabled: &enabled}
	first := NormalizeReasoningConfig(original)
	second := NormalizeReasoningConfig(first)
	if first == nil || second == nil || first == original || second == first {
		t.Fatalf("normalization must return independent non-nil copies: original=%p first=%p second=%p", original, first, second)
	}
	if first.Effort != "medium" || first.MaxTokens != 0 || second.Effort != "medium" || second.MaxTokens != 0 {
		t.Fatalf("repeated normalization changed envelope: first=%+v second=%+v", first, second)
	}
	if first.Enabled == nil || second.Enabled == nil || first.Enabled == original.Enabled || second.Enabled == first.Enabled || !*second.Enabled {
		t.Fatalf("normalization did not safely clone enabled pointers: original=%+v first=%+v second=%+v", original, first, second)
	}
	if original.Effort != " Medium " || original.MaxTokens != 2048 {
		t.Fatalf("normalization mutated caller envelope: %+v", original)
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
	if got.Messages[1].Reasoning != "" {
		t.Fatalf("expected direct OpenAI request to strip reasoning metadata, got %+v", got.Messages[1])
	}
}

func TestApplyProviderTransformsPreservesOpenRouterReasoningDetails(t *testing.T) {
	req := ChatRequest{
		Model: "qwen/qwen3.6-max-preview",
		Messages: []Message{
			{Role: "user", Content: "start"},
			{
				Role:      "assistant",
				Content:   "answer",
				Reasoning: "thinking",
				ReasoningDetails: []ReasoningDetail{{
					Type:     "reasoning.text",
					Text:     "thinking",
					HasIndex: true,
				}},
			},
			{Role: "user", Content: "next"},
		},
	}

	got := applyProviderTransforms(req, "openrouter")

	if len(got.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(got.Messages))
	}
	if got.Messages[1].Reasoning != "thinking" {
		t.Fatalf("expected reasoning to be preserved, got %+v", got.Messages[1])
	}
	if len(got.Messages[1].ReasoningDetails) != 1 || got.Messages[1].ReasoningDetails[0].Text != "thinking" {
		t.Fatalf("expected reasoning details to be preserved, got %+v", got.Messages[1].ReasoningDetails)
	}
}

func TestApplyProviderTransformsPreservesConfiguredCompatibleReasoningExactly(t *testing.T) {
	raw := "思\n\n\n考\n\n\n alpha\n\n\n beta\n\n\n γ\n\n\n delta\n\n\n epsilon\n\n\n zeta\n\n\n eta\n\n\n."
	req := ChatRequest{
		Model: "openai_compatible/glm-5.3-flash",
		Messages: []Message{
			{Role: "user", Content: "start"},
			{
				Role:      "assistant",
				Content:   "",
				Reasoning: raw,
				ToolCalls: []ToolCall{{
					ID:   "call_α",
					Type: "function",
					Function: FunctionCall{
						Name:      "read_fixture",
						Arguments: `{"path":"fixtures/未知.txt"}`,
					},
				}},
			},
			{Role: "tool", ToolCallID: "call_α", Name: "read_fixture", Content: `{"nonce":"値-123"}`},
		},
	}

	got := applyProviderTransformsWithOptions(req, "openai_compatible", providerTransformOptions{PreserveReasoningMessages: true})

	if len(got.Messages) != 3 {
		t.Fatalf("messages = %d, want 3: %+v", len(got.Messages), got.Messages)
	}
	assistant := got.Messages[1]
	if assistant.Reasoning != raw {
		t.Fatalf("reasoning changed:\n got %q\nwant %q", assistant.Reasoning, raw)
	}
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call_α" ||
		assistant.ToolCalls[0].Function.Arguments != `{"path":"fixtures/未知.txt"}` {
		t.Fatalf("tool call did not round-trip: %+v", assistant.ToolCalls)
	}
	if got.Messages[2].ToolCallID != "call_α" {
		t.Fatalf("tool_call_id = %q, want call_α", got.Messages[2].ToolCallID)
	}
}

func TestApplyProviderTransformsStripsCompatibleReasoningWithoutOptIn(t *testing.T) {
	req := ChatRequest{
		Model: "openai_compatible/glm-5.3-flash",
		Messages: []Message{{
			Role:      "assistant",
			Content:   "answer",
			Reasoning: "private",
		}},
	}

	got := applyProviderTransforms(req, "openai_compatible")

	if len(got.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(got.Messages))
	}
	if got.Messages[0].Reasoning != "" {
		t.Fatalf("reasoning leaked without opt-in: %+v", got.Messages[0])
	}
}

func TestApplyProviderTransformsDoesNotPromoteConfiguredReasoningOnlyAssistantToContent(t *testing.T) {
	req := ChatRequest{
		Model: "openai_compatible/glm-5.3-flash",
		Messages: []Message{
			{Role: "user", Content: "start"},
			{
				Role:      "assistant",
				Content:   "",
				Reasoning: "private\n\n\n chain\n\n\n raw\n\n\n bytes\n\n\n stay\n\n\n private\n\n\n across\n\n\n tool\n\n\n turn",
			},
			{Role: "tool", ToolCallID: "missing", Name: "read_fixture", Content: "orphan"},
		},
	}

	got := applyProviderTransformsWithOptions(req, "openai_compatible", providerTransformOptions{PreserveReasoningMessages: true})

	if len(got.Messages) < 2 {
		t.Fatalf("messages = %d, want preserved assistant", len(got.Messages))
	}
	assistant := got.Messages[1]
	if assistant.Role != "assistant" {
		t.Fatalf("message[1] role = %q, want assistant: %+v", assistant.Role, got.Messages)
	}
	if !messageContentEmpty(assistant.Content) {
		t.Fatalf("private reasoning was promoted to visible content: %#v", assistant.Content)
	}
	if !strings.Contains(assistant.Reasoning, "raw") {
		t.Fatalf("reasoning continuity was not preserved: %+v", assistant)
	}
}
