package widgets

import (
	"testing"

	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/terminal"
)

func TestSidebar_SetSessionNodes_PreservesSelectionByID(t *testing.T) {
	s := NewSidebar()
	s.SetSessionNodes([]SessionNavNode{
		{ID: "run-1", Label: "root run"},
		{ID: "run-2", Label: "child run", Depth: 1},
	})
	s.sessionSelected = 1 // simulate a prior click on run-2

	s.SetSessionNodes([]SessionNavNode{
		{ID: "run-1", Label: "root run"},
		{ID: "run-2", Label: "child run (updated)", Depth: 1},
		{ID: "run-3", Label: "another child", Depth: 1},
	})

	node, ok := s.SelectedSessionNode()
	if !ok {
		t.Fatal("expected a selection to survive SetSessionNodes")
	}
	if node.ID != "run-2" {
		t.Fatalf("selected ID = %q, want run-2", node.ID)
	}
}

func TestSidebar_SetSessionNodes_DropsSelectionWhenMissing(t *testing.T) {
	s := NewSidebar()
	s.SetSessionNodes([]SessionNavNode{{ID: "run-1"}})
	s.sessionSelected = 0

	s.SetSessionNodes([]SessionNavNode{{ID: "run-2"}})

	if _, ok := s.SelectedSessionNode(); ok {
		t.Fatal("expected no selection once the previously selected ID is gone")
	}
}

func TestSidebar_HasSessionsSection(t *testing.T) {
	s := NewSidebar()
	if hasSessionsSection(s) {
		t.Fatal("expected no sessions section with no nodes")
	}
	s.SetSessionNodes([]SessionNavNode{{ID: "session-1", Label: "session-1"}})
	if !hasSessionsSection(s) {
		t.Fatal("expected a sessions section once nodes are set")
	}
}

func TestSessionNavLabel_IndentsAndMarksActive(t *testing.T) {
	root := SessionNavNode{ID: "r", Label: "root", Depth: 0, Active: true}
	if got := sessionNavLabel(root); got != "root *" {
		t.Fatalf("root label = %q, want %q", got, "root *")
	}

	child := SessionNavNode{ID: "c", Label: "child", Depth: 1, Status: "running"}
	got := sessionNavLabel(child)
	if got[:2] != "  " {
		t.Fatalf("child label = %q, want a two-space indent prefix", got)
	}
}

func TestSidebar_Render_ShowsSessionsFlatList(t *testing.T) {
	s := NewSidebar()
	s.SetSessionNodes([]SessionNavNode{
		{ID: "session-1", Label: "session-1", Active: true},
		{ID: "session-2", Label: "session-2"},
	})
	s.Layout(runtime.Rect{X: 0, Y: 0, Width: 30, Height: 20})

	buf := runtime.NewBuffer(30, 20)
	ctx := runtime.RenderContext{Buffer: buf}
	s.Render(ctx)

	if s.sessionsRowCount != 2 {
		t.Fatalf("sessionsRowCount = %d, want 2", s.sessionsRowCount)
	}
}

func TestSidebar_HandleMessage_ClickSelectsSessionNode(t *testing.T) {
	s := NewSidebar()
	s.SetSessionNodes([]SessionNavNode{
		{ID: "session-1", Label: "session-1"},
		{ID: "session-2", Label: "session-2"},
	})
	s.Layout(runtime.Rect{X: 0, Y: 0, Width: 30, Height: 20})
	buf := runtime.NewBuffer(30, 20)
	s.Render(runtime.RenderContext{Buffer: buf})

	var selected SessionNavNode
	var calls int
	s.SetOnSessionSelect(func(node SessionNavNode) {
		selected = node
		calls++
	})

	result := s.HandleMessage(runtime.MouseMsg{
		X:      2,
		Y:      s.sessionsRowsY + 1, // second row
		Button: runtime.MouseLeft,
		Action: runtime.MousePress,
	})
	if !result.Handled {
		t.Fatal("expected the click to be handled")
	}
	if calls != 1 {
		t.Fatalf("onSessionSelect calls = %d, want 1", calls)
	}
	if selected.ID != "session-2" {
		t.Fatalf("selected.ID = %q, want session-2", selected.ID)
	}

	node, ok := s.SelectedSessionNode()
	if !ok || node.ID != "session-2" {
		t.Fatalf("SelectedSessionNode() = (%+v, %v), want session-2", node, ok)
	}
}

func TestSidebar_HandleMessage_ClickOutsideSessionsIgnored(t *testing.T) {
	s := NewSidebar()
	s.SetSessionNodes([]SessionNavNode{{ID: "session-1", Label: "session-1"}})
	s.Layout(runtime.Rect{X: 0, Y: 0, Width: 30, Height: 20})
	buf := runtime.NewBuffer(30, 20)
	s.Render(runtime.RenderContext{Buffer: buf})

	calls := 0
	s.SetOnSessionSelect(func(SessionNavNode) { calls++ })

	result := s.HandleMessage(runtime.MouseMsg{
		X:      2,
		Y:      s.sessionsRowsY + 50, // well past the rendered rows
		Button: runtime.MouseLeft,
		Action: runtime.MousePress,
	})
	if result.Handled {
		t.Fatal("expected clicks outside the rendered rows to be unhandled")
	}
	if calls != 0 {
		t.Fatalf("onSessionSelect calls = %d, want 0", calls)
	}
}

func TestSidebar_HandleKey_ToggleSessionsSection(t *testing.T) {
	s := NewSidebar()
	if !s.showSessions {
		t.Fatal("showSessions should default to true")
	}

	result := s.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: '8'})
	if !result.Handled {
		t.Fatal("expected '8' to be handled")
	}
	if s.showSessions {
		t.Fatal("expected showSessions to toggle off")
	}
}
