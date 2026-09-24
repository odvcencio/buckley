package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// newReviewCommandContext makes --timeout a total command budget. Callers
// create it immediately after argument validation, before model/provider
// initialization, so setup time cannot silently extend the advertised review
// window.
func newReviewCommandContext(started time.Time, timeout time.Duration) (context.Context, context.CancelFunc) {
	parent, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithDeadline(parent, started.Add(timeout))
	return ctx, func() { cancel(); stop() }
}
