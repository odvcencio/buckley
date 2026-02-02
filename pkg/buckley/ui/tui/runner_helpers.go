// Package tui provides the integrated terminal user interface for Buckley.
// This file implements helper methods for the fluffyui-native Runner.

package tui

import (
	"strconv"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// showFilePicker displays the file picker overlay.
func (r *Runner) showFilePicker() {
	if r.filePicker == nil || r.app == nil {
		return
	}
	if r.filePicker.IsActive() {
		return
	}

	r.filePicker.Activate(0)
	widget := buckleywidgets.NewFilePickerWidget(r.filePicker)
	if r.theme != nil && r.styleCache != nil {
		widget.SetStyles(
			r.styleCache.Get(r.theme.SurfaceRaised),
			r.styleCache.Get(r.theme.Border),
			r.styleCache.Get(r.theme.TextPrimary),
			r.styleCache.Get(r.theme.Selection),
			r.styleCache.Get(r.theme.Accent),
			r.styleCache.Get(r.theme.TextPrimary),
		)
	}
	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: widget,
		Modal:  true,
	})
}

// showSearchOverlay displays the search overlay.
func (r *Runner) showSearchOverlay() {
	if r.app == nil {
		return
	}

	// Create search widget
	searchWidget := buckleywidgets.NewInteractiveSearch()
	updateMatches := func() {
		if r.chatView == nil {
			return
		}
		current, total := r.chatView.SearchMatches()
		searchWidget.SetMatchInfo(current, total)
	}
	searchWidget.SetOnSearch(func(query string) {
		if r.chatView != nil {
			r.chatView.Search(query)
		}
		updateMatches()
	})
	searchWidget.SetOnNavigate(func() {
		if r.chatView != nil {
			r.chatView.NextMatch()
		}
		updateMatches()
	}, func() {
		if r.chatView != nil {
			r.chatView.PrevMatch()
		}
		updateMatches()
	})
	searchWidget.SetOnClose(func() {
		if r.chatView != nil {
			r.chatView.ClearSearch()
		}
	})
	updateMatches()

	// Show as overlay
	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: searchWidget,
		Modal:  true,
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

	palette := buckleywidgets.NewInteractivePalette("Commands")
	palette.SetItems(items)
	palette.SetOnSelect(func(item uiwidgets.PaletteItem) {
		// Insert the command into input
		r.inputArea.InsertText(item.ID + " ")
	})

	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: palette,
		Modal:  true,
	})
}

// showCodePreview displays a modal code preview overlay.
func (r *Runner) showCodePreview(language, code string) {
	if r.app == nil {
		return
	}
	preview := buckleywidgets.NewCodePreview(language, code)
	if r.theme != nil && r.styleCache != nil {
		preview.SetStyles(
			r.styleCache.Get(r.theme.Border),
			r.styleCache.Get(r.theme.Surface),
			r.styleCache.Get(r.theme.TextMuted),
			r.styleCache.Get(r.theme.TextPrimary),
		)
	}
	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: preview,
		Modal:  true,
	})
}

// showApprovalOverlay displays a modal approval dialog.
func (r *Runner) showApprovalOverlay(request buckleywidgets.ApprovalRequest) {
	if r.app == nil {
		return
	}
	dialog := buckleywidgets.NewApprovalWidget(request)
	if r.theme != nil && r.styleCache != nil {
		dialog.SetStyles(
			r.styleCache.Get(r.theme.SurfaceRaised),
			r.styleCache.Get(r.theme.Border),
			r.styleCache.Get(r.theme.TextPrimary),
			r.styleCache.Get(r.theme.TextPrimary),
		)
	}
	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: dialog,
		Modal:  true,
	})
}

func (r *Runner) noteOverlayPush(modal bool) {
	if r == nil {
		return
	}
	r.overlayStack = append(r.overlayStack, modal)
	if !modal || r.bundle == nil || r.bundle.Keymaps == nil || r.overlayKeymap == nil {
		return
	}
	if r.overlayDepth == 0 {
		r.bundle.Keymaps.Push(r.overlayKeymap)
	}
	r.overlayDepth++
}

func (r *Runner) noteOverlayPop() {
	if r == nil || len(r.overlayStack) == 0 {
		return
	}
	modal := r.overlayStack[len(r.overlayStack)-1]
	r.overlayStack = r.overlayStack[:len(r.overlayStack)-1]
	if !modal || r.bundle == nil || r.bundle.Keymaps == nil {
		return
	}
	if r.overlayDepth == 0 {
		return
	}
	r.overlayDepth--
	if r.overlayDepth == 0 {
		r.bundle.Keymaps.Pop()
	}
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

func widgetInTree(root, target runtime.Widget) bool {
	if root == nil || target == nil {
		return false
	}
	if root == target {
		return true
	}
	if container, ok := root.(runtime.ChildProvider); ok {
		for _, child := range container.ChildWidgets() {
			if widgetInTree(child, target) {
				return true
			}
		}
	}
	return false
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
