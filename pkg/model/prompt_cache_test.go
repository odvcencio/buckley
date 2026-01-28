package model

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/config"
)

func TestApplyPromptCache_OpenAICompatibleProviders(t *testing.T) {
	providers := []string{"openrouter", "litellm"}
	for _, providerID := range providers {
		t.Run(providerID, func(t *testing.T) {
			cfg := &config.Config{
				PromptCache: config.PromptCacheConfig{
					Enabled:        true,
					Providers:      []string{providerID},
					SystemMessages: 1,
					TailMessages:   2,
				},
			}
			mgr := &Manager{config: cfg}
			req := ChatRequest{
				Model: "example",
				Messages: []Message{
					{Role: "system", Content: "sys"},
					{Role: "user", Content: "u1"},
					{Role: "assistant", Content: "a1"},
					{Role: "tool", Content: "tool output"},
					{Role: "user", Content: "u2"},
				},
			}

			out := mgr.applyPromptCache(req, providerID)

			if _, ok := req.Messages[0].Content.(string); !ok {
				t.Fatalf("expected original system content to remain string, got %T", req.Messages[0].Content)
			}
			if _, ok := req.Messages[2].Content.(string); !ok {
				t.Fatalf("expected original assistant content to remain string, got %T", req.Messages[2].Content)
			}

			isCached := func(content any) bool {
				parts, ok := content.([]ContentPart)
				if !ok {
					return false
				}
				for _, part := range parts {
					if part.CacheControl != nil && part.CacheControl.Type == promptCacheControlType {
						return true
					}
				}
				return false
			}

			if !isCached(out.Messages[0].Content) {
				t.Fatalf("expected system message to be cached")
			}
			if isCached(out.Messages[1].Content) {
				t.Fatalf("did not expect cache on first user message")
			}
			if !isCached(out.Messages[2].Content) {
				t.Fatalf("expected assistant message to be cached")
			}
			if isCached(out.Messages[3].Content) {
				t.Fatalf("did not expect cache on tool message")
			}
			if !isCached(out.Messages[4].Content) {
				t.Fatalf("expected tail user message to be cached")
			}
		})
	}
}

func TestApplyPromptCache_OpenAI(t *testing.T) {
	cfg := &config.Config{
		PromptCache: config.PromptCacheConfig{
			Enabled:        true,
			Providers:      []string{"openai"},
			SystemMessages: 0,
			TailMessages:   0,
			Key:            "user-123",
			Retention:      "24h",
		},
	}
	mgr := &Manager{config: cfg}
	req := ChatRequest{
		Model: "openai/gpt-4o",
		Messages: []Message{
			{Role: "user", Content: "Hello"},
		},
	}

	out := mgr.applyPromptCache(req, "openai")

	if out.PromptCacheKey != "user-123" {
		t.Fatalf("expected prompt_cache_key to be set, got %q", out.PromptCacheKey)
	}
	if out.PromptCacheRetention != "24h" {
		t.Fatalf("expected prompt_cache_retention to be set, got %q", out.PromptCacheRetention)
	}
	if _, ok := out.Messages[0].Content.(string); !ok {
		t.Fatalf("expected message content to remain string, got %T", out.Messages[0].Content)
	}
}
