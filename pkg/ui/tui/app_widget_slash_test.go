package tui

import (
	"fmt"
	"reflect"
	"testing"

	"m31labs.dev/buckley/pkg/ui/widgets"
	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/terminal"
)

func typeSlashTestText(app *WidgetApp, text string) {
	for _, r := range text {
		app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyRune), Rune: r})
	}
}

func TestWidgetApp_SlashCommandArgumentsReachSubmit(t *testing.T) {
	for _, tc := range []struct{ typed, want string }{
		{"/resume session/branch-1", "/resume session/branch-1"},
		{"/task read_only Inspect λ → file", "/task read_only Inspect λ → file"},
		{"/model provider/model", "/model provider/model"},
		{"/export My Report α.md", "/export My Report α.md"},
		{"/agent send worker-a next step", "/agent send worker-a next step"},
		{"/skill project-playbook", "/skill project-playbook"},
		{"/ReSuMe saved-session", "/resume saved-session"},
		{"/does-not-exist value", "/does-not-exist value"},
	} {
		t.Run(tc.typed, func(t *testing.T) {
			var submitted []string
			app := newKeyTestWidgetApp(t, WidgetAppConfig{OnSubmit: func(text string) { submitted = append(submitted, text) }})
			typeSlashTestText(app, tc.typed)
			app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyEnter)})
			if !reflect.DeepEqual(submitted, []string{tc.want}) {
				t.Fatalf("submitted=%q want=%q", submitted, tc.want)
			}
			if app.screen.LayerCount() != 1 || app.inputArea.HasText() || app.inputArea.Mode() != widgets.ModeNormal {
				t.Fatal("command submission left a modal or stale input mode")
			}
		})
	}
}

func TestWidgetApp_SlashPickerSelectionAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		query string
		key   terminal.Key
		want  []string
		input string
	}{
		{"/help", terminal.KeyEnter, []string{"/help"}, ""},
		{"/res", terminal.KeyEnter, nil, "/resume "},
		{"/saved sessions", terminal.KeyEnter, []string{"/sessions"}, ""},
		{"/does-not-exist", terminal.KeyEnter, []string{"/does-not-exist"}, ""},
		{"/res", terminal.KeyEscape, nil, ""},
	} {
		t.Run(fmt.Sprintf("%s/%d", tc.query, tc.key), func(t *testing.T) {
			var submitted []string
			app := newKeyTestWidgetApp(t, WidgetAppConfig{OnSubmit: func(text string) { submitted = append(submitted, text) }})
			typeSlashTestText(app, tc.query)
			app.handleKeyMsg(KeyMsg{Key: int(tc.key)})
			if !reflect.DeepEqual(submitted, tc.want) || app.inputArea.Text() != tc.input || app.screen.LayerCount() != 1 {
				t.Fatalf("submitted=%q input=%q layers=%d", submitted, app.inputArea.Text(), app.screen.LayerCount())
			}
		})
	}
}

func TestWidgetApp_SlashArgumentPrefillAcceptsPastedText(t *testing.T) {
	var submitted []string
	app := newKeyTestWidgetApp(t, WidgetAppConfig{OnSubmit: func(text string) { submitted = append(submitted, text) }})
	typeSlashTestText(app, "/resume ")
	if app.screen.LayerCount() != 1 || app.inputArea.Text() != "/resume " {
		t.Fatal("space did not move argument entry into the main input")
	}
	app.inputArea.InsertText("session/α branch")
	app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyEnter)})
	if !reflect.DeepEqual(submitted, []string{"/resume session/α branch"}) {
		t.Fatalf("pasted arguments lost: %q", submitted)
	}
}

func TestWidgetApp_CommandPrefillResetsShellMode(t *testing.T) {
	var submitted []string
	app := newKeyTestWidgetApp(t, WidgetAppConfig{OnSubmit: func(text string) { submitted = append(submitted, text) }})
	app.inputArea.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: '!'})
	if app.inputArea.Mode() != widgets.ModeShell {
		t.Fatal("fixture did not enter shell mode")
	}
	app.prefillInput("/task ")
	typeSlashTestText(app, "read_only inspect")
	app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyEnter)})
	if !reflect.DeepEqual(submitted, []string{"/task read_only inspect"}) {
		t.Fatalf("command used stale shell mode: %q", submitted)
	}
}

func TestWidgetApp_CommandPaletteSearchStillAcceptsSpaces(t *testing.T) {
	var submitted []string
	app := newKeyTestWidgetApp(t, WidgetAppConfig{OnSubmit: func(text string) { submitted = append(submitted, text) }})
	app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyCtrlP)})
	typeSlashTestText(app, "show help")
	app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyEnter)})
	if !reflect.DeepEqual(submitted, []string{"/help"}) || app.screen.LayerCount() != 1 {
		t.Fatalf("regular command search changed: %q", submitted)
	}
}

func TestWidgetApp_SlashPickerArrowSelection(t *testing.T) {
	var submitted []string
	app := newKeyTestWidgetApp(t, WidgetAppConfig{OnSubmit: func(text string) { submitted = append(submitted, text) }})
	typeSlashTestText(app, "/")
	app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyDown)})
	app.handleKeyMsg(KeyMsg{Key: int(terminal.KeyEnter)})
	if !reflect.DeepEqual(submitted, []string{"/commit"}) || app.screen.LayerCount() != 1 {
		t.Fatalf("arrow selection changed: submitted=%q layers=%d", submitted, app.screen.LayerCount())
	}
}
