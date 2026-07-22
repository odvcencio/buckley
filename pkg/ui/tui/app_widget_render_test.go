package tui

import (
	"strings"
	"testing"

	"m31labs.dev/fluffyui/backend/sim"
)

func TestWidgetAppRendersStartupTranscript(t *testing.T) {
	be := sim.New(155, 58)
	app, err := NewWidgetApp(WidgetAppConfig{
		Backend:   be,
		ModelName: "moonshotai/kimi-k3",
	})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}

	app.WelcomeScreen()
	app.AddMessage("Resuming session: test (1 messages)", "system")
	app.AddMessage("restored conversation marker", "assistant")

	for len(app.messages) > 0 {
		if app.update(<-app.messages) {
			app.dirty = true
		}
	}
	app.render()

	capture := be.Capture()
	for _, want := range []string{"Buckley", "moonshotai/kimi-k3", "restored conversation marker", "Ready"} {
		if !strings.Contains(capture, want) {
			t.Fatalf("screen does not contain %q:\n%s", want, capture)
		}
	}
}
