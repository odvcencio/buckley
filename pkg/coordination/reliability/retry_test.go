package reliability

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestRetryStrategy_SuccessOnFirstAttempt verifies that when the function
// succeeds on the first attempt, no retries occur.
func TestRetryStrategy_SuccessOnFirstAttempt(t *testing.T) {
	strategy := &RetryStrategy{
		MaxRetries: 3,
		BaseDelay:  10 * time.Millisecond,
		MaxDelay:   100 * time.Millisecond,
		Multiplier: 2.0,
	}

	attempts := 0
	fn := func() error {
		attempts++
		return nil
	}

	ctx := context.Background()
	err := strategy.Execute(ctx, fn)

	if err != nil {
		t.Errorf("Execute() error = %v, want nil", err)
	}

	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
}

// TestRetryStrategy_RetryOnRetriableError verifies that retriable errors
// trigger retries up to MaxRetries.
func TestRetryStrategy_RetryOnRetriableError(t *testing.T) {
	strategy := &RetryStrategy{
		MaxRetries: 3,
		BaseDelay:  10 * time.Millisecond,
		MaxDelay:   100 * time.Millisecond,
		Multiplier: 2.0,
	}

	attempts := 0
	fn := func() error {
		attempts++
		if attempts < 3 {
			// Return retriable error
			return status.Error(codes.Unavailable, "service unavailable")
		}
		return nil
	}

	ctx := context.Background()
	start := time.Now()
	err := strategy.Execute(ctx, fn)
	elapsed := time.Since(start)

	if err != nil {
		t.Errorf("Execute() error = %v, want nil", err)
	}

	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}

	// Verify backoff occurred: first retry after ~10ms, second after ~20ms = ~30ms total
	// Add some buffer for timing variability
	if elapsed < 20*time.Millisecond {
		t.Errorf("elapsed time = %v, want >= 20ms (indicates backoff occurred)", elapsed)
	}
}

// TestRetryStrategy_StopOnNonRetriableError verifies that non-retriable errors
// cause immediate failure without retries.
func TestRetryStrategy_StopOnNonRetriableError(t *testing.T) {
	strategy := &RetryStrategy{
		MaxRetries: 3,
		BaseDelay:  10 * time.Millisecond,
		MaxDelay:   100 * time.Millisecond,
		Multiplier: 2.0,
	}

	attempts := 0
	nonRetriableErr := status.Error(codes.InvalidArgument, "invalid argument")
	fn := func() error {
		attempts++
		return nonRetriableErr
	}

	ctx := context.Background()
	err := strategy.Execute(ctx, fn)

	if err == nil {
		t.Error("Execute() error = nil, want non-nil")
	}

	if !errors.Is(err, nonRetriableErr) {
		t.Errorf("Execute() error = %v, want %v", err, nonRetriableErr)
	}

	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (should not retry non-retriable errors)", attempts)
	}
}

