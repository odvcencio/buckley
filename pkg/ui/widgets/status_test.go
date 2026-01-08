package widgets

import (
	"testing"

	"github.com/odvcencio/buckley/pkg/ui/backend"
	"github.com/odvcencio/buckley/pkg/ui/runtime"
)

func TestNewStatusBar(t *testing.T) {
	sb := NewStatusBar()

	if sb == nil {
		t.Fatal("expected non-nil StatusBar")
	}
	if sb.status != "Ready" {
		t.Errorf("expected default status 'Ready', got '%s'", sb.status)
	}
	if sb.tokens != 0 {
		t.Errorf("expected tokens 0, got %d", sb.tokens)
	}
	if sb.costCents != 0 {
		t.Errorf("expected costCents 0, got %f", sb.costCents)
	}
	if sb.contextUsed != 0 {
		t.Errorf("expected contextUsed 0, got %d", sb.contextUsed)
	}
	if sb.contextBudget != 0 {
		t.Errorf("expected contextBudget 0, got %d", sb.contextBudget)
	}
	if sb.contextWindow != 0 {
		t.Errorf("expected contextWindow 0, got %d", sb.contextWindow)
	}
	if sb.executionMode != "" {
		t.Errorf("expected executionMode empty, got %q", sb.executionMode)
	}
}

func TestStatusBar_SetStatus(t *testing.T) {
	sb := NewStatusBar()

	sb.SetStatus("Processing...")

	if sb.status != "Processing..." {
		t.Errorf("expected 'Processing...', got '%s'", sb.status)
	}
}

func TestStatusBar_SetStatus_Empty(t *testing.T) {
	sb := NewStatusBar()

	sb.SetStatus("")

	if sb.status != "" {
		t.Errorf("expected empty status, got '%s'", sb.status)
	}
}

func TestStatusBar_SetTokens(t *testing.T) {
	sb := NewStatusBar()

	sb.SetTokens(1500, 0.05)

	if sb.tokens != 1500 {
		t.Errorf("expected tokens 1500, got %d", sb.tokens)
	}
	if sb.costCents != 0.05 {
		t.Errorf("expected costCents 0.05, got %f", sb.costCents)
	}
}

func TestStatusBar_SetTokens_Large(t *testing.T) {
	sb := NewStatusBar()

	sb.SetTokens(1500000, 150.50)

	if sb.tokens != 1500000 {
		t.Errorf("expected tokens 1500000, got %d", sb.tokens)
	}
	if sb.costCents != 150.50 {
		t.Errorf("expected costCents 150.50, got %f", sb.costCents)
	}
}

func TestStatusBar_SetContextUsage(t *testing.T) {
	sb := NewStatusBar()

	sb.SetContextUsage(1200, 8000, 8192)

	if sb.contextUsed != 1200 {
		t.Errorf("expected contextUsed 1200, got %d", sb.contextUsed)
	}
	if sb.contextBudget != 8000 {
		t.Errorf("expected contextBudget 8000, got %d", sb.contextBudget)
	}
	if sb.contextWindow != 8192 {
		t.Errorf("expected contextWindow 8192, got %d", sb.contextWindow)
	}
}

func TestStatusBar_SetExecutionMode(t *testing.T) {
	sb := NewStatusBar()

	sb.SetExecutionMode("classic")

	if sb.executionMode != "classic" {
		t.Errorf("expected executionMode 'classic', got %q", sb.executionMode)
	}
}

func TestStatusBar_SetScrollPosition(t *testing.T) {
	sb := NewStatusBar()

	sb.SetScrollPosition("TOP")

	if sb.scrollPos != "TOP" {
		t.Errorf("expected scrollPos 'TOP', got '%s'", sb.scrollPos)
	}

	sb.SetScrollPosition("50%")

	if sb.scrollPos != "50%" {
		t.Errorf("expected scrollPos '50%%', got '%s'", sb.scrollPos)
	}

	sb.SetScrollPosition("END")

	if sb.scrollPos != "END" {
		t.Errorf("expected scrollPos 'END', got '%s'", sb.scrollPos)
	}
}

