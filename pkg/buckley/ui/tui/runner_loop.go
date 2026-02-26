package tui

import (
	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/runtime"
)

// update handles messages in the event loop.
// This is the Buckley-specific update function that processes domain messages.
func (r *Runner) update(app *runtime.App, msg runtime.Message) bool {
	if mouse, ok := msg.(runtime.MouseMsg); ok {
		r.handleDragDrop(app, mouse)
		r.handleMouseFocus(app, mouse)
	}
	if custom, ok := msg.(runtime.CustomMsg); ok {
		if update, ok := custom.Value.(stylesheetUpdateMsg); ok {
			r.handleStylesheetUpdate(update)
			return true
		}
		if d, ok := custom.Value.(dispatchFunc); ok {
			if d.fn != nil {
				d.fn()
			}
			return true
		}
	}
	if key, ok := msg.(runtime.KeyMsg); ok {
		key = normalizeCtrlGChord(key)
		msg = key
		r.maybeCaptureInput(app, key)
	}
	r.showNextApproval()

	// First, let the default handler process terminal events
	if runtime.DefaultUpdate(app, msg) {
		return true
	}

	// Handle tick for coalescer
	if _, ok := msg.(runtime.TickMsg); ok {
		r.coalescer.Tick()
	}

	return r.flushPending()
}

func (r *Runner) handleCommand(cmd runtime.Command) bool {
	switch c := cmd.(type) {
	case runtime.PushOverlay:
		if r.app != nil {
			if screen := r.app.Screen(); screen != nil {
				screen.PushLayer(c.Widget, c.Modal)
				r.syncOverlayKeymap(screen)
				r.focusTopLayer(screen)
				return true
			}
		}
		return false
	case runtime.PopOverlay:
		if r.app != nil {
			if screen := r.app.Screen(); screen != nil {
				if r.overlayCount(screen) == 0 {
					r.syncOverlayKeymap(screen)
					r.focusTopLayer(screen)
					return true
				}
				if screen.PopLayer() {
					r.syncOverlayKeymap(screen)
					r.focusTopLayer(screen)
					return true
				}
				return false
			}
		}
		return false
	case runtime.FileSelected:
		if r.onFileSelect != nil {
			r.onFileSelect(c.Path)
		}
		return false
	case buckleywidgets.ApprovalResponse:
		return r.handleApprovalResponse(c)
	default:
		return false
	}
}

func (r *Runner) handleApprovalResponse(resp buckleywidgets.ApprovalResponse) bool {
	if r == nil {
		return false
	}
	if r.onApproval != nil {
		r.onApproval(resp.RequestID, resp.Approved, resp.AlwaysAllow)
	}

	r.approvalMu.Lock()
	r.approvalActive = false
	r.approvalMu.Unlock()
	r.showNextApproval()
	return true
}

func (r *Runner) showNextApproval() {
	if r == nil || r.app == nil || r.app.Screen() == nil {
		return
	}
	r.approvalMu.Lock()
	if r.approvalActive || len(r.approvalQueue) == 0 {
		r.approvalMu.Unlock()
		return
	}
	request := r.approvalQueue[0]
	r.approvalQueue = r.approvalQueue[1:]
	r.approvalActive = true
	r.approvalMu.Unlock()
	r.showApprovalOverlay(request)
}

func (r *Runner) flushPending() bool {
	if r == nil || r.coalescer == nil || r.chatService == nil {
		return false
	}
	flushed := r.coalescer.Drain()
	if len(flushed) == 0 {
		return false
	}
	currentSessionID := r.state.SessionID.Get()
	for _, item := range flushed {
		// Only append chunks belonging to the currently displayed session
		if item.SessionID != currentSessionID {
			continue
		}
		r.chatService.AppendToLastMessage(item.Text)
	}
	return true
}

// Announce sends an accessibility announcement.
func (r *Runner) Announce(text string, priority accessibility.Priority) {
	if r.app == nil {
		return
	}
	services := r.app.Services()
	if announcer := services.Announcer(); announcer != nil {
		announcer.Announce(text, priority)
	}
}
