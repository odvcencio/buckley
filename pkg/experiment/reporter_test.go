package experiment

import (
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/parallel"
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
				"**Current experiment task:** test prompt",
				"Best verified run: variant-1 / run-1 (gpt-4, 100.0% score)",
				"## Rankings",
				"| Rank | Run | Variant | Requested model | Execution identity | Status | Evidence | Score | Cost | Duration | Tokens |",
				"| 1 | run-1 | variant-1 | gpt-4 | unknown (no model response identity evidence) | completed | verified | 100.0% | $0.0100 legacy scalar | 1s | 150 |",
				"| 2 | run-2 | variant-2 | claude-3 | unknown (no model response identity evidence) | completed | evaluated: criteria failed | 0.0% | $0.0200 legacy scalar | 2s | 225 |",
				"## Variant Details",
				"### variant-1 / run-1 (gpt-4)",
				"### variant-2 / run-2 (claude-3)",
				"- **Run ID:** run-1",
				"- **Variant ID:** v1",
				"- **Evidence:** verified",
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

func TestComparisonMarkdown_DisclosesUnverifiedRunsAndIdentity(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	exp := &Experiment{
		ID:   "exp-unverified",
		Name: "unverified",
		Task: Task{Prompt: "compare"},
		Variants: []Variant{
			{ID: "v1", Name: "paid", ModelID: "paid-model", ProviderID: "provider-paid"},
			{ID: "v2", Name: "free", ModelID: "free-model", ProviderID: "provider-free"},
		},
		Criteria: []SuccessCriterion{
			{Name: "automated", Type: CriterionTestPass, Weight: 1},
			{Name: "manual", Type: CriterionManual, Weight: 1},
		},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	errText := "tool failed"
	runs := []*Run{
		{
			ID:           "paid-run",
			ExperimentID: exp.ID,
			VariantID:    "v1",
			SessionID:    "session-paid",
			Branch:       "branch-paid",
			Status:       RunCompleted,
			Metrics:      RunMetrics{TotalCost: 0.02, DurationMs: 1500, PromptTokens: 7, CompletionTokens: 8},
			ModelExecutions: []model.ExecutionIdentity{{
				RequestedModel: "paid-model",
				SelectedModel:  "paid-model",
				ProviderID:     "provider-paid",
				ResponseModel:  "paid-model-2026-09-05",
				ResponseID:     "resp-paid-001",
			}},
		},
		{
			ID:           "free-run",
			ExperimentID: exp.ID,
			VariantID:    "v2",
			Status:       RunFailed,
			Metrics:      RunMetrics{TotalCost: 0, DurationMs: 500, PromptTokens: 1, CompletionTokens: 2},
			Error:        &errText,
		},
	}
	for _, run := range runs {
		if err := store.SaveRun(run); err != nil {
			t.Fatalf("SaveRun %s: %v", run.ID, err)
		}
	}
	if err := store.ReplaceEvaluations("paid-run", []CriterionEvaluation{{CriterionID: exp.Criteria[0].ID, Passed: true, Score: 1}}); err != nil {
		t.Fatalf("ReplaceEvaluations: %v", err)
	}

	got, err := NewReporterWithComparator(NewComparator(store)).ComparisonMarkdown(exp)
	if err != nil {
		t.Fatalf("ComparisonMarkdown: %v", err)
	}
	for _, want := range []string{
		"No verified winner",
		"| Rank | Run | Variant | Requested model | Execution identity | Status | Evidence | Score | Cost | Duration | Tokens |",
		"| 1 | paid-run | paid | paid-model | 1 observed; selected=paid-model; provider=provider-paid; reported=paid-model-2026-09-05 | completed | manual review pending | 100.0% | $0.0200 legacy scalar | 2s | 15 |",
		"| - | free-run | free | free-model | unknown (no model response identity evidence) | failed | unverified: missing automated evaluation | not evaluated | unknown (legacy run without retained cost evidence) | 500ms | 3 |",
		"- **Provider:** provider-paid",
		"- **Session:** session-paid",
		"- **Branch:** branch-paid",
		"- **Execution identity:** 1 observed response(s): 1:requested=paid-model selected=paid-model provider=provider-paid response_model=paid-model-2026-09-05 response_id=resp-paid-001",
		"- **Execution identity limit:** observed response identities only; not a complete attempt audit or backend revision proof",
		"- **Pending review:** manual",
		"- **Error:** tool failed",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ComparisonMarkdown missing %q in:\n%s", want, got)
		}
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
		{VariantID: "v1", RunID: "run-1", VariantName: "first"},
		{VariantID: "v1", RunID: "run-2", VariantName: "second duplicate variant"},
		{VariantID: "collision-id", RunID: "run-3", VariantName: "variant collision"},
		{VariantID: "v4", RunID: "collision-id", VariantName: "run collision wins"},
		{VariantID: "v4", RunID: "run-5", VariantName: "later duplicate variant"},
		{VariantID: "v6", RunID: "run-5", VariantName: "later duplicate run"},
	}

	tests := []struct {
		name string
		id   string
		want string
	}{
		{
			name: "found by run id",
			id:   "run-2",
			want: "second duplicate variant",
		},
		{
			name: "variant fallback uses first match",
			id:   "v1",
			want: "first",
		},
		{
			name: "run id wins over variant id collision",
			id:   "collision-id",
			want: "run collision wins",
		},
		{
			name: "run duplicate uses first match",
			id:   "run-5",
			want: "later duplicate variant",
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
