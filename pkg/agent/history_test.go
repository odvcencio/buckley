package agent

import (
	"context"
	"fmt"
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

// Create temp database

// Save a task

// Get the task

// Save multiple tasks

// List all

// List with limit

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
