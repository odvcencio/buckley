// Package tui provides the integrated terminal user interface for Buckley.
// This file implements helper methods for the fluffyui-native Runner.

package tui

import (
	"strconv"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// showFilePicker displays the file picker overlay.
func (r *Runner) showFilePicker() {
	if r.filePicker == nil || r.app == nil {
		return
	}

	// For now, just trigger the callback with empty path
	// The actual file picker implementation would be more complex
	if r.onFileSelect != nil {
		r.onFileSelect("")
	}
}

// showSearchOverlay displays the search overlay.
func (r *Runner) showSearchOverlay() {
	if r.app == nil {
		return
	}

	// Create search widget
	searchWidget := uiwidgets.NewSearchWidget()
	searchWidget.SetOnSearch(func(query string) {
		r.chatView.Search(query)
	})
	searchWidget.SetOnNavigate(func() {
		r.chatView.NextMatch()
	}, func() {
		r.chatView.PrevMatch()
	})
	searchWidget.SetOnClose(func() {
		r.app.ExecuteCommand(runtime.PopOverlay{})
		r.chatView.ClearSearch()
	})

	// Show as overlay
	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: searchWidget,
		Modal:  false,
	})
}

// showSlashCommandPalette displays the slash command palette.
func (r *Runner) showSlashCommandPalette() {
	if r.app == nil {
		return
	}

	items := []uiwidgets.PaletteItem{
		{ID: "/new", Label: "/new", Description: "Start a new session"},
		{ID: "/clear", Label: "/clear", Description: "Clear the current conversation"},
		{ID: "/sessions", Label: "/sessions", Description: "List active sessions"},
		{ID: "/next", Label: "/next", Description: "Switch to next session"},
		{ID: "/prev", Label: "/prev", Description: "Switch to previous session"},
		{ID: "/model", Label: "/model", Description: "Pick or set the execution model"},
		{ID: "/mode", Label: "/mode", Description: "Switch execution mode (classic/rlm)"},
		{ID: "/skill", Label: "/skill", Description: "List or activate a skill"},
		{ID: "/context", Label: "/context", Description: "Show context budget details"},
		{ID: "/search", Label: "/search", Description: "Search conversation history"},
		{ID: "/export", Label: "/export", Description: "Export current session"},
		{ID: "/import", Label: "/import", Description: "Import conversation file"},
		{ID: "/review", Label: "/review", Description: "Review current git diff"},
		{ID: "/commit", Label: "/commit", Description: "Generate commit message"},
		{ID: "/help", Label: "/help", Description: "Show available commands"},
		{ID: "/quit", Label: "/quit", Description: "Exit Buckley"},
	}

	palette := uiwidgets.NewPaletteWidget("Commands")
	palette.SetItems(items)
	palette.SetOnSelect(func(item uiwidgets.PaletteItem) {
		r.app.ExecuteCommand(runtime.PopOverlay{})
		// Insert the command into input
		r.inputArea.InsertText(item.ID + " ")
	})

	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: palette,
		Modal:  true,
	})
}

// jumpToBottom scrolls to the bottom of the chat.
func (r *Runner) jumpToBottom() {
	if r.chatView != nil {
		r.chatView.ScrollToBottom()
	}
}

// toggleSidebar toggles the sidebar visibility.
func (r *Runner) toggleSidebar() {
	if r == nil {
		return
	}
	if r.state == nil || r.state.SidebarVisible == nil {
		return
	}
	visible := r.state.SidebarVisible.Get()
	r.state.SidebarVisible.Set(!visible)
}

