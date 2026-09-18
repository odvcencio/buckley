package parallel

import (
	"context"

	"m31labs.dev/buckley/pkg/worktree"
)

// ==== Scope Validator Tests ====

// No conflicts

// With conflicts

// Empty

// With partitions

// ==== File Lock Manager Tests ====

// Avoid background goroutines in this stress test

// Repeated close after concurrent callers should remain safe.

// Don't interfere with test

// Should be able to acquire again

// ==== Merge Orchestrator Tests ====

// ==== Coordinator Tests ====

// Handlers should be set without error

// ==== Config Tests ====

// ==== Edge Cases ====

// testCoordinatorExecutor is a mock for coordinator tests
type testCoordinatorExecutor struct {
	*mockExecutor
}

func (e *testCoordinatorExecutor) Execute(ctx context.Context, task *AgentTask, wtPath string) (*AgentResult, error) {
	return e.mockExecutor.Execute(ctx, task, wtPath)
}

// testCoordinatorManager wraps mockWorktreeManager
type testCoordinatorManager struct {
	*mockWorktreeManager
}

func (m *testCoordinatorManager) Create(branch string) (*worktree.Worktree, error) {
	return m.mockWorktreeManager.Create(branch)
}

func (m *testCoordinatorManager) Remove(branch string, force bool) error {
	return m.mockWorktreeManager.Remove(branch, force)
}
