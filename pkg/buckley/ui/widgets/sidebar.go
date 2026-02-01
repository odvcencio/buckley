package widgets

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/odvcencio/fluffyui/accessibility"
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

// SidebarConfig holds configurable options for the sidebar.
type SidebarConfig struct {
	// Width is the sidebar width in characters. Default 24, min 16, max 60.
	Width int
	// MinWidth is the minimum width when resizing. Default 16.
	MinWidth int
	// MaxWidth is the maximum width when resizing. Default 60.
	MaxWidth int
}

// SidebarState captures dynamic sidebar data for reactive bindings.
type SidebarState struct {
	CurrentTask        string
	TaskProgress       int
	PlanTasks          []PlanTask
	RunningTools       []RunningTool
	ToolHistory        []ToolHistoryEntry
	ActiveTouches      []TouchSummary
	RecentFiles        []string
	RLMStatus          *RLMStatus
	RLMScratchpad      []RLMScratchpadEntry
	CircuitStatus      *CircuitStatus
	Experiment         string
	ExperimentStatus   string
	ExperimentVariants []ExperimentVariant
}

// SidebarBindings connects reactive state to the sidebar.
type SidebarBindings struct {
	State           state.Readable[SidebarState]
	ContextUsed     state.Readable[int]
	ContextBudget   state.Readable[int]
	ContextWindow   state.Readable[int]
	ShowCurrentTask state.Readable[bool]
	ShowPlan        state.Readable[bool]
	ShowTools       state.Readable[bool]
	ShowContext     state.Readable[bool]
	ShowTouches     state.Readable[bool]
	ShowRecentFiles state.Readable[bool]
	ShowExperiment  state.Readable[bool]
	ShowRLM         state.Readable[bool]
	ShowCircuit     state.Readable[bool]
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

	stateSig           state.Readable[SidebarState]
	contextUsedSig     state.Readable[int]
	contextBudgetSig   state.Readable[int]
	contextWindowSig   state.Readable[int]
	showCurrentTaskSig state.Readable[bool]
	showPlanSig        state.Readable[bool]
	showToolsSig       state.Readable[bool]
	showContextSig     state.Readable[bool]
	showTouchesSig     state.Readable[bool]
	showRecentFilesSig state.Readable[bool]
	showExperimentSig  state.Readable[bool]
	showRLMSig         state.Readable[bool]
	showCircuitSig     state.Readable[bool]

	currentTask     string
	taskProgress    int
	showCurrentTask bool

	planTasks []PlanTask
	showPlan  bool

	runningTools []RunningTool
	toolHistory  []ToolHistoryEntry
	showTools    bool

	contextUsed    int
	contextBudget  int
	contextWindow  int
	contextHistory []float64
	contextMax     int
	showContext    bool

	activeTouches []TouchSummary
	showTouches   bool

	recentFiles     []string
	showRecentFiles bool

	experimentName     string
	experimentStatus   string
	experimentVariants []ExperimentVariant
	showExperiment     bool

	rlmStatus     *RLMStatus
	rlmScratchpad []RLMScratchpadEntry
	showRLM       bool

	circuitStatus *CircuitStatus
	showCircuit   bool

	projectPath string

	// Widgets
	tabs          *uiwidgets.Tabs
	statusScroll  *uiwidgets.ScrollView
	filesScroll   *uiwidgets.ScrollView
	statusContent *runtime.Flex
	filesContent  *runtime.Flex

	taskPanel       *taskPanel
	planPanel       *planPanel
	toolsPanel      *toolsPanel
	contextPanel    *contextPanel
	experimentPanel *experimentPanel
	rlmPanel        *rlmPanel
	circuitPanel    *circuitPanel
	calendarPanel   *calendarPanel
	breadcrumbPanel *breadcrumbPanel
	filesPanel      *filesPanel
	touchesPanel    *touchesPanel
	spinnerFrame    int

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
		showCurrentTask: true,
		showPlan:        true,
		showTools:       true,
		showContext:     true,
		showTouches:     true,
		showRecentFiles: true,
		showExperiment:  true,
		showRLM:         true,
		contextMax:      12,
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
	s.stateSig = bindings.State
	s.contextUsedSig = bindings.ContextUsed
	s.contextBudgetSig = bindings.ContextBudget
	s.contextWindowSig = bindings.ContextWindow
	s.showCurrentTaskSig = bindings.ShowCurrentTask
	s.showPlanSig = bindings.ShowPlan
	s.showToolsSig = bindings.ShowTools
	s.showContextSig = bindings.ShowContext
	s.showTouchesSig = bindings.ShowTouches
	s.showRecentFilesSig = bindings.ShowRecentFiles
	s.showExperimentSig = bindings.ShowExperiment
	s.showRLMSig = bindings.ShowRLM
	s.showCircuitSig = bindings.ShowCircuit

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
	if s.stateSig != nil {
		s.subs.Observe(s.stateSig, s.onStateChanged)
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
	s.onStateChanged()
	s.onContextChanged()
	s.onVisibilityChanged()
}

func (s *Sidebar) onStateChanged() {
	if s.stateSig == nil {
		return
	}
	s.applyState(s.stateSig.Get())
	s.requestRelayout()
}

func (s *Sidebar) onContextChanged() {
	if s.contextUsedSig == nil && s.contextBudgetSig == nil && s.contextWindowSig == nil {
		return
	}
	s.applyContext()
	s.requestInvalidate()
}

func (s *Sidebar) onVisibilityChanged() {
	statusChanged, filesChanged := s.applyVisibility()
	if statusChanged {
		s.rebuildStatus()
	}
	if filesChanged {
		s.rebuildFiles()
	}
	if statusChanged || filesChanged {
		s.requestRelayout()
	}
}

func (s *Sidebar) applyState(state SidebarState) {
	s.SetCurrentTask(state.CurrentTask, state.TaskProgress)
	s.SetPlanTasks(state.PlanTasks)
	s.SetRunningTools(state.RunningTools)
	s.SetToolHistory(state.ToolHistory)
	s.SetActiveTouches(state.ActiveTouches)
	s.SetRecentFiles(state.RecentFiles)
	s.SetExperiment(state.Experiment, state.ExperimentStatus, state.ExperimentVariants)
	s.SetRLMStatus(state.RLMStatus, state.RLMScratchpad)
	s.SetCircuitStatus(state.CircuitStatus)
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
	s.SetContextUsage(used, budget, window)
}

func (s *Sidebar) applyVisibility() (bool, bool) {
	statusChanged := s.applyVisibilityFlag(s.showCurrentTaskSig, &s.showCurrentTask)
	if s.applyVisibilityFlag(s.showPlanSig, &s.showPlan) {
		statusChanged = true
	}
	if s.applyVisibilityFlag(s.showToolsSig, &s.showTools) {
		statusChanged = true
	}
	if s.applyVisibilityFlag(s.showContextSig, &s.showContext) {
		statusChanged = true
	}
	if s.applyVisibilityFlag(s.showExperimentSig, &s.showExperiment) {
		statusChanged = true
	}
	if s.applyVisibilityFlag(s.showRLMSig, &s.showRLM) {
		statusChanged = true
	}
	if s.applyVisibilityFlag(s.showCircuitSig, &s.showCircuit) {
		statusChanged = true
	}

	filesChanged := s.applyVisibilityFlag(s.showRecentFilesSig, &s.showRecentFiles)
	if s.applyVisibilityFlag(s.showTouchesSig, &s.showTouches) {
		filesChanged = true
	}
	return statusChanged, filesChanged
}

func (s *Sidebar) applyVisibilityFlag(sig state.Readable[bool], target *bool) bool {
	if sig == nil || target == nil {
		return false
	}
	next := sig.Get()
	if *target == next {
		return false
	}
	*target = next
	return true
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
	s.taskPanel = newTaskPanel(s.borderStyle)
	s.planPanel = newPlanPanel(s.borderStyle)
	s.toolsPanel = newToolsPanel(s.borderStyle)
	s.contextPanel = newContextPanel(s.borderStyle)

	s.experimentPanel = newExperimentPanel(s.borderStyle)
	s.rlmPanel = newRLMPanel(s.borderStyle)
	s.circuitPanel = newCircuitPanel(s.borderStyle)
	s.calendarPanel = newCalendarPanel(s.borderStyle)
	s.breadcrumbPanel = newBreadcrumbPanel(s.borderStyle)
	s.filesPanel = newFilesPanel(s.borderStyle)
	s.touchesPanel = newTouchesPanel(s.borderStyle)

	s.statusContent = s.buildStatusContent()
	s.filesContent = s.buildFilesContent()
	s.statusScroll = uiwidgets.NewScrollView(s.statusContent)
	s.filesScroll = uiwidgets.NewScrollView(s.filesContent)
	s.tabs = uiwidgets.NewTabs(
		uiwidgets.Tab{Title: "Status", Content: s.statusScroll},
		uiwidgets.Tab{Title: "Files", Content: s.filesScroll},
	)
	s.Base.Role = accessibility.RoleGroup
}

func (s *Sidebar) buildStatusContent() *runtime.Flex {
	children := make([]runtime.FlexChild, 0, 8)
	if s.showCurrentTask {
		if panel := s.taskPanel.Panel(); panel != nil {
			children = append(children, runtime.Fixed(panel))
		}
	}
	if s.showPlan {
		if panel := s.planPanel.Panel(); panel != nil {
			children = append(children, runtime.Fixed(panel))
		}
	}
	if s.showTools {
		if panel := s.toolsPanel.Panel(); panel != nil {
			children = append(children, runtime.Fixed(panel))
		}
	}
	if s.showContext {
		if panel := s.contextPanel.Panel(); panel != nil {
			children = append(children, runtime.Fixed(panel))
		}
	}
	if s.showExperiment {
		if panel := s.experimentPanel.Panel(); panel != nil {
			children = append(children, runtime.Fixed(panel))
		}
	}
	if s.showRLM {
		if panel := s.rlmPanel.Panel(); panel != nil {
			children = append(children, runtime.Fixed(panel))
		}
	}
	if s.showCircuit {
		if panel := s.circuitPanel.Panel(); panel != nil {
			children = append(children, runtime.Fixed(panel))
		}
	}
	if panel := s.calendarPanel.Panel(); panel != nil {
		children = append(children, runtime.Fixed(panel))
	}
	return runtime.VBox(children...).WithGap(1)
}

func (s *Sidebar) buildFilesContent() *runtime.Flex {
	children := make([]runtime.FlexChild, 0, 4)
	if panel := s.breadcrumbPanel.Panel(); panel != nil {
		children = append(children, runtime.Fixed(panel))
	}
	if s.showRecentFiles {
		if panel := s.filesPanel.Panel(); panel != nil {
			children = append(children, runtime.Fixed(panel))
		}
	}
	if s.showTouches {
		if panel := s.touchesPanel.Panel(); panel != nil {
			children = append(children, runtime.Fixed(panel))
		}
	}
	return runtime.VBox(children...).WithGap(1)
}

// SetStyles configures the sidebar appearance.
func (s *Sidebar) SetStyles(border, header, text, progressFull, progressEmpty, background backend.Style) {
	s.borderStyle = border
	s.headerStyle = header
	s.textStyle = text
	s.progressFull = progressFull
	s.progressEmpty = progressEmpty
	s.bgStyle = background

	if s.taskPanel != nil {
		s.taskPanel.SetStyles(border, background, text)
	}
	if s.planPanel != nil {
		s.planPanel.SetStyles(border, background)
	}
	if s.toolsPanel != nil {
		s.toolsPanel.SetStyles(border, background)
	}
	if s.contextPanel != nil {
		s.contextPanel.SetStyles(border, background, text)
	}
	if s.experimentPanel != nil {
		s.experimentPanel.SetStyles(border, background)
	}
	if s.rlmPanel != nil {
		s.rlmPanel.SetStyles(border, background)
	}
	if s.circuitPanel != nil {
		s.circuitPanel.SetStyles(border, background)
	}
	if s.calendarPanel != nil {
		s.calendarPanel.SetStyles(border, background)
	}
	if s.breadcrumbPanel != nil {
		s.breadcrumbPanel.SetStyles(border, background)
	}
	if s.filesPanel != nil {
		s.filesPanel.SetStyles(border, background)
	}
	if s.touchesPanel != nil {
		s.touchesPanel.SetStyles(border, background)
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
	if s.contextPanel != nil {
		s.contextPanel.SetGaugeStyle(uiwidgets.GaugeStyle{
			EmptyStyle: muted,
			Thresholds: []uiwidgets.GaugeThreshold{
				{Ratio: 0.7, Style: warn},
				{Ratio: 0.9, Style: critical},
			},
		})
	}
}

// SetSpinnerStyle configures the spinner style.
func (s *Sidebar) SetSpinnerStyle(style backend.Style) {
	s.spinnerStyle = style
	if s.taskPanel != nil {
		s.taskPanel.SetSpinnerStyle(style)
	}
}

// HasContent returns true when any sidebar section has data to render.
func (s *Sidebar) HasContent() bool {
	if strings.TrimSpace(s.currentTask) != "" {
		return true
	}
	if len(s.planTasks) > 0 {
		return true
	}
	if len(s.runningTools) > 0 || len(s.toolHistory) > 0 {
		return true
	}
	if s.contextUsed > 0 || s.contextBudget > 0 || s.contextWindow > 0 {
		return true
	}
	if strings.TrimSpace(s.experimentName) != "" || strings.TrimSpace(s.experimentStatus) != "" || len(s.experimentVariants) > 0 {
		return true
	}
	if s.rlmStatus != nil || len(s.rlmScratchpad) > 0 {
		return true
	}
	if s.circuitStatus != nil {
		return true
	}
	if len(s.activeTouches) > 0 {
		return true
	}
	if len(s.recentFiles) > 0 {
		return true
	}
	return false
}

// SetProjectPath updates breadcrumb path display.
func (s *Sidebar) SetProjectPath(path string) {
	s.projectPath = strings.TrimSpace(path)
	s.updateBreadcrumb()
}

// SetCurrentTask updates the current task display.
func (s *Sidebar) SetCurrentTask(name string, progress int) {
	s.currentTask = name
	s.taskProgress = clampPercent(progress)
	s.updateTaskPanel()
}

func (s *Sidebar) setVisibility(sig state.Readable[bool], target *bool, show bool, rebuild func()) {
	if s == nil {
		return
	}
	if sig != nil {
		if writable, ok := sig.(state.Writable[bool]); ok && writable != nil {
			writable.Set(show)
		}
		return
	}
	if target == nil || *target == show {
		return
	}
	*target = show
	if rebuild != nil {
		rebuild()
		s.requestRelayout()
	}
}

// SetShowCurrentTask controls visibility of current task section.
func (s *Sidebar) SetShowCurrentTask(show bool) {
	s.setVisibility(s.showCurrentTaskSig, &s.showCurrentTask, show, s.rebuildStatus)
}

// ToggleCurrentTask toggles the current task section visibility.
func (s *Sidebar) ToggleCurrentTask() {
	s.SetShowCurrentTask(!s.showCurrentTask)
}

// SetPlanTasks updates the plan task list.
func (s *Sidebar) SetPlanTasks(tasks []PlanTask) {
	s.planTasks = tasks
	s.updatePlanPanel()
}

// SetShowPlan controls visibility of plan section.
func (s *Sidebar) SetShowPlan(show bool) {
	s.setVisibility(s.showPlanSig, &s.showPlan, show, s.rebuildStatus)
}

// TogglePlan toggles the plan section visibility.
func (s *Sidebar) TogglePlan() {
	s.SetShowPlan(!s.showPlan)
}

// SetRunningTools updates the running tools list.
func (s *Sidebar) SetRunningTools(tools []RunningTool) {
	s.runningTools = tools
	s.updateToolsPanel()
}

// SetToolHistory updates recent tool history entries.
func (s *Sidebar) SetToolHistory(history []ToolHistoryEntry) {
	s.toolHistory = history
	s.updateToolsPanel()
}

// SetShowTools controls visibility of tools section.
func (s *Sidebar) SetShowTools(show bool) {
	s.setVisibility(s.showToolsSig, &s.showTools, show, s.rebuildStatus)
}

// ToggleTools toggles the tools section visibility.
func (s *Sidebar) ToggleTools() {
	s.SetShowTools(!s.showTools)
}

// SetContextUsage updates context usage values.
func (s *Sidebar) SetContextUsage(used, budget, window int) {
	s.contextUsed = used
	s.contextBudget = budget
	s.contextWindow = window
	if budget > 0 || window > 0 {
		ratio := s.contextRatio()
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		s.contextHistory = append(s.contextHistory, ratio)
		if s.contextMax > 0 && len(s.contextHistory) > s.contextMax {
			s.contextHistory = s.contextHistory[len(s.contextHistory)-s.contextMax:]
		}
	}
	s.updateContextPanel()
}

// SetShowContext controls visibility of context section.
func (s *Sidebar) SetShowContext(show bool) {
	s.setVisibility(s.showContextSig, &s.showContext, show, s.rebuildStatus)
}

// ToggleContext toggles the context section visibility.
func (s *Sidebar) ToggleContext() {
	s.SetShowContext(!s.showContext)
}

// SetActiveTouches updates the active touches list.
func (s *Sidebar) SetActiveTouches(touches []TouchSummary) {
	s.activeTouches = touches
	s.updateTouchesPanel()
}

// SetShowTouches controls visibility of touches section.
func (s *Sidebar) SetShowTouches(show bool) {
	s.setVisibility(s.showTouchesSig, &s.showTouches, show, s.rebuildFiles)
}

// ToggleTouches toggles the touches section visibility.
func (s *Sidebar) ToggleTouches() {
	s.SetShowTouches(!s.showTouches)
}

// SetRecentFiles updates the recent files list.
func (s *Sidebar) SetRecentFiles(files []string) {
	s.recentFiles = files
	s.updateFilesPanel()
}

// SetShowRecentFiles controls visibility of recent files section.
func (s *Sidebar) SetShowRecentFiles(show bool) {
	s.setVisibility(s.showRecentFilesSig, &s.showRecentFiles, show, s.rebuildFiles)
}

// SetExperiment updates the experiment summary.
func (s *Sidebar) SetExperiment(name, status string, variants []ExperimentVariant) {
	s.experimentName = name
	s.experimentStatus = status
	s.experimentVariants = variants
	s.updateExperimentPanel()
}

// SetRLMStatus updates the RLM iteration status and scratchpad summaries.
func (s *Sidebar) SetRLMStatus(status *RLMStatus, scratchpad []RLMScratchpadEntry) {
	s.rlmStatus = status
	if scratchpad == nil {
		s.rlmScratchpad = nil
	} else {
		s.rlmScratchpad = append([]RLMScratchpadEntry{}, scratchpad...)
	}
	s.updateRLMPanel()
}

// SetCircuitStatus updates the circuit breaker status display.
func (s *Sidebar) SetCircuitStatus(status *CircuitStatus) {
	s.circuitStatus = status
	s.updateCircuitPanel()
}

// SetShowCircuit controls visibility of circuit breaker section.
func (s *Sidebar) SetShowCircuit(show bool) {
	s.setVisibility(s.showCircuitSig, &s.showCircuit, show, s.rebuildStatus)
}

// SetShowRLM controls visibility of the RLM section.
func (s *Sidebar) SetShowRLM(show bool) {
	s.setVisibility(s.showRLMSig, &s.showRLM, show, s.rebuildStatus)
}

// SetShowExperiment controls visibility of experiments section.
func (s *Sidebar) SetShowExperiment(show bool) {
	s.setVisibility(s.showExperimentSig, &s.showExperiment, show, s.rebuildStatus)
}

// SetSpinnerFrame updates the spinner animation frame.
func (s *Sidebar) SetSpinnerFrame(frame int) {
	if frame < 0 {
		frame = 0
	}
	if s.taskPanel != nil && frame != s.spinnerFrame {
		s.taskPanel.AdvanceSpinner()
	}
	s.spinnerFrame = frame
}

// ToggleRecentFiles toggles the recent files section.
func (s *Sidebar) ToggleRecentFiles() {
	s.SetShowRecentFiles(!s.showRecentFiles)
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
	if width < s.config.MinWidth {
		width = s.config.MinWidth
	}
	if width > s.config.MaxWidth {
		width = s.config.MaxWidth
	}
	s.config.Width = width
}

// Width returns the current sidebar width.
func (s *Sidebar) Width() int {
	return s.config.Width
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
	for y := bounds.Y; y < bounds.Y+bounds.Height; y++ {
		ctx.Buffer.Set(bounds.X, y, '│', s.borderStyle)
	}
}

// HandleMessage processes sidebar input.
func (s *Sidebar) HandleMessage(msg runtime.Message) runtime.HandleResult {
	if s.tabs == nil {
		return runtime.Unhandled()
	}
	return s.tabs.HandleMessage(msg)
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

func (s *Sidebar) rebuildStatus() {
	if s.statusScroll == nil {
		return
	}
	s.statusContent = s.buildStatusContent()
	s.statusScroll.SetContent(s.statusContent)
}

func (s *Sidebar) rebuildFiles() {
	if s.filesScroll == nil {
		return
	}
	s.filesContent = s.buildFilesContent()
	s.filesScroll.SetContent(s.filesContent)
}

func (s *Sidebar) updateAllPanels() {
	s.updateTaskPanel()
	s.updatePlanPanel()
	s.updateToolsPanel()
	s.updateContextPanel()
	s.updateExperimentPanel()
	s.updateRLMPanel()
	s.updateCircuitPanel()
	s.updateBreadcrumb()
	s.updateFilesPanel()
	s.updateTouchesPanel()
}

func (s *Sidebar) updateTaskPanel() {
	if s.taskPanel == nil {
		return
	}
	s.taskPanel.Update(s.currentTask, s.taskProgress)
}

func (s *Sidebar) updatePlanPanel() {
	if s.planPanel == nil {
		return
	}
	s.planPanel.Update(s.planTasks)
}

func (s *Sidebar) updateToolsPanel() {
	if s.toolsPanel == nil {
		return
	}
	s.toolsPanel.Update(s.runningTools, s.toolHistory)
}

func (s *Sidebar) updateContextPanel() {
	if s.contextPanel == nil {
		return
	}
	label := formatContextLabel(s.contextUsed, s.contextBudget, s.contextWindow)
	max := s.contextMaxValue()
	if max <= 0 {
		max = 1
	}
	ratio := s.contextRatio()
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	s.contextPanel.Update(label, s.contextUsed, max, ratio)
}

func (s *Sidebar) updateExperimentPanel() {
	if s.experimentPanel == nil {
		return
	}
	s.experimentPanel.Update(s.experimentName, s.experimentStatus, s.experimentVariants)
}

func (s *Sidebar) updateRLMPanel() {
	if s.rlmPanel == nil {
		return
	}
	s.rlmPanel.Update(s.rlmStatus, s.rlmScratchpad)
}

func (s *Sidebar) updateCircuitPanel() {
	if s.circuitPanel == nil {
		return
	}
	s.circuitPanel.Update(s.circuitStatus)
}

func (s *Sidebar) updateBreadcrumb() {
	if s.breadcrumbPanel == nil {
		return
	}
	s.breadcrumbPanel.Update(s.projectPath)
}

func (s *Sidebar) updateFilesPanel() {
	if s.filesPanel == nil {
		return
	}
	s.filesPanel.Update(s.recentFiles, s.projectPath)
}

func (s *Sidebar) updateTouchesPanel() {
	if s.touchesPanel == nil {
		return
	}
	s.touchesPanel.Update(s.activeTouches)
}

func (s *Sidebar) contextRatio() float64 {
	if s.contextBudget > 0 {
		return float64(s.contextUsed) / float64(s.contextBudget)
	}
	if s.contextWindow > 0 {
		return float64(s.contextUsed) / float64(s.contextWindow)
	}
	return 0
}

func (s *Sidebar) contextMaxValue() int {
	if s.contextBudget > 0 {
		return s.contextBudget
	}
	if s.contextWindow > 0 {
		return s.contextWindow
	}
	if s.contextUsed > 0 {
		return s.contextUsed
	}
	return 1
}

func summarizePlan(tasks []PlanTask) (completed, total int) {
	for _, task := range tasks {
		total++
		if task.Status == TaskCompleted {
			completed++
		}
	}
	return completed, total
}

func taskStatusLabel(status TaskStatus) string {
	switch status {
	case TaskCompleted:
		return "done"
	case TaskInProgress:
		return "running"
	case TaskFailed:
		return "failed"
	default:
		return "pending"
	}
}

func clampPercent(progress int) int {
	if progress < 0 {
		return 0
	}
	if progress > 100 {
		return 100
	}
	return progress
}

func formatContextLabel(used, budget, window int) string {
	if budget > 0 {
		return intToStr(used) + " / " + intToStr(budget)
	}
	if window > 0 {
		return intToStr(used) + " / " + intToStr(window)
	}
	if used > 0 {
		return intToStr(used)
	}
	return "0"
}

func splitPath(path string) []string {
	clean := filepath.Clean(path)
	if clean == "." || clean == string(filepath.Separator) {
		return []string{path}
	}
	parts := strings.Split(clean, string(filepath.Separator))
	if len(parts) == 0 {
		return []string{path}
	}
	for i := range parts {
		if parts[i] == "" {
			parts[i] = string(filepath.Separator)
		}
	}
	return parts
}

func buildTreeFromPaths(paths []string, rootLabel string) *uiwidgets.TreeNode {
	label := "Files"
	if strings.TrimSpace(rootLabel) != "" {
		label = filepath.Base(rootLabel)
	}
	root := &uiwidgets.TreeNode{Label: label, Expanded: true}
	if len(paths) == 0 {
		root.Children = []*uiwidgets.TreeNode{{Label: "(none)"}}
		return root
	}
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	for _, path := range sorted {
		addPathNode(root, path)
	}
	return root
}

func buildTouchesTree(touches []TouchSummary) *uiwidgets.TreeNode {
	root := &uiwidgets.TreeNode{Label: "Touches", Expanded: true}
	if len(touches) == 0 {
		root.Children = []*uiwidgets.TreeNode{{Label: "(none)"}}
		return root
	}
	for _, touch := range touches {
		label := touch.Path
		if label == "" {
			label = "(unknown)"
		}
		child := &uiwidgets.TreeNode{Label: label}
		for _, r := range touch.Ranges {
			child.Children = append(child.Children, &uiwidgets.TreeNode{Label: rangeLabel(r)})
		}
		root.Children = append(root.Children, child)
	}
	return root
}

func addPathNode(root *uiwidgets.TreeNode, path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	parts := strings.Split(path, string(filepath.Separator))
	if len(parts) == 1 {
		parts = strings.Split(path, "/")
	}
	cur := root
	for _, part := range parts {
		if part == "" {
			continue
		}
		next := findChild(cur, part)
		if next == nil {
			next = &uiwidgets.TreeNode{Label: part}
			cur.Children = append(cur.Children, next)
		}
		cur = next
	}
}

func findChild(node *uiwidgets.TreeNode, label string) *uiwidgets.TreeNode {
	for _, child := range node.Children {
		if child.Label == label {
			return child
		}
	}
	return nil
}

func rangeLabel(r TouchRange) string {
	if r.End > r.Start {
		return "lines " + intToStr(r.Start) + "-" + intToStr(r.End)
	}
	return "line " + intToStr(r.Start)
}

var _ runtime.Widget = (*Sidebar)(nil)
var _ runtime.ChildProvider = (*Sidebar)(nil)
var _ runtime.Bindable = (*Sidebar)(nil)
var _ runtime.Unbindable = (*Sidebar)(nil)