// rebuildLayout rebuilds the widget tree layout.
func (r *Runner) rebuildLayout() {
	if r.app == nil {
		return
	}

	var focused runtime.Focusable
	if screen := r.app.Screen(); screen != nil {
		if scope := screen.BaseFocusScope(); scope != nil {
			if current := scope.Current(); current != nil {
				if f, ok := current.(runtime.Focusable); ok {
					focused = f
				}
			}
		}
	}

	root := r.buildLayout()
	r.app.SetRoot(root)

	if screen := r.app.Screen(); screen != nil {
		if scope := screen.BaseFocusScope(); scope != nil {
			if focused != nil && (scope.SetFocus(focused) || scope.Current() == focused) {
				return
			}
		}
	}
	r.ensureInputFocus()
}

// ensureInputFocus ensures the input area has focus.
func (r *Runner) ensureInputFocus() {
	if r == nil || r.app == nil || r.inputArea == nil {
		return
	}
	screen := r.app.Screen()
	if screen == nil {
		return
	}
	scope := screen.BaseFocusScope()
	if scope == nil {
		return
	}
	focusable, ok := interface{}(r.inputArea).(runtime.Focusable)
	if !ok || !focusable.CanFocus() {
		return
	}
	if scope.SetFocus(focusable) || scope.Current() == focusable {
		r.app.Invalidate()
	}
}

// setToastStyles configures the toast stack styles.
func (r *Runner) setToastStyles(bg, text, info, success, warn, err backend.Style) {
	if r.toastStack != nil {
		r.toastStack.SetStyles(bg, text, info, success, warn, err)
	}
}

func (r *Runner) initFocusIfNeeded(app *runtime.App) {
	if r == nil || r.focusInitialized {
		return
	}
	if app == nil || app.Screen() == nil {
		return
	}
	r.ensureInputFocus()
	r.focusInitialized = true
}

func (r *Runner) handleMouseFocus(app *runtime.App, mouse runtime.MouseMsg) {
	if r == nil || app == nil {
		return
	}
	if mouse.Action != runtime.MousePress || mouse.Button != runtime.MouseLeft {
		return
	}
	screen := app.Screen()
	if screen == nil {
		return
	}
	target := screen.WidgetAt(mouse.X, mouse.Y)
	if target == nil {
		return
	}
	scope, focusable := focusScopeForWidget(screen, target)
	if scope == nil || focusable == nil || !focusable.CanFocus() {
		return
	}
	if scope.SetFocus(focusable) || scope.Current() == focusable {
		app.Invalidate()
	}
}

func focusScopeForWidget(screen *runtime.Screen, target runtime.Widget) (*runtime.FocusScope, runtime.Focusable) {
	if screen == nil || target == nil {
		return nil, nil
	}
	for i := screen.LayerCount() - 1; i >= 0; i-- {
		layer := screen.Layer(i)
		if layer == nil || layer.Root == nil {
			continue
		}
		focusable, found := findFocusableInPath(layer.Root, target)
		if !found {
			continue
		}
		return layer.FocusScope, focusable
	}
	return screen.FocusScope(), nil
}

func findFocusableInPath(root, target runtime.Widget) (runtime.Focusable, bool) {
	if root == nil || target == nil {
		return nil, false
	}
	if root == target {
		if focusable, ok := root.(runtime.Focusable); ok {
			return focusable, true
		}
		return nil, true
	}
	if container, ok := root.(runtime.ChildProvider); ok {
		for _, child := range container.ChildWidgets() {
			if focusable, found := findFocusableInPath(child, target); found {
				if focusable != nil {
					return focusable, true
				}
				if parentFocusable, ok := root.(runtime.Focusable); ok {
					return parentFocusable, true
				}
				return nil, true
			}
		}
	}
	return nil, false
}

func scrollStatusText(top, total, viewHeight int) string {
	if total <= 0 || viewHeight <= 0 {
		return ""
	}
	if total <= viewHeight {
		return "All"
	}
	if top <= 0 {
		return "Top"
	}
	if top >= total-viewHeight {
		return "Bot"
	}
	denom := total - viewHeight
	if denom <= 0 {
		return "All"
	}
	pct := 100 * top / denom
	return strconv.Itoa(pct) + "%"
}
