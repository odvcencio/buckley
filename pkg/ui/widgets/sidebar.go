package widgets

import (
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/ui/backend"
	"github.com/odvcencio/buckley/pkg/ui/runtime"
	"github.com/odvcencio/buckley/pkg/ui/terminal"
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

// Sidebar displays task progress, plan, and running tools.
type Sidebar struct {
	FocusableBase

	// Current task info
	currentTask     string
	taskProgress    int // 0-100
	showCurrentTask bool

	// Plan section
	planTasks []PlanTask
	showPlan  bool

	// Running tools section
	runningTools []RunningTool
	showTools    bool

	// Active touches section
	activeTouches []TouchSummary
	showTouches   bool

	// Recent files section
	recentFiles     []string
	showRecentFiles bool

	// Experiments section
	experimentName     string
	experimentStatus   string
	experimentVariants []ExperimentVariant
	showExperiment     bool

	// RLM section
	rlmStatus     *RLMStatus
	rlmScratchpad []RLMScratchpadEntry
	showRLM       bool

	// Scroll state (for long lists)
	planScrollOffset int
	focusedSection   int // 0=task, 1=plan, 2=tools, 3=touches, 4=files

	// Styles
	borderStyle    backend.Style
	headerStyle    backend.Style
	textStyle      backend.Style
	progressFull   backend.Style
	progressEmpty  backend.Style
	completedStyle backend.Style
	activeStyle    backend.Style
	pendingStyle   backend.Style
	failedStyle    backend.Style
}

// NewSidebar creates a new sidebar widget.
func NewSidebar() *Sidebar {
	return &Sidebar{
		showCurrentTask: true,
		showPlan:        true,
		showTools:       true,
		showTouches:     true,
		showRecentFiles: false, // Collapsed by default
		showExperiment:  false,
		showRLM:         true,
		borderStyle:     backend.DefaultStyle(),
		headerStyle:     backend.DefaultStyle().Bold(true),
		textStyle:       backend.DefaultStyle(),
		progressFull:    backend.DefaultStyle().Foreground(backend.ColorGreen),
		progressEmpty:   backend.DefaultStyle().Foreground(backend.ColorDefault),
		completedStyle:  backend.DefaultStyle().Foreground(backend.ColorGreen),
		activeStyle:     backend.DefaultStyle().Foreground(backend.ColorYellow).Bold(true),
		pendingStyle:    backend.DefaultStyle().Foreground(backend.ColorDefault),
		failedStyle:     backend.DefaultStyle().Foreground(backend.ColorRed),
	}
}

// SetStyles configures the sidebar appearance.
func (s *Sidebar) SetStyles(border, header, text, progressFull, progressEmpty backend.Style) {
	s.borderStyle = border
	s.headerStyle = header
	s.textStyle = text
	s.progressFull = progressFull
	s.progressEmpty = progressEmpty
}

// HasContent returns true when any sidebar section has data to render.
func (s *Sidebar) HasContent() bool {
	if s.showCurrentTask && strings.TrimSpace(s.currentTask) != "" {
		return true
	}
	if s.showPlan && len(s.planTasks) > 0 {
		return true
	}
	if s.showTools && len(s.runningTools) > 0 {
		return true
	}
	if s.showExperiment && (strings.TrimSpace(s.experimentName) != "" || len(s.experimentVariants) > 0) {
		return true
	}
	if s.showRLM && (s.rlmStatus != nil || len(s.rlmScratchpad) > 0) {
		return true
	}
	if s.showTouches && len(s.activeTouches) > 0 {
		return true
	}
	if len(s.recentFiles) > 0 {
		return true
	}
	return false
}

// SetCurrentTask updates the current task display.
func (s *Sidebar) SetCurrentTask(name string, progress int) {
	s.currentTask = name
	s.taskProgress = progress
	if progress < 0 {
		s.taskProgress = 0
	}
	if progress > 100 {
		s.taskProgress = 100
	}
}

// SetShowCurrentTask controls visibility of current task section.
func (s *Sidebar) SetShowCurrentTask(show bool) {
	s.showCurrentTask = show
}

// SetPlanTasks updates the plan task list.
func (s *Sidebar) SetPlanTasks(tasks []PlanTask) {
	s.planTasks = tasks
}

// SetShowPlan controls visibility of plan section.
func (s *Sidebar) SetShowPlan(show bool) {
	s.showPlan = show
}

// SetRunningTools updates the running tools list.
func (s *Sidebar) SetRunningTools(tools []RunningTool) {
	s.runningTools = tools
}

// SetShowTools controls visibility of tools section.
func (s *Sidebar) SetShowTools(show bool) {
	s.showTools = show
}

// SetActiveTouches updates the active touches list.
func (s *Sidebar) SetActiveTouches(touches []TouchSummary) {
	s.activeTouches = touches
}

// SetShowTouches controls visibility of touches section.
func (s *Sidebar) SetShowTouches(show bool) {
	s.showTouches = show
}

// SetRecentFiles updates the recent files list.
func (s *Sidebar) SetRecentFiles(files []string) {
	s.recentFiles = files
}

// SetShowRecentFiles controls visibility of recent files section.
func (s *Sidebar) SetShowRecentFiles(show bool) {
	s.showRecentFiles = show
}

// SetExperiment updates the experiment summary.
func (s *Sidebar) SetExperiment(name, status string, variants []ExperimentVariant) {
	s.experimentName = name
	s.experimentStatus = status
	s.experimentVariants = variants
	if strings.TrimSpace(name) != "" || len(variants) > 0 {
		s.showExperiment = true
	}
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
}

// SetShowRLM controls visibility of the RLM section.
func (s *Sidebar) SetShowRLM(show bool) {
	s.showRLM = show
}

// SetShowExperiment controls visibility of experiments section.
func (s *Sidebar) SetShowExperiment(show bool) {
	s.showExperiment = show
}

// ToggleRecentFiles toggles the recent files section.
func (s *Sidebar) ToggleRecentFiles() {
	s.showRecentFiles = !s.showRecentFiles
}

// Measure returns the preferred size.
func (s *Sidebar) Measure(constraints runtime.Constraints) runtime.Size {
	// Sidebar has fixed width, flexible height
	width := 24
	if constraints.MaxWidth < width {
		width = constraints.MaxWidth
	}
	return runtime.Size{
		Width:  width,
		Height: constraints.MaxHeight,
	}
}

// Layout stores the assigned bounds.
func (s *Sidebar) Layout(bounds runtime.Rect) {
	s.bounds = bounds
}

// Render draws the sidebar.
func (s *Sidebar) Render(ctx runtime.RenderContext) {
	b := s.bounds
	if b.Width < 10 || b.Height < 5 {
		return
	}

	// Draw left border
	for y := b.Y; y < b.Y+b.Height; y++ {
		ctx.Buffer.Set(b.X, y, '│', s.borderStyle)
	}

	y := b.Y
	contentX := b.X + 1
	contentWidth := b.Width - 1

	// Current Task Section
	if s.showCurrentTask && s.currentTask != "" {
		y = s.renderCurrentTask(ctx.Buffer, contentX, y, contentWidth)
	}

	// Plan Section
	if s.showPlan && len(s.planTasks) > 0 {
		y = s.renderPlan(ctx.Buffer, contentX, y, contentWidth, b.Y+b.Height-y)
	}

	// Running Tools Section
	if s.showTools && len(s.runningTools) > 0 {
		y = s.renderTools(ctx.Buffer, contentX, y, contentWidth)
	}

	// RLM Section
	if s.showRLM && (s.rlmStatus != nil || len(s.rlmScratchpad) > 0) {
		y = s.renderRLM(ctx.Buffer, contentX, y, contentWidth)
	}

	// Experiments Section
	if s.showExperiment && (s.experimentName != "" || len(s.experimentVariants) > 0) {
		y = s.renderExperiment(ctx.Buffer, contentX, y, contentWidth)
	}

	// Active Touches Section
	if s.showTouches && len(s.activeTouches) > 0 {
		y = s.renderTouches(ctx.Buffer, contentX, y, contentWidth)
	}

	// Recent Files Section
	if len(s.recentFiles) > 0 {
		s.renderRecentFiles(ctx.Buffer, contentX, y, contentWidth)
	}
}

// renderCurrentTask draws the current task section.
func (s *Sidebar) renderCurrentTask(buf *runtime.Buffer, x, y, width int) int {
	// Header
	icon := '▼'
	if !s.showCurrentTask {
		icon = '▶'
	}
	buf.Set(x, y, icon, s.headerStyle)
	buf.SetString(x+2, y, "Current Task", s.headerStyle)
	y++

	// Task name
	taskName := s.currentTask
	if len(taskName) > width-2 {
		taskName = taskName[:width-5] + "..."
	}
	buf.SetString(x+2, y, taskName, s.textStyle)
	y++

	// Progress bar
	y = s.renderProgressBar(buf, x+2, y, width-4, s.taskProgress)
	y++

	return y
}

// renderProgressBar draws a progress bar.
func (s *Sidebar) renderProgressBar(buf *runtime.Buffer, x, y, width, percent int) int {
	filled := (width * percent) / 100

	for i := 0; i < width; i++ {
		ch := '░'
		style := s.progressEmpty
		if i < filled {
			ch = '█'
			style = s.progressFull
		}
		buf.Set(x+i, y, ch, style)
	}

	// Show percentage
	percentStr := intToStr(percent) + "%"
	buf.SetString(x+width+1, y, percentStr, s.textStyle)

	return y + 1
}

// renderPlan draws the plan section.
func (s *Sidebar) renderPlan(buf *runtime.Buffer, x, y, width, maxHeight int) int {
	// Header with count
	icon := '▼'
	if !s.showPlan {
		icon = '▶'
	}
	buf.Set(x, y, icon, s.headerStyle)

	completed := 0
	for _, t := range s.planTasks {
		if t.Status == TaskCompleted {
			completed++
		}
	}
	header := "Plan (" + intToStr(completed) + "/" + intToStr(len(s.planTasks)) + ")"
	buf.SetString(x+2, y, header, s.headerStyle)
	y++

	if !s.showPlan {
		return y
	}

	// Tasks
	maxTasks := maxHeight - 1
	if maxTasks > len(s.planTasks) {
		maxTasks = len(s.planTasks)
	}

	for i := 0; i < maxTasks; i++ {
		task := s.planTasks[i]

		// Status icon
		var icon rune
		var iconStyle backend.Style
		switch task.Status {
		case TaskCompleted:
			icon = '✓'
			iconStyle = s.completedStyle
		case TaskInProgress:
			icon = '→'
			iconStyle = s.activeStyle
		case TaskPending:
			icon = '○'
			iconStyle = s.pendingStyle
		case TaskFailed:
			icon = '✗'
			iconStyle = s.failedStyle
		}

		buf.Set(x+2, y, icon, iconStyle)
		buf.Set(x+3, y, ' ', s.textStyle)

		// Task name
		name := task.Name
		maxName := width - 5
		if len(name) > maxName {
			name = name[:maxName-3] + "..."
		}

		textStyle := s.textStyle
		if task.Status == TaskInProgress {
			textStyle = s.activeStyle
		}
		buf.SetString(x+4, y, name, textStyle)
		y++
	}

	return y
}

// renderTools draws the running tools section.
func (s *Sidebar) renderTools(buf *runtime.Buffer, x, y, width int) int {
	// Header
	buf.Set(x, y, '▼', s.headerStyle)
	buf.SetString(x+2, y, "Tools Running", s.headerStyle)
	y++

	for _, tool := range s.runningTools {
		// Spinner icon
		buf.Set(x+2, y, '⟳', s.activeStyle)
		buf.Set(x+3, y, ' ', s.textStyle)

		name := tool.Name
		maxName := width - 5
		if len(name) > maxName {
			name = name[:maxName-3] + "..."
		}
		buf.SetString(x+4, y, name, s.textStyle)
		y++

		// Command detail if present
		if tool.Command != "" {
			cmd := "  " + tool.Command
			if len(cmd) > width-4 {
				cmd = cmd[:width-7] + "..."
			}
			buf.SetString(x+4, y, cmd, s.textStyle)
			y++
		}
	}

	return y
}

func (s *Sidebar) renderRLM(buf *runtime.Buffer, x, y, width int) int {
	icon := '▼'
	if !s.showRLM {
		icon = '▶'
	}
	buf.Set(x, y, icon, s.headerStyle)
	buf.SetString(x+2, y, "RLM", s.headerStyle)
	y++

	if !s.showRLM {
		return y
	}

	if s.rlmStatus != nil {
		iterLine := "Iter " + intToStr(s.rlmStatus.Iteration)
		if s.rlmStatus.MaxIterations > 0 {
			iterLine += "/" + intToStr(s.rlmStatus.MaxIterations)
		}
		if s.rlmStatus.Ready {
			iterLine += " ✓"
		}
		buf.SetString(x+2, y, truncateSidebarText(iterLine, width-2), s.textStyle)
		y++

		if s.rlmStatus.TokensUsed > 0 {
			tokenLine := "Tokens " + intToStr(s.rlmStatus.TokensUsed)
			buf.SetString(x+2, y, truncateSidebarText(tokenLine, width-2), s.textStyle)
			y++
		}

		summary := strings.TrimSpace(s.rlmStatus.Summary)
		if summary != "" {
			buf.SetString(x+2, y, truncateSidebarText(summary, width-2), s.textStyle)
			y++
		}
	}

	if len(s.rlmScratchpad) > 0 {
		buf.SetString(x+2, y, "Scratchpad", s.headerStyle)
		y++
		maxEntries := len(s.rlmScratchpad)
		if maxEntries > 3 {
			maxEntries = 3
		}
		for i := 0; i < maxEntries; i++ {
			entry := s.rlmScratchpad[i]
			line := strings.TrimSpace(entry.Type)
			if entry.Summary != "" {
				if line != "" {
					line += ": "
				}
				line += entry.Summary
			}
			if line == "" {
				line = entry.Key
			}
			line = truncateSidebarText(line, width-4)
			buf.SetString(x+4, y, line, s.textStyle)
			y++
		}
	}

	return y
}

// renderTouches draws the active touches section.
func (s *Sidebar) renderTouches(buf *runtime.Buffer, x, y, width int) int {
	buf.Set(x, y, '▼', s.headerStyle)
	buf.SetString(x+2, y, "Active Touches", s.headerStyle)
	y++

	for _, touch := range s.activeTouches {
		label := touch.Path
		if len(touch.Ranges) > 0 {
			r := touch.Ranges[0]
			label = fmt.Sprintf("%s:%d-%d", label, r.Start, r.End)
		}
		if touch.Operation != "" {
			label = label + " (" + touch.Operation + ")"
		}
		if len(label) > width-4 {
			label = label[:width-7] + "..."
		}
		buf.SetString(x+4, y, label, s.textStyle)
		y++
	}

	return y
}

func (s *Sidebar) renderExperiment(buf *runtime.Buffer, x, y, width int) int {
	buf.Set(x, y, '▼', s.headerStyle)
	buf.SetString(x+2, y, "Experiment", s.headerStyle)
	y++

	if s.experimentName != "" {
		name := truncateSidebarText(s.experimentName, width-4)
		buf.SetString(x+4, y, name, s.textStyle)
		y++
	}
	if s.experimentStatus != "" {
		statusLine := truncateSidebarText("status "+s.experimentStatus, width-4)
		buf.SetString(x+4, y, statusLine, s.textStyle)
		y++
	}

	for _, variant := range s.experimentVariants {
		label := strings.TrimSpace(variant.Name)
		if label == "" {
			label = strings.TrimSpace(variant.ModelID)
		}
		if label == "" {
			label = variant.ID
		}
		symbol := experimentStatusSymbol(variant.Status)
		line := truncateSidebarText(fmt.Sprintf("%s %s", symbol, label), width-4)
		buf.SetString(x+4, y, line, experimentStatusStyle(variant.Status, s))
		y++
	}

	return y
}

// renderRecentFiles draws the recent files section.
func (s *Sidebar) renderRecentFiles(buf *runtime.Buffer, x, y, width int) int {
	// Header
	icon := '▶'
	if s.showRecentFiles {
		icon = '▼'
	}
	buf.Set(x, y, icon, s.headerStyle)
	buf.SetString(x+2, y, "Recent Files", s.headerStyle)
	y++

	if !s.showRecentFiles {
		return y
	}

	for _, file := range s.recentFiles {
		name := file
		if len(name) > width-4 {
			// Show just filename
			for i := len(name) - 1; i >= 0; i-- {
				if name[i] == '/' {
					name = name[i+1:]
					break
				}
			}
			if len(name) > width-4 {
				name = name[:width-7] + "..."
			}
		}
		buf.SetString(x+4, y, name, s.textStyle)
		y++
	}

	return y
}

// HandleMessage processes input.
func (s *Sidebar) HandleMessage(msg runtime.Message) runtime.HandleResult {
	key, ok := msg.(runtime.KeyMsg)
	if !ok {
		return runtime.Unhandled()
	}

	switch key.Key {
	case terminal.KeyUp:
		if s.planScrollOffset > 0 {
			s.planScrollOffset--
		}
		return runtime.Handled()

	case terminal.KeyDown:
		maxScroll := len(s.planTasks) - 5
		if maxScroll < 0 {
			maxScroll = 0
		}
		if s.planScrollOffset < maxScroll {
			s.planScrollOffset++
		}
		return runtime.Handled()

	case terminal.KeyRune:
		switch key.Rune {
		case '1': // Toggle current task
			s.showCurrentTask = !s.showCurrentTask
			return runtime.Handled()
		case '2': // Toggle plan
			s.showPlan = !s.showPlan
			return runtime.Handled()
		case '3': // Toggle tools
			s.showTools = !s.showTools
			return runtime.Handled()
		case '4': // Toggle touches
			s.showTouches = !s.showTouches
			return runtime.Handled()
		case '5': // Toggle recent files
			s.showRecentFiles = !s.showRecentFiles
			return runtime.Handled()
		case '6': // Toggle experiments
			s.showExperiment = !s.showExperiment
			return runtime.Handled()
		case '7': // Toggle RLM
			s.showRLM = !s.showRLM
			return runtime.Handled()
		}
	}

	return runtime.Unhandled()
}

func experimentStatusSymbol(status string) string {
	switch status {
	case "running":
		return "[>]"
	case "completed":
		return "[+]"
	case "failed":
		return "[x]"
	default:
		return "[ ]"
	}
}

func experimentStatusStyle(status string, s *Sidebar) backend.Style {
	switch status {
	case "running":
		return s.activeStyle
	case "completed":
		return s.completedStyle
	case "failed":
		return s.failedStyle
	default:
		return s.pendingStyle
	}
}

func truncateSidebarText(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" || max <= 0 {
		return ""
	}
	if len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return value[:max-3] + "..."
}
