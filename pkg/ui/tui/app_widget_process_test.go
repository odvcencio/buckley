package tui

import (
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/ui/backend/sim"
	"m31labs.dev/buckley/pkg/ui/widgets"
)

func TestFormatProcessElapsed(t *testing.T) {
	tests := []struct {
		name    string
		elapsed time.Duration
		want    string
	}{
		{name: "seconds", elapsed: 12 * time.Second, want: "12s"},
		{name: "minutes", elapsed: 73 * time.Second, want: "1m13s"},
		{name: "negative", elapsed: -time.Second, want: "0s"},
	}

	for _, tt := range tests {
		if got := formatProcessElapsed(tt.elapsed); got != tt.want {
			t.Fatalf("%s: formatProcessElapsed() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestWidgetAppProcessStatusAnimates(t *testing.T) {
	be := sim.New(80, 24)
	app, err := NewWidgetApp(WidgetAppConfig{Backend: be})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}

	now := time.Now()
	if !app.update(ProcessStatusMsg{Text: "Thinking with z-ai/glm-5.2", Active: true, ResetElapsed: true}) {
		t.Fatal("expected process status update to dirty the app")
	}
	if got := app.statusBar.Status(); !strings.Contains(got, "Thinking with z-ai/glm-5.2") || !strings.Contains(got, "(0s)") {
		t.Fatalf("unexpected process status: %q", got)
	}

	if !app.updateAnimations(now.Add(processStatusTick + time.Second)) {
		t.Fatal("expected process status animation to dirty the app")
	}
	if got := app.statusBar.Status(); !strings.Contains(got, "Thinking with z-ai/glm-5.2") {
		t.Fatalf("process status label was lost: %q", got)
	}

	if !app.update(ProcessStatusMsg{Active: false}) {
		t.Fatal("expected process stop to dirty the app")
	}
	if got := app.statusBar.Status(); got != "Ready" {
		t.Fatalf("expected status to restore to Ready, got %q", got)
	}
}

func TestWidgetAppHandleStatusMsg_RespectsActiveOverride(t *testing.T) {
	be := sim.New(80, 24)
	app, err := NewWidgetApp(WidgetAppConfig{Backend: be})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}

	now := time.Unix(100, 0)
	app.statusOverride = "Copied"
	app.statusOverrideUntil = now.Add(time.Second)
	app.statusBar.SetStatus("Copied")

	if app.handleStatusMsg(StatusMsg{Text: "Ready"}, now) {
		t.Fatal("active override should not dirty the app")
	}
	if app.statusText != "Ready" {
		t.Fatalf("statusText = %q, want Ready", app.statusText)
	}
	if got := app.statusBar.Status(); got != "Copied" {
		t.Fatalf("status bar = %q, want active override", got)
	}

	if !app.handleStatusMsg(StatusMsg{Text: "Ready"}, now.Add(2*time.Second)) {
		t.Fatal("expired override should dirty the app")
	}
	if app.statusOverride != "" || !app.statusOverrideUntil.IsZero() {
		t.Fatal("expired override should be cleared")
	}
	if got := app.statusBar.Status(); got != "Ready" {
		t.Fatalf("status bar = %q, want Ready", got)
	}
}

func TestWidgetAppHandleProcessStatusMsg_DefaultsAndStops(t *testing.T) {
	be := sim.New(80, 24)
	app, err := NewWidgetApp(WidgetAppConfig{Backend: be})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}

	now := time.Unix(200, 0)
	if !app.handleProcessStatusMsg(ProcessStatusMsg{Active: true}, now) {
		t.Fatal("starting process status should dirty the app")
	}
	if !app.processActive {
		t.Fatal("process status should be active")
	}
	if app.processText != "Working" {
		t.Fatalf("processText = %q, want Working", app.processText)
	}
	if got := app.statusBar.Status(); !strings.Contains(got, "Working") || !strings.Contains(got, "(0s)") {
		t.Fatalf("status bar = %q, want Working with elapsed time", got)
	}

	app.statusText = "Ready"
	if !app.handleProcessStatusMsg(ProcessStatusMsg{Active: false}, now.Add(time.Second)) {
		t.Fatal("stopping active process status should dirty the app")
	}
	if app.processActive || app.processText != "" || !app.processStarted.IsZero() {
		t.Fatalf("process state was not cleared: active=%v text=%q started=%v", app.processActive, app.processText, app.processStarted)
	}
	if got := app.statusBar.Status(); got != "Ready" {
		t.Fatalf("status bar = %q, want Ready", got)
	}
}

func TestWidgetAppSidebarDoesNotAutoShowForTransientTools(t *testing.T) {
	be := sim.New(120, 24)
	app, err := NewWidgetApp(WidgetAppConfig{Backend: be})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	app.minWidthForSidebar = 1

	if app.IsSidebarVisible() {
		t.Fatal("sidebar should start hidden when it has no content")
	}

	app.SetRunningTools([]widgets.RunningTool{{ID: "tool-1", Name: "read_file", Command: "AGENTS.md"}})
	if app.IsSidebarVisible() {
		t.Fatal("transient tool telemetry should not auto-show sidebar")
	}
	if !app.sidebar.HasContent() {
		t.Fatal("sidebar should retain telemetry content while hidden")
	}

	app.SetSidebarVisible(true)
	if !app.IsSidebarVisible() {
		t.Fatal("explicit sidebar visibility should show sidebar at wide terminal widths")
	}

	app.SetRunningTools(nil)
	if !app.IsSidebarVisible() {
		t.Fatal("sidebar should not auto-hide when transient telemetry clears")
	}
}