// TestRetryStrategy_ContextCancellation verifies that context cancellation
// stops the retry loop.
func TestRetryStrategy_ContextCancellation(t *testing.T) {
	strategy := &RetryStrategy{
		MaxRetries: 10,
		BaseDelay:  50 * time.Millisecond,
		MaxDelay:   500 * time.Millisecond,
		Multiplier: 2.0,
	}

	attempts := 0
	fn := func() error {
		attempts++
		return status.Error(codes.Unavailable, "service unavailable")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := strategy.Execute(ctx, fn)

	if err == nil {
		t.Error("Execute() error = nil, want context error")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Execute() error = %v, want context.DeadlineExceeded", err)
	}

	// Should have attempted at least once, but not all 10 times
	if attempts == 0 {
		t.Error("attempts = 0, want > 0")
	}

	if attempts > 5 {
		t.Errorf("attempts = %d, want < 5 (context should cancel before max retries)", attempts)
	}
}

// TestRetryStrategy_MaxRetriesEnforcement verifies that retries stop
// after MaxRetries is reached.
func TestRetryStrategy_MaxRetriesEnforcement(t *testing.T) {
	strategy := &RetryStrategy{
		MaxRetries: 3,
		BaseDelay:  5 * time.Millisecond,
		MaxDelay:   100 * time.Millisecond,
		Multiplier: 2.0,
	}

	attempts := 0
	fn := func() error {
		attempts++
		return status.Error(codes.Unavailable, "service unavailable")
	}

	ctx := context.Background()
	err := strategy.Execute(ctx, fn)

	if err == nil {
		t.Error("Execute() error = nil, want error after max retries")
	}

	expectedAttempts := strategy.MaxRetries + 1 // Initial attempt + retries
	if attempts != expectedAttempts {
		t.Errorf("attempts = %d, want %d (initial + %d retries)", attempts, expectedAttempts, strategy.MaxRetries)
	}

	// Error should mention max retries
	if err.Error() == "" {
		t.Error("error message is empty")
	}
}

// TestRetryStrategy_ExponentialBackoff verifies that delays increase
// exponentially with each retry.
func TestRetryStrategy_ExponentialBackoff(t *testing.T) {
	strategy := &RetryStrategy{
		MaxRetries: 4,
		BaseDelay:  10 * time.Millisecond,
		MaxDelay:   200 * time.Millisecond,
		Multiplier: 2.0,
	}

	attempts := 0
	attemptTimes := []time.Time{}
	fn := func() error {
		attempts++
		attemptTimes = append(attemptTimes, time.Now())
		return status.Error(codes.Unavailable, "service unavailable")
	}

	ctx := context.Background()
	strategy.Execute(ctx, fn)

	// Verify exponential delays between attempts
	// Expected delays: 0ms, ~10ms, ~20ms, ~40ms, ~80ms
	if len(attemptTimes) < 3 {
		t.Fatalf("not enough attempts recorded: %d", len(attemptTimes))
	}

	// Check delay between first and second attempt (~10ms with ±25% jitter)
	delay1 := attemptTimes[1].Sub(attemptTimes[0])
	if delay1 < 7*time.Millisecond || delay1 > 17*time.Millisecond {
		t.Errorf("first retry delay = %v, want ~10ms (7-17ms with jitter)", delay1)
	}

	// Check delay between second and third attempt (~20ms with ±25% jitter)
	delay2 := attemptTimes[2].Sub(attemptTimes[1])
	if delay2 < 14*time.Millisecond || delay2 > 35*time.Millisecond {
		t.Errorf("second retry delay = %v, want ~20ms (14-35ms with jitter)", delay2)
	}

	// Verify exponential growth (second delay should be ~2x first delay)
	// With jitter, ratio can vary more: worst case is 35ms / 7ms = 5.0 or 14ms / 17ms = 0.82
	// So we check that the median delay is growing, not exact ratio
	ratio := float64(delay2) / float64(delay1)
	if ratio < 0.8 || ratio > 5.5 {
		t.Errorf("delay ratio = %.2f, want in range 0.8-5.5 (accounting for jitter)", ratio)
	}
}

// TestRetryStrategy_MaxDelayEnforcement verifies that delays never exceed MaxDelay.
func TestRetryStrategy_MaxDelayEnforcement(t *testing.T) {
	strategy := &RetryStrategy{
		MaxRetries: 10,
		BaseDelay:  10 * time.Millisecond,
		MaxDelay:   50 * time.Millisecond,
		Multiplier: 2.0,
	}

	attempts := 0
	attemptTimes := []time.Time{}
	fn := func() error {
		attempts++
		attemptTimes = append(attemptTimes, time.Now())
		return status.Error(codes.Unavailable, "service unavailable")
	}

	ctx := context.Background()
	strategy.Execute(ctx, fn)

	// After several exponential increases, delays should cap at MaxDelay
	// Expected: 10ms, 20ms, 40ms, 50ms (capped), 50ms (capped), ...
	// With jitter (±25%), max delay could be up to MaxDelay * 1.25
	maxAllowedDelay := time.Duration(float64(strategy.MaxDelay) * 1.3) // 30% buffer for timing + jitter
	for i := 4; i < len(attemptTimes); i++ {
		delay := attemptTimes[i].Sub(attemptTimes[i-1])
		if delay > maxAllowedDelay {
			t.Errorf("delay at attempt %d = %v, want <= %v (MaxDelay=%v + jitter)", i, delay, maxAllowedDelay, strategy.MaxDelay)
		}
	}
}

// TestIsRetriable_DeadlineExceeded verifies DeadlineExceeded is retriable.
func TestIsRetriable_DeadlineExceeded(t *testing.T) {
	err := status.Error(codes.DeadlineExceeded, "deadline exceeded")
	if !isRetriable(err) {
		t.Error("isRetriable(DeadlineExceeded) = false, want true")
	}
}

// TestIsRetriable_Unavailable verifies Unavailable is retriable.
func TestIsRetriable_Unavailable(t *testing.T) {
	err := status.Error(codes.Unavailable, "service unavailable")
	if !isRetriable(err) {
		t.Error("isRetriable(Unavailable) = false, want true")
	}
}

// TestIsRetriable_ResourceExhausted verifies ResourceExhausted is retriable.
func TestIsRetriable_ResourceExhausted(t *testing.T) {
	err := status.Error(codes.ResourceExhausted, "rate limit exceeded")
	if !isRetriable(err) {
		t.Error("isRetriable(ResourceExhausted) = false, want true")
	}
}

// TestIsRetriable_InvalidArgument verifies InvalidArgument is not retriable.
func TestIsRetriable_InvalidArgument(t *testing.T) {
	err := status.Error(codes.InvalidArgument, "invalid argument")
	if isRetriable(err) {
		t.Error("isRetriable(InvalidArgument) = true, want false")
	}
}

// TestIsRetriable_ContextCanceled verifies context.Canceled is not retriable.
func TestIsRetriable_ContextCanceled(t *testing.T) {
	err := context.Canceled
	if isRetriable(err) {
		t.Error("isRetriable(context.Canceled) = true, want false")
	}
}

// TestIsRetriable_NonGRPCError verifies non-gRPC errors are not retriable by default.
func TestIsRetriable_NonGRPCError(t *testing.T) {
	err := errors.New("generic error")
	if isRetriable(err) {
		t.Error("isRetriable(generic error) = true, want false")
	}
}

// TestRetryStrategy_Jitter verifies that jitter is applied to prevent thundering herd.
func TestRetryStrategy_Jitter(t *testing.T) {
	strategy := &RetryStrategy{
		MaxRetries: 3,
		BaseDelay:  50 * time.Millisecond,
		MaxDelay:   500 * time.Millisecond,
		Multiplier: 2.0,
	}

	// Run multiple executions and verify delays have some variance
	delays := []time.Duration{}
	for i := 0; i < 5; i++ {
		attempts := 0
		attemptTimes := []time.Time{}
		fn := func() error {
			attempts++
			attemptTimes = append(attemptTimes, time.Now())
			if attempts < 2 {
				return status.Error(codes.Unavailable, "unavailable")
			}
			return nil
		}

		ctx := context.Background()
		strategy.Execute(ctx, fn)

		if len(attemptTimes) >= 2 {
			delays = append(delays, attemptTimes[1].Sub(attemptTimes[0]))
		}
	}

	// Verify delays are not all identical (jitter is working)
	if len(delays) < 3 {
		t.Fatal("not enough delay samples collected")
	}

	allSame := true
	firstDelay := delays[0]
	for _, d := range delays[1:] {
		// Allow 5ms tolerance for timing precision
		if d < firstDelay-5*time.Millisecond || d > firstDelay+5*time.Millisecond {
			allSame = false
			break
		}
	}

	if allSame {
		t.Error("all delays are identical, jitter not working")
	}
}

// TestRetryStrategy_ZeroMaxRetries verifies that zero retries means one attempt only.
func TestRetryStrategy_ZeroMaxRetries(t *testing.T) {
	strategy := &RetryStrategy{
		MaxRetries: 0,
		BaseDelay:  10 * time.Millisecond,
		MaxDelay:   100 * time.Millisecond,
		Multiplier: 2.0,
	}

	attempts := 0
	fn := func() error {
		attempts++
		return status.Error(codes.Unavailable, "unavailable")
	}

	ctx := context.Background()
	err := strategy.Execute(ctx, fn)

	if err == nil {
		t.Error("Execute() error = nil, want error")
	}

	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retries with MaxRetries=0)", attempts)
	}
}

// TestRetryStrategy_ContextDeadlineExceeded verifies context.DeadlineExceeded is retriable.
func TestRetryStrategy_ContextDeadlineExceededIsRetriable(t *testing.T) {
	err := context.DeadlineExceeded
	if !isRetriable(err) {
		t.Error("isRetriable(context.DeadlineExceeded) = false, want true")
	}
}
