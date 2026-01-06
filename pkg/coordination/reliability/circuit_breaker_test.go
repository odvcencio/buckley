package reliability

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// TestCircuitBreakerClosedState tests that the circuit remains closed with successes
func TestCircuitBreakerClosedState(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:      3,
		Timeout:          100 * time.Millisecond,
		SuccessThreshold: 2,
	})

	// Execute successful operations
	for i := 0; i < 10; i++ {
		err := cb.Execute(func() error {
			return nil
		})
		if err != nil {
			t.Errorf("Expected success, got error: %v", err)
		}
	}

	// Verify circuit is still closed
	state := cb.State()
	if state != CircuitClosed {
		t.Errorf("Expected circuit to be Closed, got %v", state)
	}
}

// TestCircuitBreakerOpensAfterMaxFailures tests that circuit opens after MaxFailures
func TestCircuitBreakerOpensAfterMaxFailures(t *testing.T) {
	maxFailures := 3
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:      maxFailures,
		Timeout:          100 * time.Millisecond,
		SuccessThreshold: 2,
	})

	testErr := errors.New("test error")

	// Execute failing operations until circuit opens
	for i := 0; i < maxFailures; i++ {
		err := cb.Execute(func() error {
			return testErr
		})
		if err == nil {
			t.Errorf("Expected error, got nil")
		}
	}

	// Verify circuit is now open
	state := cb.State()
	if state != CircuitOpen {
		t.Errorf("Expected circuit to be Open, got %v", state)
	}

	// Next execution should fail immediately with ErrCircuitOpen
	err := cb.Execute(func() error {
		return nil
	})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("Expected ErrCircuitOpen, got %v", err)
	}
}

// TestCircuitBreakerHalfOpenTransition tests that circuit transitions to half-open after timeout
func TestCircuitBreakerHalfOpenTransition(t *testing.T) {
	timeout := 100 * time.Millisecond
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:      2,
		Timeout:          timeout,
		SuccessThreshold: 2,
	})

	testErr := errors.New("test error")

	// Open the circuit
	for i := 0; i < 2; i++ {
		cb.Execute(func() error {
			return testErr
		})
	}

	// Verify circuit is open
	if cb.State() != CircuitOpen {
		t.Errorf("Expected circuit to be Open")
	}

	// Wait for timeout
	time.Sleep(timeout + 10*time.Millisecond)

	// Next execution should trigger half-open state
	err := cb.Execute(func() error {
		return nil
	})
	if err != nil {
		t.Errorf("Expected success in half-open state, got error: %v", err)
	}

	// Circuit should be in half-open state after successful execution
	state := cb.State()
	if state != CircuitHalfOpen && state != CircuitClosed {
		t.Errorf("Expected circuit to be HalfOpen or Closed, got %v", state)
	}
}

// TestCircuitBreakerClosesAfterSuccessThreshold tests that circuit closes after SuccessThreshold successes
func TestCircuitBreakerClosesAfterSuccessThreshold(t *testing.T) {
	successThreshold := 3
	timeout := 50 * time.Millisecond
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:      2,
		Timeout:          timeout,
		SuccessThreshold: successThreshold,
	})

	testErr := errors.New("test error")

	// Open the circuit
	for i := 0; i < 2; i++ {
		cb.Execute(func() error {
			return testErr
		})
	}

	// Wait for timeout to enter half-open state
	time.Sleep(timeout + 10*time.Millisecond)

	// Execute successful operations
	for i := 0; i < successThreshold; i++ {
		err := cb.Execute(func() error {
			return nil
		})
		if err != nil {
			t.Errorf("Expected success, got error: %v", err)
		}
	}

	// Verify circuit is now closed
	state := cb.State()
	if state != CircuitClosed {
		t.Errorf("Expected circuit to be Closed after %d successes, got %v", successThreshold, state)
	}
}

// TestCircuitBreakerConcurrentExecution tests thread-safety with concurrent execution
func TestCircuitBreakerConcurrentExecution(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:      10,
		Timeout:          100 * time.Millisecond,
		SuccessThreshold: 5,
	})

	const numGoroutines = 100
	const opsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	successCount := 0
	errorCount := 0
	var mu sync.Mutex

	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				err := cb.Execute(func() error {
					// Simulate work
					time.Sleep(time.Microsecond)
					return nil
				})

				mu.Lock()
				if err == nil {
					successCount++
				} else {
					errorCount++
				}
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	// Verify that operations completed without panics
	totalOps := numGoroutines * opsPerGoroutine
	if successCount+errorCount != totalOps {
		t.Errorf("Expected %d total operations, got %d", totalOps, successCount+errorCount)
	}

	// Circuit should still be in a valid state
	state := cb.State()
	if state != CircuitClosed && state != CircuitOpen && state != CircuitHalfOpen {
		t.Errorf("Invalid circuit state: %v", state)
	}
}

// TestCircuitBreakerStateTransitions tests various state transitions
func TestCircuitBreakerStateTransitions(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:      2,
		Timeout:          50 * time.Millisecond,
		SuccessThreshold: 2,
	})

	// Initial state should be closed
	if cb.State() != CircuitClosed {
		t.Errorf("Expected initial state to be Closed")
	}

	testErr := errors.New("test error")

	// Transition to Open
	cb.Execute(func() error { return testErr })
	cb.Execute(func() error { return testErr })

	if cb.State() != CircuitOpen {
		t.Errorf("Expected state to be Open after failures")
	}

	// Wait for timeout
	time.Sleep(60 * time.Millisecond)

	// Execute success to transition to HalfOpen
	cb.Execute(func() error { return nil })

	state := cb.State()
	if state != CircuitHalfOpen && state != CircuitClosed {
		t.Errorf("Expected state to be HalfOpen or Closed, got %v", state)
	}

	// Execute one more success to potentially close
	cb.Execute(func() error { return nil })

	// Should be closed now
	if cb.State() != CircuitClosed {
		t.Errorf("Expected state to be Closed after success threshold")
	}
}

// TestCircuitBreakerHalfOpenFailure tests that circuit reopens on failure in half-open state
func TestCircuitBreakerHalfOpenFailure(t *testing.T) {
	timeout := 50 * time.Millisecond
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:      2,
		Timeout:          timeout,
		SuccessThreshold: 2,
	})

	testErr := errors.New("test error")

	// Open the circuit
	for i := 0; i < 2; i++ {
		cb.Execute(func() error {
			return testErr
		})
	}

	// Wait for timeout
	time.Sleep(timeout + 10*time.Millisecond)

	// Fail in half-open state
	cb.Execute(func() error {
		return testErr
	})

	// Circuit should go back to open
	state := cb.State()
	if state != CircuitOpen {
		t.Errorf("Expected circuit to reopen after failure in half-open state, got %v", state)
	}
}

// TestCircuitBreakerMetrics tests that metrics are tracked correctly
func TestCircuitBreakerMetrics(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:      3,
		Timeout:          100 * time.Millisecond,
		SuccessThreshold: 2,
	})

	testErr := errors.New("test error")

	// Execute some operations
	cb.Execute(func() error { return nil })
	cb.Execute(func() error { return testErr })
	cb.Execute(func() error { return nil })

	// Get metrics
	metrics := cb.Metrics()

	expectedTotal := 3
	if metrics.TotalCalls != expectedTotal {
		t.Errorf("Expected %d total calls, got %d", expectedTotal, metrics.TotalCalls)
	}

	if metrics.SuccessCount+metrics.FailureCount != expectedTotal {
		t.Errorf("Success + Failure counts should equal total calls")
	}
}
