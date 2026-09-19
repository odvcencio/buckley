package experiment

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactedCriterionTargetPresenceIsComparisonOnly(t *testing.T) {
	for _, kind := range []CriterionType{CriterionContains, CriterionFileExists} {
		criterion := SuccessCriterion{ID: 1, Name: "redacted", Type: kind, Weight: 1, targetKnownNonempty: true}
		got := assessCriteria([]SuccessCriterion{criterion}, []CriterionEvaluation{{CriterionID: 1, Passed: true}})
		if !got.Verified || !got.RankEligible || got.Score != 1 {
			t.Errorf("known nonempty redacted target lost retained evaluation: %+v", got)
		}
		if passed, _ := evaluateCriterion(context.Background(), t.TempDir(), "", criterion); passed {
			t.Error("presence metadata substituted for an actual evaluation target")
		}
		raw, err := json.Marshal(criterion)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "targetKnownNonempty") {
			t.Fatal("internal projection metadata leaked into configuration")
		}
		var fromJSON SuccessCriterion
		if err := json.Unmarshal([]byte(`{"ID":1,"Type":"contains","targetKnownNonempty":true}`), &fromJSON); err != nil {
			t.Fatal(err)
		}
		if assessCriteria([]SuccessCriterion{fromJSON}, []CriterionEvaluation{{CriterionID: 1, Passed: true}}).Verified {
			t.Fatal("task JSON spoofed nonempty target evidence")
		}
	}
}

func TestAssessCriteriaEmptyTargetsKeepRequiredWeight(t *testing.T) {
	for _, kind := range []CriterionType{CriterionContains, CriterionFileExists} {
		criteria := []SuccessCriterion{
			{ID: 1, Name: "invalid", Type: kind, Target: "", Weight: 1},
			{ID: 2, Name: "literal newline", Type: CriterionContains, Target: "\n", Weight: 1},
		}
		evaluations := []CriterionEvaluation{{CriterionID: 1, Passed: true}, {CriterionID: 2, Passed: true}}
		got := assessCriteria(criteria, evaluations)
		if got.Score != 0.5 || got.AutomatedTotal != 2 || got.EvaluatedAutomated != 1 || got.Verified || got.RankEligible || len(got.Pending) != 1 {
			t.Errorf("%s dropped required invalid criterion or rejected valid literal: %+v", kind, got)
		}
		validOnly := assessCriteria(criteria[1:], evaluations[1:])
		if !validOnly.Verified || validOnly.Score != 1 {
			t.Errorf("literal newline target rejected: %+v", validOnly)
		}
	}
}

func TestEmptyCriterionTargetCannotVerifyWinner(t *testing.T) {
	for _, kind := range []CriterionType{CriterionContains, CriterionFileExists, CriterionCommand, CriterionTestPass} {
		t.Run(string(kind), func(t *testing.T) {
			store := NewStore(setupTestDB(t))
			exp := &Experiment{
				ID: "empty-target", Name: "empty target", Task: Task{Prompt: "read and summarize"},
				Variants: []Variant{{ID: "v", Name: "candidate", ModelID: "model"}},
				Criteria: []SuccessCriterion{{Name: "required evidence", Type: kind, Target: "", Weight: 1}},
			}
			if err := store.CreateExperiment(exp); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveRun(&Run{ID: "r", ExperimentID: exp.ID, VariantID: "v", Status: RunCompleted, Output: ""}); err != nil {
				t.Fatal(err)
			}
			evaluations := EvaluateCriteria(context.Background(), t.TempDir(), "", "", exp.Criteria)
			if len(evaluations) != 1 {
				t.Fatalf("missing explicit failed evaluation: %+v", evaluations)
			}
			if evaluations[0].Passed || evaluations[0].Score != 0 || evaluations[0].Details == "" {
				t.Errorf("empty target counted as evidence: %+v", evaluations[0])
			}
			if err := store.ReplaceEvaluations("r", evaluations); err != nil {
				t.Fatal(err)
			}
			report, err := NewComparator(store).Compare(exp)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Variants) != 1 || report.Variants[0].Verified || len(report.Rankings) != 1 || report.Rankings[0].Winner || !strings.Contains(report.Summary, "No verified winner") {
				t.Fatalf("empty target crowned a verified winner: %+v", report)
			}
			if kind != CriterionContains && kind != CriterionFileExists {
				return
			}
			// Older versions stored passing evaluations for these empty targets.
			legacy := []CriterionEvaluation{{CriterionID: exp.Criteria[0].ID, Passed: true, Score: 1, Details: "legacy empty-target pass"}}
			if err := store.ReplaceEvaluations("r", legacy); err != nil {
				t.Fatal(err)
			}
			report, err = NewComparator(store).Compare(exp)
			if err != nil {
				t.Fatal(err)
			}
			if report.Variants[0].Verified || report.Variants[0].RankEligible || report.Rankings[0].Winner || !strings.Contains(strings.Join(report.Variants[0].CriteriaPending, " "), "empty target") {
				t.Errorf("legacy empty-target pass remains eligible: %+v", report)
			}
			stored, err := store.ListEvaluationsByExperiment(exp.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(stored["r"]) != 1 || !stored["r"][0].Passed || stored["r"][0].Details != "legacy empty-target pass" {
				t.Fatalf("comparison rewrote historical evidence: %+v", stored)
			}
		})
	}
}

func TestContainsCriterionPreservesNonemptyLiteralTarget(t *testing.T) {
	for _, tc := range []struct {
		output, target string
		passed         bool
	}{
		{"evidence", "evidence", true},
		{"evidence", "missing", false},
		{"line\n", "\n", true},
		{"line", "\n", false},
		{" a ", " a ", true},
		{"a", " a ", false},
	} {
		passed, _ := evaluateCriterion(context.Background(), t.TempDir(), tc.output, SuccessCriterion{Type: CriterionContains, Target: tc.target})
		if passed != tc.passed {
			t.Errorf("contains(%q, %q)=%v, want %v", tc.output, tc.target, passed, tc.passed)
		}
	}
}
