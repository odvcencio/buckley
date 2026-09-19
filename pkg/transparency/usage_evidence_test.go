package transparency

import "testing"

func TestTokenUsageEvidenceMarkersAggregateAndMissingMarksCostUnknown(t *testing.T) {
	got := AddTokenUsage(TokenUsage{Input: 10, Output: 5, UsageEvidencePresent: true}, TokenUsage{UsageEvidenceMissing: true})
	if !got.UsageEvidencePresent || !got.UsageEvidenceMissing {
		t.Fatalf("UsageEvidenceMissing = false after aggregation: %+v", got)
	}
	if !CostUnknownForUsage(got, ModelPricing{InputPerMillion: 1, OutputPerMillion: 2}) {
		t.Fatalf("CostUnknownForUsage = false for missing usage evidence with nonzero pricing")
	}
	if CostUnknownForUsage(TokenUsage{UsageEvidenceMissing: true}, ModelPricing{}) {
		t.Fatal("CostUnknownForUsage = true for missing usage evidence with explicit zero/free pricing")
	}
}

func TestCostUnknownForUsagePreservesExistingClassifierBoundaries(t *testing.T) {
	if CostUnknownForUsage(TokenUsage{Input: 1, Output: 2}, ModelPricing{InputPerMillion: 1, OutputPerMillion: 2}) {
		t.Fatal("ordinary split usage unexpectedly cost-unknown")
	}
	if !CostUnknownForUsage(TokenUsage{Unclassified: 42, ReportedTotal: 42}, ModelPricing{InputPerMillion: 1, OutputPerMillion: 2}) {
		t.Fatal("total-only unclassified usage should be cost-unknown")
	}
	if !CostUnknownForUsage(TokenUsage{ReportedUsageInconsistent: true}, ModelPricing{}) {
		t.Fatal("inconsistent reported usage should be cost-unknown")
	}
	if !CostUnknownForUsage(TokenUsage{ReportedCacheWrite: 1}, ModelPricing{InputPerMillion: 1}) {
		t.Fatal("cache-write usage should be cost-unknown without authoritative cache-write rate")
	}
	reasoning := 1
	if !CostUnknownForUsage(TokenUsage{Output: 2, ReportedReasoning: &reasoning}, ModelPricing{OutputPerMillion: 2, ReasoningPerMillion: 3}) {
		t.Fatal("reported reasoning subset with special reasoning rate should be cost-unknown")
	}
}
