package artifact

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.uber.org/mock/gomock"
)

func TestNewCompactor(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := NewMockModelClient(ctrl)
	mockCounter := NewMockTokenCounter(ctrl)
	config := DefaultCompactionConfig()

	compactor := NewCompactor(config, mockClient, mockCounter)

	if compactor == nil {
		t.Fatal("NewCompactor returned nil")
	}
	if compactor.config.ContextThreshold != 0.80 {
		t.Errorf("config.ContextThreshold = %f, want 0.80", compactor.config.ContextThreshold)
	}
}

func TestDefaultCompactionConfig(t *testing.T) {
	config := DefaultCompactionConfig()

	if config.ContextThreshold != 0.80 {
		t.Errorf("ContextThreshold = %f, want 0.80", config.ContextThreshold)
	}
	if config.TaskInterval != 20 {
		t.Errorf("TaskInterval = %d, want 20", config.TaskInterval)
	}
	if config.TokenThreshold != 15000 {
		t.Errorf("TokenThreshold = %d, want 15000", config.TokenThreshold)
	}
	if config.TargetReduction != 0.70 {
		t.Errorf("TargetReduction = %f, want 0.70", config.TargetReduction)
	}
	if !config.PreserveCommands {
		t.Error("PreserveCommands should be true")
	}
	if !config.PreserveDecisions {
		t.Error("PreserveDecisions should be true")
	}
	if len(config.Models) == 0 {
		t.Error("Models should not be empty")
	}
}

func TestShouldCompact_ContextThreshold(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := NewMockModelClient(ctrl)
	mockCounter := NewMockTokenCounter(ctrl)

	config := DefaultCompactionConfig()
	config.ContextThreshold = 0.80
	compactor := NewCompactor(config, mockClient, mockCounter)

	artifact := &ExecutionArtifact{
		ProgressLog: []TaskProgress{
			{Description: "Test task"},
		},
	}

	// Mock token counter to return 8000 tokens (80% of 10000)
	mockCounter.EXPECT().Count(gomock.Any()).Return(8000, nil)

	should, reason := compactor.ShouldCompact(artifact, 10000)

	if !should {
		t.Error("ShouldCompact should return true when context threshold exceeded")
	}
	if !strings.Contains(reason, "context usage") {
		t.Errorf("Reason should mention context usage, got: %s", reason)
	}
}

func TestShouldCompact_TaskInterval(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := NewMockModelClient(ctrl)
	mockCounter := NewMockTokenCounter(ctrl)

	config := DefaultCompactionConfig()
	config.TaskInterval = 20
	compactor := NewCompactor(config, mockClient, mockCounter)

	// Create artifact with 20 completed tasks
	tasks := make([]TaskProgress, 20)
	for i := range tasks {
		tasks[i] = TaskProgress{
			TaskID:      i + 1,
			Status:      "completed",
			Description: "Task " + string(rune(i)),
		}
	}

	artifact := &ExecutionArtifact{
		ProgressLog: tasks,
	}

	// Mock low token count (below threshold)
	mockCounter.EXPECT().Count(gomock.Any()).Return(1000, nil)

	should, reason := compactor.ShouldCompact(artifact, 100000)

	if !should {
		t.Error("ShouldCompact should return true when task interval reached")
	}
	if !strings.Contains(reason, "completed 20 tasks") {
		t.Errorf("Reason should mention task interval, got: %s", reason)
	}
}

func TestShouldCompact_TokenThreshold(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := NewMockModelClient(ctrl)
	mockCounter := NewMockTokenCounter(ctrl)

	config := DefaultCompactionConfig()
	config.TokenThreshold = 15000
	compactor := NewCompactor(config, mockClient, mockCounter)

	artifact := &ExecutionArtifact{
		ProgressLog: []TaskProgress{
			{Description: "Large task with lots of content"},
		},
	}

	// Mock token counter to return exactly threshold
	mockCounter.EXPECT().Count(gomock.Any()).Return(15000, nil)

	should, reason := compactor.ShouldCompact(artifact, 100000)

	if !should {
		t.Error("ShouldCompact should return true when token threshold reached")
	}
	if !strings.Contains(reason, "artifact tokens") {
		t.Errorf("Reason should mention token threshold, got: %s", reason)
	}
}

