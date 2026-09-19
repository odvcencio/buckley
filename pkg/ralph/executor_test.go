// pkg/ralph/executor_test.go
package ralph

import (
	"context"
)

type mockHeadlessRunner struct {
	processCount int
	shouldError  bool
}

func (m *mockHeadlessRunner) ProcessInput(ctx context.Context, input string) error {
	m.processCount++
	if m.shouldError {
		return context.Canceled
	}
	return nil
}

func (m *mockHeadlessRunner) State() string {
	return "idle"
}

// Run should complete after max iterations

// High limit to ensure timeout hits first

// Should have stopped due to timeout, not max iterations

// Start the session to transition to running state

// Test pause

// Test resume

// Test nil executor

// Test executor with nil session

// Write initial prompt

// Run first iteration

// Start a goroutine to update the prompt file after a short delay

// Note: Due to timing, we can't guarantee the reload happened,
// but we're testing the code path doesn't panic or error

// Start executor in a goroutine; it should block on pause

// Give it time to enter the pause loop

// Verify no iterations ran while paused

// Unpause

// Since we're running without an orchestrator, results slice is empty.
// The verify command still runs and sets lastError on failure.
// With 2 ok + 1 FAIL, the verify command itself succeeds (exit 0),
// but the fail count should be tracked.
