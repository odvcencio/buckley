package widgets

import (
	"strings"

	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/terminal"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// DiffLine represents a single line in a diff preview.
type DiffLine struct {
	Type    DiffLineType // Add, Remove, Context
	Content string
}

// DiffLineType indicates the type of diff line.
type DiffLineType int

const (
	DiffContext DiffLineType = iota
	DiffAdd
	DiffRemove
)

// ApprovalRequest contains all information needed to display an approval dialog.
type ApprovalRequest struct {
	ID           string     // Unique identifier for this request
	Tool         string     // Tool name (e.g., "run_shell", "write_file")
	Operation    string     // Operation type (e.g., "shell:write", "write")
	Description  string     // Human-readable explanation
	Command      string     // For shell operations, the command to run
	FilePath     string     // For file operations, the target path
	DiffLines    []DiffLine // For file edits, the diff preview
	AddedLines   int        // Lines added (for diff summary)
	RemovedLines int        // Lines removed (for diff summary)
}

// ApprovalResponse captures the user's decision on a tool approval request.
type ApprovalResponse struct {
	RequestID   string // ID of the original request
	Approved    bool   // Whether the operation was approved
	AlwaysAllow bool   // Remember this decision for the tool
}

func (ApprovalResponse) Command() {}

// approvalResult builds a HandleResult that emits an ApprovalResponse and pops the overlay.
func approvalResult(requestID string, approved, alwaysAllow bool) runtime.HandleResult {
	return runtime.WithCommands(
		ApprovalResponse{
			RequestID:   requestID,
			Approved:    approved,
			AlwaysAllow: alwaysAllow,
		},
		runtime.PopOverlay{},
	)
}

// ApprovalWidget displays a modal dialog for tool approval.
type ApprovalWidget struct {
	uiwidgets.FocusableBase

	services         runtime.Services
	request          ApprovalRequest
	scrollOffset     int // For scrolling through diff
	diffVisibleLines int // Visible diff lines from last layout

	// Styles
	bgStyle       backend.Style
	borderStyle   backend.Style
	titleStyle    backend.Style
	textStyle     backend.Style
	labelStyle    backend.Style
	commandStyle  backend.Style
	addStyle      backend.Style
	removeStyle   backend.Style
	contextStyle  backend.Style
	buttonStyle   backend.Style
	buttonHlStyle backend.Style
}

type approvalButtonSpec struct {
	label  string
	action string
}

type approvalButtonRange struct {
	action string
	start  int
	end    int
}

var approvalButtons = []approvalButtonSpec{
	{label: "[A]llow", action: "allow"},
	{label: "[D]eny", action: "deny"},
	{label: "A[l]ways allow", action: "always"},
}

const approvalButtonSep = "    "

// NewApprovalWidget creates a new approval dialog widget.
func NewApprovalWidget(req ApprovalRequest) *ApprovalWidget {
	w := &ApprovalWidget{
		request:       req,
		bgStyle:       backend.DefaultStyle(),
		borderStyle:   backend.DefaultStyle(),
		titleStyle:    backend.DefaultStyle().Bold(true),
		textStyle:     backend.DefaultStyle(),
		labelStyle:    backend.DefaultStyle().Bold(true),
		commandStyle:  backend.DefaultStyle(),
		addStyle:      backend.DefaultStyle().Foreground(backend.ColorGreen),
		removeStyle:   backend.DefaultStyle().Foreground(backend.ColorRed),
		contextStyle:  backend.DefaultStyle().Foreground(backend.ColorDefault),
		buttonStyle:   backend.DefaultStyle(),
		buttonHlStyle: backend.DefaultStyle().Bold(true).Reverse(true),
	}
	w.Base.Role = accessibility.RoleAlert
	w.Base.Live = accessibility.LiveAssertive
	return w
}

// SetStyles configures the widget appearance.
func (a *ApprovalWidget) SetStyles(bg, border, title, text backend.Style) {
	if a == nil {
		return
	}
	a.bgStyle = bg
	a.borderStyle = border
	a.titleStyle = title
	a.textStyle = text
}

// Measure returns the preferred size for the dialog.
func (a *ApprovalWidget) Measure(constraints runtime.Constraints) runtime.Size {
	// Dialog width: 60-70 chars, height depends on content
	width := 65
	if constraints.MaxWidth < width {
		width = constraints.MaxWidth
	}

	// Base height: title(1) + border(2) + tool/op info(3) + buttons(2) + padding(2)
	height := 10

	// Add height for command preview
	if a.request.Command != "" {
		height += 4 // label + border + command + border
	}

	// Add height for diff preview (max 10 lines shown)
	if len(a.request.DiffLines) > 0 {
		diffHeight := len(a.request.DiffLines)
		if diffHeight > 10 {
			diffHeight = 10
		}
		height += diffHeight + 4 // label + border + lines + border + summary
	}

	if constraints.MaxHeight < height {
		height = constraints.MaxHeight
	}

	return runtime.Size{Width: width, Height: height}
}

// Layout positions the widget centered in bounds.
func (a *ApprovalWidget) Layout(bounds runtime.Rect) {
	size := a.Measure(runtime.Constraints{
		MaxWidth:  bounds.Width,
		MaxHeight: bounds.Height,
	})

	x := bounds.X + (bounds.Width-size.Width)/2
	y := bounds.Y + (bounds.Height-size.Height)/2

	newBounds := runtime.Rect{
		X:      x,
		Y:      y,
		Width:  size.Width,
		Height: size.Height,
	}
	a.FocusableBase.Layout(newBounds)
}

// Render draws the approval dialog.
func (a *ApprovalWidget) Render(ctx runtime.RenderContext) {
	b := a.Bounds()
	if b.Width < 20 || b.Height < 8 {
		return
	}

	// Draw background
	ctx.Buffer.Fill(b, ' ', a.bgStyle)

	// Draw border
	a.drawBorder(ctx.Buffer, b)

	// Draw warning title
	title := " Approval Required "
	titleX := b.X + (b.Width-textWidth(title))/2
	ctx.Buffer.SetString(titleX, b.Y, title, a.titleStyle)

	y := b.Y + 2

	// Draw tool and operation info
	toolLabel := "Tool: "
	ctx.Buffer.SetString(b.X+2, y, toolLabel, a.labelStyle)
	ctx.Buffer.SetString(b.X+2+textWidth(toolLabel), y, a.request.Tool, a.textStyle)
	y++

	opLabel := "Operation: "
	ctx.Buffer.SetString(b.X+2, y, opLabel, a.labelStyle)
	ctx.Buffer.SetString(b.X+2+textWidth(opLabel), y, a.request.Operation, a.textStyle)
	y++

	// Draw description if present
	if a.request.Description != "" {
		y++
		desc := a.request.Description
		maxLen := b.Width - 4
		if textWidth(desc) > maxLen {
			desc = truncateString(desc, maxLen)
		}
		ctx.Buffer.SetString(b.X+2, y, desc, a.textStyle)
		y++
	}

	// Draw command preview for shell operations
	if a.request.Command != "" {
		y++
		ctx.Buffer.SetString(b.X+2, y, "Command:", a.labelStyle)
		y++
		a.drawCommandBox(ctx.Buffer, b.X+2, y, b.Width-4, a.request.Command)
		y += 3
	}

	// Draw diff preview for file operations
	if len(a.request.DiffLines) > 0 {
		y++
		fileLabel := "File: "
		ctx.Buffer.SetString(b.X+2, y, fileLabel, a.labelStyle)
		ctx.Buffer.SetString(b.X+2+textWidth(fileLabel), y, a.request.FilePath, a.textStyle)
		y++
		y++
		ctx.Buffer.SetString(b.X+2, y, "Changes:", a.labelStyle)
		y++
		y = a.drawDiffPreview(ctx.Buffer, b.X+2, y, b.Width-4, b.Y+b.Height-4-y)

		// Draw diff summary
		summary := formatDiffSummary(a.request.AddedLines, a.request.RemovedLines)
		ctx.Buffer.SetString(b.X+2, y, summary, a.textStyle)
		// y++ not needed - last line before buttons
	}

	// Draw buttons at bottom
	a.drawButtons(ctx.Buffer, b)
}

// drawBorder draws the dialog border.
func (a *ApprovalWidget) drawBorder(buf *runtime.Buffer, b runtime.Rect) {
	// Corners
	buf.Set(b.X, b.Y, '╭', a.borderStyle)
	buf.Set(b.X+b.Width-1, b.Y, '╮', a.borderStyle)
	buf.Set(b.X, b.Y+b.Height-1, '╰', a.borderStyle)
	buf.Set(b.X+b.Width-1, b.Y+b.Height-1, '╯', a.borderStyle)

	// Horizontal edges
	for x := b.X + 1; x < b.X+b.Width-1; x++ {
		buf.Set(x, b.Y, '─', a.borderStyle)
		buf.Set(x, b.Y+b.Height-1, '─', a.borderStyle)
	}

	// Vertical edges
	for y := b.Y + 1; y < b.Y+b.Height-1; y++ {
		buf.Set(b.X, y, '│', a.borderStyle)
		buf.Set(b.X+b.Width-1, y, '│', a.borderStyle)
	}
}

// drawCommandBox draws a command in a styled box.
func (a *ApprovalWidget) drawCommandBox(buf *runtime.Buffer, x, y, width int, cmd string) {
	// Top border
	buf.Set(x, y, '┌', a.borderStyle)
	for i := 1; i < width-1; i++ {
		buf.Set(x+i, y, '─', a.borderStyle)
	}
	buf.Set(x+width-1, y, '┐', a.borderStyle)

	// Command content (truncate if needed)
	y++
	buf.Set(x, y, '│', a.borderStyle)
	cmdDisplay := cmd
	maxCmd := width - 4
	if textWidth(cmdDisplay) > maxCmd {
		cmdDisplay = truncateString(cmdDisplay, maxCmd)
	}
	buf.SetString(x+2, y, cmdDisplay, a.commandStyle)
	buf.Set(x+width-1, y, '│', a.borderStyle)

	// Bottom border
	y++
	buf.Set(x, y, '└', a.borderStyle)
	for i := 1; i < width-1; i++ {
		buf.Set(x+i, y, '─', a.borderStyle)
	}
	buf.Set(x+width-1, y, '┘', a.borderStyle)
}

// drawDiffPreview renders the diff lines in a scrollable area.
func (a *ApprovalWidget) drawDiffPreview(buf *runtime.Buffer, x, y, width, maxLines int) int {
	if maxLines <= 0 {
		return y
	}

	// Top border
	buf.Set(x, y, '┌', a.borderStyle)
	for i := 1; i < width-1; i++ {
		buf.Set(x+i, y, '─', a.borderStyle)
	}
	buf.Set(x+width-1, y, '┐', a.borderStyle)
	y++

	// Draw visible lines
	visibleLines := maxLines - 2 // Account for borders
	if visibleLines > len(a.request.DiffLines) {
		visibleLines = len(a.request.DiffLines)
	}
	a.diffVisibleLines = visibleLines

	startIdx := a.scrollOffset
	if startIdx+visibleLines > len(a.request.DiffLines) {
		startIdx = len(a.request.DiffLines) - visibleLines
		if startIdx < 0 {
			startIdx = 0
		}
	}

	for i := 0; i < visibleLines; i++ {
		lineIdx := startIdx + i
		if lineIdx >= len(a.request.DiffLines) {
			break
		}

		line := a.request.DiffLines[lineIdx]
		buf.Set(x, y, '│', a.borderStyle)

		// Determine prefix and style
		var prefix string
		var style backend.Style
		switch line.Type {
		case DiffAdd:
			prefix = "+ "
			style = a.addStyle
		case DiffRemove:
			prefix = "- "
			style = a.removeStyle
		default:
			prefix = "  "
			style = a.contextStyle
		}

		// Draw line content
		content := prefix + line.Content
		maxContent := width - 3
		if textWidth(content) > maxContent {
			content = truncateString(content, maxContent)
		}
		buf.SetString(x+1, y, content, style)
		buf.Set(x+width-1, y, '│', a.borderStyle)
		y++
	}

	// Bottom border
	buf.Set(x, y, '└', a.borderStyle)
	for i := 1; i < width-1; i++ {
		buf.Set(x+i, y, '─', a.borderStyle)
	}
	buf.Set(x+width-1, y, '┘', a.borderStyle)
	y++

	return y
}

// drawButtons renders the action buttons at the bottom.
func (a *ApprovalWidget) drawButtons(buf *runtime.Buffer, b runtime.Rect) {
	y := b.Y + b.Height - 2

	buttons, _ := approvalButtonLayout()
	x := b.X + (b.Width-textWidth(buttons))/2

	// Draw with highlights on hotkey letters (characters inside brackets)
	inBracket := false
	for i, ch := range buttons {
		style := a.buttonStyle
		if ch == '[' {
			inBracket = true
			buf.Set(x+i, y, ch, style)
			continue
		}
		if ch == ']' {
			inBracket = false
			buf.Set(x+i, y, ch, style)
			continue
		}
		if inBracket {
			style = a.buttonHlStyle
		}
		buf.Set(x+i, y, ch, style)
	}
}

func approvalButtonLayout() (string, []approvalButtonRange) {
	line := ""
	ranges := make([]approvalButtonRange, 0, len(approvalButtons))
	pos := 0
	for i, spec := range approvalButtons {
		if i > 0 {
			line += approvalButtonSep
			pos += len(approvalButtonSep)
		}
		start := pos
		line += spec.label
		pos += len(spec.label)
		ranges = append(ranges, approvalButtonRange{
			action: spec.action,
			start:  start,
			end:    pos,
		})
	}
	return line, ranges
}

// HandleMessage processes keyboard input.
func (a *ApprovalWidget) HandleMessage(msg runtime.Message) runtime.HandleResult {
	if mouse, ok := msg.(runtime.MouseMsg); ok {
		return a.handleMouse(mouse)
	}
	key, ok := msg.(runtime.KeyMsg)
	if !ok {
		return runtime.Unhandled()
	}
	if !a.IsFocused() {
		return runtime.Unhandled()
	}

	switch key.Key {
	case terminal.KeyEscape:
		// Escape = Deny
		return approvalResult(a.request.ID, false, false)

	case terminal.KeyUp:
		a.scrollDiff(-1)
		return runtime.Handled()

	case terminal.KeyDown:
		a.scrollDiff(1)
		return runtime.Handled()

	case terminal.KeyRune:
		switch key.Rune {
		case 'a', 'A', 'y', 'Y': // Allow / Yes
			return approvalResult(a.request.ID, true, false)

		case 'd', 'D', 'n', 'N': // Deny / No
			return approvalResult(a.request.ID, false, false)

		case 'l', 'L': // Always allow
			return approvalResult(a.request.ID, true, true)
		}
	}

	return runtime.Unhandled()
}

func (a *ApprovalWidget) handleMouse(msg runtime.MouseMsg) runtime.HandleResult {
	if a == nil {
		return runtime.Unhandled()
	}
	bounds := a.Bounds()
	if !bounds.Contains(msg.X, msg.Y) {
		return runtime.Unhandled()
	}
	switch msg.Button {
	case runtime.MouseWheelUp:
		a.scrollDiff(-1)
		return runtime.Handled()
	case runtime.MouseWheelDown:
		a.scrollDiff(1)
		return runtime.Handled()
	case runtime.MouseLeft:
		if msg.Action != runtime.MousePress {
			return runtime.Unhandled()
		}
		if !a.IsFocused() {
			a.Focus()
		}
		action, ok := a.buttonActionAt(msg.X, msg.Y)
		if !ok {
			return runtime.Handled()
		}
		switch action {
		case "allow":
			return approvalResult(a.request.ID, true, false)
		case "deny":
			return approvalResult(a.request.ID, false, false)
		case "always":
			return approvalResult(a.request.ID, true, true)
		}
		return runtime.Handled()
	}
	return runtime.Unhandled()
}

func (a *ApprovalWidget) buttonActionAt(x, y int) (string, bool) {
	b := a.Bounds()
	line, ranges := approvalButtonLayout()
	if len(line) == 0 {
		return "", false
	}
	buttonY := b.Y + b.Height - 2
	if y != buttonY {
		return "", false
	}
	startX := b.X + (b.Width-textWidth(line))/2
	if x < startX || x >= startX+textWidth(line) {
		return "", false
	}
	rel := x - startX
	for _, r := range ranges {
		if rel >= r.start && rel < r.end {
			return r.action, true
		}
	}
	return "", false
}

func (a *ApprovalWidget) scrollDiff(delta int) {
	if a == nil || delta == 0 || len(a.request.DiffLines) == 0 {
		return
	}
	visible := a.diffVisibleLines
	if visible <= 0 {
		visible = 5
	}
	maxScroll := len(a.request.DiffLines) - visible
	if maxScroll < 0 {
		maxScroll = 0
	}
	next := a.scrollOffset + delta
	if next < 0 {
		next = 0
	}
	if next > maxScroll {
		next = maxScroll
	}
	if next != a.scrollOffset {
		a.scrollOffset = next
		a.Invalidate()
	}
}

// formatDiffSummary creates a summary string for diff stats.
func formatDiffSummary(added, removed int) string {
	var parts []string
	if added > 0 {
		parts = append(parts, "+"+intToStr(added)+" lines")
	}
	if removed > 0 {
		parts = append(parts, "-"+intToStr(removed)+" lines")
	}
	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, ", ")
}

