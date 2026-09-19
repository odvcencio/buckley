package model

import (
	"errors"
	"fmt"
	"math/rand"
	"time"
)

// RetryConfig configures the retry budgets and backoff for provider HTTP requests.
type RetryConfig struct {
	MaxRetries          int
	MaxRateLimitRetries int
	InitialInterval     time.Duration
	MaxInterval         time.Duration
	Multiplier          float64
}

// DefaultRetryConfig returns the default retry configuration.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:          3,
		MaxRateLimitRetries: 12,
		InitialInterval:     1 * time.Second,
		MaxInterval:         30 * time.Second,
		Multiplier:          2.0,
	}
}

// isRetryableError checks if an error is retryable based on status code.
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Retryable
	}
	// Network errors are generally retryable
	return true
}

func (c RetryConfig) retryLimit(err error) int {
	limit := max(c.MaxRetries, 0)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.IsRateLimitError() && c.MaxRateLimitRetries > limit {
		limit = c.MaxRateLimitRetries
	}
	return limit
}

func (c RetryConfig) canRetry(attempt int, err error) bool {
	return isRetryableError(err) && attempt < c.retryLimit(err)
}

func retryExhaustedError(attempt int, err error) error {
	return fmt.Errorf("model request retries exhausted after %d attempts: %w", attempt+1, err)
}

func (c RetryConfig) retryDelay(attempt int, lastErr error) time.Duration {
	// Check if error has Retry-After header
	var apiErr *APIError
	if errors.As(lastErr, &apiErr) && apiErr.RetryAfter > 0 {
		// Retry-After is the provider's earliest acceptable retry time, not a
		// backoff suggestion. The caller's context remains the upper bound.
		return apiErr.RetryAfter
	}

	if attempt <= 0 {
		return c.InitialInterval
	}

	// Exponential backoff with positive jitter avoids retrying before the
	// calculated delay while spreading concurrent clients across the window.
	delay := float64(c.InitialInterval)
	for i := 0; i < attempt-1; i++ {
		delay *= c.Multiplier
	}
	delay = min(delay, float64(c.MaxInterval))
	if delay < float64(c.MaxInterval) {
		delay += rand.Float64() * delay * 0.2
	}
	return min(time.Duration(delay), c.MaxInterval)
}
