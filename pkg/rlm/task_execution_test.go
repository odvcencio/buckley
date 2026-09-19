package rlm

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestExecuteTasksValidatesWholeRequestBeforeEffects(t *testing.T) {
	rt := &Runtime{dispatcher: &BatchDispatcher{}}
	_, err := rt.ExecuteTasks(context.Background(), BatchRequest{Tasks: []SubTask{
		{ID: "task-a", Prompt: "one"},
		{ID: "task-a", Prompt: "two"},
	}})
	if err == nil || !strings.Contains(err.Error(), "duplicate task id: task-a") {
		t.Fatalf("ExecuteTasks error = %v, want duplicate ID validation before dispatcher effects", err)
	}
}

func TestCloneBatchRequestCopiesAllowedTools(t *testing.T) {
	req := BatchRequest{Tasks: []SubTask{{ID: "task-a", Prompt: "work", AllowedTools: []string{"read_file"}}}}
	cloned := cloneBatchRequest(req)
	req.Tasks[0].AllowedTools[0] = "write_file"
	req.Tasks[0].ID = "mutated"

	if cloned.Tasks[0].ID != "task-a" || cloned.Tasks[0].AllowedTools[0] != "read_file" {
		t.Fatalf("cloned request = %+v, want immutable task ID/tools copy", cloned)
	}
}

func TestTaskExecutionContextPreservesRuntimeWallLimit(t *testing.T) {
	rt := &Runtime{config: Config{Coordinator: CoordinatorConfig{MaxWallTime: 25 * time.Millisecond}}}
	parent, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	ctx, done := rt.taskExecutionContext(parent, time.Now())
	defer done()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("task execution context has no deadline")
	}
	if until := time.Until(deadline); until > time.Second {
		t.Fatalf("deadline = %s from now, want runtime wall cap not parent hour", until)
	}

	shortParent, shortCancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer shortCancel()
	ctx, done = rt.taskExecutionContext(shortParent, time.Now())
	defer done()
	if got, want := ctx.Deadline(); !got.Equal(func() time.Time { d, _ := shortParent.Deadline(); return d }()) {
		t.Fatalf("deadline = %v, want shorter parent deadline %v", got, want)
	}
}
