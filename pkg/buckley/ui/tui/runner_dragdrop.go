package tui

import (
	"github.com/odvcencio/fluffyui/dragdrop"
	"github.com/odvcencio/fluffyui/runtime"
)

type dragCandidate struct {
	source dragdrop.Draggable
	startX int
	startY int
}

func (r *Runner) handleDragDrop(app *runtime.App, mouse runtime.MouseMsg) {
	if r == nil || app == nil {
		return
	}
	screen := app.Screen()
	if screen == nil {
		return
	}
	switch mouse.Action {
	case runtime.MousePress:
		if mouse.Button != runtime.MouseLeft {
			return
		}
		target := screen.WidgetAt(mouse.X, mouse.Y)
		if draggable, ok := target.(dragdrop.Draggable); ok {
			r.dragCandidate = &dragCandidate{source: draggable, startX: mouse.X, startY: mouse.Y}
		}
	case runtime.MouseMove:
		if r.dragging {
			r.updateDragTarget(screen, mouse)
			return
		}
		if r.dragCandidate == nil {
			return
		}
		if !dragThresholdReached(mouse.X-r.dragCandidate.startX, mouse.Y-r.dragCandidate.startY) {
			return
		}
		data := r.dragCandidate.source.DragStart()
		if data.Kind == "" {
			r.dragCandidate = nil
			return
		}
		if data.Source == nil {
			if widget, ok := r.dragCandidate.source.(runtime.Widget); ok {
				data.Source = widget
			}
		}
		r.dragging = true
		r.dragSource = r.dragCandidate.source
		r.dragData = data
		r.dragCandidate = nil
		r.updateDragTarget(screen, mouse)
	case runtime.MouseRelease:
		if mouse.Button != runtime.MouseLeft {
			return
		}
		if r.dragging {
			r.finishDrag(screen, mouse)
			return
		}
		r.dragCandidate = nil
	default:
	}
}

func (r *Runner) updateDragTarget(screen *runtime.Screen, mouse runtime.MouseMsg) {
	if r == nil || !r.dragging || screen == nil {
		return
	}
	target := screen.WidgetAt(mouse.X, mouse.Y)
	dropTarget, ok := target.(dragdrop.DropTarget)
	if !ok || dropTarget == nil {
		r.dragTarget = nil
		return
	}
	if !dropTarget.CanDrop(r.dragData) {
		r.dragTarget = nil
		return
	}
	r.dragTarget = dropTarget
	dropTarget.DropPreview(r.dragData, dragdrop.DropPosition{X: mouse.X, Y: mouse.Y})
}

func (r *Runner) finishDrag(screen *runtime.Screen, mouse runtime.MouseMsg) {
	if r == nil || !r.dragging {
		return
	}
	target := screen.WidgetAt(mouse.X, mouse.Y)
	dropTarget, ok := target.(dragdrop.DropTarget)
	dropped := false
	if ok && dropTarget != nil && dropTarget.CanDrop(r.dragData) {
		dropTarget.Drop(r.dragData, dragdrop.DropPosition{X: mouse.X, Y: mouse.Y})
		dropped = true
	}
	if r.dragSource != nil {
		r.dragSource.DragEnd(!dropped)
	}
	r.dragging = false
	r.dragData = dragdrop.DragData{}
	r.dragSource = nil
	r.dragTarget = nil
}

func dragThresholdReached(dx, dy int) bool {
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	return dx+dy >= 2
}
