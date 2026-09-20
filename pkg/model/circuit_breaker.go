package model

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// CircuitState represents the state of the circuit breaker
type CircuitState int

const (
	// CircuitClosed allows requests to pass through
	CircuitClosed CircuitState = iota
	// CircuitOpen blocks all requests
	CircuitOpen
	// CircuitHalfOpen allows a test request to check if service recovered
	CircuitHalfOpen
)

// String returns the string representation of the circuit state
func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig holds configuration for the circuit breaker
type CircuitBreakerConfig struct {
	// MaxFailures is the number of consecutive failures before opening the circuit
	MaxFailures uint32
	// ResetTimeout is the duration to wait before transitioning from open to half-open
	ResetTimeout time.Duration
}

// DefaultCircuitBreakerConfig returns sensible defaults
func DefaultCircuitBreakerConfig() CircuitBreakerConfig {
	return CircuitBreakerConfig{
		MaxFailures:  5,
		ResetTimeout: 30 * time.Second,
	}
}

// CircuitBreaker implements the circuit breaker pattern for API resilience
type CircuitBreaker struct {
	config CircuitBreakerConfig

	state           CircuitState
	failureCount    uint32
	lastFailureTime time.Time
	halfOpenProbe   atomic.Bool
	generation      uint64

	mu sync.RWMutex
}

type circuitBreakerTransition struct {
	oldState               CircuitState
	newState               CircuitState
	reason                 string
	resetTimeout           time.Duration
	includeResetTimeout    bool
	consecutiveFailures    uint32
	includeConsecutiveNums bool
}

func (t circuitBreakerTransition) log() {
	args := []any{"old_state", t.oldState.String(), "new_state", t.newState.String()}
	if t.includeResetTimeout {
		args = append(args, "reset_timeout", t.resetTimeout)
	}
	if t.reason != "" {
		args = append(args, "reason", t.reason)
	}
	if t.includeConsecutiveNums {
		args = append(args, "consecutive_failures", t.consecutiveFailures)
	}
	slog.Warn("circuit breaker state transition", args...)
}

// NewCircuitBreaker creates a new circuit breaker with the given configuration
func NewCircuitBreaker(config CircuitBreakerConfig) *CircuitBreaker {
	return &CircuitBreaker{
		config: config,
		state:  CircuitClosed,
	}
}

// DefaultCircuitBreaker creates a circuit breaker with default settings
func DefaultCircuitBreaker() *CircuitBreaker {
	return NewCircuitBreaker(DefaultCircuitBreakerConfig())
}

// State returns the current state of the circuit breaker
func (cb *CircuitBreaker) State() string {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state.String()
}

// Reset manually resets the circuit breaker to closed state
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	oldState := cb.state
	cb.generation++
	cb.state = CircuitClosed
	cb.failureCount = 0
	cb.lastFailureTime = time.Time{}
	cb.halfOpenProbe.Store(false)
	cb.mu.Unlock()

	if oldState != CircuitClosed {
		slog.Warn("circuit breaker manually reset", "old_state", oldState.String(), "new_state", "closed")
	}
}

// Call wraps a function call with circuit breaker logic
// Returns an error if the circuit is open, otherwise executes the function
func (cb *CircuitBreaker) Call(fn func() error) (err error) {
	cb.mu.Lock()

	ownsHalfOpenProbe := false
	var admissionTransition *circuitBreakerTransition

	// Check if we should transition from open to half-open
	if cb.state == CircuitOpen {
		if time.Since(cb.lastFailureTime) >= cb.config.ResetTimeout {
			// Only allow one goroutine to probe in half-open state
			if !cb.halfOpenProbe.CompareAndSwap(false, true) {
				cb.mu.Unlock()
				return fmt.Errorf("circuit breaker is open (half-open probe in progress)")
			}
			ownsHalfOpenProbe = true
			cb.state = CircuitHalfOpen
			cb.failureCount = 0
			cb.generation++
			admissionTransition = &circuitBreakerTransition{
				oldState:            CircuitOpen,
				newState:            CircuitHalfOpen,
				resetTimeout:        cb.config.ResetTimeout,
				includeResetTimeout: true,
			}
		} else {
			failureAge := time.Since(cb.lastFailureTime)
			cb.mu.Unlock()
			return fmt.Errorf("circuit breaker is open (last failure: %v ago)", failureAge)
		}
	} else if cb.state == CircuitHalfOpen {
		// Admit one replacement probe after a neutral outcome releases the
		// previous one, and reject every other caller while it is in flight.
		if !cb.halfOpenProbe.CompareAndSwap(false, true) {
			cb.mu.Unlock()
			return fmt.Errorf("circuit breaker is open (half-open probe in progress)")
		}
		ownsHalfOpenProbe = true
	}

	generation := cb.generation
	cb.mu.Unlock()

	completed := false
	defer func() {
		cb.finishCall(generation, ownsHalfOpenProbe, err, !completed)
	}()

	if admissionTransition != nil {
		admissionTransition.log()
	}
	err = fn()
	completed = true
	return err
}

