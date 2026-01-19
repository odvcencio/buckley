package tui

import (
	"testing"
	"time"

	"github.com/odvcencio/fluffy-ui/progress"
	"github.com/odvcencio/fluffy-ui/toast"
)

func TestMessages_ImplementInterface(t *testing.T) {
	// Verify all message types implement Message interface
	messages := []Message{
		KeyMsg{Key: 1, Rune: 'a'},
		ResizeMsg{Width: 80, Height: 24},
		StreamChunk{SessionID: "s1", Text: "hello"},
		StreamFlush{SessionID: "s1", Text: "hello"},
		StreamDone{SessionID: "s1", FullText: "hello world"},
		ToolStart{ToolID: "t1", ToolName: "test"},
		ToolResult{ToolID: "t1", Result: "ok"},
		TickMsg{Time: time.Now()},
		QuitMsg{},
		RefreshMsg{},
		StatusMsg{Text: "Ready"},
		StatusOverrideMsg{Text: "Busy", Duration: 2 * time.Second},
		TokensMsg{Tokens: 100, CostCent: 0.01},
		ContextMsg{Used: 1000, Budget: 8000, Window: 8192},
		ExecutionModeMsg{Mode: "classic"},
		ProgressMsg{Items: []progress.Progress{{ID: "p1"}}},
		ToastsMsg{Toasts: []*toast.Toast{{ID: "t1"}}},
		StreamingMsg{Active: true},
		ModelMsg{Name: "gpt-4"},
		SessionMsg{ID: "session-1"},
		AddMessageMsg{Content: "hi", Source: "user"},
		AppendMsg{Text: " world"},
		ThinkingMsg{Show: true},
		ModeChangeMsg{Mode: "normal"},
		OverlayMsg{Show: true, Name: "file_picker"},
		SubmitMsg{Text: "hello"},
		ApprovalRequestMsg{ID: "req-1", Tool: "run_shell"},
		MouseMsg{X: 10, Y: 5, Button: MouseLeft, Action: MousePress},
		PasteMsg{Text: "pasted text"},
	}

	for _, m := range messages {
		// This will fail to compile if any message doesn't implement the interface
		_ = m
	}
}

// TestMessages_IsMessageMethods directly calls the isMessage() method on each type
// to ensure coverage of these marker methods.
func TestMessages_IsMessageMethods(t *testing.T) {
	// Call isMessage() on each message type to trigger coverage
	KeyMsg{}.isMessage()
	ResizeMsg{}.isMessage()
	PasteMsg{}.isMessage()
	StreamChunk{}.isMessage()
	StreamDone{}.isMessage()
	StreamFlush{}.isMessage()
	ToolStart{}.isMessage()
	ToolResult{}.isMessage()
	TickMsg{}.isMessage()
	QuitMsg{}.isMessage()
	RefreshMsg{}.isMessage()
	StatusMsg{}.isMessage()
	StatusOverrideMsg{}.isMessage()
	TokensMsg{}.isMessage()
	ContextMsg{}.isMessage()
	ExecutionModeMsg{}.isMessage()
	ProgressMsg{}.isMessage()
	ToastsMsg{}.isMessage()
	StreamingMsg{}.isMessage()
	ModelMsg{}.isMessage()
	SessionMsg{}.isMessage()
	AddMessageMsg{}.isMessage()
	AppendMsg{}.isMessage()
	ThinkingMsg{}.isMessage()
	ModeChangeMsg{}.isMessage()
	OverlayMsg{}.isMessage()
	SubmitMsg{}.isMessage()
	MouseMsg{}.isMessage()
	ApprovalRequestMsg{}.isMessage()
}

func TestKeyMsg(t *testing.T) {
	msg := KeyMsg{
		Key:   65, // 'A'
		Rune:  'A',
		Alt:   true,
		Ctrl:  false,
		Shift: true,
	}

	if msg.Key != 65 {
		t.Errorf("expected Key=65, got %d", msg.Key)
	}
	if msg.Rune != 'A' {
		t.Errorf("expected Rune='A', got %c", msg.Rune)
	}
	if !msg.Alt {
		t.Error("expected Alt=true")
	}
	if msg.Ctrl {
		t.Error("expected Ctrl=false")
	}
	if !msg.Shift {
		t.Error("expected Shift=true")
	}
}

func TestStreamMessages(t *testing.T) {
	chunk := StreamChunk{SessionID: "session-1", Text: "hello"}
	if chunk.SessionID != "session-1" {
		t.Errorf("expected SessionID='session-1', got %s", chunk.SessionID)
	}
	if chunk.Text != "hello" {
		t.Errorf("expected Text='hello', got %s", chunk.Text)
	}

	flush := StreamFlush{SessionID: "session-1", Text: "hello world"}
	if flush.Text != "hello world" {
		t.Errorf("expected Text='hello world', got %s", flush.Text)
	}

	done := StreamDone{SessionID: "session-1", FullText: "complete message"}
	if done.FullText != "complete message" {
		t.Errorf("expected FullText='complete message', got %s", done.FullText)
	}
}

