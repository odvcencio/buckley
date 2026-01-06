package agent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// TaskHistory provides persistent storage for task execution history.
type TaskHistory interface {
	// Save stores a completed task result
	Save(ctx context.Context, result *TaskResult) error

	// Get retrieves a task by ID
	Get(ctx context.Context, taskID string) (*TaskResult, error)

	// List returns tasks matching the filter
	List(ctx context.Context, filter TaskFilter) ([]*TaskResult, error)

	// Search performs semantic search over task history
	Search(ctx context.Context, query string, limit int) ([]*TaskResult, error)

	// Stats returns aggregate statistics
	Stats(ctx context.Context) (*HistoryStats, error)
}

// TaskFilter specifies criteria for listing tasks.
type TaskFilter struct {
	AgentRole Role      // Filter by agent role
	Success   *bool     // Filter by success/failure
	After     time.Time // Tasks after this time
	Before    time.Time // Tasks before this time
	Limit     int       // Max results (default 100)
	Offset    int       // Pagination offset
}

// HistoryStats provides aggregate metrics.
type HistoryStats struct {
	TotalTasks      int64         `json:"total_tasks"`
	SuccessfulTasks int64         `json:"successful_tasks"`
	FailedTasks     int64         `json:"failed_tasks"`
	TotalTokens     int64         `json:"total_tokens"`
	TotalDuration   time.Duration `json:"total_duration"`
	AvgDuration     time.Duration `json:"avg_duration"`
}

// SQLiteTaskHistory implements TaskHistory using SQLite.
type SQLiteTaskHistory struct {
	db *sql.DB
}

// NewSQLiteTaskHistory creates a new SQLite-backed task history.
func NewSQLiteTaskHistory(db *sql.DB) (*SQLiteTaskHistory, error) {
	h := &SQLiteTaskHistory{db: db}
	if err := h.ensureSchema(); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *SQLiteTaskHistory) ensureSchema() error {
	schema := `
	CREATE TABLE IF NOT EXISTS task_history (
		id TEXT PRIMARY KEY,
		task_id TEXT NOT NULL,
		agent_id TEXT NOT NULL,
		agent_role TEXT,
		success INTEGER NOT NULL,
		output TEXT,
		error TEXT,
		duration_ns INTEGER,
		tokens_used INTEGER,
		artifacts TEXT,
		tool_calls TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_task_history_task_id ON task_history(task_id);
	CREATE INDEX IF NOT EXISTS idx_task_history_agent_role ON task_history(agent_role);
	CREATE INDEX IF NOT EXISTS idx_task_history_success ON task_history(success);
	CREATE INDEX IF NOT EXISTS idx_task_history_created_at ON task_history(created_at);

	CREATE TABLE IF NOT EXISTS task_embeddings (
		task_id TEXT PRIMARY KEY,
		embedding BLOB,
		FOREIGN KEY(task_id) REFERENCES task_history(task_id)
	);
	`
	_, err := h.db.Exec(schema)
	return err
}

