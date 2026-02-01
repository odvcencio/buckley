package widgets

import (
	"fmt"
	"strings"

	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/scroll"
	"github.com/odvcencio/fluffyui/terminal"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// InteractiveTable is a table with mouse selection support.
type InteractiveTable struct {
	uiwidgets.FocusableBase
	Columns       []uiwidgets.TableColumn
	Rows          [][]string
	selected      int
	offset        int
	label         string
	style         backend.Style
	headerStyle   backend.Style
	selectedStyle backend.Style
	cachedWidths  []int
	cachedTotal   int
	cachedSig     uint32
}

// NewInteractiveTable creates a table with columns.
func NewInteractiveTable(columns ...uiwidgets.TableColumn) *InteractiveTable {
	table := &InteractiveTable{
		Columns:       columns,
		label:         "Table",
		style:         backend.DefaultStyle(),
		headerStyle:   backend.DefaultStyle().Bold(true),
		selectedStyle: backend.DefaultStyle().Reverse(true),
	}
	table.Base.Role = accessibility.RoleTable
	table.syncA11y()
	return table
}

// SetStyle updates the base table style.
func (t *InteractiveTable) SetStyle(style backend.Style) {
	if t == nil {
		return
	}
	t.style = style
}

// SetHeaderStyle updates the header style.
func (t *InteractiveTable) SetHeaderStyle(style backend.Style) {
	if t == nil {
		return
	}
	t.headerStyle = style
}

// SetSelectedStyle updates the selected row style.
func (t *InteractiveTable) SetSelectedStyle(style backend.Style) {
	if t == nil {
		return
	}
	t.selectedStyle = style
}

// StyleType returns the selector type name.
func (t *InteractiveTable) StyleType() string {
	return "Table"
}

// SetRows updates table rows.
func (t *InteractiveTable) SetRows(rows [][]string) {
	if t == nil {
		return
	}
	t.Rows = rows
	t.syncA11y()
}

// SetLabel updates the accessibility label.
func (t *InteractiveTable) SetLabel(label string) {
	if t == nil {
		return
	}
	t.label = label
	t.syncA11y()
}

// SelectedIndex returns the currently selected row index.
func (t *InteractiveTable) SelectedIndex() int {
	if t == nil {
		return 0
	}
	return t.selected
}

// SetSelected updates the selected row index.
func (t *InteractiveTable) SetSelected(index int) {
	if t == nil {
		return
	}
	t.setSelected(index)
}

// RowCount returns the number of rows.
func (t *InteractiveTable) RowCount() int {
	if t == nil {
		return 0
	}
	return len(t.Rows)
}

// ColumnCount returns the number of columns.
func (t *InteractiveTable) ColumnCount() int {
	if t == nil {
		return 0
	}
	return len(t.Columns)
}

// SelectedRow returns the currently selected row data, or nil if no selection.
func (t *InteractiveTable) SelectedRow() []string {
	if t == nil || t.selected < 0 || t.selected >= len(t.Rows) {
		return nil
	}
	return t.Rows[t.selected]
}

// GetCell returns the cell value at the given row and column.
func (t *InteractiveTable) GetCell(row, col int) string {
	if t == nil || row < 0 || row >= len(t.Rows) {
		return ""
	}
	if col < 0 || col >= len(t.Rows[row]) {
		return ""
	}
	return t.Rows[row][col]
}

// SetCell updates a cell value at the given row and column.
func (t *InteractiveTable) SetCell(row, col int, value string) {
	if t == nil || row < 0 || row >= len(t.Rows) {
		return
	}
	for len(t.Rows[row]) <= col {
		t.Rows[row] = append(t.Rows[row], "")
	}
	t.Rows[row][col] = value
}

// Measure returns the desired size.
func (t *InteractiveTable) Measure(constraints runtime.Constraints) runtime.Size {
	if t == nil {
		return constraints.MinSize()
	}
	height := minInt(len(t.Rows)+1, constraints.MaxHeight)
	if height <= 0 {
		height = constraints.MinHeight
	}
	return constraints.Constrain(runtime.Size{Width: constraints.MaxWidth, Height: height})
}

// Render draws the table.
func (t *InteractiveTable) Render(ctx runtime.RenderContext) {
	if t == nil {
		return
	}
	t.syncA11y()
	outer := t.Bounds()
	content := t.ContentBounds()
	if outer.Width <= 0 || outer.Height <= 0 {
		return
	}
	baseStyle := mergeBackendStyles(resolveBaseStyle(ctx, t, backend.DefaultStyle(), false), t.style)
	ctx.Buffer.Fill(outer, ' ', baseStyle)
	if content.Width <= 0 || content.Height <= 0 {
		return
	}
	widths := t.columnWidths(content.Width)
	if len(widths) == 0 {
		return
	}
	headerStyle := mergeBackendStyles(baseStyle, t.headerStyle)
	x := content.X
	for i, col := range t.Columns {
		if x >= content.X+content.Width {
			break
		}
		width := widths[i]
		title := truncateString(col.Title, width)
		writePadded(ctx.Buffer, x, content.Y, width, title, headerStyle)
		x += width + 1
	}

	rowArea := content.Height - 1
	if rowArea <= 0 {
		return
	}
	if t.selected < 0 {
		t.selected = 0
	}
	if t.selected >= len(t.Rows) {
		t.selected = len(t.Rows) - 1
	}
	if t.selected < t.offset {
		t.offset = t.selected
	}
	if t.selected >= t.offset+rowArea {
		t.offset = t.selected - rowArea + 1
	}
	for row := 0; row < rowArea; row++ {
		rowIndex := t.offset + row
		if rowIndex < 0 || rowIndex >= len(t.Rows) {
			break
		}
		style := baseStyle
		if rowIndex == t.selected {
			style = mergeBackendStyles(baseStyle, t.selectedStyle)
		}
		x = content.X
		for colIndex, width := range widths {
			if x >= content.X+content.Width {
				break
			}
			cell := ""
			if colIndex < len(t.Rows[rowIndex]) {
				cell = t.Rows[rowIndex][colIndex]
			}
			cell = truncateString(cell, width)
			writePadded(ctx.Buffer, x, content.Y+1+row, width, cell, style)
			x += width + 1
		}
	}
}

// HandleMessage handles row navigation and mouse selection.
func (t *InteractiveTable) HandleMessage(msg runtime.Message) runtime.HandleResult {
	if t == nil {
		return runtime.Unhandled()
	}
	switch ev := msg.(type) {
	case runtime.MouseMsg:
		switch ev.Button {
		case runtime.MouseWheelUp:
			t.ScrollBy(0, -1)
			return runtime.Handled()
		case runtime.MouseWheelDown:
			t.ScrollBy(0, 1)
			return runtime.Handled()
		case runtime.MouseLeft:
			if ev.Action == runtime.MousePress {
				if t.selectRowAt(ev.X, ev.Y) {
					return runtime.Handled()
				}
			}
		}
	case runtime.KeyMsg:
		if !t.IsFocused() {
			return runtime.Unhandled()
		}
		bounds := t.Bounds()
		switch ev.Key {
		case terminal.KeyUp:
			t.setSelected(t.selected - 1)
			return runtime.Handled()
		case terminal.KeyDown:
			t.setSelected(t.selected + 1)
			return runtime.Handled()
		case terminal.KeyPageUp:
			t.setSelected(t.selected - bounds.Height)
			return runtime.Handled()
		case terminal.KeyPageDown:
			t.setSelected(t.selected + bounds.Height)
			return runtime.Handled()
		case terminal.KeyHome:
			t.setSelected(0)
			return runtime.Handled()
		case terminal.KeyEnd:
			t.setSelected(len(t.Rows) - 1)
			return runtime.Handled()
		}
	}
	return runtime.Unhandled()
}

func (t *InteractiveTable) selectRowAt(x, y int) bool {
	content := t.ContentBounds()
	if x < content.X || x >= content.X+content.Width {
		return false
	}
	row := y - content.Y - 1
	if row < 0 || row >= content.Height-1 {
		return false
	}
	rowIndex := t.offset + row
	if rowIndex < 0 || rowIndex >= len(t.Rows) {
		return false
	}
	t.setSelected(rowIndex)
	t.Invalidate()
	return true
}

func (t *InteractiveTable) setSelected(index int) {
	if t == nil {
		return
	}
	if len(t.Rows) == 0 {
		t.selected = 0
		return
	}
	if index < 0 {
		index = 0
	}
	if index >= len(t.Rows) {
		index = len(t.Rows) - 1
	}
	t.selected = index
	t.syncA11y()
}

func (t *InteractiveTable) syncA11y() {
	if t == nil {
		return
	}
	if t.Base.Role == "" {
		t.Base.Role = accessibility.RoleTable
	}
	label := strings.TrimSpace(t.label)
	if label == "" {
		label = "Table"
	}
	t.Base.Label = label
	t.Base.Description = fmt.Sprintf("%d rows, %d columns", len(t.Rows), len(t.Columns))
	if t.selected >= 0 && t.selected < len(t.Rows) {
		t.Base.Value = &accessibility.ValueInfo{Text: t.selectedRowSummary()}
	} else {
		t.Base.Value = nil
	}
}

func (t *InteractiveTable) selectedRowSummary() string {
	if t == nil || t.selected < 0 || t.selected >= len(t.Rows) {
		return ""
	}
	row := t.Rows[t.selected]
	if len(row) == 0 {
		return ""
	}
	out := make([]string, 0, len(row))
	for _, cell := range row {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			continue
		}
		out = append(out, cell)
	}
	return strings.Join(out, " | ")
}

