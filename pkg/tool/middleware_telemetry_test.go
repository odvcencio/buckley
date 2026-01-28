package tool

import (
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
)

type telemetryTool struct{}

func (telemetryTool) Name() string {
	return "telemetry_tool"
}

func (telemetryTool) Description() string {
	return "telemetry tool"
}

func (telemetryTool) Parameters() builtin.ParameterSchema {
	return builtin.ParameterSchema{Type: "object"}
}

func (telemetryTool) Execute(params map[string]any) (*builtin.Result, error) {
	return &builtin.Result{Success: true}, nil
}

func TestTelemetryMiddlewarePublishesToolEvents(t *testing.T) {
	hub := telemetry.NewHub()
	eventCh, unsubscribe := hub.Subscribe()
	t.Cleanup(unsubscribe)

	r := NewEmptyRegistry()
	r.Register(telemetryTool{})
	r.EnableTelemetry(hub, "session-1")

	if _, err := r.Execute("telemetry_tool", map[string]any{"param": "value"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[telemetry.EventType]bool{
		telemetry.EventToolStarted:   true,
		telemetry.EventToolCompleted: true,
	}
	got := map[telemetry.EventType]bool{}

	deadline := time.After(1 * time.Second)
	for len(got) < len(want) {
		select {
		case event := <-eventCh:
			if want[event.Type] {
				got[event.Type] = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for telemetry events: got %#v", got)
		}
	}
}

func TestTelemetryMiddlewarePublishesShellEvents(t *testing.T) {
	hub := telemetry.NewHub()
	eventCh, unsubscribe := hub.Subscribe()
	t.Cleanup(unsubscribe)

	r := NewEmptyRegistry()
	r.Register(&builtin.ShellCommandTool{})
	r.EnableTelemetry(hub, "session-2")

	if _, err := r.Execute("run_shell", map[string]any{
		"command":         "echo telemetry",
		"timeout_seconds": 5,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := map[telemetry.EventType]bool{
		telemetry.EventShellCommandStarted:   true,
		telemetry.EventShellCommandCompleted: true,
	}
	got := map[telemetry.EventType]bool{}

	deadline := time.After(2 * time.Second)
	for len(got) < len(want) {
		select {
		case event := <-eventCh:
			if want[event.Type] {
				got[event.Type] = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for shell telemetry events: got %#v", got)
		}
	}
}
