package tui

import (
	"testing"
	"time"
)

func TestStreamCorrection_PublishedAfterInFlightFlush(t *testing.T) {
	app := newEventDeliveryApp(t)
	app.bindPresentationSession("session")
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	app.coalescer = NewCoalescer(CoalescerConfig{MaxChars: 1024, MaxWait: time.Nanosecond}, func(m Message) {
		if _, ok := m.(StreamFlush); ok {
			close(entered)
			<-release
		}
		app.postCoalescerPublication(m)
	})
	ctrl, state, sess := &Controller{app: app}, &toolLoopState{}, &SessionState{ID: "session"}
	ctrl.appendTextProgress(state, sess, "draft")
	go func() { app.coalescer.Tick(); close(done) }()
	<-entered
	ctrl.closeTextProgress(state, sess, "", true)
	close(release)
	<-done
	messages := drainFastMessages(app)
	for _, m := range messages {
		app.processMessage(m)
	}
	if containsPresentation(app, "draft") {
		t.Fatal("pending raw flush overwrote empty final correction")
	}
	snap, ok := app.live.snapshot(state.progress.textID)
	if !ok || snap.content != "" {
		t.Fatalf("final snapshot=%+v, %v", snap, ok)
	}
}
