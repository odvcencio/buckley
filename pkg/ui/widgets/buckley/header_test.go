package buckley

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/ui/backend"
	"github.com/odvcencio/buckley/pkg/ui/runtime"
)

func TestNewHeader(t *testing.T) {
	h := NewHeader()

	if h == nil {
		t.Fatal("expected non-nil Header")
	}
	if h.logo != " ● Buckley" {
		t.Errorf("expected logo ' ● Buckley', got '%s'", h.logo)
	}
	if h.modelName != "" {
		t.Errorf("expected empty model name, got '%s'", h.modelName)
	}
}

func TestHeader_SetModelName(t *testing.T) {
	h := NewHeader()

	h.SetModelName("gpt-4")

	if h.modelName != "gpt-4" {
		t.Errorf("expected model name 'gpt-4', got '%s'", h.modelName)
	}
}

func TestHeader_SetModelName_Empty(t *testing.T) {
	h := NewHeader()

	h.SetModelName("test-model")
	h.SetModelName("")

	if h.modelName != "" {
		t.Errorf("expected empty model name, got '%s'", h.modelName)
	}
}

func TestHeader_SetStyles(t *testing.T) {
	h := NewHeader()

	bg := backend.DefaultStyle().Background(backend.ColorRGB(40, 40, 40))
	logo := backend.DefaultStyle().Bold(true).Foreground(backend.ColorRGB(100, 200, 100))
	text := backend.DefaultStyle().Foreground(backend.ColorRGB(200, 200, 200))

	h.SetStyles(bg, logo, text)

	if h.bgStyle != bg {
		t.Error("bgStyle not set correctly")
	}
	if h.logoStyle != logo {
		t.Error("logoStyle not set correctly")
	}
	if h.textStyle != text {
		t.Error("textStyle not set correctly")
	}
}

func TestHeader_Measure(t *testing.T) {
	h := NewHeader()

	size := h.Measure(runtime.Constraints{
		MaxWidth:  80,
		MaxHeight: 24,
	})

	if size.Width != 80 {
		t.Errorf("expected width 80, got %d", size.Width)
	}
	if size.Height != 1 {
		t.Errorf("expected height 1, got %d", size.Height)
	}
}

func TestHeader_Measure_SmallConstraints(t *testing.T) {
	h := NewHeader()

	size := h.Measure(runtime.Constraints{
		MaxWidth:  20,
		MaxHeight: 5,
	})

	if size.Width != 20 {
		t.Errorf("expected width 20, got %d", size.Width)
	}
	if size.Height != 1 {
		t.Errorf("expected height 1, got %d", size.Height)
	}
}

func TestHeader_Render_Empty(t *testing.T) {
	h := NewHeader()
	h.Layout(runtime.Rect{X: 0, Y: 0, Width: 0, Height: 0})

	buf := runtime.NewBuffer(40, 10)
	ctx := runtime.RenderContext{Buffer: buf}

	// Should not panic with empty bounds
	h.Render(ctx)
}

func TestHeader_Render_ZeroWidth(t *testing.T) {
	h := NewHeader()
	h.Layout(runtime.Rect{X: 0, Y: 0, Width: 0, Height: 1})

	buf := runtime.NewBuffer(40, 10)
	ctx := runtime.RenderContext{Buffer: buf}

	// Should not panic with zero width
	h.Render(ctx)
}

func TestHeader_Render_ZeroHeight(t *testing.T) {
	h := NewHeader()
	h.Layout(runtime.Rect{X: 0, Y: 0, Width: 40, Height: 0})

	buf := runtime.NewBuffer(40, 10)
	ctx := runtime.RenderContext{Buffer: buf}

	// Should not panic with zero height
	h.Render(ctx)
}

func TestHeader_Render_WithLogo(t *testing.T) {
	h := NewHeader()
	h.Layout(runtime.Rect{X: 0, Y: 0, Width: 40, Height: 1})

	buf := runtime.NewBuffer(40, 1)
	ctx := runtime.RenderContext{Buffer: buf}

	h.Render(ctx)

	// Check first character is a space (from logo " ● Buckley")
	cell := buf.Get(0, 0)
	if cell.Rune != ' ' {
		t.Errorf("expected space at (0,0), got '%c'", cell.Rune)
	}

	// Check for bullet character
	cell = buf.Get(1, 0)
	if cell.Rune != '●' {
		t.Errorf("expected '●' at (1,0), got '%c'", cell.Rune)
	}
}

func TestHeader_Render_WithModelName(t *testing.T) {
	h := NewHeader()
	h.SetModelName("gpt-4")
	h.Layout(runtime.Rect{X: 0, Y: 0, Width: 40, Height: 1})

	buf := runtime.NewBuffer(40, 1)
	ctx := runtime.RenderContext{Buffer: buf}

	h.Render(ctx)

	// Model name should be on the right with trailing space
	// "gpt-4" = 5 chars, so starts at 40 - 5 = 35
	cell := buf.Get(35, 0)
	if cell.Rune != 'g' {
		t.Errorf("expected 'g' at (35,0), got '%c'", cell.Rune)
	}
}

func TestHeader_Render_ModelNameTooLong(t *testing.T) {
	h := NewHeader()
	h.SetModelName("very-long-model-name-that-exceeds-width")
	h.Layout(runtime.Rect{X: 0, Y: 0, Width: 20, Height: 1})

	buf := runtime.NewBuffer(20, 1)
	ctx := runtime.RenderContext{Buffer: buf}

	// Should not panic even with long model name
	h.Render(ctx)
}

func TestHeader_Render_WithOffset(t *testing.T) {
	h := NewHeader()
	h.Layout(runtime.Rect{X: 5, Y: 2, Width: 30, Height: 1})

	buf := runtime.NewBuffer(40, 5)
	ctx := runtime.RenderContext{Buffer: buf}

	h.Render(ctx)

	// Check logo starts at offset position
	cell := buf.Get(5, 2)
	if cell.Rune != ' ' {
		t.Errorf("expected space at (5,2), got '%c'", cell.Rune)
	}

	cell = buf.Get(6, 2)
	if cell.Rune != '●' {
		t.Errorf("expected '●' at (6,2), got '%c'", cell.Rune)
	}
}

func TestHeader_Render_BackgroundFill(t *testing.T) {
	h := NewHeader()
	h.Layout(runtime.Rect{X: 0, Y: 0, Width: 40, Height: 1})

	buf := runtime.NewBuffer(40, 1)
	ctx := runtime.RenderContext{Buffer: buf}

	// Set a specific character before rendering
	buf.Set(20, 0, 'X', backend.DefaultStyle())

	h.Render(ctx)

	// Character should be overwritten by background fill
	cell := buf.Get(20, 0)
	if cell.Rune != ' ' {
		t.Errorf("expected space at (20,0) after fill, got '%c'", cell.Rune)
	}
}

func TestHeader_Layout(t *testing.T) {
	h := NewHeader()

	h.Layout(runtime.Rect{X: 10, Y: 5, Width: 60, Height: 3})

	bounds := h.Bounds()
	if bounds.X != 10 || bounds.Y != 5 {
		t.Errorf("expected position (10, 5), got (%d, %d)", bounds.X, bounds.Y)
	}
	if bounds.Width != 60 || bounds.Height != 3 {
		t.Errorf("expected size (60, 3), got (%d, %d)", bounds.Width, bounds.Height)
	}
}
