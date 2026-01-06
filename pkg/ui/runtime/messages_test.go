package runtime

import (
	"testing"
	"time"

	"github.com/odvcencio/buckley/pkg/ui/terminal"
)

// TestMessages verifies all message types implement the Message interface.
func TestMessages_ImplementsInterface(t *testing.T) {
	messages := []Message{
		KeyMsg{Key: terminal.KeyEnter},
		ResizeMsg{Width: 80, Height: 24},
		MouseMsg{X: 10, Y: 20, Button: MouseLeft, Action: MousePress},
		PasteMsg{Text: "pasted text"},
		TickMsg{Time: time.Now()},
		StreamChunk{SessionID: "sess1", Text: "chunk"},
		StreamFlush{SessionID: "sess1", Text: "flush"},
		StreamDone{SessionID: "sess1", FullText: "done"},
		ToolStart{ToolID: "t1", ToolName: "read_file"},
		ToolResult{ToolID: "t1", Result: "result"},
		AddMessageMsg{Content: "content", Source: "user"},
		AppendTextMsg{Text: "append"},
		StatusMsg{Text: "status"},
		TokensMsg{Tokens: 100, CostCent: 0.5},
		ModelMsg{Name: "gpt-4"},
		ThinkingMsg{Show: true},
		RefreshMsg{},
		QuitMsg{},
	}

	for i, msg := range messages {
		if msg == nil {
			t.Errorf("Message %d is nil", i)
		}
	}
}

func TestKeyMsg(t *testing.T) {
	msg := KeyMsg{
		Key:   terminal.KeyRune,
		Rune:  'a',
		Alt:   true,
		Ctrl:  false,
		Shift: true,
	}
	msg.isMessage()

	if msg.Key != terminal.KeyRune {
		t.Errorf("KeyMsg.Key = %v, want KeyRune", msg.Key)
	}
	if msg.Rune != 'a' {
		t.Errorf("KeyMsg.Rune = %c, want 'a'", msg.Rune)
	}
	if !msg.Alt {
		t.Error("KeyMsg.Alt should be true")
	}
	if msg.Ctrl {
		t.Error("KeyMsg.Ctrl should be false")
	}
	if !msg.Shift {
		t.Error("KeyMsg.Shift should be true")
	}
}

func TestResizeMsg(t *testing.T) {
	msg := ResizeMsg{Width: 120, Height: 40}
	msg.isMessage()

	if msg.Width != 120 {
		t.Errorf("ResizeMsg.Width = %d, want 120", msg.Width)
	}
	if msg.Height != 40 {
		t.Errorf("ResizeMsg.Height = %d, want 40", msg.Height)
	}
}

func TestMouseMsg(t *testing.T) {
	msg := MouseMsg{
		X:      50,
		Y:      25,
		Button: MouseRight,
		Action: MouseRelease,
	}
	msg.isMessage()

	if msg.X != 50 {
		t.Errorf("MouseMsg.X = %d, want 50", msg.X)
	}
	if msg.Y != 25 {
		t.Errorf("MouseMsg.Y = %d, want 25", msg.Y)
	}
	if msg.Button != MouseRight {
		t.Errorf("MouseMsg.Button = %v, want MouseRight", msg.Button)
	}
	if msg.Action != MouseRelease {
		t.Errorf("MouseMsg.Action = %v, want MouseRelease", msg.Action)
	}
}

func TestMouseButtons(t *testing.T) {
	buttons := []MouseButton{
		MouseNone,
		MouseLeft,
		MouseMiddle,
		MouseRight,
		MouseWheelUp,
		MouseWheelDown,
	}

	// Verify they are distinct values
	seen := make(map[MouseButton]bool)
	for _, b := range buttons {
		if seen[b] {
			t.Errorf("Duplicate MouseButton value: %d", b)
		}
		seen[b] = true
	}
}

func TestMouseActions(t *testing.T) {
	actions := []MouseAction{
		MousePress,
		MouseRelease,
		MouseMove,
	}

	// Verify they are distinct values
	seen := make(map[MouseAction]bool)
	for _, a := range actions {
		if seen[a] {
			t.Errorf("Duplicate MouseAction value: %d", a)
		}
		seen[a] = true
	}
}

func TestPasteMsg(t *testing.T) {
	msg := PasteMsg{Text: "Hello, World!"}
	msg.isMessage()

	if msg.Text != "Hello, World!" {
		t.Errorf("PasteMsg.Text = %q, want %q", msg.Text, "Hello, World!")
	}
}

func TestTickMsg(t *testing.T) {
	now := time.Now()
	msg := TickMsg{Time: now}
	msg.isMessage()

	if msg.Time != now {
		t.Errorf("TickMsg.Time = %v, want %v", msg.Time, now)
	}
}

