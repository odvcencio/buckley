package widgets

import (
	"testing"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/buckley/pkg/buckley/ui/filepicker"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/terminal"
)

func TestFilePickerWidget_Measure(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	w := NewFilePickerWidget(fp)

	size := w.Measure(runtime.Constraints{
		MaxWidth:  80,
		MaxHeight: 24,
	})

	if size.Width != 60 {
		t.Errorf("expected width 60, got %d", size.Width)
	}
	if size.Height != 13 {
		t.Errorf("expected height 13, got %d", size.Height)
	}
}

func TestFilePickerWidget_Layout(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	w := NewFilePickerWidget(fp)

	// Layout in a larger area - should center
	w.Layout(runtime.Rect{X: 0, Y: 0, Width: 100, Height: 30})

	bounds := w.Bounds()

	// Should be centered
	expectedX := (100 - 60) / 2
	expectedY := (30 - 13) / 2

	if bounds.X != expectedX {
		t.Errorf("expected X %d, got %d", expectedX, bounds.X)
	}
	if bounds.Y != expectedY {
		t.Errorf("expected Y %d, got %d", expectedY, bounds.Y)
	}
}

func TestFilePickerWidget_HandleEscape(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	fp.Activate(0)
	w := NewFilePickerWidget(fp)

	msg := runtime.KeyMsg{Key: terminal.KeyEscape}
	result := w.HandleMessage(msg)

	if !result.Handled {
		t.Error("Escape should be handled")
	}

	// Should emit PopOverlay command
	if len(result.Commands) == 0 {
		t.Error("expected PopOverlay command")
	}

	_, ok := result.Commands[0].(runtime.PopOverlay)
	if !ok {
		t.Errorf("expected PopOverlay, got %T", result.Commands[0])
	}
}

func TestFilePickerWidget_HandleUpDown(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	fp.Activate(0)
	w := NewFilePickerWidget(fp)

	// Up should be handled
	result := w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyUp})
	if !result.Handled {
		t.Error("Up should be handled")
	}

	// Down should be handled
	result = w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyDown})
	if !result.Handled {
		t.Error("Down should be handled")
	}
}

func TestFilePickerWidget_HandleTyping(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	fp.Activate(0)
	w := NewFilePickerWidget(fp)

	// Type a character
	result := w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyRune, Rune: 'a'})
	if !result.Handled {
		t.Error("Rune should be handled")
	}

	if fp.Query() != "a" {
		t.Errorf("expected query 'a', got '%s'", fp.Query())
	}
}

func TestFilePickerWidget_HandleBackspace(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	fp.Activate(0)
	fp.AppendQuery('a')
	fp.AppendQuery('b')
	w := NewFilePickerWidget(fp)

	// Backspace should remove last character
	result := w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyBackspace})
	if !result.Handled {
		t.Error("Backspace should be handled")
	}
	if fp.Query() != "a" {
		t.Errorf("expected query 'a', got '%s'", fp.Query())
	}

	// Another backspace
	w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyBackspace})
	if fp.Query() != "" {
		t.Errorf("expected empty query, got '%s'", fp.Query())
	}

	// Backspace on empty should pop overlay
	result = w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyBackspace})
	if len(result.Commands) == 0 {
		t.Error("expected PopOverlay on empty backspace")
	}
}

func TestFilePickerWidget_Render(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	fp.Activate(0)
	w := NewFilePickerWidget(fp)
	w.Focus()
	w.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 24})

	// Create buffer and render
	buf := runtime.NewBuffer(80, 24)
	ctx := runtime.RenderContext{
		Buffer: buf,
	}

	// Should not panic
	w.Render(ctx)

	// Check for border corners
	cell := buf.Get(w.Bounds().X, w.Bounds().Y)
	if cell.Rune != '╭' {
		t.Errorf("expected top-left corner '╭', got '%c'", cell.Rune)
	}
}

