package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/ui/widgets"
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
	regained := resolveWorkspaceVisibility(90, true, true, 44, 26)
	if regained.left || !regained.right {
		t.Fatalf("regained visibility = %+v, want inspector alone once the navigator collapses", regained)
	}
}

// drainPostedMessage applies the next message SetActivities (or any other
// Post-based call) queued on the UI event channel. SetActivities is
// thread-safe via message passing, like AddMessage, so tests that call it
// outside the running event loop must pump the queue themselves.
func drainPostedMessage(t *testing.T, app *WidgetApp) {
	t.Helper()
	select {
	case msg := <-app.messages:
		app.update(msg)
	default:
		t.Fatal("expected a message to be queued")
	}
}

func TestSetActivitiesAutoOpensInspectorOnce(t *testing.T) {
	app := newKeyTestWidgetApp(t, WidgetAppConfig{})
	app.SetActivities([]widgets.ActivityRecord{{ID: "one", Title: "read_file", Status: widgets.ActivityRunning}})
	drainPostedMessage(t, app)
	if !app.activityPanelWanted || !app.IsActivityPanelVisible() {
		t.Fatal("first activity should open inspector in a wide terminal")
	}
	app.SetActivityPanelVisible(false)
	app.SetActivities([]widgets.ActivityRecord{{ID: "two", Title: "write_file", Status: widgets.ActivityRunning}})
	drainPostedMessage(t, app)
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

func TestTelemetryBridgeStreamsShellOutputIntoRunningDetail(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()
	bridge := NewTelemetryUIBridge(hub, nil)
	bridge.handleEvent(telemetry.Event{Type: telemetry.EventToolStarted, TaskID: "shell-1", Data: map[string]any{
		"toolName": "run_shell", "command": "go build ./...",
	}})

	// Simulate a shell sink emitting bounded chunks as a long build runs.
	bridge.AppendActivityOutput("shell-1", "compiling pkg/a\n")
	afterFirst := bridge.activities["shell-1"].Detail
	bridge.AppendActivityOutput("shell-1", "compiling pkg/b\n")
	afterSecond := bridge.activities["shell-1"].Detail

	if len(afterSecond) <= len(afterFirst) {
		t.Fatalf("detail did not grow incrementally: first=%q second=%q", afterFirst, afterSecond)
	}
	if !strings.Contains(afterSecond, "compiling pkg/a") || !strings.Contains(afterSecond, "compiling pkg/b") {
		t.Fatalf("detail missing streamed chunks: %q", afterSecond)
	}
	if bridge.activities["shell-1"].Status != widgets.ActivityRunning {
		t.Fatalf("streamed record should still be running, got %s", bridge.activities["shell-1"].Status)
	}
}

func TestTelemetryBridgeAppendActivityOutputCreatesPlaceholderRecord(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()
	bridge := NewTelemetryUIBridge(hub, nil)

	// The sink can fire before tool.started arrives; the inspector should
	// still show a running placeholder rather than dropping the output.
	bridge.AppendActivityOutput("shell-2", "first line\n")
	record, ok := bridge.activities["shell-2"]
	if !ok || record.Status != widgets.ActivityRunning || !strings.Contains(record.Detail, "first line") {
		t.Fatalf("expected running placeholder with streamed output, got %+v (ok=%v)", record, ok)
	}
}

func TestTelemetryBridgeEvictsOldCompletedActivities(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()
	bridge := NewTelemetryUIBridge(hub, nil)

	for i := 0; i < 500; i++ {
		taskID := fmt.Sprintf("tool-%d", i)
		bridge.handleEvent(telemetry.Event{
			Type:      telemetry.EventToolCompleted,
			TaskID:    taskID,
			Timestamp: time.Now().Add(time.Duration(i) * time.Millisecond),
			Data:      map[string]any{"toolName": "read_file", "result": strings.Repeat("x", 1024)},
		})
	}
	bridge.handleEvent(telemetry.Event{Type: telemetry.EventToolStarted, TaskID: "running-1", Data: map[string]any{"toolName": "run_shell"}})

	runningCount := 0
	for _, record := range bridge.activities {
		if record.Status == widgets.ActivityRunning {
			runningCount++
		}
	}
	if len(bridge.activities) > maxActivityEntries+runningCount {
		t.Fatalf("activities map not evicted: len=%d, want <= %d", len(bridge.activities), maxActivityEntries+runningCount)
	}
	if runningCount != 1 {
		t.Fatalf("expected the running record to survive eviction, got %d running", runningCount)
	}
}
