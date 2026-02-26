package scrollback

import (
	"testing"

	"github.com/odvcencio/fluffyui/compositor"
)

func TestDefaultRenderConfig(t *testing.T) {
	cfg := DefaultRenderConfig()

	// Verify all styles are set
	if cfg.UserStyle.Equal(compositor.DefaultStyle()) {
		t.Error("UserStyle should not be default")
	}
	if cfg.AssistantStyle.Equal(compositor.DefaultStyle()) {
		t.Error("AssistantStyle should not be default")
	}
	if cfg.SystemStyle.Equal(compositor.DefaultStyle()) {
		t.Error("SystemStyle should not be default")
	}
	if cfg.ToolStyle.Equal(compositor.DefaultStyle()) {
		t.Error("ToolStyle should not be default")
	}
}

func TestGetStyleForSource(t *testing.T) {
	cfg := DefaultRenderConfig()

	tests := []struct {
		source   string
		expected compositor.Style
	}{
		{"user", cfg.UserStyle},
		{"assistant", cfg.AssistantStyle},
		{"system", cfg.SystemStyle},
		{"tool", cfg.ToolStyle},
		{"unknown", compositor.DefaultStyle()},
		{"", compositor.DefaultStyle()},
	}

	for _, tt := range tests {
		got := getStyleForSource(tt.source, cfg)
		if !got.Equal(tt.expected) {
			t.Errorf("getStyleForSource(%q) style mismatch", tt.source)
		}
	}
}

func TestItoa(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{10, "10"},
		{100, "100"},
		{12345, "12345"},
		{-1, "-1"},
		{-100, "-100"},
		{-12345, "-12345"},
	}

	for _, tt := range tests {
		got := itoa(tt.input)
		if got != tt.expected {
			t.Errorf("itoa(%d) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestRender_NilInputs(t *testing.T) {
	// Should not panic with nil inputs
	screen := compositor.NewScreen(80, 24)
	cfg := DefaultRenderConfig()

	Render(nil, screen, 0, 0, 80, 24, cfg)
	Render(NewBuffer(80, 24), nil, 0, 0, 80, 24, cfg)
	Render(nil, nil, 0, 0, 80, 24, cfg)
}

func TestRender_EmptyBuffer(t *testing.T) {
	buf := NewBuffer(80, 24)
	screen := compositor.NewScreen(80, 24)
	cfg := DefaultRenderConfig()

	// Should not panic
	Render(buf, screen, 0, 0, 80, 24, cfg)
}

func TestRender_WithSelection(t *testing.T) {
	buf := NewBuffer(80, 24)
	buf.AppendLine("Hello World", LineStyle{}, "user")

	buf.StartSelection(0, 0)
	buf.UpdateSelection(0, 5)
	buf.EndSelection()

	screen := compositor.NewScreen(80, 24)
	cfg := DefaultRenderConfig()

	// Should render with selection highlighting
	Render(buf, screen, 0, 0, 80, 24, cfg)

	// First character should have selection style
	cell := screen.Get(0, 0)
	if cell.Rune != 'H' {
		t.Errorf("expected 'H', got '%c'", cell.Rune)
	}
}

func TestRender_WithSearchHighlight(t *testing.T) {
	buf := NewBuffer(80, 24)
	buf.AppendLine("Hello World Hello", LineStyle{}, "user")

	buf.Search("Hello")

	screen := compositor.NewScreen(80, 24)
	cfg := DefaultRenderConfig()

	Render(buf, screen, 0, 0, 80, 24, cfg)

	// Content should be rendered
	cell := screen.Get(0, 0)
	if cell.Rune != 'H' {
		t.Errorf("expected 'H', got '%c'", cell.Rune)
	}
}

func TestRender_WithExplicitStyle(t *testing.T) {
	buf := NewBuffer(80, 24)
	style := LineStyle{
		FG:   0xFF0000, // Red
		Bold: true,
	}
	buf.AppendLine("Styled text", style, "user")

	screen := compositor.NewScreen(80, 24)
	cfg := DefaultRenderConfig()

	Render(buf, screen, 0, 0, 80, 24, cfg)

	// Should render with custom style
	cell := screen.Get(0, 0)
	if cell.Rune != 'S' {
		t.Errorf("expected 'S', got '%c'", cell.Rune)
	}
}

func TestRenderStatusLine(t *testing.T) {
	screen := compositor.NewScreen(80, 24)
	style := compositor.DefaultStyle()

	t.Run("empty buffer shows All", func(t *testing.T) {
		buf := NewBuffer(80, 10)
		RenderStatusLine(buf, screen, 0, 0, 20, style)
		// Should show "All" since content fits
	})

	t.Run("buffer at top shows Top", func(t *testing.T) {
		buf := NewBuffer(80, 5)
		for i := 0; i < 20; i++ {
			buf.AppendLine("Line", LineStyle{}, "user")
		}
		buf.ScrollToTop()
		RenderStatusLine(buf, screen, 0, 0, 20, style)
	})

	t.Run("buffer at bottom shows Bot", func(t *testing.T) {
		buf := NewBuffer(80, 5)
		for i := 0; i < 20; i++ {
			buf.AppendLine("Line", LineStyle{}, "user")
		}
		buf.ScrollToBottom()
		RenderStatusLine(buf, screen, 0, 0, 20, style)
	})

	t.Run("buffer in middle shows percentage", func(t *testing.T) {
		buf := NewBuffer(80, 5)
		for i := 0; i < 50; i++ {
			buf.AppendLine("Line", LineStyle{}, "user")
		}
		buf.ScrollToTop()
		buf.ScrollDown(10)
		RenderStatusLine(buf, screen, 0, 0, 20, style)
	})

	t.Run("with search matches", func(t *testing.T) {
		buf := NewBuffer(80, 10)
		buf.AppendLine("test one", LineStyle{}, "user")
		buf.AppendLine("test two", LineStyle{}, "user")
		buf.Search("test")
		RenderStatusLine(buf, screen, 0, 0, 20, style)
	})
}

func TestRenderScrollbar(t *testing.T) {
	screen := compositor.NewScreen(80, 24)
	cfg := DefaultRenderConfig()

	t.Run("no scrollbar needed", func(t *testing.T) {
		buf := NewBuffer(80, 24)
		buf.AppendLine("Short", LineStyle{}, "user")

		renderScrollbar(buf, screen, 79, 0, 24, cfg)
		// Should fill with background
	})

	t.Run("scrollbar with content", func(t *testing.T) {
		buf := NewBuffer(80, 10)
		for i := 0; i < 50; i++ {
			buf.AppendLine("Line", LineStyle{}, "user")
		}

		renderScrollbar(buf, screen, 79, 0, 10, cfg)
		// Should draw scrollbar
	})
}

func BenchmarkRender(b *testing.B) {
	buf := NewBuffer(80, 24)
	for i := 0; i < 100; i++ {
		buf.AppendLine("This is a line of content for benchmarking", LineStyle{}, "user")
	}

	screen := compositor.NewScreen(80, 24)
	cfg := DefaultRenderConfig()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Render(buf, screen, 0, 0, 80, 24, cfg)
	}
}
