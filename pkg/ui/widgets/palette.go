package widgets

import (
	"strconv"
	"strings"

	"m31labs.dev/buckley/pkg/ui/backend"
	"m31labs.dev/buckley/pkg/ui/runtime"
	"m31labs.dev/buckley/pkg/ui/terminal"
)

// PaletteItem represents a single item in the palette.
type PaletteItem struct {
	ID          string // Unique identifier
	Category    string // Optional category for grouping (e.g., "Recent", "Files", "Actions")
	Label       string // Display text
	Description string // Optional secondary text
	Shortcut    string // Optional keyboard shortcut hint
	Data        any    // Custom data for the action
}

// PaletteWidget provides a fuzzy-filtering command palette overlay.
type PaletteWidget struct {
	FocusableBase

	query    string
	items    []PaletteItem
	filtered []PaletteItem
	selected int

	// Callbacks
	onSelect func(item PaletteItem)
	filterFn func(item PaletteItem, query string) bool

	// Configuration
	title       string
	placeholder string
	maxVisible  int

	// Styles
	bgStyle       backend.Style
	borderStyle   backend.Style
	titleStyle    backend.Style
	queryStyle    backend.Style
	itemStyle     backend.Style
	selectedStyle backend.Style
	categoryStyle backend.Style
	descStyle     backend.Style
	shortcutStyle backend.Style
}

// NewPaletteWidget creates a new palette widget.
func NewPaletteWidget(title string) *PaletteWidget {
	p := &PaletteWidget{
		title:         title,
		placeholder:   "> ",
		maxVisible:    10,
		bgStyle:       backend.DefaultStyle(),
		borderStyle:   backend.DefaultStyle(),
		titleStyle:    backend.DefaultStyle().Bold(true),
		queryStyle:    backend.DefaultStyle().Bold(true),
		itemStyle:     backend.DefaultStyle(),
		selectedStyle: backend.DefaultStyle().Reverse(true),
		categoryStyle: backend.DefaultStyle().Foreground(backend.ColorBlue),
		descStyle:     backend.DefaultStyle().Foreground(backend.ColorDefault),
		shortcutStyle: backend.DefaultStyle().Foreground(backend.ColorDefault),
	}
	p.filterFn = p.defaultFilter
	return p
}

// SetItems sets the palette items.
func (p *PaletteWidget) SetItems(items []PaletteItem) {
	p.items = items
	p.updateFiltered()
}

// SetOnSelect sets the callback for item selection.
func (p *PaletteWidget) SetOnSelect(fn func(item PaletteItem)) {
	p.onSelect = fn
}

// SetFilterFn sets a custom filter function.
func (p *PaletteWidget) SetFilterFn(fn func(item PaletteItem, query string) bool) {
	p.filterFn = fn
}

// SetPlaceholder sets the query placeholder text.
func (p *PaletteWidget) SetPlaceholder(placeholder string) {
	p.placeholder = placeholder
}

// SetMaxVisible sets the maximum visible items.
func (p *PaletteWidget) SetMaxVisible(max int) {
	p.maxVisible = max
}

// SetStyles configures the palette appearance.
func (p *PaletteWidget) SetStyles(bg, border, title, query, item, selected, category backend.Style) {
	p.bgStyle = bg
	p.borderStyle = border
	p.titleStyle = title
	p.queryStyle = query
	p.itemStyle = item
	p.selectedStyle = selected
	p.categoryStyle = category
}

// Query returns the current query string.
func (p *PaletteWidget) Query() string {
	return p.query
}

// SelectedItem returns the currently selected item, or nil if none.
func (p *PaletteWidget) SelectedItem() *PaletteItem {
	if p.selected >= 0 && p.selected < len(p.filtered) {
		return &p.filtered[p.selected]
	}
	return nil
}

// updateFiltered refilters items based on current query.
func (p *PaletteWidget) updateFiltered() {
	if p.query == "" {
		p.filtered = p.items
	} else {
		p.filtered = nil
		for _, item := range p.items {
			if p.filterFn(item, p.query) {
				p.filtered = append(p.filtered, item)
			}
		}
	}

	// Clamp selection
	if p.selected >= len(p.filtered) {
		p.selected = len(p.filtered) - 1
	}
	if p.selected < 0 && len(p.filtered) > 0 {
		p.selected = 0
	}
}

