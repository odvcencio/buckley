package tui

import (
	"m31labs.dev/buckley/pkg/ui/backend"
	"m31labs.dev/buckley/pkg/ui/compositor"
)

func themeToBackendStyle(cs compositor.Style) backend.Style {
	style := backend.DefaultStyle().
		Foreground(compositorColorToBackend(cs.FG)).
		Background(compositorColorToBackend(cs.BG))
	return applyCompositorAttrs(style, cs)
}

func compositorColorToBackend(c compositor.Color) backend.Color {
	switch c.Mode {
	case compositor.ColorModeRGB:
		return backend.ColorRGB(
			uint8((c.Value>>16)&0xFF),
			uint8((c.Value>>8)&0xFF),
			uint8(c.Value&0xFF),
		)
	case compositor.ColorMode16, compositor.ColorMode256:
		return backend.Color(c.Value & 0xFF)
	default:
		return backend.ColorDefault
	}
}

func applyCompositorAttrs(style backend.Style, cs compositor.Style) backend.Style {
	if cs.Bold {
		style = style.Bold(true)
	}
	if cs.Italic {
		style = style.Italic(true)
	}
	if cs.Underline {
		style = style.Underline(true)
	}
	if cs.Dim {
		style = style.Dim(true)
	}
	return style
}

func blendColor(a, b backend.Color, t float64) backend.Color {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	if !a.IsRGB() || !b.IsRGB() {
		if t < 0.5 {
			return a
		}
		return b
	}

	ar, ag, ab := a.RGB()
	br, bg, bb := b.RGB()

	r := uint8(float64(ar) + (float64(br)-float64(ar))*t + 0.5)
	g := uint8(float64(ag) + (float64(bg)-float64(ag))*t + 0.5)
	bv := uint8(float64(ab) + (float64(bb)-float64(ab))*t + 0.5)
	return backend.ColorRGB(r, g, bv)
}
