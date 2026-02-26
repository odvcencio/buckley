// Package tui provides the integrated terminal user interface for Buckley.
// This file defines the App interface that Runner implements.

package tui

import (
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/buckley/pkg/diagnostics"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/state"
	"github.com/odvcencio/fluffyui/toast"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// SidebarSignals exposes writable sidebar state signals.
type SidebarSignals struct {
	CurrentTask        state.Writable[string]
	TaskProgress       state.Writable[int]
	PlanTasks          state.Writable[[]buckleywidgets.PlanTask]
	RunningTools       state.Writable[[]buckleywidgets.RunningTool]
	ToolHistory        state.Writable[[]buckleywidgets.ToolHistoryEntry]
	ActiveTouches      state.Writable[[]buckleywidgets.TouchSummary]
	RecentFiles        state.Writable[[]string]
	RLMStatus          state.Writable[*buckleywidgets.RLMStatus]
	RLMScratchpad      state.Writable[[]buckleywidgets.RLMScratchpadEntry]
	CircuitStatus      state.Writable[*buckleywidgets.CircuitStatus]
	Experiment         state.Writable[string]
	ExperimentStatus   state.Writable[string]
	ExperimentVariants state.Writable[[]buckleywidgets.ExperimentVariant]
}

// App is the interface for the TUI application.
type App interface {
	// Lifecycle
	Run() error
	Quit()

	// Content
	AddMessage(content, source string)
	AppendToLastMessage(text string)
	SetChatMessages(messages []buckleywidgets.ChatMessage)

	// Streaming
	StreamChunk(sessionID, text string)
	StreamEnd(sessionID, fullText string)
	IsStreaming() bool

	// Status
	SetStatus(text string)
	SetStatusOverride(text string, duration time.Duration)
	SetStreaming(active bool)

	// Metadata
	SetModelName(name string)
	SetSessionID(id string)
	SetExecutionMode(mode string)
	SetTokenCount(tokens int, costCents float64)
	SetContextUsage(used, budget, window int)

	// Thinking/Reasoning
	ShowThinkingIndicator()
	RemoveThinkingIndicator()
	AppendReasoning(text string)
	CollapseReasoning(preview, full string)

	// Overlays
	ShowModelPicker(items []uiwidgets.PaletteItem, onSelect func(item uiwidgets.PaletteItem))
	ShowApproval(request buckleywidgets.ApprovalRequest)
	ShowSettings()

	// Callbacks
	SetCallbacks(onSubmit func(string), onFileSelect func(string), onShellCmd func(string) string)
	SetSessionCallbacks(onNext, onPrev func())

	// Progress and Toasts
	SetProgress(items []progress.Progress)
	SetToasts(toasts []*toast.Toast)
	SetToastDismissHandler(onDismiss func(string))

	// Sidebar signals
	SidebarSignals() SidebarSignals

	// Diagnostics
	SetDiagnostics(collector *diagnostics.Collector)
}