// defaultFilter performs case-insensitive substring matching.
func (p *PaletteWidget) defaultFilter(item PaletteItem, query string) bool {
	queryLower := strings.ToLower(query)
	return strings.Contains(strings.ToLower(item.Label), queryLower) ||
		strings.Contains(strings.ToLower(item.Description), queryLower) ||
		strings.Contains(strings.ToLower(item.Category), queryLower)
}

// Measure returns the preferred size.
func (p *PaletteWidget) Measure(constraints runtime.Constraints) runtime.Size {
	width := 60
	if constraints.MaxWidth < width {
		width = constraints.MaxWidth
	}

	// Height: title(1) + query(1) + separator(1) + items(up to maxVisible) + border(2)
	itemCount := len(p.filtered)
	if itemCount > p.maxVisible {
		itemCount = p.maxVisible
	}
	height := 5 + itemCount
	if constraints.MaxHeight < height {
		height = constraints.MaxHeight
	}

	return runtime.Size{Width: width, Height: height}
}

// Layout positions the widget (centered overlay).
func (p *PaletteWidget) Layout(bounds runtime.Rect) {
	size := p.Measure(runtime.Constraints{
		MaxWidth:  bounds.Width,
		MaxHeight: bounds.Height,
	})

	x := bounds.X + (bounds.Width-size.Width)/2
	y := bounds.Y + (bounds.Height-size.Height)/2

	p.bounds = runtime.Rect{
		X:      x,
		Y:      y,
		Width:  size.Width,
		Height: size.Height,
	}
}

// Render draws the palette.
func (p *PaletteWidget) Render(ctx runtime.RenderContext) {
	b := p.bounds
	if b.Width < 20 || b.Height < 5 {
		return
	}

	ctx.Buffer.Fill(b, ' ', p.bgStyle)
	p.drawBorder(ctx.Buffer, b)
	p.renderTitle(ctx.Buffer, b)

	y := p.renderQuery(ctx.Buffer, b, b.Y+1)
	y = p.renderSeparator(ctx.Buffer, b, y)
	p.renderItems(ctx.Buffer, b, y)
	p.renderResultCount(ctx.Buffer, b)
}

func (p *PaletteWidget) renderTitle(buf *runtime.Buffer, b runtime.Rect) {
	title := " " + p.title + " "
	titleX := b.X + (b.Width-runeLen(title))/2
	buf.SetString(titleX, b.Y, title, p.titleStyle)
}

func (p *PaletteWidget) renderQuery(buf *runtime.Buffer, b runtime.Rect, y int) int {
	query := truncateString(p.placeholder+p.query, b.Width-4)
	buf.SetString(b.X+2, y, query, p.queryStyle)

	cursorX := b.X + 2 + runeLen(query)
	if cursorX < b.X+b.Width-2 && p.focused {
		buf.Set(cursorX, y, '█', p.queryStyle)
	}
	return y + 1
}

func (p *PaletteWidget) renderSeparator(buf *runtime.Buffer, b runtime.Rect, y int) int {
	for x := b.X + 1; x < b.X+b.Width-1; x++ {
		buf.Set(x, y, '─', p.borderStyle)
	}
	return y + 1
}

func (p *PaletteWidget) renderItems(buf *runtime.Buffer, b runtime.Rect, y int) {
	maxY := b.Y + b.Height - 1
	lastCategory := ""

	for i, item := range p.filtered {
		if y >= maxY {
			break
		}
		if item.Category != "" && item.Category != lastCategory {
			lastCategory = item.Category
			p.renderCategory(buf, b, y, item.Category)
			y++
			if y >= maxY {
				break
			}
		}

		p.renderItem(buf, b, y, item, i == p.selected)
		y++
	}
}

func (p *PaletteWidget) renderCategory(buf *runtime.Buffer, b runtime.Rect, y int, category string) {
	buf.SetString(b.X+2, y, truncateString(category, b.Width-4), p.categoryStyle)
}

