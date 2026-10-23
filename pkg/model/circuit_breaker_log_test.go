package model

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// An operator who sees "circuit breaker is open" must be able to find the
// errors that opened it.
func TestCircuitBreaker_LogsTheErrorsThatCountAsFailures(t *testing.T) {
	var buf bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 3, ResetTimeout: time.Minute})
	for i := 0; i < 3; i++ {
		_ = cb.Call(func() error { return errors.New("upstream said no: code 502") })
	}
	out := buf.String()
	if strings.Count(out, "upstream said no: code 502") < 3 {
		t.Fatalf("each counted failure should be logged with its error:\n%s", out)
	}
	if !strings.Contains(out, "consecutive_failures=3") {
		t.Fatalf("log lacks the running failure count:\n%s", out)
	}
}

// A ZDR-restricted route answers 404 "No endpoints found" before the fallback
// route succeeds. Client errors say the service is up, so they must not count
// toward opening the circuit, however many arrive together.
func TestCircuitBreaker_ClientErrorsDoNotOpenTheCircuit(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 3, ResetTimeout: time.Minute})
	for i := 0; i < 10; i++ {
		_ = cb.Call(func() error {
			return &APIError{StatusCode: 404, Message: "No endpoints found matching your data policy"}
		})
	}
	if state := cb.State(); state != "closed" {
		t.Fatalf("state after ten 404s = %s, want closed", state)
	}
	for _, status := range []int{408, 429, 500, 502} {
		cb := NewCircuitBreaker(CircuitBreakerConfig{MaxFailures: 3, ResetTimeout: time.Minute})
		for i := 0; i < 3; i++ {
			_ = cb.Call(func() error { return &APIError{StatusCode: status} })
		}
		if state := cb.State(); state != "open" {
			t.Errorf("state after three %d errors = %s, want open", status, state)
		}
	}
}
