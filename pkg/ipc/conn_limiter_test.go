package ipc

import "testing"

func TestConnLimiterAcquireRelease(t *testing.T) {
	limiter := newConnLimiter(2)

	if !limiter.Acquire() {
		t.Fatalf("expected first Acquire to succeed")
	}
	if !limiter.Acquire() {
		t.Fatalf("expected second Acquire to succeed")
	}
	if limiter.Acquire() {
		t.Fatalf("expected third Acquire to fail at capacity")
	}

	limiter.Release()
	if !limiter.Acquire() {
		t.Fatalf("expected Acquire to succeed after Release")
	}

	limiter.Release()
	limiter.Release()
	limiter.Release() // should not underflow
}

func TestConnLimiterNilOrUnlimitedAlwaysAllows(t *testing.T) {
	var nilLimiter *connLimiter
	if !nilLimiter.Acquire() {
		t.Fatalf("expected nil Acquire to allow")
	}
	nilLimiter.Release()

	unlimited := newConnLimiter(0)
	if !unlimited.Acquire() {
		t.Fatalf("expected unlimited Acquire to allow")
	}
	unlimited.Release()
}
