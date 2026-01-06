package builtin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"go.uber.org/mock/gomock"
)

func TestTodoTool_Name(t *testing.T) {
	tool := &TodoTool{}
	if got := tool.Name(); got != "todo" {
		t.Errorf("Name() = %s, want todo", got)
	}
}

func TestTodoTool_Description(t *testing.T) {
	tool := &TodoTool{}
	desc := tool.Description()
	if desc == "" {
		t.Error("Description() returned empty string")
	}
}

func TestTodoTool_Parameters(t *testing.T) {
	tool := &TodoTool{}
	params := tool.Parameters()
	if params.Type != "object" {
		t.Errorf("Parameters().Type = %s, want object", params.Type)
	}
	if len(params.Required) != 2 {
		t.Errorf("Parameters().Required length = %d, want 2", len(params.Required))
	}
}

func TestTodoTool_Execute_NoStore(t *testing.T) {
	tool := &TodoTool{Store: nil}
	result, err := tool.Execute(map[string]any{
		"action":     "list",
		"session_id": "test",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail when store is nil")
	}
	if result.Error != "TODO store not initialized" {
		t.Errorf("Execute() error = %s, want 'TODO store not initialized'", result.Error)
	}
}

func TestTodoTool_Execute_MissingAction(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	result, err := tool.Execute(map[string]any{
		"session_id": "test",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail when action is missing")
	}
}

func TestTodoTool_Execute_MissingSessionID(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	result, err := tool.Execute(map[string]any{
		"action": "list",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail when session_id is missing")
	}
}

func TestTodoTool_Execute_UnknownAction(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	result, err := tool.Execute(map[string]any{
		"action":     "unknown_action",
		"session_id": "test",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail for unknown action")
	}
}

func TestTodoTool_Execute_Create_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	sessionID := "test-session"
	todos := []map[string]any{
		{
			"content":    "Implement feature",
			"activeForm": "Implementing feature",
			"status":     "pending",
		},
		{
			"content":    "Write tests",
			"activeForm": "Writing tests",
			"status":     "pending",
		},
	}

	// Expect session creation, deletion, and two todo creations
	mockStore.EXPECT().EnsureSession(sessionID).Return(nil)
	mockStore.EXPECT().DeleteTodos(sessionID).Return(nil)
	mockStore.EXPECT().CreateTodo(gomock.Any()).Return(nil).Times(2)

	result, err := tool.Execute(map[string]any{
		"action":     "create",
		"session_id": sessionID,
		"todos":      todos,
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}
}

func TestTodoTool_Execute_Create_EnsureSessionFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	sessionID := "test-session"
	mockStore.EXPECT().EnsureSession(sessionID).Return(errors.New("database error"))

	result, err := tool.Execute(map[string]any{
		"action":     "create",
		"session_id": sessionID,
		"todos":      []map[string]any{},
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail when EnsureSession fails")
	}
}

func TestTodoTool_Execute_Create_DeleteFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	sessionID := "test-session"
	mockStore.EXPECT().EnsureSession(sessionID).Return(nil)
	mockStore.EXPECT().DeleteTodos(sessionID).Return(errors.New("delete failed"))

	result, err := tool.Execute(map[string]any{
		"action":     "create",
		"session_id": sessionID,
		"todos":      []map[string]any{},
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail when DeleteTodos fails")
	}
}

func TestTodoTool_Execute_Update_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	todoID := int64(42)
	mockStore.EXPECT().UpdateTodoStatus(todoID, "completed", "").Return(nil)

	result, err := tool.Execute(map[string]any{
		"action":     "update",
		"session_id": "test",
		"todo_id":    float64(todoID), // JSON unmarshals numbers as float64
		"status":     "completed",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}
}

func TestTodoTool_Execute_Update_WithError(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	todoID := int64(42)
	errorMsg := "test failed"
	mockStore.EXPECT().UpdateTodoStatus(todoID, "failed", errorMsg).Return(nil)

	result, err := tool.Execute(map[string]any{
		"action":        "update",
		"session_id":    "test",
		"todo_id":       float64(todoID),
		"status":        "failed",
		"error_message": errorMsg,
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}
}

func TestTodoTool_Execute_List_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	sessionID := "test-session"
	now := time.Now()
	todos := []TodoItem{
		{
			ID:         1,
			SessionID:  sessionID,
			Content:    "Task 1",
			ActiveForm: "Doing task 1",
			Status:     "pending",
			OrderIndex: 0,
			CreatedAt:  now,
			UpdatedAt:  now,
		},
		{
			ID:         2,
			SessionID:  sessionID,
			Content:    "Task 2",
			ActiveForm: "Doing task 2",
			Status:     "in_progress",
			OrderIndex: 1,
			CreatedAt:  now,
			UpdatedAt:  now,
		},
	}

	mockStore.EXPECT().GetTodos(sessionID).Return(todos, nil)

	result, err := tool.Execute(map[string]any{
		"action":     "list",
		"session_id": sessionID,
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}
	if count, ok := result.Data["count"].(int); !ok || count != 2 {
		t.Errorf("Expected count=2, got %v", result.Data["count"])
	}
}

func TestTodoTool_Execute_GetActive_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	sessionID := "test-session"
	now := time.Now()
	activeTodo := &TodoItem{
		ID:         1,
		SessionID:  sessionID,
		Content:    "Active task",
		ActiveForm: "Doing active task",
		Status:     "in_progress",
		OrderIndex: 0,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	mockStore.EXPECT().GetActiveTodo(sessionID).Return(activeTodo, nil)

	result, err := tool.Execute(map[string]any{
		"action":     "get_active",
		"session_id": sessionID,
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}
	if active := result.Data["active"]; active == nil {
		t.Error("Expected active todo, got nil")
	}
}

func TestTodoTool_Execute_GetActive_NoActive(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	sessionID := "test-session"
	mockStore.EXPECT().GetActiveTodo(sessionID).Return(nil, nil)

	result, err := tool.Execute(map[string]any{
		"action":     "get_active",
		"session_id": sessionID,
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}
	if active := result.Data["active"]; active != nil {
		t.Errorf("Expected no active todo, got %v", active)
	}
}

func TestTodoTool_Execute_Clear_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	sessionID := "test-session"
	mockStore.EXPECT().DeleteTodos(sessionID).Return(nil)

	result, err := tool.Execute(map[string]any{
		"action":     "clear",
		"session_id": sessionID,
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}
}

func TestTodoTool_Execute_Checkpoint_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	sessionID := "test-session"

	// Mock GetTodos to return some todos for counting
	todos := []TodoItem{
		{Status: "completed"},
		{Status: "completed"},
		{Status: "pending"},
	}
	mockStore.EXPECT().GetTodos(sessionID).Return(todos, nil)
	mockStore.EXPECT().CreateCheckpoint(gomock.Any()).Return(nil)

	result, err := tool.Execute(map[string]any{
		"action":               "checkpoint",
		"session_id":           sessionID,
		"checkpoint_type":      "manual",
		"conversation_summary": "Progress update",
		"conversation_tokens":  1500,
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}
}

func TestTodoTool_Execute_Checkpoint_GetTodosFails(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	sessionID := "test-session"
	mockStore.EXPECT().GetTodos(sessionID).Return(nil, errors.New("database error"))

	result, err := tool.Execute(map[string]any{
		"action":          "checkpoint",
		"session_id":      sessionID,
		"checkpoint_type": "auto",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail when GetTodos fails")
	}
}

// Tests for new planning actions

func TestTodoTool_Execute_Brainstorm_NoLLMClient(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore, LLMClient: nil}

	result, err := tool.Execute(map[string]any{
		"action":     "brainstorm",
		"session_id": "test-session",
		"task":       "Add dark mode",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail when LLMClient is nil")
	}
	if result.Error != "LLM client not configured - brainstorm action requires planning capabilities" {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

func TestTodoTool_Execute_Brainstorm_MissingTask(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	mockLLM := &mockPlanningClient{}
	tool := &TodoTool{Store: mockStore, LLMClient: mockLLM}

	result, err := tool.Execute(map[string]any{
		"action":     "brainstorm",
		"session_id": "test-session",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail when task is missing")
	}
	if result.Error != "task parameter is required for brainstorm action" {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

func TestTodoTool_Execute_Refine_NoLLMClient(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore, LLMClient: nil}

	result, err := tool.Execute(map[string]any{
		"action":     "refine",
		"session_id": "test-session",
		"approaches": []map[string]any{
			{"name": "Test", "description": "Test approach", "steps": 3, "risk": "low"},
		},
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail when LLMClient is nil")
	}
}

func TestTodoTool_Execute_Refine_MissingApproaches(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	mockLLM := &mockPlanningClient{}
	tool := &TodoTool{Store: mockStore, LLMClient: mockLLM}

	result, err := tool.Execute(map[string]any{
		"action":     "refine",
		"session_id": "test-session",
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail when approaches is missing")
	}
	if result.Error != "approaches parameter is required for refine action" {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

func TestTodoTool_Execute_Commit_Success(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	sessionID := "test-session"
	mockStore.EXPECT().EnsureSession(sessionID).Return(nil)
	mockStore.EXPECT().DeleteTodos(sessionID).Return(nil)
	mockStore.EXPECT().CreateTodo(gomock.Any()).DoAndReturn(func(todo *TodoItem) error {
		todo.ID = 1
		return nil
	}).Times(2)

	result, err := tool.Execute(map[string]any{
		"action":     "commit",
		"session_id": sessionID,
		"todos": []map[string]any{
			{"content": "First task", "activeForm": "Doing first task", "status": "pending"},
			{"content": "Second task", "activeForm": "Doing second task", "status": "pending"},
		},
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Errorf("Execute() failed: %s", result.Error)
	}
	if result.Data["count"] != 2 {
		t.Errorf("Expected 2 todos created, got %v", result.Data["count"])
	}
}

func TestTodoTool_Execute_Commit_MissingTodos(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockStore := NewMockTodoStore(ctrl)
	tool := &TodoTool{Store: mockStore}

	sessionID := "test-session"
	mockStore.EXPECT().EnsureSession(sessionID).Return(nil)
	mockStore.EXPECT().DeleteTodos(sessionID).Return(nil)

	result, err := tool.Execute(map[string]any{
		"action":     "commit",
		"session_id": sessionID,
	})

	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Success {
		t.Error("Execute() should fail when todos is missing")
	}
	if result.Error != "todos parameter is required for commit action" {
		t.Errorf("unexpected error: %s", result.Error)
	}
}

// Helper tests

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain JSON",
			input:    `{"key": "value"}`,
			expected: `{"key": "value"}`,
		},
		{
			name:     "JSON in markdown code block",
			input:    "```json\n{\"key\": \"value\"}\n```",
			expected: `{"key": "value"}`,
		},
		{
			name:     "JSON in generic code block",
			input:    "```\n{\"key\": \"value\"}\n```",
			expected: `{"key": "value"}`,
		},
		{
			name:     "with whitespace",
			input:    "  \n{\"key\": \"value\"}\n  ",
			expected: `{"key": "value"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.expected {
				t.Errorf("extractJSON() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestToActiveForm(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Add feature", "Adding feature"},
		{"Create file", "Creating file"},
		{"Update config", "Updating config"},
		{"Fix bug", "Fixing bug"},
		{"Remove code", "Removing code"},
		{"Implement API", "Implementing API"},
		{"Write tests", "Writing tests"},
		{"Test function", "Testing function"},
		{"Refactor module", "Refactoring module"},
		{"Configure server", "Configuring server"},
		{"Check status", "Checking status"},
		{"Parse data", "Parsing data"}, // Default: remove 'e' and add 'ing'
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := toActiveForm(tt.input)
			if got != tt.expected {
				t.Errorf("toActiveForm(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"this is a long string", 10, "this is..."},
		{"exactly10!", 10, "exactly10!"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := truncate(tt.input, tt.maxLen)
			if got != tt.expected {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.expected)
			}
		})
	}
}

// Mock PlanningClient for testing
type mockPlanningClient struct {
	response *mockChatResponse
	err      error
}

type mockChatResponse struct {
	content string
}

func (m *mockPlanningClient) ChatCompletion(ctx context.Context, req model.ChatRequest) (*model.ChatResponse, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.response == nil {
		return &model.ChatResponse{
			Choices: []model.Choice{
				{Message: model.Message{Content: "{}"}},
			},
		}, nil
	}
	return &model.ChatResponse{
		Choices: []model.Choice{
			{Message: model.Message{Content: m.response.content}},
		},
	}, nil
}
