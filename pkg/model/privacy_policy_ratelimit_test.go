package model

import (
	"errors"
	"testing"
)

// OpenRouter reports two different conditions on the zero-data-retention leg,
// and both leave the data-collection-deny leg able to serve the request.
//
// A 404 with "no endpoints" means guardrails left no ZDR route at all. A 429
// means a ZDR route exists but its endpoint pool is saturated upstream, which is
// the common case for a model with few ZDR providers. Only the first used to
// qualify, so the configured zdr_then_data_collection_deny policy could not fire
// for the second and the request failed while a permitted route was free.
func TestShouldTryOpenRouterPrivacyFallback(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "zdr route rate limited upstream",
			err:  &APIError{StatusCode: 429, Message: "openai/gpt-5.6-luna-pro is temporarily rate-limited upstream."},
			want: true,
		},
		{
			name: "no zdr endpoint at all",
			err:  &APIError{StatusCode: 404, Message: "No endpoints found matching your data policy (Zero data retention)."},
			want: true,
		},
		{
			name: "unrelated not found is not a privacy refusal",
			err:  &APIError{StatusCode: 404, Message: "model not found"},
			want: false,
		},
		{
			name: "server error is not retried on the deny leg",
			err:  &APIError{StatusCode: 500, Message: "internal error"},
			want: false,
		},
		{
			name: "non-API error",
			err:  errors.New("dial tcp: connection refused"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldTryOpenRouterPrivacyFallback(tt.err); got != tt.want {
				t.Fatalf("shouldTryOpenRouterPrivacyFallback(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// openRouterPolicyBlocked also feeds APIError.Error(), which appends a hint
// telling the reader to check their ZDR and guardrail settings. Widening it to
// cover rate limits would stamp that hint onto every 429 in the system, so the
// rate-limit case belongs in the fallback predicate alone.
func TestOpenRouterPolicyBlockedStillRejectsRateLimits(t *testing.T) {
	rateLimited := &APIError{StatusCode: 429, Message: "temporarily rate-limited upstream"}
	if openRouterPolicyBlocked(rateLimited) {
		t.Fatal("openRouterPolicyBlocked matched a 429; the ZDR hint would be attached to every rate limit")
	}
	if !shouldTryOpenRouterPrivacyFallback(rateLimited) {
		t.Fatal("the fallback predicate must still accept the rate-limited ZDR leg")
	}
}
