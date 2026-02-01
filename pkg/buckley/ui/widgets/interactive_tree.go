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

// InteractiveTree renders a tree with mouse selection support.
type InteractiveTree struct {
	uiwidgets.FocusableBase
	Root          *uiwidgets.TreeNode
	selectedIndex int
	offset        int
	label         string
	style         backend.Style
	selectedStyle backend.Style
	indentCache   []string
	flatCache     []treeRow
	flatDirty     bool
	rootRef       *uiwidgets.TreeNode
}

// NewInteractiveTree creates a tree widget.
func NewInteractiveTree(root *uiwidgets.TreeNode) *InteractiveTree {
	tree := &InteractiveTree{
		Root:          root,
		selectedIndex: 0,
		label:         "Tree",
		style:         backend.DefaultStyle(),
		selectedStyle: backend.DefaultStyle().Reverse(true),
		flatDirty:     true,
		rootRef:       root,
	}
	tree.Base.Role = accessibility.RoleTree
	tree.syncA11y()
	return tree
}

// SetStyle updates the base tree style.
func (t *InteractiveTree) SetStyle(style backend.Style) {
	if t == nil {
		return
	}
	t.style = style
}

// SetSelectedStyle updates the selected row style.
func (t *InteractiveTree) SetSelectedStyle(style backend.Style) {
	if t == nil {
		return
	}
	t.selectedStyle = style
}

// StyleType returns the selector type name.
func (t *InteractiveTree) StyleType() string {
	return "Tree"
}

// SetRoot updates the tree root and clears cached rows.
func (t *InteractiveTree) SetRoot(root *uiwidgets.TreeNode) {
	if t == nil {
		return
	}
	t.Root = root
	t.rootRef = root
	t.flatDirty = true
	t.syncA11y()
}

// SetLabel updates the accessibility label.
func (t *InteractiveTree) SetLabel(label string) {
	if t == nil {
		return
	}
	t.label = label
	t.syncA11y()
}

// SelectedIndex returns the currently selected row index.
func (t *InteractiveTree) SelectedIndex() int {
	if t == nil {
		return 0
	}
	return t.selectedIndex
}

// SetSelected updates the selected row index.
func (t *InteractiveTree) SetSelected(index int) {
	if t == nil {
		return
	}
	rows := t.flatten()
	t.setSelected(index, len(rows))
}

// Measure returns desired size.
func (t *InteractiveTree) Measure(constraints runtime.Constraints) runtime.Size {
	if t == nil {
		return constraints.MinSize()
	}
	count := len(t.flatten())
	height := minInt(count, constraints.MaxHeight)
	if height <= 0 {
		height = constraints.MinHeight
	}
	return constraints.Constrain(runtime.Size{Width: constraints.MaxWidth, Height: height})
}

// Render draws the tree.
func (t *InteractiveTree) Render(ctx runtime.RenderContext) {
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
	rows := t.flatten()
	if len(rows) == 0 {
		return
	}
	if t.selectedIndex < 0 {
		t.selectedIndex = 0
	}
	if t.selectedIndex >= len(rows) {
		t.selectedIndex = len(rows) - 1
	}
	if t.selectedIndex < t.offset {
		t.offset = t.selectedIndex
	}
	if t.selectedIndex >= t.offset+content.Height {
		t.offset = t.selectedIndex - content.Height + 1
	}
	for i := 0; i < content.Height; i++ {
		rowIndex := t.offset + i
		if rowIndex < 0 || rowIndex >= len(rows) {
			break
		}
		row := rows[rowIndex]
		style := baseStyle
		if rowIndex == t.selectedIndex {
			style = mergeBackendStyles(baseStyle, t.selectedStyle)
		}
		prefix := "  "
		if len(row.node.Children) > 0 {
			if row.node.Expanded {
				prefix = "- "
			} else {
				prefix = "+ "
			}
		}
		indent := t.indent(row.depth)
		line := indent + prefix + row.node.Label
		line = truncateString(line, content.Width)
		writePadded(ctx.Buffer, content.X, content.Y+i, content.Width, line, style)
	}
}

// HandleMessage handles navigation and mouse selection.
func (t *InteractiveTree) HandleMessage(msg runtime.Message) runtime.HandleResult {
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
		rows := t.flatten()
		switch ev.Key {
		case terminal.KeyUp:
			t.setSelected(t.selectedIndex-1, len(rows))
			return runtime.Handled()
		case terminal.KeyDown:
			t.setSelected(t.selectedIndex+1, len(rows))
			return runtime.Handled()
		case terminal.KeyLeft:
			if row := t.selectedRow(rows); row != nil && row.node.Expanded {
				row.node.Expanded = false
				t.flatDirty = true
				t.syncA11y()
			}
			return runtime.Handled()
		case terminal.KeyRight:
			if row := t.selectedRow(rows); row != nil && len(row.node.Children) > 0 {
				row.node.Expanded = true
				t.flatDirty = true
				t.syncA11y()
			}
			return runtime.Handled()
		case terminal.KeyEnter:
			if row := t.selectedRow(rows); row != nil && len(row.node.Children) > 0 {
				row.node.Expanded = !row.node.Expanded
				t.flatDirty = true
				t.syncA11y()
			}
			return runtime.Handled()
		}
	}
	return runtime.Unhandled()
}

