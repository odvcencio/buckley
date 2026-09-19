package modelprofile

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
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

func TestAggregate_TaskSuccessObservedFalseDoesNotCountSuccessOrFailure(t *testing.T) {
	base := validProfile()
	base.SampleSize = 0
	base.Confidence = 0
	base.Metrics.TaskSuccessRate = 0.75
	unknown := false

	got, err := Aggregate(base, []Observation{{
		Succeeded:           true,
		TaskSuccessObserved: &unknown,
		LatencyMS:           100,
		PromptTokens:        80,
		CompletionTokens:    20,
		TokensObserved:      true,
		CostUSD:             0.02,
		CostObserved:        true,
	}}, time.Now())
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if got.SampleSize != 1 || got.Samples.TaskSuccess != 0 || got.Samples.TaskSuccessUnknown != 1 {
		t.Fatalf("task samples = %+v, sample_size=%d", got.Samples, got.SampleSize)
	}
	if got.Metrics.TaskSuccessRate != 0.75 {
		t.Fatalf("task success rate = %.2f, want preserved prior rate for unknown-only batch", got.Metrics.TaskSuccessRate)
	}
	if got.Samples.Latency != 1 || got.Samples.Tokens != 1 || got.Samples.Cost != 1 {
		t.Fatalf("operational samples = %+v", got.Samples)
	}
	if got.Metrics.CostUSDPerSuccessfulTask != 0 {
		t.Fatalf("cost per success = %.4f, want unavailable", got.Metrics.CostUSDPerSuccessfulTask)
	}
}

func TestAggregate_TaskSuccessObservedNilKeepsLegacySucceededBehavior(t *testing.T) {
	base := validProfile()
	base.SampleSize = 0
	got, err := Aggregate(base, []Observation{{Succeeded: true}}, time.Now())
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if got.Samples.TaskSuccess != 1 || got.Samples.TaskSuccessUnknown != 0 || got.Metrics.TaskSuccessRate != 1 {
		t.Fatalf("legacy task aggregate = %+v", got)
	}
}

func TestAggregate_MixedUnknownThenKnownPreservesOperationalMetrics(t *testing.T) {
	base := validProfile()
	base.SampleSize = 0
	base.Confidence = 0
	unknown := false
	known := true

	first, err := Aggregate(base, []Observation{{
		Succeeded:           true,
		TaskSuccessObserved: &unknown,
		LatencyMS:           100,
		PromptTokens:        5,
		CompletionTokens:    5,
		TokensObserved:      true,
		CostUSD:             0.01,
		CostObserved:        true,
	}}, time.Now())
	if err != nil {
		t.Fatalf("first Aggregate: %v", err)
	}
	second, err := Aggregate(first, []Observation{{
		Succeeded:           true,
		TaskSuccessObserved: &known,
		LatencyMS:           300,
		PromptTokens:        10,
		CompletionTokens:    10,
		TokensObserved:      true,
		CostUSD:             0.03,
		CostObserved:        true,
	}}, time.Now())
	if err != nil {
		t.Fatalf("second Aggregate: %v", err)
	}
	if second.SampleSize != 2 || second.Samples.TaskSuccess != 1 || second.Samples.TaskSuccessUnknown != 1 || second.Metrics.TaskSuccessRate != 1 {
		t.Fatalf("task evidence = %+v", second)
	}
	if second.Samples.Latency != 2 || second.Metrics.AverageTaskLatencyMS != 200 || second.Samples.Tokens != 2 || second.Metrics.AverageTokensPerTask != 15 || second.Samples.Cost != 2 || math.Abs(second.Metrics.AverageCostUSDPerTask-0.02) > 1e-9 {
		t.Fatalf("operational evidence = %+v", second)
	}
	if second.Metrics.CostUSDPerSuccessfulTask != 0 {
		t.Fatalf("cost per success = %.4f, want unavailable with unknown task evidence", second.Metrics.CostUSDPerSuccessfulTask)
	}
}

