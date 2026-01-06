package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// mockTodoStoreForPlanning implements builtin.TodoStore for testing
type mockTodoStoreForPlanning struct {
	todos       []builtin.TodoItem
	ensureErr   error
	createErr   error
	updateErr   error
	getErr      error
	deleteErr   error
	saveChkErr  error
	getChkErr   error
	checkpoints []builtin.TodoCheckpointData
	nextID      int64
}

func (m *mockTodoStoreForPlanning) EnsureSession(sessionID string) error {
	return m.ensureErr
}

func (m *mockTodoStoreForPlanning) CreateTodo(todo *builtin.TodoItem) error {
	m.nextID++
	todo.ID = m.nextID
	todo.CreatedAt = time.Now()
	todo.UpdatedAt = time.Now()
	m.todos = append(m.todos, *todo)
	return m.createErr
}

func (m *mockTodoStoreForPlanning) UpdateTodoStatus(id int64, status, errorMsg string) error {
	for i := range m.todos {
		if m.todos[i].ID == id {
			m.todos[i].Status = status
			m.todos[i].ErrorMessage = errorMsg
			m.todos[i].UpdatedAt = time.Now()
		}
	}
	return m.updateErr
}

func (m *mockTodoStoreForPlanning) GetTodos(sessionID string) ([]builtin.TodoItem, error) {
	return m.todos, m.getErr
}

func (m *mockTodoStoreForPlanning) DeleteTodos(sessionID string) error {
	m.todos = nil
	return m.deleteErr
}

func (m *mockTodoStoreForPlanning) GetActiveTodo(sessionID string) (*builtin.TodoItem, error) {
	for _, t := range m.todos {
		if t.Status == "in_progress" {
			return &t, nil
		}
	}
	return nil, nil
}

func (m *mockTodoStoreForPlanning) CreateCheckpoint(checkpoint *builtin.TodoCheckpointData) error {
	m.checkpoints = append(m.checkpoints, *checkpoint)
	return m.saveChkErr
}

func (m *mockTodoStoreForPlanning) GetLatestCheckpoint(sessionID string) (*builtin.TodoCheckpointData, error) {
	if len(m.checkpoints) == 0 {
		return nil, m.getChkErr
	}
	return &m.checkpoints[len(m.checkpoints)-1], m.getChkErr
}

// mockPlanningLLMClient implements builtin.PlanningClient for testing
type mockPlanningLLMClient struct {
	response *model.ChatResponse
	err      error
}

func (m *mockPlanningLLMClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	return m.response, m.err
}

func TestNewPlanningCoordinator(t *testing.T) {
	cfg := &config.PlanningConfig{
		Enabled:             true,
		ComplexityThreshold: 0.5,
		PlanningModel:       "test-model",
	}

	mockStore := &mockTodoStoreForPlanning{}
	mockLLM := &mockPlanningLLMClient{}

	todoTool := &builtin.TodoTool{
		Store:     mockStore,
		LLMClient: mockLLM,
	}

	coord := NewPlanningCoordinator(cfg, todoTool, nil)

	if coord == nil {
		t.Fatal("Expected non-nil coordinator")
	}

	if coord.complexityDetect == nil {
		t.Error("Expected complexity detector to be initialized")
	}

	if coord.complexityDetect.Threshold != 0.5 {
		t.Errorf("Expected threshold 0.5, got %f", coord.complexityDetect.Threshold)
	}
}

func TestPlanningCoordinator_SetSession(t *testing.T) {
	cfg := &config.PlanningConfig{Enabled: true}
	coord := NewPlanningCoordinator(cfg, nil, nil)

	coord.SetSession("test-session-123")

	if coord.currentSession != "test-session-123" {
		t.Errorf("Expected session 'test-session-123', got '%s'", coord.currentSession)
	}

	if coord.decisionLog.sessionID != "test-session-123" {
		t.Errorf("Expected decision log session 'test-session-123', got '%s'", coord.decisionLog.sessionID)
	}
}

