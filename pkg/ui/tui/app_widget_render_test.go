package tui

import (
	"strings"
	"testing"

	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/backend/sim"
)

type clearCountingBackend struct {
	backend.Backend
	clears int
}

func (b *clearCountingBackend) Clear() {
	b.clears++
	b.Backend.Clear()
}

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

func TestWidgetAppResyncsAfterViewportChanges(t *testing.T) {
	simBackend := sim.New(80, 24)
	be := &clearCountingBackend{Backend: simBackend}
	app, err := NewWidgetApp(WidgetAppConfig{Backend: be})
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}

	for i := 0; i < 40; i++ {
		app.addMessageImmediately("viewport resync marker", "assistant")
	}
	app.render()
	initialClears := be.clears

	simBackend.Resize(120, 30)
	app.handleResizeMsg(ResizeMsg{Width: 120, Height: 30})
	app.render()
	if be.clears != initialClears+1 {
		t.Fatalf("resize clears = %d, want %d", be.clears, initialClears+1)
	}

	app.chatView.ScrollUp(1)
	app.render()
	if be.clears != initialClears+2 {
		t.Fatalf("scroll clears = %d, want %d", be.clears, initialClears+2)
	}
}
