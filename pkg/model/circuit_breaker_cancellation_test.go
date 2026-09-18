package model

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCall_CanceledIsNeutralInClosedState(t *testing.T) {
	config := CircuitBreakerConfig{
		MaxFailures:  3,
		ResetTimeout: time.Second,
	}
	cb := NewCircuitBreaker(config)

	for i := uint32(0); i < config.MaxFailures+2; i++ {
		err := cb.Call(func() error { return context.Canceled })
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("call %d: err = %v, want context.Canceled", i, err)
		}
	}

	if got := cb.State(); got != "closed" {
		t.Errorf("state = %q, want closed", got)
	}
	if got := cb.FailureCount(); got != 0 {
		t.Errorf("failureCount = %d, want 0", got)
	}
	if got := cb.LastFailureTime(); !got.IsZero() {
		t.Errorf("lastFailureTime = %v, want zero", got)
	}
}

func TestCall_DeadlineExceededStillCounts(t *testing.T) {
	config := CircuitBreakerConfig{
		MaxFailures:  2,
		ResetTimeout: time.Second,
	}
	cb := NewCircuitBreaker(config)

	for i := uint32(0); i < config.MaxFailures; i++ {
		err := cb.Call(func() error { return context.DeadlineExceeded })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call %d: err = %v, want context.DeadlineExceeded", i, err)
		}
	}

	if got := cb.State(); got != "open" {
		t.Errorf("state = %q, want open after %d DeadlineExceeded failures", got, config.MaxFailures)
	}
}

