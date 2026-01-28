// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"strings"
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/keybind"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/scroll"
	"github.com/odvcencio/fluffyui/terminal"
	"github.com/odvcencio/fluffyui/toast"
	"github.com/odvcencio/fluffyui/widgets"
)

// ============================================================================
// FILE: app_input.go
// PURPOSE: Keyboard and mouse input handling, keybindings
// FUNCTIONS:
//   - handleKeyMsg
//   - handleMouseMsg
//   - handleKeybind
//   - keyContext
//   - scrollPage
//   - handleWebClick
//   - dispatchRuntimeMouse
// ============================================================================

// handleKeyMsg processes keyboard input.
func (a *WidgetApp) handleKeyMsg(m KeyMsg) bool {
	key := terminal.Key(m.Key)

	// Ctrl+C: clear input first, second press within 3s exits
	if key == terminal.KeyCtrlC {
		now := time.Now()
		if now.Before(a.ctrlCArmedUntil) {
			a.Quit()
			return false
		}
		hadText := a.inputArea.HasText()
		a.inputArea.Clear()
		a.ctrlCArmedUntil = now.Add(3 * time.Second)
		if hadText {
			a.setStatusOverride("Input cleared · Press Ctrl+C again to quit", 3*time.Second)
		} else {
			a.setStatusOverride("Press Ctrl+C again to quit", 3*time.Second)
		}
		return true
	}
	if key == terminal.KeyEscape && a.contextMenuActive() {
		a.dismissContextMenu()
		return true
	}

	if a.handleKeybind(m) {
		return true
	}

	// Convert to runtime.KeyMsg and dispatch through screen
	runtimeMsg := runtime.KeyMsg{
		Key:   key,
		Rune:  m.Rune,
		Alt:   m.Alt,
		Ctrl:  m.Ctrl,
		Shift: m.Shift,
	}

	// Let fluffyui's screen handle the key event.
	// The screen routes to the focused widget in the active layer.
	// Modal overlays (when push with 'true') take precedence automatically.
	result := a.screen.HandleMessage(runtimeMsg)

	// Process commands that bubble up from widgets
	for _, cmd := range result.Commands {
		a.handleCommand(cmd)
	}

	return result.Handled
}

// handleMouseMsg processes mouse input.
func (a *WidgetApp) handleMouseMsg(m MouseMsg) bool {
	if a.contextMenuActive() && m.Action == MousePress && (m.Button == MouseLeft || m.Button == MouseRight) {
		if a.contextMenuPanel != nil && a.contextMenuPanel.Bounds().Contains(m.X, m.Y) {
			return a.dispatchRuntimeMouse(m)
		}
		a.dismissContextMenu()
		return true
	}

	if !a.sidebarVisible && a.presenceVisible && a.presence != nil {
		if a.presence.Bounds().Contains(m.X, m.Y) {
			if m.Action == MousePress && m.Button == MouseLeft {
				a.toggleSidebar()
				return true
			}
		}
	}

	if m.Action == MousePress && m.Button == MouseLeft {
		if a.handleWebClick(m) {
			return true
		}
	}

	if m.Action == MousePress && m.Button == MouseRight {
		if a.showContextMenu(m.X, m.Y) {
			return true
		}
	}

	if a.sidebarVisible && a.sidebar != nil {
		if a.sidebar.Bounds().Contains(m.X, m.Y) {
			return a.dispatchRuntimeMouse(m)
		}
	}

	// Handle scroll wheel on the chat view
	switch m.Button {
	case MouseWheelUp:
		a.chatView.ScrollUp(3)
		return true
	case MouseWheelDown:
		a.chatView.ScrollDown(3)
		return true
	}

	if m.Action == MouseRelease && a.selectionActive {
		line, col, ok := a.chatView.PositionForPoint(m.X, m.Y)
		if ok {
			a.chatView.UpdateSelection(line, col)
			a.selectionLastLine = line
			a.selectionLastCol = col
			a.selectionLastValid = true
		} else if a.selectionLastValid {
			a.chatView.UpdateSelection(a.selectionLastLine, a.selectionLastCol)
		}

		a.chatView.EndSelection()
		a.selectionActive = false
		a.selectionLastValid = false
		if a.chatView.HasSelection() {
			text := a.chatView.SelectionText()
			if text == "" {
				a.chatView.ClearSelection()
			} else if err := a.writeClipboard(text); err != nil {
				a.setStatusOverride("Copy failed: "+err.Error(), 3*time.Second)
			} else {
				a.setStatusOverride("Selection copied", 2*time.Second)
			}
		}
		a.dirty = true
		return true
	}

	if m.Action == MousePress && m.Button == MouseLeft {
		if a.chatView != nil && a.chatView.ReasoningContains(m.X, m.Y) {
			return a.dispatchRuntimeMouse(m)
		}
		line, col, ok := a.chatView.PositionForPoint(m.X, m.Y)
		if ok {
			// Otherwise start selection
			if !a.selectionActive {
				a.chatView.ClearSelection()
				a.chatView.StartSelection(line, col)
				a.selectionActive = true
			} else {
				a.chatView.UpdateSelection(line, col)
			}
			a.selectionLastLine = line
			a.selectionLastCol = col
			a.selectionLastValid = true
			a.dirty = true
			return true
		}
	}

	return a.dispatchRuntimeMouse(m)
}