// Bind attaches app services and announces the approval request.
func (a *ApprovalWidget) Bind(services runtime.Services) {
	if a == nil {
		return
	}
	a.services = services
	if announcer := services.Announcer(); announcer != nil {
		msg := "Tool approval required"
		if a.request.Tool != "" {
			msg += ": " + a.request.Tool
		}
		announcer.Announce(msg, accessibility.PriorityAssertive)
	}
}

// Unbind releases app services.
func (a *ApprovalWidget) Unbind() {
	if a == nil {
		return
	}
	a.services = runtime.Services{}
}

func (a *ApprovalWidget) AccessibleRole() accessibility.Role {
	return accessibility.RoleDialog
}

func (a *ApprovalWidget) AccessibleLabel() string {
	return "Approval request"
}

func (a *ApprovalWidget) AccessibleDescription() string {
	if strings.TrimSpace(a.request.Tool) == "" {
		return "Approve requested action"
	}
	operation := strings.TrimSpace(a.request.Operation)
	if operation == "" {
		return "Approve " + a.request.Tool
	}
	return "Approve " + operation + " for " + a.request.Tool
}

func (a *ApprovalWidget) AccessibleState() accessibility.StateSet {
	return accessibility.StateSet{}
}

func (a *ApprovalWidget) AccessibleValue() *accessibility.ValueInfo {
	return nil
}

var _ accessibility.Accessible = (*ApprovalWidget)(nil)
