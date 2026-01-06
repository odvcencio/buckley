package experiment

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"
)

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
		"Winner:",
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
