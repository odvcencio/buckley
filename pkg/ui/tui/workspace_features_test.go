package tui

import (
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/v2/pkg/telemetry"
	"m31labs.dev/buckley/v2/pkg/ui/widgets"
)

func TestResolveWorkspaceVisibilityPrioritizesChat(t *testing.T) {
	wide := resolveWorkspaceVisibility(140, true, true, 26, 40)
	if !wide.left || !wide.right {
		t.Fatalf("wide visibility = %+v, want both panels", wide)
	}
	medium := resolveWorkspaceVisibility(90, true, true, 26, 40)
	if !medium.left || medium.right {
		t.Fatalf("medium visibility = %+v, want navigator only", medium)
	}
	narrow := resolveWorkspaceVisibility(60, true, true, 26, 40)
	if narrow.left || narrow.right {
		t.Fatalf("narrow visibility = %+v, want chat only", narrow)
	}
}

func TestSetActivitiesAutoOpensInspectorOnce(t *testing.T) {
	app := newKeyTestWidgetApp(t, WidgetAppConfig{})
	app.SetActivities([]widgets.ActivityRecord{{ID: "one", Title: "read_file", Status: widgets.ActivityRunning}})
	if !app.activityPanelWanted || !app.IsActivityPanelVisible() {
		t.Fatal("first activity should open inspector in a wide terminal")
	}
	app.SetActivityPanelVisible(false)
	app.SetActivities([]widgets.ActivityRecord{{ID: "two", Title: "write_file", Status: widgets.ActivityRunning}})
	if app.IsActivityPanelVisible() {
		t.Fatal("explicitly hidden inspector should stay hidden")
	}
}

func TestWorkspacePanelResizeClamps(t *testing.T) {
	if got := clampWorkspacePanel(4, 18, 44); got != 18 {
		t.Fatalf("low clamp = %d, want 18", got)
	}
	if got := clampWorkspacePanel(100, 18, 44); got != 44 {
		t.Fatalf("high clamp = %d, want 44", got)
	}
}

func TestTelemetryBridgeRetainsCompletedToolActivity(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()
	bridge := NewTelemetryUIBridge(hub, nil)
	now := time.Now()
	bridge.handleEvent(telemetry.Event{Type: telemetry.EventToolStarted, TaskID: "tool-1", Timestamp: now, Data: map[string]any{
		"toolName": "read_file", "filePath": "pkg/model/manager.go", "arguments": "{\"path\":\"pkg/model/manager.go\"}",
	}})
	bridge.handleEvent(telemetry.Event{Type: telemetry.EventToolCompleted, TaskID: "tool-1", Timestamp: now.Add(time.Second), Data: map[string]any{
		"toolName": "read_file", "filePath": "pkg/model/manager.go", "result": "full result",
	}})
	record, ok := bridge.activities["tool-1"]
	if !ok || record.Status != widgets.ActivityCompleted || !strings.Contains(record.Detail, "full result") {
		t.Fatalf("completed activity not retained: %+v", record)
	}
}

func TestTelemetryBridgeCapturesSubagentOutput(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()
	bridge := NewTelemetryUIBridge(hub, nil)
	bridge.handleEvent(telemetry.Event{Type: telemetry.EventSubagentCompleted, TaskID: "agent-1", Data: map[string]any{
		"agent": "reviewer", "state": "completed", "task": "inspect parser", "output": "found two issues",
	}})
	record := bridge.activities["agent-1"]
	if record.Title != "agent:reviewer" || !strings.Contains(record.Detail, "found two issues") {
		t.Fatalf("unexpected subagent activity: %+v", record)
	}
}
