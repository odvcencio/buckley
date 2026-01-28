package widgets

import (
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/terminal"
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

	taskLabel       *uiwidgets.Label
	taskSpinner     *uiwidgets.Spinner
	taskProgressBar *uiwidgets.Progress
	taskPanel       *uiwidgets.Panel
	planProgressBar *uiwidgets.Progress
	planTable       *uiwidgets.Table
	planPanel       *uiwidgets.Panel
	toolsTable      *uiwidgets.Table
	toolsPanel      *uiwidgets.Panel
	contextGauge    *uiwidgets.AnimatedGauge
	contextBar      *uiwidgets.Progress
	contextLabel    *uiwidgets.Label
	contextGrid     *uiwidgets.Grid
	contextPanel    *uiwidgets.Panel
	experimentTable *uiwidgets.Table
	experimentPanel *uiwidgets.Panel
	rlmTable        *uiwidgets.Table
	rlmPanel        *uiwidgets.Panel
	circuitAlert    *uiwidgets.Alert
	circuitPanel    *uiwidgets.Panel
	calendar        *uiwidgets.Calendar
	calendarPanel   *uiwidgets.Panel
	breadcrumb      *uiwidgets.Breadcrumb
	breadcrumbPanel *uiwidgets.Panel
	filesTree       *uiwidgets.Tree
	filesPanel      *uiwidgets.Panel
	touchesTree     *uiwidgets.Tree
	touchesPanel    *uiwidgets.Panel
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

	s.initWidgets()
	s.updateAllPanels()
	return s
}

func (s *Sidebar) initWidgets() {
	s.taskLabel = uiwidgets.NewLabel("No active task")
	s.taskSpinner = uiwidgets.NewSpinner()
	s.taskProgressBar = uiwidgets.NewProgress()
	s.taskProgressBar.Label = "Task"
	s.taskProgressBar.ShowPercent = true
	taskHeader := runtime.HBox(
		runtime.Fixed(s.taskSpinner),
		runtime.Flexible(s.taskLabel, 1),
	).WithGap(1)
	taskContent := runtime.VBox(
		runtime.Fixed(taskHeader),
		runtime.Fixed(s.taskProgressBar),
	)
	s.taskPanel = uiwidgets.NewPanel(taskContent).WithBorder(s.borderStyle)
	s.taskPanel.SetTitle("Current Task")

	s.planProgressBar = uiwidgets.NewProgress()
	s.planProgressBar.Label = "Plan"
	s.planProgressBar.ShowPercent = true
	s.planTable = uiwidgets.NewTable(
		uiwidgets.TableColumn{Title: "Task"},
		uiwidgets.TableColumn{Title: "Status"},
	)
	planContent := runtime.VBox(
		runtime.Fixed(s.planProgressBar),
		runtime.Fixed(s.planTable),
	).WithGap(1)
	s.planPanel = uiwidgets.NewPanel(planContent).WithBorder(s.borderStyle)
	s.planPanel.SetTitle("Plan")

	s.toolsTable = uiwidgets.NewTable(
		uiwidgets.TableColumn{Title: "Tool"},
		uiwidgets.TableColumn{Title: "Status"},
		uiwidgets.TableColumn{Title: "Detail"},
	)
	s.toolsPanel = uiwidgets.NewPanel(s.toolsTable).WithBorder(s.borderStyle)
	s.toolsPanel.SetTitle("Tools")

	s.contextGauge = uiwidgets.NewAnimatedGauge(0, 1)
	s.contextLabel = uiwidgets.NewLabel("0 / 0")
	s.contextBar = uiwidgets.NewProgress()
	s.contextBar.Label = "Context"
	s.contextBar.ShowPercent = true
	s.contextGrid = uiwidgets.NewGrid(2, 2)
	s.contextGrid.Add(s.contextGauge, 0, 0, 2, 1)
	s.contextGrid.Add(s.contextLabel, 0, 1, 1, 1)
	s.contextGrid.Add(s.contextBar, 1, 1, 1, 1)
	s.contextPanel = uiwidgets.NewPanel(s.contextGrid).WithBorder(s.borderStyle)
	s.contextPanel.SetTitle("Context")

	s.experimentTable = uiwidgets.NewTable(
		uiwidgets.TableColumn{Title: "Variant"},
		uiwidgets.TableColumn{Title: "Status"},
	)
	s.experimentPanel = uiwidgets.NewPanel(s.experimentTable).WithBorder(s.borderStyle)
	s.experimentPanel.SetTitle("Experiments")

	s.rlmTable = uiwidgets.NewTable(
		uiwidgets.TableColumn{Title: "Key"},
		uiwidgets.TableColumn{Title: "Type"},
		uiwidgets.TableColumn{Title: "Summary"},
	)
	s.rlmPanel = uiwidgets.NewPanel(s.rlmTable).WithBorder(s.borderStyle)
	s.rlmPanel.SetTitle("RLM")

	s.circuitAlert = uiwidgets.NewAlert("All systems nominal", uiwidgets.AlertSuccess)
	s.circuitPanel = uiwidgets.NewPanel(s.circuitAlert).WithBorder(s.borderStyle)
	s.circuitPanel.SetTitle("Circuit")

	s.calendar = uiwidgets.NewCalendar()
	s.calendarPanel = uiwidgets.NewPanel(s.calendar).WithBorder(s.borderStyle)
	s.calendarPanel.SetTitle("Schedule")

	s.breadcrumb = uiwidgets.NewBreadcrumb(uiwidgets.BreadcrumbItem{Label: "Project"})
	s.breadcrumbPanel = uiwidgets.NewPanel(s.breadcrumb).WithBorder(s.borderStyle)
	s.breadcrumbPanel.SetTitle("Path")

	s.filesTree = uiwidgets.NewTree(&uiwidgets.TreeNode{Label: "(no files)"})
	s.filesPanel = uiwidgets.NewPanel(s.filesTree).WithBorder(s.borderStyle)
	s.filesPanel.SetTitle("Files")

	s.touchesTree = uiwidgets.NewTree(&uiwidgets.TreeNode{Label: "(no touches)"})
	s.touchesPanel = uiwidgets.NewPanel(s.touchesTree).WithBorder(s.borderStyle)
	s.touchesPanel.SetTitle("Touches")

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
		children = append(children, runtime.Fixed(s.taskPanel))
	}
	if s.showPlan {
		children = append(children, runtime.Fixed(s.planPanel))
	}
	if s.showTools {
		children = append(children, runtime.Fixed(s.toolsPanel))
	}
	if s.showContext {
		children = append(children, runtime.Fixed(s.contextPanel))
	}
	if s.showExperiment {
		children = append(children, runtime.Fixed(s.experimentPanel))
	}
	if s.showRLM {
		children = append(children, runtime.Fixed(s.rlmPanel))
	}
	if s.showCircuit {
		children = append(children, runtime.Fixed(s.circuitPanel))
	}
	children = append(children, runtime.Fixed(s.calendarPanel))
	return runtime.VBox(children...).WithGap(1)
}

