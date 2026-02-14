package tui

import (
	"testing"
	"time"
)

func TestController_UpdateQueueIndicator_InactiveSession_NoStatus(t *testing.T) {
	app := &recordingApp{}
	active := &SessionState{ID: "active"}
	inactive := &SessionState{
		ID: "inactive",
		MessageQueue: []QueuedMessage{
			{Content: "queued"},
		},
	}
	ctrl := &Controller{
		app:            app,
		sessions:       []*SessionState{active, inactive},
		currentSession: 0,
	}

	ctrl.updateQueueIndicator(inactive)

	if got := len(app.GetStatusUpdates()); got != 0 {
		t.Fatalf("expected no status updates for inactive session, got %d", got)
	}
}

func TestController_DequeueMessage_InactiveSession_NoAckRender(t *testing.T) {
	app := &recordingApp{}
	active := &SessionState{ID: "active"}
	inactive := &SessionState{
		ID: "inactive",
		MessageQueue: []QueuedMessage{
			{Content: "queued", Timestamp: time.Unix(1700000000, 0)},
		},
	}
	ctrl := &Controller{
		app:            app,
		sessions:       []*SessionState{active, inactive},
		currentSession: 0,
	}

	queued, ok := ctrl.dequeueMessage(inactive)
	if !ok {
		t.Fatal("expected queued message")
	}
	if queued.Content != "queued" {
		t.Fatalf("expected queued message content, got %q", queued.Content)
	}
	if got := len(app.GetMessages()); got != 0 {
		t.Fatalf("expected no rendered ack message for inactive session, got %d", got)
	}
}
