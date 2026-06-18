package tui

import (
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/ui/backend/sim"
	"m31labs.dev/buckley/pkg/ui/terminal"
)

func newKeyTestWidgetApp(t *testing.T, cfg WidgetAppConfig) *WidgetApp {
	t.Helper()

	cfg.Backend = sim.New(120, 24)
	app, err := NewWidgetApp(cfg)
	if err != nil {
		t.Fatalf("NewWidgetApp: %v", err)
	}
	t.Cleanup(app.backend.Fini)
	return app
}

func TestWidgetAppCtrlCClearsInputBeforeQuit(t *testing.T) {
	quitCount := 0
	app := newKeyTestWidgetApp(t, WidgetAppConfig{
		OnQuit: func() {
			quitCount++
		},
	})
	app.running = true
	app.inputArea.SetText("pending prompt")

	if !app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyCtrlC)}) {
		t.Fatal("first Ctrl+C should dirty the app after clearing input")
	}
	if got := app.inputArea.Text(); got != "" {
		t.Fatalf("input text = %q, want empty", got)
	}
	if quitCount != 0 {
		t.Fatalf("quit count = %d, want 0 after first Ctrl+C", quitCount)
	}
	if got := app.statusBar.Status(); !strings.Contains(got, "Input cleared") {
		t.Fatalf("status = %q, want input-cleared prompt", got)
	}

	if app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyCtrlC)}) {
		t.Fatal("second Ctrl+C should exit without requesting another render")
	}
	if quitCount != 1 {
		t.Fatalf("quit count = %d, want 1 after second Ctrl+C", quitCount)
	}
	if app.running {
		t.Fatal("app should no longer be running after confirmed quit")
	}
}

func TestWidgetAppGlobalControlShortcuts(t *testing.T) {
	app := newKeyTestWidgetApp(t, WidgetAppConfig{})
	app.minWidthForSidebar = 1

	if !app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyCtrlB)}) {
		t.Fatal("Ctrl+B should be handled")
	}
	if !app.IsSidebarVisible() {
		t.Fatal("Ctrl+B should show the sidebar at wide terminal widths")
	}

	layers := app.screen.LayerCount()
	if !app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyCtrlP)}) {
		t.Fatal("Ctrl+P should be handled")
	}
	if got := app.screen.LayerCount(); got != layers+1 {
		t.Fatalf("layer count = %d, want %d after command palette", got, layers+1)
	}
}

func TestWidgetAppAltSessionNavigation(t *testing.T) {
	app := newKeyTestWidgetApp(t, WidgetAppConfig{})
	nextCount := 0
	prevCount := 0
	app.SetSessionCallbacks(
		func() { nextCount++ },
		func() { prevCount++ },
	)

	if !app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyLeft), Alt: true}) {
		t.Fatal("Alt+Left should be handled")
	}
	if !app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyRight), Alt: true}) {
		t.Fatal("Alt+Right should be handled")
	}
	if prevCount != 1 || nextCount != 1 {
		t.Fatalf("prev/next counts = %d/%d, want 1/1", prevCount, nextCount)
	}
}

func TestWidgetAppFocusedInputNavigationWinsBeforeChatScroll(t *testing.T) {
	app := newKeyTestWidgetApp(t, WidgetAppConfig{})
	app.inputArea.SetText("short\nmuch longer line")
	beforeX, _ := app.inputArea.CursorPosition()

	if !app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyUp)}) {
		t.Fatal("Up should be handled by multiline input navigation")
	}

	afterX, _ := app.inputArea.CursorPosition()
	if afterX >= beforeX {
		t.Fatalf("cursor x = %d, want less than %d after input Up", afterX, beforeX)
	}
}

func TestWidgetAppRuneKeyTypesIntoInput(t *testing.T) {
	app := newKeyTestWidgetApp(t, WidgetAppConfig{})

	if !app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyRune), Rune: 'x'}) {
		t.Fatal("rune key should be handled by input")
	}
	if got := app.inputArea.Text(); got != "x" {
		t.Fatalf("input text = %q, want x", got)
	}
}
