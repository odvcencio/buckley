// Package tui provides the integrated terminal user interface for Buckley.
// This file implements helper methods for the fluffyui-native Runner.

package tui

import (
	"fmt"
	"strconv"
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/state"
	"github.com/odvcencio/fluffyui/terminal"
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
	overlay := r.wrapModalOverlay(widget, func() {
		r.filePicker.Deactivate()
	})
	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: overlay,
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
	var subs state.Subscriptions
	if r.app != nil {
		subs.SetScheduler(r.app.Services().Scheduler())
	}
	updateMatches := func() {
		if r.state != nil && r.state.ChatSearchMatches != nil {
			info := r.state.ChatSearchMatches.Get()
			searchWidget.SetMatchInfo(info.Current, info.Total)
			return
		}
		if r.chatView != nil {
			current, total := r.chatView.SearchMatches()
			searchWidget.SetMatchInfo(current, total)
		}
	}
	if r.state != nil && r.state.ChatSearchMatches != nil {
		subs.Observe(r.state.ChatSearchMatches, updateMatches)
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
		subs.Clear()
	})
	if r.state != nil && r.state.ChatSearchQuery != nil {
		searchWidget.SetQuery(r.state.ChatSearchQuery.Get())
	}
	updateMatches()

	// Show as overlay
	overlay := r.wrapModalOverlay(searchWidget, func() {
		if r.chatView != nil {
			r.chatView.ClearSearch()
		}
		subs.Clear()
	})
	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: overlay,
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
		{ID: "/settings", Label: "/settings", Description: "Edit UI settings"},
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

	overlay := r.wrapModalOverlay(palette, nil)
	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: overlay,
		Modal:  true,
	})
}

// showSettingsOverlay displays the settings dialog.
func (r *Runner) showSettingsOverlay() {
	if r.app == nil || r.state == nil {
		return
	}
	values := buckleywidgets.SettingsValues{}
	if r.state.ThemeName != nil {
		values.Theme = r.state.ThemeName.Get()
	}
	if r.state.StylesheetPath != nil {
		values.StylesheetPath = r.state.StylesheetPath.Get()
	}
	if r.state.MessageMetadata != nil {
		values.MessageMetadata = r.state.MessageMetadata.Get()
	}
	if r.state.HighContrast != nil {
		values.HighContrast = r.state.HighContrast.Get()
	}
	if r.state.ReduceMotion != nil {
		values.ReduceMotion = r.state.ReduceMotion.Get()
	}
	if r.state.EffectsEnabled != nil {
		values.EffectsEnabled = r.state.EffectsEnabled.Get()
	}
	dialog := buckleywidgets.NewSettingsDialog(buckleywidgets.SettingsDialogConfig{
		Values: values,
		OnSubmit: func(updated buckleywidgets.SettingsValues) {
			r.applySettings(UISettings{
				ThemeName:       updated.Theme,
				StylesheetPath:  updated.StylesheetPath,
				MessageMetadata: updated.MessageMetadata,
				HighContrast:    updated.HighContrast,
				ReduceMotion:    updated.ReduceMotion,
				EffectsEnabled:  updated.EffectsEnabled,
			})
			if r.statusService != nil {
				r.statusService.SetStatusOverride("Settings saved", 2*time.Second)
			}
		},
	})
	if r.theme != nil && r.styleCache != nil {
		dialog.SetStyles(
			r.styleCache.Get(r.theme.SurfaceRaised),
			r.styleCache.Get(r.theme.Border),
			r.styleCache.Get(r.theme.TextPrimary),
			r.styleCache.Get(r.theme.TextPrimary),
			r.styleCache.Get(r.theme.Selection),
			r.styleCache.Get(r.theme.TextMuted),
			r.styleCache.Get(r.theme.Error),
		)
	}
	overlay := r.wrapModalOverlay(buckleywidgets.NewCenteredOverlay(dialog), nil)
	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: overlay,
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
	overlay := r.wrapModalOverlay(preview, nil)
	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: overlay,
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
	overlay := r.wrapModalOverlay(dialog, nil)
	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: overlay,
		Modal:  true,
	})
}

func (r *Runner) wrapModalOverlay(child runtime.Widget, onClose func()) runtime.Widget {
	overlay := buckleywidgets.NewModalOverlay(child)
	if r.theme != nil && r.styleCache != nil {
		overlay.SetBackdropStyle(r.styleCache.Get(r.theme.SurfaceDim))
	}
	overlay.SetOnClose(onClose)
	overlay.SetCloseOnBackdrop(true)
	return overlay
}

