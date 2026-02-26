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

// observeSignal subscribes to sig using subs if sig is non-nil.
func observeSignal[T any](subs *state.Subscriptions, sig state.Readable[T], fn func()) {
	if sig != nil {
		subs.Observe(sig, fn)
	}
}

func (s *Sidebar) subscribe() {
	s.subs.Clear()

	observeSignal(&s.subs, s.currentTaskSig, s.onCurrentTaskChanged)
	observeSignal(&s.subs, s.taskProgressSig, s.onCurrentTaskChanged)
	observeSignal(&s.subs, s.planTasksSig, s.onPlanTasksChanged)
	observeSignal(&s.subs, s.runningToolsSig, s.onRunningToolsChanged)
	observeSignal(&s.subs, s.toolHistorySig, s.onToolHistoryChanged)
	observeSignal(&s.subs, s.activeTouchesSig, s.onActiveTouchesChanged)
	observeSignal(&s.subs, s.recentFilesSig, s.onRecentFilesChanged)
	observeSignal(&s.subs, s.rlmStatusSig, s.onRLMStatusChanged)
	observeSignal(&s.subs, s.rlmScratchpadSig, s.onRLMStatusChanged)
	observeSignal(&s.subs, s.circuitStatusSig, s.onCircuitStatusChanged)
	observeSignal(&s.subs, s.experimentSig, s.onExperimentChanged)
	observeSignal(&s.subs, s.experimentStatusSig, s.onExperimentChanged)
	observeSignal(&s.subs, s.experimentVariantsSig, s.onExperimentChanged)
	observeSignal(&s.subs, s.contextUsedSig, s.onContextChanged)
	observeSignal(&s.subs, s.contextBudgetSig, s.onContextChanged)
	observeSignal(&s.subs, s.contextWindowSig, s.onContextChanged)
	observeSignal(&s.subs, s.projectPathSig, s.onProjectPathChanged)
	observeSignal(&s.subs, s.widthSig, s.onWidthChanged)
	observeSignal(&s.subs, s.tabIndexSig, s.onTabIndexChanged)
	observeSignal(&s.subs, s.showCurrentTaskSig, s.onVisibilityChanged)
	observeSignal(&s.subs, s.showPlanSig, s.onVisibilityChanged)
	observeSignal(&s.subs, s.showToolsSig, s.onVisibilityChanged)
	observeSignal(&s.subs, s.showContextSig, s.onVisibilityChanged)
	observeSignal(&s.subs, s.showTouchesSig, s.onVisibilityChanged)
	observeSignal(&s.subs, s.showRecentFilesSig, s.onVisibilityChanged)
	observeSignal(&s.subs, s.showExperimentSig, s.onVisibilityChanged)
	observeSignal(&s.subs, s.showRLMSig, s.onVisibilityChanged)
	observeSignal(&s.subs, s.showCircuitSig, s.onVisibilityChanged)
	observeSignal(&s.subs, s.showAgentsSig, s.onVisibilityChanged)
	observeSignal(&s.subs, s.showLocksSig, s.onVisibilityChanged)
	observeSignal(&s.subs, s.activeAgentsSig, s.onActiveAgentsChanged)
	observeSignal(&s.subs, s.fileLocksSig, s.onFileLocksChanged)

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
