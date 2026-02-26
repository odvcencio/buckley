package widgets

import (
	"github.com/odvcencio/fluffyui/runtime"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

type chatMessagesOverlay struct {
	uiwidgets.Base
	owner *ChatMessages
}

func (o *chatMessagesOverlay) Measure(constraints runtime.Constraints) runtime.Size {
	return runtime.Size{Width: constraints.MaxWidth, Height: constraints.MaxHeight}
}

func (o *chatMessagesOverlay) Layout(bounds runtime.Rect) {
	o.Base.Layout(bounds)
}

func (o *chatMessagesOverlay) Render(ctx runtime.RenderContext) {
	if o == nil || o.owner == nil {
		return
	}
	o.owner.renderMetadataOverlay(ctx)
}

func (o *chatMessagesOverlay) HandleMessage(msg runtime.Message) runtime.HandleResult {
	return runtime.Unhandled()
}
