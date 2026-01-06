package storage

import (
	"database/sql"
	"time"
)

// Todo represents a task item
type Todo struct {
	ID           int64      `json:"id"`
	SessionID    string     `json:"sessionId"`
	Content      string     `json:"content"`
	ActiveForm   string     `json:"activeForm"`
	Status       string     `json:"status"`
	OrderIndex   int        `json:"orderIndex"`
	ParentID     *int64     `json:"parentId,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
	ErrorMessage string     `json:"errorMessage,omitempty"`
	Metadata     string     `json:"metadata,omitempty"`
}

// TodoCheckpoint represents a checkpoint snapshot
type TodoCheckpoint struct {
	ID                  int64     `json:"id"`
	SessionID           string    `json:"sessionId"`
	CheckpointType      string    `json:"checkpointType"`
	TodoCount           int       `json:"todoCount"`
	CompletedCount      int       `json:"completedCount"`
	ConversationSummary string    `json:"conversationSummary"`
	ConversationTokens  int       `json:"conversationTokens"`
	CreatedAt           time.Time `json:"createdAt"`
	Metadata            string    `json:"metadata"`
}

// CreateTodo creates a new TODO item
func (s *Store) CreateTodo(todo *Todo) error {
	query := `
		INSERT INTO todos (session_id, content, active_form, status, order_index, parent_id, created_at, updated_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	result, err := s.db.Exec(query,
		todo.SessionID,
		todo.Content,
		todo.ActiveForm,
		todo.Status,
		todo.OrderIndex,
		todo.ParentID,
		todo.CreatedAt,
		todo.UpdatedAt,
		todo.Metadata,
	)
	if err != nil {
		return err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	todo.ID = id

	clone := *todo
	s.notify(newEvent(EventTodoCreated, todo.SessionID, todo.ID, clone))

	return nil
}

// UpdateTodoStatus updates the status of a TODO
func (s *Store) UpdateTodoStatus(id int64, status string, errorMessage string) error {
	now := time.Now()
	query := `
		UPDATE todos
		SET status = ?, updated_at = ?, error_message = ?, completed_at = ?
		WHERE id = ?
	`
	var completedAt *time.Time
	if status == "completed" || status == "failed" {
		completedAt = &now
	}
	_, err := s.db.Exec(query, status, now, errorMessage, completedAt, id)
	if err != nil {
		return err
	}

	todo, err := s.GetTodoByID(id)
	if err != nil {
		return err
	}
	if todo != nil {
		clone := *todo
		s.notify(newEvent(EventTodoUpdated, todo.SessionID, todo.ID, clone))
	}

	return nil
}

// GetTodos retrieves all TODOs for a session ordered by order_index
func (s *Store) GetTodos(sessionID string) ([]Todo, error) {
	query := `
		SELECT id, session_id, content, active_form, status, order_index, parent_id,
		       created_at, updated_at, completed_at, COALESCE(error_message, ''), COALESCE(metadata, '')
		FROM todos
		WHERE session_id = ?
		ORDER BY order_index ASC
	`
	rows, err := s.db.Query(query, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	todos := make([]Todo, 0)
	for rows.Next() {
		var todo Todo
		if err := rows.Scan(
			&todo.ID,
			&todo.SessionID,
			&todo.Content,
			&todo.ActiveForm,
			&todo.Status,
			&todo.OrderIndex,
			&todo.ParentID,
			&todo.CreatedAt,
			&todo.UpdatedAt,
			&todo.CompletedAt,
			&todo.ErrorMessage,
			&todo.Metadata,
		); err != nil {
			return nil, err
		}
		todos = append(todos, todo)
	}

	return todos, rows.Err()
}

// GetTodoByID returns a single TODO item by ID.
func (s *Store) GetTodoByID(id int64) (*Todo, error) {
	query := `
		SELECT id, session_id, content, active_form, status, order_index, parent_id,
		       created_at, updated_at, completed_at, COALESCE(error_message, ''), COALESCE(metadata, '')
		FROM todos
		WHERE id = ?
	`
	var todo Todo
	err := s.db.QueryRow(query, id).Scan(
		&todo.ID,
		&todo.SessionID,
		&todo.Content,
		&todo.ActiveForm,
		&todo.Status,
		&todo.OrderIndex,
		&todo.ParentID,
		&todo.CreatedAt,
		&todo.UpdatedAt,
		&todo.CompletedAt,
		&todo.ErrorMessage,
		&todo.Metadata,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &todo, nil
}

// GetActiveTodo returns the currently in_progress TODO
func (s *Store) GetActiveTodo(sessionID string) (*Todo, error) {
	query := `
		SELECT id, session_id, content, active_form, status, order_index, parent_id,
		       created_at, updated_at, completed_at, COALESCE(error_message, ''), COALESCE(metadata, '')
		FROM todos
		WHERE session_id = ? AND status = 'in_progress'
		ORDER BY order_index ASC
		LIMIT 1
	`
	var todo Todo
	err := s.db.QueryRow(query, sessionID).Scan(
		&todo.ID,
		&todo.SessionID,
		&todo.Content,
		&todo.ActiveForm,
		&todo.Status,
		&todo.OrderIndex,
		&todo.ParentID,
		&todo.CreatedAt,
		&todo.UpdatedAt,
		&todo.CompletedAt,
		&todo.ErrorMessage,
		&todo.Metadata,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &todo, nil
}

// DeleteTodos deletes all TODOs for a session
func (s *Store) DeleteTodos(sessionID string) error {
	query := `DELETE FROM todos WHERE session_id = ?`
	_, err := s.db.Exec(query, sessionID)
	if err != nil {
		return err
	}

	s.notify(newEvent(EventTodoCleared, sessionID, "", map[string]any{
		"sessionId": sessionID,
	}))
	return nil
}

// CreateCheckpoint creates a checkpoint snapshot
func (s *Store) CreateCheckpoint(checkpoint *TodoCheckpoint) error {
	query := `
		INSERT INTO todo_checkpoints (session_id, checkpoint_type, todo_count, completed_count,
		                              conversation_summary, conversation_tokens, created_at, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`
	result, err := s.db.Exec(query,
		checkpoint.SessionID,
		checkpoint.CheckpointType,
		checkpoint.TodoCount,
		checkpoint.CompletedCount,
		checkpoint.ConversationSummary,
		checkpoint.ConversationTokens,
		checkpoint.CreatedAt,
		checkpoint.Metadata,
	)
	if err != nil {
		return err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	checkpoint.ID = id
	return nil
}

// TodoSummary contains aggregate counts for a session's todos
type TodoSummary struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Pending   int `json:"pending"`
	Failed    int `json:"failed"`
}

// GetTodoSummary returns aggregate todo counts for a session
func (s *Store) GetTodoSummary(sessionID string) (*TodoSummary, error) {
	query := `
		SELECT
			COUNT(*) as total,
			COALESCE(SUM(CASE WHEN status = 'completed' THEN 1 ELSE 0 END), 0) as completed,
			COALESCE(SUM(CASE WHEN status = 'pending' OR status = 'in_progress' THEN 1 ELSE 0 END), 0) as pending,
			COALESCE(SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0) as failed
		FROM todos
		WHERE session_id = ?
	`
	var summary TodoSummary
	err := s.db.QueryRow(query, sessionID).Scan(
		&summary.Total,
		&summary.Completed,
		&summary.Pending,
		&summary.Failed,
	)
	if err != nil {
		return nil, err
	}
	return &summary, nil
}

// GetLatestCheckpoint retrieves the most recent checkpoint for a session
func (s *Store) GetLatestCheckpoint(sessionID string) (*TodoCheckpoint, error) {
	query := `
		SELECT id, session_id, checkpoint_type, todo_count, completed_count,
		       COALESCE(conversation_summary, ''), conversation_tokens, created_at, COALESCE(metadata, '')
		FROM todo_checkpoints
		WHERE session_id = ?
		ORDER BY created_at DESC
		LIMIT 1
	`
	var checkpoint TodoCheckpoint
	err := s.db.QueryRow(query, sessionID).Scan(
		&checkpoint.ID,
		&checkpoint.SessionID,
		&checkpoint.CheckpointType,
		&checkpoint.TodoCount,
		&checkpoint.CompletedCount,
		&checkpoint.ConversationSummary,
		&checkpoint.ConversationTokens,
		&checkpoint.CreatedAt,
		&checkpoint.Metadata,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &checkpoint, nil
}
