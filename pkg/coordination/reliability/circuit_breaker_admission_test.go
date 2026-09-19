package reliability

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCircuitBreaker_LateSuccessPreservesCooldown(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, Timeout: time.Hour})
	providerErr := errors.New("provider unavailable")
	if err := cb.Execute(func() error {
		overlappingErr := cb.Execute(func() error { return providerErr })
		if !errors.Is(overlappingErr, providerErr) {
			t.Fatalf("overlapping failure = %v", overlappingErr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if cb.State() != CircuitOpen {
		t.Fatalf("late success changed state to %v, want open", cb.State())
	}
	err := cb.Execute(func() error { t.Fatal("cooldown bypassed"); return nil })
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("admission = %v, want circuit open", err)
	}
	if metrics := cb.Metrics(); metrics.SuccessCount != 1 || metrics.FailureCount != 1 {
		t.Fatalf("completed call metrics lost: %+v", metrics)
	}
}

func TestCircuitBreaker_LateFailureDoesNotUndoReset(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1})
	providerErr := errors.New("old failure")
	err := cb.Execute(func() error {
		cb.Reset()
		return providerErr
	})
	if !errors.Is(err, providerErr) {
		t.Fatalf("caller lost its result: %v", err)
	}
	if cb.State() != CircuitClosed || cb.ConsecutiveFailures() != 0 || cb.LastError() != nil {
		t.Fatalf("old result changed reset state: state=%v failures=%d error=%v", cb.State(), cb.ConsecutiveFailures(), cb.LastError())
	}
	if cb.Metrics().FailureCount != 1 {
		t.Fatal("late failure missing from lifetime metrics")
	}
}

func TestCircuitBreaker_OneRecoveryProbeAtATime(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, Timeout: time.Nanosecond, SuccessThreshold: 2})
	_ = cb.Execute(func() error { return errors.New("offline") })
	cb.mu.Lock()
	cb.lastFailTime = time.Now().Add(-time.Hour)
	cb.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		if err := cb.Execute(func() error {
			err := cb.Execute(func() error { t.Error("concurrent recovery probe admitted"); return nil })
			if !errors.Is(err, ErrCircuitOpen) {
				t.Errorf("concurrent probe = %v, want circuit open", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		want := CircuitHalfOpen
		if attempt == 1 {
			want = CircuitClosed
		}
		if cb.State() != want {
			t.Fatalf("after probe %d: state=%v, want %v", attempt+1, cb.State(), want)
		}
	}
}

func TestCircuitBreaker_RecoveryProbeReleasedOnNeutralOrPanic(t *testing.T) {
	for _, mode := range []string{"neutral", "panic", "filter_panic", "observer_panic"} {
		t.Run(mode, func(t *testing.T) {
			cb := NewCircuitBreaker(CircuitBreakerConfig{
				MaxFailures: 1, Timeout: time.Nanosecond, SuccessThreshold: 1,
				OnStateChange: func(event StateChangeEvent) {
					if mode == "observer_panic" && event.To == CircuitHalfOpen {
						panic("observer panic")
					}
				},
			})
			_ = cb.Execute(func() error { return errors.New("offline") })
			cb.mu.Lock()
			cb.lastFailTime = time.Now().Add(-time.Hour)
			cb.mu.Unlock()
			func() {
				defer func() {
					if got := recover(); (got != nil) != (mode != "neutral") {
						t.Errorf("panic = %v for mode %s", got, mode)
					}
				}()
				_ = cb.ExecuteWithResultFilter(func() error {
					if mode == "panic" {
						panic("worker panic")
					}
					return nil
				}, func(error) bool {
					if mode == "filter_panic" {
						panic("filter panic")
					}
					return false
				})
			}()
			if cb.State() != CircuitHalfOpen || cb.Metrics().SuccessCount != 0 {
				t.Fatal("unrecorded probe changed recovery state")
			}
			if err := cb.Execute(func() error { return nil }); err != nil || cb.State() != CircuitClosed {
				t.Fatalf("replacement probe failed: err=%v state=%v", err, cb.State())
			}
		})
	}
}

func TestCircuitBreaker_StaleProbeCannotReleaseNewProbe(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, Timeout: time.Nanosecond, SuccessThreshold: 1})
	openForProbe := func() {
		_ = cb.Execute(func() error { return errors.New("offline") })
		cb.mu.Lock()
		cb.lastFailTime = time.Now().Add(-time.Hour)
		cb.mu.Unlock()
	}
	openForProbe()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	go func() {
		done <- cb.Execute(func() error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	cb.Reset()
	openForProbe()
	if err := cb.Execute(func() error {
		unblock()
		if err := <-done; err != nil {
			t.Errorf("old caller lost its success: %v", err)
		}
		if cb.State() != CircuitHalfOpen {
			t.Errorf("stale probe changed recovery state: %v", cb.State())
		}
		err := cb.Execute(func() error { t.Error("stale completion released the new probe"); return nil })
		if !errors.Is(err, ErrCircuitOpen) {
			t.Errorf("admission = %v, want circuit open", err)
		}
		return nil
	}); err != nil || cb.State() != CircuitClosed {
		t.Fatalf("current probe could not recover: err=%v state=%v", err, cb.State())
	}
}

func TestCircuitBreaker_FailureCallbackObservesCommittedState(t *testing.T) {
	var cb *CircuitBreaker
	cb = NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, OnFailure: func(event FailureEvent) {
		if !event.WillOpen || cb.State() != CircuitOpen {
			t.Errorf("callback observed partial transition: event=%+v state=%v", event, cb.State())
		}
		cb.Reset()
	}})
	_ = cb.Execute(func() error { return errors.New("offline") })
	if cb.State() != CircuitClosed {
		t.Fatal("callback reset was overwritten")
	}
}
