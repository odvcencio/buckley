package tcell

import (
	"testing"

	tcellv2 "github.com/gdamore/tcell/v2"
	"m31labs.dev/buckley/pkg/ui/terminal"
)

func TestHandlePasteEvent_AccumulatesBracketedPaste(t *testing.T) {
	var backend Backend

	if event, consumed := backend.handlePasteEvent(tcellv2.NewEventPaste(true)); !consumed || event != nil {
		t.Fatalf("paste start consumed=%v event=%T, want consumed nil event", consumed, event)
	}
	if !backend.inPaste {
		t.Fatal("backend should enter paste mode")
	}

	for _, event := range []*tcellv2.EventKey{
		tcellv2.NewEventKey(tcellv2.KeyRune, 'a', tcellv2.ModNone),
		tcellv2.NewEventKey(tcellv2.KeyEnter, 0, tcellv2.ModNone),
		tcellv2.NewEventKey(tcellv2.KeyTab, 0, tcellv2.ModNone),
		tcellv2.NewEventKey(tcellv2.KeyUp, 0, tcellv2.ModNone),
	} {
		if pasteEvent, consumed := backend.handlePasteEvent(event); !consumed || pasteEvent != nil {
			t.Fatalf("paste key consumed=%v event=%T, want consumed nil event", consumed, pasteEvent)
		}
	}

	event, consumed := backend.handlePasteEvent(tcellv2.NewEventPaste(false))
	if !consumed {
		t.Fatal("paste end should be consumed")
	}
	paste, ok := event.(terminal.PasteEvent)
	if !ok {
		t.Fatalf("paste end returned %T, want terminal.PasteEvent", event)
	}
	if paste.Text != "a\n\t" {
		t.Fatalf("paste text = %q, want %q", paste.Text, "a\n\t")
	}
	if backend.inPaste {
		t.Fatal("backend should leave paste mode")
	}
	if backend.pasteBuffer.Len() != 0 {
		t.Fatal("paste buffer should be reset")
	}
}

func TestHandlePasteEvent_EmptyPasteIsConsumedWithoutEvent(t *testing.T) {
	var backend Backend

	backend.handlePasteEvent(tcellv2.NewEventPaste(true))
	event, consumed := backend.handlePasteEvent(tcellv2.NewEventPaste(false))
	if !consumed {
		t.Fatal("paste end should be consumed")
	}
	if event != nil {
		t.Fatalf("empty paste event = %T, want nil", event)
	}
}

func TestConvertEvent_Key(t *testing.T) {
	event := convertEvent(tcellv2.NewEventKey(tcellv2.KeyRune, 'x', tcellv2.ModAlt|tcellv2.ModShift))
	key, ok := event.(terminal.KeyEvent)
	if !ok {
		t.Fatalf("convertEvent returned %T, want terminal.KeyEvent", event)
	}
	if key.Key != terminal.KeyRune || key.Rune != 'x' || !key.Alt || key.Ctrl || !key.Shift {
		t.Fatalf("key event = %+v", key)
	}
}

func TestConvertEvent_Resize(t *testing.T) {
	event := convertEvent(tcellv2.NewEventResize(120, 40))
	resize, ok := event.(terminal.ResizeEvent)
	if !ok {
		t.Fatalf("convertEvent returned %T, want terminal.ResizeEvent", event)
	}
	if resize.Width != 120 || resize.Height != 40 {
		t.Fatalf("resize = %+v, want 120x40", resize)
	}
}

func TestConvertEvent_Mouse(t *testing.T) {
	event := convertEvent(tcellv2.NewEventMouse(7, 3, tcellv2.Button3, tcellv2.ModNone))
	mouse, ok := event.(terminal.MouseEvent)
	if !ok {
		t.Fatalf("convertEvent returned %T, want terminal.MouseEvent", event)
	}
	if mouse.X != 7 || mouse.Y != 3 || mouse.Button != terminal.MouseRight || mouse.Action != terminal.MousePress {
		t.Fatalf("mouse = %+v", mouse)
	}
}

func TestConvertMouseAction(t *testing.T) {
	tests := []struct {
		name    string
		buttons tcellv2.ButtonMask
		want    terminal.MouseAction
	}{
		{name: "release", buttons: tcellv2.ButtonNone, want: terminal.MouseRelease},
		{name: "wheel", buttons: tcellv2.WheelUp, want: terminal.MousePress},
		{name: "button", buttons: tcellv2.Button1, want: terminal.MousePress},
	}

	for _, tt := range tests {
		if got := convertMouseAction(tt.buttons); got != tt.want {
			t.Fatalf("%s: convertMouseAction() = %v, want %v", tt.name, got, tt.want)
		}
	}
}
