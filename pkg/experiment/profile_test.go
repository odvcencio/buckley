package experiment

import (
	"math"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
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

func TestModelCalibrationsUsesFrozenManifestIdentityAndCriteria(t *testing.T) {
	now := time.Date(2026, 9, 4, 17, 0, 0, 0, time.UTC)
	exp := &Experiment{
		ID:   "exp-frozen-profile",
		Name: "profile",
		Task: Task{Prompt: "original task", Context: map[string]string{"public": "ok"}},
		Variants: []Variant{{
			ID:         "variant-original",
			Name:       "original",
			ModelID:    "original/model-release",
			ProviderID: "original-provider",
		}},
		Criteria: []SuccessCriterion{{ID: 41, Name: "original tests", Type: CriterionTestPass, Target: "go test ./original", Weight: 1}},
	}
	manifest, err := buildRunInputManifest(exp, exp.Variants[0], time.Minute)
	if err != nil {
		t.Fatalf("buildRunInputManifest: %v", err)
	}
	run := Run{
		ID:            "run-original",
		VariantID:     "variant-original",
		Status:        RunCompleted,
		StartedAt:     now,
		Metrics:       RunMetrics{DurationMs: 123, PromptTokens: 10, CompletionTokens: 5, TotalCost: 0.02},
		InputManifest: manifest,
	}
	evaluations := map[string][]CriterionEvaluation{
		"run-original": {{RunID: "run-original", CriterionID: 41, Passed: true, Score: 1}},
	}

	mutated := &Experiment{
		ID:   exp.ID,
		Name: exp.Name,
		Task: Task{Prompt: "mutated task", Context: map[string]string{"secret": "must-not-leak"}},
		Variants: []Variant{{
			ID:         "variant-original",
			Name:       "mutated",
			ModelID:    "mutated/model-release",
			ProviderID: "mutated-provider",
		}},
		Criteria: []SuccessCriterion{{ID: 999, Name: "mutated tests", Type: CriterionTestPass, Target: "go test ./mutated", Weight: 1}},
	}

	got := ModelCalibrations(mutated, []Run{run}, evaluations)
	if len(got) != 1 {
		t.Fatalf("calibrations = %+v, want one", got)
	}
	if got[0].ModelID != "original/model-release" || got[0].ProviderID != "original-provider" {
		t.Fatalf("calibration identity = %+v, want original manifest model/provider", got[0])
	}
	if len(got[0].Observations) != 1 || got[0].Observations[0].VerificationPassed == nil || !*got[0].Observations[0].VerificationPassed || !got[0].Observations[0].Succeeded {
		t.Fatalf("calibration evidence = %+v, want original criterion pass", got[0].Observations)
	}
	if got[0].ModelID == "mutated/model-release" || got[0].ProviderID == "mutated-provider" {
		t.Fatalf("calibration leaked mutated current config: %+v", got[0])
	}
}

func TestModelCalibrationsUsesManifestWhenCurrentVariantDeleted(t *testing.T) {
	exp := &Experiment{
		ID:       "exp-deleted-profile",
		Task:     Task{Prompt: "original task"},
		Variants: []Variant{{ID: "variant-original", Name: "original", ModelID: "original/model", ProviderID: "original-provider"}},
		Criteria: []SuccessCriterion{{ID: 7, Name: "original tests", Type: CriterionTestPass, Target: "go test", Weight: 1}},
	}
	manifest, err := buildRunInputManifest(exp, exp.Variants[0], time.Minute)
	if err != nil {
		t.Fatalf("buildRunInputManifest: %v", err)
	}
	mutated := &Experiment{ID: exp.ID, Task: Task{Prompt: "mutated"}, Variants: nil, Criteria: nil}
	got := ModelCalibrations(mutated, []Run{{
		ID:            "run-original",
		VariantID:     "variant-original",
		Status:        RunCompleted,
		Metrics:       RunMetrics{PromptTokens: 1, CompletionTokens: 1},
		InputManifest: manifest,
	}}, map[string][]CriterionEvaluation{"run-original": {{RunID: "run-original", CriterionID: 7, Passed: true, Score: 1}}})
	if len(got) != 1 || got[0].ModelID != "original/model" || got[0].ProviderID != "original-provider" {
		t.Fatalf("calibrations = %+v, want deleted current variant recovered from manifest", got)
	}
	if got[0].Observations[0].VerificationPassed == nil || !*got[0].Observations[0].VerificationPassed {
		t.Fatalf("verification = %+v, want manifest criterion pass", got[0].Observations[0])
	}
}

func TestModelCalibrationsSkipsMismatchedExecutionIdentity(t *testing.T) {
	exp := &Experiment{
		Variants: []Variant{{ID: "v1", ModelID: "requested/model", ProviderID: "provider-a"}},
		Criteria: []SuccessCriterion{{ID: 1, Name: "tests", Type: CriterionTestPass}},
	}
	runs := []Run{
		{
			ID:        "run-good",
			VariantID: "v1",
			Status:    RunCompleted,
			ModelExecutions: []model.ExecutionIdentity{{
				RequestedModel: "requested/model",
				SelectedModel:  "requested/model",
				ProviderID:     "provider-a",
				ResponseModel:  "requested/model",
				ResponseID:     "resp-good",
			}},
		},
		{
			ID:        "run-mismatch",
			VariantID: "v1",
			Status:    RunCompleted,
			ModelExecutions: []model.ExecutionIdentity{{
				RequestedModel: "requested/model",
				SelectedModel:  "different/model",
				ProviderID:     "provider-a",
				ResponseID:     "resp-mismatch",
			}},
		},
	}
	evaluations := map[string][]CriterionEvaluation{
		"run-good":     {{RunID: "run-good", CriterionID: 1, Passed: true}},
		"run-mismatch": {{RunID: "run-mismatch", CriterionID: 1, Passed: true}},
	}
	got := ModelCalibrations(exp, runs, evaluations)
	if len(got) != 1 || len(got[0].Observations) != 1 {
		t.Fatalf("calibrations = %+v, want one attributable observation", got)
	}
	if len(got[0].AttributionCaveats) != 1 || !strings.Contains(got[0].AttributionCaveats[0], "selected model") {
		t.Fatalf("attribution caveats = %+v, want mismatched selected model caveat", got[0].AttributionCaveats)
	}
}

func TestModelCalibrationsDistinguishesLegacyUnknownFromNewMissingExecutionIdentity(t *testing.T) {
	exp := &Experiment{
		Variants: []Variant{{ID: "v1", ModelID: "requested/model"}},
		Criteria: []SuccessCriterion{{ID: 1, Name: "tests", Type: CriterionTestPass}},
	}
	evaluations := map[string][]CriterionEvaluation{
		"run-legacy":       {{RunID: "run-legacy", CriterionID: 1, Passed: true}},
		"run-new-empty":    {{RunID: "run-new-empty", CriterionID: 1, Passed: true}},
		"run-new-unknown":  {{RunID: "run-new-unknown", CriterionID: 1, Passed: true}},
		"run-new-conflict": {{RunID: "run-new-conflict", CriterionID: 1, Passed: true}},
	}
	got := ModelCalibrations(exp, []Run{
		{ID: "run-legacy", VariantID: "v1", Status: RunCompleted, ModelExecutions: nil},
		{ID: "run-new-empty", VariantID: "v1", Status: RunCompleted, ModelExecutions: []model.ExecutionIdentity{}},
		{ID: "run-new-unknown", VariantID: "v1", Status: RunCompleted, ModelExecutions: []model.ExecutionIdentity{{}}},
		{ID: "run-new-conflict", VariantID: "v1", Status: RunCompleted, ModelExecutions: []model.ExecutionIdentity{{Conflicted: true}}},
	}, evaluations)
	if len(got) != 1 || len(got[0].Observations) != 1 {
		t.Fatalf("calibrations = %+v, want only legacy nil identity attributed", got)
	}
	if len(got[0].AttributionCaveats) != 3 {
		t.Fatalf("attribution caveats = %+v, want new missing/unknown/conflicted skips", got[0].AttributionCaveats)
	}
}

func TestModelCalibrationsReturnsCaveatsWhenAllExecutionIdentitiesUnattributable(t *testing.T) {
	exp := &Experiment{
		Variants: []Variant{{ID: "v1", ModelID: "requested/model", ProviderID: "provider-a"}},
		Criteria: []SuccessCriterion{{ID: 1, Name: "tests", Type: CriterionTestPass}},
	}
	got := ModelCalibrations(exp, []Run{
		{
			ID:              "run-response-id-only",
			VariantID:       "v1",
			Status:          RunCompleted,
			ModelExecutions: []model.ExecutionIdentity{{ResponseID: "resp-only"}},
		},
		{
			ID:        "run-missing-response-model",
			VariantID: "v1",
			Status:    RunCompleted,
			ModelExecutions: []model.ExecutionIdentity{{
				RequestedModel: "requested/model",
				SelectedModel:  "requested/model",
				ProviderID:     "provider-a",
				ResponseID:     "resp-no-model",
			}},
		},
	}, map[string][]CriterionEvaluation{
		"run-response-id-only":       {{RunID: "run-response-id-only", CriterionID: 1, Passed: true}},
		"run-missing-response-model": {{RunID: "run-missing-response-model", CriterionID: 1, Passed: true}},
	})
	if len(got) != 1 || len(got[0].Observations) != 0 || len(got[0].AttributionCaveats) != 2 {
		t.Fatalf("calibrations = %+v, want visible zero-observation caveats", got)
	}
	if _, err := CalibrateModelProfile(modelprofile.Profile{}, got[0], "v1", ""); err == nil || !strings.Contains(err.Error(), "no attributable terminal observations") {
		t.Fatalf("CalibrateModelProfile error = %v, want attribution caveat", err)
	}
}

func TestModelCalibrationsSeparatesBlankRequestedProviderByObservedProvider(t *testing.T) {
	exp := &Experiment{
		Variants: []Variant{{ID: "v1", ModelID: "requested/model"}},
		Criteria: []SuccessCriterion{{ID: 1, Name: "tests", Type: CriterionTestPass}},
	}
	got := ModelCalibrations(exp, []Run{
		{
			ID:        "run-provider-a",
			VariantID: "v1",
			Status:    RunCompleted,
			ModelExecutions: []model.ExecutionIdentity{{
				RequestedModel: "requested/model",
				SelectedModel:  "requested/model",
				ProviderID:     "provider-a",
				ResponseModel:  "requested/model",
				ResponseID:     "resp-a",
			}},
		},
		{
			ID:        "run-provider-b",
			VariantID: "v1",
			Status:    RunCompleted,
			ModelExecutions: []model.ExecutionIdentity{{
				RequestedModel: "requested/model",
				SelectedModel:  "requested/model",
				ProviderID:     "provider-b",
				ResponseModel:  "requested/model",
				ResponseID:     "resp-b",
			}},
		},
	}, map[string][]CriterionEvaluation{
		"run-provider-a": {{RunID: "run-provider-a", CriterionID: 1, Passed: true}},
		"run-provider-b": {{RunID: "run-provider-b", CriterionID: 1, Passed: true}},
	})
	if len(got) != 2 {
		t.Fatalf("calibrations = %+v, want separate observed providers", got)
	}
	if got[0].ProviderID != "provider-a" || got[1].ProviderID != "provider-b" || got[0].ModelID != "requested/model" || got[1].ModelID != "requested/model" {
		t.Fatalf("calibration identities = %+v, want model split by observed provider", got)
	}
}

func TestModelCalibrations_NoCriteriaDoesNotInflateTaskSuccess(t *testing.T) {
	exp := &Experiment{
		Variants: []Variant{{ID: "v1", ModelID: "model"}},
	}
	got := ModelCalibrations(exp, []Run{{
		ID:        "run-1",
		VariantID: "v1",
		Status:    RunCompleted,
		Metrics:   RunMetrics{DurationMs: 100, PromptTokens: 10, CompletionTokens: 5, TotalCost: 0.01},
	}}, nil)
	if len(got) != 1 || len(got[0].Observations) != 1 {
		t.Fatalf("calibrations = %+v", got)
	}
	observation := got[0].Observations[0]
	if observation.TaskSuccessObserved == nil || *observation.TaskSuccessObserved || observation.Succeeded || observation.VerificationPassed != nil {
		t.Fatalf("observation = %+v, want unknown task success with no verification", observation)
	}
	if observation.LatencyMS != 100 || !observation.TokensObserved || !observation.CostObserved {
		t.Fatalf("operational metrics not preserved: %+v", observation)
	}
}

func TestModelCalibrations_MissingAutomatedEvalIsUnknownNotFailure(t *testing.T) {
	exp := &Experiment{
		Variants: []Variant{{ID: "v1", ModelID: "model"}},
		Criteria: []SuccessCriterion{{ID: 1, Name: "tests", Type: CriterionTestPass}},
	}
	got := ModelCalibrations(exp, []Run{{ID: "run-1", VariantID: "v1", Status: RunCompleted}}, nil)
	observation := got[0].Observations[0]
	if observation.TaskSuccessObserved == nil || *observation.TaskSuccessObserved || observation.Succeeded || observation.VerificationPassed != nil {
		t.Fatalf("observation = %+v, want unknown rather than failed", observation)
	}
}

func TestModelCalibrations_ManualCriteriaIsUnknownEvenWithEvalRow(t *testing.T) {
	exp := &Experiment{
		Variants: []Variant{{ID: "v1", ModelID: "model"}},
		Criteria: []SuccessCriterion{{ID: 1, Name: "manual", Type: CriterionManual}},
	}
	got := ModelCalibrations(exp, []Run{{ID: "run-1", VariantID: "v1", Status: RunCompleted}}, map[string][]CriterionEvaluation{
		"run-1": {{RunID: "run-1", CriterionID: 1, Passed: true}},
	})
	observation := got[0].Observations[0]
	if observation.TaskSuccessObserved == nil || *observation.TaskSuccessObserved || observation.Succeeded || observation.VerificationPassed != nil {
		t.Fatalf("observation = %+v, want manual evidence pending/unknown", observation)
	}
}

func TestModelCalibrations_AutomatedFailIsKnownFailure(t *testing.T) {
	exp := &Experiment{
		Variants: []Variant{{ID: "v1", ModelID: "model"}},
		Criteria: []SuccessCriterion{{ID: 1, Name: "tests", Type: CriterionTestPass}},
	}
	got := ModelCalibrations(exp, []Run{{ID: "run-1", VariantID: "v1", Status: RunCompleted}}, map[string][]CriterionEvaluation{
		"run-1": {{RunID: "run-1", CriterionID: 1, Passed: false}},
	})
	observation := got[0].Observations[0]
	if observation.TaskSuccessObserved == nil || !*observation.TaskSuccessObserved || observation.Succeeded {
		t.Fatalf("observation = %+v, want known task failure", observation)
	}
	if observation.VerificationPassed == nil || *observation.VerificationPassed {
		t.Fatalf("verification = %+v, want conclusive failed verification", observation.VerificationPassed)
	}
}

func TestModelCalibrations_AutomatedFailWithManualPendingIsTaskFailureNotVerificationComplete(t *testing.T) {
	exp := &Experiment{
		Variants: []Variant{{ID: "v1", ModelID: "model"}},
		Criteria: []SuccessCriterion{
			{ID: 1, Name: "tests", Type: CriterionTestPass},
			{ID: 2, Name: "manual", Type: CriterionManual},
		},
	}
	got := ModelCalibrations(exp, []Run{{ID: "run-1", VariantID: "v1", Status: RunCompleted}}, map[string][]CriterionEvaluation{
		"run-1": {{RunID: "run-1", CriterionID: 1, Passed: false}},
	})
	observation := got[0].Observations[0]
	if observation.TaskSuccessObserved == nil || !*observation.TaskSuccessObserved || observation.Succeeded {
		t.Fatalf("observation = %+v, want known task failure", observation)
	}
	if observation.VerificationPassed != nil {
		t.Fatalf("verification = %+v, want incomplete because manual review is pending", observation.VerificationPassed)
	}
}

func TestModelCalibrations_FailedExecutionKnownFailureCancelledUnknown(t *testing.T) {
	exp := &Experiment{
		Variants: []Variant{{ID: "v1", ModelID: "model"}},
		Criteria: []SuccessCriterion{{ID: 1, Name: "tests", Type: CriterionTestPass}},
	}
	got := ModelCalibrations(exp, []Run{
		{ID: "run-failed", VariantID: "v1", Status: RunFailed, Metrics: RunMetrics{DurationMs: 10, TotalCost: 0.01}},
		{ID: "run-cancelled", VariantID: "v1", Status: RunCancelled, Metrics: RunMetrics{DurationMs: 20, TotalCost: 0.02}},
	}, nil)
	if len(got) != 1 || len(got[0].Observations) != 2 {
		t.Fatalf("calibrations = %+v", got)
	}
	failed := got[0].Observations[0]
	if failed.TaskSuccessObserved == nil || !*failed.TaskSuccessObserved || failed.Succeeded || failed.VerificationPassed != nil {
		t.Fatalf("failed observation = %+v, want known execution failure without verification", failed)
	}
	cancelled := got[0].Observations[1]
	if cancelled.TaskSuccessObserved == nil || *cancelled.TaskSuccessObserved || cancelled.Succeeded || cancelled.VerificationPassed != nil {
		t.Fatalf("cancelled observation = %+v, want unknown task outcome", cancelled)
	}
}

func TestModelCalibrations_TerminalExecutionKeepsIndependentVerificationEvidence(t *testing.T) {
	exp := &Experiment{
		Variants: []Variant{{ID: "v1", ModelID: "model"}},
		Criteria: []SuccessCriterion{{ID: 1, Name: "tests", Type: CriterionTestPass}},
	}
	got := ModelCalibrations(exp, []Run{
		{ID: "run-failed-pass", VariantID: "v1", Status: RunFailed},
		{ID: "run-failed-fail", VariantID: "v1", Status: RunFailed},
		{ID: "run-cancelled-pass", VariantID: "v1", Status: RunCancelled},
	}, map[string][]CriterionEvaluation{
		"run-failed-pass":    {{RunID: "run-failed-pass", CriterionID: 1, Passed: true}},
		"run-failed-fail":    {{RunID: "run-failed-fail", CriterionID: 1, Passed: false}},
		"run-cancelled-pass": {{RunID: "run-cancelled-pass", CriterionID: 1, Passed: true}},
	})
	if len(got) != 1 || len(got[0].Observations) != 3 {
		t.Fatalf("calibrations = %+v", got)
	}
	failedPass := got[0].Observations[0]
	if failedPass.TaskSuccessObserved == nil || !*failedPass.TaskSuccessObserved || failedPass.Succeeded || failedPass.VerificationPassed == nil || !*failedPass.VerificationPassed {
		t.Fatalf("failed pass observation = %+v, want task failure with independent verification pass", failedPass)
	}
	failedFail := got[0].Observations[1]
	if failedFail.TaskSuccessObserved == nil || !*failedFail.TaskSuccessObserved || failedFail.Succeeded || failedFail.VerificationPassed == nil || *failedFail.VerificationPassed {
		t.Fatalf("failed fail observation = %+v, want task failure with independent verification fail", failedFail)
	}
	cancelledPass := got[0].Observations[2]
	if cancelledPass.TaskSuccessObserved == nil || *cancelledPass.TaskSuccessObserved || cancelledPass.Succeeded || cancelledPass.VerificationPassed == nil || !*cancelledPass.VerificationPassed {
		t.Fatalf("cancelled pass observation = %+v, want unknown task outcome with independent verification pass", cancelledPass)
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

func TestCalibrateModelProfileRejectsProviderRelabelOfBaseProfile(t *testing.T) {
	base := modelprofile.Profile{
		SchemaVersion: modelprofile.SchemaVersion,
		ModelID:       "model",
		Version:       "prior",
		Provider:      "provider-a",
	}
	known := true
	_, err := CalibrateModelProfile(base, ModelCalibration{
		ModelID:    "model",
		ProviderID: "provider-b",
		MeasuredAt: time.Now(),
		Observations: []modelprofile.Observation{{
			Succeeded:           true,
			TaskSuccessObserved: &known,
		}},
	}, "next", "")
	if err == nil || !strings.Contains(err.Error(), "does not match base profile provider") {
		t.Fatalf("CalibrateModelProfile error = %v, want provider mismatch", err)
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
