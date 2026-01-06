package experiment

import (
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/parallel"
)

func TestNewReporter(t *testing.T) {
	r := NewReporter()
	if r == nil {
		t.Error("NewReporter() returned nil")
	}
	if r.comparator != nil {
		t.Error("NewReporter() comparator should be nil")
	}
}

func TestNewReporterWithComparator(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	comparator := NewComparator(store)

	r := NewReporterWithComparator(comparator)
	if r == nil {
		t.Error("NewReporterWithComparator() returned nil")
	}
	if r.comparator == nil {
		t.Error("NewReporterWithComparator() comparator should not be nil")
	}
}

func TestMarkdownTable(t *testing.T) {
	tests := []struct {
		name        string
		exp         *Experiment
		results     []*parallel.AgentResult
		wantEmpty   bool
		wantContain []string
	}{
		{
			name:      "nil experiment returns empty",
			exp:       nil,
			results:   nil,
			wantEmpty: true,
		},
		{
			name: "experiment with no results",
			exp: &Experiment{
				Name: "test-exp",
				Variants: []Variant{
					{ID: "v1", Name: "variant-1", ModelID: "gpt-4"},
				},
			},
			results:   nil,
			wantEmpty: false,
			wantContain: []string{
				"# Experiment: test-exp",
				"| Model | Success | Duration | Tokens | Files |",
				"| gpt-4 | no | - | - | - |",
			},
		},
		{
			name: "experiment with successful result",
			exp: &Experiment{
				Name: "test-exp",
				Variants: []Variant{
					{ID: "v1", Name: "variant-1", ModelID: "gpt-4"},
				},
			},
			results: []*parallel.AgentResult{
				{
					TaskID:   "v1",
					Success:  true,
					Duration: 5 * time.Second,
					Files:    []string{"a.go", "b.go"},
					Metrics: map[string]int{
						"prompt_tokens":     100,
						"completion_tokens": 200,
					},
				},
			},
			wantEmpty: false,
			wantContain: []string{
				"| gpt-4 | yes | 5s | 300 | 2 |",
			},
		},
		{
			name: "experiment with failed result",
			exp: &Experiment{
				Name: "test-exp",
				Variants: []Variant{
					{ID: "v1", Name: "variant-1", ModelID: "claude-3"},
				},
			},
			results: []*parallel.AgentResult{
				{
					TaskID:   "v1",
					Success:  false,
					Duration: 2 * time.Second,
				},
			},
			wantEmpty: false,
			wantContain: []string{
				"| claude-3 | no | 2s | - | 0 |",
			},
		},
		{
			name: "experiment with nil result in slice",
			exp: &Experiment{
				Name: "test-exp",
				Variants: []Variant{
					{ID: "v1", Name: "variant-1", ModelID: "gpt-4"},
				},
			},
			results: []*parallel.AgentResult{nil},
			wantContain: []string{
				"| gpt-4 | no | - | - | - |",
			},
		},
		{
			name: "variant with empty model uses name",
			exp: &Experiment{
				Name: "test-exp",
				Variants: []Variant{
					{ID: "v1", Name: "custom-variant", ModelID: ""},
				},
			},
			results: nil,
			wantContain: []string{
				"| custom-variant |",
			},
		},
		{
			name: "multiple variants",
			exp: &Experiment{
				Name: "multi-exp",
				Variants: []Variant{
					{ID: "v1", ModelID: "gpt-4"},
					{ID: "v2", ModelID: "claude-3"},
				},
			},
			results: []*parallel.AgentResult{
				{TaskID: "v1", Success: true, Duration: 3 * time.Second},
				{TaskID: "v2", Success: false, Duration: 5 * time.Second},
			},
			wantContain: []string{
				"| gpt-4 | yes | 3s |",
				"| claude-3 | no | 5s |",
			},
		},
	}

	reporter := NewReporter()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reporter.MarkdownTable(tt.exp, tt.results)

			if tt.wantEmpty && got != "" {
				t.Errorf("MarkdownTable() = %q, want empty", got)
				return
			}

			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("MarkdownTable() missing %q in:\n%s", want, got)
				}
			}
		})
	}
}

