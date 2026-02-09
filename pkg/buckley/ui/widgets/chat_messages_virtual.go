package widgets

import (
	"github.com/odvcencio/buckley/pkg/buckley/ui/scrollback"
	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/scroll"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

type chatMessagesVirtual struct {
	uiwidgets.Base
	owner *ChatMessages
}

func newChatMessagesVirtual(owner *ChatMessages) *chatMessagesVirtual {
	v := &chatMessagesVirtual{owner: owner}
	v.Base.Role = accessibility.RolePresentation
	return v
}

func (v *chatMessagesVirtual) Measure(constraints runtime.Constraints) runtime.Size {
	// Return minimal size - the ScrollView will expand this as needed
	return constraints.Constrain(runtime.Size{Width: 1, Height: 1})
}

func (v *chatMessagesVirtual) Layout(bounds runtime.Rect) {
	v.Base.Layout(bounds)
}

func (v *chatMessagesVirtual) Render(ctx runtime.RenderContext) {
}

func (v *chatMessagesVirtual) HandleMessage(msg runtime.Message) runtime.HandleResult {
	return runtime.Unhandled()
}

func (v *chatMessagesVirtual) ItemCount() int {
	if v == nil || v.owner == nil || v.owner.buffer == nil {
		return 0
	}
	return v.owner.buffer.RowCount()
}

func (v *chatMessagesVirtual) ItemHeight(index int) int {
	if v == nil {
		return 0
	}
	return 1
}

func (v *chatMessagesVirtual) RenderItem(index int, ctx runtime.RenderContext) {
	if v == nil || v.owner == nil || v.owner.buffer == nil {
		return
	}
	line, ok := v.owner.buffer.VisibleLineAtRow(index)
	if !ok {
		return
	}
	v.owner.renderVisibleLine(ctx, line)
}

func (v *chatMessagesVirtual) ItemAt(index int) any {
	if v == nil || v.owner == nil || v.owner.buffer == nil {
		return scrollback.VisibleLine{}
	}
	line, _ := v.owner.buffer.VisibleLineAtRow(index)
	return line
}

func (v *chatMessagesVirtual) TotalHeight() int {
	if v == nil || v.owner == nil || v.owner.buffer == nil {
		return 0
	}
	return v.owner.buffer.RowCount()
}

func (v *chatMessagesVirtual) IndexForOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	count := v.ItemCount()
	if count <= 0 {
		return 0
	}
	if offset >= count {
		return count - 1
	}
	return offset
}

func (v *chatMessagesVirtual) OffsetForIndex(index int) int {
	if index < 0 {
		return 0
	}
	count := v.ItemCount()
	if count <= 0 {
		return 0
	}
	if index >= count {
		return count - 1
	}
	return index
}

var _ runtime.Widget = (*chatMessagesVirtual)(nil)
var _ scroll.VirtualContent = (*chatMessagesVirtual)(nil)
var _ scroll.VirtualSizer = (*chatMessagesVirtual)(nil)
var _ scroll.VirtualIndexer = (*chatMessagesVirtual)(nil)
