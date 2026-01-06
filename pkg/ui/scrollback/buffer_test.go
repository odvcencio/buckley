package scrollback

import (
	"strings"
	"testing"

	"github.com/odvcencio/buckley/pkg/ui/compositor"
)

func TestBuffer(t *testing.T) {
	t.Run("new buffer", func(t *testing.T) {
		buf := NewBuffer(80, 24)
		if buf.LineCount() != 0 {
			t.Error("new buffer should be empty")
		}
		if buf.RowCount() != 0 {
			t.Error("new buffer should have 0 rows")
		}
	})

	t.Run("append line", func(t *testing.T) {
		buf := NewBuffer(80, 24)
		buf.AppendLine("Hello World", LineStyle{}, "user")

		if buf.LineCount() != 1 {
			t.Errorf("LineCount() = %d, want 1", buf.LineCount())
		}

		lines := buf.GetVisibleLines()
		if len(lines) != 1 {
			t.Fatalf("GetVisibleLines() = %d lines, want 1", len(lines))
		}
		if lines[0].Content != "Hello World" {
			t.Errorf("content = %q, want %q", lines[0].Content, "Hello World")
		}
	})

	t.Run("append text streaming", func(t *testing.T) {
		buf := NewBuffer(80, 24)
		buf.AppendLine("Hello", LineStyle{}, "assistant")
		buf.AppendText(" World")

		lines := buf.GetVisibleLines()
		if len(lines) != 1 {
			t.Fatalf("expected 1 line, got %d", len(lines))
		}
		if lines[0].Content != "Hello World" {
			t.Errorf("content = %q, want %q", lines[0].Content, "Hello World")
		}
	})

	t.Run("line wrapping", func(t *testing.T) {
		buf := NewBuffer(20, 10)
		longLine := "This is a very long line that should wrap to multiple lines"
		buf.AppendLine(longLine, LineStyle{}, "user")

		if buf.RowCount() <= 1 {
			t.Error("long line should wrap to multiple rows")
		}
	})

	t.Run("max lines", func(t *testing.T) {
		buf := NewBuffer(80, 24)
		buf.SetMaxLines(5)

		for i := 0; i < 10; i++ {
			buf.AppendLine("Line", LineStyle{}, "user")
		}

		if buf.LineCount() > 5 {
			t.Errorf("LineCount() = %d, should be <= 5", buf.LineCount())
		}
	})

	t.Run("clear", func(t *testing.T) {
		buf := NewBuffer(80, 24)
		buf.AppendLine("Test", LineStyle{}, "user")
		buf.Clear()

		if buf.LineCount() != 0 {
			t.Error("buffer should be empty after Clear()")
		}
	})
}

func TestScrolling(t *testing.T) {
	t.Run("scroll up/down", func(t *testing.T) {
		buf := NewBuffer(80, 5)

		// Add more lines than viewport
		for i := 0; i < 20; i++ {
			buf.AppendLine("Line", LineStyle{}, "user")
		}

		// Should be at bottom initially
		top, _, _ := buf.ScrollPosition()
		if top == 0 {
			t.Error("should not be at top with many lines")
		}

		buf.ScrollToTop()
		top, _, _ = buf.ScrollPosition()
		if top != 0 {
			t.Error("ScrollToTop should set top to 0")
		}

		buf.ScrollDown(5)
		top, _, _ = buf.ScrollPosition()
		if top != 5 {
			t.Errorf("after ScrollDown(5), top = %d, want 5", top)
		}

		buf.ScrollUp(3)
		top, _, _ = buf.ScrollPosition()
		if top != 2 {
			t.Errorf("after ScrollUp(3), top = %d, want 2", top)
		}
	})

	t.Run("follow mode", func(t *testing.T) {
		buf := NewBuffer(80, 5)

		for i := 0; i < 10; i++ {
			buf.AppendLine("Line", LineStyle{}, "user")
		}

		if !buf.IsFollowing() {
			t.Error("should be following after append")
		}

		buf.ScrollUp(1)
		if buf.IsFollowing() {
			t.Error("should not be following after manual scroll")
		}

		buf.ScrollToBottom()
		if !buf.IsFollowing() {
			t.Error("should resume following after ScrollToBottom")
		}
	})

	t.Run("page up/down", func(t *testing.T) {
		buf := NewBuffer(80, 10)

		for i := 0; i < 50; i++ {
			buf.AppendLine("Line", LineStyle{}, "user")
		}

		buf.ScrollToTop()
		buf.PageDown()

		top, _, _ := buf.ScrollPosition()
		if top != 9 { // height - 1
			t.Errorf("after PageDown, top = %d, want 9", top)
		}

		buf.PageUp()
		top, _, _ = buf.ScrollPosition()
		if top != 0 {
			t.Errorf("after PageUp, top = %d, want 0", top)
		}
	})
}

