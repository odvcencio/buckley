package widgets

import (
	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// ModalOverlay draws a backdrop and hosts a modal child widget.
type ModalOverlay struct {
	uiwidgets.Base
	child           runtime.Widget
	childBounds     runtime.Rect
	backdropStyle   backend.Style
	closeOnBackdrop bool
	onClose         func()
}

// NewModalOverlay creates a modal overlay wrapper.
func NewModalOverlay(child runtime.Widget) *ModalOverlay {
	o := &ModalOverlay{child: child, closeOnBackdrop: true}
	o.Base.Role = accessibility.RoleDialog
	o.Base.State.Modal = true
	return o
}

// SetBackdropStyle configures the backdrop fill style.
func (o *ModalOverlay) SetBackdropStyle(style backend.Style) {
	if o == nil {
		return
	}
	o.backdropStyle = style
}

// SetCloseOnBackdrop toggles click-to-close behavior.
func (o *ModalOverlay) SetCloseOnBackdrop(close bool) {
	if o == nil {
		return
	}
	o.closeOnBackdrop = close
}

// SetOnClose registers a callback invoked when the overlay closes.
func (o *ModalOverlay) SetOnClose(fn func()) {
	if o == nil {
		return
	}
	o.onClose = fn
}

// Measure returns the full available size.
func (o *ModalOverlay) Measure(constraints runtime.Constraints) runtime.Size {
	return runtime.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight}
}

// Layout assigns bounds and lets the child layout itself.
func (o *ModalOverlay) Layout(bounds runtime.Rect) {
	o.Base.Layout(bounds)
	if o.child == nil {
		o.childBounds = runtime.Rect{}
		return
	}
	o.child.Layout(bounds)
	if provider, ok := o.child.(runtime.BoundsProvider); ok {
		o.childBounds = provider.Bounds()
	} else {
		o.childBounds = bounds
	}
}

// Render draws the backdrop and child.
func (o *ModalOverlay) Render(ctx runtime.RenderContext) {
	bounds := o.Bounds()
	if bounds.Width > 0 && bounds.Height > 0 {
		ctx.Buffer.Fill(bounds, ' ', o.backdropStyle)
	}
	if o.child != nil {
		o.child.Render(ctx.Sub(o.childBounds))
	}
}

// HandleMessage closes on backdrop clicks and forwards input.
func (o *ModalOverlay) HandleMessage(msg runtime.Message) runtime.HandleResult {
	if mouse, ok := msg.(runtime.MouseMsg); ok && o.closeOnBackdrop {
		if mouse.Action == runtime.MousePress && mouse.Button == runtime.MouseLeft {
			if !o.childBounds.Contains(mouse.X, mouse.Y) {
				if o.onClose != nil {
					o.onClose()
				}
				return runtime.WithCommand(runtime.PopOverlay{})
			}
		}
	}
	if o.child == nil {
		return runtime.Unhandled()
	}
	result := o.child.HandleMessage(msg)
	if len(result.Commands) == 0 {
		return result
	}
	for _, cmd := range result.Commands {
		if _, ok := cmd.(runtime.PopOverlay); ok {
			if o.onClose != nil {
				o.onClose()
			}
			break
		}
	}
	return result
}

// ChildWidgets returns the child widget for traversal.
func (o *ModalOverlay) ChildWidgets() []runtime.Widget {
	if o.child == nil {
		return nil
	}
	return []runtime.Widget{o.child}
}

// HitSelf ensures backdrop receives mouse events.
func (o *ModalOverlay) HitSelf() bool {
	return true
}

// CenteredOverlay positions a child widget in the center of the available bounds.
type CenteredOverlay struct {
	uiwidgets.Base
	child       runtime.Widget
	childBounds runtime.Rect
}

// NewCenteredOverlay creates a new centered overlay wrapper.
func NewCenteredOverlay(child runtime.Widget) *CenteredOverlay {
	o := &CenteredOverlay{child: child}
	o.Base.Role = accessibility.RolePresentation
	return o
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
	o := &PositionedOverlay{child: child, x: x, y: y}
	o.Base.Role = accessibility.RolePresentation
	return o
}

// SetPosition updates the overlay origin.
func (o *PositionedOverlay) SetPosition(x, y int) {
	if o == nil {
		return
	}
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
var _ runtime.Widget = (*ModalOverlay)(nil)
var _ runtime.ChildProvider = (*ModalOverlay)(nil)
var _ runtime.HitSelfProvider = (*ModalOverlay)(nil)
