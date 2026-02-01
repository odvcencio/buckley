package services

import (
	"github.com/odvcencio/buckley/pkg/buckley/ui/tui/state"
	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
)

// SidebarService manages sidebar state updates.
type SidebarService struct {
	state *state.AppState
}

// NewSidebarService creates a new sidebar service.
func NewSidebarService(s *state.AppState) *SidebarService {
	return &SidebarService{state: s}
}

// SetSidebarState updates the sidebar snapshot state.
func (svc *SidebarService) SetSidebarState(snapshot buckleywidgets.SidebarState) {
	if svc == nil || svc.state == nil {
		return
	}
	svc.state.SidebarState.Set(cloneSidebarState(snapshot))
}

// ToggleCurrentTask toggles the current task section visibility.
func (svc *SidebarService) ToggleCurrentTask() {
	if svc == nil || svc.state == nil {
		return
	}
	toggleBool(svc.state.SidebarShowCurrentTask)
}

// TogglePlan toggles the plan section visibility.
func (svc *SidebarService) TogglePlan() {
	if svc == nil || svc.state == nil {
		return
	}
	toggleBool(svc.state.SidebarShowPlan)
}

// ToggleTools toggles the tools section visibility.
func (svc *SidebarService) ToggleTools() {
	if svc == nil || svc.state == nil {
		return
	}
	toggleBool(svc.state.SidebarShowTools)
}

// ToggleContext toggles the context section visibility.
func (svc *SidebarService) ToggleContext() {
	if svc == nil || svc.state == nil {
		return
	}
	toggleBool(svc.state.SidebarShowContext)
}

// ToggleTouches toggles the touches section visibility.
func (svc *SidebarService) ToggleTouches() {
	if svc == nil || svc.state == nil {
		return
	}
	toggleBool(svc.state.SidebarShowTouches)
}

// ToggleRecentFiles toggles the recent files section visibility.
func (svc *SidebarService) ToggleRecentFiles() {
	if svc == nil || svc.state == nil {
		return
	}
	toggleBool(svc.state.SidebarShowRecentFiles)
}

// SetWidth sets the sidebar width in characters.
func (svc *SidebarService) SetWidth(width int) {
	if svc == nil || svc.state == nil || svc.state.SidebarWidth == nil {
		return
	}
	svc.state.SidebarWidth.Set(width)
}

// Grow increases the sidebar width by delta characters.
func (svc *SidebarService) Grow(delta int) {
	if svc == nil || svc.state == nil || svc.state.SidebarWidth == nil {
		return
	}
	if delta == 0 {
		return
	}
	current := svc.state.SidebarWidth.Get()
	svc.state.SidebarWidth.Set(current + delta)
}

// Shrink decreases the sidebar width by delta characters.
func (svc *SidebarService) Shrink(delta int) {
	if svc == nil || svc.state == nil || svc.state.SidebarWidth == nil {
		return
	}
	if delta == 0 {
		return
	}
	current := svc.state.SidebarWidth.Get()
	svc.state.SidebarWidth.Set(current - delta)
}

// SetTabIndex selects the sidebar tab by index.
func (svc *SidebarService) SetTabIndex(index int) {
	if svc == nil || svc.state == nil || svc.state.SidebarTabIndex == nil {
		return
	}
	svc.state.SidebarTabIndex.Set(index)
}

// NextTab advances the sidebar tab selection.
func (svc *SidebarService) NextTab() {
	if svc == nil || svc.state == nil || svc.state.SidebarTabIndex == nil {
		return
	}
	current := svc.state.SidebarTabIndex.Get()
	svc.state.SidebarTabIndex.Set(current + 1)
}

// PrevTab moves the sidebar tab selection backward.
func (svc *SidebarService) PrevTab() {
	if svc == nil || svc.state == nil || svc.state.SidebarTabIndex == nil {
		return
	}
	current := svc.state.SidebarTabIndex.Get()
	svc.state.SidebarTabIndex.Set(current - 1)
}

type boolSignal interface {
	Get() bool
	Set(bool) bool
}

func toggleBool(sig boolSignal) {
	if sig == nil {
		return
	}
	sig.Set(!sig.Get())
}

func cloneSidebarState(src buckleywidgets.SidebarState) buckleywidgets.SidebarState {
	dst := buckleywidgets.SidebarState{
		CurrentTask:      src.CurrentTask,
		TaskProgress:     src.TaskProgress,
		Experiment:       src.Experiment,
		ExperimentStatus: src.ExperimentStatus,
	}
	if src.PlanTasks != nil {
		dst.PlanTasks = append([]buckleywidgets.PlanTask(nil), src.PlanTasks...)
	}
	if src.RunningTools != nil {
		dst.RunningTools = append([]buckleywidgets.RunningTool(nil), src.RunningTools...)
	}
	if src.ToolHistory != nil {
		dst.ToolHistory = append([]buckleywidgets.ToolHistoryEntry(nil), src.ToolHistory...)
	}
	if src.ActiveTouches != nil {
		dst.ActiveTouches = append([]buckleywidgets.TouchSummary(nil), src.ActiveTouches...)
	}
	if src.RecentFiles != nil {
		dst.RecentFiles = append([]string(nil), src.RecentFiles...)
	}
	if src.RLMScratchpad != nil {
		dst.RLMScratchpad = append([]buckleywidgets.RLMScratchpadEntry(nil), src.RLMScratchpad...)
	}
	if src.ExperimentVariants != nil {
		dst.ExperimentVariants = append([]buckleywidgets.ExperimentVariant(nil), src.ExperimentVariants...)
	}
	if src.RLMStatus != nil {
		copyStatus := *src.RLMStatus
		dst.RLMStatus = &copyStatus
	}
	if src.CircuitStatus != nil {
		copyStatus := *src.CircuitStatus
		dst.CircuitStatus = &copyStatus
	}
	return dst
}
