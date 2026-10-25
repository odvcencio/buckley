package tui

import (
	"testing"

	"m31labs.dev/buckley/pkg/ui/widgets"
	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/terminal"
)

func TestWidgetAppInspector_PromptOwnsEnterAndCursorKeys(t *testing.T) {
	var submitted []string
	app := newKeyTestWidgetApp(t, WidgetAppConfig{OnSubmit: func(text string) { submitted = append(submitted, text) }})
	app.SetActivities([]widgets.ActivityRecord{{ID: "verified", Title: "run_tests", Status: widgets.ActivityCompleted}})
	drainPostedMessage(t, app)
	if !app.IsActivityPanelVisible() {
		t.Fatal("fixture inspector did not open")
	}
	app.inputArea.SetText("follow up")
	x, y := app.inputArea.CursorPosition()
	app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyLeft)})
	leftX, leftY := app.inputArea.CursorPosition()
	if leftX != x-1 || leftY != y {
		t.Fatal("inspector consumed the prompt cursor key")
	}
	app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyRight)})
	app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyEnter)})
	if len(submitted) != 1 || submitted[0] != "follow up" || app.inputArea.Text() != "" {
		t.Fatalf("first Enter did not submit the focused prompt: %v", submitted)
	}
	app.inputArea.SetText("another prompt")
	app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyEnter), Shift: true})
	if app.inputArea.Text() != "another prompt\n" || len(submitted) != 1 {
		t.Fatal("Shift+Enter did not retain multiline input")
	}
	app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyEnter)})
	if len(submitted) != 2 || submitted[1] != "another prompt" {
		t.Fatalf("subsequent prompt was not submitted: %v", submitted)
	}
}

func TestWidgetAppInspector_ExplicitPanelFocusStillNavigates(t *testing.T) {
	submitted := false
	app := newKeyTestWidgetApp(t, WidgetAppConfig{OnSubmit: func(string) { submitted = true }})
	app.SetActivities([]widgets.ActivityRecord{{ID: "one", Title: "write_file", Detail: "completed evidence"}})
	drainPostedMessage(t, app)
	app.inputArea.SetText("pending")
	app.inputArea.Blur()
	app.activityPanel.Focus()
	if !app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyEnter)}) || submitted || app.inputArea.Text() != "pending" {
		t.Fatal("focused inspector did not retain Enter")
	}
}

func TestWidgetAppOverlay_ReceivesKeysBeforeFocusedPrompt(t *testing.T) {
	submitted := false
	app := newKeyTestWidgetApp(t, WidgetAppConfig{OnSubmit: func(string) { submitted = true }})
	app.inputArea.SetText("draft\nlast")
	x, y := app.inputArea.CursorPosition()
	app.showSearchOverlay()
	app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyUp)})
	app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyEnter)})
	afterX, afterY := app.inputArea.CursorPosition()
	if afterX != x || afterY != y || submitted || app.inputArea.Text() != "draft\nlast" {
		t.Fatal("modal key escaped into the underlying prompt")
	}
	if app.screen.LayerCount() > 1 {
		app.screen.PopLayer()
	}
	// The normal input path resumes once the modal layer is gone.
	app.inputArea.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: '!'})
	if app.inputArea.Text() != "draft\nlast!" {
		t.Fatal("prompt cursor changed while an overlay owned input")
	}
}
