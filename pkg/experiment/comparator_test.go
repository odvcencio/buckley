package experiment

import (
	"testing"
)

func TestNewComparator(t *testing.T) {
	tests := []struct {
		name  string
		store *Store
		want  bool
	}{
		{"nil store returns nil", nil, false},
		{"valid store returns comparator", NewStore(setupTestDB(t)), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NewComparator(tt.store)
			if (got != nil) != tt.want {
				t.Errorf("NewComparator() = %v, want non-nil: %v", got, tt.want)
			}
		})
	}
}

func TestComparator_Compare(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	comparator := NewComparator(store)

	// Create experiment with variants and criteria
	exp := &Experiment{
		ID:   "exp-compare",
		Name: "comparison-test",
		Task: Task{Prompt: "test"},
		Variants: []Variant{
			{ID: "var-1", Name: "variant-1", ModelID: "gpt-4"},
			{ID: "var-2", Name: "variant-2", ModelID: "claude-3"},
		},
		Criteria: []SuccessCriterion{
			{Name: "test1", Type: CriterionTestPass, Target: "test", Weight: 1},
			{Name: "test2", Type: CriterionTestPass, Target: "test", Weight: 2},
		},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("failed to create experiment: %v", err)
	}

	// Create runs with different metrics
	run1 := &Run{
		ID:           "run-1",
		ExperimentID: "exp-compare",
		VariantID:    "var-1",
		Status:       RunCompleted,
		Output:       "output 1",
		Metrics: RunMetrics{
			DurationMs: 1000,
			TotalCost:  0.01,
		},
	}
	run2 := &Run{
		ID:           "run-2",
		ExperimentID: "exp-compare",
		VariantID:    "var-2",
		Status:       RunCompleted,
		Output:       "output 2",
		Metrics: RunMetrics{
			DurationMs: 2000,
			TotalCost:  0.02,
		},
	}
	if err := store.SaveRun(run1); err != nil {
		t.Fatalf("failed to save run 1: %v", err)
	}
	if err := store.SaveRun(run2); err != nil {
		t.Fatalf("failed to save run 2: %v", err)
	}

	// Add evaluations - var-1 passes both, var-2 passes only one
	evals1 := []CriterionEvaluation{
		{CriterionID: exp.Criteria[0].ID, Passed: true, Score: 1.0},
		{CriterionID: exp.Criteria[1].ID, Passed: true, Score: 1.0},
	}
	evals2 := []CriterionEvaluation{
		{CriterionID: exp.Criteria[0].ID, Passed: true, Score: 1.0},
		{CriterionID: exp.Criteria[1].ID, Passed: false, Score: 0.0},
	}
	if err := store.ReplaceEvaluations("run-1", evals1); err != nil {
		t.Fatalf("failed to save evaluations for run 1: %v", err)
	}
	if err := store.ReplaceEvaluations("run-2", evals2); err != nil {
		t.Fatalf("failed to save evaluations for run 2: %v", err)
	}

	// Test Compare
	report, err := comparator.Compare(exp)
	if err != nil {
		t.Fatalf("Compare() error = %v", err)
	}

	if report == nil {
		t.Fatal("Compare() returned nil report")
	}

	if report.ExperimentID != exp.ID {
		t.Errorf("Compare() ExperimentID = %v, want %v", report.ExperimentID, exp.ID)
	}

	if len(report.Variants) != 2 {
		t.Errorf("Compare() Variants count = %v, want 2", len(report.Variants))
	}

	if len(report.Rankings) != 2 {
		t.Errorf("Compare() Rankings count = %v, want 2", len(report.Rankings))
	}

	// var-1 should rank first (100% score vs ~33% score)
	if report.Rankings[0].VariantID != "var-1" {
		t.Errorf("Compare() first rank = %v, want var-1", report.Rankings[0].VariantID)
	}

	if report.Summary == "" {
		t.Error("Compare() Summary is empty")
	}
}

func TestComparator_Compare_NilExperiment(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	comparator := NewComparator(store)

	_, err := comparator.Compare(nil)
	if err == nil {
		t.Error("Compare(nil) should return error")
	}
}

func TestComparator_Compare_NilComparator(t *testing.T) {
	var comparator *Comparator

	_, err := comparator.Compare(&Experiment{})
	if err != ErrStoreUnavailable {
		t.Errorf("Compare() on nil comparator should return ErrStoreUnavailable, got %v", err)
	}
}

