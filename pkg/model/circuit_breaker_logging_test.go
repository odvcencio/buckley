package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestCircuitBreaker_FailureLogIsRedactedAndBounded(t *testing.T) {
	var output bytes.Buffer
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(original) })
	secret := "sk-or-v1-" + strings.Repeat("a", 64)
	cause := errors.New("upstream overloaded; " + secret + "; password=private-value " + strings.Repeat("x", 5000))
	breaker := DefaultCircuitBreaker()
	if got := breaker.Call(func() error { return cause }); got != cause {
		t.Fatalf("error identity changed: %v", got)
	}
	if strings.Contains(output.String(), secret) || strings.Contains(output.String(), "private-value") {
		t.Fatal("provider credentials leaked to circuit breaker log")
	}
	var entry struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(entry.Error, "upstream overloaded") || len(entry.Error) > 2048 {
		t.Fatalf("provider error lost or unbounded: %q", entry.Error)
	}
}
