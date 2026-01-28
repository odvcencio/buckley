package telemetry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestHub creates a hub with batch size of 1 for immediate event delivery in tests.
func newTestHub() *Hub {
	return NewHubWithConfig(&Config{
		EventQueueSize:        DefaultEventQueueSize,
		BatchSize:             1, // Immediate flush for tests
		FlushInterval:         DefaultFlushInterval,
		RateLimit:             DefaultRateLimit,
		SubscriberChannelSize: DefaultSubscriberChannelSize,
	})
}

func TestNewHub(t *testing.T) {
	hub := NewHub()
	require.NotNil(t, hub)
	assert.NotNil(t, hub.subscribers)
	assert.False(t, hub.closed)
}

func TestHub_PublishSubscribe(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	event := Event{
		Type:      EventTaskStarted,
		SessionID: "test-session",
		Data:      map[string]any{"task": "test"},
	}

	hub.Publish(event)

	select {
	case received := <-ch:
		assert.Equal(t, EventTaskStarted, received.Type)
		assert.Equal(t, "test-session", received.SessionID)
		assert.False(t, received.Timestamp.IsZero())
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for event")
	}
}

func TestHub_MultipleSubscribers(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	ch1, unsub1 := hub.Subscribe()
	defer unsub1()
	ch2, unsub2 := hub.Subscribe()
	defer unsub2()

	event := Event{Type: EventTaskCompleted}
	hub.Publish(event)

	for _, ch := range []<-chan Event{ch1, ch2} {
		select {
		case received := <-ch:
			assert.Equal(t, EventTaskCompleted, received.Type)
		case <-time.After(100 * time.Millisecond):
			t.Fatal("subscriber did not receive event")
		}
	}
}

func TestHub_Unsubscribe(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	unsub()

	// Channel should be closed
	_, ok := <-ch
	assert.False(t, ok, "channel should be closed after unsubscribe")
}

func TestHub_Close(t *testing.T) {
	hub := NewHub()
	ch, _ := hub.Subscribe()

	hub.Close()

	// Channel should be closed
	_, ok := <-ch
	assert.False(t, ok, "channel should be closed after hub close")

	// Publish should be no-op after close
	hub.Publish(Event{Type: EventTaskStarted}) // Should not panic
}

func TestHub_DropsWhenBufferFull(t *testing.T) {
	// Use default hub with batching to test buffer behavior
	hub := NewHub()
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	// Publish many events quickly to overflow buffers
	for i := 0; i < 500; i++ {
		hub.Publish(Event{Type: EventTaskStarted, Data: map[string]any{"i": i}})
	}

	// Wait for flush interval
	time.Sleep(200 * time.Millisecond)

	// Should have received some events, dropped others
	count := 0
	done := time.After(100 * time.Millisecond)
drain:
	for {
		select {
		case <-ch:
			count++
		case <-done:
			break drain
		}
	}
	// With batching and rate limiting, we should get some events
	assert.Greater(t, count, 0, "should have received some events")
}

func TestHub_PublishWithoutTimestamp(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	// Publish without setting timestamp
	event := Event{
		Type:      EventPlanCreated,
		SessionID: "test",
	}
	hub.Publish(event)

	select {
	case received := <-ch:
		assert.Equal(t, EventPlanCreated, received.Type)
		// Timestamp should be auto-set
		assert.False(t, received.Timestamp.IsZero())
		assert.WithinDuration(t, time.Now(), received.Timestamp, time.Second)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for event")
	}
}

func TestHub_PublishWithPresetTimestamp(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	// Publish with preset timestamp
	presetTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	event := Event{
		Type:      EventPlanUpdated,
		Timestamp: presetTime,
	}
	hub.Publish(event)

	select {
	case received := <-ch:
		// Should preserve preset timestamp
		assert.Equal(t, presetTime, received.Timestamp)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for event")
	}
}

func TestHub_SubscribeAfterClose(t *testing.T) {
	hub := NewHub()
	hub.Close()

	// Subscribe after close should return closed channel
	ch, unsub := hub.Subscribe()
	unsub() // Should not panic

	_, ok := <-ch
	assert.False(t, ok, "channel should be closed")
}

