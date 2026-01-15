package widgets

import (
	"github.com/odvcencio/buckley/pkg/ui/backend"
	"github.com/odvcencio/buckley/pkg/ui/runtime"
)

// PresenceStrip renders a compact activity indicator when the sidebar is hidden.
type PresenceStrip struct {
	Base

	width int

	active    bool
	attention bool
	streaming bool
	planPct   int
	pulseStep int

	borderStyle    backend.Style
	idleStyle      backend.Style
	activeStyle    backend.Style
	attentionStyle backend.Style
	planStyle      backend.Style
	bgStyle        backend.Style
}

// NewPresenceStrip creates a new presence strip widget.
func NewPresenceStrip() *PresenceStrip {
	return &PresenceStrip{
		width:          2,
		planPct:        -1,
		borderStyle:    backend.DefaultStyle(),
		idleStyle:      backend.DefaultStyle(),
		activeStyle:    backend.DefaultStyle(),
		attentionStyle: backend.DefaultStyle(),
		planStyle:      backend.DefaultStyle(),
		bgStyle:        backend.DefaultStyle(),
	}
}

// SetWidth updates the strip width (min 1).
func (p *PresenceStrip) SetWidth(width int) {
	if width < 1 {
		width = 1
	}
	p.width = width
}

// SetStyles configures the strip appearance.
func (p *PresenceStrip) SetStyles(border, idle, active, attention, plan, background backend.Style) {
	p.borderStyle = border
	p.idleStyle = idle
	p.activeStyle = active
	p.attentionStyle = attention
	p.planStyle = plan
	p.bgStyle = background
}

// SetActivity updates the activity state.
func (p *PresenceStrip) SetActivity(active, attention, streaming bool) {
	p.active = active
	p.attention = attention
	p.streaming = streaming
}

// SetPlanProgress sets plan progress (0-100). Use -1 to hide.
func (p *PresenceStrip) SetPlanProgress(percent int) {
	if percent < 0 {
		p.planPct = -1
		return
	}
	if percent > 100 {
		percent = 100
	}
	p.planPct = percent
}

// SetPulseStep updates the pulse frame index.
func (p *PresenceStrip) SetPulseStep(step int) {
	if step < 0 {
		step = 0
	}
	p.pulseStep = step
}

// Measure returns the preferred size.
func (p *PresenceStrip) Measure(constraints runtime.Constraints) runtime.Size {
	width := p.width
	if width <= 0 {
		width = 2
	}
	if constraints.MaxWidth < width {
		width = constraints.MaxWidth
	}
	return runtime.Size{Width: width, Height: constraints.MaxHeight}
}

// Layout stores the assigned bounds.
func (p *PresenceStrip) Layout(bounds runtime.Rect) {
	p.Base.Layout(bounds)
}

// Render draws the presence strip.
func (p *PresenceStrip) Render(ctx runtime.RenderContext) {
	b := p.bounds
	if b.Width <= 0 || b.Height <= 0 {
		return
	}
	ctx.Clear(p.bgStyle)

	stripX := b.X
	indicatorX := b.X
	if b.Width > 1 {
		indicatorX = b.X + 1
	}

	for y := b.Y; y < b.Y+b.Height; y++ {
		ctx.Buffer.Set(stripX, y, '│', p.borderStyle)
	}

	if b.Height < 2 {
		return
	}

	// Plan indicator near the top.
	if p.planPct >= 0 {
		planRune := progressGlyph(p.planPct)
		ctx.Buffer.Set(indicatorX, b.Y+1, planRune, p.planStyle)
	}

	centerY := b.Y + b.Height/2
	indicator := '·'
	style := p.idleStyle
	if p.attention {
		style = p.attentionStyle
		if p.pulseStep%2 == 0 {
			indicator = '●'
		} else {
			indicator = '○'
		}
	} else if p.active || p.streaming {
		style = p.activeStyle
		if p.pulseStep%2 == 0 {
			indicator = '●'
		} else {
			indicator = '◦'
		}
	}
	ctx.Buffer.Set(indicatorX, centerY, indicator, style)
}