func (r *Runner) syncOverlayKeymap(screen *runtime.Screen) {
	if r == nil || screen == nil || r.bundle == nil || r.bundle.Keymaps == nil || r.overlayKeymap == nil {
		return
	}
	hasModal := false
	for i := screen.LayerCount() - 1; i >= 0; i-- {
		layer := screen.Layer(i)
		if layer == nil {
			continue
		}
		if layer.Modal {
			hasModal = true
			break
		}
	}
	if hasModal && !r.overlayKeymapActive {
		r.bundle.Keymaps.Push(r.overlayKeymap)
		r.overlayKeymapActive = true
		return
	}
	if !hasModal && r.overlayKeymapActive {
		if r.bundle.Keymaps.Current() == r.overlayKeymap {
			r.bundle.Keymaps.Pop()
			r.overlayKeymapActive = false
		} else {
			// Keymap is buried under another push or was already removed.
			// Check if it's still in the stack before clearing the flag.
			found := false
			for _, km := range r.bundle.Keymaps.All() {
				if km == r.overlayKeymap {
					found = true
					break
				}
			}
			if !found {
				r.overlayKeymapActive = false
			}
		}
	}
}

func (r *Runner) focusTopLayer(screen *runtime.Screen) {
	if r == nil || screen == nil {
		return
	}
	scope := screen.FocusScope()
	if scope == nil || scope.Current() != nil {
		return
	}
	if scope.FocusFirst() && r.app != nil {
		r.app.Invalidate()
	}
}

func (r *Runner) overlayCount(screen *runtime.Screen) int {
	if screen == nil {
		return 0
	}
	return screen.OverlayCount()
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

// bindWidgets binds all widgets to app services so they can request redraws.
func (r *Runner) bindWidgets() {
	if r == nil || r.app == nil {
		return
	}
	services := r.app.Services()
	if r.chatView != nil {
		r.chatView.Bind(services)
	}
	if r.sidebar != nil {
		r.sidebar.Bind(services)
	}
	if r.header != nil {
		r.header.Bind(services)
	}
	if r.inputArea != nil {
		r.inputArea.Bind(services)
	}
	if r.statusBar != nil {
		r.statusBar.Bind(services)
	}
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
	r.trySetInputFocus()
}

// onAppReady is called once during Run, after the Screen is initialized.
func (r *Runner) onAppReady(app *runtime.App) {
	if r == nil || app == nil {
		return
	}
	r.trySetInputFocus()
	r.focusInitialized = true
}

func (r *Runner) initFocusIfNeeded(app *runtime.App) {
	if r == nil || r.focusInitialized {
		return
	}
	if app == nil || app.Screen() == nil {
		return
	}
	// Only mark initialized if focus was successfully set to inputArea
	if r.trySetInputFocus() {
		r.focusInitialized = true
	}
}

// trySetInputFocus attempts to focus the input area and returns true if successful.
func (r *Runner) trySetInputFocus() bool {
	if r == nil || r.app == nil || r.inputArea == nil {
		return false
	}
	screen := r.app.Screen()
	if screen == nil {
		return false
	}
	scope := screen.BaseFocusScope()
	if scope == nil {
		return false
	}
	focusable, ok := interface{}(r.inputArea).(runtime.Focusable)
	if !ok || !focusable.CanFocus() {
		return false
	}
	// If inputArea is already focused, we're done
	current := scope.Current()
	if current == focusable {
		return true
	}
	// Try to set focus
	if scope.SetFocus(focusable) {
		r.app.Invalidate()
		return true
	}
	// SetFocus failed - inputArea might not be registered yet
	// This can happen if focus registration hasn't completed
	return false
}

func (r *Runner) maybeCaptureInput(app *runtime.App, key runtime.KeyMsg) {
	if r == nil || r.inputArea == nil || app == nil {
		return
	}
	if !isTypingKey(key) {
		return
	}
	screen := app.Screen()
	if screen == nil {
		return
	}
	if top := screen.TopLayer(); top != nil && top.Modal && screen.LayerCount() > 1 {
		return
	}
	scope := screen.BaseFocusScope()
	if scope == nil {
		return
	}
	current := scope.Current()
	if current == r.inputArea {
		return
	}
	if current != nil && current != r.chatView {
		return
	}
	if r.trySetInputFocus() {
		r.focusInitialized = true
		return
	}
	r.inputArea.Focus()
	if r.app != nil {
		r.app.Invalidate()
	}
}

func isTypingKey(key runtime.KeyMsg) bool {
	switch key.Key {
	case terminal.KeyRune:
		return key.Rune != 0
	case terminal.KeyBackspace, terminal.KeyDelete:
		return true
	default:
		return false
	}
}

// DebugFocusState logs the current focus state for debugging.
func (r *Runner) DebugFocusState() string {
	if r == nil || r.app == nil {
		return "runner or app is nil"
	}
	screen := r.app.Screen()
	if screen == nil {
		return "screen is nil"
	}
	scope := screen.BaseFocusScope()
	if scope == nil {
		return "scope is nil"
	}
	current := scope.Current()
	inputFocused := r.inputArea != nil && r.inputArea.IsFocused()
	return "scope.Count=" + strconv.Itoa(scope.Count()) +
		", current=" + widgetTypeName(current) +
		", inputArea.IsFocused=" + strconv.FormatBool(inputFocused) +
		", focusInitialized=" + strconv.FormatBool(r.focusInitialized)
}

func widgetTypeName(w interface{}) string {
	if w == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T", w)
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
	if top := screen.TopLayer(); top != nil && top.Modal && top.Root != nil {
		if !widgetInTree(top.Root, target) {
			return
		}
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
