package experiment

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestComparator_WinnerRequiresCompletedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     RunStatus
		criteria   []CriterionType
		evaluation string
		winner     bool
		rank       int
	}{
		{"completed pass", RunCompleted, []CriterionType{CriterionContains}, "pass", true, 1},
		{"failed despite passed criterion", RunFailed, []CriterionType{CriterionContains}, "pass", false, 0},
		{"cancelled", RunCancelled, []CriterionType{CriterionContains}, "pass", false, 0},
		{"pending", RunPending, []CriterionType{CriterionContains}, "pass", false, 0},
		{"running", RunRunning, []CriterionType{CriterionContains}, "pass", false, 0},
		{"completed failed criterion", RunCompleted, []CriterionType{CriterionContains}, "fail", false, 1},
		{"missing evaluation", RunCompleted, []CriterionType{CriterionContains}, "missing", false, 0},
		{"no criteria", RunCompleted, nil, "missing", false, 0},
		{"manual only", RunCompleted, []CriterionType{CriterionManual}, "missing", false, 0},
		{"manual pending", RunCompleted, []CriterionType{CriterionContains, CriterionManual}, "pass", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := NewStore(setupTestDB(t))
			exp := &Experiment{ID: "winner-evidence", Name: "winner evidence", Task: Task{Prompt: "read and summarize"}, Variants: []Variant{{ID: "v", Name: "candidate", ModelID: "model"}}}
			for _, kind := range tc.criteria {
				exp.Criteria = append(exp.Criteria, SuccessCriterion{Name: string(kind), Type: kind, Target: "evidence", Weight: 1})
			}
			if err := store.CreateExperiment(exp); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveRun(&Run{ID: "r", ExperimentID: exp.ID, VariantID: "v", Status: tc.status, Output: "retained evidence"}); err != nil {
				t.Fatal(err)
			}
			var evals []CriterionEvaluation
			for _, criterion := range exp.Criteria {
				if criterion.Type != CriterionManual && tc.evaluation != "missing" {
					evals = append(evals, CriterionEvaluation{CriterionID: criterion.ID, Passed: tc.evaluation == "pass"})
				}
			}
			if err := store.ReplaceEvaluations("r", evals); err != nil {
				t.Fatal(err)
			}
			comparator := NewComparator(store)
			report, err := comparator.Compare(exp)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Variants) != 1 || report.Variants[0].OutputPreview != "retained evidence" {
				t.Fatalf("lost run evidence: %+v", report)
			}
			if len(report.Rankings) != 1 || report.Rankings[0].Rank != tc.rank {
				t.Errorf("rankings=%+v, want one entry with rank=%d", report.Rankings, tc.rank)
			}
			wantSummary := "No verified winner"
			if tc.winner {
				wantSummary = "Best verified run"
			}
			if !strings.Contains(report.Summary, wantSummary) {
				t.Errorf("summary=%q, want %q", report.Summary, wantSummary)
			}
			data, err := json.Marshal(report.Rankings)
			if err != nil {
				t.Fatal(err)
			}
			var rankings []struct {
				Winner bool
				RunID  string
			}
			if err := json.Unmarshal(data, &rankings); err != nil {
				t.Fatal(err)
			}
			if len(rankings) != 1 || rankings[0].Winner != tc.winner || rankings[0].RunID != "r" {
				t.Errorf("machine-readable rankings=%s", data)
			}
			markdown, err := NewReporterWithComparator(comparator).ComparisonMarkdown(exp)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(markdown, wantSummary) || !strings.Contains(markdown, "retained evidence") {
				t.Errorf("misleading or lossy markdown: %s", markdown)
			}
			var terminal bytes.Buffer
			renderer := NewTerminalReporterWithOutput(&terminal, comparator)
			renderer.noColor = true
			if err := renderer.RenderReport(exp); err != nil {
				t.Fatal(err)
			}
			if !tc.winner && strings.Contains(terminal.String(), "Winner:") {
				t.Errorf("terminal crowned unverified run: %s", terminal.String())
			}
			if !strings.Contains(terminal.String(), report.Variants[0].VerificationStatus) {
				t.Errorf("full terminal output omitted evidence state: %s", terminal.String())
			}
			terminal.Reset()
			if err := renderer.RenderCompact(exp); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(terminal.String(), report.Variants[0].VerificationStatus) {
				t.Errorf("compact output omitted evidence state: %s", terminal.String())
			}
		})
	}
}

