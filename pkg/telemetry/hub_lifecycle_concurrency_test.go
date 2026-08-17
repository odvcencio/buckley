package telemetry

import (
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop/lifecycle"
)

func TestHubLifecyclePublishConcurrentWithCloseIsSafe(t *testing.T) {
	hub := NewHub()
	ch, unsubscribe := hub.Subscribe()
	observer := NewAgentLoopObserver(hub)
	start := make(chan struct{})

	var workers sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for sequence := 1; sequence <= 256; sequence++ {
				observer(lifecycle.Event{
					Sequence: uint64(sequence),
					Type:     lifecycle.ModelRequest,
					TurnID:   "turn-concurrent-close",
				})
			}
		}()
	}
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		hub.Close()
	}()
	go func() {
		defer workers.Done()
		<-start
		unsubscribe()
	}()
	close(start)

	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent lifecycle publish/close did not complete")
	}

	// Close is idempotent, post-close in-flight completions are dropped, and
	// the subscriber eventually observes closure after any buffered events.
	hub.Close()
	observer(lifecycle.Event{Type: lifecycle.TurnEnd, Error: "post-close"})
	for range ch {
	}
}
