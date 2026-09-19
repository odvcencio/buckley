package experiment_test

import (
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/experiment"
	"m31labs.dev/buckley/pkg/storage"
)

func TestCompareRunsRejectsDuplicateCriterionEvaluationsForSuppliedRun(t *testing.T) {
	exp := publicCompareExperiment([]experiment.SuccessCriterion{
		{ID: 1, Name: "tests", Type: experiment.CriterionTestPass, Weight: 1},
	})
	runs := []experiment.Run{{ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted}}

	tests := []struct {
		name  string
		evals []experiment.CriterionEvaluation
	}{
		{
			name: "conflicting failed then passed",
			evals: []experiment.CriterionEvaluation{
				{RunID: "run-1", CriterionID: 1, Passed: false, Score: 0},
				{RunID: "run-1", CriterionID: 1, Passed: true, Score: 1},
			},
		},
		{
			name: "conflicting passed then failed",
			evals: []experiment.CriterionEvaluation{
				{RunID: "run-1", CriterionID: 1, Passed: true, Score: 1},
				{RunID: "run-1", CriterionID: 1, Passed: false, Score: 0},
			},
		},
		{
			name: "identical duplicate",
			evals: []experiment.CriterionEvaluation{
				{RunID: "run-1", CriterionID: 1, Passed: true, Score: 1},
				{RunID: "run-1", CriterionID: 1, Passed: true, Score: 1},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := experiment.CompareRuns(exp, runs, map[string][]experiment.CriterionEvaluation{"run-1": tt.evals})
			if err == nil || !strings.Contains(err.Error(), "duplicate criterion evaluation") {
				t.Fatalf("CompareRuns error = %v, want duplicate criterion evaluation", err)
			}
		})
	}
}

func TestCompareRunsAllowsDistinctCriterionEvaluationsForSuppliedRun(t *testing.T) {
	exp := publicCompareExperiment([]experiment.SuccessCriterion{
		{ID: 1, Name: "tests", Type: experiment.CriterionTestPass, Weight: 1},
		{ID: 2, Name: "lint", Type: experiment.CriterionCommand, Weight: 1},
	})
	report, err := experiment.CompareRuns(exp, []experiment.Run{{
		ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted,
	}}, map[string][]experiment.CriterionEvaluation{
		"run-1": {
			{RunID: "run-1", CriterionID: 1, Passed: true, Score: 1},
			{RunID: "run-1", CriterionID: 2, Passed: true, Score: 1},
		},
	})
	if err != nil {
		t.Fatalf("CompareRuns: %v", err)
	}
	if got := report.Variants[0]; !got.Verified || got.EvaluatedCriteria != 2 || got.AutomatedCriteria != 2 {
		t.Fatalf("variant report = %#v, want both distinct criteria verified", got)
	}
}

func TestCompareRunsStillIgnoresDuplicateEvaluationsForUnsuppliedRuns(t *testing.T) {
	exp := publicCompareExperiment([]experiment.SuccessCriterion{
		{ID: 1, Name: "tests", Type: experiment.CriterionTestPass, Weight: 1},
	})
	report, err := experiment.CompareRuns(exp, []experiment.Run{{
		ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted,
	}}, map[string][]experiment.CriterionEvaluation{
		"run-1": {{RunID: "run-1", CriterionID: 1, Passed: true, Score: 1}},
		"orphan-run": {
			{RunID: "orphan-run", CriterionID: 1, Passed: false, Score: 0},
			{RunID: "orphan-run", CriterionID: 1, Passed: true, Score: 1},
		},
	})
	if err != nil {
		t.Fatalf("CompareRuns: %v", err)
	}
	if !report.Rankings[0].Winner || report.Variants[0].VerificationStatus != "verified" {
		t.Fatalf("report = %#v, want supplied run verified and orphan duplicates ignored", report)
	}
}

func TestComparatorCompareRejectsStoreBackedDuplicateCriterionEvaluations(t *testing.T) {
	data, err := storage.New(filepath.Join(t.TempDir(), "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = data.Close() })
	store := experiment.NewStoreFromStorage(data)
	exp := &experiment.Experiment{
		ID:       "exp-duplicate-evals",
		Name:     "duplicate evals",
		Task:     experiment.Task{Prompt: "compare"},
		Variants: []experiment.Variant{{ID: "variant-a", Name: "A", ModelID: "model-a"}},
		Criteria: []experiment.SuccessCriterion{{Name: "tests", Type: experiment.CriterionTestPass, Weight: 1}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	if err := store.SaveRun(&experiment.Run{ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: experiment.RunCompleted}); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	if err := store.ReplaceEvaluations("run-1", []experiment.CriterionEvaluation{
		{CriterionID: exp.Criteria[0].ID, Passed: false, Score: 0},
		{CriterionID: exp.Criteria[0].ID, Passed: true, Score: 1},
	}); err != nil {
		t.Fatalf("ReplaceEvaluations: %v", err)
	}

	_, err = experiment.NewComparator(store).Compare(exp)
	if err == nil || !strings.Contains(err.Error(), "duplicate criterion evaluation") {
		t.Fatalf("Compare error = %v, want duplicate criterion evaluation", err)
	}
}

func TestCompareRunsReportsNonCompletedPassingCriteriaAsUnverified(t *testing.T) {
	exp := publicCompareExperiment([]experiment.SuccessCriterion{
		{ID: 1, Name: "tests", Type: experiment.CriterionTestPass, Weight: 1},
	})
	tests := []struct {
		name       string
		status     experiment.RunStatus
		wantStatus string
		wantOK     bool
	}{
		{name: "completed", status: experiment.RunCompleted, wantStatus: "verified", wantOK: true},
		{name: "failed", status: experiment.RunFailed, wantStatus: "unverified: run not completed (criteria passed)"},
		{name: "cancelled", status: experiment.RunCancelled, wantStatus: "unverified: run not completed (criteria passed)"},
		{name: "running", status: experiment.RunRunning, wantStatus: "unverified: run not completed (criteria passed)"},
		{name: "pending", status: experiment.RunPending, wantStatus: "unverified: run not completed (criteria passed)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			report, err := experiment.CompareRuns(exp, []experiment.Run{{
				ID: "run-1", ExperimentID: exp.ID, VariantID: "variant-a", Status: tt.status,
			}}, map[string][]experiment.CriterionEvaluation{
				"run-1": {{RunID: "run-1", CriterionID: 1, Passed: true, Score: 1}},
			})
			if err != nil {
				t.Fatalf("CompareRuns: %v", err)
			}
			got := report.Variants[0]
			if got.VerificationStatus != tt.wantStatus {
				t.Fatalf("VerificationStatus = %q, want %q", got.VerificationStatus, tt.wantStatus)
			}
			if got.Verified != tt.wantOK || got.RankEligible != tt.wantOK {
				t.Fatalf("Verified/RankEligible = %v/%v, want %v/%v", got.Verified, got.RankEligible, tt.wantOK, tt.wantOK)
			}
			if got.CriteriaScore != 1 || len(got.CriteriaPassed) != 1 || got.CriteriaPassed[0] != "tests" {
				t.Fatalf("criteria evidence = score %v passed %v, want passing evidence retained", got.CriteriaScore, got.CriteriaPassed)
			}
		})
	}
}
