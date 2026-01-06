package widgets

import (
	"github.com/odvcencio/buckley/pkg/ui/backend"
	"github.com/odvcencio/buckley/pkg/ui/runtime"
)

// Header is the Buckley header bar widget.
type Header struct {
	Base
	logo      string
	modelName string
	bgStyle   backend.Style
	logoStyle backend.Style
	textStyle backend.Style
}

// NewHeader creates a new header widget.
func NewHeader() *Header {
	return &Header{
		logo:      " ● Buckley",
		bgStyle:   backend.DefaultStyle(),
		logoStyle: backend.DefaultStyle().Bold(true),
		textStyle: backend.DefaultStyle(),
	}
}

// SetModelName updates the displayed model name.
func (h *Header) SetModelName(name string) {
	h.modelName = name
}

// SetStyles sets the header styles.
func (h *Header) SetStyles(bg, logo, text backend.Style) {
	h.bgStyle = bg
	h.logoStyle = logo
	h.textStyle = text
}

// Measure returns the header size (1 row tall, full width).
func (h *Header) Measure(constraints runtime.Constraints) runtime.Size {
	return runtime.Size{
		Width:  constraints.MaxWidth,
		Height: 1,
	}
}

// Render draws the header.
func (h *Header) Render(ctx runtime.RenderContext) {
	bounds := h.bounds
	if bounds.Width == 0 || bounds.Height == 0 {
		return
	}

	// Fill background
	ctx.Buffer.Fill(bounds, ' ', h.bgStyle)

	// Draw logo on left
	ctx.Buffer.SetString(bounds.X, bounds.Y, h.logo, h.logoStyle)

	// Draw model name on right
	if h.modelName != "" {
		modelStr := h.modelName + " "
		x := bounds.X + bounds.Width - len(modelStr)
		if x > bounds.X+len(h.logo) {
			ctx.Buffer.SetString(x, bounds.Y, modelStr, h.textStyle)
		}
	}
}
