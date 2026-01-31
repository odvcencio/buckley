package widgets

import (
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/state"
	"github.com/odvcencio/fluffyui/toast"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// SignalToastStack binds a toast stack to reactive state.
type SignalToastStack struct {
	uiwidgets.Component

	stack  *uiwidgets.ToastStack
	toasts state.Readable[[]*toast.Toast]
}

// NewSignalToastStack creates a new toast stack bound to a signal.
func NewSignalToastStack(toasts state.Readable[[]*toast.Toast]) *SignalToastStack {
	return &SignalToastStack{
		stack:  uiwidgets.NewToastStack(),
		toasts: toasts,
	}
}

// SetOnDismiss registers a handler for dismiss actions.
func (s *SignalToastStack) SetOnDismiss(fn func(id string)) {
	if s.stack == nil {
		return
	}
	s.stack.SetOnDismiss(fn)
}

// SetAnimationsEnabled toggles toast animations.
func (s *SignalToastStack) SetAnimationsEnabled(enabled bool) {
	if s.stack == nil {
		return
	}
	s.stack.SetAnimationsEnabled(enabled)
}

// SetStyles configures the toast styles by level.
func (s *SignalToastStack) SetStyles(bg, text, info, success, warn, err backend.Style) {
	if s.stack == nil {
		return
	}
	s.stack.SetStyles(bg, text, info, success, warn, err)
}

// Bind attaches app services and subscriptions.
func (s *SignalToastStack) Bind(services runtime.Services) {
	s.Component.Bind(services)
	s.subscribe()
}

// Unbind releases app services and subscriptions.
func (s *SignalToastStack) Unbind() {
	s.Component.Unbind()
}

func (s *SignalToastStack) subscribe() {
	s.Subs.Clear()
	if s.toasts != nil {
		s.Observe(s.toasts, s.onToastsChanged)
	}
	s.onToastsChanged()
}

func (s *SignalToastStack) onToastsChanged() {
	if s.stack == nil || s.toasts == nil {
		return
	}
	items := s.toasts.Get()
	if len(items) == 0 {
		s.stack.SetToasts(nil)
		return
	}
	cloned := append([]*toast.Toast(nil), items...)
	s.stack.SetToasts(cloned)
	s.Services.Invalidate()
}

// Measure returns the preferred size.
func (s *SignalToastStack) Measure(constraints runtime.Constraints) runtime.Size {
	if s.stack == nil {
		return runtime.Size{}
	}
	return s.stack.Measure(constraints)
}

// Layout positions the toast stack.
func (s *SignalToastStack) Layout(bounds runtime.Rect) {
	s.Base.Layout(bounds)
	if s.stack == nil {
		return
	}
	s.stack.Layout(bounds)
}

// Render draws the toast stack.
func (s *SignalToastStack) Render(ctx runtime.RenderContext) {
	if s.stack == nil {
		return
	}
	s.stack.Render(ctx)
}

// HandleMessage forwards input to the toast stack.
func (s *SignalToastStack) HandleMessage(msg runtime.Message) runtime.HandleResult {
	if s.stack == nil {
		return runtime.Unhandled()
	}
	return s.stack.HandleMessage(msg)
}

// ChildWidgets returns child widgets for traversal.
func (s *SignalToastStack) ChildWidgets() []runtime.Widget {
	if s.stack == nil {
		return nil
	}
	return []runtime.Widget{s.stack}
}

var _ runtime.Widget = (*SignalToastStack)(nil)
var _ runtime.ChildProvider = (*SignalToastStack)(nil)
var _ runtime.Bindable = (*SignalToastStack)(nil)
var _ runtime.Unbindable = (*SignalToastStack)(nil)
