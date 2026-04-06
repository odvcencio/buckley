package widgets

import (
	"testing"

	"github.com/odvcencio/fluffyui/runtime"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

func TestInteractiveTable_MouseSelection(t *testing.T) {
	table := NewInteractiveTable(
		uiwidgets.TableColumn{Title: "Name"},
		uiwidgets.TableColumn{Title: "Status"},
	)
	table.SetRows([][]string{
		{"Alpha", "running"},
		{"Beta", "queued"},
		{"Gamma", "done"},
	})
	table.Layout(runtime.Rect{X: 0, Y: 0, Width: 20, Height: 4})

	content := table.ContentBounds()
	msg := runtime.MouseMsg{
		X:      content.X + 1,
		Y:      content.Y + 2,
		Button: runtime.MouseLeft,
		Action: runtime.MousePress,
	}
	table.HandleMessage(msg)

	if table.SelectedIndex() != 1 {
		t.Fatalf("expected selected index 1, got %d", table.SelectedIndex())
	}
}

func TestInteractiveTable_MouseWheel(t *testing.T) {
	table := NewInteractiveTable(uiwidgets.TableColumn{Title: "Name"})
	table.SetRows([][]string{
		{"Alpha"},
		{"Beta"},
		{"Gamma"},
	})
	table.Layout(runtime.Rect{X: 0, Y: 0, Width: 10, Height: 3})

	table.HandleMessage(runtime.MouseMsg{Button: runtime.MouseWheelDown})

	if table.SelectedIndex() != 1 {
		t.Fatalf("expected selected index 1 after wheel, got %d", table.SelectedIndex())
	}
}
