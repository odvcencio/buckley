package widgets

import (
	"strings"

	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/state"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// HeaderConfig provides external state for the header.
type HeaderConfig struct {
	ModelName state.Readable[string]
	SessionID state.Readable[string]
}

// Header is the Buckley header bar widget.
type Header struct {
	uiwidgets.Base
	logo          string
	modelName     string
	sessionID     string
	services      runtime.Services
	subs          state.Subscriptions
	modelSig      state.Readable[string]
	sessionSig    state.Readable[string]
	ownedModelSig *state.Signal[string]
	ownedSessSig  *state.Signal[string]
	bgStyle       backend.Style
	logoStyle     backend.Style
	textStyle     backend.Style
	sessionBounds runtime.Rect
	modelBounds   runtime.Rect
}

// NewHeader creates a new header widget.
func NewHeader() *Header {
	return NewHeaderWithConfig(HeaderConfig{})
}

// NewHeaderWithConfig creates a new header widget with optional state bindings.
func NewHeaderWithConfig(cfg HeaderConfig) *Header {
	h := &Header{
		logo:      " ● Buckley",
		bgStyle:   backend.DefaultStyle(),
		logoStyle: backend.DefaultStyle().Bold(true),
		textStyle: backend.DefaultStyle(),
	}
	h.ownedModelSig = state.NewSignal("")
	h.ownedSessSig = state.NewSignal("")
	if cfg.ModelName != nil {
		h.modelSig = cfg.ModelName
	} else {
		h.modelSig = h.ownedModelSig
	}
	if cfg.SessionID != nil {
		h.sessionSig = cfg.SessionID
	} else {
		h.sessionSig = h.ownedSessSig
	}
	h.Base.Landmark = accessibility.LandmarkBanner
	h.subscribe()
	h.syncA11y()
	return h
}

// SetModelName updates the displayed model name.
func (h *Header) SetModelName(name string) {
	if h == nil {
		return
	}
	name = strings.TrimSpace(name)
	if h.ownsModel() && h.ownedModelSig != nil {
		h.ownedModelSig.Set(name)
	}
}

// SetSessionID updates the displayed session ID.
func (h *Header) SetSessionID(id string) {
	if h == nil {
		return
	}
	id = strings.TrimSpace(id)
	if h.ownsSession() && h.ownedSessSig != nil {
		h.ownedSessSig.Set(id)
	}
}

// SetStyles sets the header styles.
func (h *Header) SetStyles(bg, logo, text backend.Style) {
	if h == nil {
		return
	}
	h.bgStyle = bg
	h.logoStyle = logo
	h.textStyle = text
}

// Bind attaches app services and subscriptions.
func (h *Header) Bind(services runtime.Services) {
	if h == nil {
		return
	}
	h.services = services
	h.subs.SetScheduler(services.Scheduler())
	h.subscribe()
	h.syncA11y()
}

// Unbind releases app services and subscriptions.
func (h *Header) Unbind() {
	if h == nil {
		return
	}
	h.subs.Clear()
	h.services = runtime.Services{}
}

func (h *Header) ownsModel() bool {
	if h.ownedModelSig == nil || h.modelSig == nil {
		return false
	}
	sig, ok := h.modelSig.(*state.Signal[string])
	return ok && sig == h.ownedModelSig
}

func (h *Header) ownsSession() bool {
	if h.ownedSessSig == nil || h.sessionSig == nil {
		return false
	}
	sig, ok := h.sessionSig.(*state.Signal[string])
	return ok && sig == h.ownedSessSig
}

func (h *Header) subscribe() {
	h.subs.Clear()
	if h.modelSig != nil {
		h.subs.Observe(h.modelSig, h.onModelChanged)
	}
	if h.sessionSig != nil {
		h.subs.Observe(h.sessionSig, h.onSessionChanged)
	}
	h.onModelChanged()
	h.onSessionChanged()
}

func (h *Header) onModelChanged() {
	if h.modelSig == nil {
		return
	}
	h.modelName = strings.TrimSpace(h.modelSig.Get())
	h.syncA11y()
	if h.services != (runtime.Services{}) {
		h.services.Invalidate()
	}
}

func (h *Header) onSessionChanged() {
	if h.sessionSig == nil {
		return
	}
	h.sessionID = strings.TrimSpace(h.sessionSig.Get())
	h.syncA11y()
	if h.services != (runtime.Services{}) {
		h.services.Invalidate()
	}
}

func (h *Header) syncA11y() {
	if h == nil {
		return
	}
	label := "Buckley"
	if h.modelName != "" {
		label += " · " + h.modelName
	}
	if h.sessionID != "" {
		label += " · " + formatSessionLabel(h.sessionID)
	}
	h.Base.Label = label
}

// Measure returns the header size (1 row tall, full width).
func (h *Header) Measure(constraints runtime.Constraints) runtime.Size {
	// Use Constrain to handle unbounded MaxWidth safely
	return constraints.Constrain(runtime.Size{
		Width:  constraints.MaxWidth,
		Height: 1,
	})
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
		x := bounds.X + bounds.Width - textWidth(right)
		if x > bounds.X+textWidth(h.logo) {
			ctx.Buffer.SetString(x, bounds.Y, right, h.textStyle)
			if sessionStr != "" && modelStr != "" {
				h.sessionBounds = runtime.Rect{X: x, Y: bounds.Y, Width: textWidth(sessionStr), Height: 1}
				h.modelBounds = runtime.Rect{X: x + textWidth(sessionStr) + textWidth(" · "), Y: bounds.Y, Width: textWidth(modelStr), Height: 1}
			} else if sessionStr != "" {
				h.sessionBounds = runtime.Rect{X: x, Y: bounds.Y, Width: textWidth(sessionStr), Height: 1}
			} else if modelStr != "" {
				h.modelBounds = runtime.Rect{X: x, Y: bounds.Y, Width: textWidth(modelStr), Height: 1}
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

var _ runtime.Bindable = (*Header)(nil)
var _ runtime.Unbindable = (*Header)(nil)