func TestAssessCriteria(t *testing.T) {
	tests := []struct {
		name             string
		criteria         []SuccessCriterion
		evaluations      []CriterionEvaluation
		wantScore        float64
		wantPassed       int
		wantFailed       int
		wantPending      int
		wantStatus       string
		wantVerified     bool
		wantRankEligible bool
	}{
		{
			name:        "empty criteria is unverified",
			criteria:    nil,
			wantScore:   0,
			wantPassed:  0,
			wantFailed:  0,
			wantPending: 0,
			wantStatus:  "unverified: no success criteria configured",
		},
		{
			name: "all passed",
			criteria: []SuccessCriterion{
				{ID: 1, Name: "c1", Type: CriterionTestPass, Weight: 1},
				{ID: 2, Name: "c2", Type: CriterionTestPass, Weight: 1},
			},
			evaluations: []CriterionEvaluation{
				{CriterionID: 1, Passed: true},
				{CriterionID: 2, Passed: true},
			},
			wantScore:        1.0,
			wantPassed:       2,
			wantFailed:       0,
			wantStatus:       "verified",
			wantVerified:     true,
			wantRankEligible: true,
		},
		{
			name: "all failed",
			criteria: []SuccessCriterion{
				{ID: 1, Name: "c1", Type: CriterionTestPass, Weight: 1},
				{ID: 2, Name: "c2", Type: CriterionTestPass, Weight: 1},
			},
			evaluations: []CriterionEvaluation{
				{CriterionID: 1, Passed: false},
				{CriterionID: 2, Passed: false},
			},
			wantScore:        0.0,
			wantPassed:       0,
			wantFailed:       2,
			wantStatus:       "evaluated: criteria failed",
			wantRankEligible: true,
		},
		{
			name: "partial pass with weights",
			criteria: []SuccessCriterion{
				{ID: 1, Name: "c1", Type: CriterionTestPass, Weight: 1},
				{ID: 2, Name: "c2", Type: CriterionTestPass, Weight: 2},
			},
			evaluations: []CriterionEvaluation{
				{CriterionID: 1, Passed: true},
				{CriterionID: 2, Passed: false},
			},
			wantScore:        1.0 / 3.0, // 1 out of 3 weight
			wantPassed:       1,
			wantFailed:       1,
			wantStatus:       "evaluated: criteria failed",
			wantRankEligible: true,
		},
		{
			name: "manual criteria remains pending",
			criteria: []SuccessCriterion{
				{ID: 1, Name: "c1", Type: CriterionTestPass, Weight: 1},
				{ID: 2, Name: "c2", Type: CriterionManual, Weight: 1},
			},
			evaluations: []CriterionEvaluation{
				{CriterionID: 1, Passed: true},
			},
			wantScore:        1.0,
			wantPassed:       1,
			wantFailed:       0,
			wantPending:      1,
			wantStatus:       "manual review pending",
			wantRankEligible: true,
		},
		{
			name: "manual only is not auto verified",
			criteria: []SuccessCriterion{
				{ID: 1, Name: "manual-check", Type: CriterionManual, Weight: 1},
			},
			wantScore:   0,
			wantPending: 1,
			wantStatus:  "manual review pending",
		},
		{
			name: "zero weight defaults to 1",
			criteria: []SuccessCriterion{
				{ID: 1, Name: "c1", Type: CriterionTestPass, Weight: 0},
				{ID: 2, Name: "c2", Type: CriterionTestPass, Weight: 0},
			},
			evaluations: []CriterionEvaluation{
				{CriterionID: 1, Passed: true},
				{CriterionID: 2, Passed: false},
			},
			wantScore:        0.5,
			wantPassed:       1,
			wantFailed:       1,
			wantStatus:       "evaluated: criteria failed",
			wantRankEligible: true,
		},
		{
			name: "missing evaluations are pending evidence",
			criteria: []SuccessCriterion{
				{ID: 1, Name: "c1", Type: CriterionTestPass, Weight: 1},
				{ID: 2, Name: "c2", Type: CriterionTestPass, Weight: 1},
			},
			evaluations: []CriterionEvaluation{
				{CriterionID: 1, Passed: true},
				// c2 missing
			},
			wantScore:   0.5,
			wantPassed:  1,
			wantFailed:  0,
			wantPending: 1,
			wantStatus:  "unverified: missing automated evaluation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := assessCriteria(tt.criteria, tt.evaluations)

			if got.Score != tt.wantScore {
				t.Errorf("assessCriteria() score = %v, want %v", got.Score, tt.wantScore)
			}
			if len(got.Passed) != tt.wantPassed {
				t.Errorf("assessCriteria() passed count = %v, want %v", len(got.Passed), tt.wantPassed)
			}
			if len(got.Failed) != tt.wantFailed {
				t.Errorf("assessCriteria() failed count = %v, want %v", len(got.Failed), tt.wantFailed)
			}
			if len(got.Pending) != tt.wantPending {
				t.Errorf("assessCriteria() pending count = %v, want %v", len(got.Pending), tt.wantPending)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("assessCriteria() status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Verified != tt.wantVerified {
				t.Errorf("assessCriteria() verified = %v, want %v", got.Verified, tt.wantVerified)
			}
			if got.RankEligible != tt.wantRankEligible {
				t.Errorf("assessCriteria() rank eligible = %v, want %v", got.RankEligible, tt.wantRankEligible)
			}
		})
	}
}

