package tui

import (
	"strings"
	"testing"

	uiruntime "m31labs.dev/fluffyui/runtime"
)

func layoutChatForMouseTest(app *WidgetApp) {
	app.chatView.Layout(uiruntime.Rect{
		X:      0,
		Y:      0,
		Width:  40,
		Height: 4,
	})
}

func TestWidgetAppHandleMouseMsg_MouseWheelScrollsChat(t *testing.T) {
	app := newKeyTestWidgetApp(t, WidgetAppConfig{})
	layoutChatForMouseTest(app)
	for i := 0; i < 12; i++ {
		app.chatView.AddMessage("line", "system")
	}
	beforeTop, _, _ := app.chatView.ScrollPosition()

	if !app.handleMouseMsg(MouseMsg{Button: MouseWheelUp}) {
		t.Fatal("mouse wheel up should be handled")
	}
	afterUpTop, _, _ := app.chatView.ScrollPosition()
	if afterUpTop >= beforeTop {
		t.Fatalf("scroll top = %d, want less than %d after wheel up", afterUpTop, beforeTop)
	}

	if !app.handleMouseMsg(MouseMsg{Button: MouseWheelDown}) {
		t.Fatal("mouse wheel down should be handled")
	}
	afterDownTop, _, _ := app.chatView.ScrollPosition()
	if afterDownTop <= afterUpTop {
		t.Fatalf("scroll top = %d, want greater than %d after wheel down", afterDownTop, afterUpTop)
	}
}

func TestWidgetAppHandleMouseMsg_LeftMouseStartsSelection(t *testing.T) {
	app := newKeyTestWidgetApp(t, WidgetAppConfig{})
	layoutChatForMouseTest(app)
	app.chatView.AddMessage("hello world", "system")

	if !app.handleMouseMsg(MouseMsg{X: 1, Y: 0, Button: MouseLeft, Action: MousePress}) {
		t.Fatal("left click inside chat should be handled")
	}

	if !app.selectionActive {
		t.Fatal("selection should be active after left mouse press")
	}
	if !app.selectionLastValid {
		t.Fatal("selection should remember the last valid point")
	}
	if app.selectionLastLine != 0 || app.selectionLastCol != 1 {
		t.Fatalf("selection point = (%d,%d), want (0,1)", app.selectionLastLine, app.selectionLastCol)
	}
	if !app.dirty {
		t.Fatal("left mouse press should dirty the app")
	}
}

func TestWidgetAppHandleMouseMsg_EmptySelectionReleaseClearsTracking(t *testing.T) {
	app := newKeyTestWidgetApp(t, WidgetAppConfig{})
	layoutChatForMouseTest(app)
	app.chatView.AddMessage("hello world", "system")

	app.handleMouseMsg(MouseMsg{X: 1, Y: 0, Button: MouseLeft, Action: MousePress})
	app.dirty = false

	if !app.handleMouseMsg(MouseMsg{X: 1, Y: 0, Button: MouseLeft, Action: MouseRelease}) {
		t.Fatal("mouse release should finish an active selection")
	}

	if app.selectionActive {
		t.Fatal("selection should not remain active after release")
	}
	if app.selectionLastValid {
		t.Fatal("selection tracking should reset after release")
	}
	if app.chatView.HasSelection() {
		t.Fatal("empty selection should be cleared")
	}
	if !app.dirty {
		t.Fatal("selection release should dirty the app")
	}
}

func TestWidgetAppHandleMouseMsg_RightMouseClearsSelection(t *testing.T) {
	app := newKeyTestWidgetApp(t, WidgetAppConfig{})
	layoutChatForMouseTest(app)
	app.chatView.AddMessage("hello world", "system")
	app.chatView.StartSelection(0, 0)
	app.chatView.UpdateSelection(0, 5)
	app.selectionActive = true
	app.selectionLastValid = true
	app.dirty = false

	if !app.handleMouseMsg(MouseMsg{X: 1, Y: 0, Button: MouseRight, Action: MousePress}) {
		t.Fatal("right click inside chat should be handled")
	}

	if app.selectionActive {
		t.Fatal("right click should cancel active selection tracking")
	}
	if app.selectionLastValid {
		t.Fatal("right click should clear the last valid selection point")
	}
	if app.chatView.HasSelection() {
		t.Fatal("right click should clear selected text")
	}
	if !strings.Contains(app.statusBar.Status(), "Selection cleared") {
		t.Fatalf("status = %q, want selection-cleared message", app.statusBar.Status())
	}
	if !app.dirty {
		t.Fatal("right click should dirty the app")
	}
}
