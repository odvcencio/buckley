package model

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestCall_ResetInvalidatesInFlightClosedCall(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, ResetTimeout: time.Second})
	entered := make(chan struct{})
	release := make(chan struct{})
	callErr := errors.New("stale closed-call failure")
	result := make(chan error, 1)

	go func() {
		result <- cb.Call(func() error {
			close(entered)
			<-release
			return callErr
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("closed call did not start")
	}

	cb.Reset()
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, callErr) {
			t.Fatalf("call result = %v, want %v", err, callErr)
		}
	case <-time.After(time.Second):
		t.Fatal("closed call did not finish")
	}

	if got := cb.State(); got != "closed" {
		t.Fatalf("state = %q, want closed after stale call", got)
	}
	if got := cb.FailureCount(); got != 0 {
		t.Fatalf("failureCount = %d, want 0 after stale call", got)
	}
}

func TestCall_OpenRejectionAndResetAreRaceFree(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, ResetTimeout: time.Hour})
	if err := cb.Call(func() error { return errors.New("open") }); err == nil {
		t.Fatal("opening call unexpectedly succeeded")
	}
	if err := cb.Call(func() error { return nil }); err == nil {
		t.Fatal("open breaker admitted a call")
	}

	const (
		callers    = 4
		resetters  = 2
		iterations = 500
	)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(callers + resetters)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				_ = cb.Call(func() error { return nil })
			}
		}()
	}
	for i := 0; i < resetters; i++ {
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				cb.Reset()
				_ = cb.Call(func() error { return errors.New("reopen") })
			}
		}()
	}
	close(start)
	wg.Wait()
}

func TestCall_PanickingHalfOpenProbeReleasesProbe(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, ResetTimeout: time.Second})
	if err := cb.Call(func() error { return errors.New("open") }); err == nil {
		t.Fatal("opening call unexpectedly succeeded")
	}
	cb.mu.Lock()
	cb.lastFailureTime = time.Now().Add(-cb.config.ResetTimeout)
	cb.mu.Unlock()

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("probe panic was not propagated")
			}
		}()
		_ = cb.Call(func() error {
			panic("probe panic")
		})
	}()

	if got := cb.State(); got != "half-open" {
		t.Fatalf("state = %q, want half-open after panicking probe", got)
	}
	if cb.halfOpenProbe.Load() {
		t.Fatal("halfOpenProbe latch still held after panicking probe")
	}
	if err := cb.Call(func() error { return nil }); err != nil {
		t.Fatalf("replacement probe = %v, want nil", err)
	}
	if got := cb.State(); got != "closed" {
		t.Fatalf("state = %q, want closed after replacement probe", got)
	}
}

func TestCall_GoexitHalfOpenProbeReleasesProbe(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, ResetTimeout: time.Second})
	if err := cb.Call(func() error { return errors.New("open") }); err == nil {
		t.Fatal("opening call unexpectedly succeeded")
	}
	cb.mu.Lock()
	cb.lastFailureTime = time.Now().Add(-cb.config.ResetTimeout)
	cb.mu.Unlock()

	exited := make(chan struct{})
	go func() {
		defer close(exited)
		_ = cb.Call(func() error {
			runtime.Goexit()
			return nil
		})
	}()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("Goexit probe did not terminate")
	}

	if got := cb.State(); got != "half-open" {
		t.Fatalf("state = %q, want half-open after Goexit probe", got)
	}
	if cb.halfOpenProbe.Load() {
		t.Fatal("halfOpenProbe latch still held after Goexit probe")
	}
	if err := cb.Call(func() error { return nil }); err != nil {
		t.Fatalf("replacement probe = %v, want nil", err)
	}
	if got := cb.State(); got != "closed" {
		t.Fatalf("state = %q, want closed after replacement probe", got)
	}
}

func TestCall_StaleProbeCannotReleaseNewerProbe(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, ResetTimeout: time.Second})
	if err := cb.Call(func() error { return errors.New("open") }); err == nil {
		t.Fatal("opening call unexpectedly succeeded")
	}
	cb.mu.Lock()
	cb.lastFailureTime = time.Now().Add(-cb.config.ResetTimeout)
	cb.mu.Unlock()

	oldEntered := make(chan struct{})
	oldRelease := make(chan error, 1)
	oldResult := make(chan error, 1)
	go func() {
		oldResult <- cb.Call(func() error {
			close(oldEntered)
			return <-oldRelease
		})
	}()
	select {
	case <-oldEntered:
	case <-time.After(time.Second):
		t.Fatal("old probe did not start")
	}

	cb.Reset()
	if err := cb.Call(func() error { return errors.New("reopen") }); err == nil {
		t.Fatal("reopening call unexpectedly succeeded")
	}
	cb.mu.Lock()
	cb.lastFailureTime = time.Now().Add(-cb.config.ResetTimeout)
	cb.mu.Unlock()

	newEntered := make(chan struct{})
	newRelease := make(chan error, 1)
	newResult := make(chan error, 1)
	go func() {
		newResult <- cb.Call(func() error {
			close(newEntered)
			return <-newRelease
		})
	}()
	select {
	case <-newEntered:
	case <-time.After(time.Second):
		t.Fatal("new probe did not start")
	}

	oldErr := errors.New("old probe failure")
	oldRelease <- oldErr
	select {
	case err := <-oldResult:
		if !errors.Is(err, oldErr) {
			t.Fatalf("old probe result = %v, want %v", err, oldErr)
		}
	case <-time.After(time.Second):
		t.Fatal("old probe did not finish")
	}
	if got := cb.State(); got != "half-open" || !cb.halfOpenProbe.Load() {
		t.Fatalf("state = %q latch=%t, want newer half-open probe intact", got, cb.halfOpenProbe.Load())
	}

	if err := cb.Call(func() error { return nil }); err == nil {
		t.Fatal("call entered while newer probe was active")
	}
	newRelease <- nil
	select {
	case err := <-newResult:
		if err != nil {
			t.Fatalf("new probe result = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("new probe did not finish")
	}
	if got := cb.State(); got != "closed" || cb.halfOpenProbe.Load() {
		t.Fatalf("state = %q latch=%t, want closed and released", got, cb.halfOpenProbe.Load())
	}
}

func TestCircuitBreaker_LoggingCanInspectState(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 1, ResetTimeout: time.Second})
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })
	slog.SetDefault(slog.New(circuitBreakerStateHandler{cb: cb}))

	result := make(chan error, 1)
	go func() {
		result <- cb.Call(func() error { return errors.New("failure") })
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("call unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("call deadlocked while logging state")
	}
}

type circuitBreakerStateHandler struct {
	cb *CircuitBreaker
}

func (h circuitBreakerStateHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

func (h circuitBreakerStateHandler) Handle(_ context.Context, _ slog.Record) error {
	_ = h.cb.State()
	return nil
}

func (h circuitBreakerStateHandler) WithAttrs(_ []slog.Attr) slog.Handler {
	return h
}

func (h circuitBreakerStateHandler) WithGroup(_ string) slog.Handler {
	return h
}

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
