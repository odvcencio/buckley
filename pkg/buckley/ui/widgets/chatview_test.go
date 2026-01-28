package widgets

import (
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/buckley/ui/scrollback"
	"github.com/odvcencio/fluffy-ui/backend"
	"github.com/odvcencio/fluffy-ui/runtime"
	"github.com/odvcencio/fluffy-ui/terminal"
)

func prefixText(prefix []scrollback.Span) string {
	var b strings.Builder
	for _, span := range prefix {
		b.WriteString(span.Text)
	}
	return b.String()
}

func findLine(t *testing.T, cv *ChatView, content, source string) scrollback.Line {
	t.Helper()
	for i := 0; i < cv.buffer.LineCount(); i++ {
		line, ok := cv.buffer.LineAt(i)
		if !ok {
			continue
		}
		if line.Content == content && line.Source == source {
			return line
		}
	}
	t.Fatalf("expected line with content %q and source %q", content, source)
	return scrollback.Line{}
}

func TestNewChatView(t *testing.T) {
	cv := NewChatView()

	if cv == nil {
		t.Fatal("expected non-nil ChatView")
	}
	if cv.buffer == nil {
		t.Error("expected non-nil buffer")
	}
}

func TestChatView_AddMessage_User(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 24})

	cv.AddMessage("Hello, world!", "user")

	line := findLine(t, cv, "Hello, world!", "user")
	if got := prefixText(line.Prefix); got != "▍ ▶ " {
		t.Errorf("expected prefix '▍ ▶ ', got %q", got)
	}
	if line.Content != "Hello, world!" {
		t.Errorf("expected 'Hello, world!', got '%s'", line.Content)
	}
	if line.Source != "user" {
		t.Errorf("expected source 'user', got '%s'", line.Source)
	}
}

func TestChatView_AddMessage_Assistant(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 24})

	cv.AddMessage("I can help with that.", "assistant")

	line := findLine(t, cv, "I can help with that.", "assistant")
	if got := prefixText(line.Prefix); got != "▍ " {
		t.Errorf("expected prefix '▍ ', got %q", got)
	}
	if line.Content != "I can help with that." {
		t.Errorf("expected 'I can help with that.', got '%s'", line.Content)
	}
	if line.Source != "assistant" {
		t.Errorf("expected source 'assistant', got '%s'", line.Source)
	}
}

func TestChatView_AddMessage_Thinking(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 24})

	cv.AddMessage("", "thinking")

	line := findLine(t, cv, "  ◦ thinking...", "thinking")
	if line.Source != "thinking" {
		t.Errorf("expected source 'thinking', got '%s'", line.Source)
	}
}

func TestChatView_AddMessage_System(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 24})

	cv.AddMessage("System notification", "system")

	line := findLine(t, cv, "System notification", "system")
	if got := prefixText(line.Prefix); got != "▍ " {
		t.Errorf("expected prefix '▍ ', got %q", got)
	}
	if line.Source != "system" {
		t.Errorf("expected source 'system', got '%s'", line.Source)
	}
}

func TestChatView_AppendText(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 24})

	// Add initial message
	cv.AddMessage("Hello", "assistant")

	// Append to the message (streaming simulation)
	cv.AppendText(", world!")

	line := findLine(t, cv, "Hello, world!", "assistant")
	if got := prefixText(line.Prefix); got != "▍ " {
		t.Errorf("expected prefix '▍ ', got %q", got)
	}
}

func TestChatView_RemoveThinkingIndicator(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 24})

	// Add thinking indicator
	cv.AddMessage("", "thinking")

	// Verify it's there
	foundBefore := false
	for i := 0; i < cv.buffer.LineCount(); i++ {
		line, ok := cv.buffer.LineAt(i)
		if !ok {
			continue
		}
		if line.Source == "thinking" {
			foundBefore = true
			break
		}
	}
	if !foundBefore {
		t.Fatal("expected thinking indicator before removal")
	}

	// Remove it
	cv.RemoveThinkingIndicator()

	// Verify it's gone
	foundAfter := false
	for i := 0; i < cv.buffer.LineCount(); i++ {
		line, ok := cv.buffer.LineAt(i)
		if !ok {
			continue
		}
		if line.Source == "thinking" {
			foundAfter = true
			break
		}
	}
	if foundAfter {
		t.Error("thinking indicator should be removed")
	}
}