func (t *InteractiveTable) columnWidths(total int) []int {
	if len(t.Columns) == 0 {
		return nil
	}
	if total == t.cachedTotal && len(t.cachedWidths) == len(t.Columns) && t.cachedSig == t.columnsSignature() {
		return t.cachedWidths
	}
	available := total - (len(t.Columns) - 1)
	if available < 0 {
		available = 0
	}
	fixed := 0
	flexCount := 0
	for _, col := range t.Columns {
		if col.Width > 0 {
			fixed += col.Width
		} else {
			flexCount++
		}
	}
	widths := make([]int, len(t.Columns))
	remaining := available - fixed
	if remaining < 0 {
		remaining = 0
	}
	flexWidth := 0
	if flexCount > 0 {
		flexWidth = remaining / flexCount
		if flexWidth <= 0 {
			flexWidth = 1
		}
	}
	for i, col := range t.Columns {
		if col.Width > 0 {
			widths[i] = col.Width
		} else {
			widths[i] = flexWidth
		}
	}
	t.cachedTotal = total
	t.cachedSig = t.columnsSignature()
	t.cachedWidths = widths
	return widths
}

func (t *InteractiveTable) columnsSignature() uint32 {
	if t == nil {
		return 0
	}
	var sig uint32 = uint32(len(t.Columns))
	for _, col := range t.Columns {
		sig = sig*31 + uint32(col.Width+1)
	}
	return sig
}

