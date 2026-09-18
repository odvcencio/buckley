package experiment

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTerminalReporter_ExplicitKnownFreeCostChart(t *testing.T) {
	var out bytes.Buffer
	r := NewTerminalReporterWithOutput(&out, nil)
	r.SetNoColor(true)
	r.renderCostChart(&ComparisonReport{Variants: []VariantReport{{
		ModelID: "local/model", RunID: "free-run", Status: RunCompleted,
		Metrics:      RunMetrics{TotalCost: 0},
		CostEvidence: CostEvidence{Status: CostEvidenceKnown, Comparable: true},
	}}})
	if !strings.Contains(out.String(), "$0.0000") || strings.Contains(out.String(), "unknown") || strings.Contains(out.String(), "No comparable") {
		t.Fatalf("explicit free evidence lost: %s", out.String())
	}
}

func TestTerminalReporter_RenderReport(t *testing.T) {
	// Create test data
	db := setupTestDB(t)
	store := NewStore(db)
	comparator := NewComparator(store)

	exp := &Experiment{
		ID:          "exp-1",
		Name:        "test-experiment",
		Description: "Test description",
		Status:      ExperimentCompleted,
		Task:        Task{Prompt: "test prompt"},
		Variants: []Variant{
			{ID: "var-1", Name: "gpt-4", ModelID: "gpt-4"},
			{ID: "var-2", Name: "claude-3", ModelID: "claude-3-sonnet"},
		},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("failed to create experiment: %v", err)
	}

	// Add runs to store
	run1 := &Run{
		ID:           "run-1",
		ExperimentID: "exp-1",
		VariantID:    "var-1",
		Status:       RunCompleted,
		Metrics: RunMetrics{
			DurationMs:       60000,
			PromptTokens:     1000,
			CompletionTokens: 2000,
			TotalCost:        0.05,
		},
	}
	run2 := &Run{
		ID:           "run-2",
		ExperimentID: "exp-1",
		VariantID:    "var-2",
		Status:       RunCompleted,
		Metrics: RunMetrics{
			DurationMs:       45000,
			PromptTokens:     800,
			CompletionTokens: 1500,
			TotalCost:        0.03,
		},
	}
	if err := store.SaveRun(run1); err != nil {
		t.Fatalf("failed to save run 1: %v", err)
	}
	if err := store.SaveRun(run2); err != nil {
		t.Fatalf("failed to save run 2: %v", err)
	}

	// Create reporter
	var buf bytes.Buffer
	reporter := NewTerminalReporterWithOutput(&buf, comparator)
	reporter.SetNoColor(true)

	err := reporter.RenderReport(exp)
	if err != nil {
		t.Fatalf("RenderReport failed: %v", err)
	}

	output := buf.String()

	// Verify key content
	checks := []string{
		"test-experiment",
		"(completed)",
		"gpt-4",
		"claude-3-sonnet",
		"Cost Comparison",
		"Duration Comparison",
		"No verified winner:",
	}

	for _, check := range checks {
		if !strings.Contains(output, check) {
			t.Errorf("output missing %q", check)
		}
	}
}

