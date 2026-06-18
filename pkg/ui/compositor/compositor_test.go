package compositor

import "testing"

func TestColor(t *testing.T) {
	t.Run("predefined colors", func(t *testing.T) {
		tests := []struct {
			name  string
			color Color
			mode  ColorMode
			value uint32
		}{
			{"none", ColorNone, ColorModeNone, 0},
			{"default", ColorDefault, ColorModeDefault, 0},
			{"black", ColorBlack, ColorMode16, 0},
			{"red", ColorRed, ColorMode16, 1},
			{"bright white", ColorBrightWhite, ColorMode16, 15},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if tt.color.Mode != tt.mode {
					t.Errorf("got mode %v, want %v", tt.color.Mode, tt.mode)
				}
				if tt.color.Value != tt.value {
					t.Errorf("got value %d, want %d", tt.color.Value, tt.value)
				}
			})
		}
	})

	t.Run("Color256", func(t *testing.T) {
		c := Color256(128)
		if c.Mode != ColorMode256 {
			t.Errorf("got mode %v, want ColorMode256", c.Mode)
		}
		if c.Value != 128 {
			t.Errorf("got value %d, want 128", c.Value)
		}
	})

	t.Run("RGB", func(t *testing.T) {
		c := RGB(255, 128, 64)
		if c.Mode != ColorModeRGB {
			t.Errorf("got mode %v, want ColorModeRGB", c.Mode)
		}
		expected := uint32(0xFF8040)
		if c.Value != expected {
			t.Errorf("got value 0x%06X, want 0x%06X", c.Value, expected)
		}
	})

	t.Run("Hex", func(t *testing.T) {
		c := Hex(0xABCDEF)
		if c.Mode != ColorModeRGB {
			t.Errorf("got mode %v, want ColorModeRGB", c.Mode)
		}
		if c.Value != 0xABCDEF {
			t.Errorf("got value 0x%06X, want 0xABCDEF", c.Value)
		}
	})
}

func TestStyle(t *testing.T) {
	t.Run("DefaultStyle", func(t *testing.T) {
		s := DefaultStyle()
		if s.FG != ColorDefault {
			t.Error("default FG should be ColorDefault")
		}
		if s.BG != ColorDefault {
			t.Error("default BG should be ColorDefault")
		}
		if s.Bold || s.Dim || s.Italic || s.Underline {
			t.Error("default style should have no attributes")
		}
	})

	t.Run("WithFG", func(t *testing.T) {
		s := DefaultStyle().WithFG(ColorRed)
		if s.FG != ColorRed {
			t.Errorf("got FG %v, want ColorRed", s.FG)
		}
	})

	t.Run("WithBG", func(t *testing.T) {
		s := DefaultStyle().WithBG(ColorBlue)
		if s.BG != ColorBlue {
			t.Errorf("got BG %v, want ColorBlue", s.BG)
		}
	})

	t.Run("chained modifiers", func(t *testing.T) {
		s := DefaultStyle().
			WithFG(ColorGreen).
			WithBG(ColorBlack).
			WithBold(true).
			WithItalic(true)

		if s.FG != ColorGreen {
			t.Error("FG not set correctly")
		}
		if s.BG != ColorBlack {
			t.Error("BG not set correctly")
		}
		if !s.Bold {
			t.Error("Bold not set")
		}
		if !s.Italic {
			t.Error("Italic not set")
		}
	})

	t.Run("Equal", func(t *testing.T) {
		s1 := DefaultStyle().WithBold(true)
		s2 := DefaultStyle().WithBold(true)
		s3 := DefaultStyle().WithItalic(true)

		if !s1.Equal(s2) {
			t.Error("identical styles should be equal")
		}
		if s1.Equal(s3) {
			t.Error("different styles should not be equal")
		}
	})
}

func TestCell(t *testing.T) {
	t.Run("EmptyCell", func(t *testing.T) {
		c := EmptyCell()
		if c.Rune != ' ' {
			t.Errorf("got rune %q, want ' '", c.Rune)
		}
		if c.Width != 1 {
			t.Errorf("got width %d, want 1", c.Width)
		}
		if !c.Empty() {
			t.Error("empty cell should return Empty() true")
		}
	})

	t.Run("NewCell", func(t *testing.T) {
		style := DefaultStyle().WithFG(ColorRed)
		c := NewCell('X', style)
		if c.Rune != 'X' {
			t.Errorf("got rune %q, want 'X'", c.Rune)
		}
		if !c.Style.Equal(style) {
			t.Error("style not preserved")
		}
	})

	t.Run("non-empty cell", func(t *testing.T) {
		c := NewCell('A', DefaultStyle())
		if c.Empty() {
			t.Error("cell with 'A' should not be empty")
		}
	})

	t.Run("Equal", func(t *testing.T) {
		c1 := NewCell('X', DefaultStyle().WithBold(true))
		c2 := NewCell('X', DefaultStyle().WithBold(true))
		c3 := NewCell('Y', DefaultStyle().WithBold(true))
		c4 := NewCell('X', DefaultStyle())

		if !c1.Equal(c2) {
			t.Error("identical cells should be equal")
		}
		if c1.Equal(c3) {
			t.Error("cells with different runes should not be equal")
		}
		if c1.Equal(c4) {
			t.Error("cells with different styles should not be equal")
		}
	})
}

