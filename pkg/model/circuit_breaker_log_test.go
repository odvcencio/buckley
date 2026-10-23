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
