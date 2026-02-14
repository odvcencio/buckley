package tui

import (
	"strings"
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/buckley/pkg/diagnostics"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/toast"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// =============================================================================
// Public API methods (matching App interface)
// =============================================================================

// SetStatus updates the status bar text.
func (r *Runner) SetStatus(text string) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetStatus(text)
}

// AddMessage adds a message to the chat view.
func (r *Runner) AddMessage(content, source string) {
	if r == nil || r.chatService == nil {
		return
	}
	r.chatService.AddMessage(content, source)
}

// SetChatMessages replaces the chat history.
func (r *Runner) SetChatMessages(messages []buckleywidgets.ChatMessage) {
	if r == nil || r.chatService == nil {
		return
	}
	r.chatService.SetMessages(messages)
}

// StreamChunk sends streaming text chunk.
func (r *Runner) StreamChunk(sessionID, text string) {
	if r == nil || r.coalescer == nil {
		return
	}
	r.coalescer.Add(sessionID, text)
}

// StreamEnd signals end of streaming.
func (r *Runner) StreamEnd(sessionID, fullText string) {
	if r == nil || r.coalescer == nil {
		return
	}
	// Flush all pending content to ensure nothing is lost on session switch.
	r.coalescer.FlushAll()
	r.coalescer.Clear(sessionID)
}

// AppendToLastMessage appends text to the last message.
func (r *Runner) AppendToLastMessage(text string) {
	if r == nil || r.chatService == nil {
		return
	}
	r.chatService.AppendToLastMessage(text)
}

// AppendReasoning appends reasoning content.
func (r *Runner) AppendReasoning(text string) {
	if r == nil || r.chatService == nil {
		return
	}
	r.chatService.AppendReasoning(text)
}

// CollapseReasoning collapses the reasoning block with a preview.
func (r *Runner) CollapseReasoning(preview, full string) {
	if r == nil || r.chatService == nil {
		return
	}
	r.chatService.CollapseReasoning(preview, full)
}

// SetModel updates the displayed model name.
func (r *Runner) SetModel(name string) {
	r.SetModelName(name)
}

// SetSession updates the displayed session ID.
func (r *Runner) SetSession(id string) {
	r.SetSessionID(id)
}

// SetStreaming updates the streaming indicator.
func (r *Runner) SetStreaming(active bool) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetStreaming(active)
}

// IsStreaming returns true if currently streaming a response.
func (r *Runner) IsStreaming() bool {
	if r == nil || r.state == nil {
		return false
	}
	return r.state.IsStreaming.Get()
}

// SetContextUsage updates context usage display.
func (r *Runner) SetContextUsage(used, budget, window int) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetContextUsage(used, budget, window)
}

// Quit stops the application.
func (r *Runner) Quit() {
	if r == nil || r.app == nil {
		return
	}
	if r.onQuit != nil {
		r.onQuit()
	}
	r.app.ExecuteCommand(runtime.Quit{})
}

// =============================================================================
// Additional API methods required by Controller
// =============================================================================

// SetModelName updates the displayed model name.
func (r *Runner) SetModelName(name string) {
	if r == nil || r.state == nil {
		return
	}
	name = strings.TrimSpace(name)
	r.state.ModelName.Set(name)
}

// SetSessionID updates the displayed session ID.
func (r *Runner) SetSessionID(id string) {
	if r == nil || r.state == nil {
		return
	}
	id = strings.TrimSpace(id)
	r.state.SessionID.Set(id)
}

// SetStatusOverride temporarily overrides the status bar text.
func (r *Runner) SetStatusOverride(text string, duration time.Duration) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetStatusOverride(text, duration)
}

// SetTokenCount updates the token count display.
func (r *Runner) SetTokenCount(tokens int, costCents float64) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetTokenCount(tokens, costCents)
}

// SetExecutionMode updates the execution mode display.
func (r *Runner) SetExecutionMode(mode string) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetMode(mode)
}

// ShowThinkingIndicator shows the thinking indicator.
func (r *Runner) ShowThinkingIndicator() {
	if r == nil || r.chatService == nil {
		return
	}
	r.chatService.ShowThinkingIndicator()
}

// RemoveThinkingIndicator hides the thinking indicator.
func (r *Runner) RemoveThinkingIndicator() {
	if r == nil || r.chatService == nil {
		return
	}
	r.chatService.RemoveThinkingIndicator()
}