func TestShouldCompact_NoTrigger(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := NewMockModelClient(ctrl)
	mockCounter := NewMockTokenCounter(ctrl)

	config := DefaultCompactionConfig()
	compactor := NewCompactor(config, mockClient, mockCounter)

	artifact := &ExecutionArtifact{
		ProgressLog: []TaskProgress{
			{TaskID: 1, Status: "completed", Description: "Small task"},
		},
	}

	// Mock low token count
	mockCounter.EXPECT().Count(gomock.Any()).Return(100, nil)

	should, reason := compactor.ShouldCompact(artifact, 100000)

	if should {
		t.Errorf("ShouldCompact should return false, got reason: %s", reason)
	}
	if reason != "" {
		t.Errorf("Reason should be empty when not compacting, got: %s", reason)
	}
}

func TestShouldCompact_TokenCounterError_UsesFallback(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := NewMockModelClient(ctrl)
	mockCounter := NewMockTokenCounter(ctrl)

	config := DefaultCompactionConfig()
	config.TokenThreshold = 100 // Low threshold for testing
	compactor := NewCompactor(config, mockClient, mockCounter)

	artifact := &ExecutionArtifact{
		ProgressLog: []TaskProgress{
			{Description: strings.Repeat("x", 500)}, // 500 chars / 4 = 125 tokens fallback
		},
	}

	// Mock token counter error
	mockCounter.EXPECT().Count(gomock.Any()).Return(0, errors.New("token counter failed"))

	should, reason := compactor.ShouldCompact(artifact, 100000)

	if !should {
		t.Error("ShouldCompact should use fallback token count and trigger on threshold")
	}
	if !strings.Contains(reason, "artifact tokens") {
		t.Errorf("Expected token threshold reason, got: %s", reason)
	}
}

func TestCompact_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := NewMockModelClient(ctrl)
	mockCounter := NewMockTokenCounter(ctrl)

	config := DefaultCompactionConfig()
	compactor := NewCompactor(config, mockClient, mockCounter)

	now := time.Now()
	tasksToCompact := []TaskProgress{
		{
			TaskID:              1,
			Description:         "Implement feature",
			Status:              "completed",
			StartedAt:           now,
			CompletedAt:         &now,
			ImplementationNotes: "Added new functionality",
			FilesModified: []FileModification{
				{Path: "main.go", LinesAdded: 50, LinesDeleted: 10},
			},
			TestsAdded: []TestResult{
				{Name: "TestFeature", Status: "pass"},
			},
		},
	}

	artifact := &ExecutionArtifact{
		ProgressLog: tasksToCompact,
	}

	// Mock token counting
	mockCounter.EXPECT().Count(gomock.Any()).Return(500, nil).Times(2) // original and compacted

	// Mock successful model completion
	mockClient.EXPECT().Complete(gomock.Any(), gomock.Any(), gomock.Any()).
		Return("Feature implemented successfully with proper testing.", nil)

	result, compacted, err := compactor.Compact(context.Background(), artifact, tasksToCompact)

	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if result == nil {
		t.Fatal("Compact() result is nil")
	}
	if result.TasksCompacted != 1 {
		t.Errorf("TasksCompacted = %d, want 1", result.TasksCompacted)
	}
	if result.Model == "" {
		t.Error("Model should be set")
	}
	if compacted == "" {
		t.Error("Compacted content should not be empty")
	}
	if !strings.Contains(compacted, "Compacted") {
		t.Error("Compacted content should contain 'Compacted' marker")
	}
}

func TestCompact_AllModelsFail(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := NewMockModelClient(ctrl)
	mockCounter := NewMockTokenCounter(ctrl)

	config := DefaultCompactionConfig()
	config.Models = []string{"model1", "model2"}
	compactor := NewCompactor(config, mockClient, mockCounter)

	tasksToCompact := []TaskProgress{
		{TaskID: 1, Description: "Task", Status: "completed"},
	}

	artifact := &ExecutionArtifact{
		ProgressLog: tasksToCompact,
	}

	// Mock token counting
	mockCounter.EXPECT().Count(gomock.Any()).Return(100, nil).AnyTimes()

	// Mock all models failing
	mockClient.EXPECT().Complete(gomock.Any(), "model1", gomock.Any()).
		Return("", errors.New("model1 failed"))
	mockClient.EXPECT().Complete(gomock.Any(), "model2", gomock.Any()).
		Return("", errors.New("model2 failed"))

	_, _, err := compactor.Compact(context.Background(), artifact, tasksToCompact)

	if err == nil {
		t.Error("Compact() should return error when all models fail")
	}
	if !strings.Contains(err.Error(), "all compaction models failed") {
		t.Errorf("Error should mention all models failed, got: %v", err)
	}
}

