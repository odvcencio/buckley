package widgets

import (
	"testing"

	"github.com/odvcencio/fluffyui/runtime"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

func TestInteractivePalette_MouseSelectAndActivate(t *testing.T) {
	palette := NewInteractivePalette("Commands")
	palette.SetItems([]uiwidgets.PaletteItem{
		{ID: "a", Label: "Alpha"},
		{ID: "b", Label: "Beta"},
		{ID: "c", Label: "Gamma"},
	})
	palette.Layout(runtime.Rect{X: 0, Y: 0, Width: 60, Height: 10})

	bounds := palette.Bounds()
	clickX := bounds.X + 3
	clickY := bounds.Y + 4 // second item (items start at Y+3)

	result := palette.HandleMessage(runtime.MouseMsg{
		X:      clickX,
		Y:      clickY,
		Button: runtime.MouseLeft,
		Action: runtime.MousePress,
	})
	if !result.Handled {
		t.Fatal("expected mouse click to be handled")
	}
	if palette.SelectedIndex() != 1 {
		t.Fatalf("expected selected index 1, got %d", palette.SelectedIndex())
	}

	result = palette.HandleMessage(runtime.MouseMsg{
		X:      clickX,
		Y:      clickY,
		Button: runtime.MouseLeft,
		Action: runtime.MousePress,
	})
	if len(result.Commands) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(result.Commands))
	}
	if _, ok := result.Commands[0].(runtime.PaletteSelected); !ok {
		t.Fatalf("expected PaletteSelected, got %T", result.Commands[0])
	}
	if _, ok := result.Commands[1].(runtime.PopOverlay); !ok {
		t.Fatalf("expected PopOverlay, got %T", result.Commands[1])
	}
}

func TestInteractivePalette_MouseWheel(t *testing.T) {
	palette := NewInteractivePalette("Commands")
	palette.SetItems([]uiwidgets.PaletteItem{
		{ID: "a", Label: "Alpha"},
		{ID: "b", Label: "Beta"},
	})
	palette.Layout(runtime.Rect{X: 0, Y: 0, Width: 60, Height: 10})

	bounds := palette.Bounds()
	palette.HandleMessage(runtime.MouseMsg{
		X:      bounds.X + 1,
		Y:      bounds.Y + 3,
		Button: runtime.MouseWheelDown,
	})

	if palette.SelectedIndex() != 1 {
		t.Fatalf("expected selected index 1, got %d", palette.SelectedIndex())
	}
}
