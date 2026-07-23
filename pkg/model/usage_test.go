package model

import "testing"

func TestAddUsage_PreservesDetailedAccounting(t *testing.T) {
	total := AddUsage(Usage{
		PromptTokens: 10,
		PromptTokensDetails: &PromptTokensDetails{
			CachedTokens: 4,
		},
	}, Usage{
		PromptTokens:     20,
		CompletionTokens: 5,
		TotalTokens:      25,
		PromptTokensDetails: &PromptTokensDetails{
			CachedTokens: 12,
		},
		CompletionTokenDetails: &CompletionTokenDetails{
			ReasoningTokens: 3,
		},
		CacheWriteTokens: 7,
	})

	if total.PromptTokens != 30 || total.CompletionTokens != 5 || total.TotalTokens != 25 {
		t.Fatalf("basic usage not combined: %+v", total)
	}
	if total.PromptTokensDetails == nil || total.PromptTokensDetails.CachedTokens != 16 {
		t.Fatalf("cached usage not combined: %+v", total.PromptTokensDetails)
	}
	if total.CompletionTokenDetails == nil || total.CompletionTokenDetails.ReasoningTokens != 3 {
		t.Fatalf("reasoning usage not combined: %+v", total.CompletionTokenDetails)
	}
	if total.CacheWriteTokens != 7 {
		t.Fatalf("cache write usage = %d, want 7", total.CacheWriteTokens)
	}
}
