// Package tui provides the integrated terminal user interface for Buckley.
// This file implements helper methods for the fluffyui-native Runner.

package tui

import (
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
	// Sidebar visibility is managed by the layout
	// For now, this is a no-op as the sidebar is always visible
	// when there's content to display
}

// rebuildLayout rebuilds the widget tree layout.
func (r *Runner) rebuildLayout() {
	if r.app == nil {
		return
	}

	// Get current root and rebuild
	root := r.buildLayout()
	r.app.SetRoot(root)
}

// ensureInputFocus ensures the input area has focus.
func (r *Runner) ensureInputFocus() {
	// Focus management is handled by the runtime
	// The input area is typically focused by default
}

// setToastStyles configures the toast stack styles.
func (r *Runner) setToastStyles(bg, text, info, success, warn, err backend.Style) {
	if r.toastStack != nil {
		r.toastStack.SetStyles(bg, text, info, success, warn, err)
	}
}
