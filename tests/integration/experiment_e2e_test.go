//go:build integration
// +build integration

package integration

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/experiment"
	"github.com/odvcencio/buckley/pkg/storage"
)

// TestExperimentStorePersistence tests the full experiment persistence lifecycle
func TestExperimentStorePersistence(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Create temp directory for database
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "experiment.db")

	// Setup storage
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	// Create experiment store
	expStore := experiment.NewStoreFromStorage(store)
	if expStore == nil {
		t.Fatal("Failed to create experiment store")
	}

	// Create experiment with full details
	exp := &experiment.Experiment{
		ID:          "exp-e2e-test-001",
		Name:        "e2e-persistence-test",
		Description: "Testing full persistence round-trip",
		Hypothesis:  "All data should be persisted correctly",
		Task: experiment.Task{
			Prompt:     "Test prompt for e2e testing",
			WorkingDir: "/test/working/dir",
			Timeout:    5 * time.Minute,
			Context: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		Variants: []experiment.Variant{
			{
				ID:         "var-1",
				Name:       "gpt-4-variant",
				ModelID:    "gpt-4",
				ProviderID: "openrouter",
			},
			{
				ID:         "var-2",
				Name:       "claude-variant",
				ModelID:    "claude-3-sonnet",
				ProviderID: "openrouter",
			},
		},
		Criteria: []experiment.SuccessCriterion{
			{
				Name:   "test-passes",
				Type:   experiment.CriterionTestPass,
				Target: "go test ./...",
				Weight: 2.0,
			},
			{
				Name:   "file-exists",
				Type:   experiment.CriterionFileExists,
				Target: "main.go",
				Weight: 1.0,
			},
		},
	}

	// Create experiment
	if err := expStore.CreateExperiment(exp); err != nil {
		t.Fatalf("Failed to create experiment: %v", err)
	}

	// Verify experiment was created
	loaded, err := expStore.GetExperiment(exp.ID)
	if err != nil {
		t.Fatalf("Failed to get experiment: %v", err)
	}
	if loaded == nil {
		t.Fatal("Experiment not found after creation")
	}

	// Verify experiment fields
	if loaded.Name != exp.Name {
		t.Errorf("Name = %q, want %q", loaded.Name, exp.Name)
	}
	if loaded.Description != exp.Description {
		t.Errorf("Description = %q, want %q", loaded.Description, exp.Description)
	}
	if loaded.Task.Prompt != exp.Task.Prompt {
		t.Errorf("Task.Prompt = %q, want %q", loaded.Task.Prompt, exp.Task.Prompt)
	}
	if len(loaded.Variants) != len(exp.Variants) {
		t.Errorf("Variants count = %d, want %d", len(loaded.Variants), len(exp.Variants))
	}
	if len(loaded.Criteria) != len(exp.Criteria) {
		t.Errorf("Criteria count = %d, want %d", len(loaded.Criteria), len(exp.Criteria))
	}

	// Update status
	if err := expStore.UpdateExperimentStatus(exp.ID, experiment.ExperimentRunning, nil); err != nil {
		t.Fatalf("Failed to update status: %v", err)
	}

	// Create runs for each variant
	run1 := &experiment.Run{
		ID:           "run-1",
		ExperimentID: exp.ID,
		VariantID:    "var-1",
		Status:       experiment.RunCompleted,
		Output:       "Variant 1 completed successfully",
		Files:        []string{"main.go", "test.go"},
		Metrics: experiment.RunMetrics{
			DurationMs:       60000,
			PromptTokens:     1000,
			CompletionTokens: 2000,
			TotalCost:        0.05,
			ToolCalls:        10,
			ToolSuccesses:    9,
			ToolFailures:     1,
			FilesModified:    2,
			LinesChanged:     150,
		},
		StartedAt: time.Now().Add(-time.Minute),
	}
	completedAt := time.Now()
	run1.CompletedAt = &completedAt

	if err := expStore.SaveRun(run1); err != nil {
		t.Fatalf("Failed to save run 1: %v", err)
	}

	run2 := &experiment.Run{
		ID:           "run-2",
		ExperimentID: exp.ID,
		VariantID:    "var-2",
		Status:       experiment.RunFailed,
		Output:       "Variant 2 failed",
		Metrics: experiment.RunMetrics{
			DurationMs:       30000,
			PromptTokens:     500,
			CompletionTokens: 1000,
			TotalCost:        0.02,
		},
		StartedAt: time.Now().Add(-30 * time.Second),
	}
	errMsg := "Test failure: assertion failed"
	run2.Error = &errMsg

	if err := expStore.SaveRun(run2); err != nil {
		t.Fatalf("Failed to save run 2: %v", err)
	}

	// Verify runs were persisted
	runs, err := expStore.ListRuns(exp.ID)
	if err != nil {
		t.Fatalf("Failed to list runs: %v", err)
	}
	if len(runs) != 2 {
		t.Errorf("Runs count = %d, want 2", len(runs))
	}

	// Add evaluations for run 1
	evals := []experiment.CriterionEvaluation{
		{
			CriterionID: loaded.Criteria[0].ID,
			Passed:      true,
			Score:       1.0,
			Details:     "All tests passed",
			EvaluatedAt: time.Now(),
		},
		{
			CriterionID: loaded.Criteria[1].ID,
			Passed:      true,
			Score:       1.0,
			Details:     "File exists",
			EvaluatedAt: time.Now(),
		},
	}
	if err := expStore.ReplaceEvaluations("run-1", evals); err != nil {
		t.Fatalf("Failed to save evaluations: %v", err)
	}

	// Complete experiment
	if err := expStore.UpdateExperimentStatus(exp.ID, experiment.ExperimentCompleted, nil); err != nil {
		t.Fatalf("Failed to complete experiment: %v", err)
	}

	// Verify final state
	final, err := expStore.GetExperiment(exp.ID)
	if err != nil {
		t.Fatalf("Failed to get final experiment: %v", err)
	}
	if final.Status != experiment.ExperimentCompleted {
		t.Errorf("Final status = %q, want %q", final.Status, experiment.ExperimentCompleted)
	}

	// Test comparator
	comparator := experiment.NewComparator(expStore)
	report, err := comparator.Compare(final)
	if err != nil {
		t.Fatalf("Failed to compare: %v", err)
	}
	if report == nil {
		t.Fatal("Comparison report is nil")
	}
	if len(report.Rankings) != 2 {
		t.Errorf("Rankings count = %d, want 2", len(report.Rankings))
	}

	// Test reporter
	reporter := experiment.NewReporterWithComparator(comparator)
	markdown, err := reporter.ComparisonMarkdown(final)
	if err != nil {
		t.Fatalf("Failed to generate report: %v", err)
	}
	if markdown == "" {
		t.Error("Markdown report is empty")
	}

	// Verify report contains key information
	expectedStrings := []string{
		"e2e-persistence-test",
		"gpt-4",
		"claude-3-sonnet",
		"Rankings",
	}
	for _, expected := range expectedStrings {
		if !strings.Contains(markdown, expected) {
			t.Errorf("Markdown missing %q", expected)
		}
	}

	t.Logf("Generated report:\n%s", markdown)
}

