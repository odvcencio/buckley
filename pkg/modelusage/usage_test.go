package modelusage

import (
	"testing"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestFromUsagePreservesReportedDetailsWithoutDoubleCounting(t *testing.T) {
	reasoning := 20
	cached := 30
	got := FromUsage(model.Usage{
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
	})
	if got.Total() != 150 {
		t.Fatalf("Total() = %d, want 150 without adding reported reasoning subset", got.Total())
	}
	if got.Input != 100 || got.Output != 50 || got.ReportedTotal != 150 || got.ReportedCacheWrite != 12 {
		t.Fatalf("usage = %+v, want split and reported total/cache write preserved", got)
	}
	if got.ReportedReasoning == nil || *got.ReportedReasoning != reasoning {
		t.Fatalf("ReportedReasoning = %v, want 20", got.ReportedReasoning)
	}
	if got.ReportedCachedInput == nil || *got.ReportedCachedInput != cached {
		t.Fatalf("ReportedCachedInput = %v, want 30", got.ReportedCachedInput)
	}
}

func TestFromUsageTotalOnlyAndInconsistent(t *testing.T) {
	totalOnly := FromUsage(model.Usage{TotalTokens: 42})
	if totalOnly != (transparency.TokenUsage{ReportedTotal: 42, Unclassified: 42}) {
		t.Fatalf("total-only usage = %+v, want reported total as unclassified", totalOnly)
	}

	inconsistent := FromUsage(model.Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 140})
	if inconsistent.Input != 100 || inconsistent.Output != 50 || inconsistent.ReportedTotal != 140 || !inconsistent.ReportedUsageInconsistent {
		t.Fatalf("inconsistent usage = %+v, want split retained and inconsistency flagged", inconsistent)
	}
}

func TestFromResponsePreservesPopulatedUsageEvenWhenUsagePresentFalse(t *testing.T) {
	got := FromResponse(&model.ChatResponse{
		UsagePresent: false,
		Usage:        model.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
	})
	if got.Input != 3 || got.Output != 2 || got.ReportedTotal != 5 || !got.UsageEvidencePresent || got.UsageEvidenceMissing {
		t.Fatalf("usage = %+v, want populated custom-provider usage without missing flag", got)
	}

	explicitZero := FromResponse(&model.ChatResponse{UsagePresent: true})
	if !explicitZero.UsageEvidencePresent || explicitZero.UsageEvidenceMissing || explicitZero.Total() != 0 {
		t.Fatalf("explicit zero usage = %+v, want present marker without estimated counts", explicitZero)
	}
	if !HasEvidence(explicitZero) {
		t.Fatal("HasEvidence returned false for explicit zero usage evidence")
	}

	missing := FromResponse(&model.ChatResponse{UsagePresent: false})
	if !missing.UsageEvidenceMissing || missing.Total() != 0 {
		t.Fatalf("missing usage = %+v, want evidence-missing marker only", missing)
	}
	if !HasEvidence(missing) {
		t.Fatal("HasEvidence returned false for explicit missing-usage evidence")
	}
}
