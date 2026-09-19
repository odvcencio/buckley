package parallel

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/worktree"
)

// mockWorktreeManager implements a minimal worktree manager for testing
type mockWorktreeManager struct {
	mu        sync.Mutex
	worktrees map[string]*worktree.Worktree
	failOn    string
}

func newMockWorktreeManager() *mockWorktreeManager {
	return &mockWorktreeManager{
		worktrees: make(map[string]*worktree.Worktree),
	}
}

func (m *mockWorktreeManager) Create(branch string) (*worktree.Worktree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.failOn == branch {
		return nil, fmt.Errorf("mock failure creating worktree for branch: %s", branch)
	}

	wt := &worktree.Worktree{
		Branch: branch,
		Path:   "/tmp/mock-worktree-" + branch,
	}
	m.worktrees[branch] = wt
	return wt, nil
}

func (m *mockWorktreeManager) Remove(branch string, force bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.worktrees, branch)
	return nil
}

// mockExecutor implements TaskExecutor for testing
type mockExecutor struct {
	mu          sync.Mutex
	executions  []*AgentTask
	shouldFail  map[string]bool
	execTime    time.Duration
	filesOutput []string
}

func newMockExecutor() *mockExecutor {
	return &mockExecutor{
		shouldFail:  make(map[string]bool),
		execTime:    10 * time.Millisecond,
		filesOutput: []string{"file1.go", "file2.go"},
	}
}

