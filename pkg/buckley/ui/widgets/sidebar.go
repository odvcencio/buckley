package widgets

import (
	"time"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
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
