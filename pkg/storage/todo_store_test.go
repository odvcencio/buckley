package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func TestTodoStoreCRUD(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "todo.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sessionID := "todo-session"
	if err := store.EnsureSession(sessionID); err != nil {
		t.Fatalf("ensure session: %v", err)
	}

	now := time.Now()
	todo := &Todo{
		SessionID:  sessionID,
		Content:    "Implement feature",
		ActiveForm: "Implement feature",
		Status:     "pending",
		OrderIndex: 1,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := store.CreateTodo(todo); err != nil {
		t.Fatalf("create todo: %v", err)
	}
	if todo.ID == 0 {
		t.Fatalf("expected todo ID to be assigned")
	}

	active, err := store.GetActiveTodo(sessionID)
	if err != nil {
		t.Fatalf("get active todo: %v", err)
	}
	if active != nil {
		t.Fatalf("expected no active todo yet, got %+v", active)
	}

	if err := store.UpdateTodoStatus(todo.ID, "in_progress", ""); err != nil {
		t.Fatalf("update todo status: %v", err)
	}

	active, err = store.GetActiveTodo(sessionID)
	if err != nil {
		t.Fatalf("get active todo after status update: %v", err)
	}
	if active == nil || active.ID != todo.ID {
		t.Fatalf("expected todo to be active, got %+v", active)
	}

	todos, err := store.GetTodos(sessionID)
	if err != nil {
		t.Fatalf("get todos: %v", err)
	}
	if len(todos) != 1 || todos[0].Status != "in_progress" {
		t.Fatalf("expected single in-progress todo, got %+v", todos)
	}

	checkpoint := &TodoCheckpoint{
		SessionID:           sessionID,
		CheckpointType:      "auto",
		TodoCount:           1,
		CompletedCount:      0,
		ConversationSummary: "summary",
		ConversationTokens:  123,
		CreatedAt:           time.Now(),
		Metadata:            "{}",
	}
	if err := store.CreateCheckpoint(checkpoint); err != nil {
		t.Fatalf("create checkpoint: %v", err)
	}
	latest, err := store.GetLatestCheckpoint(sessionID)
	if err != nil {
		t.Fatalf("get latest checkpoint: %v", err)
	}
	if latest == nil || latest.ID == 0 {
		t.Fatalf("expected checkpoint to exist, got %+v", latest)
	}

	if err := store.DeleteTodos(sessionID); err != nil {
		t.Fatalf("delete todos: %v", err)
	}
	todos, err = store.GetTodos(sessionID)
	if err != nil {
		t.Fatalf("get todos after delete: %v", err)
	}
	if len(todos) != 0 {
		t.Fatalf("expected all todos deleted, got %+v", todos)
	}
}

func TestGetTodoSummary(t *testing.T) {
	dir := t.TempDir()
	store, err := New(filepath.Join(dir, "summary.db"))
	if err != nil {
		t.Fatalf("failed to init store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	sessionID := "summary-session"
	if err := store.EnsureSession(sessionID); err != nil {
		t.Fatalf("ensure session: %v", err)
	}

	// Test empty summary
	summary, err := store.GetTodoSummary(sessionID)
	if err != nil {
		t.Fatalf("get empty summary: %v", err)
	}
	if summary.Total != 0 || summary.Completed != 0 || summary.Pending != 0 || summary.Failed != 0 {
		t.Errorf("expected all zeros for empty session, got %+v", summary)
	}

	// Create some todos with different statuses
	now := time.Now()
	todos := []Todo{
		{SessionID: sessionID, Content: "Task 1", ActiveForm: "Doing task 1", Status: "completed", OrderIndex: 1, CreatedAt: now, UpdatedAt: now},
		{SessionID: sessionID, Content: "Task 2", ActiveForm: "Doing task 2", Status: "completed", OrderIndex: 2, CreatedAt: now, UpdatedAt: now},
		{SessionID: sessionID, Content: "Task 3", ActiveForm: "Doing task 3", Status: "pending", OrderIndex: 3, CreatedAt: now, UpdatedAt: now},
		{SessionID: sessionID, Content: "Task 4", ActiveForm: "Doing task 4", Status: "in_progress", OrderIndex: 4, CreatedAt: now, UpdatedAt: now},
		{SessionID: sessionID, Content: "Task 5", ActiveForm: "Doing task 5", Status: "failed", OrderIndex: 5, CreatedAt: now, UpdatedAt: now},
	}
	for i := range todos {
		if err := store.CreateTodo(&todos[i]); err != nil {
			t.Fatalf("create todo %d: %v", i, err)
		}
	}

	// Get summary
	summary, err = store.GetTodoSummary(sessionID)
	if err != nil {
		t.Fatalf("get summary: %v", err)
	}

	if summary.Total != 5 {
		t.Errorf("expected total 5, got %d", summary.Total)
	}
	if summary.Completed != 2 {
		t.Errorf("expected completed 2, got %d", summary.Completed)
	}
	if summary.Pending != 2 { // pending + in_progress
		t.Errorf("expected pending 2, got %d", summary.Pending)
	}
	if summary.Failed != 1 {
		t.Errorf("expected failed 1, got %d", summary.Failed)
	}
}
