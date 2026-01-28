package model

import (
	"errors"
	"testing"
	"time"
)

func TestCircuitBreakerState_String(t *testing.T) {
	tests := []struct {
		state    CircuitState
		expected string
	}{
		{CircuitClosed, "closed"},
		{CircuitOpen, "open"},
		{CircuitHalfOpen, "half-open"},
		{CircuitState(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("CircuitState.String() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestDefaultCircuitBreakerConfig(t *testing.T) {
	cfg := DefaultCircuitBreakerConfig()
	if cfg.MaxFailures != 5 {
		t.Errorf("MaxFailures = %v, want 5", cfg.MaxFailures)
	}
	if cfg.ResetTimeout != 30*time.Second {
		t.Errorf("ResetTimeout = %v, want 30s", cfg.ResetTimeout)
	}
}

func TestCircuitBreaker_InitialState(t *testing.T) {
	cb := DefaultCircuitBreaker()
	if cb.State() != "closed" {
		t.Errorf("initial state = %v, want closed", cb.State())
	}
	if cb.FailureCount() != 0 {
		t.Errorf("initial failure count = %v, want 0", cb.FailureCount())
	}
}

func TestCircuitBreaker_SuccessfulCalls(t *testing.T) {
	cb := DefaultCircuitBreaker()

	// Multiple successful calls should keep circuit closed
	for i := 0; i < 10; i++ {
		err := cb.Call(func() error {
			return nil
		})
		if err != nil {
			t.Errorf("unexpected error on call %d: %v", i, err)
		}
	}

	if cb.State() != "closed" {
		t.Errorf("state after successes = %v, want closed", cb.State())
	}
	if cb.FailureCount() != 0 {
		t.Errorf("failure count after successes = %v, want 0", cb.FailureCount())
	}
}

func TestCircuitBreaker_OpensAfterFailures(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:  3,
		ResetTimeout: 30 * time.Second,
	})

	testErr := errors.New("test error")

	// First 3 failures should keep circuit closed (not yet at threshold)
	for i := 0; i < 3; i++ {
		err := cb.Call(func() error {
			return testErr
		})
		if err == nil || err.Error() != testErr.Error() {
			t.Errorf("expected test error on call %d, got: %v", i, err)
		}
	}

	// Circuit should now be open
	if cb.State() != "open" {
		t.Errorf("state after 3 failures = %v, want open", cb.State())
	}

	// Next call should fail immediately with circuit open error
	err := cb.Call(func() error {
		t.Error("function should not be called when circuit is open")
		return nil
	})
	if err == nil {
		t.Error("expected error when circuit is open, got nil")
	}
	if err.Error() == testErr.Error() {
		t.Error("expected circuit breaker error, got original error")
	}
}

func TestCircuitBreaker_Reset(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:  1,
		ResetTimeout: 30 * time.Second,
	})

	// Open the circuit
	cb.Call(func() error {
		return errors.New("test error")
	})

	if cb.State() != "open" {
		t.Fatal("circuit should be open")
	}

	// Reset the circuit
	cb.Reset()

	if cb.State() != "closed" {
		t.Errorf("state after reset = %v, want closed", cb.State())
	}
	if cb.FailureCount() != 0 {
		t.Errorf("failure count after reset = %v, want 0", cb.FailureCount())
	}

	// Should be able to make successful calls again
	err := cb.Call(func() error {
		return nil
	})
	if err != nil {
		t.Errorf("unexpected error after reset: %v", err)
	}
}

func TestCircuitBreaker_HalfOpen_TransitionsToClosed(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:  1,
		ResetTimeout: 100 * time.Millisecond,
	})

	// Open the circuit
	cb.Call(func() error {
		return errors.New("test error")
	})

	if cb.State() != "open" {
		t.Fatal("circuit should be open")
	}

	// Wait for reset timeout
	time.Sleep(150 * time.Millisecond)

	// Next call should transition to half-open and allow the call
	err := cb.Call(func() error {
		return nil // Success
	})
	if err != nil {
		t.Errorf("unexpected error in half-open: %v", err)
	}

	// Circuit should now be closed
	if cb.State() != "closed" {
		t.Errorf("state after success in half-open = %v, want closed", cb.State())
	}
}

func TestCircuitBreaker_HalfOpen_TransitionsBackToOpen(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:  1,
		ResetTimeout: 100 * time.Millisecond,
	})

	// Open the circuit
	cb.Call(func() error {
		return errors.New("test error")
	})

	if cb.State() != "open" {
		t.Fatal("circuit should be open")
	}

	// Wait for reset timeout
	time.Sleep(150 * time.Millisecond)

	// Next call should transition to half-open but fail
	err := cb.Call(func() error {
		return errors.New("still failing")
	})
	if err == nil {
		t.Error("expected error")
	}

	// Circuit should be open again
	if cb.State() != "open" {
		t.Errorf("state after failure in half-open = %v, want open", cb.State())
	}
}

func TestCircuitBreaker_ConcurrentAccess(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:  10,
		ResetTimeout: 30 * time.Second,
	})

	// Run multiple concurrent calls
	done := make(chan bool, 100)
	for i := 0; i < 100; i++ {
		go func() {
			cb.Call(func() error {
				return nil
			})
			done <- true
		}()
	}

	// Wait for all to complete
	for i := 0; i < 100; i++ {
		<-done
	}

	// Circuit should still be closed
	if cb.State() != "closed" {
		t.Errorf("state after concurrent calls = %v, want closed", cb.State())
	}
}

func TestCircuitBreaker_FailureCountResetOnSuccess(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:  5,
		ResetTimeout: 30 * time.Second,
	})

	// 3 failures
	for i := 0; i < 3; i++ {
		cb.Call(func() error {
			return errors.New("test error")
		})
	}

	if cb.FailureCount() != 3 {
		t.Errorf("failure count = %v, want 3", cb.FailureCount())
	}

	// 1 success should reset failure count
	err := cb.Call(func() error {
		return nil
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if cb.FailureCount() != 0 {
		t.Errorf("failure count after success = %v, want 0", cb.FailureCount())
	}

	// Need 5 more failures to open circuit
	for i := 0; i < 5; i++ {
		cb.Call(func() error {
			return errors.New("test error")
		})
	}

	if cb.State() != "open" {
		t.Errorf("state after 5 more failures = %v, want open", cb.State())
	}
}

func TestNewCircuitBreaker_WithCustomConfig(t *testing.T) {
	cfg := CircuitBreakerConfig{
		MaxFailures:  10,
		ResetTimeout: 1 * time.Minute,
	}
	cb := NewCircuitBreaker(cfg)

	if cb.config.MaxFailures != 10 {
		t.Errorf("MaxFailures = %v, want 10", cb.config.MaxFailures)
	}
	if cb.config.ResetTimeout != 1*time.Minute {
		t.Errorf("ResetTimeout = %v, want 1m", cb.config.ResetTimeout)
	}
}
