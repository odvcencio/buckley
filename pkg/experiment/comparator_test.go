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

func TestScoreCriteria(t *testing.T) {
	tests := []struct {
		name        string
		criteria    []SuccessCriterion
		evaluations []CriterionEvaluation
		wantScore   float64
		wantPassed  int
		wantFailed  int
	}{
		{
			name:       "empty criteria returns 1.0",
			criteria:   nil,
			wantScore:  1.0,
			wantPassed: 0,
			wantFailed: 0,
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
			wantScore:  1.0,
			wantPassed: 2,
			wantFailed: 0,
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
			wantScore:  0.0,
			wantPassed: 0,
			wantFailed: 2,
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
			wantScore:  1.0 / 3.0, // 1 out of 3 weight
			wantPassed: 1,
			wantFailed: 1,
		},
		{
			name: "manual criteria skipped",
			criteria: []SuccessCriterion{
				{ID: 1, Name: "c1", Type: CriterionTestPass, Weight: 1},
				{ID: 2, Name: "c2", Type: CriterionManual, Weight: 1},
			},
			evaluations: []CriterionEvaluation{
				{CriterionID: 1, Passed: true},
			},
			wantScore:  1.0,
			wantPassed: 1,
			wantFailed: 0,
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
			wantScore:  0.5,
			wantPassed: 1,
			wantFailed: 1,
		},
		{
			name: "missing evaluations count as failed",
			criteria: []SuccessCriterion{
				{ID: 1, Name: "c1", Type: CriterionTestPass, Weight: 1},
				{ID: 2, Name: "c2", Type: CriterionTestPass, Weight: 1},
			},
			evaluations: []CriterionEvaluation{
				{CriterionID: 1, Passed: true},
				// c2 missing
			},
			wantScore:  0.5,
			wantPassed: 1,
			wantFailed: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, passed, failed := scoreCriteria(tt.criteria, tt.evaluations)

			if score != tt.wantScore {
				t.Errorf("scoreCriteria() score = %v, want %v", score, tt.wantScore)
			}
			if len(passed) != tt.wantPassed {
				t.Errorf("scoreCriteria() passed count = %v, want %v", len(passed), tt.wantPassed)
			}
			if len(failed) != tt.wantFailed {
				t.Errorf("scoreCriteria() failed count = %v, want %v", len(failed), tt.wantFailed)
			}
		})
	}
}

func TestRankVariants(t *testing.T) {
	tests := []struct {
		name     string
		reports  []VariantReport
		wantRank []string // variant IDs in expected rank order
	}{
		{
			name:     "empty reports returns nil",
			reports:  nil,
			wantRank: nil,
		},
		{
			name: "single report",
			reports: []VariantReport{
				{VariantID: "v1", CriteriaScore: 1.0},
			},
			wantRank: []string{"v1"},
		},
		{
			name: "ranked by score (higher first)",
			reports: []VariantReport{
				{VariantID: "v1", CriteriaScore: 0.5},
				{VariantID: "v2", CriteriaScore: 1.0},
				{VariantID: "v3", CriteriaScore: 0.75},
			},
			wantRank: []string{"v2", "v3", "v1"},
		},
		{
			name: "same score ranked by cost (lower first)",
			reports: []VariantReport{
				{VariantID: "v1", CriteriaScore: 1.0, Metrics: RunMetrics{TotalCost: 0.02}},
				{VariantID: "v2", CriteriaScore: 1.0, Metrics: RunMetrics{TotalCost: 0.01}},
			},
			wantRank: []string{"v2", "v1"},
		},
		{
			name: "same score and cost ranked by duration (lower first)",
			reports: []VariantReport{
				{VariantID: "v1", CriteriaScore: 1.0, Metrics: RunMetrics{TotalCost: 0.01, DurationMs: 2000}},
				{VariantID: "v2", CriteriaScore: 1.0, Metrics: RunMetrics{TotalCost: 0.01, DurationMs: 1000}},
			},
			wantRank: []string{"v2", "v1"},
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

			for i, want := range tt.wantRank {
				if rankings[i].VariantID != want {
					t.Errorf("rankVariants()[%d].VariantID = %v, want %v", i, rankings[i].VariantID, want)
				}
				if rankings[i].Rank != i+1 {
					t.Errorf("rankVariants()[%d].Rank = %v, want %v", i, rankings[i].Rank, i+1)
				}
			}
		})
	}
}

func TestSummarize(t *testing.T) {
	tests := []struct {
		name     string
		exp      *Experiment
		rankings []Ranking
		reports  []VariantReport
		wantEmpty bool
	}{
		{
			name:     "nil experiment returns empty",
			exp:      nil,
			rankings: []Ranking{{VariantID: "v1"}},
			reports:  []VariantReport{{VariantID: "v1"}},
			wantEmpty: true,
		},
		{
			name:     "empty rankings returns empty",
			exp:      &Experiment{},
			rankings: nil,
			reports:  []VariantReport{{VariantID: "v1"}},
			wantEmpty: true,
		},
		{
			name:     "missing report returns empty",
			exp:      &Experiment{},
			rankings: []Ranking{{VariantID: "v1"}},
			reports:  []VariantReport{{VariantID: "v2"}}, // different ID
			wantEmpty: true,
		},
		{
			name:     "valid inputs returns summary",
			exp:      &Experiment{},
			rankings: []Ranking{{VariantID: "v1", Score: 0.85}},
			reports:  []VariantReport{{VariantID: "v1", VariantName: "best-variant", CriteriaScore: 0.85}},
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
