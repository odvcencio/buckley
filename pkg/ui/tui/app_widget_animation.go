package tui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/ui/backend"
)

func (a *WidgetApp) updateAnimations(now time.Time) bool {
	dirty := false
	if a.expireStatusOverride(now) {
		dirty = true
	}
	a.expireCtrlCArm(now)
	if a.tickProcessStatus(now) {
		dirty = true
	}
	if a.tickCursorPulse(now) {
		dirty = true
	}
	return dirty
}

func (a *WidgetApp) expireStatusOverride(now time.Time) bool {
	if a.statusOverride == "" || now.Before(a.statusOverrideUntil) {
		return false
	}
	a.statusOverride = ""
	a.statusOverrideUntil = time.Time{}
	a.statusBar.SetStatus(a.currentStatusText(now))
	return true
}

func (a *WidgetApp) expireCtrlCArm(now time.Time) {
	if !a.ctrlCArmedUntil.IsZero() && !now.Before(a.ctrlCArmedUntil) {
		a.ctrlCArmedUntil = time.Time{}
	}
}

func (a *WidgetApp) tickProcessStatus(now time.Time) bool {
	if !a.processActive {
		return false
	}
	if !a.processLastTick.IsZero() && now.Sub(a.processLastTick) < processStatusTick {
		return false
	}
	a.statusBar.SetStatus(a.formatProcessStatus(now))
	a.processFrame = (a.processFrame + 1) % len(processSpinnerFrames)
	a.processLastTick = now
	return true
}

func (a *WidgetApp) tickCursorPulse(now time.Time) bool {
	if !a.inputArea.IsFocused() {
		return false
	}
	a.ensureCursorPulseConfig(now)
	if now.Sub(a.cursorPulseLast) < a.cursorPulseInterval {
		return false
	}

	style := a.cursorStyleForPhase(a.cursorPhase(now))
	a.cursorPulseLast = now
	if style == a.cursorStyle {
		return false
	}
	a.cursorStyle = style
	a.inputArea.SetCursorStyle(style)
	return true
}

func (a *WidgetApp) ensureCursorPulseConfig(now time.Time) {
	if a.cursorPulsePeriod <= 0 {
		a.cursorPulsePeriod = 2600 * time.Millisecond
	}
	if a.cursorPulseInterval <= 0 {
		a.cursorPulseInterval = 50 * time.Millisecond
	}
	if a.cursorPulseStart.IsZero() {
		a.cursorPulseStart = now
	}
}

func (a *WidgetApp) initSoftCursor() {
	accent := themeToBackendStyle(a.theme.Accent).FG()
	accentDim := themeToBackendStyle(a.theme.AccentDim).FG()
	surface := themeToBackendStyle(a.theme.SurfaceRaised).BG()
	textInverse := themeToBackendStyle(a.theme.TextInverse).FG()

	a.cursorBGHigh = accent
	a.cursorBGLow = blendColor(surface, accentDim, 0.35)
	a.cursorFG = textInverse
	a.cursorStyle = a.cursorStyleForPhase(0.2)
	a.inputArea.SetCursorStyle(a.cursorStyle)
}

func (a *WidgetApp) cursorPhase(now time.Time) float64 {
	if a.cursorPulsePeriod <= 0 {
		return 1
	}
	elapsed := now.Sub(a.cursorPulseStart)
	phase := float64(elapsed%a.cursorPulsePeriod) / float64(a.cursorPulsePeriod)
	return 0.5 - 0.5*math.Cos(2*math.Pi*phase)
}

func (a *WidgetApp) cursorStyleForPhase(phase float64) backend.Style {
	bg := blendColor(a.cursorBGLow, a.cursorBGHigh, phase)
	style := backend.DefaultStyle().Foreground(a.cursorFG).Background(bg)
	if phase < 0.35 {
		style = style.Dim(true)
	} else if phase > 0.75 {
		style = style.Bold(true)
	}
	return style
}

func (a *WidgetApp) setStatusOverride(text string, duration time.Duration) {
	if duration <= 0 {
		duration = 3 * time.Second
	}
	a.statusOverride = text
	a.statusOverrideUntil = time.Now().Add(duration)
	a.statusBar.SetStatus(text)
}

func (a *WidgetApp) currentStatusText(now time.Time) string {
	if a.processActive {
		return a.formatProcessStatus(now)
	}
	return a.statusText
}

func (a *WidgetApp) formatProcessStatus(now time.Time) string {
	text := strings.TrimSpace(a.processText)
	if text == "" {
		text = "Working"
	}
	if a.processStarted.IsZero() {
		a.processStarted = now
	}
	frame := processSpinnerFrames[a.processFrame%len(processSpinnerFrames)]
	return fmt.Sprintf("%s %s (%s)", frame, text, formatProcessElapsed(now.Sub(a.processStarted)))
}

func formatProcessElapsed(elapsed time.Duration) string {
	if elapsed < 0 {
		elapsed = 0
	}
	seconds := int(elapsed.Round(time.Second) / time.Second)
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm%02ds", seconds/60, seconds%60)
}