func TestStreamChunk(t *testing.T) {
	msg := StreamChunk{SessionID: "session-123", Text: "chunk content"}
	msg.isMessage()

	if msg.SessionID != "session-123" {
		t.Errorf("StreamChunk.SessionID = %q, want %q", msg.SessionID, "session-123")
	}
	if msg.Text != "chunk content" {
		t.Errorf("StreamChunk.Text = %q, want %q", msg.Text, "chunk content")
	}
}

func TestStreamFlush(t *testing.T) {
	msg := StreamFlush{SessionID: "session-456", Text: "flushed content"}
	msg.isMessage()

	if msg.SessionID != "session-456" {
		t.Errorf("StreamFlush.SessionID = %q, want %q", msg.SessionID, "session-456")
	}
	if msg.Text != "flushed content" {
		t.Errorf("StreamFlush.Text = %q, want %q", msg.Text, "flushed content")
	}
}

func TestStreamDone(t *testing.T) {
	msg := StreamDone{SessionID: "session-789", FullText: "complete response"}
	msg.isMessage()

	if msg.SessionID != "session-789" {
		t.Errorf("StreamDone.SessionID = %q, want %q", msg.SessionID, "session-789")
	}
	if msg.FullText != "complete response" {
		t.Errorf("StreamDone.FullText = %q, want %q", msg.FullText, "complete response")
	}
}

func TestToolStart(t *testing.T) {
	args := map[string]any{"path": "/tmp/file.txt", "mode": "read"}
	msg := ToolStart{ToolID: "tool-1", ToolName: "read_file", Args: args}
	msg.isMessage()

	if msg.ToolID != "tool-1" {
		t.Errorf("ToolStart.ToolID = %q, want %q", msg.ToolID, "tool-1")
	}
	if msg.ToolName != "read_file" {
		t.Errorf("ToolStart.ToolName = %q, want %q", msg.ToolName, "read_file")
	}
	if msg.Args["path"] != "/tmp/file.txt" {
		t.Errorf("ToolStart.Args[path] = %v, want /tmp/file.txt", msg.Args["path"])
	}
}

func TestToolResult(t *testing.T) {
	msg := ToolResult{ToolID: "tool-2", Result: map[string]any{"success": true}, Err: nil}
	msg.isMessage()

	if msg.ToolID != "tool-2" {
		t.Errorf("ToolResult.ToolID = %q, want %q", msg.ToolID, "tool-2")
	}
	if msg.Err != nil {
		t.Error("ToolResult.Err should be nil")
	}
}

func TestAddMessageMsg(t *testing.T) {
	msg := AddMessageMsg{Content: "User message", Source: "user"}
	msg.isMessage()

	if msg.Content != "User message" {
		t.Errorf("AddMessageMsg.Content = %q, want %q", msg.Content, "User message")
	}
	if msg.Source != "user" {
		t.Errorf("AddMessageMsg.Source = %q, want %q", msg.Source, "user")
	}
}

func TestAppendTextMsg(t *testing.T) {
	msg := AppendTextMsg{Text: " appended"}
	msg.isMessage()

	if msg.Text != " appended" {
		t.Errorf("AppendTextMsg.Text = %q, want %q", msg.Text, " appended")
	}
}

func TestStatusMsg(t *testing.T) {
	msg := StatusMsg{Text: "Processing..."}
	msg.isMessage()

	if msg.Text != "Processing..." {
		t.Errorf("StatusMsg.Text = %q, want %q", msg.Text, "Processing...")
	}
}

func TestTokensMsg(t *testing.T) {
	msg := TokensMsg{Tokens: 1500, CostCent: 2.25}
	msg.isMessage()

	if msg.Tokens != 1500 {
		t.Errorf("TokensMsg.Tokens = %d, want 1500", msg.Tokens)
	}
	if msg.CostCent != 2.25 {
		t.Errorf("TokensMsg.CostCent = %f, want 2.25", msg.CostCent)
	}
}

func TestModelMsg(t *testing.T) {
	msg := ModelMsg{Name: "claude-3-sonnet"}
	msg.isMessage()

	if msg.Name != "claude-3-sonnet" {
		t.Errorf("ModelMsg.Name = %q, want %q", msg.Name, "claude-3-sonnet")
	}
}

func TestThinkingMsg(t *testing.T) {
	showMsg := ThinkingMsg{Show: true}
	showMsg.isMessage()

	if !showMsg.Show {
		t.Error("ThinkingMsg.Show should be true")
	}

	hideMsg := ThinkingMsg{Show: false}
	if hideMsg.Show {
		t.Error("ThinkingMsg.Show should be false")
	}
}

func TestRefreshMsg(t *testing.T) {
	msg := RefreshMsg{}
	msg.isMessage()
}

func TestQuitMsg(t *testing.T) {
	msg := QuitMsg{}
	msg.isMessage()
}