func TestToolMessages(t *testing.T) {
	start := ToolStart{
		ToolID:   "tool-1",
		ToolName: "read_file",
		Args:     map[string]any{"path": "/tmp/test.txt"},
	}
	if start.ToolName != "read_file" {
		t.Errorf("expected ToolName='read_file', got %s", start.ToolName)
	}
	if start.Args["path"] != "/tmp/test.txt" {
		t.Errorf("unexpected Args: %v", start.Args)
	}

	result := ToolResult{
		ToolID: "tool-1",
		Result: "file contents",
		Err:    nil,
	}
	if result.Result != "file contents" {
		t.Errorf("unexpected Result: %v", result.Result)
	}
	if result.Err != nil {
		t.Errorf("expected no error, got %v", result.Err)
	}
}

func TestUIMessages(t *testing.T) {
	status := StatusMsg{Text: "Ready"}
	if status.Text != "Ready" {
		t.Errorf("expected Text='Ready', got %s", status.Text)
	}

	tokens := TokensMsg{Tokens: 1000, CostCent: 0.05}
	if tokens.Tokens != 1000 {
		t.Errorf("expected Tokens=1000, got %d", tokens.Tokens)
	}
	if tokens.CostCent != 0.05 {
		t.Errorf("expected CostCent=0.05, got %f", tokens.CostCent)
	}

	addMsg := AddMessageMsg{Content: "Hello!", Source: "user"}
	if addMsg.Content != "Hello!" || addMsg.Source != "user" {
		t.Errorf("unexpected AddMessageMsg: %+v", addMsg)
	}

	appendMsg := AppendMsg{Text: " more text"}
	if appendMsg.Text != " more text" {
		t.Errorf("expected Text=' more text', got %s", appendMsg.Text)
	}

	thinking := ThinkingMsg{Show: true}
	if !thinking.Show {
		t.Error("expected Show=true")
	}
}

func TestResizeMsg(t *testing.T) {
	msg := ResizeMsg{Width: 120, Height: 40}
	if msg.Width != 120 || msg.Height != 40 {
		t.Errorf("expected 120x40, got %dx%d", msg.Width, msg.Height)
	}
}

func TestModeMessages(t *testing.T) {
	mode := ModeChangeMsg{Mode: "shell"}
	if mode.Mode != "shell" {
		t.Errorf("expected Mode='shell', got %s", mode.Mode)
	}

	overlay := OverlayMsg{Show: true, Name: "command_palette"}
	if !overlay.Show || overlay.Name != "command_palette" {
		t.Errorf("unexpected OverlayMsg: %+v", overlay)
	}

	submit := SubmitMsg{Text: "user input"}
	if submit.Text != "user input" {
		t.Errorf("expected Text='user input', got %s", submit.Text)
	}
}

func TestApprovalMessages(t *testing.T) {
	// Test basic approval request
	req := ApprovalRequestMsg{
		ID:          "req-123",
		Tool:        "run_shell",
		Operation:   "shell:write",
		Description: "Execute a potentially dangerous command",
		Command:     "rm -rf node_modules",
	}

	if req.ID != "req-123" {
		t.Errorf("expected ID='req-123', got %s", req.ID)
	}
	if req.Tool != "run_shell" {
		t.Errorf("expected Tool='run_shell', got %s", req.Tool)
	}
	if req.Operation != "shell:write" {
		t.Errorf("expected Operation='shell:write', got %s", req.Operation)
	}
	if req.Command != "rm -rf node_modules" {
		t.Errorf("expected Command='rm -rf node_modules', got %s", req.Command)
	}

	// Test file edit approval with diff
	fileReq := ApprovalRequestMsg{
		ID:        "req-456",
		Tool:      "write_file",
		Operation: "write",
		FilePath:  "pkg/api/server.go",
		DiffLines: []DiffLine{
			{Type: DiffRemove, Content: "old code"},
			{Type: DiffAdd, Content: "new code"},
			{Type: DiffContext, Content: "unchanged"},
		},
		AddedLines:   1,
		RemovedLines: 1,
	}

	if fileReq.FilePath != "pkg/api/server.go" {
		t.Errorf("expected FilePath='pkg/api/server.go', got %s", fileReq.FilePath)
	}
	if len(fileReq.DiffLines) != 3 {
		t.Errorf("expected 3 diff lines, got %d", len(fileReq.DiffLines))
	}
	if fileReq.DiffLines[0].Type != DiffRemove {
		t.Errorf("expected first line to be Remove, got %d", fileReq.DiffLines[0].Type)
	}
	if fileReq.DiffLines[1].Type != DiffAdd {
		t.Errorf("expected second line to be Add, got %d", fileReq.DiffLines[1].Type)
	}
	if fileReq.DiffLines[2].Type != DiffContext {
		t.Errorf("expected third line to be Context, got %d", fileReq.DiffLines[2].Type)
	}
}

