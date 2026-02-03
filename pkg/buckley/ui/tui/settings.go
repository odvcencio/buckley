package tui

import (
	"strings"

	"github.com/odvcencio/fluffyui/compositor"
	"github.com/odvcencio/fluffyui/theme"
)

// UISettings captures runtime UI preferences persisted in storage.
type UISettings struct {
	ThemeName       string
	StylesheetPath  string
	MessageMetadata string
	HighContrast    bool
	ReduceMotion    bool
	EffectsEnabled  bool
}

const (
	uiThemeSettingKey        = "ui.theme"
	uiStylesheetSettingKey   = "ui.stylesheet"
	uiMetadataSettingKey     = "ui.message_metadata"
	uiHighContrastSettingKey = "ui.high_contrast"
	uiReduceMotionSettingKey = "ui.reduce_motion"
	uiEffectsSettingKey      = "ui.effects"
)

func (s UISettings) Normalized() UISettings {
	out := s
	out.ThemeName = normalizeThemeName(out.ThemeName)
	out.MessageMetadata = normalizeMetadataSetting(out.MessageMetadata)
	out.StylesheetPath = strings.TrimSpace(out.StylesheetPath)
	return out
}

func normalizeThemeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "light":
		return "light"
	case "dark", "":
		return "dark"
	default:
		return "dark"
	}
}

func normalizeMetadataSetting(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	switch mode {
	case "always", "hover", "never":
		return mode
	default:
		return "always"
	}
}

func resolveTheme(settings UISettings) *theme.Theme {
	normalized := settings.Normalized()
	var th *theme.Theme
	switch normalized.ThemeName {
	case "light":
		th = theme.LightTheme()
	default:
		th = theme.DefaultTheme()
	}
	if !normalized.HighContrast {
		return th
	}
	return applyHighContrast(th)
}

func applyHighContrast(base *theme.Theme) *theme.Theme {
	if base == nil {
		base = theme.DefaultTheme()
	}
	clone := *base
	clone.Background = compositor.DefaultStyle().WithBG(compositor.RGB(0, 0, 0))
	clone.Surface = compositor.DefaultStyle().WithBG(compositor.RGB(0, 0, 0))
	clone.SurfaceRaised = compositor.DefaultStyle().WithBG(compositor.RGB(0, 0, 0))
	clone.SurfaceDim = compositor.DefaultStyle().WithBG(compositor.RGB(0, 0, 0))
	clone.TextPrimary = compositor.DefaultStyle().WithFG(compositor.RGB(255, 255, 255))
	clone.TextSecondary = compositor.DefaultStyle().WithFG(compositor.RGB(220, 220, 220))
	clone.TextMuted = compositor.DefaultStyle().WithFG(compositor.RGB(190, 190, 190))
	clone.TextInverse = compositor.DefaultStyle().WithFG(compositor.RGB(0, 0, 0))
	clone.Accent = compositor.DefaultStyle().WithFG(compositor.RGB(255, 255, 0)).WithBold(true)
	clone.AccentDim = compositor.DefaultStyle().WithFG(compositor.RGB(200, 200, 0))
	clone.AccentGlow = compositor.DefaultStyle().WithFG(compositor.RGB(255, 255, 120)).WithBold(true)
	clone.Border = compositor.DefaultStyle().WithFG(compositor.RGB(255, 255, 255))
	clone.BorderFocus = compositor.DefaultStyle().WithFG(compositor.RGB(255, 255, 0))
	clone.Selection = compositor.DefaultStyle().WithBG(compositor.RGB(80, 80, 80))
	clone.SearchMatch = compositor.DefaultStyle().WithBG(compositor.RGB(255, 255, 0)).WithFG(compositor.RGB(0, 0, 0))
	clone.Scrollbar = compositor.DefaultStyle().WithFG(compositor.RGB(255, 255, 255))
	clone.ScrollThumb = compositor.DefaultStyle().WithFG(compositor.RGB(255, 255, 0))
	return &clone
}

func parseSettingBool(value string, fallback bool) bool {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func boolSettingValue(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
