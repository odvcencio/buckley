// pkg/ralph/backend_internal_test.go
package ralph

import (
	"context"
	"errors"
	"time"
)

// mockHeadlessRunnerForBackend implements HeadlessRunner for testing InternalBackend.
type mockHeadlessRunnerForBackend struct {
	processCount  int
	lastInput     string
	shouldError   bool
	errorToReturn error
	state         string
	processDelay  time.Duration
}

func (m *mockHeadlessRunnerForBackend) ProcessInput(ctx context.Context, input string) error {
	m.processCount++
	m.lastInput = input

	if m.processDelay > 0 {
		select {
		case <-time.After(m.processDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	if m.shouldError {
		if m.errorToReturn != nil {
			return m.errorToReturn
		}
		return errors.New("mock error")
	}
	return nil
}

func (m *mockHeadlessRunnerForBackend) State() string {
	if m.state != "" {
		return m.state
	}
	return "idle"
}

// Should be available by default

// Set unavailable

// Set available again

// Execute should not return error directly; error is captured in result

// Cancel immediately

// Should capture context cancellation error

// Prompt tokens should be counted; output/cost remain zero without output telemetry.

// Test concurrent SetAvailable and Available calls

// No race condition should occur

// Verify options are stored

// Compile-time check that InternalBackend implements Backend