type treeRow struct {
	node  *uiwidgets.TreeNode
	depth int
}

func (t *InteractiveTree) selectRowAt(x, y int) bool {
	content := t.ContentBounds()
	if x < content.X || x >= content.X+content.Width {
		return false
	}
	row := y - content.Y
	if row < 0 || row >= content.Height {
		return false
	}
	rows := t.flatten()
	rowIndex := t.offset + row
	if rowIndex < 0 || rowIndex >= len(rows) {
		return false
	}
	t.setSelected(rowIndex, len(rows))
	rowData := rows[rowIndex]
	if len(rowData.node.Children) > 0 {
		relativeX := x - content.X
		indentWidth := rowData.depth * 2
		if relativeX < indentWidth+2 {
			rowData.node.Expanded = !rowData.node.Expanded
			t.flatDirty = true
			t.syncA11y()
		}
	}
	t.Invalidate()
	return true
}

func (t *InteractiveTree) flatten() []treeRow {
	if t == nil || t.Root == nil {
		return nil
	}
	if t.rootRef != t.Root {
		t.rootRef = t.Root
		t.flatDirty = true
	}
	if !t.flatDirty {
		return t.flatCache
	}
	rows := t.flatCache[:0]
	var walk func(node *uiwidgets.TreeNode, depth int)
	walk = func(node *uiwidgets.TreeNode, depth int) {
		if node == nil {
			return
		}
		rows = append(rows, treeRow{node: node, depth: depth})
		if node.Expanded {
			for _, child := range node.Children {
				walk(child, depth+1)
			}
		}
	}
	walk(t.Root, 0)
	t.flatCache = rows
	t.flatDirty = false
	return t.flatCache
}

func (t *InteractiveTree) setSelected(index int, count int) {
	if count == 0 {
		t.selectedIndex = 0
		return
	}
	if index < 0 {
		index = 0
	}
	if index >= count {
		index = count - 1
	}
	t.selectedIndex = index
	t.syncA11y()
}

func (t *InteractiveTree) selectedRow(rows []treeRow) *treeRow {
	if t.selectedIndex < 0 || t.selectedIndex >= len(rows) {
		return nil
	}
	return &rows[t.selectedIndex]
}

// ScrollBy scrolls selection by delta.
func (t *InteractiveTree) ScrollBy(dx, dy int) {
	if t == nil || dy == 0 {
		return
	}
	rows := t.flatten()
	t.setSelected(t.selectedIndex+dy, len(rows))
	t.Invalidate()
}

// ScrollTo scrolls to an absolute row index.
func (t *InteractiveTree) ScrollTo(x, y int) {
	if t == nil {
		return
	}
	rows := t.flatten()
	t.setSelected(y, len(rows))
	t.Invalidate()
}

// PageBy scrolls by a number of pages.
func (t *InteractiveTree) PageBy(pages int) {
	if t == nil {
		return
	}
	rows := t.flatten()
	pageSize := t.Bounds().Height
	if pageSize < 1 {
		pageSize = 1
	}
	t.setSelected(t.selectedIndex+pages*pageSize, len(rows))
	t.Invalidate()
}

func (t *InteractiveTree) indent(depth int) string {
	if depth <= 0 {
		return ""
	}
	if len(t.indentCache) == 0 {
		t.indentCache = []string{""}
	}
	for len(t.indentCache) <= depth {
		t.indentCache = append(t.indentCache, t.indentCache[len(t.indentCache)-1]+"  ")
	}
	return t.indentCache[depth]
}

// ScrollToStart scrolls to the first row.
func (t *InteractiveTree) ScrollToStart() {
	if t == nil {
		return
	}
	rows := t.flatten()
	t.setSelected(0, len(rows))
	t.Invalidate()
}

// ScrollToEnd scrolls to the last row.
func (t *InteractiveTree) ScrollToEnd() {
	if t == nil {
		return
	}
	rows := t.flatten()
	t.setSelected(len(rows)-1, len(rows))
	t.Invalidate()
}

func (t *InteractiveTree) syncA11y() {
	if t == nil {
		return
	}
	if t.Base.Role == "" {
		t.Base.Role = accessibility.RoleTree
	}
	label := strings.TrimSpace(t.label)
	if label == "" {
		label = "Tree"
	}
	t.Base.Label = label
	rows := t.flatten()
	t.Base.Description = fmt.Sprintf("%d items", len(rows))
	if row := t.selectedRow(rows); row != nil && row.node != nil {
		t.Base.Value = &accessibility.ValueInfo{Text: row.node.Label}
		if len(row.node.Children) > 0 {
			t.Base.State.Expanded = accessibility.BoolPtr(row.node.Expanded)
		} else {
			t.Base.State.Expanded = nil
		}
	} else {
		t.Base.Value = nil
		t.Base.State.Expanded = nil
	}
}

var _ scroll.Controller = (*InteractiveTree)(nil)
var _ runtime.Widget = (*InteractiveTree)(nil)
var _ runtime.Focusable = (*InteractiveTree)(nil)
