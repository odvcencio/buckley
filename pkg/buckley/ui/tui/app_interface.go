// Package tui provides the integrated terminal user interface for Buckley.
// This file defines the App interface that Runner implements.

package tui

import (
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/buckley/pkg/diagnostics"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/toast"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

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

	// Callbacks
	SetCallbacks(onSubmit func(string), onFileSelect func(string), onShellCmd func(string) string)
	SetSessionCallbacks(onNext, onPrev func())

	// Progress and Toasts
	SetProgress(items []progress.Progress)
	SetSidebarState(state buckleywidgets.SidebarState)
	SetToasts(toasts []*toast.Toast)
	SetToastDismissHandler(onDismiss func(string))

	// Diagnostics
	SetDiagnostics(collector *diagnostics.Collector)
}

// LayoutSpec defines the layout configuration for the TUI.
type LayoutSpec struct {
	SidebarVisible  bool
	PresenceVisible bool
	SidebarWidth    int
	ShowHeader      bool
	ShowStatus      bool
}

// RenderMetrics tracks rendering performance statistics.
type RenderMetrics struct {
	FrameCount      int64
	DroppedFrames   int64
	TotalRenderTime time.Duration
	LastFrameTime   time.Duration
}
