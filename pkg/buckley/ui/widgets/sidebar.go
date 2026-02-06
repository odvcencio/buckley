package widgets

import (
	"strings"
	"time"

	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/scroll"
	"github.com/odvcencio/fluffyui/state"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// TaskStatus represents the status of a task in the plan.
type TaskStatus int

const (
	TaskPending TaskStatus = iota
	TaskInProgress
	TaskCompleted
	TaskFailed
)

// PlanTask represents a task in the plan section.
type PlanTask struct {
	Name   string
	Status TaskStatus
}

// RunningTool represents an active tool execution.
type RunningTool struct {
	ID      string
	Name    string
	Command string // Optional detail (e.g., shell command)
}

// ToolHistoryEntry represents a recent tool execution entry.
type ToolHistoryEntry struct {
	Name   string
	Status string
	Detail string
	When   time.Time
}

// TouchSummary represents an active touch on a file.
type TouchSummary struct {
	Path      string
	Operation string
	Ranges    []TouchRange
}

// TouchRange represents a 1-based inclusive range.
type TouchRange struct {
	Start int
	End   int
}

// ExperimentVariant summarizes an experiment variant.
type ExperimentVariant struct {
	ID               string
	Name             string
	ModelID          string
	Status           string
	DurationMs       int
	TotalCost        float64
	PromptTokens     int
	CompletionTokens int
}

// RLMStatus summarizes coordinator iteration status.
type RLMStatus struct {
	Iteration     int
	MaxIterations int
	Ready         bool
	TokensUsed    int
	Summary       string
}

// RLMScratchpadEntry summarizes a scratchpad entry.
type RLMScratchpadEntry struct {
	Key     string
	Type    string
	Summary string
}

// CircuitStatus summarizes circuit breaker health.
type CircuitStatus struct {
	State               string // "Closed", "Open", "HalfOpen"
	ConsecutiveFailures int
	MaxFailures         int
	LastError           string
	RetryAfterSecs      int
}

// AgentSummary represents an active machine agent for the sidebar.
type AgentSummary struct {
	ID       string
	State    string // machine state name (e.g., "calling_model", "executing_tools")
	Modality string // "classic", "rlm", "ralph"
	ParentID string // empty for root agents
	Task     string // description of what the agent is doing
}

// FileLockSummary represents a file lock held by an agent.
type FileLockSummary struct {
	Path   string
	Holder string // agent ID
	Mode   string // "read" or "write"
}

// SidebarConfig holds configurable options for the sidebar.
type SidebarConfig struct {
	// Width is the sidebar width in characters. Default 24, min 16, max 60.
	Width int
	// MinWidth is the minimum width when resizing. Default 16.
	MinWidth int
	// MaxWidth is the maximum width when resizing. Default 60.
	MaxWidth int
}

// SidebarBindings connects reactive state to the sidebar.
type SidebarBindings struct {
	CurrentTask        state.Readable[string]
	TaskProgress       state.Readable[int]
	PlanTasks          state.Readable[[]PlanTask]
	RunningTools       state.Readable[[]RunningTool]
	ToolHistory        state.Readable[[]ToolHistoryEntry]
	ActiveTouches      state.Readable[[]TouchSummary]
	RecentFiles        state.Readable[[]string]
	RLMStatus          state.Readable[*RLMStatus]
	RLMScratchpad      state.Readable[[]RLMScratchpadEntry]
	CircuitStatus      state.Readable[*CircuitStatus]
	Experiment         state.Readable[string]
	ExperimentStatus   state.Readable[string]
	ExperimentVariants state.Readable[[]ExperimentVariant]
	ActiveAgents       state.Readable[[]AgentSummary]
	FileLocks          state.Readable[[]FileLockSummary]
	ContextUsed        state.Readable[int]
	ContextBudget      state.Readable[int]
	ContextWindow      state.Readable[int]
	ProjectPath        state.Readable[string]
	Width              state.Readable[int]
	TabIndex           state.Readable[int]
	ShowCurrentTask    state.Readable[bool]
	ShowPlan           state.Readable[bool]
	ShowTools          state.Readable[bool]
	ShowContext        state.Readable[bool]
	ShowTouches        state.Readable[bool]
	ShowRecentFiles    state.Readable[bool]
	ShowExperiment     state.Readable[bool]
	ShowRLM            state.Readable[bool]
	ShowCircuit        state.Readable[bool]
	ShowAgents         state.Readable[bool]
	ShowLocks          state.Readable[bool]
}

// DefaultSidebarConfig returns sensible defaults.
func DefaultSidebarConfig() SidebarConfig {
	return SidebarConfig{
		Width:    24,
		MinWidth: 16,
		MaxWidth: 60,
	}
}

// Sidebar displays task progress, plan, and running tools.
type Sidebar struct {
	uiwidgets.FocusableBase

	config SidebarConfig

	services runtime.Services
	subs     state.Subscriptions

	currentTaskSig             state.Readable[string]
	taskProgressSig            state.Readable[int]
	planTasksSig               state.Readable[[]PlanTask]
	runningToolsSig            state.Readable[[]RunningTool]
	toolHistorySig             state.Readable[[]ToolHistoryEntry]
	activeTouchesSig           state.Readable[[]TouchSummary]
	recentFilesSig             state.Readable[[]string]
	rlmStatusSig               state.Readable[*RLMStatus]
	rlmScratchpadSig           state.Readable[[]RLMScratchpadEntry]
	circuitStatusSig           state.Readable[*CircuitStatus]
	experimentSig              state.Readable[string]
	experimentStatusSig        state.Readable[string]
	experimentVariantsSig      state.Readable[[]ExperimentVariant]
	contextUsedSig             state.Readable[int]
	contextBudgetSig           state.Readable[int]
	contextWindowSig           state.Readable[int]
	projectPathSig             state.Readable[string]
	widthSig                   state.Readable[int]
	tabIndexSig                state.Readable[int]
	showCurrentTaskSig         state.Readable[bool]
	showPlanSig                state.Readable[bool]
	showToolsSig               state.Readable[bool]
	showContextSig             state.Readable[bool]
	showTouchesSig             state.Readable[bool]
	showRecentFilesSig         state.Readable[bool]
	showExperimentSig          state.Readable[bool]
	showRLMSig                 state.Readable[bool]
	showCircuitSig             state.Readable[bool]
	ownedCurrentTaskSig        *state.Signal[string]
	ownedTaskProgressSig       *state.Signal[int]
	ownedPlanTasksSig          *state.Signal[[]PlanTask]
	ownedRunningToolsSig       *state.Signal[[]RunningTool]
	ownedToolHistorySig        *state.Signal[[]ToolHistoryEntry]
	ownedActiveTouchesSig      *state.Signal[[]TouchSummary]
	ownedRecentFilesSig        *state.Signal[[]string]
	ownedRLMStatusSig          *state.Signal[*RLMStatus]
	ownedRLMScratchpadSig      *state.Signal[[]RLMScratchpadEntry]
	ownedCircuitStatusSig      *state.Signal[*CircuitStatus]
	ownedExperimentSig         *state.Signal[string]
	ownedExperimentStatusSig   *state.Signal[string]
	ownedExperimentVariantsSig *state.Signal[[]ExperimentVariant]
	ownedContextUsed           *state.Signal[int]
	ownedContextBudget         *state.Signal[int]
	ownedContextWindow         *state.Signal[int]
	ownedProjectPath           *state.Signal[string]
	ownedWidthSig              *state.Signal[int]
	ownedTabIndexSig           *state.Signal[int]
	ownedShowTaskSig           *state.Signal[bool]
	ownedShowPlanSig           *state.Signal[bool]
	ownedShowToolsSig          *state.Signal[bool]
	ownedShowCtxSig            *state.Signal[bool]
	ownedShowTouchSig          *state.Signal[bool]
	ownedShowFilesSig          *state.Signal[bool]
	ownedShowExpSig            *state.Signal[bool]
	ownedShowRLMSig            *state.Signal[bool]
	ownedShowCircSig           *state.Signal[bool]
	activeAgentsSig            state.Readable[[]AgentSummary]
	fileLocksSig               state.Readable[[]FileLockSummary]
	showAgentsSig              state.Readable[bool]
	showLocksSig               state.Readable[bool]
	ownedActiveAgentsSig       *state.Signal[[]AgentSummary]
	ownedFileLocksSig          *state.Signal[[]FileLockSummary]
	ownedShowAgentsSig         *state.Signal[bool]
	ownedShowLocksSig          *state.Signal[bool]

	contextUpdateDepth int
	resizeHover        bool
	resizing           bool

	// Widgets
	tabs   *uiwidgets.Tabs
	status *sidebarStatus
	files  *sidebarFiles

	// Styles
	borderStyle     backend.Style
	headerStyle     backend.Style
	textStyle       backend.Style
	progressFull    backend.Style
	progressEmpty   backend.Style
	progressEdge    backend.Style
	completedStyle  backend.Style
	activeStyle     backend.Style
	pendingStyle    backend.Style
	failedStyle     backend.Style
	contextActive   backend.Style
	contextWarn     backend.Style
	contextCritical backend.Style
	contextMuted    backend.Style
	spinnerStyle    backend.Style
	bgStyle         backend.Style
}

// NewSidebar creates a new sidebar widget with default configuration.
func NewSidebar() *Sidebar {
	return NewSidebarWithConfig(DefaultSidebarConfig())
}

// NewSidebarWithConfig creates a new sidebar widget with the given configuration.
func NewSidebarWithConfig(cfg SidebarConfig) *Sidebar {
	return NewSidebarWithBindings(cfg, SidebarBindings{})
}

// NewSidebarWithBindings creates a new sidebar widget with bindings.
func NewSidebarWithBindings(cfg SidebarConfig, bindings SidebarBindings) *Sidebar {
	if cfg.Width < cfg.MinWidth {
		cfg.Width = cfg.MinWidth
	}
	if cfg.Width > cfg.MaxWidth {
		cfg.Width = cfg.MaxWidth
	}

	s := &Sidebar{
		config:          cfg,
		borderStyle:     backend.DefaultStyle(),
		headerStyle:     backend.DefaultStyle().Bold(true),
		textStyle:       backend.DefaultStyle(),
		progressFull:    backend.DefaultStyle().Foreground(backend.ColorGreen),
		progressEmpty:   backend.DefaultStyle().Foreground(backend.ColorDefault),
		progressEdge:    backend.DefaultStyle().Bold(true),
		completedStyle:  backend.DefaultStyle().Foreground(backend.ColorGreen),
		activeStyle:     backend.DefaultStyle().Foreground(backend.ColorYellow).Bold(true),
		pendingStyle:    backend.DefaultStyle().Foreground(backend.ColorDefault),
		failedStyle:     backend.DefaultStyle().Foreground(backend.ColorRed),
		contextActive:   backend.DefaultStyle().Foreground(backend.ColorGreen),
		contextWarn:     backend.DefaultStyle().Foreground(backend.ColorYellow),
		contextCritical: backend.DefaultStyle().Foreground(backend.ColorRed),
		contextMuted:    backend.DefaultStyle().Foreground(backend.ColorDefault),
		spinnerStyle:    backend.DefaultStyle(),
		bgStyle:         backend.DefaultStyle(),
	}
	if bindings.CurrentTask != nil {
		s.currentTaskSig = bindings.CurrentTask
	} else {
		s.ownedCurrentTaskSig = state.NewSignal("")
		s.currentTaskSig = s.ownedCurrentTaskSig
	}
	if bindings.TaskProgress != nil {
		s.taskProgressSig = bindings.TaskProgress
	} else {
		s.ownedTaskProgressSig = state.NewSignal(0)
		s.taskProgressSig = s.ownedTaskProgressSig
	}
	if bindings.PlanTasks != nil {
		s.planTasksSig = bindings.PlanTasks
	} else {
		s.ownedPlanTasksSig = state.NewSignal([]PlanTask(nil))
		s.planTasksSig = s.ownedPlanTasksSig
	}
	if bindings.RunningTools != nil {
		s.runningToolsSig = bindings.RunningTools
	} else {
		s.ownedRunningToolsSig = state.NewSignal([]RunningTool(nil))
		s.runningToolsSig = s.ownedRunningToolsSig
	}
	if bindings.ToolHistory != nil {
		s.toolHistorySig = bindings.ToolHistory
	} else {
		s.ownedToolHistorySig = state.NewSignal([]ToolHistoryEntry(nil))
		s.toolHistorySig = s.ownedToolHistorySig
	}
	if bindings.ActiveTouches != nil {
		s.activeTouchesSig = bindings.ActiveTouches
	} else {
		s.ownedActiveTouchesSig = state.NewSignal([]TouchSummary(nil))
		s.activeTouchesSig = s.ownedActiveTouchesSig
	}
	if bindings.RecentFiles != nil {
		s.recentFilesSig = bindings.RecentFiles
	} else {
		s.ownedRecentFilesSig = state.NewSignal([]string(nil))
		s.recentFilesSig = s.ownedRecentFilesSig
	}
	if bindings.RLMStatus != nil {
		s.rlmStatusSig = bindings.RLMStatus
	} else {
		s.ownedRLMStatusSig = state.NewSignal[*RLMStatus](nil)
		s.rlmStatusSig = s.ownedRLMStatusSig
	}
	if bindings.RLMScratchpad != nil {
		s.rlmScratchpadSig = bindings.RLMScratchpad
	} else {
		s.ownedRLMScratchpadSig = state.NewSignal([]RLMScratchpadEntry(nil))
		s.rlmScratchpadSig = s.ownedRLMScratchpadSig
	}
	if bindings.CircuitStatus != nil {
		s.circuitStatusSig = bindings.CircuitStatus
	} else {
		s.ownedCircuitStatusSig = state.NewSignal[*CircuitStatus](nil)
		s.circuitStatusSig = s.ownedCircuitStatusSig
	}
	if bindings.Experiment != nil {
		s.experimentSig = bindings.Experiment
	} else {
		s.ownedExperimentSig = state.NewSignal("")
		s.experimentSig = s.ownedExperimentSig
	}
	if bindings.ExperimentStatus != nil {
		s.experimentStatusSig = bindings.ExperimentStatus
	} else {
		s.ownedExperimentStatusSig = state.NewSignal("")
		s.experimentStatusSig = s.ownedExperimentStatusSig
	}
	if bindings.ExperimentVariants != nil {
		s.experimentVariantsSig = bindings.ExperimentVariants
	} else {
		s.ownedExperimentVariantsSig = state.NewSignal([]ExperimentVariant(nil))
		s.experimentVariantsSig = s.ownedExperimentVariantsSig
	}
	if bindings.ContextUsed != nil {
		s.contextUsedSig = bindings.ContextUsed
	} else {
		s.ownedContextUsed = state.NewSignal(0)
		s.contextUsedSig = s.ownedContextUsed
	}
	if bindings.ContextBudget != nil {
		s.contextBudgetSig = bindings.ContextBudget
	} else {
		s.ownedContextBudget = state.NewSignal(0)
		s.contextBudgetSig = s.ownedContextBudget
	}
	if bindings.ContextWindow != nil {
		s.contextWindowSig = bindings.ContextWindow
	} else {
		s.ownedContextWindow = state.NewSignal(0)
		s.contextWindowSig = s.ownedContextWindow
	}
	if bindings.ProjectPath != nil {
		s.projectPathSig = bindings.ProjectPath
	} else {
		s.ownedProjectPath = state.NewSignal("")
		s.projectPathSig = s.ownedProjectPath
	}
	if bindings.Width != nil {
		s.widthSig = bindings.Width
	} else {
		s.ownedWidthSig = state.NewSignal(cfg.Width)
		s.widthSig = s.ownedWidthSig
	}
	if bindings.TabIndex != nil {
		s.tabIndexSig = bindings.TabIndex
	} else {
		s.ownedTabIndexSig = state.NewSignal(0)
		s.tabIndexSig = s.ownedTabIndexSig
	}
	if bindings.ShowCurrentTask != nil {
		s.showCurrentTaskSig = bindings.ShowCurrentTask
	} else {
		s.ownedShowTaskSig = state.NewSignal(true)
		s.showCurrentTaskSig = s.ownedShowTaskSig
	}
	if bindings.ShowPlan != nil {
		s.showPlanSig = bindings.ShowPlan
	} else {
		s.ownedShowPlanSig = state.NewSignal(true)
		s.showPlanSig = s.ownedShowPlanSig
	}
	if bindings.ShowTools != nil {
		s.showToolsSig = bindings.ShowTools
	} else {
		s.ownedShowToolsSig = state.NewSignal(true)
		s.showToolsSig = s.ownedShowToolsSig
	}
	if bindings.ShowContext != nil {
		s.showContextSig = bindings.ShowContext
	} else {
		s.ownedShowCtxSig = state.NewSignal(true)
		s.showContextSig = s.ownedShowCtxSig
	}
	if bindings.ShowTouches != nil {
		s.showTouchesSig = bindings.ShowTouches
	} else {
		s.ownedShowTouchSig = state.NewSignal(true)
		s.showTouchesSig = s.ownedShowTouchSig
	}
	if bindings.ShowRecentFiles != nil {
		s.showRecentFilesSig = bindings.ShowRecentFiles
	} else {
		s.ownedShowFilesSig = state.NewSignal(true)
		s.showRecentFilesSig = s.ownedShowFilesSig
	}
	if bindings.ShowExperiment != nil {
		s.showExperimentSig = bindings.ShowExperiment
	} else {
		s.ownedShowExpSig = state.NewSignal(true)
		s.showExperimentSig = s.ownedShowExpSig
	}
	if bindings.ShowRLM != nil {
		s.showRLMSig = bindings.ShowRLM
	} else {
		s.ownedShowRLMSig = state.NewSignal(true)
		s.showRLMSig = s.ownedShowRLMSig
	}
	if bindings.ShowCircuit != nil {
		s.showCircuitSig = bindings.ShowCircuit
	} else {
		s.ownedShowCircSig = state.NewSignal(true)
		s.showCircuitSig = s.ownedShowCircSig
	}
	if bindings.ActiveAgents != nil {
		s.activeAgentsSig = bindings.ActiveAgents
	} else {
		s.ownedActiveAgentsSig = state.NewSignal([]AgentSummary(nil))
		s.activeAgentsSig = s.ownedActiveAgentsSig
	}
	if bindings.FileLocks != nil {
		s.fileLocksSig = bindings.FileLocks
	} else {
		s.ownedFileLocksSig = state.NewSignal([]FileLockSummary(nil))
		s.fileLocksSig = s.ownedFileLocksSig
	}
	if bindings.ShowAgents != nil {
		s.showAgentsSig = bindings.ShowAgents
	} else {
		s.ownedShowAgentsSig = state.NewSignal(true)
		s.showAgentsSig = s.ownedShowAgentsSig
	}
	if bindings.ShowLocks != nil {
		s.showLocksSig = bindings.ShowLocks
	} else {
		s.ownedShowLocksSig = state.NewSignal(true)
		s.showLocksSig = s.ownedShowLocksSig
	}

	s.initWidgets()
	s.updateAllPanels()
	s.subscribe()
	return s
}

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

func (s *Sidebar) applyContextUsage(used, budget, window int) {
	if s.status != nil {
		s.status.ApplyContextUsage(used, budget, window)
	}
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
}

// SetStyles configures the sidebar appearance.
func (s *Sidebar) SetStyles(border, header, text, progressFull, progressEmpty, background backend.Style) {
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
	s.progressEdge = style
}

// SetStatusStyles configures styles for status indicators.
func (s *Sidebar) SetStatusStyles(completed, active, pending, failed backend.Style) {
	s.completedStyle = completed
	s.activeStyle = active
	s.pendingStyle = pending
	s.failedStyle = failed
}

// SetContextStyles configures styles for context usage indicators.
func (s *Sidebar) SetContextStyles(active, warn, critical, muted backend.Style) {
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

// Measure returns the preferred size.
func (s *Sidebar) Measure(constraints runtime.Constraints) runtime.Size {
	width := s.config.Width
	if width <= 0 {
		width = 24
	}
	if constraints.MaxWidth < width {
		width = constraints.MaxWidth
	}
	return runtime.Size{Width: width, Height: constraints.MaxHeight}
}

// SetWidth changes the sidebar width within configured min/max bounds.
func (s *Sidebar) SetWidth(width int) {
	if s == nil {
		return
	}
	if s.widthSig != nil {
		s.writeWidthSignal(width)
		return
	}
	if s.setWidthLocal(width) {
		s.requestRelayout()
	}
}

func (s *Sidebar) normalizeWidth(width int) int {
	if width < s.config.MinWidth {
		width = s.config.MinWidth
	}
	if width > s.config.MaxWidth {
		width = s.config.MaxWidth
	}
	return width
}

func (s *Sidebar) setWidthLocal(width int) bool {
	if s == nil {
		return false
	}
	width = s.normalizeWidth(width)
	if s.config.Width == width {
		return false
	}
	s.config.Width = width
	return true
}

func (s *Sidebar) writeWidthSignal(width int) {
	if s == nil || s.widthSig == nil {
		return
	}
	width = s.normalizeWidth(width)
	if writable, ok := s.widthSig.(state.Writable[int]); ok && writable != nil {
		writable.Set(width)
	}
}

// Width returns the current sidebar width.
func (s *Sidebar) Width() int {
	return s.config.Width
}

// SetSelectedTab selects the sidebar tab index.
func (s *Sidebar) SetSelectedTab(index int) {
	if s == nil {
		return
	}
	s.writeTabIndex(index)
}

func (s *Sidebar) normalizeTabIndex(index int) int {
	if s.tabs == nil || len(s.tabs.Tabs) == 0 {
		return 0
	}
	if index < 0 {
		return 0
	}
	if index >= len(s.tabs.Tabs) {
		return len(s.tabs.Tabs) - 1
	}
	return index
}

func (s *Sidebar) writeTabIndex(index int) {
	if s == nil || s.tabIndexSig == nil {
		return
	}
	index = s.normalizeTabIndex(index)
	if writable, ok := s.tabIndexSig.(state.Writable[int]); ok && writable != nil {
		writable.Set(index)
	}
}

func (s *Sidebar) applyTabIndex(index int) {
	if s.tabs == nil {
		return
	}
	normalized := s.normalizeTabIndex(index)
	if normalized != index {
		s.writeTabIndex(normalized)
	}
	if s.tabs.SelectedIndex() != normalized {
		s.tabs.SetSelected(normalized)
	}
}

// Grow increases the sidebar width by delta characters.
func (s *Sidebar) Grow(delta int) {
	s.SetWidth(s.config.Width + delta)
}

// Shrink decreases the sidebar width by delta characters.
func (s *Sidebar) Shrink(delta int) {
	s.SetWidth(s.config.Width - delta)
}

// Layout stores the assigned bounds.
func (s *Sidebar) Layout(bounds runtime.Rect) {
	s.FocusableBase.Layout(bounds)
	if s.tabs != nil {
		s.tabs.Layout(bounds)
	}
}

// Render draws the sidebar.
func (s *Sidebar) Render(ctx runtime.RenderContext) {
	if s.tabs == nil {
		return
	}
	bounds := s.Bounds()
	if bounds.Width <= 0 || bounds.Height <= 0 {
		return
	}
	s.tabs.Render(ctx)
	borderStyle := s.borderStyle
	if s.resizeHover || s.resizing {
		borderStyle = borderStyle.Bold(true)
	}
	for y := bounds.Y; y < bounds.Y+bounds.Height; y++ {
		ctx.Buffer.Set(bounds.X, y, '│', borderStyle)
	}
}

// HandleMessage processes sidebar input.
func (s *Sidebar) HandleMessage(msg runtime.Message) runtime.HandleResult {
	if s.tabs == nil {
		return runtime.Unhandled()
	}
	if mouse, ok := msg.(runtime.MouseMsg); ok {
		if s.handleResizeMouse(mouse) {
			return runtime.Handled()
		}
		if mouse.Action == runtime.MousePress && mouse.Button == runtime.MouseLeft {
			if s.handleTabClick(mouse.X, mouse.Y) {
				return runtime.Handled()
			}
		}
	}
	before := s.tabs.SelectedIndex()
	result := s.tabs.HandleMessage(msg)
	if result.Handled {
		after := s.tabs.SelectedIndex()
		if after != before {
			s.writeTabIndex(after)
		}
	}
	return result
}

func (s *Sidebar) handleTabClick(x, y int) bool {
	if s == nil || s.tabs == nil {
		return false
	}
	content := s.tabs.ContentBounds()
	if content.Width <= 0 || content.Height <= 0 {
		return false
	}
	if y != content.Y {
		return false
	}
	if x < content.X || x >= content.X+content.Width {
		return false
	}
	cursor := content.X
	maxX := content.X + content.Width
	for i, tab := range s.tabs.Tabs {
		label := " " + tab.Title + " "
		labelWidth := len([]rune(label))
		if cursor >= maxX {
			break
		}
		if cursor+labelWidth > maxX {
			labelWidth = max(0, maxX-cursor)
		}
		if x >= cursor && x < cursor+labelWidth {
			s.writeTabIndex(i)
			return true
		}
		cursor += labelWidth
	}
	return false
}

func (s *Sidebar) handleResizeMouse(mouse runtime.MouseMsg) bool {
	if s == nil {
		return false
	}
	switch mouse.Action {
	case runtime.MousePress:
		if mouse.Button != runtime.MouseLeft {
			return false
		}
		if !s.resizeHandleHit(mouse.X, mouse.Y) {
			return false
		}
		s.resizing = true
		s.resizeHover = true
		s.updateWidthFromMouse(mouse.X)
		s.requestInvalidate()
		return true
	case runtime.MouseMove:
		if s.resizing {
			s.updateWidthFromMouse(mouse.X)
			return true
		}
		hover := s.resizeHandleHit(mouse.X, mouse.Y)
		if hover != s.resizeHover {
			s.resizeHover = hover
			s.requestInvalidate()
		}
		return false
	case runtime.MouseRelease:
		if mouse.Button != runtime.MouseLeft {
			return false
		}
		if !s.resizing {
			return false
		}
		s.resizing = false
		s.resizeHover = s.resizeHandleHit(mouse.X, mouse.Y)
		s.requestInvalidate()
		return true
	default:
		return false
	}
}

func (s *Sidebar) resizeHandleHit(x, y int) bool {
	bounds := s.Bounds()
	if bounds.Width <= 0 || bounds.Height <= 0 {
		return false
	}
	if y < bounds.Y || y >= bounds.Y+bounds.Height {
		return false
	}
	return x == bounds.X
}

func (s *Sidebar) updateWidthFromMouse(x int) {
	bounds := s.Bounds()
	if bounds.Width <= 0 {
		return
	}
	newWidth := bounds.X + bounds.Width - x
	s.SetWidth(newWidth)
}

func (s *Sidebar) activeScrollView() *uiwidgets.ScrollView {
	if s == nil || s.tabs == nil {
		return nil
	}
	switch s.tabs.SelectedIndex() {
	case 0:
		if s.status != nil {
			return s.status.ScrollView()
		}
	case 1:
		if s.files != nil {
			return s.files.ScrollView()
		}
	}
	return nil
}

// ScrollBy scrolls the active sidebar panel.
func (s *Sidebar) ScrollBy(dx, dy int) {
	if view := s.activeScrollView(); view != nil {
		view.ScrollBy(dx, dy)
	}
}

// ScrollTo scrolls the active sidebar panel to an offset.
func (s *Sidebar) ScrollTo(x, y int) {
	if view := s.activeScrollView(); view != nil {
		view.ScrollTo(x, y)
	}
}

// PageBy scrolls the active sidebar panel by page count.
func (s *Sidebar) PageBy(pages int) {
	if view := s.activeScrollView(); view != nil {
		view.PageBy(pages)
	}
}

// ScrollToStart scrolls the active sidebar panel to the start.
func (s *Sidebar) ScrollToStart() {
	if view := s.activeScrollView(); view != nil {
		view.ScrollToStart()
	}
}

// ScrollToEnd scrolls the active sidebar panel to the end.
func (s *Sidebar) ScrollToEnd() {
	if view := s.activeScrollView(); view != nil {
		view.ScrollToEnd()
	}
}

// ChildWidgets returns child widgets for traversal.
func (s *Sidebar) ChildWidgets() []runtime.Widget {
	if s.tabs == nil {
		return nil
	}
	return []runtime.Widget{s.tabs}
}

// WebLinkAt returns a sidebar web link hit if the point is inside one.
func (s *Sidebar) WebLinkAt(x, y int) (string, bool) {
	return "", false
}

func (s *Sidebar) updateAllPanels() {
	if s.status != nil {
		s.status.updateAllPanels()
	}
	if s.files != nil {
		s.files.updateAllPanels()
	}
}

var _ runtime.Widget = (*Sidebar)(nil)
var _ runtime.ChildProvider = (*Sidebar)(nil)
var _ runtime.Bindable = (*Sidebar)(nil)
var _ runtime.Unbindable = (*Sidebar)(nil)
var _ scroll.Controller = (*Sidebar)(nil)
