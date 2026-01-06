package agent

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestInMemoryTaskHistory(t *testing.T) {
	ctx := context.Background()
	h := NewInMemoryTaskHistory()

	// Save a task
	result := &TaskResult{
		TaskID:     "task-1",
		AgentID:    "coder-abc",
		Success:    true,
		Output:     "Code written successfully",
		Duration:   5 * time.Second,
		TokensUsed: 1500,
	}

	if err := h.Save(ctx, result); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Get the task
	got, err := h.Get(ctx, "task-1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.TaskID != result.TaskID {
		t.Errorf("Expected TaskID %q, got %q", result.TaskID, got.TaskID)
	}
	if got.Output != result.Output {
		t.Errorf("Expected Output %q, got %q", result.Output, got.Output)
	}

	// Get non-existent
	_, err = h.Get(ctx, "nonexistent")
	if err == nil {
		t.Error("Expected error for nonexistent task")
	}
}

func TestInMemoryTaskHistory_List(t *testing.T) {
	ctx := context.Background()
	h := NewInMemoryTaskHistory()

	// Save multiple tasks
	for i := 0; i < 5; i++ {
		h.Save(ctx, &TaskResult{
			TaskID:  fmt.Sprintf("task-%d", i),
			AgentID: "agent",
			Success: i%2 == 0,
		})
	}

	// List all
	results, err := h.List(ctx, TaskFilter{})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(results) != 5 {
		t.Errorf("Expected 5 results, got %d", len(results))
	}

	// List successful only
	success := true
	results, err = h.List(ctx, TaskFilter{Success: &success})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("Expected 3 successful results, got %d", len(results))
	}

	// List with limit
	results, err = h.List(ctx, TaskFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(results) > 2 {
		t.Errorf("Expected max 2 results, got %d", len(results))
	}
}

func TestInMemoryTaskHistory_Search(t *testing.T) {
	ctx := context.Background()
	h := NewInMemoryTaskHistory()

	h.Save(ctx, &TaskResult{
		TaskID: "task-1",
		Output: "Successfully compiled main.go",
	})
	h.Save(ctx, &TaskResult{
		TaskID: "task-2",
		Output: "Tests passed",
	})
	h.Save(ctx, &TaskResult{
		TaskID: "task-3",
		Error:  "compilation failed in main.go",
	})

	// Search for "main"
	results, err := h.Search(ctx, "main", 10)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Expected 2 results for 'main', got %d", len(results))
	}

	// Search with limit
	results, err = h.Search(ctx, "main", 1)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Expected 1 result with limit, got %d", len(results))
	}
}

func TestInMemoryTaskHistory_Stats(t *testing.T) {
	ctx := context.Background()
	h := NewInMemoryTaskHistory()

	h.Save(ctx, &TaskResult{
		TaskID:     "task-1",
		Success:    true,
		TokensUsed: 100,
		Duration:   time.Second,
	})
	h.Save(ctx, &TaskResult{
		TaskID:     "task-2",
		Success:    true,
		TokensUsed: 200,
		Duration:   2 * time.Second,
	})
	h.Save(ctx, &TaskResult{
		TaskID:     "task-3",
		Success:    false,
		TokensUsed: 50,
		Duration:   500 * time.Millisecond,
	})

	stats, err := h.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}

	if stats.TotalTasks != 3 {
		t.Errorf("Expected 3 total tasks, got %d", stats.TotalTasks)
	}
	if stats.SuccessfulTasks != 2 {
		t.Errorf("Expected 2 successful tasks, got %d", stats.SuccessfulTasks)
	}
	if stats.FailedTasks != 1 {
		t.Errorf("Expected 1 failed task, got %d", stats.FailedTasks)
	}
	if stats.TotalTokens != 350 {
		t.Errorf("Expected 350 total tokens, got %d", stats.TotalTokens)
	}
}