func (a *WidgetApp) handleKeybind(m KeyMsg) bool {
	if a.keyRouter == nil {
		return false
	}
	msg := runtime.KeyMsg{
		Key:   terminal.Key(m.Key),
		Rune:  m.Rune,
		Alt:   m.Alt,
		Ctrl:  m.Ctrl,
		Shift: m.Shift,
	}
	return a.keyRouter.HandleKey(msg, a.keyContext())
}

func (a *WidgetApp) keyContext() keybind.Context {
	var focused runtime.Widget
	if a.screen != nil {
		if scope := a.screen.FocusScope(); scope != nil {
			if current := scope.Current(); current != nil {
				focused = current
			}
		}
	}
	ctx := keybind.Context{Focused: focused}
	if accessible, ok := focused.(accessibility.Accessible); ok {
		ctx.FocusedWidget = accessible
	}
	if handler, ok := focused.(keybind.Handler); ok {
		ctx.Keymap = handler.Keymap()
	}
	return ctx
}

func (a *WidgetApp) scrollPage(ctx keybind.Context, pages int) {
	if pages == 0 {
		return
	}
	if ctx.Focused != nil {
		if controller, ok := ctx.Focused.(scroll.Controller); ok {
			controller.PageBy(pages)
			a.updateScrollStatus()
			a.dirty = true
			return
		}
	}
	if a.chatView == nil {
		return
	}
	if pages < 0 {
		a.chatView.PageUp()
	} else {
		a.chatView.PageDown()
	}
	a.updateScrollStatus()
	a.dirty = true
}

func (a *WidgetApp) handleWebClick(m MouseMsg) bool {
	if a.toastStack != nil {
		if entry, ok := a.toastStack.ToastAt(m.X, m.Y); ok {
			if entry.Level == toast.ToastWarning || entry.Level == toast.ToastError {
				handled := a.openWebTarget("errors", m.Shift)
				if handled && a.onToastDismiss != nil {
					a.onToastDismiss(entry.ID)
				}
				return handled
			}
		}
	}

	if a.headerVisible && a.header != nil {
		if target, ok := a.header.WebLinkAt(m.X, m.Y); ok {
			if target == "model" {
				return a.openWebTarget("model", m.Shift)
			}
			return a.openWebTarget("session", m.Shift)
		}
	}

	if a.statusVisible && a.statusBar != nil {
		if target, ok := a.statusBar.WebLinkAt(m.X, m.Y); ok {
			return a.openWebTarget(target, m.Shift)
		}
	}

	if a.sidebarVisible && a.sidebar != nil {
		if target, ok := a.sidebar.WebLinkAt(m.X, m.Y); ok {
			return a.openWebTarget(target, m.Shift)
		}
	}

	if a.chatView != nil {
		if action, language, code, ok := a.chatView.CodeHeaderActionAtPoint(m.X, m.Y); ok {
			switch action {
			case "copy":
				if err := a.writeClipboard(code); err != nil {
					a.setStatusOverride("Copy failed: "+err.Error(), 3*time.Second)
				} else {
					if language == "" {
						a.setStatusOverride("Copied code block", 2*time.Second)
					} else {
						a.setStatusOverride("Copied "+language+" code block", 2*time.Second)
					}
				}
				return true
			case "open":
				return a.openWebTarget("code", m.Shift)
			}
		}
	}

	return false
}