func TestFilePickerWidget_Render_SmallBounds(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	w := NewFilePickerWidget(fp)
	w.Layout(runtime.Rect{X: 0, Y: 0, Width: 5, Height: 3})

	buf := runtime.NewBuffer(10, 5)
	ctx := runtime.RenderContext{Buffer: buf}

	// Should not panic with too-small bounds
	w.Render(ctx)
}

func TestFilePickerWidget_SetStyles(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	w := NewFilePickerWidget(fp)

	bg := backend.DefaultStyle()
	border := backend.DefaultStyle().Bold(true)
	text := backend.DefaultStyle()
	selected := backend.DefaultStyle().Reverse(true)
	highlight := backend.DefaultStyle().Bold(true)
	query := backend.DefaultStyle().Bold(true)

	w.SetStyles(bg, border, text, selected, highlight, query)

	// No panic means success
}

func TestFilePickerWidget_HandleEnter_NoSelection(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	fp.Activate(0)
	w := NewFilePickerWidget(fp)

	// Enter with no matches should just be handled
	result := w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyEnter})
	if !result.Handled {
		t.Error("Enter should be handled")
	}
}

func TestFilePickerWidget_HandleUnknownKey(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	fp.Activate(0)
	w := NewFilePickerWidget(fp)

	// Tab key should not be handled
	result := w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyTab})
	if result.Handled {
		t.Error("Tab should not be handled")
	}
}

func TestFilePickerWidget_HandleNonKeyMsg(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	w := NewFilePickerWidget(fp)

	// Non-key message should not be handled
	result := w.HandleMessage(runtime.ResizeMsg{Width: 80, Height: 24})
	if result.Handled {
		t.Error("ResizeMsg should not be handled")
	}
}

func TestFilePickerWidget_Measure_SmallConstraints(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	w := NewFilePickerWidget(fp)

	size := w.Measure(runtime.Constraints{
		MaxWidth:  30,
		MaxHeight: 5,
	})

	if size.Width != 30 {
		t.Errorf("expected width 30, got %d", size.Width)
	}
	if size.Height != 5 {
		t.Errorf("expected height 5, got %d", size.Height)
	}
}

// Test formatCount helper function
func TestFormatCount(t *testing.T) {
	tests := []struct {
		total    int
		shown    int
		expected string
	}{
		{100, 0, "no matches"},
		{100, 100, "100 files"},
		{100, 10, "10/100"},
		{1000, 50, "50/1,000"},
		{5000, 5000, "5,000 files"},
	}

	for _, tc := range tests {
		got := formatCount(tc.total, tc.shown)
		if got != tc.expected {
			t.Errorf("formatCount(%d, %d) = %q, want %q", tc.total, tc.shown, got, tc.expected)
		}
	}
}

// Test formatFileCount helper function
func TestFormatFileCount(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{100, "100"},
		{999, "999"},
		{1000, "1,000"},
		{1234, "1,234"},
		{10000, "10,000"},
		{100000, "100,000"},
	}

	for _, tc := range tests {
		got := formatFileCount(tc.input)
		if got != tc.expected {
			t.Errorf("formatFileCount(%d) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// Test intToStr helper function
func TestIntToStr(t *testing.T) {
	tests := []struct {
		input    int
		expected string
	}{
		{0, "0"},
		{1, "1"},
		{10, "10"},
		{123, "123"},
		{9999, "9999"},
	}

	for _, tc := range tests {
		got := intToStr(tc.input)
		if got != tc.expected {
			t.Errorf("intToStr(%d) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// Test padLeftStr helper function
func TestPadLeftStr(t *testing.T) {
	tests := []struct {
		s        string
		length   int
		pad      byte
		expected string
	}{
		{"1", 3, '0', "001"},
		{"12", 3, '0', "012"},
		{"123", 3, '0', "123"},
		{"1234", 3, '0', "1234"},
		{"", 3, '0', "000"},
		{"x", 5, ' ', "    x"},
	}

	for _, tc := range tests {
		got := padLeftStr(tc.s, tc.length, tc.pad)
		if got != tc.expected {
			t.Errorf("padLeftStr(%q, %d, %c) = %q, want %q", tc.s, tc.length, tc.pad, got, tc.expected)
		}
	}
}
