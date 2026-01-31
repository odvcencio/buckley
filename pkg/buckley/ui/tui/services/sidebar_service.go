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
