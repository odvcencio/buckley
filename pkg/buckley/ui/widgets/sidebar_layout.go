package widgets

import (
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/scroll"
	"github.com/odvcencio/fluffyui/state"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// Measure returns the preferred size.
func (s *Sidebar) Measure(constraints runtime.Constraints) runtime.Size {
	width := s.config.Width
	if width <= 0 {
		width = 24
	}
	if constraints.MaxWidth < width {
		width = constraints.MaxWidth
	}
	return runtime.Size{Width: width, Height: constraints.MaxHeight}
}

// SetWidth changes the sidebar width within configured min/max bounds.
func (s *Sidebar) SetWidth(width int) {
	if s == nil {
		return
	}
	if s.widthSig != nil {
		s.writeWidthSignal(width)
		return
	}
	if s.setWidthLocal(width) {
		s.requestRelayout()
	}
}

func (s *Sidebar) normalizeWidth(width int) int {
	if width < s.config.MinWidth {
		width = s.config.MinWidth
	}
	if width > s.config.MaxWidth {
		width = s.config.MaxWidth
	}
	return width
}

func (s *Sidebar) setWidthLocal(width int) bool {
	if s == nil {
		return false
	}
	width = s.normalizeWidth(width)
	if s.config.Width == width {
		return false
	}
	s.config.Width = width
	return true
}

func (s *Sidebar) writeWidthSignal(width int) {
	if s == nil || s.widthSig == nil {
		return
	}
	width = s.normalizeWidth(width)
	if writable, ok := s.widthSig.(state.Writable[int]); ok && writable != nil {
		writable.Set(width)
	}
}

// Width returns the current sidebar width.
func (s *Sidebar) Width() int {
	return s.config.Width
}

// SetSelectedTab selects the sidebar tab index.
func (s *Sidebar) SetSelectedTab(index int) {
	if s == nil {
		return
	}
	s.writeTabIndex(index)
}

func (s *Sidebar) normalizeTabIndex(index int) int {
	if s.tabs == nil || len(s.tabs.Tabs) == 0 {
		return 0
	}
	if index < 0 {
		return 0
	}
	if index >= len(s.tabs.Tabs) {
		return len(s.tabs.Tabs) - 1
	}
	return index
}

func (s *Sidebar) writeTabIndex(index int) {
	if s == nil || s.tabIndexSig == nil {
		return
	}
	index = s.normalizeTabIndex(index)
	if writable, ok := s.tabIndexSig.(state.Writable[int]); ok && writable != nil {
		writable.Set(index)
	}
}

func (s *Sidebar) applyTabIndex(index int) {
	if s.tabs == nil {
		return
	}
	normalized := s.normalizeTabIndex(index)
	if normalized != index {
		s.writeTabIndex(normalized)
	}
	if s.tabs.SelectedIndex() != normalized {
		s.tabs.SetSelected(normalized)
	}
}

// Grow increases the sidebar width by delta characters.
func (s *Sidebar) Grow(delta int) {
	s.SetWidth(s.config.Width + delta)
}

// Shrink decreases the sidebar width by delta characters.
func (s *Sidebar) Shrink(delta int) {
	s.SetWidth(s.config.Width - delta)
}

// Layout stores the assigned bounds.
func (s *Sidebar) Layout(bounds runtime.Rect) {
	s.FocusableBase.Layout(bounds)
	if s.tabs != nil {
		s.tabs.Layout(bounds)
	}
}

// Render draws the sidebar.
func (s *Sidebar) Render(ctx runtime.RenderContext) {
	if s.tabs == nil {
		return
	}
	bounds := s.Bounds()
	if bounds.Width <= 0 || bounds.Height <= 0 {
		return
	}
	s.tabs.Render(ctx)
	borderStyle := s.borderStyle
	if s.resizeHover || s.resizing {
		borderStyle = borderStyle.Bold(true)
	}
	for y := bounds.Y; y < bounds.Y+bounds.Height; y++ {
		ctx.Buffer.Set(bounds.X, y, '│', borderStyle)
	}
}

// HandleMessage processes sidebar input.
func (s *Sidebar) HandleMessage(msg runtime.Message) runtime.HandleResult {
	if s.tabs == nil {
		return runtime.Unhandled()
	}
	if mouse, ok := msg.(runtime.MouseMsg); ok {
		if s.handleResizeMouse(mouse) {
			return runtime.Handled()
		}
		if mouse.Action == runtime.MousePress && mouse.Button == runtime.MouseLeft {
			if s.handleTabClick(mouse.X, mouse.Y) {
				return runtime.Handled()
			}
		}
	}
	before := s.tabs.SelectedIndex()
	result := s.tabs.HandleMessage(msg)
	if result.Handled {
		after := s.tabs.SelectedIndex()
		if after != before {
			s.writeTabIndex(after)
		}
	}
	return result
}

func (s *Sidebar) handleTabClick(x, y int) bool {
	if s == nil || s.tabs == nil {
		return false
	}
	content := s.tabs.ContentBounds()
	if content.Width <= 0 || content.Height <= 0 {
		return false
	}
	if y != content.Y {
		return false
	}
	if x < content.X || x >= content.X+content.Width {
		return false
	}
	cursor := content.X
	maxX := content.X + content.Width
	for i, tab := range s.tabs.Tabs {
		label := " " + tab.Title + " "
		labelWidth := textWidth(label)
		if cursor >= maxX {
			break
		}
		if cursor+labelWidth > maxX {
			labelWidth = max(0, maxX-cursor)
		}
		if x >= cursor && x < cursor+labelWidth {
			s.writeTabIndex(i)
			return true
		}
		cursor += labelWidth
	}
	return false
}

func (s *Sidebar) handleResizeMouse(mouse runtime.MouseMsg) bool {
	if s == nil {
		return false
	}
	switch mouse.Action {
	case runtime.MousePress:
		if mouse.Button != runtime.MouseLeft {
			return false
		}
		if !s.resizeHandleHit(mouse.X, mouse.Y) {
			return false
		}
		s.resizing = true
		s.resizeHover = true
		s.updateWidthFromMouse(mouse.X)
		s.requestInvalidate()
		return true
	case runtime.MouseMove:
		if s.resizing {
			s.updateWidthFromMouse(mouse.X)
			return true
		}
		hover := s.resizeHandleHit(mouse.X, mouse.Y)
		if hover != s.resizeHover {
			s.resizeHover = hover
			s.requestInvalidate()
		}
		return false
	case runtime.MouseRelease:
		if mouse.Button != runtime.MouseLeft {
			return false
		}
		if !s.resizing {
			return false
		}
		s.resizing = false
		s.resizeHover = s.resizeHandleHit(mouse.X, mouse.Y)
		s.requestInvalidate()
		return true
	default:
		return false
	}
}

func (s *Sidebar) resizeHandleHit(x, y int) bool {
	bounds := s.Bounds()
	if bounds.Width <= 0 || bounds.Height <= 0 {
		return false
	}
	if y < bounds.Y || y >= bounds.Y+bounds.Height {
		return false
	}
	return x == bounds.X
}

func (s *Sidebar) updateWidthFromMouse(x int) {
	bounds := s.Bounds()
	if bounds.Width <= 0 {
		return
	}
	newWidth := bounds.X + bounds.Width - x
	s.SetWidth(newWidth)
}

func (s *Sidebar) activeScrollView() *uiwidgets.ScrollView {
	if s == nil || s.tabs == nil {
		return nil
	}
	switch s.tabs.SelectedIndex() {
	case 0:
		if s.status != nil {
			return s.status.ScrollView()
		}
	case 1:
		if s.files != nil {
			return s.files.ScrollView()
		}
	}
	return nil
}

// ScrollBy scrolls the active sidebar panel.
func (s *Sidebar) ScrollBy(dx, dy int) {
	if view := s.activeScrollView(); view != nil {
		view.ScrollBy(dx, dy)
	}
}

// ScrollTo scrolls the active sidebar panel to an offset.
func (s *Sidebar) ScrollTo(x, y int) {
	if view := s.activeScrollView(); view != nil {
		view.ScrollTo(x, y)
	}
}

// PageBy scrolls the active sidebar panel by page count.
func (s *Sidebar) PageBy(pages int) {
	if view := s.activeScrollView(); view != nil {
		view.PageBy(pages)
	}
}

// ScrollToStart scrolls the active sidebar panel to the start.
func (s *Sidebar) ScrollToStart() {
	if view := s.activeScrollView(); view != nil {
		view.ScrollToStart()
	}
}

// ScrollToEnd scrolls the active sidebar panel to the end.
func (s *Sidebar) ScrollToEnd() {
	if view := s.activeScrollView(); view != nil {
		view.ScrollToEnd()
	}
}

// ChildWidgets returns child widgets for traversal.
func (s *Sidebar) ChildWidgets() []runtime.Widget {
	if s.tabs == nil {
		return nil
	}
	return []runtime.Widget{s.tabs}
}

// WebLinkAt returns a sidebar web link hit if the point is inside one.
func (s *Sidebar) WebLinkAt(x, y int) (string, bool) {
	return "", false
}

func (s *Sidebar) updateAllPanels() {
	if s.status != nil {
		s.status.updateAllPanels()
	}
	if s.files != nil {
		s.files.updateAllPanels()
	}
}

var _ runtime.Widget = (*Sidebar)(nil)
var _ runtime.ChildProvider = (*Sidebar)(nil)
var _ runtime.Bindable = (*Sidebar)(nil)
var _ runtime.Unbindable = (*Sidebar)(nil)
var _ scroll.Controller = (*Sidebar)(nil)
