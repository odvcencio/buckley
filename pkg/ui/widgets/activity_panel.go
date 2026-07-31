package widgets

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/terminal"
)

// ActivityStatus describes the lifecycle state of an inspectable activity.
type ActivityStatus string

const (
	ActivityRunning   ActivityStatus = "running"
	ActivityCompleted ActivityStatus = "completed"
	ActivityFailed    ActivityStatus = "failed"
	ActivityCancelled ActivityStatus = "cancelled"
)

// ActivityRecord is the compact UI representation of a tool call, subagent,
// document read, edit, or other inspectable work item.
type ActivityRecord struct {
	ID         string
	Kind       string
	Title      string
	Summary    string
	Detail     string
	Path       string
	Operation  string
	Status     ActivityStatus
	StartedAt  time.Time
	FinishedAt time.Time
}

// ActivityPanel is a persistent, clickable inspector for detailed work logs.
// It intentionally keeps verbose output out of the chat transcript.
type ActivityPanel struct {
	FocusableBase

	records      []ActivityRecord
	selected     int
	listOffset   int
	detailOffset int
	showDetail   bool

	borderStyle   backend.Style
	headerStyle   backend.Style
	textStyle     backend.Style
	selectedStyle backend.Style
	mutedStyle    backend.Style
}

// NewActivityPanel creates an empty activity inspector.
func NewActivityPanel() *ActivityPanel {
	return &ActivityPanel{
		selected:      -1,
		borderStyle:   backend.DefaultStyle(),
		headerStyle:   backend.DefaultStyle().Bold(true),
		textStyle:     backend.DefaultStyle(),
		selectedStyle: backend.DefaultStyle().Reverse(true),
		mutedStyle:    backend.DefaultStyle().Dim(true),
	}
}

// SetStyles configures inspector presentation.
func (p *ActivityPanel) SetStyles(border, header, text, selected, muted backend.Style) {
	p.borderStyle = border
	p.headerStyle = header
	p.textStyle = text
	p.selectedStyle = selected
	p.mutedStyle = muted
}

// HasContent reports whether the panel has inspectable activity.
func (p *ActivityPanel) HasContent() bool {
	return p != nil && len(p.records) > 0
}

// SetActivities replaces the current activity snapshot while preserving the
// selected record when possible.
func (p *ActivityPanel) SetActivities(records []ActivityRecord) {
	if p == nil {
		return
	}
	selectedID := ""
	if p.selected >= 0 && p.selected < len(p.records) {
		selectedID = p.records[p.selected].ID
	}
	p.records = append(p.records[:0], records...)
	sort.SliceStable(p.records, func(i, j int) bool {
		if p.records[i].Status == ActivityRunning && p.records[j].Status != ActivityRunning {
			return true
		}
		if p.records[i].Status != ActivityRunning && p.records[j].Status == ActivityRunning {
			return false
		}
		return p.records[i].StartedAt.After(p.records[j].StartedAt)
	})
	p.selected = -1
	for i := range p.records {
		if p.records[i].ID == selectedID {
			p.selected = i
			break
		}
	}
	if len(p.records) == 0 {
		p.showDetail = false
		p.listOffset = 0
		p.detailOffset = 0
	} else if p.selected < 0 {
		p.selected = 0
	}
	p.clampOffsets()
}

// Measure requests a useful inspector width while respecting layout bounds.
func (p *ActivityPanel) Measure(constraints runtime.Constraints) runtime.Size {
	width := 40
	if constraints.MaxWidth < width {
		width = constraints.MaxWidth
	}
	return runtime.Size{Width: width, Height: constraints.MaxHeight}
}

// Layout stores the assigned bounds.
func (p *ActivityPanel) Layout(bounds runtime.Rect) {
	p.bounds = bounds
	p.clampOffsets()
}

// Render draws either the activity list or the selected activity detail.
func (p *ActivityPanel) Render(ctx runtime.RenderContext) {
	b := p.bounds
	if b.Width < 12 || b.Height < 3 {
		return
	}
	ctx.Buffer.Fill(b, ' ', p.textStyle)
	for y := b.Y; y < b.Y+b.Height; y++ {
		ctx.Buffer.Set(b.X, y, '│', p.borderStyle)
	}
	if p.showDetail && p.selected >= 0 && p.selected < len(p.records) {
		p.renderDetail(ctx.Buffer)
		return
	}
	p.renderList(ctx.Buffer)
}

