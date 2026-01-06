package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/coordination/events"
	"github.com/gorilla/websocket"
)

func TestEventStream_Subscribe(t *testing.T) {
	eventStore := events.NewInMemoryStore()
	stream := NewEventStream(eventStore)

	// Create test server
	server := httptest.NewServer(http.HandlerFunc(stream.HandleWebSocket))
	defer server.Close()

	// Connect via WebSocket
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Subscribe to AgentRegistered events
	subscribeMsg := SubscribeMessage{
		Action:     "subscribe",
		EventTypes: []string{events.EventTypeAgentRegistered},
	}
	if err := ws.WriteJSON(subscribeMsg); err != nil {
		t.Fatalf("Failed to send subscribe message: %v", err)
	}

	// Allow subscription to be processed
	time.Sleep(50 * time.Millisecond)

	// Publish an event
	event := events.NewAgentRegisteredEvent("agent-1", []string{"code_execution"}, "localhost:50051")
	if err := eventStore.Append(context.Background(), "agent-1", []events.Event{event}); err != nil {
		t.Fatalf("Failed to append event: %v", err)
	}

	// Read event from WebSocket with timeout
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var receivedEvent EventMessage
	if err := ws.ReadJSON(&receivedEvent); err != nil {
		t.Fatalf("Failed to read event: %v", err)
	}

	if receivedEvent.Type != events.EventTypeAgentRegistered {
		t.Errorf("Expected event type %q, got %s", events.EventTypeAgentRegistered, receivedEvent.Type)
	}

	if receivedEvent.StreamID != "agent-1" {
		t.Errorf("Expected stream ID 'agent-1', got %s", receivedEvent.StreamID)
	}
}

