package buckley

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/ui/runtime"
	"github.com/odvcencio/buckley/pkg/ui/terminal"
)

func TestInputArea_New(t *testing.T) {
	ia := NewInputArea()
	if ia == nil {
		t.Fatal("expected non-nil input area")
	}
	if ia.Text() != "" {
		t.Errorf("expected empty text, got '%s'", ia.Text())
	}
	if ia.Mode() != ModeNormal {
		t.Errorf("expected normal mode, got %d", ia.Mode())
	}
}

func TestInputArea_Typing(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()

	// Type "hello"
	for _, r := range "hello" {
		ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: r})
	}

	if ia.Text() != "hello" {
		t.Errorf("expected 'hello', got '%s'", ia.Text())
	}
}

func TestInputArea_InsertText(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()

	// Insert text directly (paste)
	ia.InsertText("pasted content")

	if ia.Text() != "pasted content" {
		t.Errorf("expected 'pasted content', got '%s'", ia.Text())
	}
}

func TestInputArea_InsertText_AtCursor(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()

	// Type "hello world"
	ia.SetText("hello world")

	// Move cursor to position 6 (after "hello ")
	ia.cursorPos = 6

	// Paste "beautiful "
	ia.InsertText("beautiful ")

	expected := "hello beautiful world"
	if ia.Text() != expected {
		t.Errorf("expected '%s', got '%s'", expected, ia.Text())
	}
}

func TestInputArea_InsertText_Empty(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()

	// Type some text
	ia.SetText("existing")

	// Insert empty string (should be no-op)
	ia.InsertText("")

	if ia.Text() != "existing" {
		t.Errorf("expected 'existing', got '%s'", ia.Text())
	}
}

func TestInputArea_InsertText_Multiline(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()

	// Paste multiline content
	ia.InsertText("line1\nline2\nline3")

	expected := "line1\nline2\nline3"
	if ia.Text() != expected {
		t.Errorf("expected '%s', got '%s'", expected, ia.Text())
	}
}

func TestInputArea_Measure_Newlines(t *testing.T) {
	ia := NewInputArea()
	ia.SetText("line1\nline2")

	size := ia.Measure(runtime.Constraints{
		MaxWidth:  40,
		MaxHeight: 20,
	})

	if size.Height != 3 {
		t.Errorf("expected height 3, got %d", size.Height)
	}
}

func TestInputArea_Clear(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()

	ia.SetText("some text")
	ia.Clear()

	if ia.Text() != "" {
		t.Errorf("expected empty, got '%s'", ia.Text())
	}
	if ia.HasText() {
		t.Error("HasText should return false after clear")
	}
}

func TestInputArea_ModeChange(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()

	// Type ! to enter shell mode
	ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: '!'})

	if ia.Mode() != ModeShell {
		t.Errorf("expected shell mode, got %d", ia.Mode())
	}
}

func TestInputArea_Backspace(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()

	ia.SetText("test")
	ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyBackspace})

	if ia.Text() != "tes" {
		t.Errorf("expected 'tes', got '%s'", ia.Text())
	}
}

func TestInputArea_CursorMovement(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()

	ia.SetText("test")

	// Cursor should be at end
	if ia.cursorPos != 4 {
		t.Errorf("expected cursor at 4, got %d", ia.cursorPos)
	}

	// Move left
	ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyLeft})
	if ia.cursorPos != 3 {
		t.Errorf("expected cursor at 3, got %d", ia.cursorPos)
	}

	// Home
	ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyHome})
	if ia.cursorPos != 0 {
		t.Errorf("expected cursor at 0, got %d", ia.cursorPos)
	}

	// End
	ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyEnd})
	if ia.cursorPos != 4 {
		t.Errorf("expected cursor at 4, got %d", ia.cursorPos)
	}
}

func TestInputArea_CursorVerticalMovement(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()
	ia.Layout(runtime.Rect{X: 0, Y: 0, Width: 14, Height: 4}) // avail width = 10
	ia.SetText("0123456789abcde")                             // len 15, wraps to 2 lines

	if ia.cursorPos != 15 {
		t.Fatalf("expected cursor at 15, got %d", ia.cursorPos)
	}

	ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyUp})
	if ia.cursorPos != 5 {
		t.Fatalf("expected cursor at 5 after up, got %d", ia.cursorPos)
	}

	ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyDown})
	if ia.cursorPos != 15 {
		t.Fatalf("expected cursor at 15 after down, got %d", ia.cursorPos)
	}
}

func TestInputArea_CursorVerticalMovement_SingleLine(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()
	ia.Layout(runtime.Rect{X: 0, Y: 0, Width: 20, Height: 4}) // avail width = 16
	ia.SetText("short line")

	result := ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyUp})
	if result.Handled {
		t.Error("expected KeyUp to be unhandled for single-line input")
	}
}
