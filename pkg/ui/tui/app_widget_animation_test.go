package tui

import (
	"testing"
	"time"

	"m31labs.dev/fluffyui/backend/sim"
)

func TestWidgetAppUpdateAnimations_ExpiresStatusOverrideAndCtrlC(t *testing.T) {
	be := sim.New(80, 24)
	app, err := NewWidgetApp(WidgetAppConfig{Backend: be})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}

	now := time.Unix(300, 0)
	app.statusOverride = "Copied"
	app.statusOverrideUntil = now.Add(-time.Nanosecond)
	app.statusBar.SetStatus("Copied")
	app.ctrlCArmedUntil = now.Add(-time.Nanosecond)

	if !app.updateAnimations(now) {
		t.Fatal("expired status override should dirty the app")
	}
	if app.statusOverride != "" || !app.statusOverrideUntil.IsZero() {
		t.Fatal("expired status override should be cleared")
	}
	if !app.ctrlCArmedUntil.IsZero() {
		t.Fatal("expired ctrl-c arm should be cleared")
	}
	if got := app.statusBar.Status(); got != "Ready" {
		t.Fatalf("status = %q, want Ready", got)
	}
}

func TestWidgetAppTickCursorPulse_RespectsInterval(t *testing.T) {
	be := sim.New(80, 24)
	app, err := NewWidgetApp(WidgetAppConfig{Backend: be})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}

	now := time.Unix(400, 0)
	app.cursorPulsePeriod = time.Second
	app.cursorPulseInterval = 50 * time.Millisecond
	app.cursorPulseStart = now.Add(-250 * time.Millisecond)
	app.cursorPulseLast = now.Add(-app.cursorPulseInterval)
	app.cursorStyle = app.cursorStyleForPhase(0)
	app.inputArea.SetCursorStyle(app.cursorStyle)

	if !app.tickCursorPulse(now) {
		t.Fatal("cursor pulse should dirty when the interval has elapsed")
	}
	if app.cursorPulseLast != now {
		t.Fatalf("cursorPulseLast = %v, want %v", app.cursorPulseLast, now)
	}

	if app.tickCursorPulse(now.Add(10 * time.Millisecond)) {
		t.Fatal("cursor pulse should not dirty before the next interval")
	}
}
