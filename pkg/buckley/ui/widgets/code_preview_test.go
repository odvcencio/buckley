package widgets

import (
	"testing"

	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/terminal"
)

func TestCodePreview_EscapeCloses(t *testing.T) {
	preview := NewCodePreview("go", "package main")
	result := preview.HandleMessage(runtime.KeyMsg{Key: terminal.KeyEscape})
	if !result.Handled {
		t.Fatal("expected escape to be handled")
	}
	if len(result.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(result.Commands))
	}
	if _, ok := result.Commands[0].(runtime.PopOverlay); !ok {
		t.Fatalf("expected PopOverlay, got %T", result.Commands[0])
	}
}