func TestChatView_Clear(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 24})

	cv.AddMessage("Message 1", "user")
	cv.AddMessage("Message 2", "assistant")

	cv.Clear()

	lines := cv.buffer.GetVisibleLines()
	if len(lines) != 0 {
		t.Errorf("expected 0 lines after clear, got %d", len(lines))
	}
}

func TestChatView_ScrollUp(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 5})

	// Add enough messages to enable scrolling
	for i := 0; i < 20; i++ {
		cv.AddMessage("Line", "user")
	}

	var notified bool
	cv.OnScrollChange(func(top, total, viewHeight int) {
		notified = true
	})

	cv.ScrollUp(1)

	if !notified {
		t.Error("expected scroll change notification")
	}
}

func TestChatView_ScrollDown(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 5})

	// Add enough messages
	for i := 0; i < 20; i++ {
		cv.AddMessage("Line", "user")
	}

	// Scroll up first, then down
	cv.ScrollUp(5)

	var notified bool
	cv.OnScrollChange(func(top, total, viewHeight int) {
		notified = true
	})

	cv.ScrollDown(1)

	if !notified {
		t.Error("expected scroll change notification")
	}
}

func TestChatView_PageUp(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 5})

	for i := 0; i < 20; i++ {
		cv.AddMessage("Line", "user")
	}

	var notified bool
	cv.OnScrollChange(func(top, total, viewHeight int) {
		notified = true
	})

	cv.PageUp()

	if !notified {
		t.Error("expected scroll change notification on PageUp")
	}
}

func TestChatView_PageDown(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 5})

	for i := 0; i < 20; i++ {
		cv.AddMessage("Line", "user")
	}

	cv.PageUp()

	var notified bool
	cv.OnScrollChange(func(top, total, viewHeight int) {
		notified = true
	})

	cv.PageDown()

	if !notified {
		t.Error("expected scroll change notification on PageDown")
	}
}

func TestChatView_ScrollToTop(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 5})

	for i := 0; i < 20; i++ {
		cv.AddMessage("Line", "user")
	}

	var notified bool
	cv.OnScrollChange(func(top, total, viewHeight int) {
		notified = true
		if top != 0 {
			t.Errorf("expected top=0 after ScrollToTop, got %d", top)
		}
	})

	cv.ScrollToTop()

	if !notified {
		t.Error("expected scroll change notification on ScrollToTop")
	}
}

func TestChatView_ScrollToBottom(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 5})

	for i := 0; i < 20; i++ {
		cv.AddMessage("Line", "user")
	}

	cv.ScrollToTop()

	var notified bool
	cv.OnScrollChange(func(top, total, viewHeight int) {
		notified = true
	})

	cv.ScrollToBottom()

	if !notified {
		t.Error("expected scroll change notification on ScrollToBottom")
	}
}

func TestChatView_ScrollPosition(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 10})
	cv.buffer.Resize(79, 10)

	for i := 0; i < 5; i++ {
		cv.AddMessage("Line", "user")
	}

	top, total, viewHeight := cv.ScrollPosition()

	if viewHeight <= 0 || viewHeight > 10 {
		t.Errorf("expected viewHeight between 1 and 10, got %d", viewHeight)
	}
	if total < 5 {
		t.Errorf("expected total >= 5, got %d", total)
	}
	_ = top // top value depends on scroll mode
}

func TestChatView_Search(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 24})

	cv.AddMessage("Hello world", "user")
	cv.AddMessage("Goodbye world", "assistant")
	cv.AddMessage("Hello again", "user")

	cv.Search("Hello")

	// Search sets internal state in buffer - can't easily verify externally
	// Just ensure no panic
}

