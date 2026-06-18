package tui

import (
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/ui/runtime"
)

// update processes a message and returns true if a render is needed.
func (a *WidgetApp) update(msg Message) bool {
	switch m := msg.(type) {
	case KeyMsg:
		return a.handleKeyMsg(m)
	case ResizeMsg:
		return a.handleResizeMsg(m)
	case StreamChunk:
		a.coalescer.Add(m.SessionID, m.Text)
		return false
	case StreamFlush:
		return a.handleStreamFlushMsg(m)
	case StreamDone:
		return a.handleStreamDoneMsg(m)
	case AddMessageMsg:
		return a.handleAddMessageMsg(m)
	case AppendMsg:
		return a.handleAppendMsg(m)
	case StatusMsg:
		return a.handleStatusMsg(m, time.Now())
	case ProcessStatusMsg:
		return a.handleProcessStatusMsg(m, time.Now())
	case TokensMsg:
		a.statusBar.SetTokens(m.Tokens, m.CostCent)
		return true
	case ModelMsg:
		a.header.SetModelName(m.Name)
		return true
	case ThinkingMsg:
		return a.handleThinkingMsg(m)
	case RefreshMsg:
		return true
	case QuitMsg:
		a.Quit()
		return false
	case ApprovalRequestMsg:
		a.showApprovalDialog(m)
		return true
	case MouseMsg:
		return a.handleMouseMsg(m)
	case PasteMsg:
		a.inputArea.InsertText(m.Text)
		return true
	default:
		return false
	}
}

func (a *WidgetApp) handleResizeMsg(m ResizeMsg) bool {
	a.applyInputHeightLimit(m.Height)
	a.inputMeasuredHeight = a.inputArea.Measure(runtime.Constraints{
		MaxWidth:  m.Width,
		MaxHeight: m.Height,
	}).Height
	a.screen.Resize(m.Width, m.Height)
	a.updateSidebarVisibility()
	return true
}

func (a *WidgetApp) handleStreamFlushMsg(m StreamFlush) bool {
	a.chatView.AppendText(m.Text)
	a.updateScrollStatus()
	return true
}

func (a *WidgetApp) handleStreamDoneMsg(m StreamDone) bool {
	a.coalescer.Flush(m.SessionID)
	a.coalescer.Clear(m.SessionID)
	return true
}

func (a *WidgetApp) handleAddMessageMsg(m AddMessageMsg) bool {
	wasFollowing := a.isFollowing()
	a.chatView.AddMessage(m.Content, m.Source)
	if !wasFollowing && m.Source != "thinking" {
		a.unreadCount++
	}
	a.updateScrollStatus()
	return true
}

func (a *WidgetApp) handleAppendMsg(m AppendMsg) bool {
	a.chatView.AppendText(m.Text)
	a.updateScrollStatus()
	return true
}

func (a *WidgetApp) handleStatusMsg(m StatusMsg, now time.Time) bool {
	a.statusText = m.Text
	if a.statusOverrideActive(now) {
		return false
	}
	a.clearStatusOverride()
	a.statusBar.SetStatus(a.currentStatusText(now))
	return true
}

func (a *WidgetApp) handleProcessStatusMsg(m ProcessStatusMsg, now time.Time) bool {
	if m.Active {
		return a.startProcessStatusMsg(m, now)
	}
	return a.stopProcessStatusMsg(now)
}

func (a *WidgetApp) startProcessStatusMsg(m ProcessStatusMsg, now time.Time) bool {
	text := strings.TrimSpace(m.Text)
	if text == "" {
		text = "Working"
	}
	if !a.processActive || m.ResetElapsed || a.processText != text {
		a.processStarted = now
		a.processLastTick = time.Time{}
		a.processFrame = 0
	}
	a.processActive = true
	a.processText = text

	if a.statusOverrideActive(now) {
		return false
	}
	a.clearStatusOverride()
	a.statusBar.SetStatus(a.formatProcessStatus(now))
	return true
}

func (a *WidgetApp) stopProcessStatusMsg(now time.Time) bool {
	if !a.processActive {
		return false
	}
	a.processActive = false
	a.processText = ""
	a.processStarted = time.Time{}
	a.processLastTick = time.Time{}
	a.processFrame = 0

	if a.statusOverrideActive(now) {
		return false
	}
	a.clearStatusOverride()
	a.statusBar.SetStatus(a.statusText)
	return true
}

func (a *WidgetApp) statusOverrideActive(now time.Time) bool {
	return a.statusOverride != "" && !now.After(a.statusOverrideUntil)
}

func (a *WidgetApp) clearStatusOverride() {
	a.statusOverride = ""
	a.statusOverrideUntil = time.Time{}
}

func (a *WidgetApp) handleThinkingMsg(m ThinkingMsg) bool {
	if m.Show {
		a.chatView.AddMessage("", "thinking")
	} else {
		a.chatView.RemoveThinkingIndicator()
	}
	a.updateScrollStatus()
	return true
}
