package widgets

import (
	"testing"

	"github.com/odvcencio/fluffyui/runtime"
)

func TestInteractiveSearch_MouseWheel(t *testing.T) {
	search := NewInteractiveSearch()
	nextCount := 0
	prevCount := 0
	search.SetOnNavigate(func() { nextCount++ }, func() { prevCount++ })
	search.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 10})

	search.HandleMessage(runtime.MouseMsg{
		X:      1,
		Y:      9,
		Button: runtime.MouseWheelDown,
	})
	search.HandleMessage(runtime.MouseMsg{
		X:      1,
		Y:      9,
		Button: runtime.MouseWheelUp,
	})

	if nextCount != 1 {
		t.Fatalf("expected nextCount 1, got %d", nextCount)
	}
	if prevCount != 1 {
		t.Fatalf("expected prevCount 1, got %d", prevCount)
	}
}

func TestInteractiveSearch_MouseFocus(t *testing.T) {
	search := NewInteractiveSearch()
	search.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 10})

	result := search.HandleMessage(runtime.MouseMsg{
		X:      1,
		Y:      9,
		Button: runtime.MouseLeft,
		Action: runtime.MousePress,
	})
	if !result.Handled {
		t.Fatal("expected mouse click to be handled")
	}
	if !search.IsFocused() {
		t.Fatal("expected search to be focused after click")
	}
}
