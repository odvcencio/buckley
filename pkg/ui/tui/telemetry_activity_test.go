package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/v2/pkg/telemetry"
	"m31labs.dev/buckley/v2/pkg/ui/widgets"
	"m31labs.dev/fluffyui/backend/sim"
)

// TestShouldFlushActivityDetail locks in the throttle boundary in isolation,
// with no wall-clock waits: the first flush (zero last time) always
// happens, calls inside the window are skipped, and calls at or past the
// window flush again.
func TestShouldFlushActivityDetail(t *testing.T) {
	base := time.Now()

	if !shouldFlushActivityDetail(time.Time{}, base) {
		t.Fatal("expected the first call (zero last flush time) to flush")
	}
	if shouldFlushActivityDetail(base, base.Add(activityFlushInterval/2)) {
		t.Fatal("expected a call inside the throttle window to be skipped")
	}
	if !shouldFlushActivityDetail(base, base.Add(activityFlushInterval)) {
		t.Fatal("expected a call exactly at the throttle window to flush")
	}
	if !shouldFlushActivityDetail(base, base.Add(2*activityFlushInterval)) {
		t.Fatal("expected a call past the throttle window to flush")
	}
}

// TestAppendActivityOutputAccumulatesEveryChunk checks that Detail always
// reflects every chunk appended so far, even though publishing to the UI is
// throttled -- the buffering fix must not drop or reorder incremental
// output.
func TestAppendActivityOutputAccumulatesEveryChunk(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()
	bridge := NewTelemetryUIBridge(hub, nil)

	var want strings.Builder
	for i := 0; i < 500; i++ {
		chunk := fmt.Sprintf("line %d\n", i)
		want.WriteString(chunk)
		bridge.AppendActivityOutput("task-1", chunk)

		got := bridge.activities["task-1"].Detail
		if got != want.String() {
			t.Fatalf("after chunk %d: Detail = %d bytes, want %d bytes", i, len(got), want.Len())
		}
	}
}

// TestAppendActivityOutputFinalContentIdentical guards the end state after a
// long stream: the concatenation of every chunk must exactly match the
// accumulated Detail, matching what the old (quadratic) `+=` implementation
// produced.
func TestAppendActivityOutputFinalContentIdentical(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()
	bridge := NewTelemetryUIBridge(hub, nil)

	var want strings.Builder
	for i := 0; i < 2000; i++ {
		chunk := fmt.Sprintf("chunk-%d ", i)
		want.WriteString(chunk)
		bridge.AppendActivityOutput("task-1", chunk)
	}

	got := bridge.activities["task-1"].Detail
	if got != want.String() {
		t.Fatalf("final Detail mismatch: got %d bytes, want %d bytes", len(got), want.Len())
	}
}

// TestAppendActivityOutputThrottlesSidebarRefresh drives many chunks through
// a real WidgetApp (without running its event loop) and drains the posted
// messages, checking that SetActivitiesMsg publishes are throttled to a
// small, bounded count instead of firing once per chunk.
func TestAppendActivityOutputThrottlesSidebarRefresh(t *testing.T) {
	app, err := NewWidgetApp(WidgetAppConfig{Backend: sim.New(80, 24)})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	hub := telemetry.NewHub()
	defer hub.Close()
	bridge := NewTelemetryUIBridge(hub, app)

	const chunks = 200
	for i := 0; i < chunks; i++ {
		bridge.AppendActivityOutput("task-1", fmt.Sprintf("line %d\n", i))
	}

	refreshCount := 0
drain:
	for {
		select {
		case msg := <-app.messages:
			if _, ok := msg.(SetActivitiesMsg); ok {
				refreshCount++
			}
		default:
			break drain
		}
	}

	if refreshCount < 1 {
		t.Fatal("expected at least one sidebar refresh")
	}
	if refreshCount >= chunks {
		t.Fatalf("refresh count = %d, want well below chunk count %d (throttle not applied)", refreshCount, chunks)
	}
}

// TestAppendActivityOutputCleansUpBuilderOnCompletion verifies that once a
// tool's completion event overwrites Detail with structured data, the
// streaming builder for that task is dropped instead of holding onto its
// buffered bytes for the rest of the session.
func TestAppendActivityOutputCleansUpBuilderOnCompletion(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()
	bridge := NewTelemetryUIBridge(hub, nil)

	bridge.AppendActivityOutput("tool-1", "partial output\n")
	if _, ok := bridge.detailBuilders["tool-1"]; !ok {
		t.Fatal("expected a streaming builder to exist while the tool is running")
	}

	bridge.handleEvent(telemetry.Event{
		Type:   telemetry.EventToolCompleted,
		TaskID: "tool-1",
		Data:   map[string]any{"result": "done"},
	})

	if _, ok := bridge.detailBuilders["tool-1"]; ok {
		t.Fatal("expected the streaming builder to be cleaned up after completion")
	}
	if !strings.Contains(bridge.activities["tool-1"].Detail, "done") {
		t.Fatalf("expected completion to overwrite Detail with structured data, got %q", bridge.activities["tool-1"].Detail)
	}
}

// TestAppendActivityOutputCreatesPlaceholderRecord guards the pre-existing
// behavior of creating a running placeholder record when output arrives
// before the tool.started event.
func TestAppendActivityOutputCreatesPlaceholderRecord(t *testing.T) {
	hub := telemetry.NewHub()
	defer hub.Close()
	bridge := NewTelemetryUIBridge(hub, nil)

	bridge.AppendActivityOutput("task-1", "hello\n")

	record, ok := bridge.activities["task-1"]
	if !ok {
		t.Fatal("expected a placeholder activity record to be created")
	}
	if record.Status != widgets.ActivityRunning || record.Title != "run_shell" {
		t.Fatalf("unexpected placeholder record: %+v", record)
	}
	if record.Detail != "hello\n" {
		t.Fatalf("Detail = %q, want %q", record.Detail, "hello\n")
	}
}
