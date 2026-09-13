package experiment

import (
	"math"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/modelprofile"
)

func TestModelCalibrationLegacyCostEvidence(t *testing.T) {
	exp := &Experiment{
		Variants: []Variant{{ID: "v", ModelID: "model", ProviderID: "provider"}},
		Criteria: []SuccessCriterion{{ID: 1, Type: CriterionTestPass}},
	}
	for _, tc := range []struct {
		name     string
		status   RunStatus
		cost     float64
		observed bool
	}{
		{"completed positive", RunCompleted, 0.03, true},
		{"completed ambiguous zero", RunCompleted, 0, false},
		{"failed unpriced", RunFailed, 0, false},
		{"failed partial subtotal", RunFailed, 0.01, false},
		{"cancelled partial subtotal", RunCancelled, 0.01, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calibrations := ModelCalibrations(exp, []Run{{ID: "r", VariantID: "v", Status: tc.status, StartedAt: time.Now(), Metrics: RunMetrics{TotalCost: tc.cost, PromptTokens: 100, CompletionTokens: 10, DurationMs: 200}}}, map[string][]CriterionEvaluation{"r": {{CriterionID: 1, Passed: tc.status == RunCompleted}}})
			if len(calibrations) != 1 || len(calibrations[0].Observations) != 1 {
				t.Fatalf("calibrations=%+v", calibrations)
			}
			o := calibrations[0].Observations[0]
			if o.CostObserved != tc.observed || o.CostUSD != tc.cost {
				t.Errorf("cost observed=%v value=%v; want observed=%v value=%v", o.CostObserved, o.CostUSD, tc.observed, tc.cost)
			}
			if o.LatencyMS != 200 || o.PromptTokens != 100 || o.CompletionTokens != 10 || !o.TokensObserved {
				t.Fatalf("lost non-cost evidence: %+v", o)
			}
			profile, err := CalibrateModelProfile(modelprofile.Profile{}, calibrations[0], "legacy-cost-test", "")
			if err != nil {
				t.Fatal(err)
			}
			wantSamples := 0
			if tc.observed {
				wantSamples = 1
			}
			if profile.SampleSize != 1 || profile.Samples.Cost != wantSamples {
				t.Fatalf("sampleSize=%d costSamples=%d, want 1/%d", profile.SampleSize, profile.Samples.Cost, wantSamples)
			}
		})
	}
}

func TestModelCalibrationLegacyUnknownCostsDoNotDiluteMean(t *testing.T) {
	exp := &Experiment{
		Variants: []Variant{{ID: "v", ModelID: "model", ProviderID: "provider"}},
		Criteria: []SuccessCriterion{{ID: 1, Type: CriterionTestPass}},
	}
	calibrations := ModelCalibrations(exp, []Run{
		{ID: "priced", VariantID: "v", Status: RunCompleted, Metrics: RunMetrics{TotalCost: 0.03}},
		{ID: "unpriced", VariantID: "v", Status: RunFailed},
		{ID: "partial", VariantID: "v", Status: RunFailed, Metrics: RunMetrics{TotalCost: 0.01}},
		{ID: "ambiguous", VariantID: "v", Status: RunCompleted},
	}, map[string][]CriterionEvaluation{"priced": {{CriterionID: 1, Passed: true}}, "ambiguous": {{CriterionID: 1, Passed: true}}})
	if len(calibrations) != 1 {
		t.Fatalf("calibrations=%+v", calibrations)
	}
	profile, err := CalibrateModelProfile(modelprofile.Profile{}, calibrations[0], "legacy-mixed-test", "")
	if err != nil {
		t.Fatal(err)
	}
	if profile.SampleSize != 4 || profile.Samples.Cost != 1 || math.Abs(profile.Metrics.AverageCostUSDPerTask-0.03) > 1e-9 {
		t.Fatalf("unknown costs diluted mean: sampleSize=%d samples=%+v metrics=%+v", profile.SampleSize, profile.Samples, profile.Metrics)
	}
}
