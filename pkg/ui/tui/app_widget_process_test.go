package tui

import (
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/ui/backend/sim"
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
