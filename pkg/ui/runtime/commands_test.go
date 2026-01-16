package runtime

import (
	"testing"
)

// TestCommands verifies all command types implement the Command interface.
func TestCommands_ImplementsInterface(t *testing.T) {
	// This test verifies that all command types compile correctly
	// and implement the Command interface via Command().

	commands := []Command{
		Quit{},
		Refresh{},
		Submit{Text: "test"},
		Cancel{},
		FileSelected{Path: "/test"},
		FocusNext{},
		FocusPrev{},
		PushOverlay{Widget: nil, Modal: false},
		PopOverlay{},
		PaletteSelected{ID: "item1", Data: nil},
	}

	for i, cmd := range commands {
		if cmd == nil {
			t.Errorf("Command %d is nil", i)
		}
	}
}

func TestQuit(t *testing.T) {
	cmd := Quit{}
	// Verify it has the Command method
	cmd.Command()
}

func TestRefresh(t *testing.T) {
	cmd := Refresh{}
	cmd.Command()
}

func TestSubmit(t *testing.T) {
	cmd := Submit{Text: "hello world"}
	cmd.Command()

	if cmd.Text != "hello world" {
		t.Errorf("Submit.Text = %q, want %q", cmd.Text, "hello world")
	}
}

func TestCancel(t *testing.T) {
	cmd := Cancel{}
	cmd.Command()
}

func TestFileSelected(t *testing.T) {
	cmd := FileSelected{Path: "/home/user/file.txt"}
	cmd.Command()

	if cmd.Path != "/home/user/file.txt" {
		t.Errorf("FileSelected.Path = %q, want %q", cmd.Path, "/home/user/file.txt")
	}
}

func TestFocusNext(t *testing.T) {
	cmd := FocusNext{}
	cmd.Command()
}

func TestFocusPrev(t *testing.T) {
	cmd := FocusPrev{}
	cmd.Command()
}

func TestPushOverlay(t *testing.T) {
	w := &testSimpleWidget{}
	cmd := PushOverlay{Widget: w, Modal: true}
	cmd.Command()

	if cmd.Widget != w {
		t.Error("PushOverlay.Widget should be the widget")
	}
	if !cmd.Modal {
		t.Error("PushOverlay.Modal should be true")
	}
}

type testSimpleWidget struct{}

func (t *testSimpleWidget) Measure(c Constraints) Size       { return Size{} }
func (t *testSimpleWidget) Layout(bounds Rect)               {}
func (t *testSimpleWidget) Render(ctx RenderContext)         {}
func (t *testSimpleWidget) HandleMessage(msg Message) HandleResult {
	return Unhandled()
}

func TestPopOverlay(t *testing.T) {
	cmd := PopOverlay{}
	cmd.Command()
}

func TestPaletteSelected(t *testing.T) {
	data := map[string]int{"count": 42}
	cmd := PaletteSelected{ID: "option-1", Data: data}
	cmd.Command()

	if cmd.ID != "option-1" {
		t.Errorf("PaletteSelected.ID = %q, want %q", cmd.ID, "option-1")
	}
	if cmd.Data == nil {
		t.Error("PaletteSelected.Data should not be nil")
	}
}