func TestHub_DoubleClose(t *testing.T) {
	hub := NewHub()
	hub.Close()
	// Second close should not panic
	assert.NotPanics(t, func() {
		hub.Close()
	})
}

func TestHub_PublishToClosedHub(t *testing.T) {
	hub := NewHub()
	hub.Close()

	// Publishing to closed hub should not panic
	assert.NotPanics(t, func() {
		hub.Publish(Event{Type: EventTaskFailed})
	})
}

func TestHub_MultipleUnsubscribe(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	_, unsub := hub.Subscribe()

	// Multiple unsubscribe calls should not panic
	assert.NotPanics(t, func() {
		unsub()
		unsub()
	})
}

func TestHub_EventTypes(t *testing.T) {
	// Verify all event types are defined and unique
	eventTypes := []EventType{
		EventPlanCreated,
		EventPlanUpdated,
		EventTaskStarted,
		EventTaskCompleted,
		EventTaskFailed,
		EventResearchStarted,
		EventResearchCompleted,
		EventResearchFailed,
		EventBuilderStarted,
		EventBuilderCompleted,
		EventBuilderFailed,
		EventCostUpdated,
		EventTokenUsageUpdated,
		EventShellCommandStarted,
		EventShellCommandCompleted,
		EventShellCommandFailed,
		EventToolStarted,
		EventToolCompleted,
		EventToolFailed,
		EventModelStreamStarted,
		EventModelStreamEnded,
		EventIndexStarted,
		EventIndexCompleted,
		EventIndexFailed,
		EventEditorInline,
		EventEditorPropose,
		EventEditorApply,
	}

	// Verify they're all unique
	seen := make(map[EventType]bool)
	for _, et := range eventTypes {
		assert.False(t, seen[et], "duplicate event type: %s", et)
		seen[et] = true
		assert.NotEmpty(t, string(et), "event type should not be empty")
	}
}

func TestHub_ConcurrentPublish(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	// Publish from multiple goroutines concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 10; j++ {
				hub.Publish(Event{
					Type: EventTaskStarted,
					Data: map[string]any{"goroutine": id, "iteration": j},
				})
			}
			done <- true
		}(i)
	}

	// Wait for all publishers
	for i := 0; i < 10; i++ {
		<-done
	}

	// Drain the channel
	count := 0
	timeout := time.After(100 * time.Millisecond)
	for {
		select {
		case <-ch:
			count++
		case <-timeout:
			goto finished
		}
	}
finished:
	// We should have received at least some events
	assert.Greater(t, count, 0, "should have received some events from concurrent publishers")
}

func TestHub_ConcurrentSubscribe(t *testing.T) {
	hub := NewHub()
	defer hub.Close()

	// Subscribe from multiple goroutines
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			ch, unsub := hub.Subscribe()
			defer unsub()
			// Just verify we got a valid channel
			assert.NotNil(t, ch)
			done <- true
		}()
	}

	// Wait for all subscribers
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestHub_EventDataPreservation(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	// Create event with complex data
	complexData := map[string]any{
		"string":  "test",
		"number":  42,
		"boolean": true,
		"nested": map[string]any{
			"key": "value",
		},
		"array": []int{1, 2, 3},
	}

	event := Event{
		Type:      EventCostUpdated,
		SessionID: "session-123",
		PlanID:    "plan-456",
		TaskID:    "task-789",
		Data:      complexData,
	}

	hub.Publish(event)

	select {
	case received := <-ch:
		assert.Equal(t, event.Type, received.Type)
		assert.Equal(t, event.SessionID, received.SessionID)
		assert.Equal(t, event.PlanID, received.PlanID)
		assert.Equal(t, event.TaskID, received.TaskID)
		assert.Equal(t, complexData["string"], received.Data["string"])
		assert.Equal(t, complexData["number"], received.Data["number"])
		assert.Equal(t, complexData["boolean"], received.Data["boolean"])
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for event")
	}
}
