package reliability

import (
	"errors"
	"testing"
	"time"
)

func TestCircuitBreakerExecuteWithResultFilterNeutralErrorPreservesClosedCounters(t *testing.T) {
	providerErr := errors.New("provider failure")
	neutralErr := errors.New("caller canceled after retained result")
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 2})

	if err := cb.Execute(func() error { return providerErr }); !errors.Is(err, providerErr) {
		t.Fatalf("first Execute error = %v, want provider error", err)
	}
	if err := cb.ExecuteWithResultFilter(func() error { return neutralErr }, func(err error) bool {
		return !errors.Is(err, neutralErr)
	}); !errors.Is(err, neutralErr) {
		t.Fatalf("neutral ExecuteWithResultFilter error = %v, want original neutral error", err)
	}
	if cb.ConsecutiveFailures() != 1 {
		t.Fatalf("failures = %d, want prior failure preserved without neutral record", cb.ConsecutiveFailures())
	}
	if cb.LastError() != providerErr {
		t.Fatalf("last error = %v, want original provider error", cb.LastError())
	}
	metrics := cb.Metrics()
	if metrics.TotalCalls != 2 || metrics.FailureCount != 1 || metrics.SuccessCount != 0 || metrics.CurrentState != CircuitClosed {
		t.Fatalf("metrics = %+v, want two admitted calls but one recorded failure", metrics)
	}
}

func TestCircuitBreakerExecuteWithResultFilterHalfOpenNeutralDoesNotClose(t *testing.T) {
	providerErr := errors.New("provider down")
	neutralErr := errors.New("task deadline")
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, Timeout: time.Nanosecond, SuccessThreshold: 1})
	if err := cb.Execute(func() error { return providerErr }); !errors.Is(err, providerErr) {
		t.Fatalf("open Execute error = %v, want provider error", err)
	}
	time.Sleep(time.Millisecond)
	if err := cb.ExecuteWithResultFilter(func() error { return neutralErr }, func(err error) bool {
		return !errors.Is(err, neutralErr)
	}); !errors.Is(err, neutralErr) {
		t.Fatalf("half-open neutral error = %v, want original neutral error", err)
	}
	if state := cb.State(); state != CircuitHalfOpen {
		t.Fatalf("state = %v, want half-open after neutral outcome", state)
	}
	if cb.ConsecutiveFailures() != 1 || cb.Metrics().SuccessCount != 0 {
		t.Fatalf("failures/successes = %d/%d, want unchanged failure and no success", cb.ConsecutiveFailures(), cb.Metrics().SuccessCount)
	}
}

func TestCircuitBreakerExecuteWithResultFilterRecordsProviderErrorsAndNilFilterCompat(t *testing.T) {
	providerErr := errors.New("provider failure")
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 2})
	if err := cb.ExecuteWithResultFilter(func() error { return providerErr }, func(error) bool { return true }); !errors.Is(err, providerErr) {
		t.Fatalf("provider error = %v, want original", err)
	}
	if cb.ConsecutiveFailures() != 1 || cb.Metrics().FailureCount != 1 {
		t.Fatalf("after provider error failures = %d metrics=%+v, want recorded failure", cb.ConsecutiveFailures(), cb.Metrics())
	}
	if err := cb.ExecuteWithResultFilter(func() error { return nil }, nil); err != nil {
		t.Fatalf("nil-filter success error = %v", err)
	}
	if cb.ConsecutiveFailures() != 0 || cb.Metrics().SuccessCount != 1 {
		t.Fatalf("nil-filter compat failures = %d metrics=%+v, want success recorded and failures reset", cb.ConsecutiveFailures(), cb.Metrics())
	}
}

func TestCircuitBreakerExecuteWithResultFilterOpenAdmissionRejectsWithoutFunction(t *testing.T) {
	providerErr := errors.New("provider failure")
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, Timeout: time.Hour})
	if err := cb.Execute(func() error { return providerErr }); !errors.Is(err, providerErr) {
		t.Fatalf("open Execute error = %v, want provider error", err)
	}
	called := false
	err := cb.ExecuteWithResultFilter(func() error {
		called = true
		return nil
	}, func(error) bool {
		t.Fatal("filter should not run for open-circuit admission rejection")
		return true
	})
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("error = %v, want ErrCircuitOpen", err)
	}
	if called {
		t.Fatal("function ran despite open circuit")
	}
	metrics := cb.Metrics()
	if metrics.TotalCalls != 2 || metrics.FailureCount != 1 || metrics.SuccessCount != 0 {
		t.Fatalf("metrics = %+v, want admission counted but no result recorded", metrics)
	}
}
