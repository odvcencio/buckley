package widgets

import (
	"strings"

	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/state"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// ChatReasoningBindings provides reactive inputs for the reasoning panel.
type ChatReasoningBindings struct {
	Text    state.Readable[string]
	Preview state.Readable[string]
	Visible state.Readable[bool]
}

// ChatReasoning renders the collapsible reasoning panel.
type ChatReasoning struct {
	services runtime.Services
	subs     state.Subscriptions

	textSig    state.Readable[string]
	previewSig state.Readable[string]
	visibleSig state.Readable[bool]

	panel     *uiwidgets.Panel
	accordion *uiwidgets.Accordion
	section   *uiwidgets.AccordionSection
	text      *TextBlock

	visible   bool
	onVisible func(bool)
}

// NewChatReasoning creates a reasoning panel bound to reactive state.
func NewChatReasoning(bindings ChatReasoningBindings) *ChatReasoning {
	r := &ChatReasoning{
		textSig:    bindings.Text,
		previewSig: bindings.Preview,
		visibleSig: bindings.Visible,
	}

	r.text = NewTextBlock("")
	r.section = uiwidgets.NewAccordionSection("Reasoning", r.text, uiwidgets.WithSectionExpanded(false))
	r.accordion = uiwidgets.NewAccordion(r.section)
	r.accordion.SetAllowMultiple(false)
	r.panel = uiwidgets.NewPanel(r.accordion).WithBorder(backend.DefaultStyle())
	r.panel.SetTitle("Reasoning")

	r.subscribe()
	if r.panel != nil {
		r.panel.Base.Role = accessibility.RoleGroup
		r.panel.Base.Label = "Reasoning"
	}
	return r
}

// Bind attaches app services and scheduler.
func (r *ChatReasoning) Bind(services runtime.Services) {
	if r == nil {
		return
	}
	r.services = services
	r.subs.SetScheduler(services.Scheduler())
	r.subscribe()
}

// Unbind releases app services and subscriptions.
func (r *ChatReasoning) Unbind() {
	if r == nil {
		return
	}
	r.subs.Clear()
	r.services = runtime.Services{}
}

// SetOnVisibilityChange sets a callback fired when visible changes.
func (r *ChatReasoning) SetOnVisibilityChange(fn func(bool)) {
	if r == nil {
		return
	}
	r.onVisible = fn
}

// SetTextStyle updates the reasoning text style.
func (r *ChatReasoning) SetTextStyle(style backend.Style) {
	if r == nil || r.text == nil {
		return
	}
	r.text.SetStyle(style)
}

// SetPanelStyle updates the panel border and background styles.
func (r *ChatReasoning) SetPanelStyle(border, bg backend.Style) {
	if r == nil || r.panel == nil {
		return
	}
	r.panel.SetStyle(bg)
	r.panel.WithBorder(border)
}

// Widget returns the panel widget.
func (r *ChatReasoning) Widget() runtime.Widget {
	if r == nil {
		return nil
	}
	return r.panel
}

// Visible reports whether the reasoning panel should be shown.
func (r *ChatReasoning) Visible() bool {
	if r == nil {
		return false
	}
	return r.visible
}

// Bounds returns the panel bounds.
func (r *ChatReasoning) Bounds() runtime.Rect {
	if r == nil || r.panel == nil {
		return runtime.Rect{}
	}
	return r.panel.Bounds()
}

// Contains reports whether a point is within the panel bounds.
func (r *ChatReasoning) Contains(x, y int) bool {
	if r == nil || !r.visible || r.panel == nil {
		return false
	}
	return r.panel.Bounds().Contains(x, y)
}

// ToggleExpanded toggles the accordion expansion.
func (r *ChatReasoning) ToggleExpanded() bool {
	if r == nil || !r.visible || r.section == nil {
		return false
	}
	expanded := !r.section.Expanded()
	r.section.SetExpanded(expanded)
	if r.services != (runtime.Services{}) {
		if announcer := r.services.Announcer(); announcer != nil {
			if expanded {
				announcer.Announce("Reasoning expanded", accessibility.PriorityPolite)
			} else {
				announcer.Announce("Reasoning collapsed", accessibility.PriorityPolite)
			}
		}
	}
	return true
}

func (r *ChatReasoning) subscribe() {
	r.subs.Clear()
	if r.textSig != nil {
		r.subs.Observe(r.textSig, r.sync)
	}
	if r.previewSig != nil {
		r.subs.Observe(r.previewSig, r.sync)
	}
	if r.visibleSig != nil {
		r.subs.Observe(r.visibleSig, r.sync)
	}
	r.sync()
}

func (r *ChatReasoning) sync() {
	if r == nil {
		return
	}

	visible := false
	if r.visibleSig != nil {
		visible = r.visibleSig.Get()
	}
	preview := ""
	if r.previewSig != nil {
		preview = strings.TrimSpace(r.previewSig.Get())
	}
	text := ""
	if r.textSig != nil {
		text = r.textSig.Get()
	}

	if r.section != nil {
		title := "Reasoning"
		if preview != "" {
			title = "Reasoning: " + preview
			r.section.SetExpanded(false)
		}
		r.section.SetTitle(title)
	}
	if r.text != nil {
		r.text.SetText(text)
	}

	if visible != r.visible {
		r.visible = visible
		if r.onVisible != nil {
			r.onVisible(visible)
		}
	}

	if r.services != (runtime.Services{}) {
		r.services.Invalidate()
	}
}
