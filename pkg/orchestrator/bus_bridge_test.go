package orchestrator

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/bus"
	"github.com/odvcencio/buckley/pkg/telemetry"
)

func TestTelemetryBusBridge_ForwardsEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Create telemetry hub and memory bus
	telemetryHub := telemetry.NewHub()
	defer telemetryHub.Close()

	memBus := bus.NewMemoryBus()

	// Subscribe to orchestrator events on the bus
	var received []map[string]any
	var mu sync.Mutex
	_, err := memBus.Subscribe(ctx, "buckley.orchestrator.>", func(msg *bus.Message) []byte {
		var payload map[string]any
		if err := json.Unmarshal(msg.Data, &payload); err == nil {
			mu.Lock()
			received = append(received, payload)
			mu.Unlock()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	// Create and start bridge
	bridge := NewTelemetryBusBridge(telemetryHub, memBus)
	bridge.Start(ctx)
	defer bridge.Stop()

	// Publish telemetry events
	telemetryHub.Publish(telemetry.Event{
		Type:      telemetry.EventTaskStarted,
		SessionID: "sess-1",
		PlanID:    "plan-1",
		TaskID:    "task-1",
		Data:      map[string]any{"name": "implement feature"},
	})

	telemetryHub.Publish(telemetry.Event{
		Type:      telemetry.EventTaskCompleted,
		SessionID: "sess-1",
		PlanID:    "plan-1",
		TaskID:    "task-1",
		Data:      map[string]any{"output": "success"},
	})

	// Wait for events
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count != 2 {
		t.Errorf("Expected 2 events, got %d", count)
	}

	// Verify first event
	mu.Lock()
	if count >= 1 {
		first := received[0]
		if first["type"] != string(telemetry.EventTaskStarted) {
			t.Errorf("Expected type task.started, got %v", first["type"])
		}
		if first["session_id"] != "sess-1" {
			t.Errorf("Expected session_id sess-1, got %v", first["session_id"])
		}
	}
	mu.Unlock()
}

func TestTelemetryBusBridge_Stop(t *testing.T) {
	ctx := context.Background()

	telemetryHub := telemetry.NewHub()
	defer telemetryHub.Close()

	memBus := bus.NewMemoryBus()

	bridge := NewTelemetryBusBridge(telemetryHub, memBus)
	bridge.Start(ctx)

	// Stop should complete without blocking
	done := make(chan struct{})
	go func() {
		bridge.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Good
	case <-time.After(time.Second):
		t.Fatal("Stop timed out")
	}
}

func TestBusEventEmitter_EmitTaskStarted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	memBus := bus.NewMemoryBus()

	var received *bus.Message
	var mu sync.Mutex
	_, err := memBus.Subscribe(ctx, "buckley.orchestrator.>", func(msg *bus.Message) []byte {
		mu.Lock()
		received = msg
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	emitter := NewBusEventEmitter(memBus, "sess-123", "plan-456")
	if err := emitter.EmitTaskStarted(ctx, "task-789", "write code"); err != nil {
		t.Fatalf("EmitTaskStarted failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	msg := received
	mu.Unlock()

	if msg == nil {
		t.Fatal("Expected message")
	}

	// Verify subject includes task ID
	expected := "buckley.orchestrator.plan.plan-456.task.task-789.task.started"
	if msg.Subject != expected {
		t.Errorf("Expected subject %q, got %q", expected, msg.Subject)
	}

	// Verify payload
	var payload map[string]any
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if payload["session_id"] != "sess-123" {
		t.Errorf("Expected session_id sess-123, got %v", payload["session_id"])
	}
	if payload["plan_id"] != "plan-456" {
		t.Errorf("Expected plan_id plan-456, got %v", payload["plan_id"])
	}
}

func TestBusEventEmitter_EmitTaskCompleted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	memBus := bus.NewMemoryBus()

	var received *bus.Message
	var mu sync.Mutex
	_, err := memBus.Subscribe(ctx, "buckley.orchestrator.>", func(msg *bus.Message) []byte {
		mu.Lock()
		received = msg
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	emitter := NewBusEventEmitter(memBus, "sess-1", "plan-1")
	if err := emitter.EmitTaskCompleted(ctx, "task-1", "done", 1500); err != nil {
		t.Fatalf("EmitTaskCompleted failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	msg := received
	mu.Unlock()

	if msg == nil {
		t.Fatal("Expected message")
	}

	var payload map[string]any
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if payload["type"] != string(telemetry.EventTaskCompleted) {
		t.Errorf("Expected type task.completed, got %v", payload["type"])
	}

	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatal("Expected data map")
	}
	if data["tokens_used"] != float64(1500) {
		t.Errorf("Expected tokens_used 1500, got %v", data["tokens_used"])
	}
}

func TestBusEventEmitter_EmitTaskFailed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	memBus := bus.NewMemoryBus()

	var received *bus.Message
	var mu sync.Mutex
	_, err := memBus.Subscribe(ctx, "buckley.orchestrator.>", func(msg *bus.Message) []byte {
		mu.Lock()
		received = msg
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	emitter := NewBusEventEmitter(memBus, "sess-1", "plan-1")
	if err := emitter.EmitTaskFailed(ctx, "task-1", "compilation error"); err != nil {
		t.Fatalf("EmitTaskFailed failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	msg := received
	mu.Unlock()

	if msg == nil {
		t.Fatal("Expected message")
	}

	var payload map[string]any
	if err := json.Unmarshal(msg.Data, &payload); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if payload["type"] != string(telemetry.EventTaskFailed) {
		t.Errorf("Expected type task.failed, got %v", payload["type"])
	}

	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatal("Expected data map")
	}
	if data["error"] != "compilation error" {
		t.Errorf("Expected error 'compilation error', got %v", data["error"])
	}
}

func TestBusEventEmitter_EmitResearchEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	memBus := bus.NewMemoryBus()

	var received []*bus.Message
	var mu sync.Mutex
	_, err := memBus.Subscribe(ctx, "buckley.orchestrator.>", func(msg *bus.Message) []byte {
		mu.Lock()
		received = append(received, msg)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	emitter := NewBusEventEmitter(memBus, "sess-1", "plan-1")

	if err := emitter.EmitResearchStarted(ctx, "how does auth work?"); err != nil {
		t.Fatalf("EmitResearchStarted failed: %v", err)
	}

	if err := emitter.EmitResearchCompleted(ctx, "Auth uses JWT tokens..."); err != nil {
		t.Fatalf("EmitResearchCompleted failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count != 2 {
		t.Errorf("Expected 2 messages, got %d", count)
	}
}

func TestBuildSubject(t *testing.T) {
	tests := []struct {
		name     string
		event    telemetry.Event
		expected string
	}{
		{
			name: "simple event",
			event: telemetry.Event{
				Type: telemetry.EventTaskStarted,
			},
			expected: "buckley.orchestrator.task.started",
		},
		{
			name: "with plan ID",
			event: telemetry.Event{
				Type:   telemetry.EventTaskStarted,
				PlanID: "plan-123",
			},
			expected: "buckley.orchestrator.plan.plan-123.task.started",
		},
		{
			name: "with plan and task ID",
			event: telemetry.Event{
				Type:   telemetry.EventTaskCompleted,
				PlanID: "plan-123",
				TaskID: "task-456",
			},
			expected: "buckley.orchestrator.plan.plan-123.task.task-456.task.completed",
		},
		{
			name: "task only",
			event: telemetry.Event{
				Type:   telemetry.EventBuilderStarted,
				TaskID: "task-789",
			},
			expected: "buckley.orchestrator.task.task-789.builder.started",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildSubject(tt.event)
			if got != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, got)
			}
		})
	}
}