func TestCall_OrdinaryFailuresPreservedAcrossCancellation(t *testing.T) {
	config := CircuitBreakerConfig{
		MaxFailures:  5,
		ResetTimeout: time.Second,
	}
	cb := NewCircuitBreaker(config)

	if err := cb.Call(func() error { return errors.New("boom") }); err == nil {
		t.Fatal("err = nil, want failure")
	}
	err := cb.Call(func() error { return context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if err := cb.Call(func() error { return errors.New("boom") }); err == nil {
		t.Fatal("err = nil, want failure")
	}

	if got := cb.FailureCount(); got != 2 {
		t.Fatalf("failureCount = %d, want 2 (cancellation must not reset or count)", got)
	}

	if err := cb.Call(func() error { return nil }); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got := cb.FailureCount(); got != 0 {
		t.Errorf("failureCount = %d, want 0 after success", got)
	}
	if got := cb.State(); got != "closed" {
		t.Errorf("state = %q, want closed", got)
	}
}

func TestCall_CanceledProbeRestoresOpenAndReleasesLatch(t *testing.T) {
	config := CircuitBreakerConfig{
		MaxFailures:  1,
		ResetTimeout: time.Second,
	}
	cb := NewCircuitBreaker(config)

	if err := cb.Call(func() error { return errors.New("boom") }); err == nil {
		t.Fatal("err = nil, want failure to open circuit")
	}
	if got := cb.State(); got != "open" {
		t.Fatalf("state = %q, want open", got)
	}

	// Expire the open-state timestamp without sleeping.
	cb.mu.Lock()
	cb.lastFailureTime = time.Now().Add(-config.ResetTimeout)
	cb.mu.Unlock()

	err := cb.Call(func() error { return context.Canceled })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := cb.State(); got != "open" {
		t.Fatalf("state = %q, want open after canceled probe", got)
	}
	if cb.halfOpenProbe.Load() {
		t.Fatal("halfOpenProbe latch still held after canceled probe")
	}

	if err := cb.Call(func() error { return nil }); err != nil {
		t.Fatalf("err = %v, want successful immediate probe", err)
	}
	if got := cb.State(); got != "closed" {
		t.Errorf("state = %q, want closed after successful probe", got)
	}
	if got := cb.FailureCount(); got != 0 {
		t.Errorf("failureCount = %d, want 0", got)
	}
}

func TestCall_HalfOpenProbeIsSingleFlight(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, ResetTimeout: time.Second})
	if err := cb.Call(func() error { return errors.New("open") }); err == nil {
		t.Fatal("opening call unexpectedly succeeded")
	}
	cb.mu.Lock()
	cb.lastFailureTime = time.Now().Add(-cb.config.ResetTimeout)
	cb.mu.Unlock()

	entered := make(chan struct{})
	release := make(chan struct{})
	probeResult := make(chan error, 1)
	go func() {
		probeResult <- cb.Call(func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("half-open probe did not start")
	}

	secondRan := false
	if err := cb.Call(func() error {
		secondRan = true
		return nil
	}); err == nil {
		t.Fatal("concurrent half-open call unexpectedly succeeded")
	}
	if secondRan {
		t.Fatal("concurrent half-open call executed its function")
	}
	close(release)
	select {
	case err := <-probeResult:
		if err != nil {
			t.Fatalf("probe result = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("half-open probe did not finish")
	}
	if got := cb.State(); got != "closed" || cb.halfOpenProbe.Load() {
		t.Fatalf("state = %q latch=%t, want closed and released", got, cb.halfOpenProbe.Load())
	}
}

func TestCall_StaleCanceledCallCannotReleaseOwnedProbe(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, ResetTimeout: time.Second})
	staleEntered := make(chan struct{})
	staleRelease := make(chan error, 1)
	staleResult := make(chan error, 1)
	go func() {
		staleResult <- cb.Call(func() error {
			close(staleEntered)
			return <-staleRelease
		})
	}()
	select {
	case <-staleEntered:
	case <-time.After(time.Second):
		t.Fatal("stale closed-state call did not start")
	}

	if err := cb.Call(func() error { return errors.New("open") }); err == nil {
		t.Fatal("opening call unexpectedly succeeded")
	}
	cb.mu.Lock()
	cb.lastFailureTime = time.Now().Add(-cb.config.ResetTimeout)
	cb.mu.Unlock()

	probeEntered := make(chan struct{})
	probeRelease := make(chan struct{})
	probeResult := make(chan error, 1)
	go func() {
		probeResult <- cb.Call(func() error {
			close(probeEntered)
			<-probeRelease
			return nil
		})
	}()
	select {
	case <-probeEntered:
	case <-time.After(time.Second):
		t.Fatal("owned half-open probe did not start")
	}

	staleRelease <- context.Canceled
	select {
	case err := <-staleResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("stale result = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stale call did not finish")
	}
	if got := cb.State(); got != "half-open" || !cb.halfOpenProbe.Load() {
		t.Fatalf("state = %q latch=%t, want owned half-open probe intact", got, cb.halfOpenProbe.Load())
	}
	if err := cb.Call(func() error { return nil }); err == nil {
		t.Fatal("third call entered while owned probe was active")
	}

	close(probeRelease)
	select {
	case err := <-probeResult:
		if err != nil {
			t.Fatalf("owned probe result = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("owned probe did not finish")
	}
	if got := cb.State(); got != "closed" || cb.halfOpenProbe.Load() {
		t.Fatalf("state = %q latch=%t, want closed and released", got, cb.halfOpenProbe.Load())
	}
}

func TestCall_ResetWinsOverInFlightProbe(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, ResetTimeout: time.Second})
	if err := cb.Call(func() error { return errors.New("open") }); err == nil {
		t.Fatal("opening call unexpectedly succeeded")
	}
	cb.mu.Lock()
	cb.lastFailureTime = time.Now().Add(-cb.config.ResetTimeout)
	cb.mu.Unlock()

	entered := make(chan struct{})
	release := make(chan struct{})
	probeResult := make(chan error, 1)
	probeErr := errors.New("stale probe failure")
	go func() {
		probeResult <- cb.Call(func() error {
			close(entered)
			<-release
			return probeErr
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("half-open probe did not start")
	}

	cb.Reset()
	if got := cb.State(); got != "closed" || cb.halfOpenProbe.Load() {
		t.Fatalf("after Reset state=%q latch=%t, want closed and released", got, cb.halfOpenProbe.Load())
	}
	close(release)
	select {
	case err := <-probeResult:
		if !errors.Is(err, probeErr) {
			t.Fatalf("stale probe result = %v, want original error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stale probe did not finish")
	}
	if got := cb.State(); got != "closed" || cb.FailureCount() != 0 || cb.halfOpenProbe.Load() {
		t.Fatalf("stale probe corrupted reset: state=%q failures=%d latch=%t", got, cb.FailureCount(), cb.halfOpenProbe.Load())
	}
}
