package widgets

import (
	"strings"

	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// TextBlock renders multi-line wrapped text.
type TextBlock struct {
	uiwidgets.Base
	text     string
	style    backend.Style
	styleSet bool
}

// NewTextBlock creates a new text block widget.
func NewTextBlock(text string) *TextBlock {
	return &TextBlock{
		text:  text,
		style: backend.DefaultStyle(),
	}
}

// SetText updates the text content.
func (t *TextBlock) SetText(text string) {
	if t == nil {
		return
	}
	t.text = text
}

// SetStyle updates the text style.
func (t *TextBlock) SetStyle(style backend.Style) {
	if t == nil {
		return
	}
	t.style = style
	t.styleSet = true
}

// Measure returns the preferred size based on wrapped text.
func (t *TextBlock) Measure(constraints runtime.Constraints) runtime.Size {
	width := constraints.MaxWidth
	if width <= 0 {
		width = constraints.MinWidth
	}
	if width <= 0 {
		width = 1
	}
	lines := wrapText(t.text, width)
	height := len(lines)
	if height < 1 {
		height = 1
	}
	return constraints.Constrain(runtime.Size{Width: width, Height: height})
}

// Render draws the wrapped text.
func (t *TextBlock) Render(ctx runtime.RenderContext) {
	if t == nil {
		return
	}
	bounds := t.Bounds()
	if bounds.Width <= 0 || bounds.Height <= 0 {
		return
	}
	style := t.style
	if !t.styleSet {
		style = backend.DefaultStyle()
	}
	lines := wrapText(t.text, bounds.Width)
	maxLines := bounds.Height
	if len(lines) < maxLines {
		maxLines = len(lines)
	}
	for i := 0; i < maxLines; i++ {
		ctx.Buffer.SetString(bounds.X, bounds.Y+i, lines[i], style)
	}
}

func wrapText(text string, width int) []string {
	if width <= 0 {
		return []string{""}
	}
	if strings.TrimSpace(text) == "" {
		return []string{""}
	}
	parts := strings.Split(text, "\n")
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			lines = append(lines, "")
			continue
		}
		runes := []rune(part)
		for len(runes) > width {
			lines = append(lines, string(runes[:width]))
			runes = runes[width:]
		}
		lines = append(lines, string(runes))
	}
	if len(lines) == 0 {
		lines = append(lines, "")
	}
	return lines
}

