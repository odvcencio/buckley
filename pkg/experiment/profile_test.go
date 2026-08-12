package experiment

import (
	"math"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/modelprofile"
)

func TestModelCalibrations_GroupsTerminalRunsWithoutContent(t *testing.T) {
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	later := now.Add(time.Minute)
	exp := &Experiment{
		Variants: []Variant{
			{ID: "v1", ModelID: "cheap/model", ProviderID: "openrouter"},
			{ID: "v2", ModelID: "frontier/model", ProviderID: "openrouter"},
		},
		Criteria: []SuccessCriterion{{ID: 1, Name: "tests", Type: CriterionTestPass}},
	}
	runs := []Run{
		{ID: "run-1", VariantID: "v1", Status: RunCompleted, Output: "secret output", Files: []string{"secret.go"}, StartedAt: now, CompletedAt: &later, Metrics: RunMetrics{DurationMs: 100, PromptTokens: 80, CompletionTokens: 20, TotalCost: 0.01, ToolCalls: 2, ToolSuccesses: 2}},
		{ID: "run-2", VariantID: "v1", Status: RunFailed, StartedAt: now, Metrics: RunMetrics{DurationMs: 200, PromptTokens: 100, CompletionTokens: 10, TotalCost: 0.02, ToolCalls: 1, ToolFailures: 1}},
		{ID: "run-3", VariantID: "v2", Status: RunRunning, StartedAt: now},
	}
	evaluations := map[string][]CriterionEvaluation{"run-1": {{RunID: "run-1", CriterionID: 1, Passed: true}}}
	got := ModelCalibrations(exp, runs, evaluations)
	if len(got) != 1 || got[0].ModelID != "cheap/model" || len(got[0].Observations) != 2 || !got[0].MeasuredAt.Equal(later) {
		t.Fatalf("calibrations = %+v", got)
	}
	if !got[0].Observations[0].Succeeded || got[0].Observations[1].Succeeded {
		t.Fatalf("success observations = %+v", got[0].Observations)
	}
	if got[0].Observations[0].ToolSucceeded == nil || !*got[0].Observations[0].ToolSucceeded || got[0].Observations[1].ToolSucceeded == nil || *got[0].Observations[1].ToolSucceeded {
		t.Fatalf("tool observations = %+v", got[0].Observations)
	}
}

func TestCalibrateModelProfile_TracksSuccessEfficiencyAndProvider(t *testing.T) {
	now := time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)
	toolPass, toolFail := true, false
	profile, err := CalibrateModelProfile(modelprofile.Profile{}, ModelCalibration{
		ModelID:    "cheap/model",
		ProviderID: "openrouter",
		MeasuredAt: now,
		Observations: []modelprofile.Observation{
			{Succeeded: true, ToolSucceeded: &toolPass, LatencyMS: 100, PromptTokens: 80, CompletionTokens: 20, TokensObserved: true, CostUSD: 0.01, CostObserved: true},
			{Succeeded: false, ToolSucceeded: &toolFail, LatencyMS: 300, PromptTokens: 160, CompletionTokens: 40, TokensObserved: true, CostUSD: 0.03, CostObserved: true},
		},
	}, "experiment-exp-1", "")
	if err != nil {
		t.Fatalf("CalibrateModelProfile: %v", err)
	}
	if profile.ModelID != "cheap/model" || profile.Provider != "openrouter" || profile.Version != "experiment-exp-1" || !profile.MeasuredAt.Equal(now) {
		t.Fatalf("identity = %+v", profile)
	}
	if profile.SampleSize != 2 || profile.Samples.TaskSuccess != 2 || profile.Metrics.TaskSuccessRate != 0.5 {
		t.Fatalf("task evidence = %+v", profile)
	}
	if profile.Samples.ToolReliability != 2 || profile.Metrics.ToolReliability != 0.5 || !profile.Capabilities.ToolCalls {
		t.Fatalf("tool evidence = %+v", profile)
	}
	if profile.Metrics.AverageTaskLatencyMS != 200 || profile.Metrics.AverageTokensPerTask != 150 || math.Abs(profile.Metrics.AverageCostUSDPerTask-0.02) > 1e-9 || math.Abs(profile.Metrics.CostUSDPerSuccessfulTask-0.04) > 1e-9 {
		t.Fatalf("efficiency evidence = %+v", profile.Metrics)
	}
	if math.Abs(profile.Confidence-(2.0/12.0)) > 1e-9 || profile.ResolvedClass() != modelprofile.ClassWeak {
		t.Fatalf("confidence/class = %.3f/%s", profile.Confidence, profile.ResolvedClass())
	}
}

func TestCalibrateModelProfile_AutoClassClearsPriorOverrideAndPreservesConfidence(t *testing.T) {
	base := modelprofile.Profile{
		SchemaVersion: modelprofile.SchemaVersion,
		ModelID:       "model",
		Version:       "prior",
		Class:         modelprofile.ClassFrontier,
		SampleSize:    40,
		Confidence:    0.95,
		Metrics: modelprofile.Metrics{
			ToolReliability:             0.95,
			StructuredOutputReliability: 0.95,
		},
	}
	profile, err := CalibrateModelProfile(base, ModelCalibration{
		ModelID: "model", MeasuredAt: time.Now(),
		Observations: []modelprofile.Observation{{Succeeded: false}},
	}, "next", "")
	if err != nil {
		t.Fatalf("CalibrateModelProfile: %v", err)
	}
	if profile.Class != "" || profile.Confidence != 0.95 || profile.Samples.TaskSuccess != 1 || profile.Metrics.TaskSuccessRate != 0 {
		t.Fatalf("profile = %+v", profile)
	}
}
