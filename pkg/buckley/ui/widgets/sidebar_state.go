package widgets

import (
	"strings"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/state"
)

func (s *Sidebar) applyActiveAgents(agents []AgentSummary) {
	if s.status != nil {
		s.status.applyAgents(agents)
	}
}

func (s *Sidebar) applyFileLocks(locks []FileLockSummary) {
	if s.files != nil {
		s.files.applyFileLocks(locks)
	}
}

func (s *Sidebar) applyContextUsage(used, budget, window int) {
	if s.status != nil {
		s.status.ApplyContextUsage(used, budget, window)
	}
}

// SetStyles configures the sidebar appearance.
func (s *Sidebar) SetStyles(border, header, text, progressFull, progressEmpty, background backend.Style) {
	if s == nil {
		return
	}
	s.borderStyle = border
	s.headerStyle = header
	s.textStyle = text
	s.progressFull = progressFull
	s.progressEmpty = progressEmpty
	s.bgStyle = background

	if s.status != nil {
		s.status.SetStyles(border, background, text)
	}
	if s.files != nil {
		s.files.SetStyles(border, background)
	}
	if s.tabs != nil {
		s.tabs.SetStyle(background)
	}
}

// SetProgressEdgeStyle configures the highlight style for active progress edges.
func (s *Sidebar) SetProgressEdgeStyle(style backend.Style) {
	if s == nil {
		return
	}
	s.progressEdge = style
}

// SetStatusStyles configures styles for status indicators.
func (s *Sidebar) SetStatusStyles(completed, active, pending, failed backend.Style) {
	if s == nil {
		return
	}
	s.completedStyle = completed
	s.activeStyle = active
	s.pendingStyle = pending
	s.failedStyle = failed
}

// SetContextStyles configures styles for context usage indicators.
func (s *Sidebar) SetContextStyles(active, warn, critical, muted backend.Style) {
	if s == nil {
		return
	}
	s.contextActive = active
	s.contextWarn = warn
	s.contextCritical = critical
	s.contextMuted = muted
	if s.status != nil {
		s.status.SetContextStyles(active, warn, critical, muted)
	}
}

// SetSpinnerStyle configures the spinner style.
func (s *Sidebar) SetSpinnerStyle(style backend.Style) {
	if s == nil {
		return
	}
	s.spinnerStyle = style
	if s.status != nil {
		s.status.SetSpinnerStyle(style)
	}
}

// HasContent returns true when any sidebar section has data to render.
func (s *Sidebar) HasContent() bool {
	if s.status != nil && s.status.HasContent() {
		return true
	}
	if s.files != nil && s.files.HasContent() {
		return true
	}
	return false
}

// SetProjectPath updates breadcrumb path display.
func (s *Sidebar) SetProjectPath(path string) {
	if s == nil {
		return
	}
	if s.ownsProjectPath() && s.ownedProjectPath != nil {
		s.ownedProjectPath.Set(strings.TrimSpace(path))
	}
}

func (s *Sidebar) ownsProjectPath() bool {
	if s == nil || s.projectPathSig == nil || s.ownedProjectPath == nil {
		return false
	}
	sig, ok := s.projectPathSig.(*state.Signal[string])
	return ok && sig == s.ownedProjectPath
}

// SetCurrentTask updates the current task display.
func (s *Sidebar) SetCurrentTask(name string, progress int) {
	if s == nil {
		return
	}
	progress = clampPercent(progress)
	if s.currentTaskSig != nil || s.taskProgressSig != nil {
		wrote := false
		state.Batch(func() {
			if writeSignal(s.currentTaskSig, name) {
				wrote = true
			}
			if writeSignal(s.taskProgressSig, progress) {
				wrote = true
			}
		})
		if wrote {
			return
		}
	}
	s.applyCurrentTask(name, progress)
}

func (s *Sidebar) applyCurrentTask(name string, progress int) {
	if s.status != nil {
		s.status.applyCurrentTask(name, progress)
	}
}

func (s *Sidebar) setVisibility(sig state.Readable[bool], show bool) {
	if s == nil || sig == nil {
		return
	}
	if writable, ok := sig.(state.Writable[bool]); ok && writable != nil {
		writable.Set(show)
	}
}

func writeSignal[T any](sig state.Readable[T], value T) bool {
	if sig == nil {
		return false
	}
	if writable, ok := sig.(state.Writable[T]); ok && writable != nil {
		writable.Set(value)
		return true
	}
	return false
}

// SetShowCurrentTask controls visibility of current task section.
func (s *Sidebar) SetShowCurrentTask(show bool) {
	s.setVisibility(s.showCurrentTaskSig, show)
}

// ToggleCurrentTask toggles the current task section visibility.
func (s *Sidebar) ToggleCurrentTask() {
	if s == nil || s.showCurrentTaskSig == nil {
		return
	}
	s.SetShowCurrentTask(!s.showCurrentTaskSig.Get())
}

