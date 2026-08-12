package modelprofile

import (
	"math"
	"testing"
	"time"
)

func TestAggregate_TracksTaskEconomicsAndIndependentSamples(t *testing.T) {
	base := validProfile()
	base.SampleSize = 0
	base.Metrics.ToolReliability = 0.8
	base.Metrics.StructuredOutputReliability = 0.9
	toolSuccess := true
	verificationPass := true
	verificationFail := false
	got, err := Aggregate(base, []Observation{
		{Succeeded: true, ToolSucceeded: &toolSuccess, VerificationPassed: &verificationPass, LatencyMS: 100, PromptTokens: 80, CompletionTokens: 20, TokensObserved: true, CostUSD: 0.02, CostObserved: true},
		{Succeeded: false, VerificationPassed: &verificationFail, LatencyMS: 200, PromptTokens: 160, CompletionTokens: 40, TokensObserved: true, CostUSD: 0.04, CostObserved: true},
	}, time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if got.SampleSize != 2 || got.Samples.TaskSuccess != 2 || got.Metrics.TaskSuccessRate != 0.5 {
		t.Fatalf("task aggregate = %+v", got)
	}
	if got.Samples.ToolReliability != 1 || got.Metrics.ToolReliability != 1 {
		t.Fatalf("tool aggregate = %+v", got)
	}
	if got.Samples.Verification != 2 || got.Metrics.VerificationPassRate != 0.5 {
		t.Fatalf("verification aggregate = %+v", got)
	}
	if got.Metrics.StructuredOutputReliability != 0.9 || got.Samples.StructuredOutput != 0 {
		t.Fatalf("unobserved structured metric changed: %+v", got)
	}
	if got.Samples.Latency != 2 || got.Metrics.AverageTaskLatencyMS != 150 || got.Metrics.LatencyP50MS != 200 || got.Metrics.LatencyP95MS != 200 {
		t.Fatalf("latency aggregate = %+v", got)
	}
	if got.Samples.Tokens != 2 || got.Metrics.AverageTokensPerTask != 150 {
		t.Fatalf("token aggregate = %+v", got)
	}
	if got.Samples.Cost != 2 || math.Abs(got.Metrics.AverageCostUSDPerTask-0.03) > 1e-9 || math.Abs(got.Metrics.CostUSDPerSuccessfulTask-0.06) > 1e-9 {
		t.Fatalf("cost aggregate = %+v", got)
	}
}

func TestAggregate_LegacyRatiosKeepTheirHistoricalDenominator(t *testing.T) {
	base := validProfile()
	base.SampleSize = 10
	base.Metrics.ToolReliability = 0.8
	success := true
	got, err := Aggregate(base, []Observation{{Succeeded: true, ToolSucceeded: &success}}, time.Now())
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if got.Samples.ToolReliability != 11 || math.Abs(got.Metrics.ToolReliability-(9.0/11.0)) > 1e-9 {
		t.Fatalf("legacy tool aggregate = %+v", got)
	}
	if got.Samples.TaskSuccess != 1 || got.Metrics.TaskSuccessRate != 1 {
		t.Fatalf("legacy task aggregate should not treat historical tasks as failures: %+v", got)
	}
}

func TestAggregate_UnobservedSignalIsNotCountedAsFailureOnLaterBatch(t *testing.T) {
	base := validProfile()
	base.SampleSize = 0
	base.Confidence = 0
	base.Metrics.ToolReliability = 0

	first, err := Aggregate(base, []Observation{{Succeeded: true}}, time.Now())
	if err != nil {
		t.Fatalf("first Aggregate: %v", err)
	}
	success := true
	second, err := Aggregate(first, []Observation{{Succeeded: true, ToolSucceeded: &success}}, time.Now())
	if err != nil {
		t.Fatalf("second Aggregate: %v", err)
	}
	if second.Samples.ToolReliability != 1 || second.Metrics.ToolReliability != 1 {
		t.Fatalf("tool reliability = %.2f over %d samples, want 1.00 over 1", second.Metrics.ToolReliability, second.Samples.ToolReliability)
	}
}

func TestAggregate_CumulativeLatencyDoesNotMislabelLatestBatchPercentiles(t *testing.T) {
	base := validProfile()
	base.SampleSize = 0
	base.Confidence = 0

	first, err := Aggregate(base, []Observation{{Succeeded: true, LatencyMS: 100}}, time.Now())
	if err != nil {
		t.Fatalf("first Aggregate: %v", err)
	}
	second, err := Aggregate(first, []Observation{{Succeeded: true, LatencyMS: 300}}, time.Now())
	if err != nil {
		t.Fatalf("second Aggregate: %v", err)
	}
	if second.Metrics.AverageTaskLatencyMS != 200 || second.Metrics.LatencyP50MS != 0 || second.Metrics.LatencyP95MS != 0 {
		t.Fatalf("latency metrics = %+v, want mean 200 with unavailable cumulative percentiles", second.Metrics)
	}
}

func TestProfileResolvedClass_RequiresFrontierTaskEvidenceWhenMeasured(t *testing.T) {
	profile := validProfile()
	profile.SampleSize = 100
	profile.Confidence = 0.95
	profile.Capabilities.Continuation = true
	profile.Capabilities.ParallelToolCalls = true
	profile.Metrics.ToolReliability = 0.95
	profile.Metrics.StructuredOutputReliability = 0.95
	profile.Metrics.ContinuationReliability = 0.95
	profile.Metrics.ParallelCallReliability = 0.95
	profile.Metrics.EffectiveContextTokens = 128 * 1024
	profile.Samples.TaskSuccess = 20
	profile.Metrics.TaskSuccessRate = 0.85
	if got := profile.ResolvedClass(); got != ClassBalanced {
		t.Fatalf("ResolvedClass = %s, want balanced", got)
	}
	profile.Metrics.TaskSuccessRate = 0.90
	if got := profile.ResolvedClass(); got != ClassFrontier {
		t.Fatalf("ResolvedClass = %s, want frontier", got)
	}
	profile.Metrics.TaskSuccessRate = 0.70
	if got := profile.ResolvedClass(); got != ClassWeak {
		t.Fatalf("ResolvedClass = %s, want weak", got)
	}
}

func validProfile() Profile {
	return Profile{
		SchemaVersion: SchemaVersion,
		ModelID:       "example/model",
		Version:       "v1",
		Confidence:    0.9,
		MeasuredAt:    time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC),
		Metrics: Metrics{
			ToolReliability:             0.9,
			ArgumentRepairReliability:   0.9,
			StructuredOutputReliability: 0.9,
			ParallelCallReliability:     0.9,
			EditFidelity:                0.9,
			VerificationPassRate:        0.9,
			ContinuationReliability:     0.9,
		},
	}
}