func TestRankVariants(t *testing.T) {
	tests := []struct {
		name       string
		reports    []VariantReport
		wantRank   []string // run IDs in expected display order
		wantRanks  []int
		wantWinner string
	}{
		{
			name:     "empty reports returns nil",
			reports:  nil,
			wantRank: nil,
		},
		{
			name: "single report",
			reports: []VariantReport{
				{VariantID: "v1", RunID: "run-1", Status: RunCompleted, CriteriaScore: 1.0, Verified: true, RankEligible: true},
			},
			wantRank:   []string{"run-1"},
			wantRanks:  []int{1},
			wantWinner: "run-1",
		},
		{
			name: "ranked by score (higher first)",
			reports: []VariantReport{
				{VariantID: "v1", RunID: "run-1", Status: RunCompleted, CriteriaScore: 0.5, RankEligible: true},
				{VariantID: "v2", RunID: "run-2", Status: RunCompleted, CriteriaScore: 1.0, Verified: true, RankEligible: true},
				{VariantID: "v3", RunID: "run-3", Status: RunCompleted, CriteriaScore: 0.75, RankEligible: true},
			},
			wantRank:   []string{"run-2", "run-3", "run-1"},
			wantRanks:  []int{1, 2, 3},
			wantWinner: "run-2",
		},
		{
			name: "same score ranked by cost (lower first)",
			reports: []VariantReport{
				{VariantID: "v1", RunID: "run-1", Status: RunCompleted, CriteriaScore: 1.0, Metrics: RunMetrics{TotalCost: 0.02}, Verified: true, RankEligible: true},
				{VariantID: "v2", RunID: "run-2", Status: RunCompleted, CriteriaScore: 1.0, Metrics: RunMetrics{TotalCost: 0.01}, Verified: true, RankEligible: true},
			},
			wantRank:   []string{"run-2", "run-1"},
			wantRanks:  []int{1, 2},
			wantWinner: "run-2",
		},
		{
			name: "same score and cost ranked by duration (lower first)",
			reports: []VariantReport{
				{VariantID: "v1", RunID: "run-1", Status: RunCompleted, CriteriaScore: 1.0, Metrics: RunMetrics{TotalCost: 0.01, DurationMs: 2000}, Verified: true, RankEligible: true},
				{VariantID: "v2", RunID: "run-2", Status: RunCompleted, CriteriaScore: 1.0, Metrics: RunMetrics{TotalCost: 0.01, DurationMs: 1000}, Verified: true, RankEligible: true},
			},
			wantRank:   []string{"run-2", "run-1"},
			wantRanks:  []int{1, 2},
			wantWinner: "run-2",
		},
		{
			name: "failed free run cannot outrank paid verified run",
			reports: []VariantReport{
				{VariantID: "v1", RunID: "failed-free", Status: RunFailed, CriteriaScore: 1.0, Metrics: RunMetrics{TotalCost: 0}},
				{VariantID: "v2", RunID: "paid-pass", Status: RunCompleted, CriteriaScore: 1.0, Metrics: RunMetrics{TotalCost: 0.02}, Verified: true, RankEligible: true},
			},
			wantRank:   []string{"paid-pass", "failed-free"},
			wantRanks:  []int{1, 0},
			wantWinner: "paid-pass",
		},
		{
			name: "all failed runs are unranked with no winner",
			reports: []VariantReport{
				{VariantID: "v1", RunID: "failed-a", Status: RunFailed, CriteriaScore: 1.0},
				{VariantID: "v2", RunID: "failed-b", Status: RunCancelled, CriteriaScore: 1.0},
			},
			wantRank:  []string{"failed-a", "failed-b"},
			wantRanks: []int{0, 0},
		},
		{
			name: "repeated runs for one variant stay distinct with stable ties",
			reports: []VariantReport{
				{VariantID: "v1", RunID: "run-b", Status: RunCompleted, CriteriaScore: 1.0, Metrics: RunMetrics{TotalCost: 0.01, DurationMs: 1000}, Verified: true, RankEligible: true},
				{VariantID: "v1", RunID: "run-a", Status: RunCompleted, CriteriaScore: 1.0, Metrics: RunMetrics{TotalCost: 0.01, DurationMs: 1000}, Verified: true, RankEligible: true},
			},
			wantRank:   []string{"run-a", "run-b"},
			wantRanks:  []int{1, 2},
			wantWinner: "run-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rankings := rankVariants(tt.reports)

			if tt.wantRank == nil {
				if rankings != nil {
					t.Errorf("rankVariants() = %v, want nil", rankings)
				}
				return
			}

			if len(rankings) != len(tt.wantRank) {
				t.Fatalf("rankVariants() count = %v, want %v", len(rankings), len(tt.wantRank))
			}

			winner := ""
			for i, want := range tt.wantRank {
				if rankings[i].RunID != want {
					t.Errorf("rankVariants()[%d].RunID = %v, want %v", i, rankings[i].RunID, want)
				}
				if rankings[i].Rank != tt.wantRanks[i] {
					t.Errorf("rankVariants()[%d].Rank = %v, want %v", i, rankings[i].Rank, tt.wantRanks[i])
				}
				if rankings[i].Winner {
					winner = rankings[i].RunID
				}
			}
			if winner != tt.wantWinner {
				t.Errorf("rankVariants() winner = %q, want %q", winner, tt.wantWinner)
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	tests := []struct {
		name      string
		exp       *Experiment
		rankings  []Ranking
		reports   []VariantReport
		wantEmpty bool
	}{
		{
			name:      "nil experiment returns empty",
			exp:       nil,
			rankings:  []Ranking{{VariantID: "v1"}},
			reports:   []VariantReport{{VariantID: "v1"}},
			wantEmpty: true,
		},
		{
			name:      "empty rankings returns empty",
			exp:       &Experiment{},
			rankings:  nil,
			reports:   []VariantReport{{VariantID: "v1"}},
			wantEmpty: true,
		},
		{
			name:      "missing winner report returns empty",
			exp:       &Experiment{},
			rankings:  []Ranking{{VariantID: "v1", RunID: "run-1", Winner: true}},
			reports:   []VariantReport{{VariantID: "v2", RunID: "run-2"}}, // different ID
			wantEmpty: true,
		},
		{
			name:      "valid inputs returns summary",
			exp:       &Experiment{},
			rankings:  []Ranking{{VariantID: "v1", RunID: "run-1", Score: 1, Winner: true}},
			reports:   []VariantReport{{VariantID: "v1", RunID: "run-1", VariantName: "best-variant", ModelID: "model-a", CriteriaScore: 1}},
			wantEmpty: false,
		},
		{
			name:      "no winner reports no verified winner",
			exp:       &Experiment{},
			rankings:  []Ranking{{VariantID: "v1", RunID: "run-1", Score: 0.85}},
			reports:   []VariantReport{{VariantID: "v1", RunID: "run-1", VariantName: "best-variant", CriteriaScore: 0.85}},
			wantEmpty: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarize(tt.exp, tt.rankings, tt.reports)
			if (got == "") != tt.wantEmpty {
				t.Errorf("summarize() = %q, wantEmpty %v", got, tt.wantEmpty)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{
			name:  "zero limit returns empty",
			value: "test",
			limit: 0,
			want:  "",
		},
		{
			name:  "negative limit returns empty",
			value: "test",
			limit: -1,
			want:  "",
		},
		{
			name:  "value shorter than limit unchanged",
			value: "short",
			limit: 100,
			want:  "short",
		},
		{
			name:  "value with whitespace trimmed",
			value: "  trimmed  ",
			limit: 100,
			want:  "trimmed",
		},
		{
			name:  "value longer than limit truncated",
			value: "this is a long string that should be truncated",
			limit: 20,
			want:  "this is a long strin...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncate(tt.value, tt.limit)
			if got != tt.want {
				t.Errorf("truncate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFindReport(t *testing.T) {
	reports := []VariantReport{
		{VariantID: "v1", VariantName: "first"},
		{VariantID: "v2", VariantName: "second"},
		{VariantID: "v3", VariantName: "third"},
	}

	tests := []struct {
		name string
		id   string
		want string
	}{
		{"found first", "v1", "first"},
		{"found last", "v3", "third"},
		{"not found", "v99", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findReport(reports, tt.id)
			if tt.want == "" {
				if got != nil {
					t.Errorf("findReport() = %v, want nil", got)
				}
			} else {
				if got == nil || got.VariantName != tt.want {
					t.Errorf("findReport() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}
