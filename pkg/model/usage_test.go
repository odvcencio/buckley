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
		Estimated:        true,
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
	if !total.Estimated {
		t.Fatal("combined usage should retain estimated provenance")
	}
}

func TestEstimateChatUsage_MarksLocalEstimateAndCountsGeneratedMaterial(t *testing.T) {
	req := ChatRequest{
		Model:    "openai_compatible/test-model",
		Messages: []Message{{Role: "user", Content: "explain the result"}},
		Tools: []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name": "read_file",
			},
		}},
	}
	response := Message{
		Role:      "assistant",
		Content:   "done",
		Reasoning: "checked the evidence",
	}

	usage := EstimateChatUsage(req, response)
	if !usage.Estimated {
		t.Fatal("usage should be marked estimated")
	}
	if usage.PromptTokens <= 0 || usage.CompletionTokens <= 0 {
		t.Fatalf("estimated split = %d/%d, want positive counts", usage.PromptTokens, usage.CompletionTokens)
	}
	if usage.TotalTokens != usage.PromptTokens+usage.CompletionTokens {
		t.Fatalf("total = %d, want %d", usage.TotalTokens, usage.PromptTokens+usage.CompletionTokens)
	}
}
