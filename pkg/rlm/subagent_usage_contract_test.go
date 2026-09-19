package rlm

import (
	"bytes"
	"reflect"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestRecordSubAgentModelResponsePreservesRichUsage(t *testing.T) {
	reasoning := 20
	cached := 30
	result := &SubAgentResult{}
	recordSubAgentModelResponse(result, &model.ChatResponse{
		UsagePresent: true,
		Usage: model.Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			PromptTokensDetails: &model.PromptTokensDetails{
				CachedTokens: cached,
			},
			CompletionTokenDetails: &model.CompletionTokenDetails{
				ReasoningTokens: reasoning,
			},
			CacheWriteTokens: 12,
		},
	})

	want := transparency.TokenUsage{Input: 100, Output: 50, ReportedTotal: 150, ReportedReasoning: &reasoning, ReportedCachedInput: &cached, ReportedCacheWrite: 12}
	want.UsageEvidencePresent = true
	if !reflect.DeepEqual(result.Usage, want) {
		t.Fatalf("Usage = %#v, want %#v", result.Usage, want)
	}
	if result.TokensUsed != 150 || result.InputTokens != 100 || result.OutputTokens != 50 {
		t.Fatalf("legacy counters = total:%d input:%d output:%d, want 150/100/50", result.TokensUsed, result.InputTokens, result.OutputTokens)
	}
}

func TestRecordSubAgentModelResponsePreservesMissingAndInconsistentUsage(t *testing.T) {
	result := &SubAgentResult{}
	recordSubAgentModelResponse(result, &model.ChatResponse{UsagePresent: false})
	recordSubAgentModelResponse(result, &model.ChatResponse{
		UsagePresent: true,
		Usage:        model.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 140, Estimated: true},
	})

	if !result.Usage.UsageEvidenceMissing {
		t.Fatalf("UsageEvidenceMissing = false: %+v", result.Usage)
	}
	if !result.Usage.UsageEvidencePresent {
		t.Fatalf("UsageEvidencePresent = false after populated second response: %+v", result.Usage)
	}
	if !result.Usage.ReportedUsageInconsistent || !result.Usage.Estimated {
		t.Fatalf("usage flags = %+v, want inconsistent and estimated retained", result.Usage)
	}
	if result.TokensUsed != 140 || result.InputTokens != 100 || result.OutputTokens != 50 {
		t.Fatalf("legacy counters = total:%d input:%d output:%d, want 140/100/50", result.TokensUsed, result.InputTokens, result.OutputTokens)
	}
}

func TestFinalizeSubAgentRawIncludesRichUsageWithoutReasoningText(t *testing.T) {
	reasoning := 4
	result := &SubAgentResult{
		AgentID:    "agent-1",
		ModelUsed:  "model-1",
		Summary:    "public summary",
		TokensUsed: 10,
		Usage:      transparency.TokenUsage{Output: 10, ReportedReasoning: &reasoning},
	}
	finalizeSubAgentResult(result, time.Now())
	if len(result.Raw) == 0 {
		t.Fatal("Raw is empty")
	}
	if !bytes.Contains(result.Raw, []byte(`"reported_reasoning":4`)) {
		t.Fatalf("Raw = %s, want reported reasoning metadata", result.Raw)
	}
	if bytes.Contains(result.Raw, []byte("private-reasoning-sentinel")) {
		t.Fatalf("Raw leaked private reasoning sentinel: %s", result.Raw)
	}
}
