package gts

import (
	"context"
	"testing"
	"time"
)

func TestRunner_Run_RespectsTimeout(t *testing.T) {
	r := NewRunner(
		WithBinary("sleep"),
		WithTimeout(500*time.Millisecond),
		WithMemLimit(0), // no mem limit for this test
	)

	start := time.Now()
	_, err := r.Run(context.Background(), "10")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !IsTimeout(err) {
		t.Fatalf("expected ErrGTSTimeout, got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}

func TestRunner_Run_CapsOutputSize(t *testing.T) {
	r := NewRunner(
		WithBinary("yes"),
		WithTimeout(2*time.Second),
		WithMemLimit(0),
	)

	out, err := r.Run(context.Background())
	// yes will be killed by timeout or pipe, so some error is expected
	if err != nil && !IsTimeout(err) {
		// non-timeout errors are fine; yes may exit from broken pipe
	}

	if len(out) > maxOutputBytes {
		t.Fatalf("output exceeded limit: got %d bytes, max %d", len(out), maxOutputBytes)
	}
	if len(out) == 0 {
		t.Fatal("expected some output from yes command")
	}
	t.Logf("captured %d bytes (limit %d)", len(out), maxOutputBytes)
}

func TestRunner_Run_Success(t *testing.T) {
	r := NewRunner(
		WithBinary("echo"),
		WithTimeout(5*time.Second),
		WithMemLimit(0),
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

func TestRunner_ShellQuote(t *testing.T) {
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

func TestRunner_Defaults(t *testing.T) {
	r := NewRunner()
	if r.binary != "gts" {
		t.Errorf("default binary = %q, want %q", r.binary, "gts")
	}
	if r.timeout != 30*time.Second {
		t.Errorf("default timeout = %v, want %v", r.timeout, 30*time.Second)
	}
	if r.memLimitMB != 512 {
		t.Errorf("default memLimitMB = %d, want %d", r.memLimitMB, 512)
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

func TestIsTimeout_IsOOM(t *testing.T) {
	if IsTimeout(nil) {
		t.Error("IsTimeout(nil) = true")
	}
	if IsOOM(nil) {
		t.Error("IsOOM(nil) = true")
	}
	if !IsTimeout(ErrGTSTimeout) {
		t.Error("IsTimeout(ErrGTSTimeout) = false")
	}
	if !IsOOM(ErrGTSOOM) {
		t.Error("IsOOM(ErrGTSOOM) = false")
	}
}