// SetPlanTasks updates the plan task list.
func (s *Sidebar) SetPlanTasks(tasks []PlanTask) {
	if s == nil {
		return
	}
	cloned := clonePlanTasks(tasks)
	if writeSignal(s.planTasksSig, cloned) {
		return
	}
	s.applyPlanTasks(cloned)
}

func (s *Sidebar) applyPlanTasks(tasks []PlanTask) {
	if s.status != nil {
		s.status.applyPlanTasks(tasks)
	}
}

// SetShowPlan controls visibility of plan section.
func (s *Sidebar) SetShowPlan(show bool) {
	s.setVisibility(s.showPlanSig, show)
}

// TogglePlan toggles the plan section visibility.
func (s *Sidebar) TogglePlan() {
	if s == nil || s.showPlanSig == nil {
		return
	}
	s.SetShowPlan(!s.showPlanSig.Get())
}

// SetRunningTools updates the running tools list.
func (s *Sidebar) SetRunningTools(tools []RunningTool) {
	if s == nil {
		return
	}
	cloned := cloneRunningTools(tools)
	if writeSignal(s.runningToolsSig, cloned) {
		return
	}
	s.applyRunningTools(cloned)
}

func (s *Sidebar) applyRunningTools(tools []RunningTool) {
	if s.status != nil {
		s.status.applyRunningTools(tools)
	}
}

// SetToolHistory updates recent tool history entries.
func (s *Sidebar) SetToolHistory(history []ToolHistoryEntry) {
	if s == nil {
		return
	}
	cloned := cloneToolHistory(history)
	if writeSignal(s.toolHistorySig, cloned) {
		return
	}
	s.applyToolHistory(cloned)
}

func (s *Sidebar) applyToolHistory(history []ToolHistoryEntry) {
	if s.status != nil {
		s.status.applyToolHistory(history)
	}
}

// SetShowTools controls visibility of tools section.
func (s *Sidebar) SetShowTools(show bool) {
	s.setVisibility(s.showToolsSig, show)
}

// ToggleTools toggles the tools section visibility.
func (s *Sidebar) ToggleTools() {
	if s == nil || s.showToolsSig == nil {
		return
	}
	s.SetShowTools(!s.showToolsSig.Get())
}

// SetContextUsage updates context usage values.
func (s *Sidebar) SetContextUsage(used, budget, window int) {
	if s == nil {
		return
	}
	if s.contextUsedSig != nil || s.contextBudgetSig != nil || s.contextWindowSig != nil {
		s.contextUpdateDepth++
		defer func() {
			s.contextUpdateDepth--
			if s.contextUpdateDepth == 0 {
				s.applyContextUsage(used, budget, window)
				s.requestInvalidate()
			}
		}()
		s.writeContextSignal(s.contextUsedSig, used)
		s.writeContextSignal(s.contextBudgetSig, budget)
		s.writeContextSignal(s.contextWindowSig, window)
		return
	}
	s.applyContextUsage(used, budget, window)
}

func (s *Sidebar) writeContextSignal(sig state.Readable[int], value int) {
	if sig == nil {
		return
	}
	if writable, ok := sig.(state.Writable[int]); ok && writable != nil {
		writable.Set(value)
	}
}

// SetShowContext controls visibility of context section.
func (s *Sidebar) SetShowContext(show bool) {
	s.setVisibility(s.showContextSig, show)
}

// ToggleContext toggles the context section visibility.
func (s *Sidebar) ToggleContext() {
	if s == nil || s.showContextSig == nil {
		return
	}
	s.SetShowContext(!s.showContextSig.Get())
}

// SetActiveTouches updates the active touches list.
func (s *Sidebar) SetActiveTouches(touches []TouchSummary) {
	if s == nil {
		return
	}
	cloned := cloneTouchSummaries(touches)
	if writeSignal(s.activeTouchesSig, cloned) {
		return
	}
	s.applyActiveTouches(cloned)
}

func (s *Sidebar) applyActiveTouches(touches []TouchSummary) {
	if s.files != nil {
		s.files.applyActiveTouches(touches)
	}
}

// SetShowTouches controls visibility of touches section.
func (s *Sidebar) SetShowTouches(show bool) {
	s.setVisibility(s.showTouchesSig, show)
}

// ToggleTouches toggles the touches section visibility.
func (s *Sidebar) ToggleTouches() {
	if s == nil || s.showTouchesSig == nil {
		return
	}
	s.SetShowTouches(!s.showTouchesSig.Get())
}

// SetRecentFiles updates the recent files list.
func (s *Sidebar) SetRecentFiles(files []string) {
	if s == nil {
		return
	}
	cloned := cloneStrings(files)
	if writeSignal(s.recentFilesSig, cloned) {
		return
	}
	s.applyRecentFiles(cloned)
}

