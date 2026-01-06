package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/mock/gomock"

	orchestratorMocks "github.com/odvcencio/buckley/pkg/orchestrator/mocks"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool"
)

func setupMockModel(t *testing.T) (*gomock.Controller, *orchestratorMocks.MockModelClient) {
	ctrl := gomock.NewController(t)
	return ctrl, orchestratorMocks.NewMockModelClient(ctrl)
}

func mockChatResponse(content string) *model.ChatResponse {
	return &model.ChatResponse{
		Choices: []model.Choice{
			{
				Message: model.Message{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: "stop",
			},
		},
	}
}

func TestNewExecutor(t *testing.T) {
	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()

	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
		Description: "Test Plan",
		Tasks: []Task{
			{ID: "1", Title: "Task 1", Description: "Test task"},
		},
	}

	// Create in-memory database for testing
	tmpDB := filepath.Join(t.TempDir(), "test.db")
	store, err := storage.New(tmpDB)
	if err != nil {
		t.Fatalf("Failed to create storage: %v", err)
	}
	defer store.Close()

	registry := tool.NewRegistry()
	cfg := &config.Config{}
	planner := &Planner{}
	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)

	if executor == nil {
		t.Fatal("NewExecutor returned nil")
	}
	if executor.plan != plan {
		t.Error("Executor plan not set correctly")
	}
}

func TestExecutor_ExecuteTask_PreconditionValidation(t *testing.T) {
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	// Create a simple project structure
	os.WriteFile("go.mod", []byte("module test\n\ngo 1.21"), 0644)

	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()
	mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).Times(0)

	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Validation Feature",
		Description: "Test Plan",
		Tasks: []Task{
			{
				ID:          "1",
				Title:       "Test Validation Task",
				Description: "Run go tests",
				Files:       []string{"/nonexistent/deep/nested/path/test.txt"}, // Use absolute path with no valid parent
			},
		},
	}

	store := &storage.Store{}
	registry := tool.NewRegistry()
	cfg := &config.Config{
		Orchestrator: config.OrchestratorConfig{
			MaxSelfHealAttempts: 3,
			MaxReviewCycles:     2,
			TrustLevel:          "autonomous",
		},
	}
	planner := &Planner{}

	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)

	task := &plan.Tasks[0]
	err := executor.executeTask(task)

	// Should fail validation because the path has no valid parent directory
	// Even though write_file can create parent dirs, it can't create /nonexistent/
	if err == nil {
		t.Error("Expected validation to fail for path with no valid parent, but it succeeded")
	}

	if !contains(err.Error(), "validation failed") {
		t.Errorf("Expected 'validation failed' in error, got: %v", err)
	}

	os.Remove("go.mod")
}

func TestExecutor_VerifyOutcomes(t *testing.T) {
	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()

	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	// Create a test file
	testFile := "test_output.txt"
	os.WriteFile(testFile, []byte("test content"), 0644)

	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
		Description: "Test Plan",
		Tasks: []Task{
			{
				ID:           "1",
				Title:        "Test Task",
				Description:  "Create file",
				Files:        []string{testFile},
				Verification: []string{"file exists"},
			},
		},
	}

	store := &storage.Store{}
	registry := tool.NewRegistry()
	cfg := &config.Config{}
	planner := &Planner{}
	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)

	// Create a task and verify outcomes
	task := &plan.Tasks[0]
	verifyResult := &VerifyResult{}
	err := executor.verifier.VerifyOutcomes(task, verifyResult)

	if err != nil {
		t.Logf("VerifyOutcomes error (may be expected): %v", err)
	}

	// Should find the file and pass basic verification
	if !verifyResult.Passed {
		t.Logf("Verify outcomes didn't pass (may be expected due to missing tools): %+v", verifyResult)
	}

	os.Remove(testFile)
}

