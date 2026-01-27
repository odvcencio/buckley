package widgets

import (
	"testing"

	"github.com/odvcencio/fluffy-ui/runtime"
	"github.com/odvcencio/fluffy-ui/terminal"
)

func TestInputArea_New(t *testing.T) {
	ia := NewInputArea()
	if ia == nil {
		t.Fatal("expected non-nil input area")
	}
	if ia.Text() != "" {
		t.Errorf("expected empty text, got %q", ia.Text())
	}
	if ia.Mode() != ModeNormal {
		t.Errorf("expected normal mode, got %d", ia.Mode())
	}
}

func TestInputArea_Typing(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()

	for _, r := range "hello" {
		ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: r})
	}

	if ia.Text() != "hello" {
		t.Errorf("expected 'hello', got %q", ia.Text())
	}
}

func TestInputArea_InsertText(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()
	ia.InsertText("pasted content")
	if ia.Text() != "pasted content" {
		t.Errorf("expected 'pasted content', got %q", ia.Text())
	}
}

func TestInputArea_InsertText_AtCursor(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()
	ia.SetText("hello world")

	// Move cursor left 5 times to place before "world"
	for i := 0; i < 5; i++ {
		ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyLeft})
	}

	ia.InsertText("beautiful ")
	if ia.Text() != "hello beautiful world" {
		t.Errorf("expected 'hello beautiful world', got %q", ia.Text())
	}
}

func TestInputArea_Measure_Newlines(t *testing.T) {
	ia := NewInputArea()
	ia.SetText("line1\nline2")

	size := ia.Measure(runtime.Constraints{MaxWidth: 40, MaxHeight: 20})
	if size.Height != 4 { // 2 lines + top/bottom border
		t.Errorf("expected height 4, got %d", size.Height)
	}
}

func TestInputArea_Clear(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()
	ia.SetText("some text")
	ia.Clear()

	if ia.Text() != "" {
		t.Errorf("expected empty, got %q", ia.Text())
	}
	if ia.HasText() {
		t.Error("HasText should return false after clear")
	}
	if ia.Mode() != ModeNormal {
		t.Errorf("expected normal mode after clear, got %d", ia.Mode())
	}
}

func TestInputArea_ModeChange(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()
	ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: '!'})
	if ia.Mode() != ModeShell {
		t.Errorf("expected shell mode, got %d", ia.Mode())
	}
}

func TestInputArea_CursorMovement(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()
	ia.SetText("test")

	x, _ := ia.CursorPosition()
	if x != 4 {
		t.Errorf("expected cursor at 4, got %d", x)
	}

	ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyLeft})
	x, _ = ia.CursorPosition()
	if x != 3 {
		t.Errorf("expected cursor at 3, got %d", x)
	}

	ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyHome})
	x, _ = ia.CursorPosition()
	if x != 0 {
		t.Errorf("expected cursor at 0, got %d", x)
	}

	ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyEnd})
	x, _ = ia.CursorPosition()
	if x != 4 {
		t.Errorf("expected cursor at 4, got %d", x)
	}
}

func TestInputArea_CursorVerticalMovement(t *testing.T) {
	ia := NewInputArea()
	ia.Focus()
	ia.SetText("0123456789\nabcde")
	ia.Layout(runtime.Rect{X: 0, Y: 0, Width: 16, Height: 4})

	_, y := ia.CursorPosition()
	if y != 1 {
		t.Fatalf("expected cursor on line 1, got %d", y)
	}

	ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyUp})
	_, y = ia.CursorPosition()
	if y != 0 {
		t.Fatalf("expected cursor on line 0 after up, got %d", y)
	}

	ia.HandleMessage(runtime.KeyMsg{Key: terminal.KeyDown})
	_, y = ia.CursorPosition()
	if y != 1 {
		t.Fatalf("expected cursor on line 1 after down, got %d", y)
	}
}