func (h *SQLiteTaskHistory) Save(ctx context.Context, result *TaskResult) error {
	artifacts, _ := json.Marshal(result.Artifacts)
	toolCalls, _ := json.Marshal(result.ToolCalls)

	_, err := h.db.ExecContext(ctx, `
		INSERT OR REPLACE INTO task_history
		(id, task_id, agent_id, agent_role, success, output, error, duration_ns, tokens_used, artifacts, tool_calls, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		result.TaskID, // Use task_id as primary key
		result.TaskID,
		result.AgentID,
		"", // Role not stored in TaskResult, could be added
		result.Success,
		result.Output,
		result.Error,
		result.Duration.Nanoseconds(),
		result.TokensUsed,
		string(artifacts),
		string(toolCalls),
		time.Now(),
	)
	return err
}

func (h *SQLiteTaskHistory) Get(ctx context.Context, taskID string) (*TaskResult, error) {
	row := h.db.QueryRowContext(ctx, `
		SELECT task_id, agent_id, success, output, error, duration_ns, tokens_used, artifacts, tool_calls
		FROM task_history
		WHERE task_id = ?
	`, taskID)

	result := &TaskResult{}
	var durationNs int64
	var artifactsJSON, toolCallsJSON string

	err := row.Scan(
		&result.TaskID,
		&result.AgentID,
		&result.Success,
		&result.Output,
		&result.Error,
		&durationNs,
		&result.TokensUsed,
		&artifactsJSON,
		&toolCallsJSON,
	)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}
	if err != nil {
		return nil, err
	}

	result.Duration = time.Duration(durationNs)
	json.Unmarshal([]byte(artifactsJSON), &result.Artifacts)
	json.Unmarshal([]byte(toolCallsJSON), &result.ToolCalls)

	return result, nil
}

func (h *SQLiteTaskHistory) List(ctx context.Context, filter TaskFilter) ([]*TaskResult, error) {
	query := `
		SELECT task_id, agent_id, success, output, error, duration_ns, tokens_used, artifacts, tool_calls
		FROM task_history
		WHERE 1=1
	`
	args := []any{}

	if filter.AgentRole != "" {
		query += " AND agent_role = ?"
		args = append(args, string(filter.AgentRole))
	}
	if filter.Success != nil {
		query += " AND success = ?"
		args = append(args, *filter.Success)
	}
	if !filter.After.IsZero() {
		query += " AND created_at > ?"
		args = append(args, filter.After)
	}
	if !filter.Before.IsZero() {
		query += " AND created_at < ?"
		args = append(args, filter.Before)
	}

	query += " ORDER BY created_at DESC"

	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	query += fmt.Sprintf(" LIMIT %d", limit)

	if filter.Offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", filter.Offset)
	}

	rows, err := h.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*TaskResult
	for rows.Next() {
		result := &TaskResult{}
		var durationNs int64
		var artifactsJSON, toolCallsJSON string

		err := rows.Scan(
			&result.TaskID,
			&result.AgentID,
			&result.Success,
			&result.Output,
			&result.Error,
			&durationNs,
			&result.TokensUsed,
			&artifactsJSON,
			&toolCallsJSON,
		)
		if err != nil {
			return nil, err
		}

		result.Duration = time.Duration(durationNs)
		json.Unmarshal([]byte(artifactsJSON), &result.Artifacts)
		json.Unmarshal([]byte(toolCallsJSON), &result.ToolCalls)

		results = append(results, result)
	}

	return results, rows.Err()
}

func (h *SQLiteTaskHistory) Search(ctx context.Context, query string, limit int) ([]*TaskResult, error) {
	// Simple text search on output/error fields.
	// Future: integrate with embeddings for semantic search.
	if limit <= 0 {
		limit = 10
	}

	rows, err := h.db.QueryContext(ctx, `
		SELECT task_id, agent_id, success, output, error, duration_ns, tokens_used, artifacts, tool_calls
		FROM task_history
		WHERE output LIKE ? OR error LIKE ?
		ORDER BY created_at DESC
		LIMIT ?
	`, "%"+query+"%", "%"+query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*TaskResult
	for rows.Next() {
		result := &TaskResult{}
		var durationNs int64
		var artifactsJSON, toolCallsJSON string

		err := rows.Scan(
			&result.TaskID,
			&result.AgentID,
			&result.Success,
			&result.Output,
			&result.Error,
			&durationNs,
			&result.TokensUsed,
			&artifactsJSON,
			&toolCallsJSON,
		)
		if err != nil {
			return nil, err
		}

		result.Duration = time.Duration(durationNs)
		json.Unmarshal([]byte(artifactsJSON), &result.Artifacts)
		json.Unmarshal([]byte(toolCallsJSON), &result.ToolCalls)

		results = append(results, result)
	}

	return results, rows.Err()
}

func (h *SQLiteTaskHistory) Stats(ctx context.Context) (*HistoryStats, error) {
	row := h.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*) as total,
			SUM(CASE WHEN success = 1 THEN 1 ELSE 0 END) as successful,
			SUM(CASE WHEN success = 0 THEN 1 ELSE 0 END) as failed,
			COALESCE(SUM(tokens_used), 0) as total_tokens,
			COALESCE(SUM(duration_ns), 0) as total_duration_ns,
			COALESCE(AVG(duration_ns), 0.0) as avg_duration_ns
		FROM task_history
	`)

	stats := &HistoryStats{}
	var totalDurationNs int64
	var avgDurationNs float64

	err := row.Scan(
		&stats.TotalTasks,
		&stats.SuccessfulTasks,
		&stats.FailedTasks,
		&stats.TotalTokens,
		&totalDurationNs,
		&avgDurationNs,
	)
	if err != nil {
		return nil, err
	}

	stats.TotalDuration = time.Duration(totalDurationNs)
	stats.AvgDuration = time.Duration(int64(avgDurationNs))

	return stats, nil
}

// InMemoryTaskHistory implements TaskHistory for testing.
type InMemoryTaskHistory struct {
	tasks map[string]*TaskResult
}

// NewInMemoryTaskHistory creates an in-memory task history.
func NewInMemoryTaskHistory() *InMemoryTaskHistory {
	return &InMemoryTaskHistory{
		tasks: make(map[string]*TaskResult),
	}
}

func (h *InMemoryTaskHistory) Save(ctx context.Context, result *TaskResult) error {
	h.tasks[result.TaskID] = result
	return nil
}

func (h *InMemoryTaskHistory) Get(ctx context.Context, taskID string) (*TaskResult, error) {
	if result, ok := h.tasks[taskID]; ok {
		return result, nil
	}
	return nil, fmt.Errorf("task not found: %s", taskID)
}

func (h *InMemoryTaskHistory) List(ctx context.Context, filter TaskFilter) ([]*TaskResult, error) {
	var results []*TaskResult
	for _, result := range h.tasks {
		if filter.Success != nil && result.Success != *filter.Success {
			continue
		}
		results = append(results, result)
	}

	limit := filter.Limit
	if limit <= 0 {
		limit = 100
	}
	if len(results) > limit {
		results = results[:limit]
	}

	return results, nil
}

func (h *InMemoryTaskHistory) Search(ctx context.Context, query string, limit int) ([]*TaskResult, error) {
	// Simple substring match for testing
	var results []*TaskResult
	for _, result := range h.tasks {
		if contains(result.Output, query) || contains(result.Error, query) {
			results = append(results, result)
			if limit > 0 && len(results) >= limit {
				break
			}
		}
	}
	return results, nil
}

func (h *InMemoryTaskHistory) Stats(ctx context.Context) (*HistoryStats, error) {
	stats := &HistoryStats{}
	for _, result := range h.tasks {
		stats.TotalTasks++
		stats.TotalTokens += int64(result.TokensUsed)
		stats.TotalDuration += result.Duration
		if result.Success {
			stats.SuccessfulTasks++
		} else {
			stats.FailedTasks++
		}
	}
	if stats.TotalTasks > 0 {
		stats.AvgDuration = stats.TotalDuration / time.Duration(stats.TotalTasks)
	}
	return stats, nil
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
