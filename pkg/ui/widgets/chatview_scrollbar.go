package widgets

import "m31labs.dev/fluffyui/runtime"

func (c *ChatView) handleScrollbarMouse(msg runtime.MouseMsg) runtime.HandleResult {
	if c == nil {
		return runtime.Unhandled()
	}
	inside := c.scrollbarContains(msg.X, msg.Y)
	switch msg.Button {
	case runtime.MouseWheelUp:
		if inside {
			c.ScrollUp(3)
			return runtime.Handled()
		}
	case runtime.MouseWheelDown:
		if inside {
			c.ScrollDown(3)
			return runtime.Handled()
		}
	case runtime.MouseLeft:
		switch msg.Action {
		case runtime.MousePress:
			if !inside {
				return runtime.Unhandled()
			}
			c.scrollbarDragging = true
			c.scrollToScrollbarPoint(msg.Y)
			return runtime.Handled()
		case runtime.MouseRelease:
			if !c.scrollbarDragging {
				return runtime.Unhandled()
			}
			c.scrollToScrollbarPoint(msg.Y)
			c.scrollbarDragging = false
			return runtime.Handled()
		}
	}
	if msg.Action == runtime.MouseMove && c.scrollbarDragging {
		c.scrollToScrollbarPoint(msg.Y)
		return runtime.Handled()
	}
	return runtime.Unhandled()
}

func (c *ChatView) scrollbarContains(x, y int) bool {
	viewport := chatViewport(c.bounds)
	return viewport.Height > 0 && x == viewport.X+viewport.Width-1 && y >= viewport.Y && y < viewport.Y+viewport.Height
}

func (c *ChatView) scrollToScrollbarPoint(y int) {
	viewport := chatViewport(c.bounds)
	if viewport.Height <= 1 {
		return
	}
	position := y - viewport.Y
	if position < 0 {
		position = 0
	}
	if position >= viewport.Height {
		position = viewport.Height - 1
	}
	c.buffer.ScrollToFraction(position, viewport.Height-1)
	c.notifyScroll()
}

func (c *ChatView) renderSemanticScrollbar(ctx runtime.RenderContext, bounds runtime.Rect) {
	top, total, viewH := c.buffer.ScrollPosition()
	if total <= viewH || bounds.Height <= 0 {
		return
	}

	scrollX := bounds.X + bounds.Width - 1
	for y := 0; y < bounds.Height; y++ {
		ctx.Buffer.Set(scrollX, bounds.Y+y, '░', c.scrollbarStyle)
	}

	for _, mark := range c.buffer.SemanticMarks() {
		y := semanticScrollbarY(mark.Row, total, bounds.Height)
		r := '◇'
		style := c.assistantStyle
		if mark.Source == "user" {
			r = '◆'
			style = c.userStyle
		}
		ctx.Buffer.Set(scrollX, bounds.Y+y, r, style)
	}

	thumbSize := max(1, (viewH*viewH)/total)
	thumbPos := (top * (viewH - thumbSize)) / max(1, total-viewH)
	for y := thumbPos; y < thumbPos+thumbSize && y < bounds.Height; y++ {
		ctx.Buffer.Set(scrollX, bounds.Y+y, '█', c.scrollThumb)
	}
}

func semanticScrollbarY(row, total, height int) int {
	if height <= 1 || total <= 1 {
		return 0
	}
	if row < 0 {
		row = 0
	}
	if row >= total {
		row = total - 1
	}
	return (row * (height - 1)) / (total - 1)
}