func TestComparisonMarkdown(t *testing.T) {
	tests := []struct {
		name        string
		setupStore  func(t *testing.T) (*Store, *Experiment)
		wantErr     bool
		wantContain []string
	}{
		{
			name: "nil experiment returns error",
			setupStore: func(t *testing.T) (*Store, *Experiment) {
				db := setupTestDB(t)
				return NewStore(db), nil
			},
			wantErr: true,
		},
		{
			name: "nil comparator returns error",
			setupStore: func(t *testing.T) (*Store, *Experiment) {
				return nil, &Experiment{ID: "test", Name: "test"}
			},
			wantErr: true,
		},
		{
			name: "valid experiment with runs",
			setupStore: func(t *testing.T) (*Store, *Experiment) {
				db := setupTestDB(t)
				store := NewStore(db)

				exp := &Experiment{
					ID:          "exp-1",
					Name:        "comparison-test",
					Description: "Testing comparison",
					Hypothesis:  "Model A is faster",
					Task:        Task{Prompt: "test prompt"},
					Variants: []Variant{
						{ID: "v1", Name: "variant-1", ModelID: "gpt-4"},
						{ID: "v2", Name: "variant-2", ModelID: "claude-3"},
					},
					Criteria: []SuccessCriterion{
						{Name: "test-crit", Type: CriterionTestPass, Target: "test", Weight: 1},
					},
				}
				if err := store.CreateExperiment(exp); err != nil {
					t.Fatalf("failed to create experiment: %v", err)
				}

				run1 := &Run{
					ID:           "run-1",
					ExperimentID: "exp-1",
					VariantID:    "v1",
					Status:       RunCompleted,
					Output:       "variant 1 output",
					Metrics: RunMetrics{
						DurationMs:       1000,
						TotalCost:        0.01,
						PromptTokens:     50,
						CompletionTokens: 100,
					},
				}
				run2 := &Run{
					ID:           "run-2",
					ExperimentID: "exp-1",
					VariantID:    "v2",
					Status:       RunCompleted,
					Output:       "variant 2 output",
					Metrics: RunMetrics{
						DurationMs:       2000,
						TotalCost:        0.02,
						PromptTokens:     75,
						CompletionTokens: 150,
					},
				}
				if err := store.SaveRun(run1); err != nil {
					t.Fatalf("failed to save run 1: %v", err)
				}
				if err := store.SaveRun(run2); err != nil {
					t.Fatalf("failed to save run 2: %v", err)
				}

				// Add evaluations
				evals1 := []CriterionEvaluation{{CriterionID: exp.Criteria[0].ID, Passed: true, Score: 1.0}}
				evals2 := []CriterionEvaluation{{CriterionID: exp.Criteria[0].ID, Passed: false, Score: 0.0}}
				if err := store.ReplaceEvaluations("run-1", evals1); err != nil {
					t.Fatalf("failed to save evaluations 1: %v", err)
				}
				if err := store.ReplaceEvaluations("run-2", evals2); err != nil {
					t.Fatalf("failed to save evaluations 2: %v", err)
				}

				return store, exp
			},
			wantErr: false,
			wantContain: []string{
				"# Experiment: comparison-test",
				"**Description:** Testing comparison",
				"**Hypothesis:** Model A is faster",
				"**Task:** test prompt",
				"## Rankings",
				"| Rank | Variant | Model | Score | Cost | Duration |",
				"## Variant Details",
				"### variant-1 (gpt-4)",
				"### variant-2 (claude-3)",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, exp := tt.setupStore(t)

			var reporter *Reporter
			if store != nil {
				comparator := NewComparator(store)
				reporter = NewReporterWithComparator(comparator)
			} else {
				reporter = NewReporter() // no comparator
			}

			got, err := reporter.ComparisonMarkdown(exp)

			if tt.wantErr {
				if err == nil {
					t.Error("ComparisonMarkdown() error = nil, want error")
				}
				return
			}

			if err != nil {
				t.Errorf("ComparisonMarkdown() error = %v", err)
				return
			}

			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("ComparisonMarkdown() missing %q in:\n%s", want, got)
				}
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		want     string
	}{
		{
			name:     "zero returns dash",
			duration: 0,
			want:     "-",
		},
		{
			name:     "negative returns dash",
			duration: -1 * time.Second,
			want:     "-",
		},
		{
			name:     "milliseconds",
			duration: 500 * time.Millisecond,
			want:     "500ms",
		},
		{
			name:     "seconds",
			duration: 5 * time.Second,
			want:     "5s",
		},
		{
			name:     "minutes and seconds",
			duration: 2*time.Minute + 30*time.Second,
			want:     "2m30s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDuration(tt.duration)
			if got != tt.want {
				t.Errorf("formatDuration() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatDurationMs(t *testing.T) {
	tests := []struct {
		name string
		ms   int64
		want string
	}{
		{
			name: "zero returns dash",
			ms:   0,
			want: "-",
		},
		{
			name: "negative returns dash",
			ms:   -100,
			want: "-",
		},
		{
			name: "milliseconds",
			ms:   500,
			want: "500ms",
		},
		{
			name: "seconds",
			ms:   5000,
			want: "5s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatDurationMs(tt.ms)
			if got != tt.want {
				t.Errorf("formatDurationMs() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindVariantReport(t *testing.T) {
	reports := []VariantReport{
		{VariantID: "v1", VariantName: "first"},
		{VariantID: "v2", VariantName: "second"},
	}

	tests := []struct {
		name string
		id   string
		want string
	}{
		{
			name: "found",
			id:   "v1",
			want: "first",
		},
		{
			name: "not found",
			id:   "v99",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findVariantReport(reports, tt.id)
			if tt.want == "" {
				if got != nil {
					t.Errorf("findVariantReport() = %v, want nil", got)
				}
			} else {
				if got == nil || got.VariantName != tt.want {
					t.Errorf("findVariantReport() = %v, want name %q", got, tt.want)
				}
			}
		})
	}
}