func (p *PaletteWidget) renderItem(buf *runtime.Buffer, b runtime.Rect, y int, item PaletteItem, selected bool) {
	style := p.itemStyle
	if selected {
		style = p.selectedStyle
		p.fillItemRow(buf, b, y, style)
	}

	label := truncateString(item.Label, p.maxLabelWidth(b, item))
	buf.SetString(b.X+3, y, label, style)

	if item.Shortcut == "" {
		return
	}
	shortcut := truncateString(item.Shortcut, b.Width-6)
	shortcutX := b.X + b.Width - 2 - runeLen(shortcut)
	shortcutStyle := p.shortcutStyle
	if selected {
		shortcutStyle = style
	}
	buf.SetString(shortcutX, y, shortcut, shortcutStyle)
}

func (p *PaletteWidget) fillItemRow(buf *runtime.Buffer, b runtime.Rect, y int, style backend.Style) {
	for x := b.X + 1; x < b.X+b.Width-1; x++ {
		buf.Set(x, y, ' ', style)
	}
}

func (p *PaletteWidget) maxLabelWidth(b runtime.Rect, item PaletteItem) int {
	maxLabel := b.Width - 6
	if item.Shortcut != "" {
		maxLabel -= runeLen(item.Shortcut) + 2
	}
	return max(0, maxLabel)
}

func (p *PaletteWidget) renderResultCount(buf *runtime.Buffer, b runtime.Rect) {
	if len(p.filtered) <= max(0, b.Height-4) {
		return
	}
	countStr := strconv.Itoa(len(p.filtered)) + " results"
	countStr = truncateString(countStr, b.Width-4)
	buf.SetString(b.X+b.Width-2-runeLen(countStr), b.Y+b.Height-1, countStr, p.borderStyle)
}

// drawBorder draws the palette border.
func (p *PaletteWidget) drawBorder(buf *runtime.Buffer, b runtime.Rect) {
	// Corners
	buf.Set(b.X, b.Y, '╭', p.borderStyle)
	buf.Set(b.X+b.Width-1, b.Y, '╮', p.borderStyle)
	buf.Set(b.X, b.Y+b.Height-1, '╰', p.borderStyle)
	buf.Set(b.X+b.Width-1, b.Y+b.Height-1, '╯', p.borderStyle)

	// Horizontal edges
	for x := b.X + 1; x < b.X+b.Width-1; x++ {
		buf.Set(x, b.Y, '─', p.borderStyle)
		buf.Set(x, b.Y+b.Height-1, '─', p.borderStyle)
	}

	// Vertical edges
	for y := b.Y + 1; y < b.Y+b.Height-1; y++ {
		buf.Set(b.X, y, '│', p.borderStyle)
		buf.Set(b.X+b.Width-1, y, '│', p.borderStyle)
	}
}

// HandleMessage processes keyboard input.
func (p *PaletteWidget) HandleMessage(msg runtime.Message) runtime.HandleResult {
	key, ok := msg.(runtime.KeyMsg)
	if !ok {
		return runtime.Unhandled()
	}

	switch key.Key {
	case terminal.KeyEscape:
		return runtime.WithCommand(runtime.PopOverlay{})

	case terminal.KeyEnter:
		if item := p.SelectedItem(); item != nil {
			if p.onSelect != nil {
				p.onSelect(*item)
			}
			return runtime.WithCommands(
				runtime.PaletteSelected{ID: item.ID, Data: item.Data},
				runtime.PopOverlay{},
			)
		}
		return runtime.Handled()

	case terminal.KeyUp:
		if p.selected > 0 {
			p.selected--
		}
		return runtime.Handled()

	case terminal.KeyDown:
		if p.selected < len(p.filtered)-1 {
			p.selected++
		}
		return runtime.Handled()

	case terminal.KeyBackspace:
		if p.query != "" {
			p.query = dropLastRune(p.query)
			p.updateFiltered()
			return runtime.Handled()
		}
		// Empty query, close palette
		return runtime.WithCommand(runtime.PopOverlay{})

	case terminal.KeyRune:
		p.query += string(key.Rune)
		p.updateFiltered()
		return runtime.Handled()
	}

	return runtime.Unhandled()
}

func dropLastRune(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	return string(runes[:len(runes)-1])
}