func TestSQLiteTaskHistory(t *testing.T) {
	// Create temp database
	tmpFile, err := os.CreateTemp("", "task_history_test_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := sql.Open("sqlite", tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	h, err := NewSQLiteTaskHistory(db)
	if err != nil {
		t.Fatalf("NewSQLiteTaskHistory failed: %v", err)
	}

	// Save a task
	result := &TaskResult{
		TaskID:     "task-sqlite-1",
		AgentID:    "coder-xyz",
		Success:    true,
		Output:     "SQLite test output",
		Duration:   3 * time.Second,
		TokensUsed: 250,
		Artifacts: []Artifact{
			{Type: "file", Path: "test.go", Content: "package test"},
		},
		ToolCalls: []ToolCall{
			{Name: "write_file", Success: true},
		},
	}

	if err := h.Save(ctx, result); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Get the task
	got, err := h.Get(ctx, "task-sqlite-1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got.TaskID != result.TaskID {
		t.Errorf("Expected TaskID %q, got %q", result.TaskID, got.TaskID)
	}
	if got.Output != result.Output {
		t.Errorf("Expected Output %q, got %q", result.Output, got.Output)
	}
	if len(got.Artifacts) != 1 {
		t.Errorf("Expected 1 artifact, got %d", len(got.Artifacts))
	}
	if len(got.ToolCalls) != 1 {
		t.Errorf("Expected 1 tool call, got %d", len(got.ToolCalls))
	}
}

func TestSQLiteTaskHistory_List(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "task_history_list_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := sql.Open("sqlite", tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	h, err := NewSQLiteTaskHistory(db)
	if err != nil {
		t.Fatalf("NewSQLiteTaskHistory failed: %v", err)
	}

	// Save multiple tasks
	for i := 0; i < 5; i++ {
		h.Save(ctx, &TaskResult{
			TaskID:  fmt.Sprintf("list-task-%d", i),
			AgentID: "agent",
			Success: i%2 == 0,
		})
	}

	// List all
	results, err := h.List(ctx, TaskFilter{})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(results) != 5 {
		t.Errorf("Expected 5 results, got %d", len(results))
	}

	// List with limit
	results, err = h.List(ctx, TaskFilter{Limit: 2})
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(results))
	}
}

func TestSQLiteTaskHistory_Stats(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "task_history_stats_*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	db, err := sql.Open("sqlite", tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	h, err := NewSQLiteTaskHistory(db)
	if err != nil {
		t.Fatalf("NewSQLiteTaskHistory failed: %v", err)
	}

	h.Save(ctx, &TaskResult{
		TaskID:     "stats-1",
		Success:    true,
		TokensUsed: 100,
		Duration:   time.Second,
	})
	h.Save(ctx, &TaskResult{
		TaskID:     "stats-2",
		Success:    false,
		TokensUsed: 50,
		Duration:   500 * time.Millisecond,
	})

	stats, err := h.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats failed: %v", err)
	}

	if stats.TotalTasks != 2 {
		t.Errorf("Expected 2 total tasks, got %d", stats.TotalTasks)
	}
	if stats.SuccessfulTasks != 1 {
		t.Errorf("Expected 1 successful task, got %d", stats.SuccessfulTasks)
	}
	if stats.FailedTasks != 1 {
		t.Errorf("Expected 1 failed task, got %d", stats.FailedTasks)
	}
	if stats.TotalTokens != 150 {
		t.Errorf("Expected 150 total tokens, got %d", stats.TotalTokens)
	}
}

func TestTaskFilter(t *testing.T) {
	filter := TaskFilter{
		AgentRole: RoleCoder,
		Limit:     50,
		Offset:    10,
	}

	if filter.AgentRole != RoleCoder {
		t.Errorf("Expected RoleCoder, got %v", filter.AgentRole)
	}
	if filter.Limit != 50 {
		t.Errorf("Expected Limit 50, got %d", filter.Limit)
	}
	if filter.Offset != 10 {
		t.Errorf("Expected Offset 10, got %d", filter.Offset)
	}
}
