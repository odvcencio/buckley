package tool

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

// RetryConfig configures retry behavior.
type RetryConfig struct {
	MaxAttempts   int
	InitialDelay  time.Duration
	MaxDelay      time.Duration
	Multiplier    float64
	Jitter        float64
	RetryableFunc func(error) bool
}

// Retry retries tool execution with exponential backoff.
func Retry(cfg RetryConfig) Middleware {
	return func(next Executor) Executor {
		return func(ctx *ExecutionContext) (*builtin.Result, error) {
			attempts := cfg.MaxAttempts
			if attempts <= 0 {
				attempts = 1
			}
			retryable := cfg.RetryableFunc
			if retryable == nil {
				retryable = DefaultRetryable
			}

			delay := cfg.InitialDelay
			var lastErr error
			for attempt := 1; attempt <= attempts; attempt++ {
				loopCtx := ctxContext(ctx)
				if err := loopCtx.Err(); err != nil {
					return nil, err
				}
				if ctx != nil {
					ctx.Attempt = attempt
				}
				result, err := next(ctx)
				if err == nil {
					return result, nil
				}

				lastErr = err
				if !retryable(err) || attempt == attempts {
					return result, err
				}

				jitteredDelay := applyJitter(delay, cfg.Jitter)
				if err := sleepWithContext(loopCtx, jitteredDelay); err != nil {
					return nil, err
				}

				delay = minDuration(time.Duration(float64(delay)*cfg.Multiplier), cfg.MaxDelay)
			}

			if lastErr == nil {
				lastErr = fmt.Errorf("unknown error")
			}
			name := ""
			if ctx != nil {
				name = ctx.ToolName
			}
			return nil, fmt.Errorf("tool %s failed after %d attempts: %w", name, attempts, lastErr)
		}
	}
}

// DefaultRetryable determines whether an error should be retried.
func DefaultRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var temp interface{ Temporary() bool }
	if errors.As(err, &temp) && temp.Temporary() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "temporary failure")
}

func applyJitter(delay time.Duration, jitter float64) time.Duration {
	if delay <= 0 || jitter <= 0 {
		return delay
	}
	if jitter > 1 {
		jitter = 1
	}
	base := float64(delay)
	min := base * (1 - jitter)
	max := base * (1 + jitter)
	return time.Duration(min + rand.Float64()*(max-min))
}

func minDuration(a, b time.Duration) time.Duration {
	if b <= 0 || a < b {
		return a
	}
	return b
}

func ctxContext(ctx *ExecutionContext) context.Context {
	if ctx == nil || ctx.Context == nil {
		return context.Background()
	}
	return ctx.Context
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
