package model

import (
	"testing"
	"time"
)

func TestRetryConfig_RetryDelay(t *testing.T) {
	config := DefaultRetryConfig()

	tests := []struct {
		name        string
		attempt     int
		lastErr     error
		expectedMin time.Duration
		expectedMax time.Duration
	}{
		{
			name:        "first_retry",
			attempt:     1,
			lastErr:     nil,
			expectedMin: 1 * time.Second,
			expectedMax: 1200 * time.Millisecond,
		},
		{
			name:        "second_retry",
			attempt:     2,
			lastErr:     nil,
			expectedMin: 2 * time.Second,
			expectedMax: 2400 * time.Millisecond,
		},
		{
			name:        "third_retry",
			attempt:     3,
			lastErr:     nil,
			expectedMin: 4 * time.Second,
			expectedMax: 4800 * time.Millisecond,
		},
		{
			name:        "large_attempt_capped",
			attempt:     10,
			lastErr:     nil,
			expectedMin: 30 * time.Second,
			expectedMax: 30 * time.Second,
		},
		{
			name:    "with_retry_after_header",
			attempt: 1,
			lastErr: &APIError{
				StatusCode: 429,
				RetryAfter: 5 * time.Second,
			},
			expectedMin: 5 * time.Second,
			expectedMax: 5 * time.Second,
		},
		{
			name:    "retry_after_honored_beyond_max_backoff",
			attempt: 1,
			lastErr: &APIError{
				StatusCode: 429,
				RetryAfter: 60 * time.Second,
			},
			expectedMin: 60 * time.Second,
			expectedMax: 60 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delay := config.retryDelay(tt.attempt, tt.lastErr)
			if delay < tt.expectedMin || delay > tt.expectedMax {
				t.Errorf("retryDelay() = %v, want between %v and %v",
					delay, tt.expectedMin, tt.expectedMax)
			}
		})
	}
}
