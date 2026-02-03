package tui

import (
	"strings"
	"time"

	"github.com/odvcencio/fluffyui/markdown"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/state"
	"github.com/odvcencio/fluffyui/style"
	"github.com/odvcencio/fluffyui/theme"
)

type stylesheetUpdateMsg struct {
	path  string
	sheet *style.Stylesheet
	err   error
}

func (r *Runner) bindSettingsSignals() {
	if r == nil || r.app == nil || r.state == nil {
		return
	}
	r.settingsSubs.Clear()
	r.settingsSubs.SetScheduler(r.app.Services().Scheduler())

	if r.state.ThemeName != nil {
		r.settingsSubs.Observe(r.state.ThemeName, r.applyThemeFromState)
	}
	if r.state.HighContrast != nil {
		r.settingsSubs.Observe(r.state.HighContrast, r.applyThemeFromState)
	}
	if r.state.StylesheetPath != nil {
		r.settingsSubs.Observe(r.state.StylesheetPath, r.applyStylesheetFromState)
	}
	if r.state.ReduceMotion != nil {
		r.settingsSubs.Observe(r.state.ReduceMotion, r.applyMotionSettings)
	}
	if r.state.EffectsEnabled != nil {
		r.settingsSubs.Observe(r.state.EffectsEnabled, r.applyMotionSettings)
	}

	r.applyThemeFromState()
	r.applyStylesheetFromState()
	r.applyMotionSettings()
}

func (r *Runner) applySettings(settings UISettings) {
	if r == nil || r.state == nil {
		return
	}
	settings = settings.Normalized()
	state.Batch(func() {
		if r.state.ThemeName != nil {
			r.state.ThemeName.Set(settings.ThemeName)
		}
		if r.state.StylesheetPath != nil {
			r.state.StylesheetPath.Set(settings.StylesheetPath)
		}
		if r.state.MessageMetadata != nil {
			r.state.MessageMetadata.Set(settings.MessageMetadata)
		}
		if r.state.HighContrast != nil {
			r.state.HighContrast.Set(settings.HighContrast)
		}
		if r.state.ReduceMotion != nil {
			r.state.ReduceMotion.Set(settings.ReduceMotion)
		}
		if r.state.EffectsEnabled != nil {
			r.state.EffectsEnabled.Set(settings.EffectsEnabled)
		}
	})
	if r.onSettings != nil {
		r.onSettings(settings)
	}
}

func (r *Runner) applyThemeFromState() {
	if r == nil || r.state == nil {
		return
	}
	settings := UISettings{
		ThemeName:       getSignalString(r.state.ThemeName),
		MessageMetadata: getSignalString(r.state.MessageMetadata),
		HighContrast:    getSignalBool(r.state.HighContrast),
	}
	th := resolveTheme(settings)
	if th == nil {
		th = theme.DefaultTheme()
	}
	r.applyTheme(th)
	r.applyStylesheetFromState()
}

