package widgets

import (
	"testing"

	"github.com/odvcencio/fluffyui/runtime"
)

func TestChatMessages_MouseScrollbarDrag(t *testing.T) {
	m := NewChatMessages()
	m.SetMessageMetadataMode("never")
	for i := 0; i < 40; i++ {
		m.AddMessage("line", "assistant")
	}
	m.Layout(runtime.Rect{X: 0, Y: 0, Width: 30, Height: 5})

	top, total, viewH := m.ScrollPosition()
	if total <= viewH {
		t.Fatalf("expected scrollable buffer, total=%d view=%d", total, viewH)
	}
	if top == 0 {
		t.Fatalf("expected to start scrolled, top=%d", top)
	}

	x := m.listBounds.X + m.listBounds.Width
	y := m.listBounds.Y
	m.HandleMessage(runtime.MouseMsg{X: x, Y: y, Button: runtime.MouseLeft, Action: runtime.MousePress})

	topAfter, _, _ := m.ScrollPosition()
	if topAfter != 0 {
		t.Fatalf("expected scroll to top, got %d", topAfter)
	}
}