func (p *ActivityPanel) renderList(buf *runtime.Buffer) {
	b := p.bounds
	x := b.X + 2
	width := b.Width - 3
	buf.SetString(x, b.Y, "Activity", p.headerStyle)
	if len(p.records) == 0 {
		buf.SetString(x, b.Y+2, truncateString("No detailed activity yet", width), p.mutedStyle)
		return
	}
	buf.SetString(x, b.Y+1, truncateString("click or Enter to inspect", width), p.mutedStyle)
	maxRows := maxActivityPanel(0, b.Height-3)
	p.ensureSelectionVisible(maxRows)
	for row := 0; row < maxRows; row++ {
		idx := p.listOffset + row
		if idx >= len(p.records) {
			break
		}
		record := p.records[idx]
		style := p.textStyle
		if idx == p.selected {
			style = p.selectedStyle
		}
		label := fmt.Sprintf("%s %s", activityStatusGlyph(record.Status), activityRecordLabel(record))
		buf.SetString(x, b.Y+3+row, truncateString(label, width), style)
	}
}

func (p *ActivityPanel) renderDetail(buf *runtime.Buffer) {
	b := p.bounds
	x := b.X + 2
	width := b.Width - 3
	record := p.records[p.selected]
	buf.SetString(x, b.Y, truncateString("← "+activityRecordLabel(record), width), p.headerStyle)
	lines := activityDetailLines(record, width)
	visible := maxActivityPanel(0, b.Height-2)
	maxOffset := maxActivityPanel(0, len(lines)-visible)
	if p.detailOffset > maxOffset {
		p.detailOffset = maxOffset
	}
	for row := 0; row < visible; row++ {
		idx := p.detailOffset + row
		if idx >= len(lines) {
			break
		}
		style := p.textStyle
		if strings.HasPrefix(lines[idx], "Status:") || strings.HasSuffix(lines[idx], ":") {
			style = p.headerStyle
		}
		buf.SetString(x, b.Y+2+row, truncateString(lines[idx], width), style)
	}
}

func activityRecordLabel(record ActivityRecord) string {
	label := strings.TrimSpace(record.Title)
	if label == "" {
		label = strings.TrimSpace(record.Kind)
	}
	if label == "" {
		label = "activity"
	}
	return label
}

func activityStatusGlyph(status ActivityStatus) string {
	switch status {
	case ActivityRunning:
		return "◉"
	case ActivityCompleted:
		return "✓"
	case ActivityFailed:
		return "✗"
	case ActivityCancelled:
		return "–"
	default:
		return "○"
	}
}

func activityDetailLines(record ActivityRecord, width int) []string {
	lines := []string{"Status: " + string(record.Status)}
	if summary := strings.TrimSpace(record.Summary); summary != "" {
		lines = append(lines, wrapActivityText(summary, width)...)
	}
	if record.Path != "" {
		lines = append(lines, "", "Path:")
		lines = append(lines, wrapActivityText(record.Path, width)...)
	}
	if record.Operation != "" {
		lines = append(lines, "Operation: "+record.Operation)
	}
	if !record.StartedAt.IsZero() {
		lines = append(lines, "Started: "+record.StartedAt.Format("15:04:05"))
	}
	if !record.FinishedAt.IsZero() {
		lines = append(lines, "Finished: "+record.FinishedAt.Format("15:04:05"))
		if !record.StartedAt.IsZero() {
			lines = append(lines, "Duration: "+record.FinishedAt.Sub(record.StartedAt).Round(time.Millisecond).String())
		}
	}
	if detail := strings.TrimSpace(record.Detail); detail != "" {
		lines = append(lines, "", "Details:")
		for _, line := range strings.Split(detail, "\n") {
			if strings.TrimSpace(line) == "" {
				lines = append(lines, "")
				continue
			}
			lines = append(lines, wrapActivityText(line, width)...)
		}
	}
	return lines
}

func wrapActivityText(text string, width int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	if width < 4 {
		return []string{truncateString(text, width)}
	}
	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		line := words[0]
		for _, word := range words[1:] {
			if displayWidth(line)+1+displayWidth(word) <= width {
				line += " " + word
				continue
			}
			lines = append(lines, line)
			line = word
		}
		lines = append(lines, line)
	}
	return lines
}

