package reliability

import (
	"errors"
	"sync"
	"time"
)

// ErrCircuitOpen is returned when the circuit breaker is open
var ErrCircuitOpen = errors.New("circuit breaker is open")

// CircuitState represents the state of the circuit breaker
type CircuitState int

const (
	// CircuitClosed means the circuit is closed and requests are allowed
	CircuitClosed CircuitState = iota
	// CircuitOpen means the circuit is open and requests are blocked
	CircuitOpen
	// CircuitHalfOpen means the circuit is testing if it should close
	CircuitHalfOpen
)

// String returns the string representation of the circuit state
func (s CircuitState) String() string {
	switch s {
	case CircuitClosed:
		return "Closed"
	case CircuitOpen:
		return "Open"
	case CircuitHalfOpen:
		return "HalfOpen"
	default:
		return "Unknown"
	}
}

// CircuitBreakerConfig holds the configuration for a circuit breaker
type CircuitBreakerConfig struct {
	// MaxFailures is the number of consecutive failures before opening the circuit
	MaxFailures int
	// Timeout is the duration to wait before transitioning from Open to HalfOpen
	Timeout time.Duration
	// SuccessThreshold is the number of consecutive successes needed to close the circuit from HalfOpen
	SuccessThreshold int
}

// CircuitBreaker implements the circuit breaker pattern for fault tolerance
type CircuitBreaker struct {
	config CircuitBreakerConfig

	mu           sync.RWMutex
	state        CircuitState
	failures     int
	successes    int
	lastFailTime time.Time
	totalCalls   int
	successCount int
	failureCount int
}

// CircuitBreakerMetrics holds metrics about circuit breaker operation
type CircuitBreakerMetrics struct {
	TotalCalls   int
	SuccessCount int
	FailureCount int
	CurrentState CircuitState
}

// NewCircuitBreaker creates a new circuit breaker with the given configuration
func NewCircuitBreaker(config CircuitBreakerConfig) *CircuitBreaker {
	// Set defaults if not provided
	if config.MaxFailures <= 0 {
		config.MaxFailures = 5
	}
	if config.Timeout <= 0 {
		config.Timeout = 60 * time.Second
	}
	if config.SuccessThreshold <= 0 {
		config.SuccessThreshold = 2
	}

	return &CircuitBreaker{
		config: config,
		state:  CircuitClosed,
	}
}

// Execute runs the given function through the circuit breaker
func (cb *CircuitBreaker) Execute(fn func() error) error {
	cb.mu.Lock()
	cb.totalCalls++
	cb.mu.Unlock()

	if !cb.canExecute() {
		return ErrCircuitOpen
	}

	err := fn()
	cb.recordResult(err)
	return err
}

// canExecute checks if the circuit breaker allows execution
func (cb *CircuitBreaker) canExecute() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		return true
	case CircuitOpen:
		// Check if timeout has elapsed
		if time.Since(cb.lastFailTime) > cb.config.Timeout {
			cb.state = CircuitHalfOpen
			cb.successes = 0
			return true
		}
		return false
	case CircuitHalfOpen:
		return true
	}
	return false
}

// recordResult updates the circuit breaker state based on the result
func (cb *CircuitBreaker) recordResult(err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err != nil {
		cb.failures++
		cb.failureCount++
		cb.lastFailTime = time.Now()

		// Reset successes on any failure
		cb.successes = 0

		// Open circuit if we've exceeded max failures
		if cb.failures >= cb.config.MaxFailures {
			cb.state = CircuitOpen
		}
	} else {
		cb.successCount++
		cb.successes++

		// Handle state transitions on success
		switch cb.state {
		case CircuitClosed:
			// Reset failure count on success
			cb.failures = 0
		case CircuitHalfOpen:
			// Check if we should close the circuit
			if cb.successes >= cb.config.SuccessThreshold {
				cb.state = CircuitClosed
				cb.failures = 0
				cb.successes = 0
			}
		case CircuitOpen:
			// This shouldn't happen, but handle it gracefully
			cb.state = CircuitHalfOpen
			cb.successes = 1
		}
	}
}

// State returns the current state of the circuit breaker
func (cb *CircuitBreaker) State() CircuitState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// Metrics returns the current metrics of the circuit breaker
func (cb *CircuitBreaker) Metrics() CircuitBreakerMetrics {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	return CircuitBreakerMetrics{
		TotalCalls:   cb.totalCalls,
		SuccessCount: cb.successCount,
		FailureCount: cb.failureCount,
		CurrentState: cb.state,
	}
}

// Reset resets the circuit breaker to its initial state
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.state = CircuitClosed
	cb.failures = 0
	cb.successes = 0
	cb.lastFailTime = time.Time{}
}