func TestScreen(t *testing.T) {
	t.Run("NewScreen", func(t *testing.T) {
		s := NewScreen(80, 24)
		w, h := s.Size()
		if w != 80 || h != 24 {
			t.Errorf("got size %dx%d, want 80x24", w, h)
		}
	})

	t.Run("Set and Get", func(t *testing.T) {
		s := NewScreen(10, 10)
		style := DefaultStyle().WithFG(ColorRed)
		s.Set(5, 5, 'X', style)

		cell := s.Get(5, 5)
		if cell.Rune != 'X' {
			t.Errorf("got rune %q, want 'X'", cell.Rune)
		}
		if !cell.Style.Equal(style) {
			t.Error("style not preserved")
		}
	})

	t.Run("Set out of bounds", func(t *testing.T) {
		s := NewScreen(10, 10)
		// Should not panic
		s.Set(-1, 0, 'X', DefaultStyle())
		s.Set(0, -1, 'X', DefaultStyle())
		s.Set(100, 0, 'X', DefaultStyle())
		s.Set(0, 100, 'X', DefaultStyle())
	})

	t.Run("Get out of bounds", func(t *testing.T) {
		s := NewScreen(10, 10)
		cell := s.Get(-1, 0)
		if !cell.Empty() {
			t.Error("out of bounds Get should return empty cell")
		}
	})

	t.Run("SetString", func(t *testing.T) {
		s := NewScreen(20, 5)
		style := DefaultStyle()
		n := s.SetString(0, 0, "Hello", style)

		if n != 5 {
			t.Errorf("SetString returned %d, want 5", n)
		}

		expected := "Hello"
		for i, r := range expected {
			cell := s.Get(i, 0)
			if cell.Rune != r {
				t.Errorf("at %d: got %q, want %q", i, cell.Rune, r)
			}
		}
	})

	t.Run("SetString truncation", func(t *testing.T) {
		s := NewScreen(5, 1)
		n := s.SetString(0, 0, "Hello World", DefaultStyle())
		if n != 5 {
			t.Errorf("SetString should stop at screen width, got %d", n)
		}
	})

	t.Run("Clear", func(t *testing.T) {
		s := NewScreen(10, 10)
		s.Set(5, 5, 'X', DefaultStyle())
		s.Clear()

		cell := s.Get(5, 5)
		if !cell.Empty() {
			t.Error("cell should be empty after Clear")
		}
	})

	t.Run("Resize", func(t *testing.T) {
		s := NewScreen(10, 10)
		s.Set(5, 5, 'X', DefaultStyle())
		s.Resize(20, 20)

		w, h := s.Size()
		if w != 20 || h != 20 {
			t.Errorf("got size %dx%d, want 20x20", w, h)
		}

		// Content should be preserved
		cell := s.Get(5, 5)
		if cell.Rune != 'X' {
			t.Error("content should be preserved after resize")
		}
	})

	t.Run("FillRect", func(t *testing.T) {
		s := NewScreen(10, 10)
		style := DefaultStyle().WithBG(ColorBlue)
		s.FillRect(2, 2, 5, 3, '#', style)

		// Check inside
		cell := s.Get(4, 3)
		if cell.Rune != '#' {
			t.Error("fill rect should set rune")
		}
		if !cell.Style.Equal(style) {
			t.Error("fill rect should set style")
		}

		// Check outside
		cell = s.Get(0, 0)
		if cell.Rune == '#' {
			t.Error("outside rect should not be filled")
		}
	})

	t.Run("Box", func(t *testing.T) {
		s := NewScreen(10, 10)
		s.Box(0, 0, 5, 3, DefaultStyle())

		// Check corners
		if s.Get(0, 0).Rune != '┌' {
			t.Error("top-left corner should be ┌")
		}
		if s.Get(4, 0).Rune != '┐' {
			t.Error("top-right corner should be ┐")
		}
		if s.Get(0, 2).Rune != '└' {
			t.Error("bottom-left corner should be └")
		}
		if s.Get(4, 2).Rune != '┘' {
			t.Error("bottom-right corner should be ┘")
		}

		// Check edges
		if s.Get(2, 0).Rune != '─' {
			t.Error("top edge should be ─")
		}
		if s.Get(0, 1).Rune != '│' {
			t.Error("left edge should be │")
		}
	})

	t.Run("Cursor", func(t *testing.T) {
		s := NewScreen(10, 10)
		s.SetCursor(5, 3, true)

		x, y, visible := s.Cursor()
		if x != 5 || y != 3 {
			t.Errorf("got cursor %d,%d, want 5,3", x, y)
		}
		if !visible {
			t.Error("cursor should be visible")
		}
	})

}

func BenchmarkScreenSet(b *testing.B) {
	screen := NewScreen(80, 24)
	style := DefaultStyle()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		screen.Set(i%80, (i/80)%24, 'X', style)
	}
}
