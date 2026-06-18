package tui

import (
	"testing"

	"m31labs.dev/buckley/pkg/model"
)

func TestStreamUsageStats_UsesProviderUsage(t *testing.T) {
	stats := streamUsageStats("test/model", "ignored", &model.Usage{
		PromptTokens:     10,
		CompletionTokens: 15,
		TotalTokens:      25,
	}, nil)

	if stats.tokens != 25 {
		t.Fatalf("tokens = %d, want 25", stats.tokens)
	}
	if stats.costCents != 0 {
		t.Fatalf("costCents = %f, want 0 without model manager", stats.costCents)
	}
}

func TestStreamUsageStats_EstimatesTokensWithoutUsage(t *testing.T) {
	stats := streamUsageStats("test/model", "1234567890123456", nil, nil)
	if stats.tokens != 4 {
		t.Fatalf("tokens = %d, want estimated 4", stats.tokens)
	}
	if stats.costCents != 0 {
		t.Fatalf("costCents = %f, want 0 without model manager", stats.costCents)
	}
}

func TestStreamUsageStats_EmptyFallback(t *testing.T) {
	stats := streamUsageStats("test/model", "", nil, nil)
	if stats.tokens != 0 {
		t.Fatalf("tokens = %d, want 0", stats.tokens)
	}
}