func TestComparator_RepeatedVariantKeepsRunIdentity(t *testing.T) {
	store := NewStore(setupTestDB(t))
	exp := &Experiment{ID: "repeated-variant", Name: "repeat", Task: Task{Prompt: "read"}, Variants: []Variant{{ID: "v", Name: "candidate", ModelID: "model"}}, Criteria: []SuccessCriterion{{Name: "read evidence", Type: CriterionContains, Target: "evidence", Weight: 1}}}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatal(err)
	}
	for _, run := range []Run{
		{ID: "old-failed", ExperimentID: exp.ID, VariantID: "v", Status: RunFailed, StartedAt: time.Unix(100, 0), Metrics: RunMetrics{DurationMs: 10}, Output: "old evidence"},
		{ID: "new-passed", ExperimentID: exp.ID, VariantID: "v", Status: RunCompleted, StartedAt: time.Unix(200, 0), Metrics: RunMetrics{DurationMs: 2000, TotalCost: 0.02}, Output: "new evidence"},
	} {
		if err := store.SaveRun(&run); err != nil {
			t.Fatal(err)
		}
		if err := store.ReplaceEvaluations(run.ID, []CriterionEvaluation{{CriterionID: exp.Criteria[0].ID, Passed: true}}); err != nil {
			t.Fatal(err)
		}
	}
	comparator := NewComparator(store)
	report, err := comparator.Compare(exp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report.Summary, "new-passed") || strings.Contains(report.Summary, "old-failed") {
		t.Errorf("summary lost winning run identity: %q", report.Summary)
	}
	markdown, err := NewReporterWithComparator(comparator).ComparisonMarkdown(exp)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"new-passed", "old-failed"} {
		found := false
		for _, row := range strings.Split(markdown, "\n") {
			if strings.HasPrefix(row, "|") && strings.Contains(row, id) {
				found = true
				if id == "new-passed" && !strings.Contains(row, "$0.0200") {
					t.Errorf("winning row has wrong metrics: %s", row)
				}
				if id == "old-failed" && strings.Contains(row, "$0.0200") {
					t.Errorf("failed row borrowed winning cost: %s", row)
				}
			}
		}
		if !found {
			t.Errorf("missing ranking row for %s: %s", id, markdown)
		}
	}
	for _, compact := range []bool{false, true} {
		var out bytes.Buffer
		renderer := NewTerminalReporterWithOutput(&out, comparator)
		renderer.SetNoColor(true)
		if compact {
			err = renderer.RenderCompact(exp)
		} else {
			err = renderer.RenderReport(exp)
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, expected := range []struct{ id, cost string }{{"new-passed", "$0.0200"}, {"old-failed", ""}} {
			found := false
			for _, row := range strings.Split(out.String(), "\n") {
				if !strings.Contains(row, "model") || !strings.Contains(row, expected.id) {
					continue
				}
				if expected.cost != "" && strings.Contains(row, expected.cost) {
					found = true
				}
				if expected.cost == "" {
					found = true
					if strings.Contains(row, "$0.0200") {
						t.Errorf("compact=%v failed row borrowed winning cost: %s", compact, row)
					}
				}
			}
			if !found {
				t.Errorf("compact=%v missing exact run metrics for %+v: %s", compact, expected, out.String())
			}
		}
	}
}

func TestRankVariants_UnknownCostsUseStableLatencyTieBreak(t *testing.T) {
	base := []VariantReport{
		{VariantID: "v", RunID: "unknown-fast", Status: RunCompleted, CriteriaScore: 1, Verified: true, RankEligible: true, Metrics: RunMetrics{DurationMs: 1}},
		{VariantID: "v", RunID: "priced-expensive", Status: RunCompleted, CriteriaScore: 1, Verified: true, RankEligible: true, Metrics: RunMetrics{TotalCost: 0.03, DurationMs: 3}},
		{VariantID: "v", RunID: "priced-cheap", Status: RunCompleted, CriteriaScore: 1, Verified: true, RankEligible: true, Metrics: RunMetrics{TotalCost: 0.01, DurationMs: 5}},
	}
	for _, unknownCost := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		base[0].Metrics.TotalCost = unknownCost
		for _, order := range [][]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
			input := []VariantReport{base[order[0]], base[order[1]], base[order[2]]}
			got := rankVariants(input)
			for i, want := range []string{"unknown-fast", "priced-expensive", "priced-cheap"} {
				if got[i].RunID != want {
					t.Errorf("order=%v got=%+v, want %s at %d", order, got, want, i)
				}
				if input[i].RunID != base[order[i]].RunID {
					t.Fatal("ranking mutated caller-owned report order")
				}
			}
		}
	}
}