// TestExperimentListFiltering tests listing experiments with filters
func TestExperimentListFiltering(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "filter.db")

	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	expStore := experiment.NewStoreFromStorage(store)

	// Create experiments with different statuses
	experiments := []struct {
		id     string
		name   string
		status experiment.ExperimentStatus
	}{
		{"exp-1", "pending-exp", experiment.ExperimentPending},
		{"exp-2", "running-exp", experiment.ExperimentRunning},
		{"exp-3", "completed-exp", experiment.ExperimentCompleted},
		{"exp-4", "failed-exp", experiment.ExperimentFailed},
		{"exp-5", "another-completed", experiment.ExperimentCompleted},
	}

	for _, e := range experiments {
		exp := &experiment.Experiment{
			ID:     e.id,
			Name:   e.name,
			Status: e.status,
			Task:   experiment.Task{Prompt: "test"},
			Variants: []experiment.Variant{
				{ID: e.id + "-var", Name: "variant", ModelID: "test-model"},
			},
		}
		if err := expStore.CreateExperiment(exp); err != nil {
			t.Fatalf("Failed to create experiment %s: %v", e.id, err)
		}
	}

	// Test list all
	all, err := expStore.ListExperiments(100, "")
	if err != nil {
		t.Fatalf("Failed to list all: %v", err)
	}
	if len(all) != 5 {
		t.Errorf("All experiments count = %d, want 5", len(all))
	}

	// Test filter by completed
	completed, err := expStore.ListExperiments(100, experiment.ExperimentCompleted)
	if err != nil {
		t.Fatalf("Failed to list completed: %v", err)
	}
	if len(completed) != 2 {
		t.Errorf("Completed experiments count = %d, want 2", len(completed))
	}

	// Test filter by running
	running, err := expStore.ListExperiments(100, experiment.ExperimentRunning)
	if err != nil {
		t.Fatalf("Failed to list running: %v", err)
	}
	if len(running) != 1 {
		t.Errorf("Running experiments count = %d, want 1", len(running))
	}

	// Test limit
	limited, err := expStore.ListExperiments(2, "")
	if err != nil {
		t.Fatalf("Failed to list with limit: %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("Limited experiments count = %d, want 2", len(limited))
	}
}

