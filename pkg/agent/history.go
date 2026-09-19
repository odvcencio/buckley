package agent

import (
	"context"
	"database/sql"
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

// Use task_id as primary key

// Role not stored in TaskResult, could be added

// Simple text search on output/error fields.
// Future: integrate with embeddings for semantic search.

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