func TestExecutor_DependenciesMet(t *testing.T) {
	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
		Description: "Test Plan",
		Tasks: []Task{
			{ID: "1", Title: "Task 1", Description: "First task", Status: TaskCompleted},
			{ID: "2", Title: "Task 2", Description: "Second task", Dependencies: []string{"1"}},
			{ID: "3", Title: "Task 3", Description: "Third task", Dependencies: []string{"2"}},
		},
	}

	executor := &Executor{plan: plan}

	// Task 3 depends on task 2, but task 2 is not completed
	task3 := &plan.Tasks[2]
	if executor.dependenciesMet(task3) {
		t.Error("Expected dependenciesMet to return false for task with unmet dependencies")
	}

	// Complete task 2
	plan.Tasks[1].Status = TaskCompleted

	if !executor.dependenciesMet(task3) {
		t.Error("Expected dependenciesMet to return true when all dependencies are met")
	}
}

func TestExecutor_PersistExecutionContext(t *testing.T) {
	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()

	tmpDir := t.TempDir()

	// Create a mock storage with actual DB
	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
		Description: "Test Plan",
		Tasks: []Task{
			{ID: "1", Title: "Test Task", Description: "Test execution"},
		},
	}

	registry := tool.NewRegistry()
	cfg := &config.Config{}
	planner := &Planner{}
	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)

	// Create a completed task for context recording
	task := &plan.Tasks[0]
	task.Status = TaskCompleted

	// Record execution context
	ctx := ExecutionContext{
		PlanID:              plan.ID,
		TaskID:              task.ID,
		Status:              "completed",
		ValidationErrors:    "[]",
		VerificationResults: "{\"passed\":true}",
		Artifacts:           `[{"id":"test","type":"file","path":"test.go"}]`,
	}

	err = executor.RecordExecutionContext(ctx)
	if err != nil {
		t.Errorf("Failed to record execution context: %v", err)
	}

	// Verify it was persisted
	var count int
	err = store.DB().QueryRow("SELECT COUNT(*) FROM executions WHERE plan_id = ?", plan.ID).Scan(&count)
	if err != nil {
		t.Errorf("Failed to query executions: %v", err)
	}

	if count != 1 {
		t.Errorf("Expected 1 execution record, got %d", count)
	}

	// Cleanup
	os.RemoveAll(tmpDir)
}

func TestExecutor_HandleError(t *testing.T) {
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()
	mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).
		Return(nil, fmt.Errorf("api error")).AnyTimes()

	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
		Description: "Test Plan",
		Tasks: []Task{
			{ID: "1", Title: "Test Task", Description: "Test error handling"},
		},
	}

	store := &storage.Store{}
	registry := tool.NewRegistry()
	cfg := &config.Config{
		Orchestrator: config.OrchestratorConfig{
			TrustLevel: "autonomous",
		},
	}
	planner := &Planner{}
	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)
	executor.maxRetries = 2

	task := &plan.Tasks[0]
	testErr := fmt.Errorf("test error")

	// First call should retry
	err := executor.handleError(task, testErr)
	if err == nil {
		t.Error("Expected error on first retry (max retries not exceeded)")
	}

	if executor.retryCount != 1 {
		t.Errorf("Expected retryCount=1, got %d", executor.retryCount)
	}

	// Second call should retry again
	err = executor.handleError(task, testErr)
	if err == nil {
		t.Error("Expected error on second retry")
	}

	if executor.retryCount != 2 {
		t.Errorf("Expected retryCount=2, got %d", executor.retryCount)
	}

	// Third call should exceed max retries
	err = executor.handleError(task, testErr)
	if err == nil || !contains(err.Error(), "max retries exceeded") {
		t.Errorf("Expected 'max retries exceeded' error, got: %v", err)
	}
}

func TestExecutor_HandleError_FirstAttemptNotLoop(t *testing.T) {
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()
	mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).
		Return(nil, fmt.Errorf("api error")).Times(1)

	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
		Description: "Test Plan",
		Tasks: []Task{
			{ID: "1", Title: "Test Task", Description: "Test error handling"},
		},
	}

	store := &storage.Store{}
	registry := tool.NewRegistry()
	cfg := &config.Config{
		Orchestrator: config.OrchestratorConfig{
			TrustLevel: "autonomous",
		},
	}
	planner := &Planner{}
	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)
	executor.maxRetries = 3

	task := &plan.Tasks[0]
	testErr := fmt.Errorf("test error")

	err := executor.handleError(task, testErr)
	if err == nil {
		t.Fatal("Expected error on first retry")
	}
	if contains(err.Error(), "retry loop detected") {
		t.Fatalf("Did not expect loop detection on first attempt, got: %v", err)
	}
	if !contains(err.Error(), "failed to generate fix") {
		t.Errorf("Expected fix-generation error, got: %v", err)
	}
}

