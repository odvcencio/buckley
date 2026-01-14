package widgets

import (
	"strings"

	"github.com/odvcencio/buckley/pkg/ui/backend"
	"github.com/odvcencio/buckley/pkg/ui/runtime"
	"github.com/odvcencio/buckley/pkg/ui/terminal"
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

// ApprovalWidget displays a modal dialog for tool approval.
type ApprovalWidget struct {
	FocusableBase

	request      ApprovalRequest
	scrollOffset int // For scrolling through diff

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

// NewApprovalWidget creates a new approval dialog widget.
func NewApprovalWidget(req ApprovalRequest) *ApprovalWidget {
	return &ApprovalWidget{
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
}

// SetStyles configures the widget appearance.
func (a *ApprovalWidget) SetStyles(bg, border, title, text backend.Style) {
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
	a.Base.Layout(newBounds)
}

// Render draws the approval dialog.
func (a *ApprovalWidget) Render(ctx runtime.RenderContext) {
	b := a.bounds
	if b.Width < 20 || b.Height < 8 {
		return
	}

	// Draw background
	ctx.Buffer.Fill(b, ' ', a.bgStyle)

	// Draw border
	a.drawBorder(ctx.Buffer, b)

	// Draw warning title
	title := " Approval Required "
	titleX := b.X + (b.Width-len(title))/2
	ctx.Buffer.SetString(titleX, b.Y, title, a.titleStyle)

	y := b.Y + 2

	// Draw tool and operation info
	toolLabel := "Tool: "
	ctx.Buffer.SetString(b.X+2, y, toolLabel, a.labelStyle)
	ctx.Buffer.SetString(b.X+2+len(toolLabel), y, a.request.Tool, a.textStyle)
	y++

	opLabel := "Operation: "
	ctx.Buffer.SetString(b.X+2, y, opLabel, a.labelStyle)
	ctx.Buffer.SetString(b.X+2+len(opLabel), y, a.request.Operation, a.textStyle)
	y++

	// Draw description if present
	if a.request.Description != "" {
		y++
		desc := a.request.Description
		maxLen := b.Width - 4
		if len(desc) > maxLen {
			desc = desc[:maxLen-3] + "..."
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
		ctx.Buffer.SetString(b.X+2+len(fileLabel), y, a.request.FilePath, a.textStyle)
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
	if len(cmdDisplay) > maxCmd {
		cmdDisplay = cmdDisplay[:maxCmd-3] + "..."
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
		if len(content) > maxContent {
			content = content[:maxContent]
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

	buttons := "[A]llow    [D]eny    A[l]ways allow"
	x := b.X + (b.Width-len(buttons))/2

	// Draw with highlights on key letters
	for i, ch := range buttons {
		style := a.buttonStyle
		if ch == 'A' || ch == 'D' || ch == 'l' {
			style = a.buttonHlStyle
		}
		buf.Set(x+i, y, ch, style)
	}
}

// HandleMessage processes keyboard input.
func (a *ApprovalWidget) HandleMessage(msg runtime.Message) runtime.HandleResult {
	key, ok := msg.(runtime.KeyMsg)
	if !ok {
		return runtime.Unhandled()
	}

	switch key.Key {
	case terminal.KeyEscape:
		// Escape = Deny
		return runtime.WithCommands(
			runtime.ApprovalResponse{
				RequestID:   a.request.ID,
				Approved:    false,
				AlwaysAllow: false,
			},
			runtime.PopOverlay{},
		)

	case terminal.KeyUp:
		// Scroll diff up
		if a.scrollOffset > 0 {
			a.scrollOffset--
		}
		return runtime.Handled()

	case terminal.KeyDown:
		// Scroll diff down
		maxScroll := len(a.request.DiffLines) - 5
		if maxScroll < 0 {
			maxScroll = 0
		}
		if a.scrollOffset < maxScroll {
			a.scrollOffset++
		}
		return runtime.Handled()

	case terminal.KeyRune:
		switch key.Rune {
		case 'a', 'A', 'y', 'Y': // Allow / Yes
			return runtime.WithCommands(
				runtime.ApprovalResponse{
					RequestID:   a.request.ID,
					Approved:    true,
					AlwaysAllow: false,
				},
				runtime.PopOverlay{},
			)

		case 'd', 'D', 'n', 'N': // Deny / No
			return runtime.WithCommands(
				runtime.ApprovalResponse{
					RequestID:   a.request.ID,
					Approved:    false,
					AlwaysAllow: false,
				},
				runtime.PopOverlay{},
			)

		case 'l', 'L': // Always allow
			return runtime.WithCommands(
				runtime.ApprovalResponse{
					RequestID:   a.request.ID,
					Approved:    true,
					AlwaysAllow: true,
				},
				runtime.PopOverlay{},
			)
		}
	}

	return runtime.Unhandled()
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
