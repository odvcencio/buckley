package tui

import (
	"fmt"
	"time"

	"m31labs.dev/buckley/pkg/ui/widgets"
	"m31labs.dev/fluffyui/runtime"
)

const (
	defaultSidebarWidth       = 26
	defaultActivityPanelWidth = 40
	minSidebarWidth           = 18
	maxSidebarWidth           = 44
	minActivityPanelWidth     = 26
	maxActivityPanelWidth     = 72
	minWorkspaceChatWidth     = 48
	panelResizeStep           = 4
)

type workspaceVisibility struct {
	left  bool
	right bool
}

func resolveWorkspaceVisibility(screenWidth int, leftWanted, rightWanted bool, leftWidth, rightWidth int) workspaceVisibility {
	visibility := workspaceVisibility{left: leftWanted, right: rightWanted}
	if visibility.left && visibility.right && screenWidth-leftWidth-rightWidth < minWorkspaceChatWidth {
		visibility.right = false
	}
	if visibility.left && screenWidth-leftWidth < minWorkspaceChatWidth {
		visibility.left = false
		// The navigator just freed its width. Give the inspector another
		// chance against the wider chat area instead of leaving it hidden
		// from the combined check above.
		visibility.right = rightWanted
	}
	if visibility.right && screenWidth-rightWidth < minWorkspaceChatWidth {
		visibility.right = false
	}
	return visibility
}

func workspaceMainArea(chat *widgets.ChatView, sidebar *widgets.Sidebar, activity *widgets.ActivityPanel, visibility workspaceVisibility, leftWidth, rightWidth int) *runtime.Flex {
	children := make([]runtime.FlexChild, 0, 3)
	if visibility.left {
		children = append(children, runtime.Sized(sidebar, leftWidth))
	}
	children = append(children, expandFromZero(chat))
	if visibility.right {
		children = append(children, runtime.Sized(activity, rightWidth))
	}
	return runtime.HBox(children...)
}

func (a *WidgetApp) buildWorkspaceMainArea() *runtime.Flex {
	return workspaceMainArea(
		a.chatView,
		a.sidebar,
		a.activityPanel,
		workspaceVisibility{left: a.sidebarVisible, right: a.activityPanelVisible},
		a.sidebarWidth,
		a.activityPanelWidth,
	)
}

func (a *WidgetApp) updateWorkspaceVisibility() {
	if a == nil || a.screen == nil {
		return
	}
	width, _ := a.screen.Size()
	leftWanted := a.sidebarWanted && width >= a.minWidthForSidebar
	visibility := resolveWorkspaceVisibility(
		width,
		leftWanted,
		a.activityPanelWanted,
		a.sidebarWidth,
		a.activityPanelWidth,
	)
	if visibility.left == a.sidebarVisible && visibility.right == a.activityPanelVisible {
		return
	}
	a.sidebarVisible = visibility.left
	a.activityPanelVisible = visibility.right
	a.rebuildLayout()
}

func (a *WidgetApp) toggleActivityPanel() {
	if a == nil {
		return
	}
	a.activityPanelTouched = true
	a.activityPanelWanted = !a.activityPanelWanted
	a.updateWorkspaceVisibility()
	if a.activityPanelVisible {
		a.setStatusOverride("Activity inspector shown", 2*time.Second)
	} else {
		a.setStatusOverride("Activity inspector hidden", 2*time.Second)
	}
}

func (a *WidgetApp) resizeSidebar(delta int) {
	if a == nil || delta == 0 {
		return
	}
	a.sidebarWidth = clampWorkspacePanel(a.sidebarWidth+delta, minSidebarWidth, maxSidebarWidth)
	a.sidebarWanted = true
	wasVisible := a.sidebarVisible
	a.updateWorkspaceVisibility()
	if wasVisible && a.sidebarVisible {
		a.rebuildLayout()
	}
	a.setStatusOverride(fmt.Sprintf("Navigator width %d", a.sidebarWidth), 2*time.Second)
}

func (a *WidgetApp) resizeActivityPanel(delta int) {
	if a == nil || delta == 0 {
		return
	}
	a.activityPanelTouched = true
	a.activityPanelWidth = clampWorkspacePanel(a.activityPanelWidth+delta, minActivityPanelWidth, maxActivityPanelWidth)
	a.activityPanelWanted = true
	wasVisible := a.activityPanelVisible
	a.updateWorkspaceVisibility()
	if wasVisible && a.activityPanelVisible {
		a.rebuildLayout()
	}
	a.setStatusOverride(fmt.Sprintf("Inspector width %d", a.activityPanelWidth), 2*time.Second)
}

func clampWorkspacePanel(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

// SetActivities updates the persistent inspector. The first inspectable event
// opens the panel automatically; once the user explicitly toggles it, that
// preference is respected for the remainder of the session.
//
// Telemetry delivers activity updates from its own forwarding goroutine, so
// this posts a message rather than mutating the widget tree directly. That
// keeps every mutation of a.activityPanel and the workspace layout on the UI
// goroutine, matching how AddMessage is made safe to call from any goroutine.
func (a *WidgetApp) SetActivities(records []widgets.ActivityRecord) {
	if a == nil {
		return
	}
	a.Post(SetActivitiesMsg{Records: records})
}

// applySetActivities performs the actual inspector mutation. Callers must
// already be on the UI goroutine (via the message loop) or before Run starts.
func (a *WidgetApp) applySetActivities(records []widgets.ActivityRecord) {
	if a == nil || a.activityPanel == nil {
		return
	}
	hadContent := a.activityPanel.HasContent()
	a.activityPanel.SetActivities(records)
	if len(records) > 0 && !hadContent && !a.activityPanelTouched {
		a.activityPanelWanted = true
	}
	a.updateWorkspaceVisibility()
	a.dirty = true
}

// SetActivityPanelVisible explicitly controls the inspector.
func (a *WidgetApp) SetActivityPanelVisible(visible bool) {
	if a == nil {
		return
	}
	a.activityPanelTouched = true
	a.activityPanelWanted = visible
	a.updateWorkspaceVisibility()
}

// IsActivityPanelVisible reports the responsive inspector state.
func (a *WidgetApp) IsActivityPanelVisible() bool {
	return a != nil && a.activityPanelVisible
}

func (a *WidgetApp) handleChatScrollbarMouse(m MouseMsg) bool {
	if a == nil || a.chatView == nil {
		return false
	}
	result := a.chatView.HandleMessage(runtime.MouseMsg{
		X:      m.X,
		Y:      m.Y,
		Button: runtime.MouseButton(m.Button),
		Action: runtime.MouseAction(m.Action),
	})
	if result.Handled {
		a.updateScrollStatus()
		a.dirty = true
	}
	return result.Handled
}
