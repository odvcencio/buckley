package tui

import (
	"testing"

	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/compositor"
)

func TestThemeToBackendStyle_ConvertsColorsAndAttributes(t *testing.T) {
	style := themeToBackendStyle(compositor.DefaultStyle().
		WithFG(compositor.RGB(10, 20, 30)).
		WithBG(compositor.Color256(42)).
		WithBold(true).
		WithItalic(true).
		WithUnderline(true).
		WithDim(true))

	fg, bg, attrs := style.Decompose()
	if fg != backend.ColorRGB(10, 20, 30) {
		t.Fatalf("foreground = %v, want RGB(10,20,30)", fg)
	}
	if bg != backend.Color(42) {
		t.Fatalf("background = %v, want palette 42", bg)
	}
	for _, attr := range []backend.AttrMask{
		backend.AttrBold,
		backend.AttrItalic,
		backend.AttrUnderline,
		backend.AttrDim,
	} {
		if attrs&attr == 0 {
			t.Fatalf("attributes %b missing %b", attrs, attr)
		}
	}
}

func TestCompositorColorToBackend_DefaultAndNone(t *testing.T) {
	for _, color := range []compositor.Color{compositor.ColorDefault, compositor.ColorNone} {
		if got := compositorColorToBackend(color); got != backend.ColorDefault {
			t.Fatalf("compositorColorToBackend(%+v) = %v, want default", color, got)
		}
	}
}

func TestBlendColor_RGBAndPaletteFallback(t *testing.T) {
	black := backend.ColorRGB(0, 0, 0)
	white := backend.ColorRGB(100, 150, 200)

	mid := blendColor(black, white, 0.5)
	r, g, b := mid.RGB()
	if r != 50 || g != 75 || b != 100 {
		t.Fatalf("mid blend RGB = (%d,%d,%d), want (50,75,100)", r, g, b)
	}
	if got := blendColor(backend.ColorRed, backend.ColorBlue, 0.49); got != backend.ColorRed {
		t.Fatalf("palette low blend = %v, want red", got)
	}
	if got := blendColor(backend.ColorRed, backend.ColorBlue, 0.5); got != backend.ColorBlue {
		t.Fatalf("palette high blend = %v, want blue", got)
	}
}
