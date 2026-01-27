package widgets

import (
	"github.com/odvcencio/fluffy-ui/runtime"
	uiwidgets "github.com/odvcencio/fluffy-ui/widgets"
)

// CenteredOverlay positions a child widget in the center of the available bounds.
type CenteredOverlay struct {
	uiwidgets.Base
	child       runtime.Widget
	childBounds runtime.Rect
}

// NewCenteredOverlay creates a new centered overlay wrapper.
func NewCenteredOverlay(child runtime.Widget) *CenteredOverlay {
	return &CenteredOverlay{child: child}
}

// Measure returns the full available size.
func (o *CenteredOverlay) Measure(constraints runtime.Constraints) runtime.Size {
	return runtime.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight}
}

// Layout centers the child within the bounds.
func (o *CenteredOverlay) Layout(bounds runtime.Rect) {
	o.Base.Layout(bounds)
	if o.child == nil {
		return
	}
	size := o.child.Measure(runtime.Constraints{MaxWidth: bounds.Width, MaxHeight: bounds.Height})
	x := bounds.X + (bounds.Width-size.Width)/2
	y := bounds.Y + (bounds.Height-size.Height)/2
	o.childBounds = runtime.Rect{X: x, Y: y, Width: size.Width, Height: size.Height}
	o.child.Layout(o.childBounds)
}

// Render draws the centered child.
func (o *CenteredOverlay) Render(ctx runtime.RenderContext) {
	if o.child == nil {
		return
	}
	o.child.Render(ctx.Sub(o.childBounds))
}

// HandleMessage forwards input to the child.
func (o *CenteredOverlay) HandleMessage(msg runtime.Message) runtime.HandleResult {
	if o.child == nil {
		return runtime.Unhandled()
	}
	return o.child.HandleMessage(msg)
}

// ChildWidgets returns the child widget for traversal.
func (o *CenteredOverlay) ChildWidgets() []runtime.Widget {
	if o.child == nil {
		return nil
	}
	return []runtime.Widget{o.child}
}

// PositionedOverlay positions a child widget at a specific point.
type PositionedOverlay struct {
	uiwidgets.Base
	child       runtime.Widget
	x           int
	y           int
	childBounds runtime.Rect
}

// NewPositionedOverlay creates a positioned overlay wrapper.
func NewPositionedOverlay(child runtime.Widget, x, y int) *PositionedOverlay {
	return &PositionedOverlay{child: child, x: x, y: y}
}

// SetPosition updates the overlay origin.
func (o *PositionedOverlay) SetPosition(x, y int) {
	o.x = x
	o.y = y
}

// Measure returns the full available size.
func (o *PositionedOverlay) Measure(constraints runtime.Constraints) runtime.Size {
	return runtime.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight}
}

// Layout positions the child within the bounds.
func (o *PositionedOverlay) Layout(bounds runtime.Rect) {
	o.Base.Layout(bounds)
	if o.child == nil {
		return
	}
	size := o.child.Measure(runtime.Constraints{MaxWidth: bounds.Width, MaxHeight: bounds.Height})
	x := clampInt(o.x, bounds.X, bounds.X+bounds.Width-size.Width)
	y := clampInt(o.y, bounds.Y, bounds.Y+bounds.Height-size.Height)
	o.childBounds = runtime.Rect{X: x, Y: y, Width: size.Width, Height: size.Height}
	o.child.Layout(o.childBounds)
}

// Render draws the child at its position.
func (o *PositionedOverlay) Render(ctx runtime.RenderContext) {
	if o.child == nil {
		return
	}
	o.child.Render(ctx.Sub(o.childBounds))
}

// HandleMessage forwards input to the child.
func (o *PositionedOverlay) HandleMessage(msg runtime.Message) runtime.HandleResult {
	if o.child == nil {
		return runtime.Unhandled()
	}
	return o.child.HandleMessage(msg)
}

// ChildWidgets returns the child widget for traversal.
func (o *PositionedOverlay) ChildWidgets() []runtime.Widget {
	if o.child == nil {
		return nil
	}
	return []runtime.Widget{o.child}
}

func clampInt(val, minVal, maxVal int) int {
	if maxVal < minVal {
		return minVal
	}
	if val < minVal {
		return minVal
	}
	if val > maxVal {
		return maxVal
	}
	return val
}

var _ runtime.Widget = (*CenteredOverlay)(nil)
var _ runtime.ChildProvider = (*CenteredOverlay)(nil)
var _ runtime.Widget = (*PositionedOverlay)(nil)
var _ runtime.ChildProvider = (*PositionedOverlay)(nil)