func TestSelection(t *testing.T) {
	t.Run("basic selection", func(t *testing.T) {
		buf := NewBuffer(80, 24)
		buf.AppendLine("Hello World", LineStyle{}, "user")

		buf.StartSelection(0, 0)
		buf.UpdateSelection(0, 5)
		buf.EndSelection()

		if !buf.HasSelection() {
			t.Error("should have selection")
		}

		selected := buf.GetSelection()
		if selected != "Hello" {
			t.Errorf("GetSelection() = %q, want %q", selected, "Hello")
		}
	})

	t.Run("multi-line selection", func(t *testing.T) {
		buf := NewBuffer(80, 24)
		buf.AppendLine("Line One", LineStyle{}, "user")
		buf.AppendLine("Line Two", LineStyle{}, "user")
		buf.AppendLine("Line Three", LineStyle{}, "user")

		buf.StartSelection(0, 5)
		buf.UpdateSelection(2, 4)
		buf.EndSelection()

		selected := buf.GetSelection()
		if !strings.Contains(selected, "One") || !strings.Contains(selected, "Two") {
			t.Errorf("multi-line selection incorrect: %q", selected)
		}
	})

	t.Run("clear selection", func(t *testing.T) {
		buf := NewBuffer(80, 24)
		buf.AppendLine("Test", LineStyle{}, "user")

		buf.StartSelection(0, 0)
		buf.UpdateSelection(0, 4)
		buf.ClearSelection()

		if buf.HasSelection() {
			t.Error("should not have selection after clear")
		}
	})
}

