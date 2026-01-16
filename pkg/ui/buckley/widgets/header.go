package widgets

import (
	"strings"

	"github.com/odvcencio/buckley/pkg/ui/backend"
	"github.com/odvcencio/buckley/pkg/ui/runtime"
	uiwidgets "github.com/odvcencio/buckley/pkg/ui/widgets"
)

// Header is the Buckley header bar widget.
type Header struct {
	uiwidgets.Base
	logo          string
	modelName     string
	sessionID     string
	bgStyle       backend.Style
	logoStyle     backend.Style
	textStyle     backend.Style
	sessionBounds runtime.Rect
	modelBounds   runtime.Rect
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

// SetSessionID updates the displayed session ID.
func (h *Header) SetSessionID(id string) {
	h.sessionID = id
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
	bounds := h.Bounds()
	if bounds.Width == 0 || bounds.Height == 0 {
		return
	}

	// Fill background
	ctx.Buffer.Fill(bounds, ' ', h.bgStyle)

	// Draw logo on left
	ctx.Buffer.SetString(bounds.X, bounds.Y, h.logo, h.logoStyle)

	h.sessionBounds = runtime.Rect{}
	h.modelBounds = runtime.Rect{}

	// Draw right-side session/model labels
	sessionStr := formatSessionLabel(h.sessionID)
	modelStr := strings.TrimSpace(h.modelName)
	parts := make([]string, 0, 2)
	if sessionStr != "" {
		parts = append(parts, sessionStr)
	}
	if modelStr != "" {
		parts = append(parts, modelStr)
	}
	right := strings.Join(parts, " · ")
	if right != "" {
		x := bounds.X + bounds.Width - len(right)
		if x > bounds.X+len(h.logo) {
			ctx.Buffer.SetString(x, bounds.Y, right, h.textStyle)
			if sessionStr != "" && modelStr != "" {
				h.sessionBounds = runtime.Rect{X: x, Y: bounds.Y, Width: len(sessionStr), Height: 1}
				h.modelBounds = runtime.Rect{X: x + len(sessionStr) + len(" · "), Y: bounds.Y, Width: len(modelStr), Height: 1}
			} else if sessionStr != "" {
				h.sessionBounds = runtime.Rect{X: x, Y: bounds.Y, Width: len(sessionStr), Height: 1}
			} else if modelStr != "" {
				h.modelBounds = runtime.Rect{X: x, Y: bounds.Y, Width: len(modelStr), Height: 1}
			}
		}
	}
}

// WebLinkAt returns a header link target at the given point.
func (h *Header) WebLinkAt(x, y int) (string, bool) {
	if h.sessionBounds.Contains(x, y) {
		return "session", true
	}
	if h.modelBounds.Contains(x, y) {
		return "model", true
	}
	return "", false
}

func formatSessionLabel(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	short := id
	if len(short) > 8 {
		short = short[len(short)-8:]
	}
	return "sess " + short
}