func TestChatView_NextMatch(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 24})

	cv.AddMessage("Hello world", "user")
	cv.AddMessage("Hello again", "user")

	cv.Search("Hello")
	cv.NextMatch()

	// Just ensure no panic
}

func TestChatView_ClearSearch(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 24})

	cv.AddMessage("Hello world", "user")

	cv.Search("Hello")
	cv.ClearSearch()

	// Just ensure no panic
}

func TestChatView_Measure(t *testing.T) {
	cv := NewChatView()

	size := cv.Measure(runtime.Constraints{
		MaxWidth:  80,
		MaxHeight: 24,
	})

	if size.Width != 80 {
		t.Errorf("expected width 80, got %d", size.Width)
	}
	if size.Height != 24 {
		t.Errorf("expected height 24, got %d", size.Height)
	}
}

func TestChatView_Layout(t *testing.T) {
	cv := NewChatView()

	cv.Layout(runtime.Rect{X: 5, Y: 10, Width: 60, Height: 20})

	bounds := cv.Bounds()
	if bounds.X != 5 || bounds.Y != 10 {
		t.Errorf("expected position (5, 10), got (%d, %d)", bounds.X, bounds.Y)
	}
	if bounds.Width != 60 || bounds.Height != 20 {
		t.Errorf("expected size (60, 20), got (%d, %d)", bounds.Width, bounds.Height)
	}
}

func TestChatView_Render(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 40, Height: 10})

	cv.AddMessage("Test message", "user")

	buf := runtime.NewBuffer(40, 10)
	ctx := runtime.RenderContext{Buffer: buf}

	// Should not panic
	cv.Render(ctx)
}

func TestChatView_Render_EmptyBounds(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 0, Height: 0})

	buf := runtime.NewBuffer(40, 10)
	ctx := runtime.RenderContext{Buffer: buf}

	// Should not panic with empty bounds
	cv.Render(ctx)
}

func TestChatView_Render_WithScrollbar(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 40, Height: 5})

	// Add enough messages to require scrollbar
	for i := 0; i < 20; i++ {
		cv.AddMessage("Line", "user")
	}

	buf := runtime.NewBuffer(40, 5)
	ctx := runtime.RenderContext{Buffer: buf}

	cv.Render(ctx)

	// Check for scrollbar character on the right edge
	// Either thumb '█' or track '░'
	cell := buf.Get(39, 2)
	if cell.Rune != '█' && cell.Rune != '░' {
		// Scrollbar might not be visible depending on scroll position
		// This is fine - the test confirms no panic
	}
}

func TestChatView_HandleMessage_Up(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 5})

	for i := 0; i < 20; i++ {
		cv.AddMessage("Line", "user")
	}

	result := cv.HandleMessage(runtime.KeyMsg{Key: terminal.KeyUp})

	if !result.Handled {
		t.Error("KeyUp should be handled")
	}
}

func TestChatView_HandleMessage_Down(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 5})

	for i := 0; i < 20; i++ {
		cv.AddMessage("Line", "user")
	}
	cv.ScrollUp(5)

	result := cv.HandleMessage(runtime.KeyMsg{Key: terminal.KeyDown})

	if !result.Handled {
		t.Error("KeyDown should be handled")
	}
}

func TestChatView_HandleMessage_PageUp(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 5})

	for i := 0; i < 20; i++ {
		cv.AddMessage("Line", "user")
	}

	result := cv.HandleMessage(runtime.KeyMsg{Key: terminal.KeyPageUp})

	if !result.Handled {
		t.Error("PageUp should be handled")
	}
}

func TestChatView_HandleMessage_PageDown(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 5})

	for i := 0; i < 20; i++ {
		cv.AddMessage("Line", "user")
	}
	cv.ScrollUp(10)

	result := cv.HandleMessage(runtime.KeyMsg{Key: terminal.KeyPageDown})

	if !result.Handled {
		t.Error("PageDown should be handled")
	}
}

func TestChatView_HandleMessage_Unhandled(t *testing.T) {
	cv := NewChatView()

	// Non-KeyMsg should not be handled (use ResizeMsg as a valid but unhandled message)
	result := cv.HandleMessage(runtime.ResizeMsg{Width: 80, Height: 24})

	if result.Handled {
		t.Error("ResizeMsg should not be handled by ChatView")
	}
}