// HandleMessage supports keyboard navigation, mouse selection, and detail
// scrolling. Clicking a list row opens its detail inline.
func (p *ActivityPanel) HandleMessage(msg runtime.Message) runtime.HandleResult {
	switch m := msg.(type) {
	case runtime.MouseMsg:
		return p.handleMouse(m)
	case runtime.KeyMsg:
		return p.handleKey(m)
	default:
		return runtime.Unhandled()
	}
}

func (p *ActivityPanel) handleMouse(msg runtime.MouseMsg) runtime.HandleResult {
	if !p.bounds.Contains(msg.X, msg.Y) {
		return runtime.Unhandled()
	}
	if msg.Button == runtime.MouseWheelUp {
		p.scroll(-3)
		return runtime.Handled()
	}
	if msg.Button == runtime.MouseWheelDown {
		p.scroll(3)
		return runtime.Handled()
	}
	if msg.Button != runtime.MouseLeft || msg.Action != runtime.MousePress {
		return runtime.Unhandled()
	}
	if p.showDetail {
		if msg.Y <= p.bounds.Y+1 {
			p.showDetail = false
			p.detailOffset = 0
		}
		return runtime.Handled()
	}
	row := msg.Y - (p.bounds.Y + 3)
	if row < 0 {
		return runtime.Handled()
	}
	idx := p.listOffset + row
	if idx >= 0 && idx < len(p.records) {
		p.selected = idx
		p.showDetail = true
		p.detailOffset = 0
	}
	return runtime.Handled()
}

func (p *ActivityPanel) handleKey(key runtime.KeyMsg) runtime.HandleResult {
	if p.showDetail {
		switch key.Key {
		case terminal.KeyEscape, terminal.KeyLeft:
			p.showDetail = false
			p.detailOffset = 0
			return runtime.Handled()
		case terminal.KeyUp:
			p.scroll(-1)
			return runtime.Handled()
		case terminal.KeyDown:
			p.scroll(1)
			return runtime.Handled()
		case terminal.KeyPageUp:
			p.scroll(-maxActivityPanel(1, p.bounds.Height-3))
			return runtime.Handled()
		case terminal.KeyPageDown:
			p.scroll(maxActivityPanel(1, p.bounds.Height-3))
			return runtime.Handled()
		}
		return runtime.Unhandled()
	}
	if len(p.records) == 0 {
		return runtime.Unhandled()
	}
	switch key.Key {
	case terminal.KeyUp:
		if p.selected > 0 {
			p.selected--
		}
		p.ensureSelectionVisible(maxActivityPanel(1, p.bounds.Height-3))
		return runtime.Handled()
	case terminal.KeyDown:
		if p.selected < len(p.records)-1 {
			p.selected++
		}
		p.ensureSelectionVisible(maxActivityPanel(1, p.bounds.Height-3))
		return runtime.Handled()
	case terminal.KeyEnter, terminal.KeyRight:
		p.showDetail = true
		p.detailOffset = 0
		return runtime.Handled()
	}
	return runtime.Unhandled()
}

func (p *ActivityPanel) scroll(delta int) {
	if p.showDetail {
		p.detailOffset += delta
		if p.detailOffset < 0 {
			p.detailOffset = 0
		}
		p.clampOffsets()
		return
	}
	p.listOffset += delta
	p.clampOffsets()
}

func (p *ActivityPanel) ensureSelectionVisible(visible int) {
	if p.selected < 0 || visible <= 0 {
		return
	}
	if p.selected < p.listOffset {
		p.listOffset = p.selected
	}
	if p.selected >= p.listOffset+visible {
		p.listOffset = p.selected - visible + 1
	}
	p.clampOffsets()
}

func (p *ActivityPanel) clampOffsets() {
	visible := maxActivityPanel(1, p.bounds.Height-3)
	maxList := maxActivityPanel(0, len(p.records)-visible)
	if p.listOffset > maxList {
		p.listOffset = maxList
	}
	if p.listOffset < 0 {
		p.listOffset = 0
	}
	if p.showDetail && p.selected >= 0 && p.selected < len(p.records) {
		lines := activityDetailLines(p.records[p.selected], maxActivityPanel(1, p.bounds.Width-3))
		maxDetail := maxActivityPanel(0, len(lines)-maxActivityPanel(1, p.bounds.Height-2))
		if p.detailOffset > maxDetail {
			p.detailOffset = maxDetail
		}
	}
	if p.detailOffset < 0 {
		p.detailOffset = 0
	}
}

func maxActivityPanel(a, b int) int {
	if a > b {
		return a
	}
	return b
}
