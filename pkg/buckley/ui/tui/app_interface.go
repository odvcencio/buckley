// Package tui provides the integrated terminal user interface for Buckley.
// This file defines the App interface that both WidgetApp and Runner implement.

package tui

import (
	"time"

	"github.com/odvcencio/buckley/pkg/diagnostics"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/toast"
	"github.com/odvcencio/fluffyui/widgets"
)

// App is the interface for the TUI application.
// Both WidgetApp (legacy) and Runner (fluffyui-native) implement this interface.
type App interface {
	// Lifecycle
	Run() error
	Quit()

	// Content
	AddMessage(content, source string)
	AppendToLastMessage(text string)
	ClearScrollback()
	WelcomeScreen()

	// Streaming
	StreamChunk(sessionID, text string)
	StreamEnd(sessionID, fullText string)

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
	ShowModelPicker(items []widgets.PaletteItem, onSelect func(item widgets.PaletteItem))

	// Callbacks
	SetCallbacks(onSubmit func(string), onFileSelect func(string), onShellCmd func(string) string)
	SetSessionCallbacks(onNext, onPrev func())

	// Progress and Toasts
	SetProgress(items []progress.Progress)
	SetToasts(toasts []*toast.Toast)
	SetToastDismissHandler(onDismiss func(string))

	// Diagnostics
	SetDiagnostics(collector *diagnostics.Collector)
}
