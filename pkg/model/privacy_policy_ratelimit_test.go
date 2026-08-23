package model

import "testing"

// openRouterPolicyBlocked also feeds APIError.Error(), which appends a hint
// telling the reader to check their ZDR and guardrail settings. Widening it to
// cover rate limits would stamp that hint onto every 429 in the system, so the
// rate limits remain ordinary provider failures rather than privacy failures.
func TestOpenRouterPolicyBlockedStillRejectsRateLimits(t *testing.T) {
	rateLimited := &APIError{StatusCode: 429, Message: "temporarily rate-limited upstream"}
	if openRouterPolicyBlocked(rateLimited) {
		t.Fatal("openRouterPolicyBlocked matched a 429; the ZDR hint would be attached to every rate limit")
	}
}
