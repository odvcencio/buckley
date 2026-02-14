package widgets

import (
	"strings"

	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/state"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// Bind attaches app services and subscribes to state bindings.
func (s *Sidebar) Bind(services runtime.Services) {
	if s == nil {
		return
	}
	s.services = services
	s.subs.SetScheduler(services.Scheduler())
	s.subscribe()
}

// Unbind releases app services and subscriptions.
func (s *Sidebar) Unbind() {
	if s == nil {
		return
	}
	s.subs.Clear()
	s.services = runtime.Services{}
}

// TODO: the 40+ signal observations below follow a repetitive pattern. Consider
// a helper like observeIfSet(subs, sig, fn) to reduce boilerplate if more
// signals are added. Not refactoring now to avoid risk.
func (s *Sidebar) subscribe() {
	s.subs.Clear()
	if s.currentTaskSig != nil {
		s.subs.Observe(s.currentTaskSig, s.onCurrentTaskChanged)
	}
	if s.taskProgressSig != nil {
		s.subs.Observe(s.taskProgressSig, s.onCurrentTaskChanged)
	}
	if s.planTasksSig != nil {
		s.subs.Observe(s.planTasksSig, s.onPlanTasksChanged)
	}
	if s.runningToolsSig != nil {
		s.subs.Observe(s.runningToolsSig, s.onRunningToolsChanged)
	}
	if s.toolHistorySig != nil {
		s.subs.Observe(s.toolHistorySig, s.onToolHistoryChanged)
	}
	if s.activeTouchesSig != nil {
		s.subs.Observe(s.activeTouchesSig, s.onActiveTouchesChanged)
	}
	if s.recentFilesSig != nil {
		s.subs.Observe(s.recentFilesSig, s.onRecentFilesChanged)
	}
	if s.rlmStatusSig != nil {
		s.subs.Observe(s.rlmStatusSig, s.onRLMStatusChanged)
	}
	if s.rlmScratchpadSig != nil {
		s.subs.Observe(s.rlmScratchpadSig, s.onRLMStatusChanged)
	}
	if s.circuitStatusSig != nil {
		s.subs.Observe(s.circuitStatusSig, s.onCircuitStatusChanged)
	}
	if s.experimentSig != nil {
		s.subs.Observe(s.experimentSig, s.onExperimentChanged)
	}
	if s.experimentStatusSig != nil {
		s.subs.Observe(s.experimentStatusSig, s.onExperimentChanged)
	}
	if s.experimentVariantsSig != nil {
		s.subs.Observe(s.experimentVariantsSig, s.onExperimentChanged)
	}
	if s.contextUsedSig != nil {
		s.subs.Observe(s.contextUsedSig, s.onContextChanged)
	}
	if s.contextBudgetSig != nil {
		s.subs.Observe(s.contextBudgetSig, s.onContextChanged)
	}
	if s.contextWindowSig != nil {
		s.subs.Observe(s.contextWindowSig, s.onContextChanged)
	}
	if s.projectPathSig != nil {
		s.subs.Observe(s.projectPathSig, s.onProjectPathChanged)
	}
	if s.widthSig != nil {
		s.subs.Observe(s.widthSig, s.onWidthChanged)
	}
	if s.tabIndexSig != nil {
		s.subs.Observe(s.tabIndexSig, s.onTabIndexChanged)
	}
	if s.showCurrentTaskSig != nil {
		s.subs.Observe(s.showCurrentTaskSig, s.onVisibilityChanged)
	}
	if s.showPlanSig != nil {
		s.subs.Observe(s.showPlanSig, s.onVisibilityChanged)
	}
	if s.showToolsSig != nil {
		s.subs.Observe(s.showToolsSig, s.onVisibilityChanged)
	}
	if s.showContextSig != nil {
		s.subs.Observe(s.showContextSig, s.onVisibilityChanged)
	}
	if s.showTouchesSig != nil {
		s.subs.Observe(s.showTouchesSig, s.onVisibilityChanged)
	}
	if s.showRecentFilesSig != nil {
		s.subs.Observe(s.showRecentFilesSig, s.onVisibilityChanged)
	}
	if s.showExperimentSig != nil {
		s.subs.Observe(s.showExperimentSig, s.onVisibilityChanged)
	}
	if s.showRLMSig != nil {
		s.subs.Observe(s.showRLMSig, s.onVisibilityChanged)
	}
	if s.showCircuitSig != nil {
		s.subs.Observe(s.showCircuitSig, s.onVisibilityChanged)
	}
	if s.showAgentsSig != nil {
		s.subs.Observe(s.showAgentsSig, s.onVisibilityChanged)
	}
	if s.showLocksSig != nil {
		s.subs.Observe(s.showLocksSig, s.onVisibilityChanged)
	}
	if s.activeAgentsSig != nil {
		s.subs.Observe(s.activeAgentsSig, s.onActiveAgentsChanged)
	}
	if s.fileLocksSig != nil {
		s.subs.Observe(s.fileLocksSig, s.onFileLocksChanged)
	}
	s.onCurrentTaskChanged()
	s.onPlanTasksChanged()
	s.onRunningToolsChanged()
	s.onToolHistoryChanged()
	s.onActiveTouchesChanged()
	s.onRecentFilesChanged()
	s.onRLMStatusChanged()
	s.onCircuitStatusChanged()
	s.onExperimentChanged()
	s.onContextChanged()
	s.onProjectPathChanged()
	s.onWidthChanged()
	s.onTabIndexChanged()
	s.onVisibilityChanged()
	s.onActiveAgentsChanged()
	s.onFileLocksChanged()
}

func (s *Sidebar) onContextChanged() {
	if s.contextUsedSig == nil && s.contextBudgetSig == nil && s.contextWindowSig == nil {
		return
	}
	if s.contextUpdateDepth > 0 {
		return
	}
	s.applyContext()
	s.requestInvalidate()
}

func (s *Sidebar) onProjectPathChanged() {
	if s.projectPathSig == nil {
		return
	}
	path := strings.TrimSpace(s.projectPathSig.Get())
	if s.files != nil {
		s.files.ApplyProjectPath(path)
	}
	s.requestInvalidate()
}

func (s *Sidebar) onWidthChanged() {
	if s.widthSig == nil {
		return
	}
	width := s.widthSig.Get()
	normalized := s.normalizeWidth(width)
	if normalized != width {
		s.writeWidthSignal(normalized)
	}
	if s.setWidthLocal(normalized) {
		s.requestRelayout()
	}
}

func (s *Sidebar) onTabIndexChanged() {
	if s.tabIndexSig == nil {
		return
	}
	s.applyTabIndex(s.tabIndexSig.Get())
}

func (s *Sidebar) onVisibilityChanged() {
	statusChanged, filesChanged := s.applyVisibility()
	if statusChanged || filesChanged {
		s.requestRelayout()
	}
}

func (s *Sidebar) onCurrentTaskChanged() {
	if s.currentTaskSig == nil && s.taskProgressSig == nil {
		return
	}
	name := ""
	progress := 0
	if s.currentTaskSig != nil {
		name = s.currentTaskSig.Get()
	}
	if s.taskProgressSig != nil {
		progress = s.taskProgressSig.Get()
	}
	s.applyCurrentTask(name, progress)
	s.requestRelayout()
}

func (s *Sidebar) onPlanTasksChanged() {
	if s.planTasksSig == nil {
		return
	}
	s.applyPlanTasks(clonePlanTasks(s.planTasksSig.Get()))
	s.requestRelayout()
}

func (s *Sidebar) onRunningToolsChanged() {
	if s.runningToolsSig == nil {
		return
	}
	s.applyRunningTools(cloneRunningTools(s.runningToolsSig.Get()))
	s.requestRelayout()
}

func (s *Sidebar) onToolHistoryChanged() {
	if s.toolHistorySig == nil {
		return
	}
	s.applyToolHistory(cloneToolHistory(s.toolHistorySig.Get()))
	s.requestRelayout()
}

func (s *Sidebar) onActiveTouchesChanged() {
	if s.activeTouchesSig == nil {
		return
	}
	s.applyActiveTouches(cloneTouchSummaries(s.activeTouchesSig.Get()))
	s.requestRelayout()
}

func (s *Sidebar) onRecentFilesChanged() {
	if s.recentFilesSig == nil {
		return
	}
	s.applyRecentFiles(cloneStrings(s.recentFilesSig.Get()))
	s.requestRelayout()
}

func (s *Sidebar) onRLMStatusChanged() {
	if s.rlmStatusSig == nil && s.rlmScratchpadSig == nil {
		return
	}
	status := (*RLMStatus)(nil)
	if s.rlmStatusSig != nil {
		status = s.rlmStatusSig.Get()
	}
	scratchpad := []RLMScratchpadEntry(nil)
	if s.rlmScratchpadSig != nil {
		scratchpad = s.rlmScratchpadSig.Get()
	}
	s.applyRLMStatus(cloneRLMStatus(status), cloneScratchpadEntries(scratchpad))
	s.requestRelayout()
}

func (s *Sidebar) onCircuitStatusChanged() {
	if s.circuitStatusSig == nil {
		return
	}
	s.applyCircuitStatus(cloneCircuitStatus(s.circuitStatusSig.Get()))
	s.requestRelayout()
}

func (s *Sidebar) onExperimentChanged() {
	if s.experimentSig == nil && s.experimentStatusSig == nil && s.experimentVariantsSig == nil {
		return
	}
	name := ""
	status := ""
	variants := []ExperimentVariant(nil)
	if s.experimentSig != nil {
		name = s.experimentSig.Get()
	}
	if s.experimentStatusSig != nil {
		status = s.experimentStatusSig.Get()
	}
	if s.experimentVariantsSig != nil {
		variants = s.experimentVariantsSig.Get()
	}
	s.applyExperiment(name, status, cloneExperimentVariants(variants))
	s.requestRelayout()
}

func (s *Sidebar) onActiveAgentsChanged() {
	if s.activeAgentsSig == nil {
		return
	}
	s.applyActiveAgents(cloneAgentSummaries(s.activeAgentsSig.Get()))
	s.requestRelayout()
}

func (s *Sidebar) onFileLocksChanged() {
	if s.fileLocksSig == nil {
		return
	}
	s.applyFileLocks(cloneFileLockSummaries(s.fileLocksSig.Get()))
	s.requestRelayout()
}

func (s *Sidebar) applyContext() {
	used := 0
	budget := 0
	window := 0
	if s.contextUsedSig != nil {
		used = s.contextUsedSig.Get()
	}
	if s.contextBudgetSig != nil {
		budget = s.contextBudgetSig.Get()
	}
	if s.contextWindowSig != nil {
		window = s.contextWindowSig.Get()
	}
	s.applyContextUsage(used, budget, window)
}

func (s *Sidebar) applyVisibility() (bool, bool) {
	statusChanged := false
	filesChanged := false

	if s.status != nil {
		statusChanged = s.status.ApplyVisibility(
			readVisibility(s.showCurrentTaskSig, s.status.showCurrentTask),
			readVisibility(s.showPlanSig, s.status.showPlan),
			readVisibility(s.showToolsSig, s.status.showTools),
			readVisibility(s.showContextSig, s.status.showContext),
			readVisibility(s.showExperimentSig, s.status.showExperiment),
			readVisibility(s.showRLMSig, s.status.showRLM),
			readVisibility(s.showCircuitSig, s.status.showCircuit),
			readVisibility(s.showAgentsSig, s.status.showAgents),
		)
	}
	if s.files != nil {
		filesChanged = s.files.ApplyVisibility(
			readVisibility(s.showRecentFilesSig, s.files.showRecentFiles),
			readVisibility(s.showTouchesSig, s.files.showTouches),
			readVisibility(s.showLocksSig, s.files.showLocks),
		)
	}
	return statusChanged, filesChanged
}

func readVisibility(sig state.Readable[bool], fallback bool) bool {
	if sig == nil {
		return fallback
	}
	return sig.Get()
}

func (s *Sidebar) requestInvalidate() {
	if s.services == (runtime.Services{}) {
		return
	}
	s.services.Invalidate()
}

func (s *Sidebar) requestRelayout() {
	if s.services == (runtime.Services{}) {
		return
	}
	s.services.Relayout()
}

func (s *Sidebar) initWidgets() {
	s.status = newSidebarStatus(s.borderStyle)
	s.files = newSidebarFiles(s.borderStyle)
	s.tabs = uiwidgets.NewTabs(
		uiwidgets.Tab{Title: "Status", Content: s.status.ScrollView()},
		uiwidgets.Tab{Title: "Files", Content: s.files.ScrollView()},
	)
	s.Base.Role = accessibility.RoleGroup
	s.Base.Landmark = accessibility.LandmarkComplementary
	s.Base.Label = "Conversation sidebar"
}
