package widgets

import (
	"strings"
	"testing"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/markdown"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/theme"
)

func TestChatMessages_MouseSelection(t *testing.T) {
	m := NewChatMessages()
	m.SetMessageMetadataMode("never")
	m.AddMessage("first line\nsecond line", "assistant")
	m.Layout(runtime.Rect{X: 0, Y: 0, Width: 40, Height: 10})

	if m.listBounds.Height < 2 {
		t.Fatalf("expected at least 2 rows, got %d", m.listBounds.Height)
	}

	startX := m.listBounds.X + 1
	startY := m.listBounds.Y
	endX := m.listBounds.X + 2
	endY := m.listBounds.Y + 1

	m.HandleMessage(runtime.MouseMsg{X: startX, Y: startY, Button: runtime.MouseLeft, Action: runtime.MousePress})
	m.HandleMessage(runtime.MouseMsg{X: endX, Y: endY, Button: runtime.MouseNone, Action: runtime.MouseMove})
	m.HandleMessage(runtime.MouseMsg{X: endX, Y: endY, Button: runtime.MouseLeft, Action: runtime.MouseRelease})

	if !m.HasSelection() {
		t.Fatal("expected selection after mouse drag")
	}
}

func TestChatMessages_CodeHeaderClick(t *testing.T) {
	m := NewChatMessages()
	m.SetMessageMetadataMode("never")
	renderer := markdown.NewRenderer(theme.DefaultTheme())
	m.SetMarkdownRenderer(renderer, backend.DefaultStyle())
	m.AddMessage("```go\nfmt.Println(\"hi\")\n```", "assistant")
	m.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 10})

	lines := m.buffer.GetVisibleLines()
	var headerLineLine int
	var headerRow int
	found := false
	for _, line := range lines {
		raw, ok := m.buffer.LineAt(line.LineIndex)
		if !ok {
			continue
		}
		if raw.IsCodeHeader {
			headerLineLine = line.LineIndex
			headerRow = line.RowIndex
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected code header line")
	}

	rawLine, ok := m.buffer.LineAt(headerLineLine)
	if !ok {
		t.Fatal("expected raw line for code header")
	}
	idx := strings.Index(rawLine.Content, "[copy]")
	if idx < 0 {
		t.Fatal("expected [copy] in header")
	}
	prefixLen := len([]rune(spansText(rawLine.Prefix)))
	x := m.listBounds.X + prefixLen + idx
	y := m.listBounds.Y + headerRow

	var action, language, code string
	m.OnCodeAction(func(a, l, c string) {
		action = a
		language = l
		code = c
	})

	m.HandleMessage(runtime.MouseMsg{X: x, Y: y, Button: runtime.MouseLeft, Action: runtime.MousePress})

	if action != "copy" {
		t.Fatalf("expected copy action, got %q", action)
	}
	if language != "go" {
		t.Fatalf("expected language go, got %q", language)
	}
	if !strings.Contains(code, "fmt.Println") {
		t.Fatalf("expected code content, got %q", code)
	}
}
