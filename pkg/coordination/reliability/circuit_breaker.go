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

	// OnFailure is called after recording a failure and committing its state change.
	OnFailure func(FailureEvent)
	// OnStateChange is called when the circuit state changes
	OnStateChange func(StateChangeEvent)
	// MaxRecentErrors is the number of recent errors to track (default 5)
	MaxRecentErrors int
}

// CircuitBreaker implements the circuit breaker pattern for fault tolerance
type CircuitBreaker struct {
	config CircuitBreakerConfig

	mu            sync.RWMutex
	generation    uint64
	probeInFlight bool
	state         CircuitState
	failures      int
	successes     int
	lastFailTime  time.Time
	totalCalls    int
	successCount  int
	failureCount  int
	lastError     error
	recentErrors  []error
	openedAt      time.Time
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
	return cb.ExecuteWithResultFilter(fn, nil)
}

// ExecuteWithResultFilter runs the given function through the circuit breaker.
// When shouldRecord returns false for the function's error, the call remains
// admitted and returned to the caller but does not update success/failure
// counters or transition circuit state.
func (cb *CircuitBreaker) ExecuteWithResultFilter(fn func() error, shouldRecord func(error) bool) error {
	generation, transition, openErr := cb.canExecute()
	if openErr != nil {
		return openErr
	}

	var err error
	record := false
	defer func() { cb.recordResult(generation, err, record) }()
	cb.notifyStateChange(transition)
	err = fn()
	record = shouldRecord == nil || shouldRecord(err)
	return err
}

func (cb *CircuitBreaker) canExecute() (uint64, *StateChangeEvent, error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.totalCalls++

	var transition *StateChangeEvent
	if cb.state == CircuitOpen && time.Since(cb.lastFailTime) > cb.config.Timeout {
		transition = cb.transitionLocked(CircuitHalfOpen, "timeout elapsed, testing recovery")
		cb.successes = 0
	}
	if cb.state == CircuitOpen || (cb.state == CircuitHalfOpen && cb.probeInFlight) {
		retryAfter := max(cb.config.Timeout-time.Since(cb.openedAt), 0)
		return 0, nil, &CircuitOpenError{
			Failures:     cb.failures,
			LastError:    cb.lastError,
			OpenedAt:     cb.openedAt,
			RetryAfter:   retryAfter,
			RecentErrors: append([]error(nil), cb.recentErrors...),
		}
	}
	if cb.state == CircuitHalfOpen {
		cb.probeInFlight = true
	}
	return cb.generation, transition, nil
}

func (cb *CircuitBreaker) recordResult(generation uint64, err error, record bool) {
	cb.mu.Lock()
	if record {
		if err != nil {
			cb.failureCount++
		} else {
			cb.successCount++
		}
	}
	// Completed calls remain in lifetime metrics, but an earlier admission
	// cannot change a newer recovery cycle or release its probe.
	if generation != cb.generation {
		cb.mu.Unlock()
		return
	}
	cb.probeInFlight = false
	if !record {
		cb.mu.Unlock()
		return
	}

	var failure *FailureEvent
	var transition *StateChangeEvent
	if err != nil {
		cb.failures++
		cb.lastFailTime = time.Now()
		cb.lastError = err
		if len(cb.recentErrors) >= cb.config.MaxRecentErrors {
			copy(cb.recentErrors, cb.recentErrors[1:])
			cb.recentErrors = cb.recentErrors[:len(cb.recentErrors)-1]
		}
		cb.recentErrors = append(cb.recentErrors, err)
		cb.successes = 0
		willOpen := cb.state == CircuitHalfOpen || cb.failures >= cb.config.MaxFailures
		failure = &FailureEvent{
			Error: err, ConsecutiveNum: cb.failures,
			MaxFailures: cb.config.MaxFailures, WillOpen: willOpen,
		}
		if willOpen {
			cb.openedAt = cb.lastFailTime
			transition = cb.transitionLocked(CircuitOpen, fmt.Sprintf("%d consecutive failures", cb.failures))
		}
	} else {
		cb.successes++
		if cb.state == CircuitClosed {
			cb.failures = 0
			cb.recentErrors = cb.recentErrors[:0]
		} else if cb.state == CircuitHalfOpen && cb.successes >= cb.config.SuccessThreshold {
			cb.failures = 0
			cb.successes = 0
			cb.recentErrors = cb.recentErrors[:0]
			transition = cb.transitionLocked(CircuitClosed, "recovered after successful requests")
		}
	}
	cb.mu.Unlock()

	if failure != nil && cb.config.OnFailure != nil {
		cb.config.OnFailure(*failure)
	}
	cb.notifyStateChange(transition)
}

// transitionLocked commits state before observers run and invalidates earlier admissions.
func (cb *CircuitBreaker) transitionLocked(to CircuitState, reason string) *StateChangeEvent {
	event := &StateChangeEvent{From: cb.state, To: to, Reason: reason, LastError: cb.lastError}
	cb.state = to
	cb.generation++
	return event
}

func (cb *CircuitBreaker) notifyStateChange(event *StateChangeEvent) {
	if event != nil && cb.config.OnStateChange != nil {
		cb.config.OnStateChange(*event)
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

	cb.generation++
	cb.probeInFlight = false
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
