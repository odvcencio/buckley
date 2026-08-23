package main

import (
	"context"
	"time"
)

// newReviewCommandContext makes --timeout a total command budget. Callers
// create it immediately after argument validation, before model/provider
// initialization, so setup time cannot silently extend the advertised review
// window.
func newReviewCommandContext(started time.Time, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithDeadline(context.Background(), started.Add(timeout))
}