func TestPlanningCoordinator_AnalyzeComplexity_Disabled(t *testing.T) {
	cfg := &config.PlanningConfig{Enabled: false}
	coord := NewPlanningCoordinator(cfg, nil, nil)

	signal := coord.AnalyzeComplexity("complex refactoring task", nil)

	if signal.Recommended != DirectExecution {
		t.Error("Expected DirectExecution when planning is disabled")
	}

	if signal.Score != 0 {
		t.Errorf("Expected score 0 when disabled, got %f", signal.Score)
	}
}

func TestPlanningCoordinator_AnalyzeComplexity_Enabled(t *testing.T) {
	cfg := &config.PlanningConfig{
		Enabled:             true,
		ComplexityThreshold: 0.5,
	}
	coord := NewPlanningCoordinator(cfg, nil, nil)

	// Simple task
	signal := coord.AnalyzeComplexity("fix typo", nil)
	if signal.Recommended != DirectExecution {
		t.Error("Expected DirectExecution for simple task")
	}

	// Complex task - using a more clearly complex input
	signal = coord.AnalyzeComplexity("I need to refactor the authentication system to use JWT tokens. This involves multiple files across the codebase.", nil)
	if signal.Recommended != PlanningMode {
		t.Errorf("Expected PlanningMode for complex task, got score %.2f with reasons: %v", signal.Score, signal.Reasons)
	}
}

func TestPlanningCoordinator_ShouldPlan(t *testing.T) {
	cfg := &config.PlanningConfig{
		Enabled:             true,
		ComplexityThreshold: 0.5,
	}
	coord := NewPlanningCoordinator(cfg, nil, nil)

	if coord.ShouldPlan("fix typo", nil) {
		t.Error("Should not plan for simple task")
	}

	if !coord.ShouldPlan("I need to refactor the authentication system to use JWT tokens. This involves multiple files across the codebase.", nil) {
		t.Error("Should plan for complex task")
	}
}

func TestPlanningCoordinator_IsAwaitingUser(t *testing.T) {
	cfg := &config.PlanningConfig{Enabled: true}
	coord := NewPlanningCoordinator(cfg, nil, nil)

	if coord.IsAwaitingUser() {
		t.Error("Should not be awaiting user initially")
	}

	coord.awaitingUser = true
	if !coord.IsAwaitingUser() {
		t.Error("Should be awaiting user after setting flag")
	}
}

func TestPlanningCoordinator_GetPendingResult(t *testing.T) {
	cfg := &config.PlanningConfig{Enabled: true}
	coord := NewPlanningCoordinator(cfg, nil, nil)

	if coord.GetPendingResult() != nil {
		t.Error("Should have no pending result initially")
	}

	expected := &PlanningResult{Phase: PhaseAwaitingSelection}
	coord.pendingResult = expected

	result := coord.GetPendingResult()
	if result != expected {
		t.Error("Should return the pending result")
	}
}

func TestPlanningCoordinator_GetDecisions_Empty(t *testing.T) {
	cfg := &config.PlanningConfig{Enabled: true}
	coord := NewPlanningCoordinator(cfg, nil, nil)

	decisions := coord.GetDecisions()
	if len(decisions) != 0 {
		t.Errorf("Expected 0 decisions, got %d", len(decisions))
	}
}