func (r *Runner) applyTheme(th *theme.Theme) {
	if r == nil || th == nil {
		return
	}
	r.theme = th
	if r.styleCache != nil {
		r.styleCache.Clear()
	}
	// Header
	if r.header != nil {
		r.header.SetStyles(
			r.styleCache.Get(th.Surface),
			r.styleCache.Get(th.Logo),
			r.styleCache.Get(th.TextSecondary),
		)
	}
	// ChatView
	if r.chatView != nil {
		r.chatView.SetStyles(
			r.styleCache.Get(th.User),
			r.styleCache.Get(th.Assistant),
			r.styleCache.Get(th.System),
			r.styleCache.Get(th.Tool),
			r.styleCache.Get(th.Thinking),
		)
		r.chatView.SetMetadataStyle(r.styleCache.Get(th.TextMuted))
		r.chatView.SetUIStyles(
			r.styleCache.Get(th.Scrollbar),
			r.styleCache.Get(th.ScrollThumb),
			r.styleCache.Get(th.Selection),
			r.styleCache.Get(th.SearchMatch),
			r.styleCache.Get(th.Background),
		)
		mdRenderer := markdown.NewRenderer(th)
		r.chatView.SetMarkdownRenderer(mdRenderer, r.styleCache.Get(mdRenderer.CodeBlockBackground()))
	}
	// InputArea
	if r.inputArea != nil {
		r.inputArea.SetStyles(
			r.styleCache.Get(th.SurfaceRaised),
			r.styleCache.Get(th.TextPrimary),
			r.styleCache.Get(th.Border),
		)
		r.inputArea.SetModeStyles(
			r.styleCache.Get(th.ModeNormal),
			r.styleCache.Get(th.ModeShell),
			r.styleCache.Get(th.ModeEnv),
			r.styleCache.Get(th.ModeSearch),
		)
	}
	// StatusBar
	if r.statusBar != nil {
		r.statusBar.SetStyles(
			r.styleCache.Get(th.Surface),
			r.styleCache.Get(th.TextMuted),
			r.styleCache.Get(th.Accent),
		)
	}
	// Sidebar
	if r.sidebar != nil {
		r.sidebar.SetStyles(
			r.styleCache.Get(th.Border),
			r.styleCache.Get(th.TextSecondary),
			r.styleCache.Get(th.TextPrimary),
			r.styleCache.Get(th.Accent),
			r.styleCache.Get(th.TextMuted),
			r.styleCache.Get(th.Surface),
		)
		r.sidebar.SetProgressEdgeStyle(r.styleCache.Get(th.AccentGlow))
		r.sidebar.SetStatusStyles(
			r.styleCache.Get(th.Success),
			r.styleCache.Get(th.ElectricBlue).Bold(true),
			r.styleCache.Get(th.TextMuted),
			r.styleCache.Get(th.Coral),
		)
		r.sidebar.SetContextStyles(
			r.styleCache.Get(th.Teal),
			r.styleCache.Get(th.Accent),
			r.styleCache.Get(th.Coral),
			r.styleCache.Get(th.TextMuted),
		)
		r.sidebar.SetSpinnerStyle(r.styleCache.Get(th.ElectricBlue))
	}
	// Toasts
	if r.toastStack != nil {
		r.toastStack.SetStyles(
			r.styleCache.Get(th.SurfaceRaised),
			r.styleCache.Get(th.TextPrimary),
			r.styleCache.Get(th.Info),
			r.styleCache.Get(th.Success),
			r.styleCache.Get(th.Warning),
			r.styleCache.Get(th.Error),
		)
	}
	// Palette
	if r.modelPalette != nil {
		r.modelPalette.SetStyles(
			r.styleCache.Get(th.SurfaceRaised),
			r.styleCache.Get(th.Border),
			r.styleCache.Get(th.TextPrimary),
			r.styleCache.Get(th.TextPrimary),
			r.styleCache.Get(th.TextSecondary),
			r.styleCache.Get(th.Accent),
			r.styleCache.Get(th.TextMuted),
		)
	}
	if r.app != nil {
		r.app.SetTheme(th)
	}
}

func (r *Runner) applyStylesheetFromState() {
	if r == nil || r.state == nil || r.app == nil {
		return
	}
	path := strings.TrimSpace(getSignalString(r.state.StylesheetPath))
	r.applyStylesheetPath(path)
}

func (r *Runner) applyStylesheetPath(path string) {
	if r == nil || r.app == nil {
		return
	}
	if r.styleWatchStop != nil {
		r.styleWatchStop()
		r.styleWatchStop = nil
	}
	path = strings.TrimSpace(path)
	if path == "" {
		if r.theme != nil {
			r.app.SetTheme(r.theme)
		}
		return
	}
	sheet, err := style.ParseFile(path)
	r.handleStylesheetUpdate(stylesheetUpdateMsg{path: path, sheet: sheet, err: err})
	r.styleWatchStop = style.WatchFile(path, time.Second, func(updated *style.Stylesheet, watchErr error) {
		if r.app == nil {
			return
		}
		r.app.Services().Post(runtime.CustomMsg{Value: stylesheetUpdateMsg{path: path, sheet: updated, err: watchErr}})
	})
}

func (r *Runner) handleStylesheetUpdate(msg stylesheetUpdateMsg) {
	if r == nil || r.app == nil {
		return
	}
	currentPath := ""
	if r.state != nil && r.state.StylesheetPath != nil {
		currentPath = strings.TrimSpace(r.state.StylesheetPath.Get())
	}
	if msg.path != "" && currentPath != "" && msg.path != currentPath {
		return
	}
	if msg.err != nil {
		if r.statusService != nil {
			r.statusService.SetStatusOverride("Stylesheet error: "+msg.err.Error(), 3*time.Second)
		}
		if r.theme != nil {
			r.app.SetTheme(r.theme)
		}
		return
	}
	if r.theme == nil {
		r.theme = theme.DefaultTheme()
	}
	base := theme.Stylesheet(r.theme)
	if msg.sheet != nil {
		r.app.SetStylesheet(style.Merge(base, msg.sheet))
		return
	}
	r.app.SetStylesheet(base)
}

func (r *Runner) applyMotionSettings() {
	if r == nil || r.state == nil {
		return
	}
	reduce := getSignalBool(r.state.ReduceMotion)
	effects := true
	if r.state.EffectsEnabled != nil {
		effects = r.state.EffectsEnabled.Get()
	}
	if r.toastStack != nil {
		r.toastStack.SetAnimationsEnabled(!reduce && effects)
	}
	if r.app != nil {
		r.app.Invalidate()
	}
}

func getSignalString(sig *state.Signal[string]) string {
	if sig == nil {
		return ""
	}
	return sig.Get()
}

func getSignalBool(sig *state.Signal[bool]) bool {
	if sig == nil {
		return false
	}
	return sig.Get()
}
