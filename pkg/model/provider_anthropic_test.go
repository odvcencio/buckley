package model

import "testing"

func TestAnthropicRequest_PromptCacheDisabled(t *testing.T) {
	provider := NewAnthropicProvider("test-key", "", false)
	req := ChatRequest{
		Model: "anthropic/claude-3.5-sonnet",
		Messages: []Message{
			{Role: "system", Content: "System prompt"},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there"},
		},
	}

	anthReq, err := provider.toAnthropicRequest(req, false)
	if err != nil {
		t.Fatalf("toAnthropicRequest error: %v", err)
	}

	if _, ok := anthReq.System.(string); !ok {
		t.Fatalf("expected system to be string, got %T", anthReq.System)
	}

	for i, msg := range anthReq.Messages {
		for j, part := range msg.Content {
			if part.CacheControl != nil {
				t.Fatalf("unexpected cache_control in message %d part %d", i, j)
			}
		}
	}
}

func TestAnthropicRequest_PromptCacheApplied(t *testing.T) {
	provider := NewAnthropicProvider("test-key", "", false)
	req := ChatRequest{
		Model: "anthropic/claude-3.5-sonnet",
		Messages: []Message{
			{Role: "system", Content: "System prompt"},
			{Role: "user", Content: "Hello"},
			{Role: "assistant", Content: "Hi there"},
			{Role: "user", Content: "Next"},
		},
		PromptCache: &PromptCache{
			Enabled:        true,
			SystemMessages: 1,
			TailMessages:   2,
		},
	}

	anthReq, err := provider.toAnthropicRequest(req, false)
	if err != nil {
		t.Fatalf("toAnthropicRequest error: %v", err)
	}

	systemBlocks, ok := anthReq.System.([]anthropicContent)
	if !ok {
		t.Fatalf("expected system blocks, got %T", anthReq.System)
	}
	if len(systemBlocks) != 1 {
		t.Fatalf("expected 1 system block, got %d", len(systemBlocks))
	}
	if systemBlocks[0].CacheControl == nil || systemBlocks[0].CacheControl.Type != "ephemeral" {
		t.Fatalf("expected cache_control on system block")
	}

	for i, msg := range anthReq.Messages {
		for j, part := range msg.Content {
			hasCache := part.CacheControl != nil && part.CacheControl.Type == "ephemeral"
			if i < len(anthReq.Messages)-2 {
				if hasCache {
					t.Fatalf("unexpected cache_control on message %d part %d", i, j)
				}
				continue
			}
			if !hasCache {
				t.Fatalf("missing cache_control on message %d part %d", i, j)
			}
		}
	}
}
