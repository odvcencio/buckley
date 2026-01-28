package model

import (
	"fmt"
	"log"
	"sync"
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

	state          CircuitState
	failureCount   uint32
	lastFailureTime time.Time

	mu sync.RWMutex
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
	defer cb.mu.Unlock()

	oldState := cb.state
	cb.state = CircuitClosed
	cb.failureCount = 0
	cb.lastFailureTime = time.Time{}

	if oldState != CircuitClosed {
		log.Printf("[circuit-breaker] manually reset from %s to closed", oldState.String())
	}
}

// Call wraps a function call with circuit breaker logic
// Returns an error if the circuit is open, otherwise executes the function
func (cb *CircuitBreaker) Call(fn func() error) error {
	cb.mu.Lock()

	// Check if we should transition from open to half-open
	if cb.state == CircuitOpen {
		if time.Since(cb.lastFailureTime) >= cb.config.ResetTimeout {
			cb.state = CircuitHalfOpen
			cb.failureCount = 0
			log.Printf("[circuit-breaker] transitioning from open to half-open after %v", cb.config.ResetTimeout)
		} else {
			cb.mu.Unlock()
			return fmt.Errorf("circuit breaker is open (last failure: %v ago)", time.Since(cb.lastFailureTime))
		}
	}

	cb.mu.Unlock()

	// Execute the function
	err := fn()

	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err != nil {
		cb.recordFailure()
		return err
	}

	cb.recordSuccess()
	return nil
}

// recordFailure records a failure and transitions state if needed
// Must be called with lock held
func (cb *CircuitBreaker) recordFailure() {
	cb.failureCount++
	cb.lastFailureTime = time.Now()

	switch cb.state {
	case CircuitHalfOpen:
		// Failure in half-open state goes back to open
		cb.state = CircuitOpen
		log.Printf("[circuit-breaker] transitioning from half-open to open (failure in test request)")
	case CircuitClosed:
		// Check if we should open the circuit
		if cb.failureCount >= cb.config.MaxFailures {
			cb.state = CircuitOpen
			log.Printf("[circuit-breaker] transitioning from closed to open after %d consecutive failures", cb.config.MaxFailures)
		}
	}
}

// recordSuccess records a success and transitions state if needed
// Must be called with lock held
func (cb *CircuitBreaker) recordSuccess() {
	switch cb.state {
	case CircuitHalfOpen:
		// Success in half-open state closes the circuit
		cb.state = CircuitClosed
		cb.failureCount = 0
		cb.lastFailureTime = time.Time{}
		log.Printf("[circuit-breaker] transitioning from half-open to closed (service recovered)")
	case CircuitClosed:
		// Reset failure count on success in closed state
		if cb.failureCount > 0 {
			cb.failureCount = 0
			log.Printf("[circuit-breaker] failure count reset after successful call")
		}
	}
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