func TestDiffLineTypes(t *testing.T) {
	// Verify diff line type constants
	if DiffContext != 0 {
		t.Errorf("expected DiffContext=0, got %d", DiffContext)
	}
	if DiffAdd != 1 {
		t.Errorf("expected DiffAdd=1, got %d", DiffAdd)
	}
	if DiffRemove != 2 {
		t.Errorf("expected DiffRemove=2, got %d", DiffRemove)
	}
}

func TestMouseMessages(t *testing.T) {
	msg := MouseMsg{
		X:      100,
		Y:      50,
		Button: MouseWheelUp,
		Action: MousePress,
	}

	if msg.X != 100 {
		t.Errorf("expected X=100, got %d", msg.X)
	}
	if msg.Y != 50 {
		t.Errorf("expected Y=50, got %d", msg.Y)
	}
	if msg.Button != MouseWheelUp {
		t.Errorf("expected MouseWheelUp, got %d", msg.Button)
	}
	if msg.Action != MousePress {
		t.Errorf("expected MousePress, got %d", msg.Action)
	}
}

func TestMouseButtonConstants(t *testing.T) {
	// Verify mouse button constants
	if MouseNone != 0 {
		t.Errorf("expected MouseNone=0, got %d", MouseNone)
	}
	if MouseLeft != 1 {
		t.Errorf("expected MouseLeft=1, got %d", MouseLeft)
	}
	if MouseWheelUp != 4 {
		t.Errorf("expected MouseWheelUp=4, got %d", MouseWheelUp)
	}
	if MouseWheelDown != 5 {
		t.Errorf("expected MouseWheelDown=5, got %d", MouseWheelDown)
	}
}

func TestMouseActionConstants(t *testing.T) {
	// Verify mouse action constants
	if MousePress != 0 {
		t.Errorf("expected MousePress=0, got %d", MousePress)
	}
	if MouseRelease != 1 {
		t.Errorf("expected MouseRelease=1, got %d", MouseRelease)
	}
	if MouseMove != 2 {
		t.Errorf("expected MouseMove=2, got %d", MouseMove)
	}
}

func TestPasteMsg(t *testing.T) {
	msg := PasteMsg{Text: "pasted content here"}
	if msg.Text != "pasted content here" {
		t.Errorf("expected Text='pasted content here', got '%s'", msg.Text)
	}
}

func TestToolResultWithError(t *testing.T) {
	err := &testError{msg: "tool failed"}
	result := ToolResult{
		ToolID: "tool-1",
		Result: nil,
		Err:    err,
	}

	if result.ToolID != "tool-1" {
		t.Errorf("expected ToolID='tool-1', got %s", result.ToolID)
	}
	if result.Result != nil {
		t.Errorf("expected Result=nil, got %v", result.Result)
	}
	if result.Err == nil {
		t.Error("expected Err to be non-nil")
	}
	if result.Err.Error() != "tool failed" {
		t.Errorf("expected error message 'tool failed', got %s", result.Err.Error())
	}
}

// testError is a simple error implementation for testing
type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}

func TestMouseMiddleAndRight(t *testing.T) {
	// Verify MouseMiddle and MouseRight constants
	if MouseMiddle != 2 {
		t.Errorf("expected MouseMiddle=2, got %d", MouseMiddle)
	}
	if MouseRight != 3 {
		t.Errorf("expected MouseRight=3, got %d", MouseRight)
	}
}

func TestModelMsg(t *testing.T) {
	msg := ModelMsg{Name: "claude-opus-4-5-20251101"}
	if msg.Name != "claude-opus-4-5-20251101" {
		t.Errorf("expected Name='claude-opus-4-5-20251101', got '%s'", msg.Name)
	}
}

func TestTickMsg(t *testing.T) {
	now := time.Now()
	msg := TickMsg{Time: now}
	if msg.Time != now {
		t.Errorf("expected Time=%v, got %v", now, msg.Time)
	}
}

func TestOverlayMsgHide(t *testing.T) {
	msg := OverlayMsg{Show: false, Name: "file_picker"}
	if msg.Show {
		t.Error("expected Show=false")
	}
	if msg.Name != "file_picker" {
		t.Errorf("expected Name='file_picker', got '%s'", msg.Name)
	}
}

func TestAddMessageMsgSources(t *testing.T) {
	sources := []string{"user", "assistant", "system", "tool", "thinking"}
	for _, source := range sources {
		msg := AddMessageMsg{Content: "test", Source: source}
		if msg.Source != source {
			t.Errorf("expected Source='%s', got '%s'", source, msg.Source)
		}
	}
}

func TestModeChangeMsgModes(t *testing.T) {
	modes := []string{"normal", "shell", "env", "search", "file"}
	for _, mode := range modes {
		msg := ModeChangeMsg{Mode: mode}
		if msg.Mode != mode {
			t.Errorf("expected Mode='%s', got '%s'", mode, msg.Mode)
		}
	}
}
