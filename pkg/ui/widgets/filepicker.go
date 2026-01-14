package widgets

import (
	"path/filepath"

	"github.com/odvcencio/buckley/pkg/ui/backend"
	"github.com/odvcencio/buckley/pkg/ui/filepicker"
	"github.com/odvcencio/buckley/pkg/ui/runtime"
	"github.com/odvcencio/buckley/pkg/ui/terminal"
)

// FilePickerWidget wraps FilePicker as a widget for overlay display.
type FilePickerWidget struct {
	FocusableBase

	picker *filepicker.FilePicker

	// Styles
	bgStyle        backend.Style
	borderStyle    backend.Style
	textStyle      backend.Style
	selectedStyle  backend.Style
	highlightStyle backend.Style
	queryStyle     backend.Style
}

// NewFilePickerWidget creates a new file picker widget.
func NewFilePickerWidget(picker *filepicker.FilePicker) *FilePickerWidget {
	return &FilePickerWidget{
		picker:         picker,
		bgStyle:        backend.DefaultStyle(),
		borderStyle:    backend.DefaultStyle(),
		textStyle:      backend.DefaultStyle(),
		selectedStyle:  backend.DefaultStyle().Reverse(true),
		highlightStyle: backend.DefaultStyle().Bold(true),
		queryStyle:     backend.DefaultStyle().Bold(true),
	}
}

// SetStyles configures the widget appearance.
func (f *FilePickerWidget) SetStyles(bg, border, text, selected, highlight, query backend.Style) {
	f.bgStyle = bg
	f.borderStyle = border
	f.textStyle = text
	f.selectedStyle = selected
	f.highlightStyle = highlight
	f.queryStyle = query
}

// Measure returns the preferred size.
func (f *FilePickerWidget) Measure(constraints runtime.Constraints) runtime.Size {
	// Fixed width, height based on results + chrome
	width := 60
	if constraints.MaxWidth < width {
		width = constraints.MaxWidth
	}

	// Header (1) + border (2) + results (up to 10) = 13 max
	height := 13
	if constraints.MaxHeight < height {
		height = constraints.MaxHeight
	}

	return runtime.Size{Width: width, Height: height}
}

