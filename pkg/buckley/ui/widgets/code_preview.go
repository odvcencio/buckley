package widgets

import (
	"strings"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/terminal"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// CodePreview shows a scrollable code block in a modal overlay.
type CodePreview struct {
	uiwidgets.Base
	panel  *uiwidgets.Panel
	header *uiwidgets.Label
	text   *uiwidgets.Text
	scroll *uiwidgets.ScrollView
}

// NewCodePreview creates a new code preview overlay widget.
func NewCodePreview(language, code string) *CodePreview {
	title := "Code Preview"
	headerText := "Esc to close"
	lang := strings.TrimSpace(language)
	if lang != "" {
		headerText = "Language: " + lang + " · Esc to close"
	}
	header := uiwidgets.NewLabel(headerText)
	text := uiwidgets.NewText(code)
	scroll := uiwidgets.NewScrollView(text)
	layout := runtime.VBox(
		runtime.Fixed(header),
		runtime.Flexible(scroll, 1),
	).WithGap(1)
	panel := uiwidgets.NewPanel(layout)
	panel.SetTitle(title)
	return &CodePreview{
		panel:  panel,
		header: header,
		text:   text,
		scroll: scroll,
	}
}

// SetStyles updates the preview styles.
func (c *CodePreview) SetStyles(border, background, headerStyle, textStyle backend.Style) {
	if c == nil {
		return
	}
	if c.panel != nil {
		c.panel.SetStyle(background)
		c.panel.WithBorder(border)
	}
	if c.scroll != nil {
		c.scroll.SetStyle(background)
	}
	if c.header != nil {
		c.header.SetStyle(headerStyle)
	}
	if c.text != nil {
		c.text.SetStyle(textStyle)
	}
}

// Measure returns the desired size.
func (c *CodePreview) Measure(constraints runtime.Constraints) runtime.Size {
	if c == nil || c.panel == nil {
		return constraints.MinSize()
	}
	return c.panel.Measure(constraints)
}

// Layout positions the panel.
func (c *CodePreview) Layout(bounds runtime.Rect) {
	c.Base.Layout(bounds)
	if c.panel != nil {
		c.panel.Layout(bounds)
	}
}

// Render draws the preview.
func (c *CodePreview) Render(ctx runtime.RenderContext) {
	if c == nil || c.panel == nil {
		return
	}
	c.panel.Render(ctx)
}

// HandleMessage handles close keys and forwards input.
func (c *CodePreview) HandleMessage(msg runtime.Message) runtime.HandleResult {
	if c == nil {
		return runtime.Unhandled()
	}
	if key, ok := msg.(runtime.KeyMsg); ok {
		switch key.Key {
		case terminal.KeyEscape:
			return runtime.WithCommand(runtime.PopOverlay{})
		case terminal.KeyRune:
			if key.Rune == 'q' || key.Rune == 'Q' {
				return runtime.WithCommand(runtime.PopOverlay{})
			}
		}
	}
	if c.panel != nil {
		return c.panel.HandleMessage(msg)
	}
	return runtime.Unhandled()
}

// ChildWidgets exposes the panel for focus traversal.
func (c *CodePreview) ChildWidgets() []runtime.Widget {
	if c.panel == nil {
		return nil
	}
	return []runtime.Widget{c.panel}
}

var _ runtime.Widget = (*CodePreview)(nil)
var _ runtime.ChildProvider = (*CodePreview)(nil)
