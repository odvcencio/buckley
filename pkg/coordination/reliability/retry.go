package reliability

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func cryptoRandFloat64() float64 {
	var b [8]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return 0.5
	}
	n := binary.BigEndian.Uint64(b[:]) >> 11 // 53 bits
	return float64(n) / float64(uint64(1)<<53)
}

// RetryStrategy implements exponential backoff with jitter for retrying failed operations.
// It supports automatic retry for retriable errors (network failures, timeouts, rate limits)
// while failing fast on non-retriable errors (invalid arguments, auth failures).
type RetryStrategy struct {
	// MaxRetries is the maximum number of retry attempts after the initial execution.
	// For example, MaxRetries=3 means up to 4 total attempts (1 initial + 3 retries).
	MaxRetries int

	// BaseDelay is the initial delay before the first retry.
	// Subsequent delays are calculated as: BaseDelay * (Multiplier ^ attempt) + jitter
	BaseDelay time.Duration

	// MaxDelay is the maximum delay between retry attempts.
	// Delays are capped at this value to prevent excessively long waits.
	MaxDelay time.Duration

	// Multiplier is the exponential backoff multiplier (typically 2.0).
	// Each retry delay is Multiplier times the previous delay.
	Multiplier float64
}

// Execute runs the given function with automatic retry on retriable errors.
// It implements exponential backoff with jitter to prevent thundering herd.
//
// The function will be retried up to MaxRetries times on retriable errors.
// Non-retriable errors cause immediate failure without retries.
// Context cancellation stops the retry loop immediately.
//
// Returns nil if the function eventually succeeds, or the last error encountered
// if all retries are exhausted or a non-retriable error occurs.
func (s *RetryStrategy) Execute(ctx context.Context, fn func() error) error {
	var lastErr error
	delay := s.BaseDelay

	for attempt := 0; attempt <= s.MaxRetries; attempt++ {
		// Wait before retry (skip on first attempt)
		if attempt > 0 {
			// Apply jitter: add random variance of ±25% to prevent thundering herd
			jitterFactor := 0.75 + cryptoRandFloat64()*0.5
			jitter := time.Duration(float64(delay) * jitterFactor)

			select {
			case <-time.After(jitter):
				// Continue to retry
			case <-ctx.Done():
				// Context cancelled, stop retrying
				return ctx.Err()
			}

			// Calculate next delay with exponential backoff
			delay = time.Duration(float64(delay) * s.Multiplier)
			if delay > s.MaxDelay {
				delay = s.MaxDelay
			}
		}

		// Execute the function
		err := fn()

		// Success - return immediately
		if err == nil {
			return nil
		}

		// Check if error is retriable
		if !isRetriable(err) {
			// Non-retriable error - fail immediately
			return err
		}

		// Save error for potential return
		lastErr = err
	}

	// All retries exhausted
	return fmt.Errorf("max retries (%d) exceeded: %w", s.MaxRetries, lastErr)
}

// isRetriable determines whether an error should trigger a retry attempt.
// It returns true for transient errors that might succeed on retry:
//   - gRPC Unavailable (service temporarily down)
//   - gRPC DeadlineExceeded (timeout, might succeed with more time)
//   - gRPC ResourceExhausted (rate limit, might succeed after backoff)
//   - context.DeadlineExceeded (timeout at context level)
//
// It returns false for permanent errors that won't benefit from retry:
//   - context.Canceled (user cancelled, don't retry)
//   - gRPC InvalidArgument (bad request, won't change)
//   - gRPC Unauthenticated (auth failure, won't change)
//   - gRPC PermissionDenied (authz failure, won't change)
//   - Non-gRPC errors (unknown type, fail fast)
func isRetriable(err error) bool {
	// Check for context-level errors first
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false
	}

	// Check for gRPC status codes
	st, ok := status.FromError(err)
	if !ok {
		// Not a gRPC error - don't retry by default
		return false
	}

	// Classify gRPC errors
	switch st.Code() {
	case codes.Unavailable:
		// Service temporarily unavailable (network issues, service down)
		return true

	case codes.DeadlineExceeded:
		// Request timeout - might succeed with more time
		return true

	case codes.ResourceExhausted:
		// Rate limit or quota exceeded - might succeed after backoff
		return true

	case codes.Aborted:
		// Operation aborted (e.g., transaction conflict) - might succeed on retry
		return true

	case codes.Internal:
		// Internal server error - might be transient
		return true

	case codes.Unknown:
		// Unknown error - might be transient network issue
		return true

	default:
		// All other codes are permanent errors:
		// - InvalidArgument: bad request
		// - Unauthenticated: auth failure
		// - PermissionDenied: authz failure
		// - NotFound: resource doesn't exist
		// - AlreadyExists: resource exists
		// - FailedPrecondition: state violation
		// - OutOfRange: invalid range
		// - Unimplemented: not supported
		// - DataLoss: data corruption
		return false
	}
}
