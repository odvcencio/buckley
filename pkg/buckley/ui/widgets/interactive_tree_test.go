package widgets

import (
	"testing"

	"github.com/odvcencio/fluffyui/runtime"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

func TestInteractiveTree_MouseSelectionAndToggle(t *testing.T) {
	root := &uiwidgets.TreeNode{
		Label:    "root",
		Expanded: true,
		Children: []*uiwidgets.TreeNode{
			{Label: "child"},
		},
	}
	tree := NewInteractiveTree(root)
	tree.Layout(runtime.Rect{X: 0, Y: 0, Width: 20, Height: 4})

	content := tree.ContentBounds()
	selectChild := runtime.MouseMsg{
		X:      content.X + 4,
		Y:      content.Y + 1,
		Button: runtime.MouseLeft,
		Action: runtime.MousePress,
	}
	tree.HandleMessage(selectChild)

	if tree.SelectedIndex() != 1 {
		t.Fatalf("expected selected index 1, got %d", tree.SelectedIndex())
	}

	toggleRoot := runtime.MouseMsg{
		X:      content.X,
		Y:      content.Y,
		Button: runtime.MouseLeft,
		Action: runtime.MousePress,
	}
	tree.HandleMessage(toggleRoot)

	if root.Expanded {
		t.Fatal("expected root to be collapsed after toggle")
	}
}
