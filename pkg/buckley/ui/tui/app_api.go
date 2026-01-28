// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"strings"
	"time"

	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/state"
	"github.com/odvcencio/fluffyui/toast"
)

// AddMessage adds a message. Thread-safe via message passing.
func (a *WidgetApp) AddMessage(content, source string) {
	if a == nil {
		return
	}
	a.Post(AddMessageMsg{Content: content, Source: source})
}

// RemoveThinkingIndicator removes the thinking indicator.
func (a *WidgetApp) RemoveThinkingIndicator() {
	if a == nil {
		return
	}
	a.Post(ThinkingMsg{Show: false})
}

// ShowThinkingIndicator shows the thinking indicator.
func (a *WidgetApp) ShowThinkingIndicator() {
	if a == nil {
		return
	}
	a.Post(ThinkingMsg{Show: true})
}

// AppendToLastMessage appends text. Thread-safe via message passing.
func (a *WidgetApp) AppendToLastMessage(text string) {
	if a == nil {
		return
	}
	a.Post(AppendMsg{Text: text})
}

// StreamChunk sends a streaming chunk through the coalescer.
func (a *WidgetApp) StreamChunk(sessionID, text string) {
	if a == nil {
		return
	}
	a.Post(StreamChunk{SessionID: sessionID, Text: text})
}

// StreamEnd signals the end of a streaming session.
func (a *WidgetApp) StreamEnd(sessionID, fullText string) {
	if a == nil {
		return
	}
	a.Post(StreamDone{SessionID: sessionID, FullText: fullText})
}

// AppendReasoning appends reasoning text to display with coalescing. Thread-safe.
func (a *WidgetApp) AppendReasoning(text string) {
	if a == nil {
		return
	}
	a.reasoningMu.Lock()
	a.reasoningBuffer.WriteString(text)
	bufLen := a.reasoningBuffer.Len()
	a.reasoningMu.Unlock()

	// Flush immediately if buffer is large (64 chars is good for reasoning)
	if bufLen >= 64 {
		a.flushReasoningBuffer()
	}
}

// CollapseReasoning collapses reasoning to preview. Thread-safe.
func (a *WidgetApp) CollapseReasoning(preview, full string) {
	if a == nil {
		return
	}
	a.Post(ReasoningEndMsg{Preview: preview, Full: full})
}

// SetStatus updates status. Thread-safe via message passing.
func (a *WidgetApp) SetStatus(text string) {
	if a == nil {
		return
	}
	a.Post(StatusMsg{Text: text})
}

// SetStatusOverride temporarily overrides the status bar text.
func (a *WidgetApp) SetStatusOverride(text string, duration time.Duration) {
	if a == nil {
		return
	}
	a.Post(StatusOverrideMsg{Text: text, Duration: duration})
}

// SetTokenCount updates token display. Thread-safe via message passing.
func (a *WidgetApp) SetTokenCount(tokens int, costCents float64) {
	if a == nil {
		return
	}
	a.Post(TokensMsg{Tokens: tokens, CostCent: costCents})
}

// SetContextUsage updates context usage display. Thread-safe via message passing.
func (a *WidgetApp) SetContextUsage(used, budget, window int) {
	if a == nil {
		return
	}
	a.Post(ContextMsg{Used: used, Budget: budget, Window: window})
}

// SetExecutionMode updates execution mode display. Thread-safe via message passing.
func (a *WidgetApp) SetExecutionMode(mode string) {
	if a == nil {
		return
	}
	a.Post(ExecutionModeMsg{Mode: mode})
}

// SetProgress updates progress indicators. Thread-safe via message passing.
func (a *WidgetApp) SetProgress(items []progress.Progress) {
	if a == nil {
		return
	}
	a.Post(ProgressMsg{Items: items})
}

// SetToasts updates toast notifications. Thread-safe via message passing.
func (a *WidgetApp) SetToasts(toasts []*toast.Toast) {
	if a == nil {
		return
	}
	a.Post(ToastsMsg{Toasts: toasts})
}

// StateScheduler returns the scheduler used for reactive state updates.
func (a *WidgetApp) StateScheduler() state.Scheduler {
	if a == nil {
		return nil
	}
	return a.stateScheduler
}

// PlaySFX schedules a sound effect cue.
func (a *WidgetApp) PlaySFX(cue string) {
	if a == nil || strings.TrimSpace(cue) == "" {
		return
	}
	a.Post(AudioSFXMsg{Cue: cue})
}

// Announce delivers an accessibility announcement.
func (a *WidgetApp) Announce(message string, priority accessibility.Priority) {
	if a == nil || a.announcer == nil {
		return
	}
	a.announcer.Announce(message, priority)
}

// SetStreaming updates streaming indicator state. Thread-safe via message passing.
func (a *WidgetApp) SetStreaming(active bool) {
	if a == nil {
		return
	}
	a.Post(StreamingMsg{Active: active})
}

// SetModelName updates model display. Thread-safe via message passing.
func (a *WidgetApp) SetModelName(name string) {
	if a == nil {
		return
	}
	a.Post(ModelMsg{Name: name})
}

// SetSessionID updates session display. Thread-safe via message passing.
func (a *WidgetApp) SetSessionID(id string) {
	if a == nil {
		return
	}
	a.Post(SessionMsg{ID: id})
}

// ClearScrollback clears all messages.
func (a *WidgetApp) ClearScrollback() {
	if a == nil {
		return
	}
	a.chatView.Clear()
	a.unreadCount = 0
	a.updateScrollStatus()
}

// HasInput returns true if there's text in the input.
func (a *WidgetApp) HasInput() bool {
	if a == nil {
		return false
	}
	return a.inputArea.HasText()
}

// ClearInput clears the input.
func (a *WidgetApp) ClearInput() {
	if a == nil {
		return
	}
	a.inputArea.Clear()
}

// WelcomeScreen displays a beautiful welcome screen.
func (a *WidgetApp) WelcomeScreen() {
	if a == nil {
		return
	}
	// ASCII art logo
	logo := []string{
		"",
		"   ╭──────────────────────────────────────╮",
		"   │                                      │",
		"   │   ●  B U C K L E Y                   │",
		"   │      AI Development Assistant        │",
		"   │                                      │",
		"   ╰──────────────────────────────────────╯",
		"",
	}

	for _, line := range logo {
		a.chatView.AddMessage(line, "system")
	}

	// Tips
	tips := []string{
		"  Quick tips:",
		"  • Type your question or task to get started",
		"  • Use @ to search and attach files",
		"  • Use ! to run shell commands",
		"  • Use $ to view environment variables",
		"  • Use /help to see available commands",
		"  • Use Ctrl+F to search conversation",
		"  • Use Alt+End to jump to latest",
		"  • Press Ctrl+C twice to quit",
		"",
	}

	for _, tip := range tips {
		a.chatView.AddMessage(tip, "system")
	}
}
