package widgets

import (
	"slices"
	"testing"

	"m31labs.dev/buckley/pkg/ui/filepicker"
	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/terminal"
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

func TestFilePickerWidget_HandleBackspace_Unicode(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	fp.Activate(0)
	fp.AppendQuery('模')
	fp.AppendQuery('型')
	w := NewFilePickerWidget(fp)

	result := w.HandleMessage(runtime.KeyMsg{Key: terminal.KeyBackspace})
	if !result.Handled {
		t.Error("Backspace should be handled")
	}
	if fp.Query() != "模" {
		t.Errorf("expected query '模', got %q", fp.Query())
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

func TestFilePickerWidget_Render_QueryCursorUsesRuneColumns(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	fp.Activate(0)
	fp.AppendQuery('模')
	fp.AppendQuery('型')
	w := NewFilePickerWidget(fp)
	w.Focus()
	w.Layout(runtime.Rect{X: 0, Y: 0, Width: 80, Height: 24})

	buf := runtime.NewBuffer(80, 24)
	w.Render(runtime.RenderContext{Buffer: buf})

	b := w.Bounds()
	y := b.Y + 1
	x := b.X + 2
	assertCellRune(t, buf, x, y, '@')
	assertCellRune(t, buf, x+1, y, ' ')
	assertCellRune(t, buf, x+2, y, '模')
	assertCellRune(t, buf, x+3, y, ' ')
	assertCellRune(t, buf, x+4, y, '型')
	assertCellRune(t, buf, x+5, y, ' ')
	assertCellRune(t, buf, x+6, y, '█')
}

func TestFilePickerWidget_RenderMatch_UsesRuneColumns(t *testing.T) {
	fp := filepicker.NewFilePicker("/tmp")
	w := NewFilePickerWidget(fp)
	buf := runtime.NewBuffer(12, 1)

	w.renderMatch(buf, 0, 0, "a模型", []int{1, 2}, w.textStyle, false)

	assertCellRune(t, buf, 0, 0, 'a')
	assertCellRune(t, buf, 1, 0, '模')
	assertCellRune(t, buf, 2, 0, '型')
	if buf.Get(1, 0).Style != w.highlightStyle {
		t.Fatal("expected first unicode rune to use highlight style")
	}
	if buf.Get(2, 0).Style != w.highlightStyle {
		t.Fatal("expected second unicode rune to use highlight style")
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

func TestTruncateFilePickerPath(t *testing.T) {
	tests := []struct {
		name          string
		path          string
		highlights    []int
		maxWidth      int
		expectedPath  string
		expectedMarks []int
	}{
		{
			name:          "fits",
			path:          "pkg/模型.go",
			highlights:    []int{4, 5},
			maxWidth:      16,
			expectedPath:  "pkg/模型.go",
			expectedMarks: []int{4, 5},
		},
		{
			name:          "unicode filename only",
			path:          "pkg/界面/模型文件.go",
			highlights:    []int{7, 9},
			maxWidth:      8,
			expectedPath:  "模型文件.go",
			expectedMarks: []int{0, 2},
		},
		{
			name:          "unicode dir suffix",
			path:          "pkg/界面/模型文件.go",
			highlights:    []int{5, 7, 9},
			maxWidth:      12,
			expectedPath:  "...面/模型文件.go",
			expectedMarks: []int{3, 5, 7},
		},
		{
			name:          "long unicode filename",
			path:          "pkg/模型超长文件名.go",
			highlights:    []int{4, 8, 10},
			maxWidth:      8,
			expectedPath:  "模型...",
			expectedMarks: []int{0, 4},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path, marks := truncateFilePickerPath(tt.path, tt.highlights, tt.maxWidth)
			if path != tt.expectedPath {
				t.Fatalf("path = %q, want %q", path, tt.expectedPath)
			}
			if !slices.Equal(marks, tt.expectedMarks) {
				t.Fatalf("marks = %v, want %v", marks, tt.expectedMarks)
			}
		})
	}
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

func assertCellRune(t *testing.T, buf *runtime.Buffer, x, y int, want rune) {
	t.Helper()
	if got := buf.Get(x, y).Rune; got != want {
		t.Fatalf("cell (%d,%d) = %q, want %q", x, y, got, want)
	}
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
		{1000000, "1,000,000"},
	}

	for _, tc := range tests {
		got := formatFileCount(tc.input)
		if got != tc.expected {
			t.Errorf("formatFileCount(%d) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}
