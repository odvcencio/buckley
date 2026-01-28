// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"strings"
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/fluffy-ui/accessibility"
	"github.com/odvcencio/fluffy-ui/runtime"
)

// ============================================================================
// FILE: app_layout.go
// PURPOSE: Layout calculations and sidebar management
// FUNCTIONS:
//   - initFocus
//   - rebuildLayout
//   - updateSidebarVisibility
//   - toggleSidebar
//   - toggleFocusMode
//   - refreshInputLayout
//   - applyInputHeightLimit
// ============================================================================

func (a *WidgetApp) initFocus() {
	if a == nil || a.screen == nil {
		return
	}
	a.updateAnnouncerLabels()
	// Use BaseFocusScope to get the base layer's scope where main widgets are
	// (overlay layers like toastStack may be on top but have no focusables)
	scope := a.screen.BaseFocusScope()
	if scope == nil {
		if a.inputArea != nil {
			a.inputArea.Focus()
		}
		return
	}
	scope.SetOnChange(func(prev, next runtime.Focusable) {
		if a.announcer != nil {
			if accessible, ok := next.(accessibility.Accessible); ok {
				a.announcer.AnnounceChange(accessible)
			}
		}
		a.dirty = true
	})
	if a.inputArea != nil {
		scope.SetFocus(a.inputArea)
	}
}

// rebuildLayout rebuilds the main area layout based on sidebar visibility.
func (a *WidgetApp) rebuildLayout() {
	// Get current screen size
	w, h := a.screen.Size()

	// Rebuild main area with or without sidebar
	if a.sidebarVisible {
		a.mainArea = runtime.HBox(
			runtime.Flexible(a.chatView, 3),
			runtime.Sized(a.sidebar, a.sidebar.Width()),
		)
	} else if a.presenceVisible && a.presence != nil {
		a.mainArea = runtime.HBox(
			runtime.Expanded(a.chatView),
			runtime.Sized(a.presence, 2),
		)
	} else {
		a.mainArea = runtime.HBox(
			runtime.Expanded(a.chatView),
		)
	}

	children := make([]runtime.FlexChild, 0, 4)
	if a.headerVisible {
		children = append(children, runtime.Fixed(a.header))
	}
	children = append(children, runtime.Expanded(a.mainArea))
	children = append(children, runtime.Fixed(a.inputArea))
	if a.statusVisible {
		children = append(children, runtime.Fixed(a.statusBar))
	}
	a.root = runtime.VBox(children...)

	// Update screen with new root
	a.screen.SetRoot(a.root)
	a.screen.Resize(w, h)

	// Re-focus the input area after layout rebuild
	// (SetRoot's auto-register may have given focus to sidebar)
	if scope := a.screen.BaseFocusScope(); scope != nil {
		scope.SetFocus(a.inputArea)
	}

	a.dirty = true
}

func (a *WidgetApp) updateSidebarVisibility() {
	w, h := a.screen.Size()
	layout := layoutForScreen(w, h, a.sidebar.HasContent(), a.focusMode)
	if layout.sidebarWidth > 0 {
		a.sidebar.SetWidth(layout.sidebarWidth)
	}
	shouldShow := layout.sidebarVisible
	shouldPresence := layout.presenceVisible
	if a.sidebarUserOverride && !a.focusMode {
		shouldShow = a.sidebarVisible
		shouldPresence = a.presenceVisible
	}
	if shouldShow == a.sidebarVisible && shouldPresence == a.presenceVisible && layout.showHeader == a.headerVisible && layout.showStatus == a.statusVisible {
		return
	}
	a.sidebarVisible = shouldShow
	a.presenceVisible = shouldPresence
	a.sidebarAutoHidden = shouldPresence && !shouldShow
	a.headerVisible = layout.showHeader
	a.statusVisible = layout.showStatus
	a.rebuildLayout()
}

// toggleSidebar toggles the sidebar visibility and rebuilds the layout.
func (a *WidgetApp) toggleSidebar() {
	a.sidebarVisible = !a.sidebarVisible
	a.sidebarUserOverride = true // User manually toggled, don't auto-hide
	a.sidebarAutoHidden = false
	a.presenceVisible = false
	if a.sidebarVisible {
		a.setStatusOverride("Sidebar shown", 2*time.Second)
	} else {
		a.setStatusOverride("Sidebar hidden", 2*time.Second)
	}
	a.rebuildLayout()
}