func TestStatusBar_SetStyles(t *testing.T) {
	sb := NewStatusBar()

	bg := backend.DefaultStyle().Background(backend.ColorRGB(50, 50, 50))
	text := backend.DefaultStyle().Foreground(backend.ColorRGB(200, 200, 200))

	sb.SetStyles(bg, text)

	if sb.bgStyle != bg {
		t.Error("bgStyle not set correctly")
	}
	if sb.textStyle != text {
		t.Error("textStyle not set correctly")
	}
}

func TestStatusBar_Measure(t *testing.T) {
	sb := NewStatusBar()

	size := sb.Measure(runtime.Constraints{
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

func TestStatusBar_Measure_SmallConstraints(t *testing.T) {
	sb := NewStatusBar()

	size := sb.Measure(runtime.Constraints{
		MaxWidth:  20,
		MaxHeight: 10,
	})

	if size.Width != 20 {
		t.Errorf("expected width 20, got %d", size.Width)
	}
	if size.Height != 1 {
		t.Errorf("expected height 1, got %d", size.Height)
	}
}

func TestStatusBar_Render_Empty(t *testing.T) {
	sb := NewStatusBar()
	sb.Layout(runtime.Rect{X: 0, Y: 0, Width: 0, Height: 0})

	buf := runtime.NewBuffer(40, 10)
	ctx := runtime.RenderContext{Buffer: buf}

	// Should not panic with empty bounds
	sb.Render(ctx)
}

func TestStatusBar_Render_ZeroWidth(t *testing.T) {
	sb := NewStatusBar()
	sb.Layout(runtime.Rect{X: 0, Y: 0, Width: 0, Height: 1})

	buf := runtime.NewBuffer(40, 10)
	ctx := runtime.RenderContext{Buffer: buf}

	// Should not panic
	sb.Render(ctx)
}

func TestStatusBar_Render_ZeroHeight(t *testing.T) {
	sb := NewStatusBar()
	sb.Layout(runtime.Rect{X: 0, Y: 0, Width: 40, Height: 0})

	buf := runtime.NewBuffer(40, 10)
	ctx := runtime.RenderContext{Buffer: buf}

	// Should not panic
	sb.Render(ctx)
}

func TestStatusBar_Render_WithStatus(t *testing.T) {
	sb := NewStatusBar()
	sb.SetStatus("Ready")
	sb.Layout(runtime.Rect{X: 0, Y: 0, Width: 40, Height: 1})

	buf := runtime.NewBuffer(40, 1)
	ctx := runtime.RenderContext{Buffer: buf}

	sb.Render(ctx)

	// Status should start with a space and then the status text
	// " Ready"
	cell := buf.Get(0, 0)
	if cell.Rune != ' ' {
		t.Errorf("expected space at (0,0), got '%c'", cell.Rune)
	}
	cell = buf.Get(1, 0)
	if cell.Rune != 'R' {
		t.Errorf("expected 'R' at (1,0), got '%c'", cell.Rune)
	}
}

func TestStatusBar_Render_WithTokens(t *testing.T) {
	sb := NewStatusBar()
	sb.SetStatus("OK")
	sb.SetTokens(1000, 0)
	sb.Layout(runtime.Rect{X: 0, Y: 0, Width: 40, Height: 1})

	buf := runtime.NewBuffer(40, 1)
	ctx := runtime.RenderContext{Buffer: buf}

	sb.Render(ctx)

	// Token count should be on the right side
	// "1.0K " at the end
	// Should not panic
}

func TestStatusBar_Render_WithTokensAndCost(t *testing.T) {
	sb := NewStatusBar()
	sb.SetStatus("OK")
	sb.SetTokens(5000, 50)
	sb.Layout(runtime.Rect{X: 0, Y: 0, Width: 60, Height: 1})

	buf := runtime.NewBuffer(60, 1)
	ctx := runtime.RenderContext{Buffer: buf}

	sb.Render(ctx)

	// Token count and cost should be on the right
	// "5.0K · $0.50 "
}

func TestStatusBar_Render_WithScrollPosition(t *testing.T) {
	sb := NewStatusBar()
	sb.SetStatus("OK")
	sb.SetScrollPosition("TOP")
	sb.Layout(runtime.Rect{X: 0, Y: 0, Width: 60, Height: 1})

	buf := runtime.NewBuffer(60, 1)
	ctx := runtime.RenderContext{Buffer: buf}

	sb.Render(ctx)

	// Scroll position should be in center
	// Just verify no panic
}

func TestStatusBar_Render_WithOffset(t *testing.T) {
	sb := NewStatusBar()
	sb.SetStatus("Test")
	sb.Layout(runtime.Rect{X: 5, Y: 3, Width: 30, Height: 1})

	buf := runtime.NewBuffer(40, 5)
	ctx := runtime.RenderContext{Buffer: buf}

	sb.Render(ctx)

	// Status should be at offset position
	cell := buf.Get(5, 3)
	if cell.Rune != ' ' {
		t.Errorf("expected space at (5,3), got '%c'", cell.Rune)
	}
	cell = buf.Get(6, 3)
	if cell.Rune != 'T' {
		t.Errorf("expected 'T' at (6,3), got '%c'", cell.Rune)
	}
}

func TestStatusBar_Render_BackgroundFill(t *testing.T) {
	sb := NewStatusBar()
	sb.Layout(runtime.Rect{X: 0, Y: 0, Width: 40, Height: 1})

	buf := runtime.NewBuffer(40, 1)
	ctx := runtime.RenderContext{Buffer: buf}

	// Set a specific character before rendering
	buf.Set(15, 0, 'X', backend.DefaultStyle())

	sb.Render(ctx)

	// Character should be overwritten by background fill
	cell := buf.Get(15, 0)
	if cell.Rune != ' ' {
		t.Errorf("expected space at (15,0) after fill, got '%c'", cell.Rune)
	}
}

func TestStatusBar_Render_NarrowWidth(t *testing.T) {
	sb := NewStatusBar()
	sb.SetStatus("Very long status message")
	sb.SetTokens(100000, 100)
	sb.SetScrollPosition("50%")
	sb.Layout(runtime.Rect{X: 0, Y: 0, Width: 15, Height: 1})

	buf := runtime.NewBuffer(15, 1)
	ctx := runtime.RenderContext{Buffer: buf}

	// Should not panic even with very narrow width
	sb.Render(ctx)
}

func TestStatusBar_Layout(t *testing.T) {
	sb := NewStatusBar()

	sb.Layout(runtime.Rect{X: 0, Y: 10, Width: 80, Height: 1})

	bounds := sb.Bounds()
	if bounds.X != 0 || bounds.Y != 10 {
		t.Errorf("expected position (0, 10), got (%d, %d)", bounds.X, bounds.Y)
	}
	if bounds.Width != 80 || bounds.Height != 1 {
		t.Errorf("expected size (80, 1), got (%d, %d)", bounds.Width, bounds.Height)
	}
}

// Test formatTokens helper function
func TestFormatTokens(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{100, "100"},
		{999, "999"},
		{1000, "1.0K"},
		{1500, "1.5K"},
		{10000, "10.0K"},
		{100000, "100.0K"},
		{999999, "999.9K"},
		{1000000, "1.0M"},
		{1500000, "1.5M"},
		{10000000, "10.0M"},
	}

	for _, tc := range tests {
		got := formatTokens(tc.input)
		if got != tc.expected {
			t.Errorf("formatTokens(%d) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// Test formatCost helper function
func TestFormatCost(t *testing.T) {
	tests := []struct {
		input    float64
		expected string
	}{
		{0, "0.00"},
		{1, "0.01"},
		{5, "0.05"},
		{10, "0.10"},
		{50, "0.50"},
		{99, "0.99"},
		{100, "1.00"},
		{150, "1.50"},
		{1000, "10.00"},
	}

	for _, tc := range tests {
		got := formatCost(tc.input)
		if got != tc.expected {
			t.Errorf("formatCost(%v) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// Test padZero helper function
func TestPadZero(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "00"},
		{1, "01"},
		{5, "05"},
		{9, "09"},
		{10, "10"},
		{50, "50"},
		{99, "99"},
	}

	for _, tc := range tests {
		got := padZero(tc.input)
		if got != tc.expected {
			t.Errorf("padZero(%d) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// Test itoa helper function
func TestItoa(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{5, "5"},
		{10, "10"},
		{100, "100"},
		{1234, "1234"},
		{999999, "999999"},
		{-1, "-1"},
		{-100, "-100"},
	}

	for _, tc := range tests {
		got := itoa(tc.input)
		if got != tc.expected {
			t.Errorf("itoa(%d) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}