func TestAggregate_UnknownOnlyProfileDoesNotTriggerLegacyOptionalSignalFallback(t *testing.T) {
	base := validProfile()
	base.SampleSize = 0
	base.Confidence = 0
	base.Metrics.ToolReliability = 0
	unknown := false

	first, err := Aggregate(base, []Observation{{TaskSuccessObserved: &unknown}}, time.Now())
	if err != nil {
		t.Fatalf("first Aggregate: %v", err)
	}
	success := true
	second, err := Aggregate(first, []Observation{{TaskSuccessObserved: &unknown, ToolSucceeded: &success}}, time.Now())
	if err != nil {
		t.Fatalf("second Aggregate: %v", err)
	}
	if second.Samples.ToolReliability != 1 || second.Metrics.ToolReliability != 1 {
		t.Fatalf("tool reliability = %.2f over %d samples, want 1.00 over 1", second.Metrics.ToolReliability, second.Samples.ToolReliability)
	}
}

func TestAggregate_CostPerSuccessRequiresFullProfilePairedCoverage(t *testing.T) {
	base := validProfile()
	base.SampleSize = 0
	unknown := false
	known := true

	withUnknownCost, err := Aggregate(base, []Observation{{
		TaskSuccessObserved: &unknown,
		CostUSD:             0.25,
		CostObserved:        true,
	}}, time.Now())
	if err != nil {
		t.Fatalf("unknown Aggregate: %v", err)
	}
	withKnownNoCost, err := Aggregate(withUnknownCost, []Observation{{
		Succeeded:           true,
		TaskSuccessObserved: &known,
	}}, time.Now())
	if err != nil {
		t.Fatalf("known Aggregate: %v", err)
	}
	if withKnownNoCost.Samples.Cost != withKnownNoCost.Samples.TaskSuccess {
		t.Fatalf("test setup expected equal raw counts for disjoint evidence, got %+v", withKnownNoCost.Samples)
	}
	if withKnownNoCost.Metrics.CostUSDPerSuccessfulTask != 0 {
		t.Fatalf("cost per success = %.4f, want unavailable for disjoint evidence", withKnownNoCost.Metrics.CostUSDPerSuccessfulTask)
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

func TestProfileResolvedClass_ExplicitUnknownTaskEvidenceDoesNotUseLegacyFrontierExemption(t *testing.T) {
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

	if got := profile.ResolvedClass(); got != ClassFrontier {
		t.Fatalf("legacy zero task evidence ResolvedClass = %s, want frontier", got)
	}
	profile.Samples.TaskSuccessUnknown = 1
	if got := profile.ResolvedClass(); got != ClassBalanced {
		t.Fatalf("explicit unknown task evidence ResolvedClass = %s, want balanced", got)
	}
}

func TestProfileValidate_RejectsTaskSuccessKnownUnknownOverflow(t *testing.T) {
	profile := validProfile()
	profile.SampleSize = int(^uint(0) >> 1)
	profile.Samples.TaskSuccess = profile.SampleSize
	profile.Samples.TaskSuccessUnknown = 1
	err := profile.Validate()
	if err == nil || !strings.Contains(err.Error(), "known and unknown") {
		t.Fatalf("Validate error = %v, want known/unknown sample error", err)
	}
}

func TestProfileJSON_TaskSuccessUnknownOmitemptyPreservesLegacyShape(t *testing.T) {
	profile := validProfile()
	encoded, err := json.Marshal(profile.Normalize())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(encoded), "task_success_unknown") {
		t.Fatalf("legacy JSON included task_success_unknown: %s", encoded)
	}
	var decoded Profile
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Samples.TaskSuccessUnknown != 0 {
		t.Fatalf("decoded unknown samples = %d, want 0", decoded.Samples.TaskSuccessUnknown)
	}
}

func TestProfileNormalize_ReasoningEffortsCanonicalOrder(t *testing.T) {
	profile := validProfile()
	profile.Capabilities.ReasoningEfforts = []string{" HIGH ", "minimal", "Medium", "low"}

	got := profile.Normalize()
	want := []string{"minimal", "low", "medium", "high"}
	if !reflect.DeepEqual(got.Capabilities.ReasoningEfforts, want) {
		t.Fatalf("reasoning efforts = %v, want %v", got.Capabilities.ReasoningEfforts, want)
	}
	if profile.Capabilities.ReasoningEfforts[0] != " HIGH " {
		t.Fatalf("Normalize mutated caller-owned capabilities: %v", profile.Capabilities.ReasoningEfforts)
	}
}

func TestProfileDigest_NilReviewKeepsLegacyV1Digest(t *testing.T) {
	profile := validProfile()
	digest, err := profile.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	const legacyV1Digest = "977304ed3639bc5ec59cf55aea82d9c56bb5446274d9eb109b070ef65f82c2a8"
	if digest != legacyV1Digest {
		t.Fatalf("digest = %s, want legacy v1 digest %s", digest, legacyV1Digest)
	}
}

func TestProfileNormalize_ClonesReviewTokenMaps(t *testing.T) {
	profile := validProfile()
	profile.Review = &ReviewBehavior{
		Profile:                    " Evidence_First ",
		ReasoningMaxTokensBySize:   map[string]int{" Focused ": 111},
		ReasoningMaxTokensByEffort: map[string]int{" LOW ": 222},
	}
	got := profile.Normalize()
	got.Review.ReasoningMaxTokensBySize["focused"] = 333
	got.Review.ReasoningMaxTokensByEffort["low"] = 444
	if profile.Review.Profile != " Evidence_First " ||
		profile.Review.ReasoningMaxTokensBySize[" Focused "] != 111 ||
		profile.Review.ReasoningMaxTokensByEffort[" LOW "] != 222 {
		t.Fatalf("Normalize mutated caller-owned review behavior: %+v", profile.Review)
	}
}

func TestProfileValidate_RejectsInvalidReasoningEfforts(t *testing.T) {
	for _, tt := range []struct {
		name    string
		efforts []string
		want    string
	}{
		{name: "duplicate", efforts: []string{"LOW", " low "}, want: "duplicated"},
		{name: "unsupported", efforts: []string{"ultra"}, want: "unsupported"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			profile := validProfile()
			profile.Capabilities.ReasoningEfforts = tt.efforts
			err := profile.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestProfileValidate_RejectsInvalidReviewBehavior(t *testing.T) {
	for _, tt := range []struct {
		name   string
		review ReviewBehavior
	}{
		{name: "unknown preset", review: ReviewBehavior{Profile: "family-name"}},
		{name: "negative context", review: ReviewBehavior{SupportingContextTokens: -1}},
		{name: "invalid size", review: ReviewBehavior{ReasoningMaxTokensBySize: map[string]int{"tiny": 1}}},
		{name: "zero size budget", review: ReviewBehavior{ReasoningMaxTokensBySize: map[string]int{"focused": 0}}},
		{name: "invalid effort", review: ReviewBehavior{ReasoningMaxTokensByEffort: map[string]int{"extreme": 1}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			profile := validProfile()
			profile.Review = &tt.review
			if err := profile.Validate(); err == nil {
				t.Fatalf("Validate accepted review behavior %+v", tt.review)
			}
		})
	}
}

func TestMemoryStore_ReturnsImmutableReviewBehaviorCopies(t *testing.T) {
	store := NewMemoryStore()
	profile := validProfile()
	profile.Review = &ReviewBehavior{
		Profile:                  ReviewProfileEvidenceFirst,
		ReasoningMaxTokensBySize: map[string]int{"focused": 111},
	}
	if err := store.Put(nil, profile); err != nil {
		t.Fatalf("Put: %v", err)
	}
	fromGet, ok, err := store.Get(nil, profile.ModelID, profile.Version)
	if err != nil || !ok {
		t.Fatalf("Get = %+v, %v, %v", fromGet, ok, err)
	}
	fromGet.Review.ReasoningMaxTokensBySize["focused"] = 999
	again, ok, err := store.Get(nil, profile.ModelID, profile.Version)
	if err != nil || !ok {
		t.Fatalf("Get again = %+v, %v, %v", again, ok, err)
	}
	if again.Review.ReasoningMaxTokensBySize["focused"] != 111 {
		t.Fatalf("Get mutation changed stored profile: %+v", again.Review)
	}

	listed, err := store.List(nil, profile.ModelID)
	if err != nil || len(listed) != 1 {
		t.Fatalf("List = %+v, %v", listed, err)
	}
	listed[0].Review.ReasoningMaxTokensBySize["focused"] = 777
	again, ok, err = store.Get(nil, profile.ModelID, profile.Version)
	if err != nil || !ok {
		t.Fatalf("Get after List = %+v, %v, %v", again, ok, err)
	}
	if again.Review.ReasoningMaxTokensBySize["focused"] != 111 {
		t.Fatalf("List mutation changed stored profile: %+v", again.Review)
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
