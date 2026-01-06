package main

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestFormatIPCErrorBodyIncludesRemediation(t *testing.T) {
	data := []byte(`{
  "error": "unauthorized",
  "status": 401,
  "code": "auth.unauthorized",
  "message": "unauthorized",
  "remediation": ["set a token"],
  "retryable": true,
  "timestamp": "2025-01-01T00:00:00Z"
}`)

	got := formatIPCErrorBody(data)
	if !strings.Contains(got, "unauthorized") {
		t.Fatalf("expected message, got %q", got)
	}
	if !strings.Contains(got, "auth.unauthorized") {
		t.Fatalf("expected code, got %q", got)
	}
	if !strings.Contains(got, "set a token") {
		t.Fatalf("expected remediation, got %q", got)
	}
	if !strings.Contains(got, "retryable") {
		t.Fatalf("expected retryable marker, got %q", got)
	}
}

func TestFormatConnectAuthErrorIncludesHint(t *testing.T) {
	err := connect.NewError(connect.CodeUnauthenticated, errors.New("nope"))
	got := formatConnectAuthError(err).Error()
	if !strings.Contains(got, "remote stream unauthorized") {
		t.Fatalf("expected context, got %q", got)
	}
	if !strings.Contains(got, "nope") {
		t.Fatalf("expected message, got %q", got)
	}
	if !strings.Contains(got, "buckley remote login") {
		t.Fatalf("expected auth hint, got %q", got)
	}
}
