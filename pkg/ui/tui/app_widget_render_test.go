package tui

import (
	"fmt"
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
	app.addMessageImmediately("Resuming session: test (1 messages)", "system")
	app.addMessageImmediately("restored conversation marker", "assistant")
	if queued := len(app.messages); queued != 0 {
		t.Fatalf("startup hydration queued %d event(s), want none", queued)
	}
	app.render()

	capture := be.Capture()
	for _, want := range []string{"Buckley", "moonshotai/kimi-k3", "restored conversation marker", "Ready"} {
		if !strings.Contains(capture, want) {
			t.Fatalf("screen does not contain %q:\n%s", want, capture)
		}
	}
}

func TestWidgetAppScrollReplacesPreviousViewport(t *testing.T) {
	be := sim.New(155, 58)
	app, err := NewWidgetApp(WidgetAppConfig{Backend: be})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}

	var transcript strings.Builder
	for i := 0; i < 90; i++ {
		fmt.Fprintf(&transcript, "## SECTION_%02d\n\nparagraph_%02d with enough text to occupy a distinct rendered row\n\n", i, i)
	}
	app.addMessageImmediately(transcript.String(), "assistant")
	app.render()
	if !be.ContainsText("SECTION_89") {
		t.Fatal("initial viewport does not contain bottom marker")
	}

	app.chatView.PageUp()
	app.render()
	if be.ContainsText("SECTION_89") {
		t.Fatalf("scrolled viewport retained bottom marker:\n%s", be.Capture())
	}
}