// ShowModelPicker displays the model picker palette.
func (r *Runner) ShowModelPicker(items []uiwidgets.PaletteItem, onSelect func(item uiwidgets.PaletteItem)) {
	if r.app == nil || r.modelPalette == nil || len(items) == 0 {
		return
	}

	// Reset query and selection state for a fresh view
	r.modelPalette.Reset()

	// Set the items (also resets filtered list)
	r.modelPalette.SetItems(items)

	// Set the callback that dismisses the palette after selection
	r.modelPalette.SetOnSelect(func(item uiwidgets.PaletteItem) {
		// Then call the user's callback
		if onSelect != nil {
			onSelect(item)
		}
	})

	// Show as overlay via command
	overlay := r.wrapModalOverlay(r.modelPalette, nil)
	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: overlay,
		Modal:  true,
	})
}

// ShowApproval displays a modal approval dialog.
func (r *Runner) ShowApproval(request buckleywidgets.ApprovalRequest) {
	if r == nil || strings.TrimSpace(request.ID) == "" {
		return
	}
	r.approvalMu.Lock()
	r.approvalQueue = append(r.approvalQueue, request)
	r.approvalMu.Unlock()
	r.showNextApproval()
}

// ShowSettings displays the settings dialog.
func (r *Runner) ShowSettings() {
	if r == nil {
		return
	}
	r.showSettingsOverlay()
}

// =============================================================================
// Callbacks - App interface methods
// =============================================================================

// SetCallbacks sets the submit, file select, and shell command callbacks.
func (r *Runner) SetCallbacks(onSubmit func(string), onFileSelect func(string), onShellCmd func(string) string) {
	if r == nil {
		return
	}
	r.onSubmit = onSubmit
	r.onFileSelect = onFileSelect
	r.onShellCmd = onShellCmd
}

// SetSessionCallbacks sets the next/prev session callbacks.
func (r *Runner) SetSessionCallbacks(onNext, onPrev func()) {
	if r == nil {
		return
	}
	r.onNextSession = onNext
	r.onPrevSession = onPrev
}

// SetProgress updates the progress display.
func (r *Runner) SetProgress(items []progress.Progress) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetProgress(items)
}

// SetToasts updates the toast display.
func (r *Runner) SetToasts(toasts []*toast.Toast) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetToasts(toasts)
}

// SetToastDismissHandler sets the handler for dismissing toasts.
func (r *Runner) SetToastDismissHandler(onDismiss func(string)) {
	if r == nil || r.bundle == nil {
		return
	}
	if r.bundle.ToastStack == nil {
		return
	}
	r.bundle.ToastStack.SetOnDismiss(onDismiss)
}

// SetDiagnostics sets the diagnostics collector.
func (r *Runner) SetDiagnostics(collector *diagnostics.Collector) {
	// Runner doesn't use diagnostics directly in the same way the legacy app did
	// The telemetry bridge handles this through events
}

// dispatchFunc is a wrapper for posting arbitrary functions to the event loop
// via runtime.CustomMsg.
type dispatchFunc struct{ fn func() }

// Dispatch schedules fn to run on the UI event loop. It is non-blocking:
// if the event loop is full or the app is shut down, fn is silently dropped.
// This is safe to call from any goroutine.
func (r *Runner) Dispatch(fn func()) {
	if r == nil || r.app == nil || fn == nil {
		return
	}
	r.app.Post(runtime.CustomMsg{Value: dispatchFunc{fn: fn}})
}

// SidebarSignals exposes writable signals for sidebar state.
func (r *Runner) SidebarSignals() SidebarSignals {
	if r == nil || r.state == nil {
		return SidebarSignals{}
	}
	return SidebarSignals{
		CurrentTask:        r.state.SidebarCurrentTask,
		TaskProgress:       r.state.SidebarTaskProgress,
		PlanTasks:          r.state.SidebarPlanTasks,
		RunningTools:       r.state.SidebarRunningTools,
		ToolHistory:        r.state.SidebarToolHistory,
		ActiveTouches:      r.state.SidebarActiveTouches,
		RecentFiles:        r.state.SidebarRecentFiles,
		RLMStatus:          r.state.SidebarRLMStatus,
		RLMScratchpad:      r.state.SidebarRLMScratchpad,
		CircuitStatus:      r.state.SidebarCircuitStatus,
		Experiment:         r.state.SidebarExperiment,
		ExperimentStatus:   r.state.SidebarExperimentStatus,
		ExperimentVariants: r.state.SidebarExperimentVariants,
	}
}
