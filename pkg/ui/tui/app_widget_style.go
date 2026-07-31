package tui

import (
	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/compositor"
	"m31labs.dev/fluffyui/theme"
)

func defaultBuckleyTheme() *theme.Theme {
	th := theme.DefaultTheme()
	style := compositor.DefaultStyle
	th.Background = style().WithBG(compositor.RGB(10, 12, 15))
	th.Surface = style().WithBG(compositor.RGB(16, 19, 24))
	th.SurfaceRaised = style().WithBG(compositor.RGB(23, 27, 34))
	th.SurfaceDim = style().WithBG(compositor.RGB(7, 9, 12))
	th.TextPrimary = style().WithFG(compositor.RGB(220, 226, 234))
	th.TextSecondary = style().WithFG(compositor.RGB(151, 161, 175))
	th.TextMuted = style().WithFG(compositor.RGB(94, 104, 118))
	th.TextInverse = style().WithFG(compositor.RGB(10, 12, 15))
	th.Accent = style().WithFG(compositor.RGB(122, 162, 247))
	th.AccentDim = style().WithFG(compositor.RGB(82, 112, 173))
	th.AccentGlow = style().WithFG(compositor.RGB(160, 190, 255)).WithBold(true)
	th.Success = style().WithFG(compositor.RGB(158, 206, 106))
	th.Warning = style().WithFG(compositor.RGB(224, 175, 104))
	th.Error = style().WithFG(compositor.RGB(247, 118, 142))
	th.Info = style().WithFG(compositor.RGB(125, 207, 255))
	th.User = th.Info
	th.Assistant = th.TextPrimary
	th.System = th.TextSecondary.WithItalic(true)
	th.Tool = style().WithFG(compositor.RGB(187, 154, 247))
	th.Thinking = th.TextMuted.WithItalic(true)
	th.Border = style().WithFG(compositor.RGB(43, 49, 59))
	th.BorderFocus = th.Accent
	th.Selection = style().WithBG(compositor.RGB(40, 52, 76))
	th.SearchMatch = style().WithBG(compositor.RGB(89, 67, 28)).WithFG(compositor.RGB(238, 242, 247))
	th.Scrollbar = th.Border
	th.ScrollThumb = th.TextMuted
	th.ModeNormal = th.TextSecondary
	th.ModeShell = th.Success.WithBold(true)
	th.ModeEnv = th.Info.WithBold(true)
	th.ModeSearch = th.Warning.WithBold(true)
	th.Logo = th.AccentGlow
	th.Spinner = th.Accent
	return th
}

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