func TestEventStream_AuthRequired(t *testing.T) {
	eventStore := events.NewInMemoryStore()
	auth := func(r *http.Request) error {
		if r.Header.Get("Authorization") != "Bearer ok" {
			return fmt.Errorf("unauthorized")
		}
		return nil
	}
	stream := NewEventStreamWithAuth(eventStore, auth)

	server := httptest.NewServer(http.HandlerFunc(stream.HandleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	// Missing auth should fail
	if _, _, err := websocket.DefaultDialer.Dial(wsURL, nil); err == nil {
		t.Fatalf("expected auth failure")
	}

	// With auth header should succeed
	headers := http.Header{}
	headers.Set("Authorization", "Bearer ok")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("Failed to connect with auth: %v", err)
	}
	defer ws.Close()

	// Subscribe and ensure we can receive an event
	subscribeMsg := SubscribeMessage{
		Action:     "subscribe",
		EventTypes: []string{events.EventTypeAgentRegistered},
	}
	_ = ws.WriteJSON(subscribeMsg)
	time.Sleep(50 * time.Millisecond)

	event := events.NewAgentRegisteredEvent("agent-2", []string{"code_analysis"}, "localhost:50053")
	if err := eventStore.Append(context.Background(), "agent-2", []events.Event{event}); err != nil {
		t.Fatalf("Failed to append event: %v", err)
	}

	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var received EventMessage
	if err := ws.ReadJSON(&received); err != nil {
		t.Fatalf("Failed to read event with auth: %v", err)
	}
}

func TestEventStream_MultipleSubscribers(t *testing.T) {
	eventStore := events.NewInMemoryStore()
	stream := NewEventStream(eventStore)

	server := httptest.NewServer(http.HandlerFunc(stream.HandleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	// Connect two clients
	ws1, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect client 1: %v", err)
	}
	defer ws1.Close()

	ws2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect client 2: %v", err)
	}
	defer ws2.Close()

	// Subscribe both clients
	subscribeMsg := SubscribeMessage{
		Action:     "subscribe",
		EventTypes: []string{events.EventTypeTaskCreated},
	}
	ws1.WriteJSON(subscribeMsg)
	ws2.WriteJSON(subscribeMsg)

	// Allow subscriptions to be processed
	time.Sleep(50 * time.Millisecond)

	// Publish event
	event := events.NewTaskCreatedEvent("task-1", "plan-1", "agent-1")
	eventStore.Append(context.Background(), "agent-1", []events.Event{event})

	// Both clients should receive the event
	var wg sync.WaitGroup
	wg.Add(2)

	checkClient := func(ws *websocket.Conn, clientName string) {
		defer wg.Done()
		ws.SetReadDeadline(time.Now().Add(2 * time.Second))
		var msg EventMessage
		if err := ws.ReadJSON(&msg); err != nil {
			t.Errorf("Client %s failed to read event: %v", clientName, err)
			return
		}
		if msg.Type != events.EventTypeTaskCreated {
			t.Errorf("Client %s expected %s, got %s", clientName, events.EventTypeTaskCreated, msg.Type)
		}
	}

	go checkClient(ws1, "1")
	go checkClient(ws2, "2")

	wg.Wait()
}

func TestEventStream_FilterByEventType(t *testing.T) {
	eventStore := events.NewInMemoryStore()
	stream := NewEventStream(eventStore)

	server := httptest.NewServer(http.HandlerFunc(stream.HandleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Subscribe only to TaskCreated events
	subscribeMsg := SubscribeMessage{
		Action:     "subscribe",
		EventTypes: []string{events.EventTypeTaskCreated},
	}
	ws.WriteJSON(subscribeMsg)

	// Allow subscription to be processed
	time.Sleep(50 * time.Millisecond)

	// Publish multiple event types
	agentEvent := events.NewAgentRegisteredEvent("agent-1", []string{"code"}, "localhost:50051")
	taskEvent := events.NewTaskCreatedEvent("task-1", "plan-1", "agent-1")

	eventStore.Append(context.Background(), "agent-1", []events.Event{agentEvent, taskEvent})

	// Should only receive TaskCreated event
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg EventMessage
	if err := ws.ReadJSON(&msg); err != nil {
		t.Fatalf("Failed to read event: %v", err)
	}

	if msg.Type != events.EventTypeTaskCreated {
		t.Errorf("Expected only %s event, got %s", events.EventTypeTaskCreated, msg.Type)
	}

	// Should not receive another event (AgentRegistered was filtered)
	ws.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if err := ws.ReadJSON(&msg); err == nil {
		t.Errorf("Expected no more events, but received: %+v", msg)
	}
}

func TestEventStream_Unsubscribe(t *testing.T) {
	eventStore := events.NewInMemoryStore()
	stream := NewEventStream(eventStore)

	server := httptest.NewServer(http.HandlerFunc(stream.HandleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Subscribe
	subscribeMsg := SubscribeMessage{
		Action:     "subscribe",
		EventTypes: []string{events.EventTypeAgentRegistered},
	}
	ws.WriteJSON(subscribeMsg)

	// Unsubscribe
	unsubscribeMsg := SubscribeMessage{
		Action: "unsubscribe",
	}
	ws.WriteJSON(unsubscribeMsg)

	// Allow unsubscribe to be processed
	time.Sleep(50 * time.Millisecond)

	// Publish event
	event := events.NewAgentRegisteredEvent("agent-1", []string{"code"}, "localhost:50051")
	eventStore.Append(context.Background(), "agent-1", []events.Event{event})

	// Should not receive event after unsubscribe
	ws.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var msg EventMessage
	if err := ws.ReadJSON(&msg); err == nil {
		t.Errorf("Expected no events after unsubscribe, but received: %+v", msg)
	}
}

func TestEventStream_InvalidMessage(t *testing.T) {
	eventStore := events.NewInMemoryStore()
	stream := NewEventStream(eventStore)

	server := httptest.NewServer(http.HandlerFunc(stream.HandleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Send invalid JSON
	ws.WriteMessage(websocket.TextMessage, []byte("invalid json"))

	// Should receive error message
	ws.SetReadDeadline(time.Now().Add(1 * time.Second))
	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("Failed to read error message: %v", err)
	}

	var errorMsg map[string]interface{}
	json.Unmarshal(msg, &errorMsg)
	if errorMsg["error"] == nil {
		t.Errorf("Expected error message, got: %s", string(msg))
	}
}

func TestEventStream_ConnectionMetrics(t *testing.T) {
	eventStore := events.NewInMemoryStore()
	stream := NewEventStream(eventStore)

	server := httptest.NewServer(http.HandlerFunc(stream.HandleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")

	// Connect client
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}

	// Check active connections increased
	if stream.ActiveConnections() != 1 {
		t.Errorf("Expected 1 active connection, got %d", stream.ActiveConnections())
	}

	// Close connection
	ws.Close()
	time.Sleep(100 * time.Millisecond) // Allow cleanup

	// Check active connections decreased
	if stream.ActiveConnections() != 0 {
		t.Errorf("Expected 0 active connections after close, got %d", stream.ActiveConnections())
	}
}

func TestEventStream_Backpressure(t *testing.T) {
	eventStore := events.NewInMemoryStore()
	stream := NewEventStream(eventStore)

	server := httptest.NewServer(http.HandlerFunc(stream.HandleWebSocket))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer ws.Close()

	// Subscribe
	subscribeMsg := SubscribeMessage{
		Action:     "subscribe",
		EventTypes: []string{events.EventTypeTaskCreated},
	}
	ws.WriteJSON(subscribeMsg)

	// Allow subscription to be processed
	time.Sleep(50 * time.Millisecond)

	// Publish many events quickly
	for i := 0; i < 100; i++ {
		event := events.NewTaskCreatedEvent("task-1", "plan-1", "agent-1")
		eventStore.Append(context.Background(), "agent-1", []events.Event{event})
	}

	// Should handle backpressure gracefully (not crash)
	receivedCount := 0
	ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var msg EventMessage
		if err := ws.ReadJSON(&msg); err != nil {
			break
		}
		receivedCount++
		if receivedCount >= 100 {
			break
		}
	}

	// Should receive most or all events
	if receivedCount < 90 {
		t.Errorf("Expected at least 90 events due to backpressure handling, got %d", receivedCount)
	}
}
