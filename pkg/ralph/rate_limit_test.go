package ralph

import (
	"testing"
	"time"
)

func TestParseRateLimitResponse_HeaderRetryAfter(t *testing.T) {
	info := ParseRateLimitResponse("", map[string]string{"Retry-After": "45"})
	if info == nil {
		t.Fatal("expected rate limit info")
	}
	if info.RetryAfter != 45*time.Second {
		t.Errorf("expected retry after 45s, got %v", info.RetryAfter)
	}
}

func TestParseRateLimitResponse_TryAgainIn(t *testing.T) {
	info := ParseRateLimitResponse("rate limit exceeded, try again in 30 seconds", nil)
	if info == nil {
		t.Fatal("expected rate limit info")
	}
	if info.RetryAfter != 30*time.Second {
		t.Errorf("expected retry after 30s, got %v", info.RetryAfter)
	}
}

func TestParseRateLimitResponse_ResetsAt(t *testing.T) {
	info := ParseRateLimitResponse("quota exceeded, resets at 2026-01-01T00:00:00Z", nil)
	if info == nil {
		t.Fatal("expected rate limit info")
	}
	if info.WindowResets.IsZero() {
		t.Fatal("expected window reset time")
	}
}

func TestParseRateLimitResponse_DefaultBackoff(t *testing.T) {
	info := ParseRateLimitResponse("rate limit exceeded", nil)
	if info == nil {
		t.Fatal("expected rate limit info")
	}
	if info.RetryAfter == 0 && info.WindowResets.IsZero() {
		t.Fatal("expected fallback retry or reset info")
	}
}