func (s *Sidebar) buildFilesContent() *runtime.Flex {
	children := make([]runtime.FlexChild, 0, 4)
	children = append(children, runtime.Fixed(s.breadcrumbPanel))
	if s.showRecentFiles {
		children = append(children, runtime.Fixed(s.filesPanel))
	}
	if s.showTouches {
		children = append(children, runtime.Fixed(s.touchesPanel))
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

	panels := []*uiwidgets.Panel{
		s.taskPanel, s.planPanel, s.toolsPanel, s.contextPanel, s.experimentPanel,
		s.rlmPanel, s.circuitPanel, s.calendarPanel, s.breadcrumbPanel, s.filesPanel, s.touchesPanel,
	}
	for _, panel := range panels {
		if panel != nil {
			panel.SetStyle(background)
			panel.WithBorder(border)
		}
	}
	if s.taskLabel != nil {
		s.taskLabel.SetStyle(text)
	}
	if s.contextLabel != nil {
		s.contextLabel.SetStyle(text)
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
	if s.contextBar != nil {
		s.contextBar.Style = uiwidgets.GaugeStyle{
			EmptyStyle: muted,
			Thresholds: []uiwidgets.GaugeThreshold{
				{Ratio: 0.7, Style: warn},
				{Ratio: 0.9, Style: critical},
			},
		}
	}
}

// SetSpinnerStyle configures the spinner style.
func (s *Sidebar) SetSpinnerStyle(style backend.Style) {
	s.spinnerStyle = style
	if s.taskSpinner != nil {
		s.taskSpinner.SetStyle(style)
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

// SetShowCurrentTask controls visibility of current task section.
func (s *Sidebar) SetShowCurrentTask(show bool) {
	s.showCurrentTask = show
	s.rebuildStatus()
}

// SetPlanTasks updates the plan task list.
func (s *Sidebar) SetPlanTasks(tasks []PlanTask) {
	s.planTasks = tasks
	s.updatePlanPanel()
}

// SetShowPlan controls visibility of plan section.
func (s *Sidebar) SetShowPlan(show bool) {
	s.showPlan = show
	s.rebuildStatus()
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
	s.showTools = show
	s.rebuildStatus()
}

// SetContextUsage updates context usage values.
func (s *Sidebar) SetContextUsage(used, budget, window int) {
	s.contextUsed = used
	s.contextBudget = budget
	s.contextWindow = window
	if used > 0 || budget > 0 || window > 0 {
		s.showContext = true
	}
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
	s.showContext = show
	s.rebuildStatus()
}

// SetActiveTouches updates the active touches list.
func (s *Sidebar) SetActiveTouches(touches []TouchSummary) {
	s.activeTouches = touches
	s.updateTouchesPanel()
}

// SetShowTouches controls visibility of touches section.
func (s *Sidebar) SetShowTouches(show bool) {
	s.showTouches = show
	s.rebuildFiles()
}

// SetRecentFiles updates the recent files list.
func (s *Sidebar) SetRecentFiles(files []string) {
	s.recentFiles = files
	s.updateFilesPanel()
}

// SetShowRecentFiles controls visibility of recent files section.
func (s *Sidebar) SetShowRecentFiles(show bool) {
	s.showRecentFiles = show
	s.rebuildFiles()
}

// SetExperiment updates the experiment summary.
func (s *Sidebar) SetExperiment(name, status string, variants []ExperimentVariant) {
	s.experimentName = name
	s.experimentStatus = status
	s.experimentVariants = variants
	if strings.TrimSpace(name) != "" || len(variants) > 0 {
		s.showExperiment = true
	}
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
	if status != nil || len(scratchpad) > 0 {
		s.showRLM = true
	}
	s.updateRLMPanel()
}

// SetCircuitStatus updates the circuit breaker status display.
func (s *Sidebar) SetCircuitStatus(status *CircuitStatus) {
	s.circuitStatus = status
	if status != nil && status.State != "Closed" {
		s.showCircuit = true
	}
	s.updateCircuitPanel()
}

// SetShowCircuit controls visibility of circuit breaker section.
func (s *Sidebar) SetShowCircuit(show bool) {
	s.showCircuit = show
	s.rebuildStatus()
}

// SetShowRLM controls visibility of the RLM section.
func (s *Sidebar) SetShowRLM(show bool) {
	s.showRLM = show
	s.rebuildStatus()
}

// SetShowExperiment controls visibility of experiments section.
func (s *Sidebar) SetShowExperiment(show bool) {
	s.showExperiment = show
	s.rebuildStatus()
}

// SetSpinnerFrame updates the spinner animation frame.
func (s *Sidebar) SetSpinnerFrame(frame int) {
	if frame < 0 {
		frame = 0
	}
	if s.taskSpinner != nil && frame != s.spinnerFrame {
		s.taskSpinner.Advance()
	}
	s.spinnerFrame = frame
}

// ToggleRecentFiles toggles the recent files section.
func (s *Sidebar) ToggleRecentFiles() {
	s.showRecentFiles = !s.showRecentFiles
	s.rebuildFiles()
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
	if key, ok := msg.(runtime.KeyMsg); ok && key.Key == terminal.KeyRune {
		switch key.Rune {
		case '1':
			s.SetShowCurrentTask(!s.showCurrentTask)
			return runtime.Handled()
		case '2':
			s.SetShowPlan(!s.showPlan)
			return runtime.Handled()
		case '3':
			s.SetShowTools(!s.showTools)
			return runtime.Handled()
		case '4':
			s.SetShowContext(!s.showContext)
			return runtime.Handled()
		case '5':
			s.SetShowTouches(!s.showTouches)
			return runtime.Handled()
		case '6':
			s.ToggleRecentFiles()
			return runtime.Handled()
		}
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
	label := strings.TrimSpace(s.currentTask)
	if label == "" {
		label = "No active task"
		if s.taskSpinner != nil {
			s.taskSpinner.Frames = []string{" "}
		}
	} else if s.taskSpinner != nil {
		s.taskSpinner.Frames = []string{"-", "\\", "|", "/"}
	}
	if s.taskLabel != nil {
		s.taskLabel.SetText(label)
	}
	if s.taskProgressBar != nil {
		s.taskProgressBar.Value = float64(clampPercent(s.taskProgress))
		s.taskProgressBar.Max = 100
	}
}

func (s *Sidebar) updatePlanPanel() {
	if s.planProgressBar != nil {
		completed, total := summarizePlan(s.planTasks)
		percent := 0.0
		if total > 0 {
			percent = float64(completed) / float64(total) * 100
		}
		s.planProgressBar.Value = percent
		s.planProgressBar.Max = 100
	}
	if s.planTable != nil {
		rows := make([][]string, 0, len(s.planTasks))
		for _, task := range s.planTasks {
			rows = append(rows, []string{task.Name, taskStatusLabel(task.Status)})
		}
		if len(rows) == 0 {
			rows = [][]string{{"No tasks", ""}}
		}
		s.planTable.SetRows(rows)
	}
}

func (s *Sidebar) updateToolsPanel() {
	if s.toolsTable == nil {
		return
	}
	rows := make([][]string, 0, len(s.runningTools)+len(s.toolHistory))
	for _, tool := range s.runningTools {
		detail := strings.TrimSpace(tool.Command)
		rows = append(rows, []string{tool.Name, "running", detail})
	}
	for i := len(s.toolHistory) - 1; i >= 0 && len(rows) < 12; i-- {
		entry := s.toolHistory[i]
		rows = append(rows, []string{entry.Name, entry.Status, entry.Detail})
	}
	if len(rows) == 0 {
		rows = [][]string{{"No tools", "", ""}}
	}
	s.toolsTable.SetRows(rows)
}

func (s *Sidebar) updateContextPanel() {
	if s.contextLabel != nil {
		label := formatContextLabel(s.contextUsed, s.contextBudget, s.contextWindow)
		s.contextLabel.SetText(label)
	}
	if s.contextBar != nil {
		max := s.contextMaxValue()
		if max <= 0 {
			max = 1
		}
		s.contextBar.Value = float64(s.contextUsed)
		s.contextBar.Max = float64(max)
	}
	if s.contextGauge != nil {
		ratio := s.contextRatio()
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		s.contextGauge.SetValue(ratio)
	}
}

func (s *Sidebar) updateExperimentPanel() {
	if s.experimentTable == nil {
		return
	}
	rows := make([][]string, 0, len(s.experimentVariants)+1)
	if strings.TrimSpace(s.experimentName) != "" {
		rows = append(rows, []string{s.experimentName, s.experimentStatus})
	}
	for _, variant := range s.experimentVariants {
		rows = append(rows, []string{variant.Name, variant.Status})
	}
	if len(rows) == 0 {
		rows = [][]string{{"No experiments", ""}}
	}
	s.experimentTable.SetRows(rows)
}

func (s *Sidebar) updateRLMPanel() {
	if s.rlmTable == nil {
		return
	}
	rows := make([][]string, 0, len(s.rlmScratchpad)+1)
	if s.rlmStatus != nil {
		summary := s.rlmStatus.Summary
		if summary == "" {
			summary = "Iteration " + intToStr(s.rlmStatus.Iteration)
		}
		rows = append(rows, []string{"Status", "", summary})
	}
	for _, entry := range s.rlmScratchpad {
		rows = append(rows, []string{entry.Key, entry.Type, entry.Summary})
	}
	if len(rows) == 0 {
		rows = [][]string{{"No entries", "", ""}}
	}
	s.rlmTable.SetRows(rows)
}

func (s *Sidebar) updateCircuitPanel() {
	if s.circuitAlert == nil {
		return
	}
	if s.circuitStatus == nil || s.circuitStatus.State == "" {
		s.circuitAlert.Text = "All systems nominal"
		s.circuitAlert.Variant = uiwidgets.AlertSuccess
		return
	}
	status := s.circuitStatus.State
	message := status
	if s.circuitStatus.LastError != "" {
		message = status + ": " + s.circuitStatus.LastError
	}
	variant := uiwidgets.AlertInfo
	switch strings.ToLower(status) {
	case "open":
		variant = uiwidgets.AlertError
	case "halfopen", "half-open":
		variant = uiwidgets.AlertWarning
	}
	s.circuitAlert.Text = message
	s.circuitAlert.Variant = variant
}

func (s *Sidebar) updateBreadcrumb() {
	if s.breadcrumb == nil {
		return
	}
	path := s.projectPath
	if path == "" {
		path = "Project"
	}
	parts := splitPath(path)
	items := make([]uiwidgets.BreadcrumbItem, 0, len(parts))
	for _, part := range parts {
		items = append(items, uiwidgets.BreadcrumbItem{Label: part})
	}
	s.breadcrumb.Items = items
}

func (s *Sidebar) updateFilesPanel() {
	if s.filesTree == nil {
		return
	}
	root := buildTreeFromPaths(s.recentFiles, s.projectPath)
	s.filesTree.SetRoot(root)
}

func (s *Sidebar) updateTouchesPanel() {
	if s.touchesTree == nil {
		return
	}
	root := buildTouchesTree(s.activeTouches)
	s.touchesTree.SetRoot(root)
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
