package widgets

import (
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/markdown"
	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/state"
	"m31labs.dev/fluffyui/theme"
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

func TestChatMessages_StateTranscriptMatchesWrappedRows(t *testing.T) {
	content := "2. God files are candidates for decomposition and the IPC server continues onto another visual row."
	renderer := markdown.NewRenderer(theme.DefaultTheme())

	direct := NewChatMessages()
	direct.SetMessageMetadataMode("never")
	direct.SetMarkdownRenderer(renderer, backend.DefaultStyle())
	direct.AddMessage(content, "assistant")
	direct.Layout(runtime.Rect{X: 0, Y: 0, Width: 38, Height: 10})
	directRows := chatMessagesRows(direct, "assistant")
	assertWrappedOrderedListRows(t, directRows, "▍ 2. ")

	messages := state.NewSignal([]ChatMessage{{ID: 1, Content: content, Source: "assistant"}})
	metadataMode := state.NewSignal("never")
	synced := NewChatMessagesWithConfig(ChatMessagesConfig{
		Messages:     messages,
		MetadataMode: metadataMode,
	})
	synced.SetMarkdownRenderer(renderer, backend.DefaultStyle())
	synced.syncFromMessages()
	synced.Layout(runtime.Rect{X: 0, Y: 0, Width: 38, Height: 10})
	syncedRows := chatMessagesRows(synced, "assistant")

	if !reflect.DeepEqual(syncedRows, directRows) {
		t.Fatalf("state transcript rows should match direct rows\nsynced: %#v\ndirect: %#v", syncedRows, directRows)
	}
}

func chatMessagesRows(m *ChatMessages, source string) []string {
	var rows []string
	for _, line := range m.buffer.GetVisibleLines() {
		if line.Source != source || strings.TrimSpace(line.Content) == "" {
			continue
		}
		rows = append(rows, line.Content)
	}
	return rows
}

func assertWrappedOrderedListRows(t *testing.T, rows []string, firstPrefix string) {
	t.Helper()
	if len(rows) < 2 {
		t.Fatalf("expected wrapped list rows, got %#v", rows)
	}
	if !strings.HasPrefix(rows[0], firstPrefix) {
		t.Fatalf("first row should keep ordered-list prefix %q, got %q", firstPrefix, rows[0])
	}
	indent := strings.Repeat(" ", len([]rune(firstPrefix)))
	for _, row := range rows[1:] {
		if !strings.HasPrefix(row, indent) {
			t.Fatalf("continuation row should preserve indent %q, got %q", indent, row)
		}
		if strings.HasPrefix(strings.TrimLeft(row, " "), "2.") {
			t.Fatalf("continuation row should not repeat ordered-list marker, got %q", row)
		}
	}
}