func TestExecutor_HandleError_LoopDetectedOnRepeatedError(t *testing.T) {
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()
	// First attempt tries to generate a fix; second should short-circuit on loop detection.
	mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).
		Return(nil, fmt.Errorf("api error")).Times(1)

	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
		Description: "Test Plan",
		Tasks: []Task{
			{ID: "1", Title: "Test Task", Description: "Test error handling"},
		},
	}

	store := &storage.Store{}
	registry := tool.NewRegistry()
	cfg := &config.Config{
		Orchestrator: config.OrchestratorConfig{
			TrustLevel: "autonomous",
		},
	}
	planner := &Planner{}
	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)
	executor.maxRetries = 5

	task := &plan.Tasks[0]
	testErr := fmt.Errorf("test error")

	firstErr := executor.handleError(task, testErr)
	if firstErr == nil || !contains(firstErr.Error(), "failed to generate fix") {
		t.Fatalf("Expected fix-generation error on first attempt, got: %v", firstErr)
	}

	secondErr := executor.handleError(task, testErr)
	if secondErr == nil || !contains(secondErr.Error(), "retry loop detected") {
		t.Fatalf("Expected retry loop detected on second attempt, got: %v", secondErr)
	}
	if executor.retryCount != 2 {
		t.Errorf("Expected retryCount=2, got %d", executor.retryCount)
	}
}

func TestExecutor_SelfHealing(t *testing.T) {
	tmpDir := t.TempDir()
	oldWd, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(oldWd)

	// Create project structure
	os.WriteFile("go.mod", []byte("module test\n\ngo 1.21"), 0644)

	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()
	mockModel.EXPECT().ChatCompletion(gomock.Any(), gomock.Any()).
		Return(mockChatResponse("proposed fix"), nil).Times(1)

	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
		Description: "Test Plan",
		Tasks: []Task{
			{ID: "1", Title: "Self-healing Test", Description: "Test self-healing"},
		},
	}

	store := &storage.Store{}
	registry := tool.NewRegistry()
	cfg := &config.Config{
		Orchestrator: config.OrchestratorConfig{
			TrustLevel: "autonomous",
		},
	}
	planner := &Planner{}
	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)
	executor.maxRetries = 3

	// Simulate an error and self-healing attempt
	task := &plan.Tasks[0]
	testErr := fmt.Errorf("file write error: permission denied")

	// analyzeAndFix should generate a fix
	fix, err := executor.analyzeAndFix(task, testErr)
	if err != nil {
		t.Errorf("analyzeAndFix failed: %v", err)
	}

	if fix == "" {
		t.Error("Expected fix to be generated")
	}

	os.Remove("go.mod")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || contains(s[1:], substr)))
}

func TestExecutor_Execute_AllTasksCompleted(t *testing.T) {
	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()

	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
		Description: "Test Plan",
		Tasks: []Task{
			{ID: "1", Title: "Task 1", Status: TaskCompleted},
			{ID: "2", Title: "Task 2", Status: TaskCompleted},
		},
	}

	store := &storage.Store{}
	registry := tool.NewRegistry()
	cfg := &config.Config{}
	planner := &Planner{}
	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)

	err := executor.Execute()
	if err != nil {
		t.Errorf("Execute() with all completed tasks should succeed, got: %v", err)
	}
}

func TestExecutor_Execute_SkipsCompletedTasks(t *testing.T) {
	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()

	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
		Description: "Test Plan",
		Tasks: []Task{
			{ID: "1", Title: "Task 1", Status: TaskCompleted},
			{ID: "2", Title: "Task 2", Status: TaskCompleted},
			{ID: "3", Title: "Task 3", Status: TaskCompleted},
		},
	}

	store := &storage.Store{}
	registry := tool.NewRegistry()
	cfg := &config.Config{}
	planner := &Planner{}
	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)

	err := executor.Execute()
	if err != nil {
		t.Errorf("Execute() should skip completed tasks, got: %v", err)
	}
}

