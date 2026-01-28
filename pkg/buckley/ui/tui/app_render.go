// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"log"
	"math"
	"time"

	"github.com/odvcencio/buckley/pkg/diagnostics"
	"github.com/odvcencio/fluffyui/animation"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
)

// ============================================================================
// FILE: app_render.go
// PURPOSE: Render loop, animation updates, and rendering metrics
// FUNCTIONS:
//   - render
//   - updateAnimations
//   - initAnimations
//   - initSoftCursor
//   - cursorPhase
//   - cursorStyleForPhase
//   - blendColor
//   - drawFocusIndicator
// ============================================================================

// render draws the UI to the backend using partial redraws with optimization.
func (a *WidgetApp) render() {
	a.renderMu.Lock()
	defer a.renderMu.Unlock()

	start := time.Now()

	// Get dirty regions
	a.dirtyRegionsMu.Lock()
	regions := make([]dirtyRect, len(a.dirtyRegions))
	copy(regions, a.dirtyRegions)
	fullRedraw := a.fullRedraw
	a.dirtyRegionsMu.Unlock()

	// Render to screen buffer (this is the back buffer)
	a.screen.Render()
	buf := a.screen.Buffer()
	a.drawFocusIndicator(buf)

	// Track cells updated
	var cellsUpdated int64
	w, h := buf.Size()
	totalCells := w * h

	// Determine if we should do full or partial redraw
	// Full redraw if: explicitly requested, too many regions, or buffer is mostly dirty
	shouldFullRedraw := fullRedraw
	if !shouldFullRedraw && buf.IsDirty() {
		dirtyCount := buf.DirtyCount()
		if dirtyCount > totalCells/2 || len(regions) > 10 {
			shouldFullRedraw = true
		}
	}

	if shouldFullRedraw {
		// Full redraw - all cells
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				cell := buf.Get(x, y)
				a.backend.SetContent(x, y, cell.Rune, nil, cell.Style)
			}
		}
		cellsUpdated = int64(totalCells)
		a.metrics.FullRedraws++
	} else if len(regions) > 0 {
		// Partial redraw based on dirty regions
		for _, region := range regions {
			// Clip region to buffer bounds
			minX := maxInt(0, region.MinX)
			minY := maxInt(0, region.MinY)
			maxX := minInt(w, region.MaxX)
			maxY := minInt(h, region.MaxY)
			
			for y := minY; y < maxY; y++ {
				for x := minX; x < maxX; x++ {
					cell := buf.Get(x, y)
					a.backend.SetContent(x, y, cell.Rune, nil, cell.Style)
					cellsUpdated++
				}
			}
		}
		a.metrics.PartialRedraws++
	} else if buf.IsDirty() {
		// Fallback: use buffer's dirty tracking
		buf.ForEachDirtyCell(func(x, y int, cell runtime.Cell) {
			a.backend.SetContent(x, y, cell.Rune, nil, cell.Style)
			cellsUpdated++
		})
		a.metrics.PartialRedraws++
	}

	buf.ClearDirty()

	// Show the screen (atomic operation for double buffering effect)
	a.backend.Show()

	// Update metrics
	elapsed := time.Since(start)
	a.lastFrameTime = elapsed
	a.metrics.FrameCount++
	a.metrics.TotalRenderTime += elapsed
	a.metrics.LastFrameTime = elapsed
	a.metrics.CellsUpdated = cellsUpdated

	// Log frame time warning if rendering is too slow
	if elapsed > a.frameTimeTarget && a.debugRender {
		log.Printf("[render] slow frame: %v (target: %v)", elapsed, a.frameTimeTarget)
	}

	if a.debugRender && a.metrics.FrameCount%60 == 0 {
		avg := time.Duration(0)
		if a.metrics.FrameCount > 0 {
			avg = a.metrics.TotalRenderTime / time.Duration(a.metrics.FrameCount)
		}
		dropPct := 0.0
		if a.metrics.FrameCount > 0 {
			dropPct = float64(a.metrics.DroppedFrames) / float64(a.metrics.FrameCount) * 100
		}
		regionCount := len(regions)
		if fullRedraw {
			regionCount = -1 // Indicate full redraw
		}
		log.Printf("[render] frames=%d avg=%v dropped=%.1f%% cells=%d full=%d partial=%d regions=%d",
			a.metrics.FrameCount,
			avg,
			dropPct,
			a.metrics.CellsUpdated,
			a.metrics.FullRedraws,
			a.metrics.PartialRedraws,
			regionCount)
	}

	if a.recorder != nil && buf != nil {
		if err := a.recorder.Frame(buf, time.Now()); err != nil {
			log.Printf("tui recording frame failed: %v", err)
			a.recorder = nil
		}
	}

	a.lastRender = time.Now()
}

