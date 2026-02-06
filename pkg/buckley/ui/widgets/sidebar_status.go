package widgets

import (
	"strings"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/scroll"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

type sidebarStatus struct {
	showCurrentTask bool
	showPlan        bool
	showTools       bool
	showContext     bool
	showExperiment  bool
	showRLM         bool
	showCircuit     bool
	showAgents      bool

	currentTask   string
	taskProgress  int
	planTasks     []PlanTask
	runningTools  []RunningTool
	toolHistory   []ToolHistoryEntry
	contextUsed   int
	contextBudget int
	contextWindow int

	experimentName     string
	experimentStatus   string
	experimentVariants []ExperimentVariant

	rlmStatus     *RLMStatus
	rlmScratchpad []RLMScratchpadEntry

	circuitStatus *CircuitStatus

	agents []AgentSummary

	spinnerFrame int

	taskPanel        *taskPanel
	planPanel        *planPanel
	toolsPanel       *toolsPanel
	contextPanel     *contextPanel
	experimentPanel  *experimentPanel
	rlmPanel         *rlmPanel
	circuitPanel     *circuitPanel
	agentsPanelInst  *agentsPanel
	calendarPanel    *calendarPanel

	content *runtime.Flex
	scroll  *uiwidgets.ScrollView
}

func newSidebarStatus(border backend.Style) *sidebarStatus {
	status := &sidebarStatus{
		showCurrentTask: true,
		showPlan:        true,
		showTools:       true,
		showContext:     true,
		showExperiment:  true,
		showRLM:         true,
		showCircuit:     true,
		showAgents:      true,
	}
	status.taskPanel = newTaskPanel(border)
	status.planPanel = newPlanPanel(border)
	status.toolsPanel = newToolsPanel(border)
	status.contextPanel = newContextPanel(border)
	status.experimentPanel = newExperimentPanel(border)
	status.rlmPanel = newRLMPanel(border)
	status.circuitPanel = newCircuitPanel(border)
	status.agentsPanelInst = newAgentsPanel(border)
	status.calendarPanel = newCalendarPanel(border)

	status.content = status.buildContent()
	status.scroll = uiwidgets.NewScrollView(status.content)
	status.scroll.SetBehavior(scroll.ScrollBehavior{Vertical: scroll.ScrollAuto, Horizontal: scroll.ScrollNever, MouseWheel: 3, PageSize: 1})
	return status
}

func (s *sidebarStatus) ScrollView() *uiwidgets.ScrollView {
	if s == nil {
		return nil
	}
	return s.scroll
}

func (s *sidebarStatus) buildContent() *runtime.Flex {
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
	if s.showAgents {
		if panel := s.agentsPanelInst.Panel(); panel != nil {
			children = append(children, runtime.Fixed(panel))
		}
	}
	if panel := s.calendarPanel.Panel(); panel != nil {
		children = append(children, runtime.Fixed(panel))
	}
	return runtime.VBox(children...).WithGap(1)
}

func (s *sidebarStatus) rebuild() {
	if s == nil || s.scroll == nil {
		return
	}
	s.content = s.buildContent()
	s.scroll.SetContent(s.content)
}

func (s *sidebarStatus) ApplyVisibility(showCurrentTask, showPlan, showTools, showContext, showExperiment, showRLM, showCircuit, showAgents bool) bool {
	if s == nil {
		return false
	}
	changed := false
	if s.showCurrentTask != showCurrentTask {
		s.showCurrentTask = showCurrentTask
		changed = true
	}
	if s.showPlan != showPlan {
		s.showPlan = showPlan
		changed = true
	}
	if s.showTools != showTools {
		s.showTools = showTools
		changed = true
	}
	if s.showContext != showContext {
		s.showContext = showContext
		changed = true
	}
	if s.showExperiment != showExperiment {
		s.showExperiment = showExperiment
		changed = true
	}
	if s.showRLM != showRLM {
		s.showRLM = showRLM
		changed = true
	}
	if s.showCircuit != showCircuit {
		s.showCircuit = showCircuit
		changed = true
	}
	if s.showAgents != showAgents {
		s.showAgents = showAgents
		changed = true
	}
	if changed {
		s.rebuild()
	}
	return changed
}

func (s *sidebarStatus) ApplyContextUsage(used, budget, window int) {
	if s == nil {
		return
	}
	s.contextUsed = used
	s.contextBudget = budget
	s.contextWindow = window
	s.updateContextPanel()
}

func (s *sidebarStatus) SetStyles(border, background, text backend.Style) {
	if s == nil {
		return
	}
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
	if s.agentsPanelInst != nil {
		s.agentsPanelInst.SetStyles(border, background)
	}
	if s.calendarPanel != nil {
		s.calendarPanel.SetStyles(border, background)
	}
}

func (s *sidebarStatus) SetContextStyles(active, warn, critical, muted backend.Style) {
	if s == nil || s.contextPanel == nil {
		return
	}
	s.contextPanel.SetGaugeStyle(uiwidgets.GaugeStyle{
		EmptyStyle: muted,
		Thresholds: []uiwidgets.GaugeThreshold{
			{Ratio: 0.7, Style: warn},
			{Ratio: 0.9, Style: critical},
		},
	})
}

func (s *sidebarStatus) SetSpinnerStyle(style backend.Style) {
	if s == nil || s.taskPanel == nil {
		return
	}
	s.taskPanel.SetSpinnerStyle(style)
}

func (s *sidebarStatus) SetSpinnerFrame(frame int) {
	if s == nil {
		return
	}
	if frame < 0 {
		frame = 0
	}
	if s.taskPanel != nil && frame != s.spinnerFrame {
		s.taskPanel.AdvanceSpinner()
	}
	s.spinnerFrame = frame
}

func (s *sidebarStatus) HasContent() bool {
	if s == nil {
		return false
	}
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
	if len(s.agents) > 0 {
		return true
	}
	return false
}

func (s *sidebarStatus) applyCurrentTask(name string, progress int) {
	if s == nil {
		return
	}
	s.currentTask = name
	s.taskProgress = clampPercent(progress)
	s.updateTaskPanel()
}

func (s *sidebarStatus) applyPlanTasks(tasks []PlanTask) {
	if s == nil {
		return
	}
	s.planTasks = tasks
	s.updatePlanPanel()
}

func (s *sidebarStatus) applyRunningTools(tools []RunningTool) {
	if s == nil {
		return
	}
	s.runningTools = tools
	s.updateToolsPanel()
}

func (s *sidebarStatus) applyToolHistory(history []ToolHistoryEntry) {
	if s == nil {
		return
	}
	s.toolHistory = history
	s.updateToolsPanel()
}

func (s *sidebarStatus) applyExperiment(name, status string, variants []ExperimentVariant) {
	if s == nil {
		return
	}
	s.experimentName = name
	s.experimentStatus = status
	s.experimentVariants = variants
	s.updateExperimentPanel()
}

func (s *sidebarStatus) applyRLMStatus(status *RLMStatus, scratchpad []RLMScratchpadEntry) {
	if s == nil {
		return
	}
	s.rlmStatus = status
	if scratchpad == nil {
		s.rlmScratchpad = nil
	} else {
		s.rlmScratchpad = scratchpad
	}
	s.updateRLMPanel()
}

func (s *sidebarStatus) applyCircuitStatus(status *CircuitStatus) {
	if s == nil {
		return
	}
	s.circuitStatus = status
	s.updateCircuitPanel()
}

func (s *sidebarStatus) applyAgents(agents []AgentSummary) {
	if s == nil {
		return
	}
	s.agents = agents
	s.updateAgentsPanel()
}

func (s *sidebarStatus) updateAllPanels() {
	s.updateTaskPanel()
	s.updatePlanPanel()
	s.updateToolsPanel()
	s.updateContextPanel()
	s.updateExperimentPanel()
	s.updateRLMPanel()
	s.updateCircuitPanel()
	s.updateAgentsPanel()
}

func (s *sidebarStatus) updateTaskPanel() {
	if s == nil || s.taskPanel == nil {
		return
	}
	s.taskPanel.Update(s.currentTask, s.taskProgress)
}

func (s *sidebarStatus) updatePlanPanel() {
	if s == nil || s.planPanel == nil {
		return
	}
	s.planPanel.Update(s.planTasks)
}

func (s *sidebarStatus) updateToolsPanel() {
	if s == nil || s.toolsPanel == nil {
		return
	}
	s.toolsPanel.Update(s.runningTools, s.toolHistory)
}

func (s *sidebarStatus) updateContextPanel() {
	if s == nil || s.contextPanel == nil {
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

func (s *sidebarStatus) updateExperimentPanel() {
	if s == nil || s.experimentPanel == nil {
		return
	}
	s.experimentPanel.Update(s.experimentName, s.experimentStatus, s.experimentVariants)
}

func (s *sidebarStatus) updateRLMPanel() {
	if s == nil || s.rlmPanel == nil {
		return
	}
	s.rlmPanel.Update(s.rlmStatus, s.rlmScratchpad)
}

func (s *sidebarStatus) updateCircuitPanel() {
	if s == nil || s.circuitPanel == nil {
		return
	}
	s.circuitPanel.Update(s.circuitStatus)
}

func (s *sidebarStatus) updateAgentsPanel() {
	if s == nil || s.agentsPanelInst == nil {
		return
	}
	s.agentsPanelInst.Update(s.agents)
}

func (s *sidebarStatus) contextRatio() float64 {
	if s == nil {
		return 0
	}
	if s.contextBudget > 0 {
		return float64(s.contextUsed) / float64(s.contextBudget)
	}
	if s.contextWindow > 0 {
		return float64(s.contextUsed) / float64(s.contextWindow)
	}
	return 0
}

func (s *sidebarStatus) contextMaxValue() int {
	if s == nil {
		return 1
	}
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
