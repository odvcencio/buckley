package widgets

import (
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// AlertBanner renders an alert widget centered near the top of the screen.
type AlertBanner struct {
	uiwidgets.Base
	alert     *uiwidgets.Alert
	panel     *uiwidgets.Panel
	visible   bool
	offsetY   int
	childRect runtime.Rect
}

// NewAlertBanner creates a banner around the alert widget.
func NewAlertBanner(alert *uiwidgets.Alert) *AlertBanner {
	panel := uiwidgets.NewPanel(alert).WithBorder(backend.DefaultStyle())
	panel.SetTitle("Alert")
	return &AlertBanner{
		alert:   alert,
		panel:   panel,
		offsetY: 1,
	}
}

// SetVisible controls visibility.
func (b *AlertBanner) SetVisible(visible bool) {
	b.visible = visible
}

// SetOffsetY changes the vertical offset from the top.
func (b *AlertBanner) SetOffsetY(offset int) {
	if offset < 0 {
		offset = 0
	}
	b.offsetY = offset
}

// SetBorderStyle updates the banner border style.
func (b *AlertBanner) SetBorderStyle(style backend.Style) {
	if b.panel != nil {
		b.panel.WithBorder(style)
	}
}

// Measure returns the full available size.
func (b *AlertBanner) Measure(constraints runtime.Constraints) runtime.Size {
	return runtime.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight}
}

// Layout positions the banner.
func (b *AlertBanner) Layout(bounds runtime.Rect) {
	b.Base.Layout(bounds)
	if b.panel == nil {
		return
	}
	size := b.panel.Measure(runtime.Constraints{MaxWidth: bounds.Width, MaxHeight: bounds.Height})
	x := bounds.X + (bounds.Width-size.Width)/2
	y := bounds.Y + b.offsetY
	if x < bounds.X {
		x = bounds.X
	}
	if y < bounds.Y {
		y = bounds.Y
	}
	if x+size.Width > bounds.X+bounds.Width {
		x = bounds.X + maxInt(0, bounds.Width-size.Width)
	}
	if y+size.Height > bounds.Y+bounds.Height {
		y = bounds.Y + maxInt(0, bounds.Height-size.Height)
	}
	b.childRect = runtime.Rect{X: x, Y: y, Width: size.Width, Height: size.Height}
	b.panel.Layout(b.childRect)
}

// Render draws the banner when visible.
func (b *AlertBanner) Render(ctx runtime.RenderContext) {
	if !b.visible || b.panel == nil {
		return
	}
	b.panel.Render(ctx.Sub(b.childRect))
}

// HandleMessage forwards input to the panel when visible.
func (b *AlertBanner) HandleMessage(msg runtime.Message) runtime.HandleResult {
	if !b.visible || b.panel == nil {
		return runtime.Unhandled()
	}
	return b.panel.HandleMessage(msg)
}

// ChildWidgets returns the panel for traversal.
func (b *AlertBanner) ChildWidgets() []runtime.Widget {
	if b.panel == nil {
		return nil
	}
	return []runtime.Widget{b.panel}
}

var _ runtime.Widget = (*AlertBanner)(nil)
var _ runtime.ChildProvider = (*AlertBanner)(nil)

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