func (a *WidgetApp) updateAnimations(now time.Time) bool {
	dirty := false

	// Update animation framework
	if a.animator != nil && !a.reduceMotion {
		dt := 0.016 // ~60fps default
		if !a.lastRender.IsZero() {
			dt = now.Sub(a.lastRender).Seconds()
		}
		if a.animator.Update(dt) {
			dirty = true
		}
	}

	if a.statusOverride != "" && !now.Before(a.statusOverrideUntil) {
		a.statusOverride = ""
		a.statusOverrideUntil = time.Time{}
		a.statusBar.SetStatus(a.statusText)
		dirty = true
	}
	if !a.ctrlCArmedUntil.IsZero() && !now.Before(a.ctrlCArmedUntil) {
		a.ctrlCArmedUntil = time.Time{}
	}

	if a.inputArea.IsFocused() {
		if a.cursorPulsePeriod <= 0 {
			a.cursorPulsePeriod = 2600 * time.Millisecond
		}
		if a.cursorPulseInterval <= 0 {
			a.cursorPulseInterval = 50 * time.Millisecond
		}
		if a.cursorPulseStart.IsZero() {
			a.cursorPulseStart = now
		}

		if now.Sub(a.cursorPulseLast) >= a.cursorPulseInterval {
			phase := a.cursorPhase(now)
			style := a.cursorStyleForPhase(phase)
			if style != a.cursorStyle {
				a.cursorStyle = style
				a.inputArea.SetCursorStyle(style)
				dirty = true
			}
			a.cursorPulseLast = now
		}
	}

	if a.streaming && !a.reduceMotion {
		if a.streamAnimInterval <= 0 {
			a.streamAnimInterval = 120 * time.Millisecond
		}
		if a.streamAnimLast.IsZero() {
			a.streamAnimLast = now
		}
		if now.Sub(a.streamAnimLast) >= a.streamAnimInterval {
			a.streamAnim++
			a.statusBar.SetStreamAnim(a.streamAnim)
			a.streamAnimLast = now
			dirty = true
		}
	}

	if !a.reduceMotion {
		if a.sidebarAnimInterval <= 0 {
			a.sidebarAnimInterval = 120 * time.Millisecond
		}
		if now.Sub(a.sidebarAnimLast) >= a.sidebarAnimInterval {
			a.sidebarAnimFrame++
			if a.runningToolCount > 0 {
				a.sidebar.SetSpinnerFrame(a.sidebarAnimFrame)
				dirty = true
			}
			if a.presenceVisible && a.presence != nil {
				a.presence.SetPulseStep(a.sidebarAnimFrame)
				dirty = true
			}
			a.sidebarAnimLast = now
		}
	}

	if a.toastStack != nil && !a.reduceMotion {
		a.toastStack.SetNow(now)
		if a.toastStack.HasActiveAnimations(now) {
			dirty = true
		}
	}

	return dirty
}

func (a *WidgetApp) initAnimations() {
	// Cursor pulse spring - gentle oscillation for smooth cursor animation
	cursorCfg := animation.SpringGentle
	cursorCfg.OnUpdate = func(value float64) {
		phase := (value + 1) / 2 // Normalize -1..1 to 0..1
		a.cursorStyle = a.cursorStyleForPhase(phase)
		a.inputArea.SetCursorStyle(a.cursorStyle)
	}
	a.cursorPulseSpring = animation.NewSpring(0, cursorCfg)

	// Context meter spring - smooth value transitions for context usage display
	meterCfg := animation.SpringDefault
	meterCfg.OnUpdate = func(value float64) {
		// Update tracked value - actual sidebar update happens in SetContextUsage call
		a.contextUsed = int(value)
	}
	a.contextMeterSpring = animation.NewSpring(0, meterCfg)
}

func (a *WidgetApp) initSoftCursor() {
	accent := a.style(a.theme.Accent).FG()
	accentDim := a.style(a.theme.AccentDim).FG()
	surface := a.style(a.theme.SurfaceRaised).BG()
	textInverse := a.style(a.theme.TextInverse).FG()

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

func (a *WidgetApp) drawFocusIndicator(buf *runtime.Buffer) {
	if a == nil || buf == nil || a.focusStyle == nil {
		return
	}
	indicator := a.focusStyle.Indicator
	if indicator == "" || a.screen == nil {
		return
	}
	scope := a.screen.FocusScope()
	if scope == nil {
		return
	}
	focused := scope.Current()
	if focused == nil {
		return
	}
	boundsProvider, ok := focused.(runtime.BoundsProvider)
	if !ok {
		return
	}
	bounds := boundsProvider.Bounds()
	if bounds.Width <= 0 || bounds.Height <= 0 {
		return
	}

	style := a.focusStyle.Style
	if a.highContrast && a.focusStyle.HighContrast != (backend.Style{}) {
		style = a.focusStyle.HighContrast
	}
	x := bounds.X - len(indicator)
	if x < 0 {
		x = bounds.X
	}
	buf.SetString(x, bounds.Y, indicator, style)
}

// Metrics returns a copy of the current render metrics.
func (a *WidgetApp) Metrics() RenderMetrics {
	if a == nil {
		return RenderMetrics{}
	}
	a.renderMu.Lock()
	defer a.renderMu.Unlock()
	return a.metrics
}

// Refresh forces a re-render.
func (a *WidgetApp) Refresh() {
	if a == nil {
		return
	}
	a.Post(RefreshMsg{})
}

// SetDiagnostics sets the backend diagnostics collector for debug dumps.
func (a *WidgetApp) SetDiagnostics(collector *diagnostics.Collector) {
	if a == nil {
		return
	}
	a.diagnostics = collector
}