func (a *WidgetApp) dispatchRuntimeMouse(m MouseMsg) bool {
	runtimeMsg := runtime.MouseMsg{
		X:      m.X,
		Y:      m.Y,
		Button: runtime.MouseButton(m.Button),
		Action: runtime.MouseAction(m.Action),
		Alt:    m.Alt,
		Ctrl:   m.Ctrl,
		Shift:  m.Shift,
	}

	result := a.screen.HandleMessage(runtimeMsg)
	for _, cmd := range result.Commands {
		a.handleCommand(cmd)
	}

	return result.Handled
}

// SetCallbacks sets the event handlers.
func (a *WidgetApp) SetCallbacks(onSubmit func(string), onFileSelect func(string), onShellCmd func(string) string) {
	if a == nil {
		return
	}
	a.onSubmit = onSubmit
	a.onFileSelect = onFileSelect
	a.onShellCmd = onShellCmd
}

// SetSessionCallbacks sets the session navigation callbacks.
func (a *WidgetApp) SetSessionCallbacks(onNext, onPrev func()) {
	if a == nil {
		return
	}
	a.onNextSession = onNext
	a.onPrevSession = onPrev
}

// SetApprovalCallback sets the callback for tool approval decisions.
func (a *WidgetApp) SetApprovalCallback(onApproval func(requestID string, approved, alwaysAllow bool)) {
	if a == nil {
		return
	}
	a.onApproval = onApproval
}

// SetToastDismissHandler sets the handler to dismiss toasts from the UI.
func (a *WidgetApp) SetToastDismissHandler(onDismiss func(string)) {
	if a == nil {
		return
	}
	if a.toastStack == nil {
		return
	}
	a.onToastDismiss = onDismiss
	a.toastStack.SetOnDismiss(onDismiss)
}

// RequestApproval displays an approval dialog for a tool operation.
// The callback set via SetApprovalCallback will be called with the decision.
func (a *WidgetApp) RequestApproval(req ApprovalRequestMsg) {
	if a == nil {
		return
	}
	a.Post(req)
}

// ShowModelPicker displays a model picker overlay.
func (a *WidgetApp) ShowModelPicker(items []widgets.PaletteItem, onSelect func(item widgets.PaletteItem)) {
	if a == nil || a.screen == nil || len(items) == 0 {
		return
	}

	options := make([]widgets.SelectOption, 0, len(items))
	for _, item := range items {
		label := item.Label
		if strings.TrimSpace(label) == "" {
			label = item.ID
		}
		options = append(options, widgets.SelectOption{Label: label, Value: item})
	}
	selector := widgets.NewSelect(options...)
	selector.SetLabel("Models")
	selector.SetStyle(a.style(a.theme.Surface))
	selector.SetFocusStyle(a.style(a.theme.Selection))

	description := buckleywidgets.NewTextBlock("")
	description.SetStyle(a.style(a.theme.TextSecondary))

	updateDescription := func(opt widgets.SelectOption) {
		item, ok := opt.Value.(widgets.PaletteItem)
		if !ok {
			description.SetText("")
			return
		}
		description.SetText(item.Description)
	}
	if opt, ok := selector.SelectedOption(); ok {
		updateDescription(opt)
	}
	selector.SetOnChange(func(opt widgets.SelectOption) {
		updateDescription(opt)
	})

	hint := widgets.NewLabel("Use ←/→ to browse · Enter to select")
	hint.SetStyle(a.style(a.theme.TextMuted))

	content := runtime.VBox(
		runtime.Fixed(selector),
		runtime.Flexible(description, 1),
		runtime.Fixed(hint),
	).WithGap(1)

	dialog := widgets.NewDialog("Select Model", "", widgets.DialogButton{
		Label: "Select",
		Key:   'S',
		OnClick: func() {
			if opt, ok := selector.SelectedOption(); ok {
				if item, ok := opt.Value.(widgets.PaletteItem); ok && onSelect != nil {
					onSelect(item)
				}
			}
			a.screen.PopLayer()
			a.dirty = true
		},
	}, widgets.DialogButton{
		Label: "Cancel",
		Key:   'C',
		OnClick: func() {
			a.screen.PopLayer()
			a.dirty = true
		},
	}).WithContent(content).OnDismiss(func() {
		a.screen.PopLayer()
		a.dirty = true
	})
	dialog.SetStyle(a.style(a.theme.SurfaceRaised))

	overlay := buckleywidgets.NewCenteredOverlay(dialog)
	selector.Focus()
	a.screen.PushLayer(overlay, true)
	a.dirty = true
}