func (cb *CircuitBreaker) finishCall(generation uint64, ownsHalfOpenProbe bool, err error, incomplete bool) {
	cb.mu.Lock()
	if generation != cb.generation {
		cb.mu.Unlock()
		return
	}

	// Any call path that does not return normally is neutral to breaker
	// health, but an in-flight recovery probe must still be released so a
	// replacement probe can run. This covers panics, runtime.Goexit, and
	// legacy panic(nil) without intercepting the original control flow.
	if incomplete {
		if cb.state == CircuitHalfOpen && ownsHalfOpenProbe {
			cb.halfOpenProbe.Store(false)
		}
		cb.mu.Unlock()
		return
	}

	// Caller-initiated cancellation is neutral: it says nothing about
	// service health. DeadlineExceeded is NOT exempt and still counts
	// as a failure. A canceled half-open probe returns to open so it can
	// be retried, but it does not update the failure timestamp.
	if errors.Is(err, context.Canceled) {
		var transition *circuitBreakerTransition
		if cb.state == CircuitHalfOpen && ownsHalfOpenProbe {
			transition = &circuitBreakerTransition{
				oldState: cb.state,
				newState: CircuitOpen,
				reason:   "probe canceled",
			}
			cb.state = CircuitOpen
			cb.generation++
			cb.halfOpenProbe.Store(false)
		}
		cb.mu.Unlock()
		if transition != nil {
			transition.log()
		}
		return
	}

	var transition *circuitBreakerTransition
	var resetFailureCount bool
	if cb.state == CircuitHalfOpen {
		if !ownsHalfOpenProbe {
			// A call admitted in an earlier state cannot decide the current
			// recovery probe. The generation check above normally handles this;
			// retain the ownership check as a second guard.
			cb.mu.Unlock()
			return
		}
		if err != nil {
			transition = cb.recordFailureLocked()
		} else {
			transition, resetFailureCount = cb.recordSuccessLocked()
		}
	} else if err != nil {
		transition = cb.recordFailureLocked()
	} else {
		transition, resetFailureCount = cb.recordSuccessLocked()
	}
	cb.mu.Unlock()

	if transition != nil {
		transition.log()
	}
	if resetFailureCount {
		slog.Debug("circuit breaker failure count reset", "reason", "successful call")
	}
}

// recordFailureLocked records a failure and transitions state if needed.
// Must be called with lock held
func (cb *CircuitBreaker) recordFailureLocked() *circuitBreakerTransition {
	cb.failureCount++
	cb.lastFailureTime = time.Now()

	switch cb.state {
	case CircuitHalfOpen:
		// Failure in half-open state goes back to open
		transition := &circuitBreakerTransition{
			oldState: cb.state,
			newState: CircuitOpen,
			reason:   "failure in test request",
		}
		cb.state = CircuitOpen
		cb.generation++
		cb.halfOpenProbe.Store(false)
		return transition
	case CircuitClosed:
		// Check if we should open the circuit
		if cb.failureCount >= cb.config.MaxFailures {
			transition := &circuitBreakerTransition{
				oldState:               cb.state,
				newState:               CircuitOpen,
				consecutiveFailures:    cb.config.MaxFailures,
				includeConsecutiveNums: true,
			}
			cb.state = CircuitOpen
			cb.generation++
			return transition
		}
	}
	return nil
}

// recordSuccessLocked records a success and transitions state if needed.
// Must be called with lock held
func (cb *CircuitBreaker) recordSuccessLocked() (*circuitBreakerTransition, bool) {
	switch cb.state {
	case CircuitHalfOpen:
		// Success in half-open state closes the circuit
		transition := &circuitBreakerTransition{
			oldState: cb.state,
			newState: CircuitClosed,
			reason:   "service recovered",
		}
		cb.state = CircuitClosed
		cb.failureCount = 0
		cb.lastFailureTime = time.Time{}
		cb.generation++
		cb.halfOpenProbe.Store(false)
		return transition, false
	case CircuitClosed:
		// Reset failure count on success in closed state
		if cb.failureCount > 0 {
			cb.failureCount = 0
			return nil, true
		}
	}
	return nil, false
}

// FailureCount returns the current failure count (for testing/monitoring)
func (cb *CircuitBreaker) FailureCount() uint32 {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.failureCount
}

// LastFailureTime returns the time of the last failure (for testing/monitoring)
func (cb *CircuitBreaker) LastFailureTime() time.Time {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.lastFailureTime
}
