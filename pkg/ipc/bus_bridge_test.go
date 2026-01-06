package ipc

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
)

// mockEventCollector collects events for testing
type mockEventCollector struct {
	mu     sync.Mutex
	events []Event
}

func (m *mockEventCollector) BroadcastEvent(event Event) {
	m.mu.Lock()
	m.events = append(m.events, event)
	m.mu.Unlock()
}

func (m *mockEventCollector) getEvents() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]Event, len(m.events))
	copy(result, m.events)
	return result
}

func TestBusBridge_ForwardToHub(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create in-memory bus and hub
	memBus := bus.NewMemoryBus()
	hub := NewHub()

	// Create collector to capture events
	collector := &mockEventCollector{}
	hub.AddForwarder(collector)

	// Create and start bridge
	bridge := NewBusBridge(memBus, hub)
	if err := bridge.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer bridge.Stop()

	// Publish an agent event
	agentEvent := map[string]any{
		"type":       "thinking",
		"session_id": "sess-123",
		"content":    "Processing task...",
	}
	data, _ := json.Marshal(agentEvent)
	if err := memBus.Publish(ctx, "buckley.agent.sess-123.thinking", data); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	// Wait for event propagation
	time.Sleep(100 * time.Millisecond)

	events := collector.getEvents()
	if len(events) == 0 {
		t.Fatal("Expected at least one event")
	}

	// Check event was forwarded
	found := false
	for _, e := range events {
		if e.Type == "agent.thinking" && e.SessionID == "sess-123" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected agent.thinking event, got: %+v", events)
	}
}

func TestBusBridge_ForwardTaskEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	memBus := bus.NewMemoryBus()
	hub := NewHub()
	collector := &mockEventCollector{}
	hub.AddForwarder(collector)

	bridge := NewBusBridge(memBus, hub)
	if err := bridge.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer bridge.Stop()

	// Publish a task event
	taskEvent := map[string]any{
		"type":    "completed",
		"task_id": "task-456",
		"output":  "Task finished successfully",
	}
	data, _ := json.Marshal(taskEvent)
	if err := memBus.Publish(ctx, "buckley.task.task-456.completed", data); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	events := collector.getEvents()
	found := false
	for _, e := range events {
		if e.Type == "task.completed" && e.SessionID == "task-456" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected task.completed event, got: %+v", events)
	}
}

func TestBusBridge_Stop(t *testing.T) {
	ctx := context.Background()

	memBus := bus.NewMemoryBus()
	hub := NewHub()

	bridge := NewBusBridge(memBus, hub)
	if err := bridge.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Stop should unsubscribe
	bridge.Stop()

	// Verify subs are cleared
	bridge.mu.Lock()
	subsCount := len(bridge.subs)
	bridge.mu.Unlock()

	if subsCount != 0 {
		t.Errorf("Expected 0 subscriptions after stop, got %d", subsCount)
	}
}

func TestBusForwarder_BroadcastEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	memBus := bus.NewMemoryBus()

	// Subscribe to IPC events
	var received *bus.Message
	var mu sync.Mutex
	_, err := memBus.Subscribe(ctx, "buckley.ipc.>", func(msg *bus.Message) []byte {
		mu.Lock()
		received = msg
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	forwarder := NewBusForwarder(ctx, memBus)

	// Broadcast an event
	forwarder.BroadcastEvent(Event{
		Type:      "user.action",
		SessionID: "sess-789",
		Payload:   map[string]any{"action": "click"},
		Timestamp: time.Now(),
	})

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	msg := received
	mu.Unlock()

	if msg == nil {
		t.Fatal("Expected message to be received on bus")
	}
	if msg.Subject != "buckley.ipc.sess-789.events" {
		t.Errorf("Expected subject buckley.ipc.sess-789.events, got %s", msg.Subject)
	}
}

func TestBusBridge_RawDataHandling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	memBus := bus.NewMemoryBus()
	hub := NewHub()
	collector := &mockEventCollector{}
	hub.AddForwarder(collector)

	bridge := NewBusBridge(memBus, hub)
	if err := bridge.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer bridge.Stop()

	// Publish raw non-JSON data
	if err := memBus.Publish(ctx, "buckley.agent.raw.data", []byte("plain text message")); err != nil {
		t.Fatalf("Publish failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	events := collector.getEvents()
	if len(events) == 0 {
		t.Fatal("Expected event for raw data")
	}

	// Should be wrapped with raw field
	found := false
	for _, e := range events {
		if payload, ok := e.Payload.(map[string]any); ok {
			if raw, ok := payload["raw"].(string); ok && raw == "plain text message" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Errorf("Expected raw data to be wrapped, got: %+v", events)
	}
}

func TestPublishHubEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	memBus := bus.NewMemoryBus()
	hub := NewHub()

	bridge := NewBusBridge(memBus, hub)

	// Subscribe to verify
	var received *bus.Message
	var mu sync.Mutex
	_, err := memBus.Subscribe(ctx, "buckley.ipc.>", func(msg *bus.Message) []byte {
		mu.Lock()
		received = msg
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Publish hub event to bus
	err = bridge.PublishHubEvent(ctx, Event{
		Type:      "browser.command",
		SessionID: "browser-1",
		Payload:   map[string]any{"cmd": "approve"},
	})
	if err != nil {
		t.Fatalf("PublishHubEvent failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	msg := received
	mu.Unlock()

	if msg == nil {
		t.Fatal("Expected message on bus")
	}
	if msg.Subject != "buckley.ipc.browser-1.browser.command" {
		t.Errorf("Expected subject with session ID, got %s", msg.Subject)
	}
}