func TestPlanningCoordinator_hasCleanWinner(t *testing.T) {
	cfg := &config.PlanningConfig{Enabled: true}
	coord := NewPlanningCoordinator(cfg, nil, nil)

	tests := []struct {
		name        string
		approaches  []builtin.Approach
		recommended int
		expected    bool
	}{
		{
			name:        "single approach",
			approaches:  []builtin.Approach{{Name: "A", Risk: "medium"}},
			recommended: 0,
			expected:    true,
		},
		{
			name: "low risk recommended",
			approaches: []builtin.Approach{
				{Name: "A", Risk: "medium", Steps: 5},
				{Name: "B", Risk: "low", Steps: 3},
			},
			recommended: 1,
			expected:    true,
		},
		{
			name: "high risk recommended with low risk alternative",
			approaches: []builtin.Approach{
				{Name: "A", Risk: "low", Steps: 5},
				{Name: "B", Risk: "high", Steps: 2},
			},
			recommended: 1,
			expected:    false,
		},
		{
			name: "fewer steps alternative",
			approaches: []builtin.Approach{
				{Name: "A", Risk: "medium", Steps: 10},
				{Name: "B", Risk: "medium", Steps: 3},
			},
			recommended: 0,
			expected:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := coord.hasCleanWinner(tt.approaches, tt.recommended)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestFormatApproachesDisplay(t *testing.T) {
	approaches := []builtin.Approach{
		{
			Name:        "Quick Fix",
			Description: "A simple solution",
			Steps:       3,
			Risk:        "low",
			Tradeoffs:   []string{"May need revisiting"},
		},
		{
			Name:        "Full Refactor",
			Description: "Complete rewrite",
			Steps:       10,
			Risk:        "high",
			Tradeoffs:   []string{"Time consuming", "Higher risk"},
		},
	}

	result := FormatApproachesDisplay(approaches, 0, "Quick fix is recommended for speed")

	if result == "" {
		t.Error("Expected non-empty display")
	}

	if !stringContains(result, "Planning Analysis") {
		t.Error("Expected 'Planning Analysis' in output")
	}

	if !stringContains(result, "Quick Fix") {
		t.Error("Expected 'Quick Fix' in output")
	}

	if !stringContains(result, "Full Refactor") {
		t.Error("Expected 'Full Refactor' in output")
	}

	if !stringContains(result, "Recommendation") {
		t.Error("Expected 'Recommendation' in output")
	}
}

func TestFormatApproachesDisplay_Empty(t *testing.T) {
	result := FormatApproachesDisplay(nil, 0, "")
	if result != "No approaches generated" {
		t.Errorf("Expected 'No approaches generated', got '%s'", result)
	}
}

func TestFormatTodosDisplay(t *testing.T) {
	todos := []builtin.TodoInput{
		{Content: "Step 1: Initialize", ActiveForm: "Initializing"},
		{Content: "Step 2: Implement", ActiveForm: "Implementing"},
	}

	result := FormatTodosDisplay(todos, "Quick Fix")

	if result == "" {
		t.Error("Expected non-empty display")
	}

	if !stringContains(result, "Quick Fix") {
		t.Error("Expected approach name in output")
	}

	if !stringContains(result, "Step 1") {
		t.Error("Expected step 1 in output")
	}

	if !stringContains(result, "Step 2") {
		t.Error("Expected step 2 in output")
	}
}

func TestFormatTodosDisplay_Empty(t *testing.T) {
	result := FormatTodosDisplay(nil, "Test")
	if result != "No TODOs generated" {
		t.Errorf("Expected 'No TODOs generated', got '%s'", result)
	}
}

// stringContains is a helper to avoid redeclaration
func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestPlanningPhaseConstants(t *testing.T) {
	// Verify phase constants are distinct
	phases := []PlanningPhase{
		PhaseIdle,
		PhaseBrainstorming,
		PhaseAwaitingSelection,
		PhaseRefining,
		PhaseAwaitingConfirmation,
		PhaseCommitting,
		PhaseComplete,
	}

	seen := make(map[PlanningPhase]bool)
	for _, p := range phases {
		if seen[p] {
			t.Errorf("Duplicate phase value: %d", p)
		}
		seen[p] = true
	}
}

func TestComplexityModeConstants(t *testing.T) {
	if DirectExecution == PlanningMode {
		t.Error("DirectExecution and PlanningMode should be different")
	}
}