// toggleFocusMode toggles focus mode (chat + input only).
func (a *WidgetApp) toggleFocusMode() {
	a.focusMode = !a.focusMode
	if a.focusMode {
		a.sidebarVisible = false
		a.presenceVisible = false
		a.sidebarAutoHidden = false
	}
	a.updateSidebarVisibility()
	if a.statusVisible {
		if a.focusMode {
			a.setStatusOverride("Focus mode on", 2*time.Second)
		} else {
			a.setStatusOverride("Focus mode off", 2*time.Second)
		}
	}
}

func (a *WidgetApp) refreshInputLayout() {
	w, h := a.screen.Size()
	desired := a.inputArea.Measure(runtime.Constraints{MaxWidth: w, MaxHeight: h}).Height
	if desired == a.inputMeasuredHeight {
		return
	}
	a.inputMeasuredHeight = desired
	a.screen.Resize(w, h)
	a.dirty = true
}

func (a *WidgetApp) applyInputHeightLimit(screenHeight int) {
	a.inputArea.SetHeightLimits(minInputHeight, maxInputHeight(screenHeight))
}

// SetSidebarVisible sets the sidebar visibility.
// Respects user override - won't change visibility if user manually toggled.
func (a *WidgetApp) SetSidebarVisible(visible bool) {
	if a == nil {
		return
	}
	if a.sidebarUserOverride {
		return // Don't override user's manual choice
	}
	if a.sidebarVisible != visible {
		a.sidebarVisible = visible
		if visible {
			a.presenceVisible = false
			a.sidebarAutoHidden = false
		}
		a.rebuildLayout()
	}
}

// IsSidebarVisible returns the sidebar visibility state.
func (a *WidgetApp) IsSidebarVisible() bool {
	if a == nil {
		return false
	}
	return a.sidebarVisible
}

// SetCurrentTask updates the sidebar's current task display.
func (a *WidgetApp) SetCurrentTask(name string, progress int) {
	if a == nil {
		return
	}
	a.sidebar.SetCurrentTask(name, progress)
	a.currentTaskActive = strings.TrimSpace(name) != ""
	a.updatePresenceStrip()
	a.updateSidebarVisibility()
	a.dirty = true
}

// SetPlanTasks updates the sidebar's plan task list.
func (a *WidgetApp) SetPlanTasks(tasks []buckleywidgets.PlanTask) {
	if a == nil {
		return
	}
	a.sidebar.SetPlanTasks(tasks)
	a.planTotal = len(tasks)
	a.planCompleted = 0
	for _, task := range tasks {
		if task.Status == buckleywidgets.TaskCompleted {
			a.planCompleted++
		}
	}
	a.updatePresenceStrip()
	a.updateSidebarVisibility()
	a.dirty = true
}

// SetRunningTools updates the sidebar's running tools list.
func (a *WidgetApp) SetRunningTools(tools []buckleywidgets.RunningTool) {
	if a == nil {
		return
	}
	a.sidebar.SetRunningTools(tools)
	a.runningToolCount = len(tools)
	a.updatePresenceStrip()
	if a.reduceMotion {
		a.sidebar.SetSpinnerFrame(0)
	}
	a.updateSidebarVisibility()
	a.dirty = true
}

// SetActiveTouches updates the sidebar's active touches list.
func (a *WidgetApp) SetActiveTouches(touches []buckleywidgets.TouchSummary) {
	if a == nil {
		return
	}
	a.sidebar.SetActiveTouches(touches)
	a.updateSidebarVisibility()
	a.dirty = true
}

// SetRLMStatus updates the sidebar's RLM status display.
// Auto-shows sidebar when RLM content arrives (unless user manually hid it).
func (a *WidgetApp) SetRLMStatus(status *buckleywidgets.RLMStatus, scratchpad []buckleywidgets.RLMScratchpadEntry) {
	if a == nil {
		return
	}
	a.sidebar.SetRLMStatus(status, scratchpad)
	// Auto-show sidebar for RLM mode if user hasn't manually hidden it
	if !a.sidebarUserOverride && !a.sidebarVisible && (status != nil || len(scratchpad) > 0) {
		a.sidebarVisible = true
		a.rebuildLayout()
	}
	a.dirty = true
}