func TestTerminalReporter_RenderCompact(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	comparator := NewComparator(store)

	exp := &Experiment{
		ID:     "exp-1",
		Name:   "compact-test",
		Status: ExperimentCompleted,
		Task:   Task{Prompt: "test prompt"},
		Variants: []Variant{
			{ID: "var-1", Name: "model-a", ModelID: "model-a"},
		},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("failed to create experiment: %v", err)
	}

	run := &Run{
		ID:           "run-1",
		ExperimentID: "exp-1",
		VariantID:    "var-1",
		Status:       RunCompleted,
		Metrics: RunMetrics{
			DurationMs: 30000,
			TotalCost:  0.01,
		},
	}
	if err := store.SaveRun(run); err != nil {
		t.Fatalf("failed to save run: %v", err)
	}

	var buf bytes.Buffer
	reporter := NewTerminalReporterWithOutput(&buf, comparator)
	reporter.SetNoColor(true)

	err := reporter.RenderCompact(exp)
	if err != nil {
		t.Fatalf("RenderCompact failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "compact-test") {
		t.Error("output missing experiment name")
	}
	if !strings.Contains(output, "model-a") {
		t.Error("output missing model name")
	}
	if !strings.Contains(output, "#-") || !strings.Contains(output, "not evaluated") {
		t.Errorf("output missing unranked evidence state:\n%s", output)
	}
}

func TestTerminalReporter_NilExperiment(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	comparator := NewComparator(store)

	var buf bytes.Buffer
	reporter := NewTerminalReporterWithOutput(&buf, comparator)

	err := reporter.RenderReport(nil)
	if err == nil {
		t.Error("expected error for nil experiment")
	}

	err = reporter.RenderCompact(nil)
	if err == nil {
		t.Error("expected error for nil experiment in compact mode")
	}
}

func TestTerminalReporter_NilComparator(t *testing.T) {
	var buf bytes.Buffer
	reporter := NewTerminalReporterWithOutput(&buf, nil)

	exp := &Experiment{ID: "exp-1", Name: "test"}
	err := reporter.RenderReport(exp)
	if err == nil {
		t.Error("expected error for nil comparator")
	}
}

func TestBuildBar(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	comparator := NewComparator(store)

	var buf bytes.Buffer
	reporter := NewTerminalReporterWithOutput(&buf, comparator)

	tests := []struct {
		name     string
		value    float64
		maxValue float64
		width    int
		wantLen  int
	}{
		{"full bar", 100, 100, 20, 20},
		{"half bar", 50, 100, 20, 20},
		{"zero max", 50, 0, 20, 20},
		{"zero value", 0, 100, 20, 20},
		{"small value", 1, 100, 20, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bar := reporter.buildBar(tt.value, tt.maxValue, tt.width)
			runeCount := utf8.RuneCountInString(bar)
			if runeCount != tt.wantLen {
				t.Errorf("bar rune length = %d, want %d", runeCount, tt.wantLen)
			}
		})
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is too long", 10, "this is t…"},
		{"", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := truncateString(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestTerminalReporter_FailedVariants(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	comparator := NewComparator(store)

	exp := &Experiment{
		ID:     "exp-1",
		Name:   "failed-test",
		Status: ExperimentCompleted,
		Task:   Task{Prompt: "test prompt"},
		Variants: []Variant{
			{ID: "var-1", Name: "success-model", ModelID: "success-model"},
			{ID: "var-2", Name: "failed-model", ModelID: "failed-model"},
		},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("failed to create experiment: %v", err)
	}

	errMsg := "test failure"
	run1 := &Run{
		ID:           "run-1",
		ExperimentID: "exp-1",
		VariantID:    "var-1",
		Status:       RunCompleted,
		Metrics:      RunMetrics{DurationMs: 30000, TotalCost: 0.01},
	}
	run2 := &Run{
		ID:           "run-2",
		ExperimentID: "exp-1",
		VariantID:    "var-2",
		Status:       RunFailed,
		Error:        &errMsg,
	}
	if err := store.SaveRun(run1); err != nil {
		t.Fatalf("failed to save run 1: %v", err)
	}
	if err := store.SaveRun(run2); err != nil {
		t.Fatalf("failed to save run 2: %v", err)
	}

	var buf bytes.Buffer
	reporter := NewTerminalReporterWithOutput(&buf, comparator)
	reporter.SetNoColor(true)

	err := reporter.RenderReport(exp)
	if err != nil {
		t.Fatalf("RenderReport failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "✓") {
		t.Error("output missing success indicator")
	}
	if !strings.Contains(output, "✗") {
		t.Error("output missing failure indicator")
	}
	if !strings.Contains(output, "failed-model") {
		t.Error("output missing failed model row")
	}
	if !strings.Contains(output, "unknown (legacy run without retained cost evidence)") {
		t.Error("output missing legacy unknown-cost failed run observation")
	}
}

func TestTerminalReporter_ZeroCostVariants(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	comparator := NewComparator(store)

	exp := &Experiment{
		ID:     "exp-1",
		Name:   "zero-cost-test",
		Status: ExperimentCompleted,
		Task:   Task{Prompt: "test prompt"},
		Variants: []Variant{
			{ID: "var-1", Name: "local-model", ModelID: "ollama/llama3"},
		},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("failed to create experiment: %v", err)
	}

	run := &Run{
		ID:           "run-1",
		ExperimentID: "exp-1",
		VariantID:    "var-1",
		Status:       RunCompleted,
		Metrics:      RunMetrics{DurationMs: 60000, TotalCost: 0},
	}
	if err := store.SaveRun(run); err != nil {
		t.Fatalf("failed to save run: %v", err)
	}

	var buf bytes.Buffer
	reporter := NewTerminalReporterWithOutput(&buf, comparator)
	reporter.SetNoColor(true)

	err := reporter.RenderReport(exp)
	if err != nil {
		t.Fatalf("RenderReport failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "ollama/llama3") {
		t.Error("output missing model name")
	}
}

func TestTerminalReporter_RenderedOutputsPreserveCollidingRunAndModelIdentity(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)
	comparator := NewComparator(store)

	runA := "01K4ABCDEF1200000000000001"
	runB := "01K4ABCDEF1200000000000002"
	modelA := "provider/model-release-2026-09-04-alpha"
	modelB := "provider/model-release-2026-09-04-beta"

	exp := &Experiment{
		ID:     "exp-colliding-identity",
		Name:   "identity-test",
		Status: ExperimentCompleted,
		Task:   Task{Prompt: "compare"},
		Variants: []Variant{
			{ID: "variant-a", Name: "sameVariant", ModelID: modelA},
			{ID: "variant-b", Name: "sameVariant", ModelID: modelB},
		},
		Criteria: []SuccessCriterion{{Name: "tests pass", Type: CriterionTestPass, Weight: 1}},
	}
	if err := store.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	runs := []*Run{
		{ID: runA, ExperimentID: exp.ID, VariantID: "variant-a", Status: RunCompleted, Metrics: RunMetrics{DurationMs: 1100, TotalCost: 0.1111, PromptTokens: 10, CompletionTokens: 11}},
		{ID: runB, ExperimentID: exp.ID, VariantID: "variant-b", Status: RunCompleted, Metrics: RunMetrics{DurationMs: 2200, TotalCost: 0.2222, PromptTokens: 20, CompletionTokens: 22}},
	}
	for _, run := range runs {
		if err := store.SaveRun(run); err != nil {
			t.Fatalf("SaveRun %s: %v", run.ID, err)
		}
		if err := store.ReplaceEvaluations(run.ID, []CriterionEvaluation{{CriterionID: exp.Criteria[0].ID, Passed: true, Score: 1}}); err != nil {
			t.Fatalf("ReplaceEvaluations %s: %v", run.ID, err)
		}
	}

	var full bytes.Buffer
	fullReporter := NewTerminalReporterWithOutput(&full, comparator)
	fullReporter.SetNoColor(true)
	if err := fullReporter.RenderReport(exp); err != nil {
		t.Fatalf("RenderReport: %v", err)
	}
	fullOutput := full.String()
	for _, want := range []string{runA, runB, modelA, modelB} {
		if !strings.Contains(fullOutput, want) {
			t.Fatalf("full output missing exact identity %q in:\n%s", want, fullOutput)
		}
	}
	for _, want := range [][]string{
		{modelA + " / " + runA, "$0.1111"},
		{modelB + " / " + runB, "$0.2222"},
		{modelA + " / " + runA, "1s"},
		{modelB + " / " + runB, "2s"},
	} {
		if !lineContainsAll(fullOutput, want...) {
			t.Fatalf("full output does not associate %v in one line:\n%s", want, fullOutput)
		}
	}
	if strings.Contains(fullOutput, "01K4ABCDEF1…") || strings.Contains(fullOutput, "provider/model-rele…") {
		t.Fatalf("full output contains visually colliding truncation:\n%s", fullOutput)
	}

	var compact bytes.Buffer
	compactReporter := NewTerminalReporterWithOutput(&compact, comparator)
	compactReporter.SetNoColor(true)
	if err := compactReporter.RenderCompact(exp); err != nil {
		t.Fatalf("RenderCompact: %v", err)
	}
	compactOutput := compact.String()
	for _, want := range []string{runA, runB, modelA, modelB, "tokens=21", "tokens=42"} {
		if !strings.Contains(compactOutput, want) {
			t.Fatalf("compact output missing %q in:\n%s", want, compactOutput)
		}
	}
}

func lineContainsAll(output string, parts ...string) bool {
	for _, line := range strings.Split(output, "\n") {
		matched := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
