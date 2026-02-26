package tui

import (
	"time"

	"github.com/odvcencio/fluffyui/keybind"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/terminal"
)

// registerKeyBindings adds Buckley-specific key bindings.
func (r *Runner) registerKeyBindings(registry *keybind.CommandRegistry) {
	// Register fluffyui standard commands (quit, refresh, focus, scroll, clipboard)
	keybind.RegisterStandardCommands(registry)
	keybind.RegisterScrollCommands(registry)
	keybind.RegisterClipboardCommands(registry)

	registry.Register(keybind.Command{
		ID:    overlayNoopCommand,
		Title: "Overlay No-op",
		Handler: func(ctx keybind.Context) {
		},
	})

	// Register Buckley-specific commands
	registry.Register(keybind.Command{
		ID:    "buckley.toggle-sidebar",
		Title: "Toggle Sidebar",
		Handler: func(ctx keybind.Context) {
			r.toggleSidebar()
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.next-session",
		Title: "Next Session",
		Handler: func(ctx keybind.Context) {
			if r.onNextSession != nil {
				r.onNextSession()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.prev-session",
		Title: "Previous Session",
		Handler: func(ctx keybind.Context) {
			if r.onPrevSession != nil {
				r.onPrevSession()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.search",
		Title: "Search",
		Handler: func(ctx keybind.Context) {
			r.showSearchOverlay()
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.file-picker",
		Title: "File Picker",
		Handler: func(ctx keybind.Context) {
			r.showFilePicker()
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.settings",
		Title: "Settings",
		Handler: func(ctx keybind.Context) {
			r.showSettingsOverlay()
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.focus-input",
		Title: "Focus Input",
		Handler: func(ctx keybind.Context) {
			r.ensureInputFocus()
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.debug-focus",
		Title: "Debug Focus State",
		Handler: func(ctx keybind.Context) {
			debugInfo := r.DebugFocusState()
			if r.statusService != nil {
				r.statusService.SetStatusOverride(debugInfo, 5*time.Second)
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.toggle-task",
		Title: "Toggle Current Task",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.ToggleCurrentTask()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.toggle-plan",
		Title: "Toggle Plan",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.TogglePlan()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.toggle-tools",
		Title: "Toggle Tools",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.ToggleTools()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.toggle-context",
		Title: "Toggle Context",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.ToggleContext()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.toggle-touches",
		Title: "Toggle Touches",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.ToggleTouches()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.toggle-files",
		Title: "Toggle Recent Files",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.ToggleRecentFiles()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.prev-tab",
		Title: "Sidebar Previous Tab",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.PrevTab()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.next-tab",
		Title: "Sidebar Next Tab",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.NextTab()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.shrink",
		Title: "Sidebar Shrink",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.Shrink(2)
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.grow",
		Title: "Sidebar Grow",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.Grow(2)
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.scroll-top",
		Title: "Scroll Top",
		Handler: func(ctx keybind.Context) {
			if r.chatView == nil {
				return
			}
			r.chatView.ScrollToTop()
			if ctx.App != nil {
				ctx.App.Invalidate()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.scroll-bottom",
		Title: "Scroll Bottom",
		Handler: func(ctx keybind.Context) {
			if r.chatView == nil {
				return
			}
			r.chatView.ScrollToBottom()
			if ctx.App != nil {
				ctx.App.Invalidate()
			}
		},
	})
}

func buckleyKeyBindings(whenScrollable, whenSidebarFocused, whenInputEmpty keybind.Condition) []keyBindingDef {
	return []keyBindingDef{
		{sequence: "ctrl+q", command: "app.quit", allowInModal: true},
		{sequence: "ctrl+c", command: "app.quit", when: whenInputEmpty, allowInModal: true},
		{sequence: "f5", command: "app.refresh", allowInModal: true},
		{sequence: "tab", command: "focus.next", allowInModal: true},
		{sequence: "shift+tab", command: "focus.prev", allowInModal: true},
		{sequence: "ctrl+up", command: "scroll.up", when: whenScrollable, allowInModal: true},
		{sequence: "ctrl+down", command: "scroll.down", when: whenScrollable, allowInModal: true},
		{sequence: "pgup", command: "scroll.pageUp", when: whenScrollable, allowInModal: true},
		{sequence: "pgdn", command: "scroll.pageDown", when: whenScrollable, allowInModal: true},
		{sequence: "home", command: "scroll.home", when: whenScrollable, allowInModal: true},
		{sequence: "end", command: "scroll.end", when: whenScrollable, allowInModal: true},
		{sequence: "ctrl+shift+c", command: "clipboard.copy", when: keybind.WhenFocusedClipboardTarget(), allowInModal: true},
		{sequence: "ctrl+shift+x", command: "clipboard.cut", when: keybind.WhenFocusedClipboardTarget(), allowInModal: true},
		{sequence: "ctrl+shift+v", command: "clipboard.paste", when: keybind.WhenFocusedClipboardTarget(), allowInModal: true},
		{sequence: "ctrl+b", command: "buckley.toggle-sidebar"},
		{sequence: "alt+right", command: "buckley.next-session"},
		{sequence: "alt+left", command: "buckley.prev-session"},
		{sequence: "ctrl+f", command: "buckley.search"},
		{sequence: "ctrl+p", command: "buckley.file-picker"},
		{sequence: "ctrl+i", command: "buckley.focus-input"},
		{sequence: "1", command: "buckley.sidebar.toggle-task", when: whenSidebarFocused},
		{sequence: "2", command: "buckley.sidebar.toggle-plan", when: whenSidebarFocused},
		{sequence: "3", command: "buckley.sidebar.toggle-tools", when: whenSidebarFocused},
		{sequence: "4", command: "buckley.sidebar.toggle-context", when: whenSidebarFocused},
		{sequence: "5", command: "buckley.sidebar.toggle-touches", when: whenSidebarFocused},
		{sequence: "6", command: "buckley.sidebar.toggle-files", when: whenSidebarFocused},
		{sequence: "ctrl+g g", command: "buckley.scroll-top"},
		{sequence: "ctrl+g b", command: "buckley.scroll-bottom"},
		{sequence: "ctrl+g s", command: "buckley.toggle-sidebar"},
		{sequence: "ctrl+g f", command: "buckley.search"},
		{sequence: "ctrl+g p", command: "buckley.file-picker"},
		{sequence: "ctrl+g i", command: "buckley.focus-input"},
		{sequence: "ctrl+g t", command: "buckley.settings"},
		{sequence: "ctrl+g ,", command: "buckley.sidebar.prev-tab"},
		{sequence: "ctrl+g .", command: "buckley.sidebar.next-tab"},
		{sequence: "ctrl+g [", command: "buckley.sidebar.shrink"},
		{sequence: "ctrl+g ]", command: "buckley.sidebar.grow"},
		{sequence: "ctrl+g d", command: "buckley.debug-focus"},
	}
}

func buildKeymap(defs []keyBindingDef) *keybind.Keymap {
	bindings := make([]keybind.Binding, 0, len(defs))
	for _, def := range defs {
		bindings = append(bindings, keybind.Binding{
			Key:     keybind.MustParseKeySequence(def.sequence),
			Command: def.command,
			When:    def.when,
		})
	}
	return &keybind.Keymap{Name: "buckley", Bindings: bindings}
}

func newOverlayKeymap(defs []keyBindingDef) *keybind.Keymap {
	bindings := make([]keybind.Binding, 0, len(defs))
	for _, def := range defs {
		if def.allowInModal {
			continue
		}
		bindings = append(bindings, keybind.Binding{
			Key:     keybind.MustParseKeySequence(def.sequence),
			Command: overlayNoopCommand,
		})
	}
	return &keybind.Keymap{Name: "buckley.overlay", Bindings: bindings}
}

func normalizeCtrlGChord(msg runtime.KeyMsg) runtime.KeyMsg {
	if msg.Key == terminal.KeyNone && msg.Rune == 7 {
		msg.Key = terminal.KeyRune
		msg.Rune = 'g'
		msg.Ctrl = true
		return msg
	}
	if msg.Key == terminal.KeyNone && msg.Ctrl && (msg.Rune == 'g' || msg.Rune == 'G') {
		msg.Key = terminal.KeyRune
		msg.Rune = 'g'
		return msg
	}
	if msg.Key == terminal.KeyRune && msg.Rune == 7 {
		msg.Rune = 'g'
		msg.Ctrl = true
		return msg
	}
	if msg.Key != terminal.KeyRune || !msg.Ctrl {
		return msg
	}
	if msg.Rune != 'g' && msg.Rune != 'G' {
		return msg
	}
	msg.Rune = 'g'
	return msg
}
