package reliability

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrCircuitOpen is returned when the circuit breaker is open
var ErrCircuitOpen = errors.New("circuit breaker is open")

// CircuitOpenError provides detailed information when the circuit is open.
type CircuitOpenError struct {
	Failures     int
	LastError    error
	OpenedAt     time.Time
	RetryAfter   time.Duration
	RecentErrors []error
}

func (e *CircuitOpenError) Error() string {
	msg := fmt.Sprintf("circuit breaker is open: %d consecutive failures", e.Failures)
	if e.LastError != nil {
		msg += fmt.Sprintf(", last error: %v", e.LastError)
	}
	if e.RetryAfter > 0 {
		msg += fmt.Sprintf(", retry after %v", e.RetryAfter.Round(time.Second))
	}
	return msg
}

func (e *CircuitOpenError) Unwrap() error {
	return ErrCircuitOpen
}

// FailureEvent is emitted when a failure is recorded.
type FailureEvent struct {
	Error          error
	ConsecutiveNum int
	MaxFailures    int
	WillOpen       bool
}

// StateChangeEvent is emitted when the circuit state changes.
type StateChangeEvent struct {
	From      CircuitState
	To        CircuitState
	Reason    string
	LastError error
}

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

	// OnFailure is called when a failure is recorded (before state change)
	OnFailure func(FailureEvent)
	// OnStateChange is called when the circuit state changes
	OnStateChange func(StateChangeEvent)
	// MaxRecentErrors is the number of recent errors to track (default 5)
	MaxRecentErrors int
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
	lastError    error
	recentErrors []error
	openedAt     time.Time
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
	if config.MaxRecentErrors <= 0 {
		config.MaxRecentErrors = 5
	}

	return &CircuitBreaker{
		config:       config,
		state:        CircuitClosed,
		recentErrors: make([]error, 0, config.MaxRecentErrors),
	}
}

// Execute runs the given function through the circuit breaker
func (cb *CircuitBreaker) Execute(fn func() error) error {
	cb.mu.Lock()
	cb.totalCalls++
	cb.mu.Unlock()

	if openErr := cb.canExecute(); openErr != nil {
		return openErr
	}

	err := fn()
	cb.recordResult(err)
	return err
}

// canExecute checks if the circuit breaker allows execution.
// Returns nil if execution is allowed, or a detailed error if circuit is open.
func (cb *CircuitBreaker) canExecute() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitClosed:
		return nil
	case CircuitOpen:
		// Check if timeout has elapsed
		if time.Since(cb.lastFailTime) > cb.config.Timeout {
			oldState := cb.state
			cb.state = CircuitHalfOpen
			cb.successes = 0
			cb.notifyStateChange(oldState, CircuitHalfOpen, "timeout elapsed, testing recovery")
			return nil
		}
		// Return detailed error
		retryAfter := cb.config.Timeout - time.Since(cb.openedAt)
		if retryAfter < 0 {
			retryAfter = 0
		}
		recentErrs := make([]error, len(cb.recentErrors))
		copy(recentErrs, cb.recentErrors)
		return &CircuitOpenError{
			Failures:     cb.failures,
			LastError:    cb.lastError,
			OpenedAt:     cb.openedAt,
			RetryAfter:   retryAfter,
			RecentErrors: recentErrs,
		}
	case CircuitHalfOpen:
		return nil
	}
	return nil
}

// recordResult updates the circuit breaker state based on the result
func (cb *CircuitBreaker) recordResult(err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err != nil {
		cb.failures++
		cb.failureCount++
		cb.lastFailTime = time.Now()
		cb.lastError = err

		// Track recent errors
		if len(cb.recentErrors) >= cb.config.MaxRecentErrors {
			// Shift errors left, drop oldest
			copy(cb.recentErrors, cb.recentErrors[1:])
			cb.recentErrors = cb.recentErrors[:len(cb.recentErrors)-1]
		}
		cb.recentErrors = append(cb.recentErrors, err)

		// Reset successes on any failure
		cb.successes = 0

		willOpen := cb.failures >= cb.config.MaxFailures
		cb.notifyFailure(err, cb.failures, willOpen)

		// Open circuit if we've exceeded max failures
		if willOpen {
			oldState := cb.state
			cb.state = CircuitOpen
			cb.openedAt = time.Now()
			cb.notifyStateChange(oldState, CircuitOpen, fmt.Sprintf("%d consecutive failures", cb.failures))
		}
	} else {
		cb.successCount++
		cb.successes++

		// Handle state transitions on success
		switch cb.state {
		case CircuitClosed:
			// Reset failure count on success
			cb.failures = 0
			cb.recentErrors = cb.recentErrors[:0] // Clear recent errors
		case CircuitHalfOpen:
			// Check if we should close the circuit
			if cb.successes >= cb.config.SuccessThreshold {
				oldState := cb.state
				cb.state = CircuitClosed
				cb.failures = 0
				cb.successes = 0
				cb.recentErrors = cb.recentErrors[:0]
				cb.notifyStateChange(oldState, CircuitClosed, "recovered after successful requests")
			}
		case CircuitOpen:
			// This shouldn't happen, but handle it gracefully
			oldState := cb.state
			cb.state = CircuitHalfOpen
			cb.successes = 1
			cb.notifyStateChange(oldState, CircuitHalfOpen, "unexpected success while open")
		}
	}
}

// notifyFailure calls the OnFailure callback if configured.
// Must be called with mu held.
func (cb *CircuitBreaker) notifyFailure(err error, consecutiveNum int, willOpen bool) {
	if cb.config.OnFailure == nil {
		return
	}
	// Call callback without lock to prevent deadlock
	cb.mu.Unlock()
	cb.config.OnFailure(FailureEvent{
		Error:          err,
		ConsecutiveNum: consecutiveNum,
		MaxFailures:    cb.config.MaxFailures,
		WillOpen:       willOpen,
	})
	cb.mu.Lock()
}

// notifyStateChange calls the OnStateChange callback if configured.
// Must be called with mu held.
func (cb *CircuitBreaker) notifyStateChange(from, to CircuitState, reason string) {
	if cb.config.OnStateChange == nil {
		return
	}
	lastErr := cb.lastError
	// Call callback without lock to prevent deadlock
	cb.mu.Unlock()
	cb.config.OnStateChange(StateChangeEvent{
		From:      from,
		To:        to,
		Reason:    reason,
		LastError: lastErr,
	})
	cb.mu.Lock()
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
	cb.lastError = nil
	cb.recentErrors = cb.recentErrors[:0]
	cb.openedAt = time.Time{}
}

// LastError returns the most recent error recorded.
func (cb *CircuitBreaker) LastError() error {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.lastError
}

// RecentErrors returns a copy of recent errors.
func (cb *CircuitBreaker) RecentErrors() []error {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	result := make([]error, len(cb.recentErrors))
	copy(result, cb.recentErrors)
	return result
}

// ConsecutiveFailures returns the current consecutive failure count.
func (cb *CircuitBreaker) ConsecutiveFailures() int {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.failures
}
