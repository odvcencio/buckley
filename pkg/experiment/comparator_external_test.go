package experiment_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/experiment"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
)

func TestCompareRunsMatchesStoreBackedCompare(t *testing.T) {
	data, err := storage.New(filepath.Join(t.TempDir(), "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = data.Close() })
	store := experiment.NewStoreFromStorage(data)
	exp := &experiment.Experiment{
		ID:       "exp-public-store",
		Name:     "public store",
		Task:     experiment.Task{Prompt: "compare"},
		Variants: []experiment.Variant{{ID: "variant-a", Name: "A", ModelID: "model-a"}, {ID: "variant-b", Name: "B", ModelID: "model-b"}},
		Criteria: []experiment.SuccessCriterion{{Name: "tests", Type: experiment.CriterionTestPass, Weight: 1}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	for _, run := range []experiment.Run{
		{ID: "run-a", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted, Metrics: experiment.RunMetrics{TotalCost: 0.02, DurationMs: 1000}},
		{ID: "run-b", ExperimentID: exp.ID, VariantID: "variant-b", Status: experiment.RunCompleted, Metrics: experiment.RunMetrics{TotalCost: 0.01, DurationMs: 2000}},
	} {
		run := run
		if err := store.SaveRun(&run); err != nil {
			t.Fatalf("SaveRun %s: %v", run.ID, err)
		}
	}
	if err := store.ReplaceEvaluations("run-a", []experiment.CriterionEvaluation{{CriterionID: exp.Criteria[0].ID, Passed: true, Score: 1}}); err != nil {
		t.Fatalf("ReplaceEvaluations run-a: %v", err)
	}
	if err := store.ReplaceEvaluations("run-b", []experiment.CriterionEvaluation{{CriterionID: exp.Criteria[0].ID, Passed: false, Score: 0}}); err != nil {
		t.Fatalf("ReplaceEvaluations run-b: %v", err)
	}
	storeReport, err := experiment.NewComparator(store).Compare(exp)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	runs, err := store.ListRuns(exp.ID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	evals, err := store.ListEvaluationsByExperiment(exp.ID)
	if err != nil {
		t.Fatalf("ListEvaluationsByExperiment: %v", err)
	}
	pureReport, err := experiment.CompareRuns(exp, runs, evals)
	if err != nil {
		t.Fatalf("CompareRuns: %v", err)
	}
	if !reflect.DeepEqual(storeReport, pureReport) {
		t.Fatalf("store-backed Compare != CompareRuns\nstore=%#v\npure=%#v", storeReport, pureReport)
	}
}

func TestCompareRunsNilExperiment(t *testing.T) {
	if _, err := experiment.CompareRuns(nil, nil, nil); err == nil || !strings.Contains(err.Error(), "experiment is nil") {
		t.Fatalf("CompareRuns(nil) error = %v, want experiment is nil", err)
	}
}

func TestCompareRunsRejectsAmbiguousInputs(t *testing.T) {
	exp := publicCompareExperiment([]experiment.SuccessCriterion{{ID: 1, Name: "tests", Type: experiment.CriterionTestPass, Weight: 1}})
	tests := []struct {
		name  string
		runs  []experiment.Run
		evals map[string][]experiment.CriterionEvaluation
		want  string
	}{
		{
			name: "empty run id",
			runs: []experiment.Run{{ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted}},
			want: "non-empty run ids",
		},
		{
			name: "duplicate run id",
			runs: []experiment.Run{
				{ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted},
				{ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted},
			},
			want: "duplicate run id",
		},
		{
			name: "wrong experiment id",
			runs: []experiment.Run{{ID: "run-1", ExperimentID: "other-exp", VariantID: "variant-a", Status: experiment.RunCompleted}},
			want: "belongs to experiment",
		},
		{
			name: "evaluation run id mismatch",
			runs: []experiment.Run{{ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted}},
			evals: map[string][]experiment.CriterionEvaluation{
				"run-1": {{RunID: "run-2", CriterionID: 1, Passed: true}},
			},
			want: "mismatched run id",
		},
		{
			name: "duplicate criteria",
			runs: []experiment.Run{{ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted}},
			evals: map[string][]experiment.CriterionEvaluation{
				"run-1": {{RunID: "run-1", CriterionID: 1, Passed: true}},
			},
			want: "duplicate criterion id",
		},
		{
			name: "malformed manifest",
			runs: []experiment.Run{{
				ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted,
				InputManifest: &experiment.RunInputManifest{Version: "unsupported"},
			}},
			want: "input manifest",
		},
		{
			name: "manifest variant mismatch",
			runs: []experiment.Run{{
				ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted,
				InputManifest: validPublicManifest(t, "variant-other"),
			}},
			want: "does not match run variant id",
		},
		{
			name: "legacy missing current variant",
			runs: []experiment.Run{{ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-missing", Status: experiment.RunCompleted}},
			want: "references missing variant id",
		},
		{
			name: "invalid model execution identity",
			runs: []experiment.Run{{
				ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted,
				ModelExecutions: []model.ExecutionIdentity{{ResponseID: "bad\nid"}},
			}},
			want: "model executions",
		},
		{
			name: "blank legacy variant",
			runs: []experiment.Run{{ID: "run-1", ExperimentID: exp.ID, Status: experiment.RunCompleted}},
			want: "references missing variant id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testExp := exp
			if tt.name == "duplicate criteria" {
				testExp = publicCompareExperiment([]experiment.SuccessCriterion{
					{ID: 1, Name: "tests", Type: experiment.CriterionTestPass, Weight: 1},
					{ID: 1, Name: "lint", Type: experiment.CriterionCommand, Weight: 1},
				})
			}
			_, err := experiment.CompareRuns(testExp, tt.runs, tt.evals)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("CompareRuns error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestCompareRunsTreatsWhitespaceBearingIDsAsExactOpaqueValues(t *testing.T) {
	exp := &experiment.Experiment{
		ID:       "exp-public",
		Name:     "public",
		Task:     experiment.Task{Prompt: "compare"},
		Variants: []experiment.Variant{{ID: " variant-a", Name: "A", ModelID: "model-a"}},
		Criteria: []experiment.SuccessCriterion{{ID: 1, Name: "tests", Type: experiment.CriterionTestPass, Weight: 1}},
	}
	report, err := experiment.CompareRuns(exp, []experiment.Run{{
		ID: " run-1", ExperimentID: exp.ID, VariantID: " variant-a", Status: experiment.RunCompleted,
	}}, map[string][]experiment.CriterionEvaluation{
		" run-1": {{RunID: " run-1", CriterionID: 1, Passed: true, Score: 1}},
	})
	if err != nil {
		t.Fatalf("CompareRuns: %v", err)
	}
	if len(report.Rankings) != 1 || report.Rankings[0].RunID != " run-1" || !report.Rankings[0].Winner {
		t.Fatalf("rankings = %#v, want exact whitespace-bearing run id winner", report.Rankings)
	}

	_, err = experiment.CompareRuns(exp, []experiment.Run{{
		ID: " run-1", ExperimentID: exp.ID, VariantID: " variant-a", Status: experiment.RunCompleted,
	}}, map[string][]experiment.CriterionEvaluation{
		" run-1": {{RunID: "run-1", CriterionID: 1, Passed: true, Score: 1}},
	})
	if err == nil || !strings.Contains(err.Error(), "mismatched run id") {
		t.Fatalf("CompareRuns mismatched whitespace ID error = %v, want mismatch", err)
	}
}

func TestCompareRunsRejectsDuplicateNonEmptyVariantIDs(t *testing.T) {
	exp := publicCompareExperiment([]experiment.SuccessCriterion{{ID: 1, Name: "tests", Type: experiment.CriterionTestPass, Weight: 1}})
	exp.Variants = append(exp.Variants, experiment.Variant{ID: "variant-a", Name: "duplicate", ModelID: "model-b"})
	_, err := experiment.CompareRuns(exp, []experiment.Run{{ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted}}, nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate variant id") {
		t.Fatalf("CompareRuns error = %v, want duplicate variant id", err)
	}
}

func TestCompareRunsIgnoresEvaluationsForUnsuppliedRuns(t *testing.T) {
	exp := publicCompareExperiment([]experiment.SuccessCriterion{{ID: 1, Name: "tests", Type: experiment.CriterionTestPass, Weight: 1}})
	report, err := experiment.CompareRuns(exp, []experiment.Run{{
		ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted,
	}}, map[string][]experiment.CriterionEvaluation{
		"run-1": {{RunID: "run-1", CriterionID: 1, Passed: true, Score: 1}},
		"run-extra": {
			{RunID: "wrong-extra-id", CriterionID: 1, Passed: false, Score: 0},
		},
	})
	if err != nil {
		t.Fatalf("CompareRuns: %v", err)
	}
	if !report.Rankings[0].Winner || report.Variants[0].VerificationStatus != "verified" {
		t.Fatalf("report = %#v, want supplied run verified and extra eval ignored", report)
	}
}

func TestCompareRunsUnverifiedEvidenceHasNoWinner(t *testing.T) {
	tests := []struct {
		name     string
		criteria []experiment.SuccessCriterion
		evals    map[string][]experiment.CriterionEvaluation
		want     string
	}{
		{name: "no criteria", want: "unverified: no success criteria configured"},
		{
			name:     "manual only",
			criteria: []experiment.SuccessCriterion{{ID: 1, Name: "human", Type: experiment.CriterionManual, Weight: 1}},
			evals:    map[string][]experiment.CriterionEvaluation{"run-1": {{CriterionID: 1, Passed: true, Score: 1}}},
			want:     "manual review pending",
		},
		{
			name:     "missing automated",
			criteria: []experiment.SuccessCriterion{{ID: 1, Name: "tests", Type: experiment.CriterionTestPass, Weight: 1}},
			want:     "unverified: missing automated evaluation",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp := publicCompareExperiment(tt.criteria)
			report, err := experiment.CompareRuns(exp, []experiment.Run{{ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted}}, tt.evals)
			if err != nil {
				t.Fatalf("CompareRuns: %v", err)
			}
			if len(report.Variants) != 1 || report.Variants[0].VerificationStatus != tt.want {
				t.Fatalf("report = %#v, want status %q", report, tt.want)
			}
			if hasWinner(report) || !strings.HasPrefix(report.Summary, "No verified winner") {
				t.Fatalf("winner/summary = %#v/%q, want no verified winner", report.Rankings, report.Summary)
			}
		})
	}
}

func TestCompareRunsRepeatedVariantDistinctRunIDsDeterministic(t *testing.T) {
	exp := publicCompareExperiment([]experiment.SuccessCriterion{{ID: 1, Name: "tests", Type: experiment.CriterionTestPass, Weight: 1}})
	runs := []experiment.Run{
		{ID: "run-b", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted, Metrics: experiment.RunMetrics{TotalCost: 0.01, DurationMs: 1000}},
		{ID: "run-a", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted, Metrics: experiment.RunMetrics{TotalCost: 0.01, DurationMs: 1000}},
	}
	evals := map[string][]experiment.CriterionEvaluation{
		"run-a": {{CriterionID: 1, Passed: true, Score: 1}},
		"run-b": {{CriterionID: 1, Passed: true, Score: 1}},
	}
	report, err := experiment.CompareRuns(exp, runs, evals)
	if err != nil {
		t.Fatalf("CompareRuns: %v", err)
	}
	reversed, err := experiment.CompareRuns(exp, []experiment.Run{runs[1], runs[0]}, evals)
	if err != nil {
		t.Fatalf("CompareRuns reversed: %v", err)
	}
	for _, got := range [][]experiment.Ranking{report.Rankings, reversed.Rankings} {
		if len(got) != 2 || got[0].RunID != "run-a" || got[1].RunID != "run-b" || !got[0].Winner || got[1].Winner {
			t.Fatalf("rankings = %#v, want run-a winner then run-b", got)
		}
	}
}

func TestCompareRunsFrozenManifestControlsReportIdentityAndCriteria(t *testing.T) {
	exp := publicCompareExperiment([]experiment.SuccessCriterion{{ID: 999, Name: "edited tests", Type: experiment.CriterionTestPass, Weight: 1}})
	exp.Variants[0].Name = "edited"
	exp.Variants[0].ModelID = "edited/model"
	exp.Variants[0].ProviderID = "edited-provider"
	manifest := validPublicManifest(t, "variant-a")
	report, err := experiment.CompareRuns(exp, []experiment.Run{{
		ID: "run-frozen", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted, InputManifest: manifest,
	}}, map[string][]experiment.CriterionEvaluation{"run-frozen": {{CriterionID: 41, Passed: true, Score: 1}}})
	if err != nil {
		t.Fatalf("CompareRuns: %v", err)
	}
	got := report.Variants[0]
	if got.VariantName != "frozen" || got.ModelID != "frozen/model" || got.ProviderID != "frozen-provider" || got.InputDigest != manifest.InputDigest || got.WorkloadDigest != manifest.WorkloadDigest {
		t.Fatalf("variant report used current experiment instead of manifest: %#v", got)
	}
	if !got.Verified || got.VerificationStatus != "verified" || report.Rankings[0].RunID != "run-frozen" || !report.Rankings[0].Winner {
		t.Fatalf("verification/ranking = %#v / %#v, want frozen criterion winner", got, report.Rankings)
	}
}

func TestCompareRunsValidManifestWorksWhenCurrentVariantDeleted(t *testing.T) {
	exp := publicCompareExperiment([]experiment.SuccessCriterion{{ID: 999, Name: "edited tests", Type: experiment.CriterionTestPass, Weight: 1}})
	exp.Variants = nil
	manifest := validPublicManifest(t, "variant-deleted")
	report, err := experiment.CompareRuns(exp, []experiment.Run{{
		ID: "run-frozen", ExperimentID: exp.ID, VariantID: "variant-deleted", Status: experiment.RunCompleted, InputManifest: manifest,
	}}, map[string][]experiment.CriterionEvaluation{"run-frozen": {{CriterionID: 41, Passed: true, Score: 1}}})
	if err != nil {
		t.Fatalf("CompareRuns: %v", err)
	}
	if got := report.Variants[0]; got.ModelID != "frozen/model" || !got.Verified {
		t.Fatalf("report = %#v, want frozen manifest identity despite deleted current variant", got)
	}
}

func TestCompareRunsFailedFreeRunHasNoWinner(t *testing.T) {
	exp := publicCompareExperiment([]experiment.SuccessCriterion{{ID: 1, Name: "tests", Type: experiment.CriterionTestPass, Weight: 1}})
	report, err := experiment.CompareRuns(exp, []experiment.Run{{
		ID: "failed-free", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunFailed, Metrics: experiment.RunMetrics{TotalCost: 0},
	}}, map[string][]experiment.CriterionEvaluation{"failed-free": {{CriterionID: 1, Passed: true, Score: 1}}})
	if err != nil {
		t.Fatalf("CompareRuns: %v", err)
	}
	if len(report.Rankings) != 1 || report.Rankings[0].Rank != 0 || hasWinner(report) || !strings.HasPrefix(report.Summary, "No verified winner") {
		t.Fatalf("report = %#v, want failed run unranked with no winner", report)
	}
}

func TestCompareRunsNilManifestReportsLegacyProvenance(t *testing.T) {
	exp := publicCompareExperiment([]experiment.SuccessCriterion{{ID: 1, Name: "tests", Type: experiment.CriterionTestPass, Weight: 1}})
	report, err := experiment.CompareRuns(exp, []experiment.Run{{
		ID: "run-legacy", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted,
	}}, map[string][]experiment.CriterionEvaluation{"run-legacy": {{CriterionID: 1, Passed: true, Score: 1}}})
	if err != nil {
		t.Fatalf("CompareRuns: %v", err)
	}
	if got := report.Variants[0].ProvenanceStatus; !strings.Contains(got, "legacy run") {
		t.Fatalf("ProvenanceStatus = %q, want legacy label", got)
	}
}

func TestCompareRunsDoesNotMutateInputs(t *testing.T) {
	exp := publicCompareExperiment([]experiment.SuccessCriterion{{ID: 1, Name: "tests", Type: experiment.CriterionTestPass, Weight: 1}})
	runs := []experiment.Run{{ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted, Output: " output "}}
	evals := map[string][]experiment.CriterionEvaluation{"run-1": {{CriterionID: 1, Passed: true, Score: 1, Details: " details "}}}
	expBefore := cloneJSON(t, exp)
	runsBefore := cloneJSON(t, runs)
	evalsBefore := cloneJSON(t, evals)

	if _, err := experiment.CompareRuns(exp, runs, evals); err != nil {
		t.Fatalf("CompareRuns: %v", err)
	}
	if !reflect.DeepEqual(exp, expBefore) || !reflect.DeepEqual(runs, runsBefore) || !reflect.DeepEqual(evals, evalsBefore) {
		t.Fatalf("CompareRuns mutated inputs\nexp=%#v want=%#v\nruns=%#v want=%#v\nevals=%#v want=%#v", exp, expBefore, runs, runsBefore, evals, evalsBefore)
	}
}

func ExampleCompareRuns() {
	exp := publicCompareExperiment([]experiment.SuccessCriterion{{ID: 1, Name: "tests", Type: experiment.CriterionTestPass, Weight: 1}})
	report, _ := experiment.CompareRuns(exp, []experiment.Run{{
		ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted,
	}}, map[string][]experiment.CriterionEvaluation{"run-1": {{CriterionID: 1, Passed: true, Score: 1}}})
	fmt.Println(report.Summary)
	fmt.Printf("%s %s %t\n", report.Rankings[0].RunID, report.Variants[0].VerificationStatus, report.Rankings[0].Winner)
	// Output:
	// Best verified run: A / run-1 (model-a, 100.0% score)
	// run-1 verified true
}

func publicCompareExperiment(criteria []experiment.SuccessCriterion) *experiment.Experiment {
	return &experiment.Experiment{
		ID:       "exp-public",
		Name:     "public",
		Task:     experiment.Task{Prompt: "compare"},
		Variants: []experiment.Variant{{ID: "variant-a", Name: "A", ModelID: "model-a", ProviderID: "provider-a"}},
		Criteria: criteria,
	}
}

func hasWinner(report *experiment.ComparisonReport) bool {
	for _, ranking := range report.Rankings {
		if ranking.Winner {
			return true
		}
	}
	return false
}

func sealManifest(t *testing.T, manifest *experiment.RunInputManifest) {
	t.Helper()
	workloadPayload := struct {
		Task     experiment.RunManifestTask        `json:"task"`
		Criteria []experiment.RunManifestCriterion `json:"criteria"`
	}{
		Task:     manifest.Task,
		Criteria: publicCriteriaWithoutStorageIDs(manifest.Criteria),
	}
	manifest.WorkloadDigest = hashPublicCanonical(t, workloadPayload)
	inputPayload := struct {
		Version        string                            `json:"version"`
		WorkloadDigest string                            `json:"workload_digest"`
		Task           experiment.RunManifestTask        `json:"task"`
		Variant        experiment.RunManifestVariant     `json:"variant"`
		Criteria       []experiment.RunManifestCriterion `json:"criteria"`
	}{
		Version:        manifest.Version,
		WorkloadDigest: manifest.WorkloadDigest,
		Task:           manifest.Task,
		Variant:        publicVariantWithoutStorageID(manifest.Variant),
		Criteria:       publicCriteriaWithoutStorageIDs(manifest.Criteria),
	}
	manifest.InputDigest = hashPublicCanonical(t, inputPayload)
}

func validPublicManifest(t *testing.T, variantID string) *experiment.RunInputManifest {
	t.Helper()
	manifest := &experiment.RunInputManifest{
		Version: "experiment-run-input-v1",
		Variant: experiment.RunManifestVariant{
			ID:                  variantID,
			Name:                "frozen",
			RequestedModelID:    "frozen/model",
			RequestedProviderID: "frozen-provider",
		},
		Criteria: []experiment.RunManifestCriterion{{
			ID: 41, Name: "frozen tests", Type: experiment.CriterionTestPass, Weight: 1,
		}},
	}
	sealManifest(t, manifest)
	return manifest
}

func publicCriteriaWithoutStorageIDs(criteria []experiment.RunManifestCriterion) []experiment.RunManifestCriterion {
	out := make([]experiment.RunManifestCriterion, 0, len(criteria))
	for _, criterion := range criteria {
		criterion.ID = 0
		out = append(out, criterion)
	}
	return out
}

func publicVariantWithoutStorageID(variant experiment.RunManifestVariant) experiment.RunManifestVariant {
	variant.ID = ""
	return variant
}

func hashPublicCanonical(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal canonical value: %v", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func cloneJSON[T any](t *testing.T, value T) T {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return out
}
