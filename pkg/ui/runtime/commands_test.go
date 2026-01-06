package runtime

import (
	"testing"
)

// TestCommands verifies all command types implement the Command interface.
func TestCommands_ImplementsInterface(t *testing.T) {
	// This test verifies that all command types compile correctly
	// and implement the Command interface via isCommand().

	commands := []Command{
		Quit{},
		Refresh{},
		Submit{Text: "test"},
		Cancel{},
		FileSelected{Path: "/test"},
		ShellCommand{Command: "echo test"},
		EnvQuery{Name: "PATH"},
		FocusNext{},
		FocusPrev{},
		PushOverlay{Widget: nil, Modal: false},
		PopOverlay{},
		ScrollUp{Lines: 5},
		ScrollDown{Lines: 10},
		PageUp{},
		PageDown{},
		ShowThinking{},
		HideThinking{},
		UpdateStatus{Text: "status"},
		UpdateTokens{Tokens: 1000, CostCent: 1.5},
		UpdateModel{Name: "gpt-4"},
		NextSession{},
		PrevSession{},
		ApprovalResponse{RequestID: "req1", Approved: true, AlwaysAllow: false},
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
	// Verify it has the isCommand method
	cmd.isCommand()
}

func TestRefresh(t *testing.T) {
	cmd := Refresh{}
	cmd.isCommand()
}

func TestSubmit(t *testing.T) {
	cmd := Submit{Text: "hello world"}
	cmd.isCommand()

	if cmd.Text != "hello world" {
		t.Errorf("Submit.Text = %q, want %q", cmd.Text, "hello world")
	}
}

func TestCancel(t *testing.T) {
	cmd := Cancel{}
	cmd.isCommand()
}

func TestFileSelected(t *testing.T) {
	cmd := FileSelected{Path: "/home/user/file.txt"}
	cmd.isCommand()

	if cmd.Path != "/home/user/file.txt" {
		t.Errorf("FileSelected.Path = %q, want %q", cmd.Path, "/home/user/file.txt")
	}
}

func TestShellCommand(t *testing.T) {
	cmd := ShellCommand{Command: "ls -la"}
	cmd.isCommand()

	if cmd.Command != "ls -la" {
		t.Errorf("ShellCommand.Command = %q, want %q", cmd.Command, "ls -la")
	}
}

func TestEnvQuery(t *testing.T) {
	cmd := EnvQuery{Name: "HOME"}
	cmd.isCommand()

	if cmd.Name != "HOME" {
		t.Errorf("EnvQuery.Name = %q, want %q", cmd.Name, "HOME")
	}
}

func TestFocusNext(t *testing.T) {
	cmd := FocusNext{}
	cmd.isCommand()
}

func TestFocusPrev(t *testing.T) {
	cmd := FocusPrev{}
	cmd.isCommand()
}

func TestPushOverlay(t *testing.T) {
	w := &testSimpleWidget{}
	cmd := PushOverlay{Widget: w, Modal: true}
	cmd.isCommand()

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
	cmd.isCommand()
}

func TestScrollUp(t *testing.T) {
	cmd := ScrollUp{Lines: 5}
	cmd.isCommand()

	if cmd.Lines != 5 {
		t.Errorf("ScrollUp.Lines = %d, want 5", cmd.Lines)
	}
}

func TestScrollDown(t *testing.T) {
	cmd := ScrollDown{Lines: 10}
	cmd.isCommand()

	if cmd.Lines != 10 {
		t.Errorf("ScrollDown.Lines = %d, want 10", cmd.Lines)
	}
}

func TestPageUp(t *testing.T) {
	cmd := PageUp{}
	cmd.isCommand()
}

func TestPageDown(t *testing.T) {
	cmd := PageDown{}
	cmd.isCommand()
}

func TestShowThinking(t *testing.T) {
	cmd := ShowThinking{}
	cmd.isCommand()
}

func TestHideThinking(t *testing.T) {
	cmd := HideThinking{}
	cmd.isCommand()
}

func TestUpdateStatus(t *testing.T) {
	cmd := UpdateStatus{Text: "Processing..."}
	cmd.isCommand()

	if cmd.Text != "Processing..." {
		t.Errorf("UpdateStatus.Text = %q, want %q", cmd.Text, "Processing...")
	}
}

func TestUpdateTokens(t *testing.T) {
	cmd := UpdateTokens{Tokens: 2500, CostCent: 3.75}
	cmd.isCommand()

	if cmd.Tokens != 2500 {
		t.Errorf("UpdateTokens.Tokens = %d, want 2500", cmd.Tokens)
	}
	if cmd.CostCent != 3.75 {
		t.Errorf("UpdateTokens.CostCent = %f, want 3.75", cmd.CostCent)
	}
}

func TestUpdateModel(t *testing.T) {
	cmd := UpdateModel{Name: "claude-3-opus"}
	cmd.isCommand()

	if cmd.Name != "claude-3-opus" {
		t.Errorf("UpdateModel.Name = %q, want %q", cmd.Name, "claude-3-opus")
	}
}

func TestNextSession(t *testing.T) {
	cmd := NextSession{}
	cmd.isCommand()
}

func TestPrevSession(t *testing.T) {
	cmd := PrevSession{}
	cmd.isCommand()
}

func TestApprovalResponse(t *testing.T) {
	cmd := ApprovalResponse{
		RequestID:   "req-123",
		Approved:    true,
		AlwaysAllow: false,
	}
	cmd.isCommand()

	if cmd.RequestID != "req-123" {
		t.Errorf("ApprovalResponse.RequestID = %q, want %q", cmd.RequestID, "req-123")
	}
	if !cmd.Approved {
		t.Error("ApprovalResponse.Approved should be true")
	}
	if cmd.AlwaysAllow {
		t.Error("ApprovalResponse.AlwaysAllow should be false")
	}
}

func TestPaletteSelected(t *testing.T) {
	data := map[string]int{"count": 42}
	cmd := PaletteSelected{ID: "option-1", Data: data}
	cmd.isCommand()

	if cmd.ID != "option-1" {
		t.Errorf("PaletteSelected.ID = %q, want %q", cmd.ID, "option-1")
	}
	if cmd.Data == nil {
		t.Error("PaletteSelected.Data should not be nil")
	}
}
