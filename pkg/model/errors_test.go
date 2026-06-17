package model

import (
	"strings"
	"testing"
)

func TestNoResponseChoicesMessageIncludesRequestShape(t *testing.T) {
	enabled := true
	msg := NoResponseChoicesMessage(ChatRequest{
		Model:               "z-ai/glm-5.2",
		Messages:            []Message{{Role: "user", Content: "secret prompt text"}},
		Tools:               []map[string]any{{"type": "function"}, {"type": "function"}},
		ToolChoice:          "auto",
		Reasoning:           &ReasoningConfig{Enabled: &enabled},
		MaxCompletionTokens: 512,
		SessionID:           "session-123",
	}, &ChatResponse{
		ID:    "resp-123",
		Model: "openrouter/z-ai/glm-5.2",
	})

	for _, want := range []string{
		"model z-ai/glm-5.2 returned no response choices",
		"response_id=resp-123",
		"response_model=openrouter/z-ai/glm-5.2",
		"messages=1",
		"tools=2",
		"tool_choice=auto",
		"reasoning_enabled=true",
		"max_completion_tokens=512",
		"session=session-123",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "secret prompt text") {
		t.Fatalf("message leaked prompt content:\n%s", msg)
	}
}

func TestNilChatResponseMessageIncludesRequestShape(t *testing.T) {
	msg := NilChatResponseMessage(ChatRequest{
		Model:     "xiaomi/mimo-v2.5-pro",
		Messages:  []Message{{Role: "user", Content: "private prompt"}},
		MaxTokens: 128,
		SessionID: "chat-check",
	})

	for _, want := range []string{
		"model xiaomi/mimo-v2.5-pro returned nil chat response",
		"messages=1",
		"max_tokens=128",
		"session=chat-check",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "private prompt") {
		t.Fatalf("message leaked prompt content:\n%s", msg)
	}
}
