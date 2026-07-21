// Package widgets provides concrete widget implementations for Buckley's TUI.
package widgets

import (
	"strings"

	"github.com/mattn/go-runewidth"
	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/runtime"
)

// Base provides common functionality for widgets.
// Embed this in widget structs to get default implementations.
type Base struct {
	bounds  runtime.Rect
	focused bool
}

// Layout stores the assigned bounds.
func (b *Base) Layout(bounds runtime.Rect) {
	b.bounds = bounds
}

// Bounds returns the widget's assigned bounds.
func (b *Base) Bounds() runtime.Rect {
	return b.bounds
}

// HandleMessage returns Unhandled by default.
func (b *Base) HandleMessage(msg runtime.Message) runtime.HandleResult {
	return runtime.Unhandled()
}

// CanFocus returns false by default.
func (b *Base) CanFocus() bool {
	return false
}

// Focus marks the widget as focused.
func (b *Base) Focus() {
	b.focused = true
}

// Blur marks the widget as unfocused.
func (b *Base) Blur() {
	b.focused = false
}

// IsFocused returns whether the widget is focused.
func (b *Base) IsFocused() bool {
	return b.focused
}

// FocusableBase extends Base for focusable widgets.
type FocusableBase struct {
	Base
}

// CanFocus returns true for focusable widgets.
func (f *FocusableBase) CanFocus() bool {
	return true
}

// drawText is a helper to draw text with word wrapping.
func drawText(buf *runtime.Buffer, bounds runtime.Rect, text string, style backend.Style) {
	x := bounds.X
	y := bounds.Y
	maxX := bounds.X + bounds.Width
	maxY := bounds.Y + bounds.Height

	for _, r := range text {
		if r == '\n' {
			x = bounds.X
			y++
			if y >= maxY {
				break
			}
			continue
		}

		if x >= maxX {
			x = bounds.X
			y++
			if y >= maxY {
				break
			}
		}

		buf.Set(x, y, r, style)
		x++
	}
}

// fillRect fills a rectangle with a character.
func fillRect(buf *runtime.Buffer, bounds runtime.Rect, ch rune, style backend.Style) {
	buf.Fill(bounds, ch, style)
}

// truncateString truncates a string to fit within maxWidth.
// Adds "..." if truncated.
func truncateString(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	if maxWidth <= 3 {
		return runewidth.Truncate(s, maxWidth, "")
	}
	return runewidth.Truncate(s, maxWidth-3, "") + "..."
}

func clipString(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	return runewidth.Truncate(s, maxWidth, "")
}

// padRight pads a string with spaces to reach the given width.
func padRight(s string, width int) string {
	current := runewidth.StringWidth(s)
	if current >= width {
		return s
	}
	return s + strings.Repeat(" ", width-current)
}

// centerString centers a string within the given width.
func centerString(s string, width int) string {
	current := runewidth.StringWidth(s)
	if current >= width {
		return s
	}
	left := (width - current) / 2
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", width-current-left)
}

func runeLen(s string) int {
	return len([]rune(s))
}

func displayWidth(s string) int {
	return runewidth.StringWidth(s)
}

func suffixDisplayWidth(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if displayWidth(s) <= maxWidth {
		return s
	}
	runes := []rune(s)
	width := 0
	start := len(runes)
	for start > 0 {
		runeWidth := runewidth.RuneWidth(runes[start-1])
		if runeWidth < 1 {
			runeWidth = 1
		}
		if width+runeWidth > maxWidth {
			break
		}
		width += runeWidth
		start--
	}
	return string(runes[start:])
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