// TestExperimentFindByName tests finding experiments by name
func TestExperimentFindByName(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "find.db")

	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	expStore := experiment.NewStoreFromStorage(store)

	// Create experiments
	exp1 := &experiment.Experiment{
		ID:   "exp-unique-1",
		Name: "unique-name-test",
		Task: experiment.Task{Prompt: "test"},
		Variants: []experiment.Variant{
			{ID: "var-1", Name: "variant", ModelID: "test-model"},
		},
	}
	if err := expStore.CreateExperiment(exp1); err != nil {
		t.Fatalf("Failed to create experiment: %v", err)
	}

	// Find by exact name
	found, err := expStore.FindExperimentByName("unique-name-test")
	if err != nil {
		t.Fatalf("Failed to find by name: %v", err)
	}
	if found == nil {
		t.Fatal("Experiment not found by name")
	}
	if found.ID != exp1.ID {
		t.Errorf("Found ID = %q, want %q", found.ID, exp1.ID)
	}

	// Find non-existent
	notFound, err := expStore.FindExperimentByName("nonexistent")
	if err != nil {
		t.Fatalf("Error finding nonexistent: %v", err)
	}
	if notFound != nil {
		t.Error("Should not find nonexistent experiment")
	}
}

// TestCriteriaEvaluation tests success criteria evaluation
func TestCriteriaEvaluation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tempDir := t.TempDir()

	// Create a test file
	testFilePath := filepath.Join(tempDir, "exists.txt")
	if err := writeTestFile(testFilePath, "test content"); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	tests := []struct {
		name       string
		criterion  experiment.SuccessCriterion
		output     string
		wantPassed bool
	}{
		{
			name: "contains passes",
			criterion: experiment.SuccessCriterion{
				ID:     1,
				Type:   experiment.CriterionContains,
				Target: "success",
			},
			output:     "Test completed with success",
			wantPassed: true,
		},
		{
			name: "contains fails",
			criterion: experiment.SuccessCriterion{
				ID:     2,
				Type:   experiment.CriterionContains,
				Target: "error",
			},
			output:     "Test completed with success",
			wantPassed: false,
		},
		{
			name: "file exists passes",
			criterion: experiment.SuccessCriterion{
				ID:     3,
				Type:   experiment.CriterionFileExists,
				Target: "exists.txt",
			},
			wantPassed: true,
		},
		{
			name: "file exists fails",
			criterion: experiment.SuccessCriterion{
				ID:     4,
				Type:   experiment.CriterionFileExists,
				Target: "missing.txt",
			},
			wantPassed: false,
		},
		{
			name: "command passes",
			criterion: experiment.SuccessCriterion{
				ID:     5,
				Type:   experiment.CriterionCommand,
				Target: "true",
			},
			wantPassed: true,
		},
		{
			name: "command fails",
			criterion: experiment.SuccessCriterion{
				ID:     6,
				Type:   experiment.CriterionCommand,
				Target: "false",
			},
			wantPassed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			criteria := []experiment.SuccessCriterion{tt.criterion}
			evals := experiment.EvaluateCriteria(nil, tempDir, "", tt.output, criteria)

			if len(evals) != 1 {
				t.Fatalf("Expected 1 evaluation, got %d", len(evals))
			}

			if evals[0].Passed != tt.wantPassed {
				t.Errorf("Passed = %v, want %v (details: %s)", evals[0].Passed, tt.wantPassed, evals[0].Details)
			}
		})
	}
}

// Helper function to write test files
func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}
