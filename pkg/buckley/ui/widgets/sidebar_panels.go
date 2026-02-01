package widgets

import (
	"strings"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

type taskPanel struct {
	label    *uiwidgets.Label
	spinner  *uiwidgets.Spinner
	progress *uiwidgets.Progress
	panel    *uiwidgets.Panel
}

func newTaskPanel(border backend.Style) *taskPanel {
	label := uiwidgets.NewLabel("No active task")
	spinner := uiwidgets.NewSpinner()
	progress := uiwidgets.NewProgress()
	progress.Label = "Task"
	progress.ShowPercent = true
	header := runtime.HBox(
		runtime.Fixed(spinner),
		runtime.Flexible(label, 1),
	).WithGap(1)
	content := runtime.VBox(
		runtime.Fixed(header),
		runtime.Fixed(progress),
	)
	panel := uiwidgets.NewPanel(content).WithBorder(border)
	panel.SetTitle("Current Task")
	return &taskPanel{
		label:    label,
		spinner:  spinner,
		progress: progress,
		panel:    panel,
	}
}

func (p *taskPanel) Panel() *uiwidgets.Panel {
	if p == nil {
		return nil
	}
	return p.panel
}

func (p *taskPanel) SetStyles(border, bg, text backend.Style) {
	if p == nil {
		return
	}
	if p.panel != nil {
		p.panel.SetStyle(bg)
		p.panel.WithBorder(border)
	}
	if p.label != nil {
		p.label.SetStyle(text)
	}
}

func (p *taskPanel) SetSpinnerStyle(style backend.Style) {
	if p == nil || p.spinner == nil {
		return
	}
	p.spinner.SetStyle(style)
}

func (p *taskPanel) Update(name string, progress int) {
	if p == nil {
		return
	}
	label := strings.TrimSpace(name)
	if label == "" {
		label = "No active task"
		if p.spinner != nil {
			p.spinner.Frames = []string{" "}
		}
	} else if p.spinner != nil {
		p.spinner.Frames = []string{"-", "\\", "|", "/"}
	}
	if p.label != nil {
		p.label.SetText(label)
	}
	if p.progress != nil {
		p.progress.Value = float64(clampPercent(progress))
		p.progress.Max = 100
	}
}

func (p *taskPanel) AdvanceSpinner() {
	if p == nil || p.spinner == nil {
		return
	}
	p.spinner.Advance()
}

type planPanel struct {
	progress *uiwidgets.Progress
	table    *InteractiveTable
	panel    *uiwidgets.Panel
}

func newPlanPanel(border backend.Style) *planPanel {
	progress := uiwidgets.NewProgress()
	progress.Label = "Plan"
	progress.ShowPercent = true
	table := NewInteractiveTable(
		uiwidgets.TableColumn{Title: "Task"},
		uiwidgets.TableColumn{Title: "Status"},
	)
	content := runtime.VBox(
		runtime.Fixed(progress),
		runtime.Fixed(table),
	).WithGap(1)
	panel := uiwidgets.NewPanel(content).WithBorder(border)
	panel.SetTitle("Plan")
	return &planPanel{progress: progress, table: table, panel: panel}
}

func (p *planPanel) Panel() *uiwidgets.Panel {
	if p == nil {
		return nil
	}
	return p.panel
}

func (p *planPanel) SetStyles(border, bg backend.Style) {
	if p == nil || p.panel == nil {
		return
	}
	p.panel.SetStyle(bg)
	p.panel.WithBorder(border)
}

func (p *planPanel) Update(tasks []PlanTask) {
	if p == nil {
		return
	}
	if p.progress != nil {
		completed, total := summarizePlan(tasks)
		percent := 0.0
		if total > 0 {
			percent = float64(completed) / float64(total) * 100
		}
		p.progress.Value = percent
		p.progress.Max = 100
	}
	if p.table != nil {
		rows := make([][]string, 0, len(tasks))
		for _, task := range tasks {
			rows = append(rows, []string{task.Name, taskStatusLabel(task.Status)})
		}
		if len(rows) == 0 {
			rows = [][]string{{"No tasks", ""}}
		}
		p.table.SetRows(rows)
	}
}

type toolsPanel struct {
	table *InteractiveTable
	panel *uiwidgets.Panel
}

func newToolsPanel(border backend.Style) *toolsPanel {
	table := NewInteractiveTable(
		uiwidgets.TableColumn{Title: "Tool"},
		uiwidgets.TableColumn{Title: "Status"},
		uiwidgets.TableColumn{Title: "Detail"},
	)
	panel := uiwidgets.NewPanel(table).WithBorder(border)
	panel.SetTitle("Tools")
	return &toolsPanel{table: table, panel: panel}
}

func (p *toolsPanel) Panel() *uiwidgets.Panel {
	if p == nil {
		return nil
	}
	return p.panel
}

func (p *toolsPanel) SetStyles(border, bg backend.Style) {
	if p == nil || p.panel == nil {
		return
	}
	p.panel.SetStyle(bg)
	p.panel.WithBorder(border)
}

func (p *toolsPanel) Update(running []RunningTool, history []ToolHistoryEntry) {
	if p == nil || p.table == nil {
		return
	}
	rows := make([][]string, 0, len(running)+len(history))
	for _, tool := range running {
		detail := strings.TrimSpace(tool.Command)
		rows = append(rows, []string{tool.Name, "running", detail})
	}
	for i := len(history) - 1; i >= 0 && len(rows) < 12; i-- {
		entry := history[i]
		rows = append(rows, []string{entry.Name, entry.Status, entry.Detail})
	}
	if len(rows) == 0 {
		rows = [][]string{{"No tools", "", ""}}
	}
	p.table.SetRows(rows)
}

type contextPanel struct {
	gauge *uiwidgets.AnimatedGauge
	label *uiwidgets.Label
	bar   *uiwidgets.Progress
	grid  *uiwidgets.Grid
	panel *uiwidgets.Panel
}

func newContextPanel(border backend.Style) *contextPanel {
	gauge := uiwidgets.NewAnimatedGauge(0, 1)
	label := uiwidgets.NewLabel("0 / 0")
	bar := uiwidgets.NewProgress()
	bar.Label = "Context"
	bar.ShowPercent = true
	grid := uiwidgets.NewGrid(2, 2)
	grid.Add(gauge, 0, 0, 2, 1)
	grid.Add(label, 0, 1, 1, 1)
	grid.Add(bar, 1, 1, 1, 1)
	panel := uiwidgets.NewPanel(grid).WithBorder(border)
	panel.SetTitle("Context")
	return &contextPanel{
		gauge: gauge,
		label: label,
		bar:   bar,
		grid:  grid,
		panel: panel,
	}
}

func (p *contextPanel) Panel() *uiwidgets.Panel {
	if p == nil {
		return nil
	}
	return p.panel
}

func (p *contextPanel) SetStyles(border, bg, text backend.Style) {
	if p == nil {
		return
	}
	if p.panel != nil {
		p.panel.SetStyle(bg)
		p.panel.WithBorder(border)
	}
	if p.label != nil {
		p.label.SetStyle(text)
	}
}

func (p *contextPanel) SetGaugeStyle(style uiwidgets.GaugeStyle) {
	if p == nil || p.bar == nil {
		return
	}
	p.bar.Style = style
}

func (p *contextPanel) Update(label string, used, max int, ratio float64) {
	if p == nil {
		return
	}
	if p.label != nil {
		p.label.SetText(label)
	}
	if p.bar != nil {
		p.bar.Value = float64(used)
		p.bar.Max = float64(max)
	}
	if p.gauge != nil {
		p.gauge.SetValue(ratio)
	}
}

type experimentPanel struct {
	table *InteractiveTable
	panel *uiwidgets.Panel
}

func newExperimentPanel(border backend.Style) *experimentPanel {
	table := NewInteractiveTable(
		uiwidgets.TableColumn{Title: "Variant"},
		uiwidgets.TableColumn{Title: "Status"},
	)
	panel := uiwidgets.NewPanel(table).WithBorder(border)
	panel.SetTitle("Experiments")
	return &experimentPanel{table: table, panel: panel}
}

func (p *experimentPanel) Panel() *uiwidgets.Panel {
	if p == nil {
		return nil
	}
	return p.panel
}

func (p *experimentPanel) SetStyles(border, bg backend.Style) {
	if p == nil || p.panel == nil {
		return
	}
	p.panel.SetStyle(bg)
	p.panel.WithBorder(border)
}

func (p *experimentPanel) Update(name, status string, variants []ExperimentVariant) {
	if p == nil || p.table == nil {
		return
	}
	rows := make([][]string, 0, len(variants)+1)
	if strings.TrimSpace(name) != "" {
		rows = append(rows, []string{name, status})
	}
	for _, variant := range variants {
		rows = append(rows, []string{variant.Name, variant.Status})
	}
	if len(rows) == 0 {
		rows = [][]string{{"No experiments", ""}}
	}
	p.table.SetRows(rows)
}

type rlmPanel struct {
	table *InteractiveTable
	panel *uiwidgets.Panel
}

func newRLMPanel(border backend.Style) *rlmPanel {
	table := NewInteractiveTable(
		uiwidgets.TableColumn{Title: "Key"},
		uiwidgets.TableColumn{Title: "Type"},
		uiwidgets.TableColumn{Title: "Summary"},
	)
	panel := uiwidgets.NewPanel(table).WithBorder(border)
	panel.SetTitle("RLM")
	return &rlmPanel{table: table, panel: panel}
}

func (p *rlmPanel) Panel() *uiwidgets.Panel {
	if p == nil {
		return nil
	}
	return p.panel
}

func (p *rlmPanel) SetStyles(border, bg backend.Style) {
	if p == nil || p.panel == nil {
		return
	}
	p.panel.SetStyle(bg)
	p.panel.WithBorder(border)
}

func (p *rlmPanel) Update(status *RLMStatus, scratchpad []RLMScratchpadEntry) {
	if p == nil || p.table == nil {
		return
	}
	rows := make([][]string, 0, len(scratchpad)+1)
	if status != nil {
		summary := status.Summary
		if summary == "" {
			summary = "Iteration " + intToStr(status.Iteration)
		}
		rows = append(rows, []string{"Status", "", summary})
	}
	for _, entry := range scratchpad {
		rows = append(rows, []string{entry.Key, entry.Type, entry.Summary})
	}
	if len(rows) == 0 {
		rows = [][]string{{"No entries", "", ""}}
	}
	p.table.SetRows(rows)
}

type circuitPanel struct {
	alert *uiwidgets.Alert
	panel *uiwidgets.Panel
}

func newCircuitPanel(border backend.Style) *circuitPanel {
	alert := uiwidgets.NewAlert("All systems nominal", uiwidgets.AlertSuccess)
	panel := uiwidgets.NewPanel(alert).WithBorder(border)
	panel.SetTitle("Circuit")
	return &circuitPanel{alert: alert, panel: panel}
}

func (p *circuitPanel) Panel() *uiwidgets.Panel {
	if p == nil {
		return nil
	}
	return p.panel
}

func (p *circuitPanel) SetStyles(border, bg backend.Style) {
	if p == nil || p.panel == nil {
		return
	}
	p.panel.SetStyle(bg)
	p.panel.WithBorder(border)
}

func (p *circuitPanel) Update(status *CircuitStatus) {
	if p == nil || p.alert == nil {
		return
	}
	if status == nil || status.State == "" {
		p.alert.Text = "All systems nominal"
		p.alert.Variant = uiwidgets.AlertSuccess
		return
	}
	message := status.State
	if status.LastError != "" {
		message = status.State + ": " + status.LastError
	}
	variant := uiwidgets.AlertInfo
	switch strings.ToLower(status.State) {
	case "open":
		variant = uiwidgets.AlertError
	case "halfopen", "half-open":
		variant = uiwidgets.AlertWarning
	}
	p.alert.Text = message
	p.alert.Variant = variant
}

type calendarPanel struct {
	calendar *uiwidgets.Calendar
	panel    *uiwidgets.Panel
}

func newCalendarPanel(border backend.Style) *calendarPanel {
	calendar := uiwidgets.NewCalendar()
	panel := uiwidgets.NewPanel(calendar).WithBorder(border)
	panel.SetTitle("Schedule")
	return &calendarPanel{calendar: calendar, panel: panel}
}

func (p *calendarPanel) Panel() *uiwidgets.Panel {
	if p == nil {
		return nil
	}
	return p.panel
}

func (p *calendarPanel) SetStyles(border, bg backend.Style) {
	if p == nil || p.panel == nil {
		return
	}
	p.panel.SetStyle(bg)
	p.panel.WithBorder(border)
}

type breadcrumbPanel struct {
	breadcrumb *uiwidgets.Breadcrumb
	panel      *uiwidgets.Panel
}

func newBreadcrumbPanel(border backend.Style) *breadcrumbPanel {
	breadcrumb := uiwidgets.NewBreadcrumb(uiwidgets.BreadcrumbItem{Label: "Project"})
	panel := uiwidgets.NewPanel(breadcrumb).WithBorder(border)
	panel.SetTitle("Path")
	return &breadcrumbPanel{breadcrumb: breadcrumb, panel: panel}
}

func (p *breadcrumbPanel) Panel() *uiwidgets.Panel {
	if p == nil {
		return nil
	}
	return p.panel
}

func (p *breadcrumbPanel) SetStyles(border, bg backend.Style) {
	if p == nil || p.panel == nil {
		return
	}
	p.panel.SetStyle(bg)
	p.panel.WithBorder(border)
}

func (p *breadcrumbPanel) Update(path string) {
	if p == nil || p.breadcrumb == nil {
		return
	}
	if path == "" {
		path = "Project"
	}
	parts := splitPath(path)
	items := make([]uiwidgets.BreadcrumbItem, 0, len(parts))
	for _, part := range parts {
		items = append(items, uiwidgets.BreadcrumbItem{Label: part})
	}
	p.breadcrumb.Items = items
}

type filesPanel struct {
	tree  *InteractiveTree
	panel *uiwidgets.Panel
}

func newFilesPanel(border backend.Style) *filesPanel {
	tree := NewInteractiveTree(&uiwidgets.TreeNode{Label: "(no files)"})
	panel := uiwidgets.NewPanel(tree).WithBorder(border)
	panel.SetTitle("Files")
	return &filesPanel{tree: tree, panel: panel}
}

func (p *filesPanel) Panel() *uiwidgets.Panel {
	if p == nil {
		return nil
	}
	return p.panel
}

func (p *filesPanel) SetStyles(border, bg backend.Style) {
	if p == nil || p.panel == nil {
		return
	}
	p.panel.SetStyle(bg)
	p.panel.WithBorder(border)
}

func (p *filesPanel) Update(paths []string, projectPath string) {
	if p == nil || p.tree == nil {
		return
	}
	root := buildTreeFromPaths(paths, projectPath)
	p.tree.SetRoot(root)
}

type touchesPanel struct {
	tree  *InteractiveTree
	panel *uiwidgets.Panel
}

func newTouchesPanel(border backend.Style) *touchesPanel {
	tree := NewInteractiveTree(&uiwidgets.TreeNode{Label: "(no touches)"})
	panel := uiwidgets.NewPanel(tree).WithBorder(border)
	panel.SetTitle("Touches")
	return &touchesPanel{tree: tree, panel: panel}
}

func (p *touchesPanel) Panel() *uiwidgets.Panel {
	if p == nil {
		return nil
	}
	return p.panel
}

func (p *touchesPanel) SetStyles(border, bg backend.Style) {
	if p == nil || p.panel == nil {
		return
	}
	p.panel.SetStyle(bg)
	p.panel.WithBorder(border)
}

func (p *touchesPanel) Update(touches []TouchSummary) {
	if p == nil || p.tree == nil {
		return
	}
	root := buildTouchesTree(touches)
	p.tree.SetRoot(root)
}
