package graft

import (
	"context"
	"testing"
	"time"
)

func TestRunner_Run_RespectsTimeout(t *testing.T) {
	r := NewRunner(
		WithBinary("sleep"),
		WithTimeout(500*time.Millisecond),
	)

	start := time.Now()
	_, err := r.Run(context.Background(), "10")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !IsTimeout(err) {
		t.Fatalf("expected ErrGraftTimeout, got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestRunner_Run_Success(t *testing.T) {
	r := NewRunner(
		WithBinary("echo"),
		WithTimeout(5*time.Second),
	)

	out, err := r.Run(context.Background(), "hello", "world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := string(out)
	if got != "hello world\n" {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestRunner_Defaults(t *testing.T) {
	r := &Runner{
		binary:  "graft",
		timeout: 30 * time.Second,
	}
	if r.binary != "graft" {
		t.Errorf("default binary = %q, want %q", r.binary, "graft")
	}
	if r.timeout != 30*time.Second {
		t.Errorf("default timeout = %v, want %v", r.timeout, 30*time.Second)
	}
}

func TestRunner_WithOptions(t *testing.T) {
	r := NewRunner(
		WithBinary("/usr/local/bin/graft"),
		WithWorkDir("/tmp"),
		WithTimeout(60*time.Second),
	)
	if r.binary != "/usr/local/bin/graft" {
		t.Errorf("binary = %q, want %q", r.binary, "/usr/local/bin/graft")
	}
	if r.workDir != "/tmp" {
		t.Errorf("workDir = %q, want %q", r.workDir, "/tmp")
	}
	if r.timeout != 60*time.Second {
		t.Errorf("timeout = %v, want %v", r.timeout, 60*time.Second)
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "'simple'"},
		{"it's", `'it'\''s'`},
		{"a b c", "'a b c'"},
		{"", "''"},
		{"$HOME", "'$HOME'"},
	}
	for _, tc := range tests {
		got := shellQuote(tc.input)
		if got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestLimitWriter_CapsAtLimit(t *testing.T) {
	w := &limitWriter{limit: 10}
	n, err := w.Write([]byte("hello world!!!"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 10 {
		t.Errorf("Write returned %d, want 10", n)
	}
	if w.buf.Len() != 10 {
		t.Errorf("buffer len = %d, want 10", w.buf.Len())
	}
	if string(w.Bytes()) != "hello worl" {
		t.Errorf("buffer = %q, want %q", string(w.Bytes()), "hello worl")
	}

	// Further writes should be discarded
	n, err = w.Write([]byte("more data"))
	if err != nil {
		t.Fatalf("unexpected error on overflow write: %v", err)
	}
	if n != 9 {
		t.Errorf("overflow Write returned %d, want 9 (reported success)", n)
	}
	if w.buf.Len() != 10 {
		t.Errorf("buffer grew past limit: %d", w.buf.Len())
	}
}

func TestIsTimeout_IsKilled(t *testing.T) {
	if IsTimeout(nil) {
		t.Error("IsTimeout(nil) = true")
	}
	if IsKilled(nil) {
		t.Error("IsKilled(nil) = true")
	}
	if !IsTimeout(ErrGraftTimeout) {
		t.Error("IsTimeout(ErrGraftTimeout) = false")
	}
	if !IsKilled(ErrGraftKilled) {
		t.Error("IsKilled(ErrGraftKilled) = false")
	}
}
