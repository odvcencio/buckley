// pkg/ralph/orchestrator_test.go
package ralph

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Should use first available backend

// Verify all backends returned results

// Create backends that track concurrency

// If run concurrently, should complete in ~50ms (not 150ms)

// Verify actual concurrency happened

// Execute multiple times and verify rotation

// After 6 iterations, each backend should be used exactly 2 times

// unavailable

// Execute multiple times

// backend-b should never be used

// The available backends should share the load

// Verify config was updated

// Writer goroutine

// Reader goroutine - Execute

// Wait for both

// At iteration 0, should not trigger

// Advance to iteration 5

// At iteration 6, should not trigger

// At iteration 10, should trigger again

// No error - should not trigger

// Different error - should not trigger

// Rate limit error - should trigger

// Case-insensitive check

// Neither when nor cron triggers should fire

// same trigger, different action

// First matching rule wins

// These should not panic

// In parallel mode, partial errors should still return successful results

// Should get at least the successful result

// The error result should have Error field set

// Error should be nil since we got partial results
// OR it could be an error - implementation choice
// acceptable either way

// concurrentMockBackend tracks concurrent execution
type concurrentMockBackend struct {
	name          string
	available     bool
	startCount    *atomic.Int32
	maxConcurrent *atomic.Int32
	mu            *sync.Mutex
	delay         time.Duration
}

func (c *concurrentMockBackend) Name() string {
	return c.name
}

func (c *concurrentMockBackend) Execute(ctx context.Context, req BackendRequest) (*BackendResult, error) {
	current := c.startCount.Add(1)

	// Update max concurrent
	c.mu.Lock()
	if current > c.maxConcurrent.Load() {
		c.maxConcurrent.Store(current)
	}
	c.mu.Unlock()

	defer c.startCount.Add(-1)

	select {
	case <-time.After(c.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return &BackendResult{
		Backend: c.name,
		Output:  "concurrent output",
	}, nil
}

func (c *concurrentMockBackend) Available() bool {
	return c.available
}

// Config references a backend that doesn't exist in registry

// Should match partial string "rate" in error message

// No backends enabled in config

// no rules

// When all backends fail, should return error

// Results should have error information

// zero means never

// Should never trigger

// empty string

// Empty OnError should not trigger on any error

// Test the internal filtering logic

// "not-in-config" is in registry but not in config

// Only "enabled-available" should be used

// First call should return the action and clear it

// Verify it was cleared

// Second call should return nil

// The model should be "sonnet" (overridden) not "opus" (default)

// No override set

// Without override, should use default model

// Helper to check if error message contains substring
func containsSubstring(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
