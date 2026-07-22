package tui

import (
	"time"

	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/terminal"
)

type keyDispatchResult struct {
	handled bool
	dirty   bool
}

// handleKeyMsg processes keyboard input.
func (a *WidgetApp) handleKeyMsg(m KeyMsg) bool {
	key := terminal.Key(m.Key)

	if result := a.handleConversationJumpKey(m, key); result.handled {
		return result.dirty
	}
	if result := a.handleCtrlCKey(key); result.handled {
		return result.dirty
	}
	if a.handleControlShortcut(m, key) {
		return true
	}
	if a.handleAltShortcut(m, key) {
		return true
	}

	return a.dispatchRuntimeKey(m, key)
}

func (a *WidgetApp) handleConversationJumpKey(m KeyMsg, key terminal.Key) keyDispatchResult {
	if !m.Alt {
		return keyDispatchResult{}
	}

	switch key {
	case terminal.KeyHome:
		a.chatView.ScrollToTop()
		a.updateScrollStatus()
		a.setStatusOverride("Top of conversation", 2*time.Second)
		return keyDispatchResult{handled: true, dirty: true}
	case terminal.KeyEnd:
		a.jumpToLatest()
		return keyDispatchResult{handled: true, dirty: true}
	default:
		return keyDispatchResult{}
	}
}

func (a *WidgetApp) handleCtrlCKey(key terminal.Key) keyDispatchResult {
	if key != terminal.KeyCtrlC {
		return keyDispatchResult{}
	}

	now := time.Now()
	if now.Before(a.ctrlCArmedUntil) {
		a.Quit()
		return keyDispatchResult{handled: true}
	}

	hadText := a.inputArea.HasText()
	a.inputArea.Clear()
	a.ctrlCArmedUntil = now.Add(3 * time.Second)
	if hadText {
		a.setStatusOverride("Input cleared · Press Ctrl+C again to quit", 3*time.Second)
	} else {
		a.setStatusOverride("Press Ctrl+C again to quit", 3*time.Second)
	}
	return keyDispatchResult{handled: true, dirty: true}
}

func (a *WidgetApp) handleControlShortcut(m KeyMsg, key terminal.Key) bool {
	switch {
	case key == terminal.KeyCtrlB || (m.Ctrl && m.Rune == 'b'):
		a.toggleSidebar()
	case key == terminal.KeyCtrlP || (m.Ctrl && m.Rune == 'p'):
		a.showCommandPalette()
	case key == terminal.KeyCtrlF || (m.Ctrl && m.Rune == 'f'):
		a.showSearchOverlay()
	default:
		return false
	}
	return true
}

func (a *WidgetApp) handleAltShortcut(m KeyMsg, key terminal.Key) bool {
	if !m.Alt {
		return false
	}

	if key == terminal.KeyRune && (m.Rune == 'c' || m.Rune == 'C') {
		a.copyLatestCodeBlock()
		return true
	}

	switch key {
	case terminal.KeyLeft:
		if a.onPrevSession != nil {
			a.onPrevSession()
		}
	case terminal.KeyRight:
		if a.onNextSession != nil {
			a.onNextSession()
		}
	default:
		return false
	}
	return true
}

func (a *WidgetApp) dispatchRuntimeKey(m KeyMsg, key terminal.Key) bool {
	runtimeMsg := runtimeKeyMsg(m, key)
	if a.handleFocusedInputNavigation(key, runtimeMsg) {
		return true
	}

	result := a.screen.HandleMessage(runtimeMsg)
	a.handleRuntimeCommands(result.Commands)
	return result.Handled
}

func runtimeKeyMsg(m KeyMsg, key terminal.Key) runtime.KeyMsg {
	return runtime.KeyMsg{
		Key:   key,
		Rune:  m.Rune,
		Alt:   m.Alt,
		Ctrl:  m.Ctrl,
		Shift: m.Shift,
	}
}

func (a *WidgetApp) handleFocusedInputNavigation(key terminal.Key, msg runtime.KeyMsg) bool {
	if !a.inputArea.IsFocused() || !isInputNavigationKey(key) {
		return false
	}

	result := a.inputArea.HandleMessage(msg)
	if !result.Handled {
		return false
	}

	a.handleRuntimeCommands(result.Commands)
	return true
}

func isInputNavigationKey(key terminal.Key) bool {
	switch key {
	case terminal.KeyUp, terminal.KeyDown, terminal.KeyHome, terminal.KeyEnd:
		return true
	default:
		return false
	}
}

func (a *WidgetApp) handleRuntimeCommands(commands []runtime.Command) {
	for _, cmd := range commands {
		a.handleCommand(cmd)
	}
}