// Layout positions the widget (centered overlay).
func (f *FilePickerWidget) Layout(bounds runtime.Rect) {
	// Center the picker in the bounds
	size := f.Measure(runtime.Constraints{
		MinWidth:  0,
		MinHeight: 0,
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
	f.Base.Layout(newBounds)
}

// Render draws the file picker.
func (f *FilePickerWidget) Render(ctx runtime.RenderContext) {
	b := f.bounds
	if b.Width < 10 || b.Height < 5 {
		return
	}

	// Draw background
	ctx.Buffer.Fill(b, ' ', f.bgStyle)

	// Draw border
	f.drawBorder(ctx.Buffer, b)

	// Draw title
	title := " File Search "
	titleX := b.X + (b.Width-len(title))/2
	ctx.Buffer.SetString(titleX, b.Y, title, f.borderStyle.Bold(true))

	// Draw query line
	query := f.picker.Query()
	queryLine := "@ " + query
	if len(queryLine) > b.Width-4 {
		queryLine = queryLine[:b.Width-4]
	}
	ctx.Buffer.SetString(b.X+2, b.Y+1, queryLine, f.queryStyle)

	// Draw cursor after query
	cursorX := b.X + 2 + len(queryLine)
	if cursorX < b.X+b.Width-2 && f.focused {
		ctx.Buffer.Set(cursorX, b.Y+1, '█', f.queryStyle)
	}

	// Draw separator
	for x := b.X + 1; x < b.X+b.Width-1; x++ {
		ctx.Buffer.Set(x, b.Y+2, '─', f.borderStyle)
	}

	// Draw matches
	matches := f.picker.GetMatches()
	selected := f.picker.SelectedIndex()

	startY := b.Y + 3
	maxResults := b.Height - 4 // Account for chrome

	for i := 0; i < maxResults && i < len(matches); i++ {
		match := matches[i]
		y := startY + i

		// Determine style
		style := f.textStyle
		if i == selected {
			style = f.selectedStyle
		}

		// Truncate path if needed
		path := match.Path
		maxLen := b.Width - 4
		if len(path) > maxLen {
			// Show filename + as much dir as fits
			dir, file := filepath.Split(path)
			if len(file) > maxLen {
				path = file[:maxLen]
			} else {
				remaining := maxLen - len(file) - 3 // "..."
				if remaining > 0 && len(dir) > remaining {
					path = "..." + dir[len(dir)-remaining:] + file
				} else {
					path = file
				}
			}
		}

		// Draw with highlights
		f.renderMatch(ctx.Buffer, b.X+2, y, path, match.Highlights, style, i == selected)
	}

	// Draw file count
	count := f.picker.FileCount()
	status := ""
	if f.picker.IsIndexReady() {
		status = formatCount(count, len(matches))
	} else {
		status = "indexing..."
	}
	ctx.Buffer.SetString(b.X+b.Width-2-len(status), b.Y+b.Height-1, status, f.borderStyle)
}

// renderMatch draws a path with character highlights.
func (f *FilePickerWidget) renderMatch(buf *runtime.Buffer, x, y int, path string, highlights []int, baseStyle backend.Style, selected bool) {
	highlightSet := make(map[int]bool)
	for _, h := range highlights {
		highlightSet[h] = true
	}

	for i, ch := range path {
		style := baseStyle
		if highlightSet[i] && !selected {
			style = f.highlightStyle
		}
		buf.Set(x+i, y, ch, style)
	}
}

// drawBorder draws a box border.
func (f *FilePickerWidget) drawBorder(buf *runtime.Buffer, b runtime.Rect) {
	// Corners
	buf.Set(b.X, b.Y, '╭', f.borderStyle)
	buf.Set(b.X+b.Width-1, b.Y, '╮', f.borderStyle)
	buf.Set(b.X, b.Y+b.Height-1, '╰', f.borderStyle)
	buf.Set(b.X+b.Width-1, b.Y+b.Height-1, '╯', f.borderStyle)

	// Horizontal edges
	for x := b.X + 1; x < b.X+b.Width-1; x++ {
		buf.Set(x, b.Y, '─', f.borderStyle)
		buf.Set(x, b.Y+b.Height-1, '─', f.borderStyle)
	}

	// Vertical edges
	for y := b.Y + 1; y < b.Y+b.Height-1; y++ {
		buf.Set(b.X, y, '│', f.borderStyle)
		buf.Set(b.X+b.Width-1, y, '│', f.borderStyle)
	}
}

// HandleMessage processes keyboard input.
func (f *FilePickerWidget) HandleMessage(msg runtime.Message) runtime.HandleResult {
	key, ok := msg.(runtime.KeyMsg)
	if !ok {
		return runtime.Unhandled()
	}

	switch key.Key {
	case terminal.KeyEscape:
		f.picker.Deactivate()
		return runtime.WithCommand(runtime.PopOverlay{})

	case terminal.KeyEnter:
		selected := f.picker.GetSelected()
		if selected != "" {
			f.picker.Deactivate()
			return runtime.WithCommands(
				runtime.FileSelected{Path: selected},
				runtime.PopOverlay{},
			)
		}
		return runtime.Handled()

	case terminal.KeyUp:
		f.picker.MoveUp()
		return runtime.Handled()

	case terminal.KeyDown:
		f.picker.MoveDown()
		return runtime.Handled()

	case terminal.KeyBackspace:
		if !f.picker.Backspace() {
			// Query empty, close picker
			return runtime.WithCommand(runtime.PopOverlay{})
		}
		return runtime.Handled()

	case terminal.KeyRune:
		f.picker.AppendQuery(key.Rune)
		return runtime.Handled()
	}

	return runtime.Unhandled()
}

// formatCount formats the file count display.
func formatCount(total, shown int) string {
	if shown == 0 {
		return "no matches"
	}
	if shown == total {
		return formatFileCount(total) + " files"
	}
	return formatFileCount(shown) + "/" + formatFileCount(total)
}

// formatFileCount adds thousand separators.
func formatFileCount(n int) string {
	if n < 1000 {
		return intToStr(n)
	}
	return intToStr(n/1000) + "," + padLeftStr(intToStr(n%1000), 3, '0')
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func padLeftStr(s string, length int, pad byte) string {
	for len(s) < length {
		s = string(pad) + s
	}
	return s
}
