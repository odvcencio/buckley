package tui

import (
	"time"

	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// todoStoreAdapter adapts storage.Store to the builtin.TodoStore interface.
type todoStoreAdapter struct {
	store *storage.Store
}

func (a *todoStoreAdapter) CreateTodo(todo *builtin.TodoItem) error {
	storageTodo := &storage.Todo{
		ID:           todo.ID,
		SessionID:    todo.SessionID,
		Content:      todo.Content,
		ActiveForm:   todo.ActiveForm,
		Status:       todo.Status,
		OrderIndex:   todo.OrderIndex,
		ParentID:     todo.ParentID,
		CreatedAt:    todo.CreatedAt,
		UpdatedAt:    todo.UpdatedAt,
		CompletedAt:  todo.CompletedAt,
		ErrorMessage: todo.ErrorMessage,
		Metadata:     todo.Metadata,
	}

	if err := a.store.CreateTodo(storageTodo); err != nil {
		return err
	}

	todo.ID = storageTodo.ID
	return nil
}

func (a *todoStoreAdapter) UpdateTodoStatus(id int64, status string, errorMessage string) error {
	return a.store.UpdateTodoStatus(id, status, errorMessage)
}

func (a *todoStoreAdapter) GetTodos(sessionID string) ([]builtin.TodoItem, error) {
	storageTodos, err := a.store.GetTodos(sessionID)
	if err != nil {
		return nil, err
	}

	todos := make([]builtin.TodoItem, len(storageTodos))
	for i, st := range storageTodos {
		todos[i] = builtin.TodoItem{
			ID:           st.ID,
			SessionID:    st.SessionID,
			Content:      st.Content,
			ActiveForm:   st.ActiveForm,
			Status:       st.Status,
			OrderIndex:   st.OrderIndex,
			ParentID:     st.ParentID,
			CreatedAt:    st.CreatedAt,
			UpdatedAt:    st.UpdatedAt,
			CompletedAt:  st.CompletedAt,
			ErrorMessage: st.ErrorMessage,
			Metadata:     st.Metadata,
		}
	}

	return todos, nil
}

func (a *todoStoreAdapter) GetActiveTodo(sessionID string) (*builtin.TodoItem, error) {
	storageTodo, err := a.store.GetActiveTodo(sessionID)
	if err != nil || storageTodo == nil {
		return nil, err
	}

	return &builtin.TodoItem{
		ID:           storageTodo.ID,
		SessionID:    storageTodo.SessionID,
		Content:      storageTodo.Content,
		ActiveForm:   storageTodo.ActiveForm,
		Status:       storageTodo.Status,
		OrderIndex:   storageTodo.OrderIndex,
		ParentID:     storageTodo.ParentID,
		CreatedAt:    storageTodo.CreatedAt,
		UpdatedAt:    storageTodo.UpdatedAt,
		CompletedAt:  storageTodo.CompletedAt,
		ErrorMessage: storageTodo.ErrorMessage,
		Metadata:     storageTodo.Metadata,
	}, nil
}

func (a *todoStoreAdapter) DeleteTodos(sessionID string) error {
	return a.store.DeleteTodos(sessionID)
}

func (a *todoStoreAdapter) CreateCheckpoint(checkpoint *builtin.TodoCheckpointData) error {
	storageCheckpoint := &storage.TodoCheckpoint{
		ID:                  checkpoint.ID,
		SessionID:           checkpoint.SessionID,
		CheckpointType:      checkpoint.CheckpointType,
		TodoCount:           checkpoint.TodoCount,
		CompletedCount:      checkpoint.CompletedCount,
		ConversationSummary: checkpoint.ConversationSummary,
		ConversationTokens:  checkpoint.ConversationTokens,
		CreatedAt:           checkpoint.CreatedAt,
		Metadata:            checkpoint.Metadata,
	}

	if err := a.store.CreateCheckpoint(storageCheckpoint); err != nil {
		return err
	}

	checkpoint.ID = storageCheckpoint.ID
	return nil
}

func (a *todoStoreAdapter) GetLatestCheckpoint(sessionID string) (*builtin.TodoCheckpointData, error) {
	storageCheckpoint, err := a.store.GetLatestCheckpoint(sessionID)
	if err != nil || storageCheckpoint == nil {
		return nil, err
	}

	return &builtin.TodoCheckpointData{
		ID:                  storageCheckpoint.ID,
		SessionID:           storageCheckpoint.SessionID,
		CheckpointType:      storageCheckpoint.CheckpointType,
		TodoCount:           storageCheckpoint.TodoCount,
		CompletedCount:      storageCheckpoint.CompletedCount,
		ConversationSummary: storageCheckpoint.ConversationSummary,
		ConversationTokens:  storageCheckpoint.ConversationTokens,
		CreatedAt:           time.Time(storageCheckpoint.CreatedAt),
		Metadata:            storageCheckpoint.Metadata,
	}, nil
}

func (a *todoStoreAdapter) EnsureSession(sessionID string) error {
	return a.store.EnsureSession(sessionID)
}
