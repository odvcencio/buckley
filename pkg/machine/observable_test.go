package machine

import (
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
)

func newTestHub() *telemetry.Hub {
	return telemetry.NewHubWithConfig(&telemetry.Config{
		EventQueueSize:        1000,
		BatchSize:             1, // immediate flush
		FlushInterval:         100 * time.Millisecond,
		RateLimit:             10000,
		SubscriberChannelSize: 64,
	})
}

func TestObservable_PublishesSpawnedEvent(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	NewObservable("agent-1", Classic, hub)

	select {
	case evt := <-ch:
		if evt.Type != telemetry.EventMachineSpawned {
			t.Errorf("type = %s, want machine.spawned", evt.Type)
		}
		if evt.Data["agent_id"] != "agent-1" {
			t.Error("missing agent_id")
		}
		if evt.Data["modality"] != "classic" {
			t.Errorf("modality = %v, want classic", evt.Data["modality"])
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for spawned event")
	}
}

func TestObservable_PublishesStateChanges(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	m := NewObservable("agent-1", Classic, hub)

	// Drain the spawned event
	<-ch

	m.Transition(UserInput{Content: "hello"})

	select {
	case evt := <-ch:
		if evt.Type != telemetry.EventMachineState {
			t.Errorf("type = %s, want machine.state", evt.Type)
		}
		if evt.Data["from"] != "idle" {
			t.Errorf("from = %v, want idle", evt.Data["from"])
		}
		if evt.Data["to"] != "calling_model" {
			t.Errorf("to = %v, want calling_model", evt.Data["to"])
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for state change event")
	}
}

func TestObservable_PublishesCompletedEvent(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	m := NewObservable("agent-1", Classic, hub)

	// Drain spawned
	<-ch

	m.Transition(UserInput{Content: "hello"})
	// Drain idle→calling_model
	<-ch

	m.Transition(ModelCompleted{Content: "done", FinishReason: "end_turn", TokensUsed: 42})

	// Should get state change and completed
	events := drainEvents(ch, 2, time.Second)
	found := false
	for _, evt := range events {
		if evt.Type == telemetry.EventMachineCompleted {
			found = true
			if evt.Data["result"] != "done" {
				t.Errorf("result = %v, want done", evt.Data["result"])
			}
		}
	}
	if !found {
		t.Error("expected EventMachineCompleted")
	}
}

func TestObservable_PublishesFailedEvent(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	m := NewObservable("agent-1", Classic, hub)
	<-ch // drain spawned

	m.Transition(UserInput{Content: "hello"})
	<-ch // drain state change

	m.Transition(Cancelled{})

	events := drainEvents(ch, 2, time.Second)
	found := false
	for _, evt := range events {
		if evt.Type == telemetry.EventMachineFailed {
			found = true
		}
	}
	if !found {
		t.Error("expected EventMachineFailed")
	}
}

func TestObservable_WithParent(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	NewObservableWithParent("sub-1", Classic, "coord-1", "implement auth", "gpt-4", hub)

	select {
	case evt := <-ch:
		if evt.Type != telemetry.EventMachineSpawned {
			t.Errorf("type = %s, want machine.spawned", evt.Type)
		}
		if evt.Data["parent_id"] != "coord-1" {
			t.Errorf("parent_id = %v, want coord-1", evt.Data["parent_id"])
		}
		if evt.Data["task"] != "implement auth" {
			t.Errorf("task = %v, want implement auth", evt.Data["task"])
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestObservable_NilHub(t *testing.T) {
	// Should not panic with nil hub
	m := NewObservable("agent-1", Classic, nil)
	m.Transition(UserInput{Content: "hello"})
	m.Transition(ModelCompleted{Content: "done", FinishReason: "end_turn"})
	if m.State() != Done {
		t.Errorf("state = %s, want done", m.State())
	}
}

func TestObservable_NoEventOnSameState(t *testing.T) {
	hub := newTestHub()
	defer hub.Close()

	ch, unsub := hub.Subscribe()
	defer unsub()

	m := NewObservable("agent-1", Classic, hub)
	<-ch // drain spawned

	m.Transition(UserInput{Content: "hello"})
	<-ch // drain state change

	// Steering doesn't change state
	m.Transition(UserSteering{Content: "try X"})

	// Should not receive a state change event
	select {
	case evt := <-ch:
		t.Errorf("unexpected event: %s", evt.Type)
	case <-time.After(100 * time.Millisecond):
		// Good — no event
	}
}

func drainEvents(ch <-chan telemetry.Event, count int, timeout time.Duration) []telemetry.Event {
	var events []telemetry.Event
	deadline := time.After(timeout)
	for len(events) < count {
		select {
		case evt := <-ch:
			events = append(events, evt)
		case <-deadline:
			return events
		}
	}
	return events
}
