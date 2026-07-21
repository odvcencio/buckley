package widgets

import (
	"path/filepath"
	"strconv"
	"strings"

	"m31labs.dev/buckley/pkg/ui/filepicker"
	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/terminal"
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

	f.bounds = runtime.Rect{
		X:      x,
		Y:      y,
		Width:  size.Width,
		Height: size.Height,
	}
}

// Render draws the file picker.
func (f *FilePickerWidget) Render(ctx runtime.RenderContext) {
	b := f.bounds
	if b.Width < 10 || b.Height < 5 {
		return
	}

	ctx.Buffer.Fill(b, ' ', f.bgStyle)
	f.drawBorder(ctx.Buffer, b)
	f.renderTitle(ctx.Buffer, b)
	f.renderQuery(ctx.Buffer, b)
	f.renderSeparator(ctx.Buffer, b)
	f.renderMatches(ctx.Buffer, b)
	f.renderStatus(ctx.Buffer, b)
}

func (f *FilePickerWidget) renderTitle(buf *runtime.Buffer, b runtime.Rect) {
	title := " File Search "
	title = truncateString(title, b.Width-2)
	titleX := b.X + (b.Width-displayWidth(title))/2
	buf.SetString(titleX, b.Y, title, f.borderStyle.Bold(true))
}

func (f *FilePickerWidget) renderQuery(buf *runtime.Buffer, b runtime.Rect) {
	query := f.picker.Query()
	queryLine := truncateString("@ "+query, b.Width-4)
	buf.SetString(b.X+2, b.Y+1, queryLine, f.queryStyle)

	cursorX := b.X + 2 + displayWidth(queryLine)
	if cursorX < b.X+b.Width-2 && f.focused {
		buf.Set(cursorX, b.Y+1, '█', f.queryStyle)
	}
}

func (f *FilePickerWidget) renderSeparator(buf *runtime.Buffer, b runtime.Rect) {
	for x := b.X + 1; x < b.X+b.Width-1; x++ {
		buf.Set(x, b.Y+2, '─', f.borderStyle)
	}
}

func (f *FilePickerWidget) renderMatches(buf *runtime.Buffer, b runtime.Rect) {
	matches := f.picker.GetMatches()
	selected := f.picker.SelectedIndex()

	startY := b.Y + 3
	maxResults := b.Height - 4

	for i := 0; i < maxResults && i < len(matches); i++ {
		match := matches[i]
		y := startY + i

		style := f.textStyle
		if i == selected {
			style = f.selectedStyle
		}

		path, highlights := truncateFilePickerPath(match.Path, match.Highlights, b.Width-4)
		f.renderMatch(buf, b.X+2, y, path, highlights, style, i == selected)
	}
}

func (f *FilePickerWidget) renderStatus(buf *runtime.Buffer, b runtime.Rect) {
	count := f.picker.FileCount()
	shown := len(f.picker.GetMatches())
	status := ""
	if f.picker.IsIndexReady() {
		status = formatCount(count, shown)
	} else {
		status = "indexing..."
	}
	buf.SetString(b.X+b.Width-2-displayWidth(status), b.Y+b.Height-1, status, f.borderStyle)
}

// renderMatch draws a path with character highlights.
func (f *FilePickerWidget) renderMatch(buf *runtime.Buffer, x, y int, path string, highlights []int, baseStyle backend.Style, selected bool) {
	highlightSet := make(map[int]bool)
	for _, h := range highlights {
		highlightSet[h] = true
	}

	col := 0
	for _, ch := range path {
		style := baseStyle
		if highlightSet[col] && !selected {
			style = f.highlightStyle
		}
		buf.Set(x+col, y, ch, style)
		col++
	}
}

func truncateFilePickerPath(path string, highlights []int, maxWidth int) (string, []int) {
	if maxWidth <= 0 {
		return "", nil
	}

	pathRunes := []rune(path)
	if len(pathRunes) <= maxWidth {
		return path, filterHighlights(highlights, 0, len(pathRunes), 0)
	}

	dir, file := filepath.Split(path)
	dirRunes := []rune(dir)
	fileRunes := []rune(file)
	dirLen := len(dirRunes)

	if len(fileRunes) >= maxWidth {
		visibleFileRunes := maxWidth
		if maxWidth > 3 {
			visibleFileRunes = maxWidth - 3
		}
		return truncateString(file, maxWidth), filterHighlights(highlights, dirLen, dirLen+visibleFileRunes, -dirLen)
	}

	remainingDirWidth := maxWidth - len(fileRunes) - 3
	if remainingDirWidth > 0 && len(dirRunes) > remainingDirWidth {
		start := len(dirRunes) - remainingDirWidth
		display := "..." + string(dirRunes[start:]) + file
		return display, filterHighlights(highlights, start, len(pathRunes), 3-start)
	}

	return file, filterHighlights(highlights, dirLen, len(pathRunes), -dirLen)
}

func filterHighlights(highlights []int, start, end, delta int) []int {
	filtered := make([]int, 0, len(highlights))
	for _, h := range highlights {
		if h >= start && h < end {
			filtered = append(filtered, h+delta)
		}
	}
	return filtered
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
	if n < 0 {
		return "-" + formatFileCount(-n)
	}
	digits := strconv.Itoa(n)
	if len(digits) <= 3 {
		return digits
	}

	first := len(digits) % 3
	if first == 0 {
		first = 3
	}

	var b strings.Builder
	b.Grow(len(digits) + len(digits)/3)
	b.WriteString(digits[:first])
	for i := first; i < len(digits); i += 3 {
		b.WriteByte(',')
		b.WriteString(digits[i : i+3])
	}
	return b.String()
}
