package diagnostics

import (
	"strings"
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
)

func TestNewCollector(t *testing.T) {
	c := NewCollector()
	if c == nil {
		t.Fatal("expected non-nil collector")
	}
	if c.maxEvents != MaxEvents {
		t.Errorf("expected maxEvents %d, got %d", MaxEvents, c.maxEvents)
	}
}

func TestCollectorSubscribe(t *testing.T) {
	hub := telemetry.NewHub()
	c := NewCollector()
	c.Subscribe(hub)

	// Publish some events
	hub.Publish(telemetry.Event{
		Type:      telemetry.EventModelStreamStarted,
		Timestamp: time.Now(),
		Data:      map[string]any{"model": "gpt-4"},
	})
	hub.Publish(telemetry.Event{
		Type:      telemetry.EventToolStarted,
		Timestamp: time.Now(),
		Data:      map[string]any{"tool": "read_file"},
	})

	// Flush events to ensure immediate delivery (needed for async batching hub)
	hub.Flush()

	// Give time for events to be dispatched and processed
	time.Sleep(300 * time.Millisecond)

	stats := c.Stats()
	if stats["api_calls"].(int) != 1 {
		t.Errorf("expected 1 api call, got %v", stats["api_calls"])
	}

	toolCalls := stats["tool_calls"].(map[string]int)
	if toolCalls["read_file"] != 1 {
		t.Errorf("expected 1 read_file call, got %v", toolCalls)
	}

	c.Close()
	hub.Close()
}

func TestCollectorDump(t *testing.T) {
	c := NewCollector()

	// Record some events manually
	c.record(telemetry.Event{
		Type:      telemetry.EventModelStreamStarted,
		Timestamp: time.Now(),
		Data:      map[string]any{"model": "claude-3"},
	})
	c.record(telemetry.Event{
		Type:      telemetry.EventToolStarted,
		Timestamp: time.Now(),
		Data:      map[string]any{"tool": "write_file"},
	})
	c.record(telemetry.Event{
		Type:      telemetry.EventToolFailed,
		Timestamp: time.Now(),
		Data:      map[string]any{"error": "permission denied"},
	})

	dump := c.Dump()

	// Check that dump contains expected sections
	expectedSections := []string{
		"Backend Diagnostics",
		"API Statistics",
		"Model Usage",
		"Tool Usage",
		"Recent Errors",
		"Recent Events",
	}

	for _, section := range expectedSections {
		if !strings.Contains(dump, section) {
			t.Errorf("dump missing section: %s", section)
		}
	}

	if !strings.Contains(dump, "claude-3") {
		t.Error("dump should contain model name")
	}
	if !strings.Contains(dump, "write_file") {
		t.Error("dump should contain tool name")
	}
	if !strings.Contains(dump, "permission denied") {
		t.Error("dump should contain error message")
	}
}

func TestCollectorSubscribeNilHub(t *testing.T) {
	c := NewCollector()
	// Should not panic
	c.Subscribe(nil)
	c.Close()
}

func TestCollectorCircuitEvents(t *testing.T) {
	c := NewCollector()

	c.record(telemetry.Event{
		Type:      telemetry.EventCircuitStateChange,
		Timestamp: time.Now(),
		Data: map[string]any{
			"name":  "model-api",
			"state": "open",
		},
	})

	stats := c.Stats()
	circuitStates := stats["circuit_states"].(map[string]string)
	if circuitStates["model-api"] != "open" {
		t.Errorf("expected circuit state 'open', got %v", circuitStates["model-api"])
	}
}