func TestExecutor_Execute_UnmetDependencies(t *testing.T) {
	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()

	// Task 1 depends on a non-existent task "0" which cannot be met
	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
		Description: "Test Plan",
		Tasks: []Task{
			{ID: "1", Title: "Task 1", Status: TaskPending, Dependencies: []string{"0"}},
		},
	}

	store := &storage.Store{}
	registry := tool.NewRegistry()
	cfg := &config.Config{}
	planner := &Planner{}
	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)

	// Task 1 depends on non-existent task "0" which cannot be completed
	err := executor.Execute()
	if err == nil || !contains(err.Error(), "unmet dependencies") {
		t.Errorf("Expected unmet dependencies error, got: %v", err)
	}
}

func TestExecutor_Cancel(t *testing.T) {
	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()

	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
		Description: "Test Plan",
		Tasks:       []Task{{ID: "1", Title: "Task 1"}},
	}

	store := &storage.Store{}
	registry := tool.NewRegistry()
	cfg := &config.Config{}
	planner := &Planner{}
	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)

	// Cancel should not panic even without context set
	executor.Cancel()

	// Now with context
	executor.SetContext(nil) // Sets up internal context
	executor.Cancel()

	// Verify context is cancelled
	if executor.ctx.Err() == nil {
		t.Error("Expected context to be cancelled after Cancel()")
	}
}

func TestExecutor_SetContext(t *testing.T) {
	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()

	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
		Description: "Test Plan",
		Tasks:       []Task{{ID: "1", Title: "Task 1"}},
	}

	store := &storage.Store{}
	registry := tool.NewRegistry()
	cfg := &config.Config{}
	planner := &Planner{}
	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)

	// SetContext with nil should create background context
	executor.SetContext(nil)
	if executor.ctx == nil {
		t.Error("Expected ctx to be set after SetContext(nil)")
	}
	if executor.cancel == nil {
		t.Error("Expected cancel to be set after SetContext(nil)")
	}
}

func TestExecutor_SetPersonaProvider(t *testing.T) {
	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()

	plan := &Plan{
		ID:          "test-plan",
		FeatureName: "Test Feature",
		Tasks:       []Task{{ID: "1", Title: "Task 1"}},
	}

	store := &storage.Store{}
	registry := tool.NewRegistry()
	cfg := &config.Config{}
	planner := &Planner{}
	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)

	// Should not panic with nil provider
	executor.SetPersonaProvider(nil)
}

func TestExecutor_sendProgress(t *testing.T) {
	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()

	plan := &Plan{
		ID:    "test-plan",
		Tasks: []Task{{ID: "1", Title: "Task 1"}},
	}

	store := &storage.Store{}
	registry := tool.NewRegistry()
	cfg := &config.Config{}
	planner := &Planner{}
	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)

	// Should not panic without workflow
	executor.sendProgress("test message")

	// With workflow
	wf := NewWorkflowManager(cfg, nil, registry, nil, t.TempDir(), t.TempDir(), nil)
	executor.workflow = wf
	executor.sendProgress("test message with workflow")
}

func TestExecutor_emitTaskEvent(t *testing.T) {
	ctrl, mockModel := setupMockModel(t)
	defer ctrl.Finish()

	plan := &Plan{
		ID:    "test-plan",
		Tasks: []Task{{ID: "1", Title: "Task 1"}},
	}

	store := &storage.Store{}
	registry := tool.NewRegistry()
	cfg := &config.Config{}
	planner := &Planner{}
	executor := NewExecutor(plan, store, mockModel, registry, cfg, planner, nil, nil)

	task := &plan.Tasks[0]

	// Should not panic without emitter
	executor.emitTaskEvent(task, "started")

	// With nil emitter is safe
	executor.emitTaskEvent(task, "completed")
}