// ScrollBy scrolls selection by delta.
func (t *InteractiveTable) ScrollBy(dx, dy int) {
	if t == nil || len(t.Rows) == 0 || dy == 0 {
		return
	}
	t.setSelected(t.selected + dy)
	t.Invalidate()
}

// ScrollTo scrolls to an absolute row index.
func (t *InteractiveTable) ScrollTo(x, y int) {
	if t == nil || len(t.Rows) == 0 {
		return
	}
	t.setSelected(y)
	t.Invalidate()
}

// PageBy scrolls by a number of pages.
func (t *InteractiveTable) PageBy(pages int) {
	if t == nil || len(t.Rows) == 0 {
		return
	}
	pageSize := t.Bounds().Height - 1
	if pageSize < 1 {
		pageSize = 1
	}
	t.setSelected(t.selected + pages*pageSize)
	t.Invalidate()
}

// ScrollToStart scrolls to the first row.
func (t *InteractiveTable) ScrollToStart() {
	if t == nil || len(t.Rows) == 0 {
		return
	}
	t.setSelected(0)
	t.Invalidate()
}

// ScrollToEnd scrolls to the last row.
func (t *InteractiveTable) ScrollToEnd() {
	if t == nil || len(t.Rows) == 0 {
		return
	}
	t.setSelected(len(t.Rows) - 1)
	t.Invalidate()
}

var _ scroll.Controller = (*InteractiveTable)(nil)
var _ runtime.Widget = (*InteractiveTable)(nil)
var _ runtime.Focusable = (*InteractiveTable)(nil)
