package widgets

import (
	"strings"

	"github.com/mattn/go-runewidth"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/style"
)

func textWidth(s string) int {
	return runewidth.StringWidth(s)
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
	return runewidth.Truncate(s, maxWidth, "...")
}

func padRight(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) >= width {
		return runewidth.Truncate(s, width, "")
	}
	padding := width - runewidth.StringWidth(s)
	return s + strings.Repeat(" ", padding)
}

func writePadded(buf *runtime.Buffer, x, y, width int, text string, style backend.Style) {
	if buf == nil || width <= 0 {
		return
	}
	if x < 0 {
		buf.SetString(x, y, padRight(text, width), style)
		return
	}
	text = runewidth.Truncate(text, width, "")
	buf.SetString(x, y, text, style)
	if pad := width - runewidth.StringWidth(text); pad > 0 {
		buf.Fill(runtime.Rect{X: x + runewidth.StringWidth(text), Y: y, Width: pad, Height: 1}, ' ', style)
	}
}

func resolveBaseStyle(ctx runtime.RenderContext, widget runtime.Widget, fallback backend.Style, fallbackSet bool) backend.Style {
	resolved := ctx.ResolveStyle(widget)
	if resolved.IsZero() {
		return fallback
	}
	final := resolved
	if fallbackSet {
		final = final.Merge(style.FromBackend(fallback))
	}
	return final.ToBackend()
}

func mergeBackendStyles(base backend.Style, override backend.Style) backend.Style {
	final := style.FromBackend(base).Merge(style.FromBackend(override))
	return final.ToBackend()
}