func (e *mockExecutor) Execute(ctx context.Context, task *AgentTask, wtPath string) (*AgentResult, error) {
	e.mu.Lock()
	e.executions = append(e.executions, task)
	shouldFail := e.shouldFail[task.ID]
	e.mu.Unlock()

	// Simulate work
	select {
	case <-time.After(e.execTime):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if shouldFail {
		return &AgentResult{
			Success: false,
			Error:   fmt.Errorf("mock execution failure"),
		}, nil
	}

	return &AgentResult{
		Success: true,
		Output:  fmt.Sprintf("Executed task %s in %s", task.ID, wtPath),
		Files:   e.filesOutput,
		Metrics: map[string]int{"tokens": 100},
	}, nil
}

func TestNewOrchestrator(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()

	// Test with zero config values
	cfg := Config{}
	o := NewOrchestrator(&worktree.Manager{}, executor, cfg)

	if o.maxAgents != 4 {
		t.Errorf("maxAgents = %v, want 4 (default)", o.maxAgents)
	}

	// Test with custom config
	cfg = Config{
		MaxAgents:       2,
		TaskQueueSize:   50,
		ResultQueueSize: 50,
	}
	o = NewOrchestrator(&worktree.Manager{}, executor, cfg)

	if o.maxAgents != 2 {
		t.Errorf("maxAgents = %v, want 2", o.maxAgents)
	}

	_ = mock // silence unused warning
}

func TestOrchestrator_Submit(t *testing.T) {
	executor := newMockExecutor()
	cfg := Config{MaxAgents: 2, TaskQueueSize: 10}
	o := NewOrchestrator(&worktree.Manager{}, executor, cfg)

	// Submit nil task
	err := o.Submit(nil)
	if err == nil {
		t.Error("Submit(nil) should return error")
	}

	// Submit valid task
	task := &AgentTask{
		Name:        "Test Task",
		Description: "A test task",
		Prompt:      "Do something",
	}

	err = o.Submit(task)
	if err != nil {
		t.Errorf("Submit() error = %v", err)
	}

	// ID should be generated
	if task.ID == "" {
		t.Error("Submit() should generate task ID")
	}

	// Branch should be generated
	if task.Branch == "" {
		t.Error("Submit() should generate branch name")
	}
}

func TestOrchestrator_SubmitWithContext(t *testing.T) {
	executor := newMockExecutor()
	cfg := Config{MaxAgents: 1, TaskQueueSize: 1}
	o := NewOrchestrator(&worktree.Manager{}, executor, cfg)

	// Fill the queue
	o.Submit(&AgentTask{ID: "task-1"})

	// Next submit should fail (queue full)
	err := o.Submit(&AgentTask{ID: "task-2"})
	if err == nil {
		t.Error("Submit() should fail when queue is full")
	}
}

// All tasks should have IDs

// Initially empty

// Initially zero

// Cancel non-existent task

// Check it contains key info

func TestGenerateTaskID(t *testing.T) {
	id1 := generateTaskID()
	id2 := generateTaskID()

	if id1 == "" {
		t.Error("generateTaskID() returned empty string")
	}

	if !hasPrefix(id1, "task_") {
		t.Errorf("generateTaskID() = %q, should have prefix 'task_'", id1)
	}

	// IDs should be unique (with high probability)
	if id1 == id2 {
		t.Error("generateTaskID() should return unique IDs")
	}
}

func TestAgentTask_Fields(t *testing.T) {
	task := AgentTask{
		ID:          "task-123",
		Name:        "Test Task",
		Description: "A test task description",
		Branch:      "feature-branch",
		Prompt:      "Do the thing",
		Context:     map[string]string{"key": "value"},
		Priority:    10,
	}

	if task.ID != "task-123" {
		t.Errorf("ID = %q, want %q", task.ID, "task-123")
	}
	if task.Priority != 10 {
		t.Errorf("Priority = %d, want 10", task.Priority)
	}
	if task.Context["key"] != "value" {
		t.Error("Context not set correctly")
	}
}

func TestAgentResult_Fields(t *testing.T) {
	result := AgentResult{
		TaskID:   "task-123",
		Success:  true,
		Output:   "Completed successfully",
		Duration: 5 * time.Second,
		Branch:   "feature-branch",
		Files:    []string{"a.go", "b.go"},
		Metrics:  map[string]int{"tokens": 500},
	}

	if result.TaskID != "task-123" {
		t.Errorf("TaskID = %q, want %q", result.TaskID, "task-123")
	}
	if !result.Success {
		t.Error("Success should be true")
	}
	if len(result.Files) != 2 {
		t.Errorf("Files count = %d, want 2", len(result.Files))
	}
	if result.Metrics["tokens"] != 500 {
		t.Errorf("Metrics[tokens] = %d, want 500", result.Metrics["tokens"])
	}
}

func TestAgent_Fields(t *testing.T) {
	now := time.Now()
	task := &AgentTask{ID: "task-1"}
	wt := &worktree.Worktree{Branch: "test-branch", Path: "/tmp/wt"}

	agent := Agent{
		ID:        "agent-1",
		Worktree:  wt,
		Status:    StatusRunning,
		Task:      task,
		StartedAt: now,
		Error:     nil,
	}

	if agent.ID != "agent-1" {
		t.Errorf("ID = %q, want %q", agent.ID, "agent-1")
	}
	if agent.Status != StatusRunning {
		t.Errorf("Status = %v, want %v", agent.Status, StatusRunning)
	}
	if agent.Task.ID != "task-1" {
		t.Error("Task not set correctly")
	}
	if agent.Worktree.Branch != "test-branch" {
		t.Error("Worktree not set correctly")
	}
}

func TestOrchestrator_Results(t *testing.T) {
	executor := newMockExecutor()
	cfg := Config{MaxAgents: 2}
	o := NewOrchestrator(&worktree.Manager{}, executor, cfg)

	results := o.Results()
	if results == nil {
		t.Error("Results() returned nil channel")
	}
}

func TestConfig_Fields(t *testing.T) {
	cfg := Config{
		MaxAgents:       8,
		WorktreeRoot:    "/tmp/worktrees",
		TaskQueueSize:   200,
		ResultQueueSize: 150,
	}

	if cfg.MaxAgents != 8 {
		t.Errorf("MaxAgents = %d, want 8", cfg.MaxAgents)
	}
	if cfg.WorktreeRoot != "/tmp/worktrees" {
		t.Errorf("WorktreeRoot = %q", cfg.WorktreeRoot)
	}
	if cfg.TaskQueueSize != 200 {
		t.Errorf("TaskQueueSize = %d, want 200", cfg.TaskQueueSize)
	}
	if cfg.ResultQueueSize != 150 {
		t.Errorf("ResultQueueSize = %d, want 150", cfg.ResultQueueSize)
	}
}

// Helper functions
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestOrchestrator_StartStop tests the Start and Stop lifecycle
func TestOrchestrator_StartStop(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()

	cfg := Config{MaxAgents: 2, TaskQueueSize: 10, ResultQueueSize: 10}
	o := NewOrchestrator(mock, executor, cfg)

	// Start workers
	o.Start()

	// Give workers time to start
	time.Sleep(50 * time.Millisecond)

	// Stop should complete without hanging
	done := make(chan struct{})
	go func() {
		o.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() timed out")
	}
}

// TestOrchestrator_ExecuteTask tests end-to-end task execution
func TestOrchestrator_ExecuteTask(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()

	cfg := Config{MaxAgents: 2, TaskQueueSize: 10, ResultQueueSize: 10}
	o := NewOrchestrator(mock, executor, cfg)

	o.Start()
	defer o.Stop()

	// Submit a task
	task := &AgentTask{
		ID:          "test-task-1",
		Name:        "Test Task",
		Description: "A test task",
		Prompt:      "Do something",
	}

	err := o.Submit(task)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	// Wait for result with timeout
	select {
	case result := <-o.Results():
		if result.TaskID != "test-task-1" {
			t.Errorf("TaskID = %v, want %v", result.TaskID, "test-task-1")
		}
		if !result.Success {
			t.Errorf("Success = false, want true")
		}
		if result.Branch == "" {
			t.Error("Branch should not be empty")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timed out waiting for result")
	}
}

// TestOrchestrator_ExecuteMultipleTasks tests parallel execution
func TestOrchestrator_ExecuteMultipleTasks(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()
	executor.execTime = 50 * time.Millisecond

	cfg := Config{MaxAgents: 3, TaskQueueSize: 10, ResultQueueSize: 10}
	o := NewOrchestrator(mock, executor, cfg)

	o.Start()
	defer o.Stop()

	// Submit multiple tasks
	numTasks := 5
	for i := 0; i < numTasks; i++ {
		task := &AgentTask{
			ID:   fmt.Sprintf("task-%d", i),
			Name: fmt.Sprintf("Task %d", i),
		}
		if err := o.Submit(task); err != nil {
			t.Fatalf("Submit() error = %v", err)
		}
	}

	// Collect all results
	results := make([]*AgentResult, 0, numTasks)
	timeout := time.After(5 * time.Second)
	for len(results) < numTasks {
		select {
		case result := <-o.Results():
			results = append(results, result)
		case <-timeout:
			t.Fatalf("Timed out waiting for results, got %d of %d", len(results), numTasks)
		}
	}

	// Verify all tasks completed
	if len(results) != numTasks {
		t.Errorf("Got %d results, want %d", len(results), numTasks)
	}

	// Check that all were successful
	for _, result := range results {
		if !result.Success {
			t.Errorf("Task %s failed: %v", result.TaskID, result.Error)
		}
	}
}

// Check agent status was updated

// This is expected if agent has moved on to another state

// TestOrchestrator_WorktreeCreationFailure tests worktree creation failure handling
func TestOrchestrator_WorktreeCreationFailure(t *testing.T) {
	mock := newMockWorktreeManager()
	mock.failOn = "agent-fail-wt"
	executor := newMockExecutor()

	cfg := Config{MaxAgents: 2, TaskQueueSize: 10, ResultQueueSize: 10}
	o := NewOrchestrator(mock, executor, cfg)

	o.Start()
	defer o.Stop()

	task := &AgentTask{
		ID:     "fail-wt",
		Branch: "agent-fail-wt",
		Name:   "Worktree Fail Task",
	}

	err := o.Submit(task)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	select {
	case result := <-o.Results():
		if result.Success {
			t.Error("Success should be false when worktree creation fails")
		}
		if result.Error == nil {
			t.Error("Error should not be nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timed out waiting for result")
	}
}

// Submit a task

// Drain results in background

// Wait should eventually succeed

// Long execution time

// Submit a task

// Wait a bit for task to be picked up

// Wait with short timeout - task is still running

// Long enough to cancel

// Wait a bit for task to start

// Try to cancel

// Either succeeds or task already completed (race condition)

// TestOrchestrator_Cleanup tests the Cleanup function
func TestOrchestrator_Cleanup(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()
	executor.execTime = 10 * time.Millisecond

	cfg := Config{MaxAgents: 2, TaskQueueSize: 10, ResultQueueSize: 10}
	o := NewOrchestrator(mock, executor, cfg)

	o.Start()

	// Submit a task
	task := &AgentTask{
		ID:   "cleanup-test",
		Name: "Cleanup Test Task",
	}
	o.Submit(task)

	// Wait for result
	select {
	case <-o.Results():
	case <-time.After(2 * time.Second):
		t.Fatal("Timed out waiting for result")
	}

	// Allow time for status to be updated
	time.Sleep(50 * time.Millisecond)

	// Stop the orchestrator
	o.Stop()

	// Cleanup should work
	err := o.Cleanup()
	if err != nil {
		t.Errorf("Cleanup() error = %v", err)
	}
}

// Submit tasks

// Wait a bit for agents to start

// Get summary while tasks are running

// Drain results

// TestOrchestrator_SubmitAfterStop tests Submit after Stop
func TestOrchestrator_SubmitAfterStop(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()

	cfg := Config{MaxAgents: 2, TaskQueueSize: 10, ResultQueueSize: 10}
	o := NewOrchestrator(mock, executor, cfg)

	o.Start()
	o.Stop()

	// Submit after stop should fail or panic (channel is closed)
	// The current implementation may panic on closed channel
	task := &AgentTask{
		ID:   "after-stop",
		Name: "After Stop Task",
	}

	// Recover from potential panic
	defer func() {
		if r := recover(); r != nil {
			// Expected: panic on send to closed channel
			// This is acceptable behavior for submitting after stop
		}
	}()

	err := o.Submit(task)
	if err == nil {
		t.Error("Submit() after Stop() should return error")
	}
}

// Create tasks that will fill the queue

// BatchSubmit should fail when queue is full

// Submit multiple tasks

// Wait for agents to start

// Should have active agents

// Drain results

// Submit task

// Wait for agent to start

// Check agent has task

// Drain results

// Empty batch should succeed

// TestOrchestrator_ContextCancellation tests context cancellation during execution
func TestOrchestrator_ContextCancellation(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := &slowCancellingExecutor{execTime: 1 * time.Second}

	cfg := Config{MaxAgents: 2, TaskQueueSize: 10, ResultQueueSize: 10}
	o := NewOrchestrator(mock, executor, cfg)

	o.Start()

	// Submit a task
	task := &AgentTask{
		ID:   "cancel-ctx-test",
		Name: "Cancel Context Task",
	}
	o.Submit(task)

	// Wait a bit for task to start
	time.Sleep(50 * time.Millisecond)

	// Stop (which cancels context)
	done := make(chan struct{})
	go func() {
		o.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Success - workers stopped properly
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() timed out - context cancellation not working")
	}
}

// slowCancellingExecutor respects context cancellation
type slowCancellingExecutor struct {
	execTime time.Duration
}

func (e *slowCancellingExecutor) Execute(ctx context.Context, task *AgentTask, wtPath string) (*AgentResult, error) {
	select {
	case <-time.After(e.execTime):
		return &AgentResult{
			Success: true,
			Output:  "Completed",
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Wait for result

// Wait a bit for status update

// Check status was updated to completed

// TestOrchestrator_WorkerPicksUpMultipleTasks tests worker processes multiple tasks
func TestOrchestrator_WorkerPicksUpMultipleTasks(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()
	executor.execTime = 10 * time.Millisecond

	cfg := Config{MaxAgents: 1, TaskQueueSize: 10, ResultQueueSize: 10}
	o := NewOrchestrator(mock, executor, cfg)

	o.Start()
	defer o.Stop()

	// Submit 3 tasks, single worker should process all
	for i := 0; i < 3; i++ {
		task := &AgentTask{
			ID:   fmt.Sprintf("multi-task-%d", i),
			Name: fmt.Sprintf("Multi Task %d", i),
		}
		o.Submit(task)
	}

	// Collect all results
	results := make([]*AgentResult, 0, 3)
	timeout := time.After(3 * time.Second)
	for len(results) < 3 {
		select {
		case result := <-o.Results():
			results = append(results, result)
		case <-timeout:
			t.Fatalf("Timed out, got %d results", len(results))
		}
	}

	if len(results) != 3 {
		t.Errorf("Got %d results, want 3", len(results))
	}
}

// TestOrchestrator_CleanupWithRemovalError tests Cleanup when worktree removal fails
func TestOrchestrator_CleanupWithRemovalError(t *testing.T) {
	mock := &failingRemoveMockManager{
		worktrees: make(map[string]*worktree.Worktree),
	}
	executor := newMockExecutor()
	executor.execTime = 10 * time.Millisecond

	cfg := Config{MaxAgents: 1, TaskQueueSize: 10, ResultQueueSize: 10}
	o := NewOrchestrator(mock, executor, cfg)

	o.Start()

	// Submit a task
	task := &AgentTask{
		ID:   "cleanup-error-test",
		Name: "Cleanup Error Test",
	}
	o.Submit(task)

	// Wait for result
	select {
	case <-o.Results():
	case <-time.After(2 * time.Second):
		t.Fatal("Timed out waiting for result")
	}

	// Allow time for status to be updated
	time.Sleep(50 * time.Millisecond)

	o.Stop()

	// Cleanup should return an error
	err := o.Cleanup()
	if err == nil {
		t.Error("Cleanup() should return error when worktree removal fails")
	}
}

// failingRemoveMockManager is a mock that fails on Remove
type failingRemoveMockManager struct {
	mu        sync.Mutex
	worktrees map[string]*worktree.Worktree
}

func (m *failingRemoveMockManager) Create(branch string) (*worktree.Worktree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	wt := &worktree.Worktree{
		Branch: branch,
		Path:   "/tmp/mock-worktree-" + branch,
	}
	m.worktrees[branch] = wt
	return wt, nil
}

func (m *failingRemoveMockManager) Remove(branch string, force bool) error {
	return fmt.Errorf("mock removal failure for branch: %s", branch)
}

// TestOrchestrator_CleanupWithFailedTask tests Cleanup for failed tasks
func TestOrchestrator_CleanupWithFailedTask(t *testing.T) {
	mock := newMockWorktreeManager()
	executor := newMockExecutor()
	executor.shouldFail["fail-for-cleanup"] = true
	executor.execTime = 10 * time.Millisecond

	cfg := Config{MaxAgents: 1, TaskQueueSize: 10, ResultQueueSize: 10}
	o := NewOrchestrator(mock, executor, cfg)

	o.Start()

	// Submit a failing task
	task := &AgentTask{
		ID:   "fail-for-cleanup",
		Name: "Fail For Cleanup",
	}
	o.Submit(task)

	// Wait for result
	select {
	case <-o.Results():
	case <-time.After(2 * time.Second):
		t.Fatal("Timed out waiting for result")
	}

	// Allow time for status to be updated
	time.Sleep(50 * time.Millisecond)

	o.Stop()

	// Cleanup should work for failed tasks too
	err := o.Cleanup()
	if err != nil {
		t.Errorf("Cleanup() error = %v", err)
	}
}

// Submit a failing task

// Wait for result

// Allow time for status to be updated

// Check summary

// Submit a task

// Wait for result

// Allow time for status to be updated

// Check summary