func TestSearch(t *testing.T) {
	t.Run("basic search", func(t *testing.T) {
		buf := NewBuffer(80, 24)
		buf.AppendLine("Hello World", LineStyle{}, "user")
		buf.AppendLine("World Hello", LineStyle{}, "user")
		buf.AppendLine("No match here", LineStyle{}, "user")

		count := buf.Search("Hello")
		if count != 2 {
			t.Errorf("Search(Hello) = %d matches, want 2", count)
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		buf := NewBuffer(80, 24)
		buf.AppendLine("HELLO world", LineStyle{}, "user")

		count := buf.Search("hello")
		if count != 1 {
			t.Errorf("Search should be case insensitive, got %d", count)
		}
	})

	t.Run("next/prev match", func(t *testing.T) {
		buf := NewBuffer(80, 24)
		buf.AppendLine("one", LineStyle{}, "user")
		buf.AppendLine("two", LineStyle{}, "user")
		buf.AppendLine("one", LineStyle{}, "user")

		buf.Search("one")

		_, total := buf.SearchMatches()
		if total != 2 {
			t.Fatalf("expected 2 matches, got %d", total)
		}

		buf.NextMatch()
		current, _ := buf.SearchMatches()
		if current != 2 {
			t.Errorf("after NextMatch, current = %d, want 2", current)
		}

		buf.PrevMatch()
		current, _ = buf.SearchMatches()
		if current != 1 {
			t.Errorf("after PrevMatch, current = %d, want 1", current)
		}
	})

	t.Run("empty search", func(t *testing.T) {
		buf := NewBuffer(80, 24)
		buf.AppendLine("Test", LineStyle{}, "user")

		count := buf.Search("")
		if count != 0 {
			t.Error("empty search should return 0")
		}
	})
}

func TestLatestCodeBlock(t *testing.T) {
	buf := NewBuffer(80, 24)
	buf.AppendLine("plain", LineStyle{}, "user")

	buf.AppendMessage([]Line{
		{Content: "go  [Alt+C copy]", IsCode: true, IsCodeHeader: true, Language: "go"},
		{Content: "fmt.Println(\"hi\")", IsCode: true, Language: "go"},
		{Content: "", IsCode: true, Language: "go"},
		{Content: "fmt.Println(\"bye\")", IsCode: true, Language: "go"},
	})

	lang, code, ok := buf.LatestCodeBlock()
	if !ok {
		t.Fatal("expected code block")
	}
	if lang != "go" {
		t.Fatalf("expected language go, got %q", lang)
	}
	if !strings.Contains(code, "fmt.Println(\"hi\")") {
		t.Fatalf("expected code to include fmt.Println(\"hi\"), got %q", code)
	}
}

func TestPositionForView(t *testing.T) {
	buf := NewBuffer(20, 5)
	buf.AppendMessage([]Line{{
		Content: "hello",
		Prefix:  []Span{{Text: "▶ "}},
	}})

	line, col, ok := buf.PositionForView(0, 0)
	if !ok {
		t.Fatal("expected position for view")
	}
	if line != 0 || col != 0 {
		t.Fatalf("expected line 0 col 0, got line %d col %d", line, col)
	}

	line, col, ok = buf.PositionForView(0, 3)
	if !ok {
		t.Fatal("expected position for view")
	}
	if line != 0 || col != 1 {
		t.Fatalf("expected line 0 col 1, got line %d col %d", line, col)
	}
}

func TestWrapLine(t *testing.T) {
	tests := []struct {
		text     string
		width    int
		expected int // number of lines
	}{
		{"short", 80, 1},
		{"", 80, 1},
		{"hello world", 5, 2},
		{"abcdefghij", 5, 2},
		{"a b c d e f", 3, 5}, // "a " "b " "c " "d " "e f"
	}

	for _, tt := range tests {
		lines := wrapPlainLine(tt.text, tt.width, false)
		if len(lines) != tt.expected {
			t.Errorf("wrapPlainLine(%q, %d) = %d lines, want %d",
				tt.text, tt.width, len(lines), tt.expected)
		}
	}
}

func TestResize(t *testing.T) {
	buf := NewBuffer(80, 24)
	buf.AppendLine("This is a test line that might wrap", LineStyle{}, "user")

	initialRows := buf.RowCount()

	// Resize to narrower
	buf.Resize(20, 24)

	if buf.RowCount() <= initialRows {
		t.Error("narrower resize should increase row count due to wrapping")
	}

	// Resize to wider
	buf.Resize(100, 24)

	if buf.RowCount() > initialRows {
		t.Error("wider resize should decrease or maintain row count")
	}
}

func TestRender(t *testing.T) {
	buf := NewBuffer(40, 10)
	buf.AppendLine("User message", LineStyle{}, "user")
	buf.AppendLine("Assistant response", LineStyle{}, "assistant")

	screen := compositor.NewScreen(50, 15)
	cfg := DefaultRenderConfig()

	// Should not panic
	Render(buf, screen, 0, 0, 40, 10, cfg)

	// Check something was rendered
	cell := screen.Get(0, 0)
	if cell.Rune == ' ' && cell.Style.Equal(compositor.DefaultStyle()) {
		t.Error("render should have produced visible content")
	}
}

func BenchmarkAppendLine(b *testing.B) {
	buf := NewBuffer(80, 24)
	style := LineStyle{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.AppendLine("Benchmark line with some content", style, "user")
	}
}

func BenchmarkSearch(b *testing.B) {
	buf := NewBuffer(80, 24)
	style := LineStyle{}

	// Add many lines
	for i := 0; i < 1000; i++ {
		buf.AppendLine("This is line number with searchable content", style, "user")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Search("searchable")
	}
}

func BenchmarkGetVisibleLines(b *testing.B) {
	buf := NewBuffer(80, 24)
	style := LineStyle{}

	for i := 0; i < 1000; i++ {
		buf.AppendLine("Content line", style, "user")
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.GetVisibleLines()
	}
}
