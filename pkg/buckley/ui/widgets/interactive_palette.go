package widgets

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/terminal"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// InteractivePalette provides a fuzzy-filtering command palette with mouse support.
type InteractivePalette struct {
	uiwidgets.FocusableBase

	query    string
	items    []uiwidgets.PaletteItem
	filtered []uiwidgets.PaletteItem
	selected int

	// Callbacks
	onSelect func(item uiwidgets.PaletteItem)
	filterFn func(item uiwidgets.PaletteItem, query string) bool
	scoreFn  func(item uiwidgets.PaletteItem, query string) int

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

// NewInteractivePalette creates a new palette widget.
func NewInteractivePalette(title string) *InteractivePalette {
	p := &InteractivePalette{
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
	p.scoreFn = p.defaultScore
	p.Base.Role = accessibility.RoleMenu
	p.syncA11y()
	return p
}

// StyleType returns the selector type name.
func (p *InteractivePalette) StyleType() string {
	return "Palette"
}

// SetItems sets the palette items.
func (p *InteractivePalette) SetItems(items []uiwidgets.PaletteItem) {
	p.items = items
	p.updateFiltered()
}

// SetOnSelect sets the callback for item selection.
func (p *InteractivePalette) SetOnSelect(fn func(item uiwidgets.PaletteItem)) {
	p.onSelect = fn
}

// SetFilterFn sets a custom filter function.
func (p *InteractivePalette) SetFilterFn(fn func(item uiwidgets.PaletteItem, query string) bool) {
	p.filterFn = fn
}

// SetScoreFn sets a custom scoring function.
func (p *InteractivePalette) SetScoreFn(fn func(item uiwidgets.PaletteItem, query string) int) {
	p.scoreFn = fn
}

// SetPlaceholder sets the query placeholder text.
func (p *InteractivePalette) SetPlaceholder(placeholder string) {
	p.placeholder = placeholder
}

// SetMaxVisible sets the maximum visible items.
func (p *InteractivePalette) SetMaxVisible(max int) {
	p.maxVisible = max
}

// SetStyles configures the palette appearance.
func (p *InteractivePalette) SetStyles(bg, border, title, query, item, selected, category backend.Style) {
	p.bgStyle = bg
	p.borderStyle = border
	p.titleStyle = title
	p.queryStyle = query
	p.itemStyle = item
	p.selectedStyle = selected
	p.categoryStyle = category
}

// Query returns the current query string.
func (p *InteractivePalette) Query() string {
	return p.query
}

// SelectedIndex returns the selected index.
func (p *InteractivePalette) SelectedIndex() int {
	if p == nil {
		return 0
	}
	return p.selected
}

// SetSelected updates the selected index.
func (p *InteractivePalette) SetSelected(index int) {
	if p == nil {
		return
	}
	p.setSelected(index)
}

// SelectedItem returns the currently selected item, or nil if none.
func (p *InteractivePalette) SelectedItem() *uiwidgets.PaletteItem {
	if p.selected >= 0 && p.selected < len(p.filtered) {
		return &p.filtered[p.selected]
	}
	return nil
}

// Measure returns the preferred size.
func (p *InteractivePalette) Measure(constraints runtime.Constraints) runtime.Size {
	if p == nil {
		return constraints.MinSize()
	}
	width := 60
	if constraints.MaxWidth < width {
		width = constraints.MaxWidth
	}
	itemCount := len(p.filtered)
	if itemCount > p.maxVisible {
		itemCount = p.maxVisible
	}
	height := 5 + itemCount
	if constraints.MaxHeight < height {
		height = constraints.MaxHeight
	}
	return constraints.Constrain(runtime.Size{Width: width, Height: height})
}

// Layout positions the widget (centered overlay).
func (p *InteractivePalette) Layout(bounds runtime.Rect) {
	size := p.Measure(runtime.Constraints{
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
	p.Base.Layout(newBounds)
}

// Render draws the palette.
func (p *InteractivePalette) Render(ctx runtime.RenderContext) {
	outer := p.Bounds()
	b := p.ContentBounds()
	p.syncA11y()

	baseStyle := resolveBaseStyle(ctx, p, backend.DefaultStyle(), false)
	bgStyle := mergeBackendStyles(baseStyle, p.bgStyle)
	borderStyle := mergeBackendStyles(baseStyle, p.borderStyle)
	titleStyle := mergeBackendStyles(baseStyle, p.titleStyle)
	queryStyle := mergeBackendStyles(baseStyle, p.queryStyle)
	itemStyle := mergeBackendStyles(baseStyle, p.itemStyle)
	selectedStyle := mergeBackendStyles(baseStyle, p.selectedStyle)
	categoryStyle := mergeBackendStyles(baseStyle, p.categoryStyle)
	shortcutStyle := mergeBackendStyles(baseStyle, p.shortcutStyle)

	// Draw background
	if outer.Width > 0 && outer.Height > 0 {
		ctx.Buffer.Fill(outer, ' ', bgStyle)
	}

	if b.Width < 20 || b.Height < 5 {
		return
	}

	// Draw border
	p.drawBorder(ctx.Buffer, b, borderStyle)

	// Draw title
	titleX := b.X + (b.Width-len(p.title))/2
	ctx.Buffer.SetString(titleX, b.Y, " "+p.title+" ", titleStyle)

	y := b.Y + 1

	// Draw query input
	query := p.placeholder + p.query
	if len(query) > b.Width-4 {
		query = query[:b.Width-4]
	}
	ctx.Buffer.SetString(b.X+2, y, query, queryStyle)

	// Draw cursor
	cursorX := b.X + 2 + len(query)
	if cursorX < b.X+b.Width-2 && p.IsFocused() {
		ctx.Buffer.Set(cursorX, y, '█', queryStyle)
	}
	y++

	// Draw separator
	for x := b.X + 1; x < b.X+b.Width-1; x++ {
		ctx.Buffer.Set(x, y, '─', borderStyle)
	}
	y++

	// Draw items
	maxItems := b.Height - 4
	if maxItems > len(p.filtered) {
		maxItems = len(p.filtered)
	}

	lastCategory := ""
	for i := 0; i < maxItems && y < b.Y+b.Height-1; i++ {
		item := p.filtered[i]

		// Draw category header if changed
		if item.Category != "" && item.Category != lastCategory {
			lastCategory = item.Category
			ctx.Buffer.SetString(b.X+2, y, item.Category, categoryStyle)
			y++
			if y >= b.Y+b.Height-1 {
				break
			}
		}

		// Draw item
		style := itemStyle
		if i == p.selected {
			style = selectedStyle
			for x := b.X + 1; x < b.X+b.Width-1; x++ {
				ctx.Buffer.Set(x, y, ' ', style)
			}
		}

		label := item.Label
		maxLabel := b.Width - 6
		if item.Shortcut != "" {
			maxLabel -= textWidth(item.Shortcut) + 2
		}
		if textWidth(label) > maxLabel {
			label = truncateString(label, maxLabel)
		}
		ctx.Buffer.SetString(b.X+3, y, label, style)

		if item.Shortcut != "" {
			shortcutX := b.X + b.Width - 2 - textWidth(item.Shortcut)
			itemShortcutStyle := shortcutStyle
			if i == p.selected {
				itemShortcutStyle = style
			}
			ctx.Buffer.SetString(shortcutX, y, item.Shortcut, itemShortcutStyle)
		}

		y++
	}

	// Draw item count if more items than visible
	if len(p.filtered) > maxItems {
		countStr := strconv.Itoa(len(p.filtered)) + " results"
		ctx.Buffer.SetString(b.X+b.Width-2-textWidth(countStr), b.Y+b.Height-1, countStr, borderStyle)
	}
}

func (p *InteractivePalette) syncA11y() {
	if p == nil {
		return
	}
	if p.Base.Role == "" {
		p.Base.Role = accessibility.RoleMenu
	}
	label := strings.TrimSpace(p.title)
	if label == "" {
		label = "Command Palette"
	}
	p.Base.Label = label
	if item := p.SelectedItem(); item != nil {
		p.Base.Value = &accessibility.ValueInfo{Text: item.Label}
	} else {
		p.Base.Value = nil
	}
	if p.query != "" {
		p.Base.Description = fmt.Sprintf("query: %s", p.query)
	} else {
		p.Base.Description = fmt.Sprintf("%d items", len(p.filtered))
	}
}

// drawBorder draws the palette border.
func (p *InteractivePalette) drawBorder(buf *runtime.Buffer, b runtime.Rect, style backend.Style) {
	buf.Set(b.X, b.Y, '╭', style)
	buf.Set(b.X+b.Width-1, b.Y, '╮', style)
	buf.Set(b.X, b.Y+b.Height-1, '╰', style)
	buf.Set(b.X+b.Width-1, b.Y+b.Height-1, '╯', style)

	for x := b.X + 1; x < b.X+b.Width-1; x++ {
		buf.Set(x, b.Y, '─', style)
		buf.Set(x, b.Y+b.Height-1, '─', style)
	}

	for y := b.Y + 1; y < b.Y+b.Height-1; y++ {
		buf.Set(b.X, y, '│', style)
		buf.Set(b.X+b.Width-1, y, '│', style)
	}
}

// HandleMessage processes keyboard and mouse input.
func (p *InteractivePalette) HandleMessage(msg runtime.Message) runtime.HandleResult {
	switch ev := msg.(type) {
	case runtime.MouseMsg:
		return p.handleMouse(ev)
	case runtime.KeyMsg:
		switch ev.Key {
		case terminal.KeyEscape:
			return runtime.WithCommand(runtime.PopOverlay{})
		case terminal.KeyEnter:
			return p.activateSelection()
		case terminal.KeyUp:
			p.setSelected(p.selected - 1)
			return runtime.Handled()
		case terminal.KeyDown:
			p.setSelected(p.selected + 1)
			return runtime.Handled()
		case terminal.KeyBackspace:
			if len(p.query) > 0 {
				p.query = p.query[:len(p.query)-1]
				p.updateFiltered()
				return runtime.Handled()
			}
			return runtime.WithCommand(runtime.PopOverlay{})
		case terminal.KeyRune:
			p.query += string(ev.Rune)
			p.updateFiltered()
			return runtime.Handled()
		}
	}
	return runtime.Unhandled()
}

func (p *InteractivePalette) handleMouse(ev runtime.MouseMsg) runtime.HandleResult {
	if p == nil {
		return runtime.Unhandled()
	}
	bounds := p.Bounds()
	if !bounds.Contains(ev.X, ev.Y) {
		return runtime.Unhandled()
	}
	switch ev.Button {
	case runtime.MouseWheelUp:
		p.setSelected(p.selected - 1)
		return runtime.Handled()
	case runtime.MouseWheelDown:
		p.setSelected(p.selected + 1)
		return runtime.Handled()
	case runtime.MouseLeft:
		if ev.Action != runtime.MousePress {
			return runtime.Unhandled()
		}
		if !p.IsFocused() {
			p.Focus()
		}
		if index, ok := p.itemIndexAt(ev.X, ev.Y); ok {
			if index == p.selected {
				return p.activateSelection()
			}
			p.setSelected(index)
			p.Invalidate()
			return runtime.Handled()
		}
		return runtime.Handled()
	}
	return runtime.Unhandled()
}

func (p *InteractivePalette) itemIndexAt(x, y int) (int, bool) {
	b := p.ContentBounds()
	if b.Width < 20 || b.Height < 5 {
		return 0, false
	}
	if !b.Contains(x, y) {
		return 0, false
	}
	rowY := b.Y + 3
	maxItems := b.Height - 4
	if maxItems > len(p.filtered) {
		maxItems = len(p.filtered)
	}
	lastCategory := ""
	for i := 0; i < maxItems && rowY < b.Y+b.Height-1; i++ {
		item := p.filtered[i]
		if item.Category != "" && item.Category != lastCategory {
			lastCategory = item.Category
			if y == rowY {
				return 0, false
			}
			rowY++
			if rowY >= b.Y+b.Height-1 {
				break
			}
		}
		if y == rowY {
			return i, true
		}
		rowY++
	}
	return 0, false
}

func (p *InteractivePalette) activateSelection() runtime.HandleResult {
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
}

// updateFiltered refilters items based on current query.
func (p *InteractivePalette) updateFiltered() {
	if p.query == "" {
		p.filtered = p.items
	} else {
		type scoredItem struct {
			item  uiwidgets.PaletteItem
			score int
			index int
		}
		scored := make([]scoredItem, 0, len(p.items))
		for i, item := range p.items {
			if p.filterFn != nil && !p.filterFn(item, p.query) {
				continue
			}
			score := 0
			if p.scoreFn != nil {
				score = p.scoreFn(item, p.query)
			}
			scored = append(scored, scoredItem{item: item, score: score, index: i})
		}
		if p.scoreFn != nil {
			sort.Slice(scored, func(i, j int) bool {
				if scored[i].score == scored[j].score {
					return scored[i].index < scored[j].index
				}
				return scored[i].score > scored[j].score
			})
		}
		p.filtered = make([]uiwidgets.PaletteItem, 0, len(scored))
		for _, item := range scored {
			p.filtered = append(p.filtered, item.item)
		}
	}

	if p.selected >= len(p.filtered) {
		p.selected = len(p.filtered) - 1
	}
	if p.selected < 0 && len(p.filtered) > 0 {
		p.selected = 0
	}
	p.syncA11y()
}

func (p *InteractivePalette) defaultFilter(item uiwidgets.PaletteItem, query string) bool {
	queryLower := strings.ToLower(query)
	return strings.Contains(strings.ToLower(item.Label), queryLower) ||
		strings.Contains(strings.ToLower(item.Description), queryLower) ||
		strings.Contains(strings.ToLower(item.Category), queryLower) ||
		strings.Contains(strings.ToLower(item.ID), queryLower) ||
		p.defaultScore(item, query) > 0
}

func (p *InteractivePalette) defaultScore(item uiwidgets.PaletteItem, query string) int {
	best := 0
	fields := []string{item.Label, item.Description, item.Category, item.ID}
	for _, field := range fields {
		if field == "" {
			continue
		}
		score, ok := p.scoreMatch(field, query)
		if ok && score > best {
			best = score
		}
	}
	return best
}

func (p *InteractivePalette) scoreMatch(text, query string) (int, bool) {
	if query == "" {
		return 1, true
	}

	queryLower := strings.ToLower(query)
	textLower := strings.ToLower(text)
	if strings.Contains(textLower, queryLower) {
		return len(query) * 10, true
	}

	score := 0
	qi := 0
	consecutive := 0
	for i, r := range textLower {
		if qi >= len(queryLower) {
			break
		}
		qr := rune(queryLower[qi])
		if r == qr {
			score += 10
			if i == 0 || isWordBoundary(rune(textLower[i-1])) {
				score += 5
			}
			if consecutive > 0 {
				score += 5
			}
			consecutive++
			qi++
		} else {
			consecutive = 0
		}
	}

	if qi < len(queryLower) {
		return 0, false
	}
	return score, true
}

func isWordBoundary(r rune) bool {
	switch r {
	case ' ', '-', '_', '/', '.', ':':
		return true
	default:
		return false
	}
}

func (p *InteractivePalette) setSelected(index int) {
	if p == nil {
		return
	}
	if len(p.filtered) == 0 {
		p.selected = 0
		return
	}
	if index < 0 {
		index = 0
	}
	if index >= len(p.filtered) {
		index = len(p.filtered) - 1
	}
	p.selected = index
	p.syncA11y()
	p.Invalidate()
}

var _ runtime.Widget = (*InteractivePalette)(nil)
var _ runtime.Focusable = (*InteractivePalette)(nil)