func TestCompact_FirstModelFailsSecondSucceeds(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := NewMockModelClient(ctrl)
	mockCounter := NewMockTokenCounter(ctrl)

	config := DefaultCompactionConfig()
	config.Models = []string{"bad-model", "good-model"}
	compactor := NewCompactor(config, mockClient, mockCounter)

	tasksToCompact := []TaskProgress{
		{TaskID: 1, Description: "Task", Status: "completed"},
	}

	artifact := &ExecutionArtifact{
		ProgressLog: tasksToCompact,
	}

	// Mock token counting
	mockCounter.EXPECT().Count(gomock.Any()).Return(100, nil).AnyTimes()

	// First model fails, second succeeds
	mockClient.EXPECT().Complete(gomock.Any(), "bad-model", gomock.Any()).
		Return("", errors.New("bad model error"))
	mockClient.EXPECT().Complete(gomock.Any(), "good-model", gomock.Any()).
		Return("Summary from good model", nil)

	result, _, err := compactor.Compact(context.Background(), artifact, tasksToCompact)

	if err != nil {
		t.Fatalf("Compact() should succeed with second model, got error: %v", err)
	}
	if result.Model != "good-model" {
		t.Errorf("Model = %s, want 'good-model'", result.Model)
	}
}

func TestCompact_PreservesCommandsAndDecisions(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := NewMockModelClient(ctrl)
	mockCounter := NewMockTokenCounter(ctrl)

	config := DefaultCompactionConfig()
	compactor := NewCompactor(config, mockClient, mockCounter)

	now := time.Now()
	tasksToCompact := []TaskProgress{
		{
			TaskID:      1,
			Description: "Task with commands and decisions",
			Status:      "completed",
			StartedAt:   now,
			FilesModified: []FileModification{
				{Path: "file.go", LinesAdded: 20, LinesDeleted: 5},
			},
			Deviations: []Deviation{
				{
					TaskID:      1,
					Type:        "Changed",
					Description: "Used different approach",
					Rationale:   "Better performance",
					Impact:      "High",
				},
			},
			TestsAdded: []TestResult{
				{Name: "TestFunc", Status: "pass"},
			},
		},
	}

	artifact := &ExecutionArtifact{
		ProgressLog: tasksToCompact,
	}

	// Mock token counting
	mockCounter.EXPECT().Count(gomock.Any()).Return(200, nil).AnyTimes()

	// Mock model completion
	mockClient.EXPECT().Complete(gomock.Any(), gomock.Any(), gomock.Any()).
		Return("Task completed", nil)

	_, compacted, err := compactor.Compact(context.Background(), artifact, tasksToCompact)

	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}

	// Verify commands are preserved
	if !strings.Contains(compacted, "Commands Executed") {
		t.Error("Compacted output should contain 'Commands Executed' section")
	}

	// Verify decisions are preserved
	if !strings.Contains(compacted, "Key Decisions") {
		t.Error("Compacted output should contain 'Key Decisions' section")
	}
	if !strings.Contains(compacted, "Better performance") {
		t.Error("Compacted output should preserve decision rationale")
	}

	// Verify aggregate metrics
	if !strings.Contains(compacted, "Files Changed") {
		t.Error("Compacted output should contain aggregate file metrics")
	}
	if !strings.Contains(compacted, "Tests:") {
		t.Error("Compacted output should contain test results")
	}
}

func TestCompact_CalculatesReductionPercent(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := NewMockModelClient(ctrl)
	mockCounter := NewMockTokenCounter(ctrl)

	config := DefaultCompactionConfig()
	compactor := NewCompactor(config, mockClient, mockCounter)

	tasksToCompact := []TaskProgress{
		{TaskID: 1, Description: "Task", Status: "completed"},
	}

	artifact := &ExecutionArtifact{
		ProgressLog: tasksToCompact,
	}

	// Mock token counting: 1000 tokens → 300 tokens (70% reduction)
	mockCounter.EXPECT().Count(gomock.Any()).Return(1000, nil).Times(1) // original
	mockCounter.EXPECT().Count(gomock.Any()).Return(300, nil).Times(1)  // compacted

	mockClient.EXPECT().Complete(gomock.Any(), gomock.Any(), gomock.Any()).
		Return("Compacted summary", nil)

	result, _, err := compactor.Compact(context.Background(), artifact, tasksToCompact)

	if err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	if result.OriginalTokens != 1000 {
		t.Errorf("OriginalTokens = %d, want 1000", result.OriginalTokens)
	}
	if result.CompactedTokens != 300 {
		t.Errorf("CompactedTokens = %d, want 300", result.CompactedTokens)
	}

	expectedReduction := 70.0
	if result.ReductionPercent < expectedReduction-1 || result.ReductionPercent > expectedReduction+1 {
		t.Errorf("ReductionPercent = %f, want ~%f", result.ReductionPercent, expectedReduction)
	}
}