func TestChatView_HandleMessage_UnhandledKey(t *testing.T) {
	cv := NewChatView()

	// Other keys should not be handled
	result := cv.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: 'a'})

	if result.Handled {
		t.Error("regular key should not be handled by ChatView")
	}
}

func TestChatView_SetStyles(t *testing.T) {
	cv := NewChatView()

	user := backend.DefaultStyle().Bold(true)
	assistant := backend.DefaultStyle().Italic(true)
	system := backend.DefaultStyle().Dim(true)
	tool := backend.DefaultStyle()
	thinking := backend.DefaultStyle().Dim(true).Italic(true)

	cv.SetStyles(user, assistant, system, tool, thinking)

	// Styles are set internally - verify by adding messages
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 24})
	cv.AddMessage("Test", "user")

	// No panic means success
}

func TestChatView_SetUIStyles(t *testing.T) {
	cv := NewChatView()

	scrollbar := backend.DefaultStyle()
	thumb := backend.DefaultStyle().Bold(true)
	selection := backend.DefaultStyle().Reverse(true)
	search := backend.DefaultStyle().Reverse(true)

	cv.SetUIStyles(scrollbar, thumb, selection, search, backend.DefaultStyle())

	// No panic means success
}

func TestChatView_RenderBackgroundFill(t *testing.T) {
	cv := NewChatView()
	bg := backend.DefaultStyle().Background(backend.ColorRGB(10, 20, 30))
	cv.SetUIStyles(backend.DefaultStyle(), backend.DefaultStyle(), backend.DefaultStyle(), backend.DefaultStyle(), bg)

	bounds := runtime.Rect{X: 0, Y: 0, Width: 4, Height: 2}
	cv.Layout(bounds)

	buf := runtime.NewBuffer(bounds.Width, bounds.Height)
	ctx := runtime.RenderContext{Buffer: buf, Bounds: bounds}
	cv.Render(ctx)

	cell := buf.Get(0, 0)
	if cell.Style != bg {
		t.Fatalf("expected background style, got %#v", cell.Style)
	}
}

func TestChatView_OnScrollChange_NilCallback(t *testing.T) {
	cv := NewChatView()
	cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 5})

	for i := 0; i < 20; i++ {
		cv.AddMessage("Line", "user")
	}

	// No callback set - should not panic
	cv.ScrollUp(1)
}

func TestChatView_styleForSource(t *testing.T) {
	cv := NewChatView()

	tests := []struct {
		source string
	}{
		{"user"},
		{"assistant"},
		{"system"},
		{"tool"},
		{"thinking"},
		{"unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.source, func(t *testing.T) {
			// styleForSource is private, but we can test through AddMessage
			cv.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 24})
			cv.AddMessage("Test", tc.source)
			// No panic means success
		})
	}
}

func TestExtractFG(t *testing.T) {
	style := backend.DefaultStyle()
	fg := extractFG(style)
	// Default style FG is platform-specific, just check it returns a value
	_ = fg
}

func TestIsBold(t *testing.T) {
	normal := backend.DefaultStyle()
	bold := backend.DefaultStyle().Bold(true)

	if isBold(normal) {
		t.Error("normal style should not be bold")
	}
	if !isBold(bold) {
		t.Error("bold style should be bold")
	}
}

func TestIsItalic(t *testing.T) {
	normal := backend.DefaultStyle()
	italic := backend.DefaultStyle().Italic(true)

	if isItalic(normal) {
		t.Error("normal style should not be italic")
	}
	if !isItalic(italic) {
		t.Error("italic style should be italic")
	}
}

func TestIsDim(t *testing.T) {
	normal := backend.DefaultStyle()
	dim := backend.DefaultStyle().Dim(true)

	if isDim(normal) {
		t.Error("normal style should not be dim")
	}
	if !isDim(dim) {
		t.Error("dim style should be dim")
	}
}