func (s *Sidebar) applyRecentFiles(files []string) {
	if s.files != nil {
		s.files.applyRecentFiles(files)
	}
}

// SetShowRecentFiles controls visibility of recent files section.
func (s *Sidebar) SetShowRecentFiles(show bool) {
	s.setVisibility(s.showRecentFilesSig, show)
}

// SetExperiment updates the experiment summary.
func (s *Sidebar) SetExperiment(name, status string, variants []ExperimentVariant) {
	if s == nil {
		return
	}
	cloned := cloneExperimentVariants(variants)
	if s.experimentSig != nil || s.experimentStatusSig != nil || s.experimentVariantsSig != nil {
		wrote := false
		state.Batch(func() {
			if writeSignal(s.experimentSig, name) {
				wrote = true
			}
			if writeSignal(s.experimentStatusSig, status) {
				wrote = true
			}
			if writeSignal(s.experimentVariantsSig, cloned) {
				wrote = true
			}
		})
		if wrote {
			return
		}
	}
	s.applyExperiment(name, status, cloned)
}

func (s *Sidebar) applyExperiment(name, status string, variants []ExperimentVariant) {
	if s.status != nil {
		s.status.applyExperiment(name, status, variants)
	}
}

// SetRLMStatus updates the RLM iteration status and scratchpad summaries.
func (s *Sidebar) SetRLMStatus(status *RLMStatus, scratchpad []RLMScratchpadEntry) {
	if s == nil {
		return
	}
	clonedStatus := cloneRLMStatus(status)
	clonedScratchpad := cloneScratchpadEntries(scratchpad)
	if s.rlmStatusSig != nil || s.rlmScratchpadSig != nil {
		wrote := false
		state.Batch(func() {
			if writeSignal(s.rlmStatusSig, clonedStatus) {
				wrote = true
			}
			if writeSignal(s.rlmScratchpadSig, clonedScratchpad) {
				wrote = true
			}
		})
		if wrote {
			return
		}
	}
	s.applyRLMStatus(clonedStatus, clonedScratchpad)
}

func (s *Sidebar) applyRLMStatus(status *RLMStatus, scratchpad []RLMScratchpadEntry) {
	if s.status != nil {
		s.status.applyRLMStatus(status, scratchpad)
	}
}

// SetCircuitStatus updates the circuit breaker status display.
func (s *Sidebar) SetCircuitStatus(status *CircuitStatus) {
	if s == nil {
		return
	}
	cloned := cloneCircuitStatus(status)
	if writeSignal(s.circuitStatusSig, cloned) {
		return
	}
	s.applyCircuitStatus(cloned)
}

func (s *Sidebar) applyCircuitStatus(status *CircuitStatus) {
	if s.status != nil {
		s.status.applyCircuitStatus(status)
	}
}

// SetShowCircuit controls visibility of circuit breaker section.
func (s *Sidebar) SetShowCircuit(show bool) {
	s.setVisibility(s.showCircuitSig, show)
}

// SetActiveAgents updates the active agents display.
func (s *Sidebar) SetActiveAgents(agents []AgentSummary) {
	if s == nil {
		return
	}
	cloned := cloneAgentSummaries(agents)
	if writeSignal(s.activeAgentsSig, cloned) {
		return
	}
	s.applyActiveAgents(cloned)
}

// SetFileLocks updates the file locks display.
func (s *Sidebar) SetFileLocks(locks []FileLockSummary) {
	if s == nil {
		return
	}
	cloned := cloneFileLockSummaries(locks)
	if writeSignal(s.fileLocksSig, cloned) {
		return
	}
	s.applyFileLocks(cloned)
}

// SetShowAgents controls visibility of agents section.
func (s *Sidebar) SetShowAgents(show bool) {
	s.setVisibility(s.showAgentsSig, show)
}

// SetShowLocks controls visibility of file locks section.
func (s *Sidebar) SetShowLocks(show bool) {
	s.setVisibility(s.showLocksSig, show)
}

// SetShowRLM controls visibility of the RLM section.
func (s *Sidebar) SetShowRLM(show bool) {
	s.setVisibility(s.showRLMSig, show)
}

// SetShowExperiment controls visibility of experiments section.
func (s *Sidebar) SetShowExperiment(show bool) {
	s.setVisibility(s.showExperimentSig, show)
}

// SetSpinnerFrame updates the spinner animation frame.
func (s *Sidebar) SetSpinnerFrame(frame int) {
	if s == nil {
		return
	}
	if s.status != nil {
		s.status.SetSpinnerFrame(frame)
	}
}

// ToggleRecentFiles toggles the recent files section.
func (s *Sidebar) ToggleRecentFiles() {
	if s == nil || s.showRecentFilesSig == nil {
		return
	}
	s.SetShowRecentFiles(!s.showRecentFilesSig.Get())
}
