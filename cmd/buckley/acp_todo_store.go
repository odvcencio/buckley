package main

import (
	"sync"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// acpTodoStoreAdapter is an in-memory todo store for ACP sessions.
// Todo items are ephemeral per-session since ACP sessions are short-lived.
type acpTodoStoreAdapter struct {
	sessionID string
	mu        sync.Mutex
	todos     []builtin.TodoItem
	nextID    int64
}

func (a *acpTodoStoreAdapter) CreateTodo(todo *builtin.TodoItem) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextID++
	todo.ID = a.nextID
	todo.SessionID = a.sessionID
	a.todos = append(a.todos, *todo)
	return nil
}

func (a *acpTodoStoreAdapter) UpdateTodoStatus(id int64, status string, errorMessage string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.todos {
		if a.todos[i].ID == id {
			a.todos[i].Status = status
			a.todos[i].ErrorMessage = errorMessage
			return nil
		}
	}
	return nil
}

func (a *acpTodoStoreAdapter) GetTodos(sessionID string) ([]builtin.TodoItem, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if sessionID != a.sessionID {
		return nil, nil
	}
	result := make([]builtin.TodoItem, len(a.todos))
	copy(result, a.todos)
	return result, nil
}

func (a *acpTodoStoreAdapter) GetActiveTodo(sessionID string) (*builtin.TodoItem, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if sessionID != a.sessionID {
		return nil, nil
	}
	for i := range a.todos {
		if a.todos[i].Status == "in_progress" {
			return &a.todos[i], nil
		}
	}
	return nil, nil
}

func (a *acpTodoStoreAdapter) DeleteTodos(sessionID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if sessionID == a.sessionID {
		a.todos = nil
	}
	return nil
}

func (a *acpTodoStoreAdapter) CreateCheckpoint(checkpoint *builtin.TodoCheckpointData) error {
	return nil
}

func (a *acpTodoStoreAdapter) GetLatestCheckpoint(sessionID string) (*builtin.TodoCheckpointData, error) {
	return nil, nil
}

func (a *acpTodoStoreAdapter) EnsureSession(sessionID string) error {
	return nil
}
