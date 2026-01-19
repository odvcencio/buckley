package scrollback

import (
	"github.com/odvcencio/fluffy-ui/compositor"
)

// RenderConfig holds styling for the scrollback buffer.
type RenderConfig struct {
	UserStyle      compositor.Style
	AssistantStyle compositor.Style
	SystemStyle    compositor.Style
	ToolStyle      compositor.Style
	SelectionStyle compositor.Style
	SearchStyle    compositor.Style
	ScrollbarStyle compositor.Style
	ScrollbarBG    compositor.Style
}

// DefaultRenderConfig returns default styling.
func DefaultRenderConfig() RenderConfig {
	return RenderConfig{
		UserStyle: compositor.DefaultStyle().
			WithFG(compositor.ColorGreen),
		AssistantStyle: compositor.DefaultStyle().
			WithFG(compositor.ColorCyan),
		SystemStyle: compositor.DefaultStyle().
			WithFG(compositor.ColorYellow).WithDim(true),
		ToolStyle: compositor.DefaultStyle().
			WithFG(compositor.ColorMagenta),
		SelectionStyle: compositor.DefaultStyle().
			WithBG(compositor.Color256(238)),
		SearchStyle: compositor.DefaultStyle().
			WithBG(compositor.ColorYellow).
			WithFG(compositor.ColorBlack),
		ScrollbarStyle: compositor.DefaultStyle().
			WithFG(compositor.ColorBrightWhite),
		ScrollbarBG: compositor.DefaultStyle().
			WithFG(compositor.Color256(238)),
	}
}

// Render draws the buffer to a compositor screen.
func Render(buf *Buffer, screen *compositor.Screen, x, y, width, height int, cfg RenderConfig) {
	if buf == nil || screen == nil {
		return
	}

	// Get visible lines
	lines := buf.GetVisibleLines()

	// Render each line
	for _, vl := range lines {
		if vl.RowIndex >= height {
			break
		}

		rowY := y + vl.RowIndex

		// Build style from line's stored style, falling back to source-based style
		var style compositor.Style
		if vl.Style.FG != 0 || vl.Style.BG != 0 || vl.Style.Bold || vl.Style.Italic || vl.Style.Dim {
			// Use the line's explicit style
			style = compositor.DefaultStyle()
			if vl.Style.FG != 0 {
				style = style.WithFG(compositor.Hex(vl.Style.FG))
			}
			if vl.Style.BG != 0 {
				style = style.WithBG(compositor.Hex(vl.Style.BG))
			}
			style = style.WithBold(vl.Style.Bold).WithItalic(vl.Style.Italic).WithDim(vl.Style.Dim)
		} else {
			// Fall back to source-based style
			style = getStyleForSource(vl.Source, cfg)
		}

		// Apply selection
		if vl.Selected {
			style = cfg.SelectionStyle
		}

		// Render content starting at left margin
		col := x

		// Render content
		for i, r := range vl.Content {
			if col >= x+width-1 { // -1 for scrollbar
				break
			}

			charStyle := style

			// Apply search highlight
			for _, highlightCol := range vl.SearchHighlights {
				// SearchHighlights already marks the start positions;
				// highlight extends based on the highlight positions stored in vl
				if i == highlightCol {
					charStyle = cfg.SearchStyle
				}
			}

			screen.Set(col, rowY, r, charStyle)
			col++
		}

		// Fill rest of line
		for col < x+width-1 {
			screen.Set(col, rowY, ' ', style)
			col++
		}
	}

	// Fill empty rows
	for row := len(lines); row < height; row++ {
		for col := x; col < x+width-1; col++ {
			screen.Set(col, y+row, ' ', compositor.DefaultStyle())
		}
	}

	// Render scrollbar
	renderScrollbar(buf, screen, x+width-1, y, height, cfg)
}

// renderScrollbar draws the scrollbar.
func renderScrollbar(buf *Buffer, screen *compositor.Screen, x, y, height int, cfg RenderConfig) {
	top, total, viewHeight := buf.ScrollPosition()

	if total <= viewHeight {
		// No scrollbar needed
		for row := 0; row < height; row++ {
			screen.Set(x, y+row, ' ', cfg.ScrollbarBG)
		}
		return
	}

	// Calculate thumb position and size
	thumbSize := max(1, height*viewHeight/total)
	thumbPos := height * top / total

	for row := 0; row < height; row++ {
		var r rune
		var style compositor.Style

		if row >= thumbPos && row < thumbPos+thumbSize {
			r = '█'
			style = cfg.ScrollbarStyle
		} else {
			r = '░'
			style = cfg.ScrollbarBG
		}

		screen.Set(x, y+row, r, style)
	}
}

// getStyleForSource returns the appropriate style for a message source.
func getStyleForSource(source string, cfg RenderConfig) compositor.Style {
	switch source {
	case "user":
		return cfg.UserStyle
	case "assistant":
		return cfg.AssistantStyle
	case "system":
		return cfg.SystemStyle
	case "tool":
		return cfg.ToolStyle
	default:
		return compositor.DefaultStyle()
	}
}

// RenderStatusLine draws a status line showing scroll position.
func RenderStatusLine(buf *Buffer, screen *compositor.Screen, x, y, width int, style compositor.Style) {
	top, total, viewHeight := buf.ScrollPosition()

	var status string
	if total <= viewHeight {
		status = "All"
	} else if top == 0 {
		status = "Top"
	} else if top >= total-viewHeight {
		status = "Bot"
	} else {
		pct := 100 * top / (total - viewHeight)
		status = itoa(pct) + "%"
	}

	// Right-align status
	startX := x + width - len(status) - 1
	screen.SetString(startX, y, status, style)

	// Show search status if active
	if current, total := buf.SearchMatches(); total > 0 {
		searchStatus := itoa(current) + "/" + itoa(total)
		screen.SetString(x+1, y, searchStatus, style)
	}
}

// itoa converts int to string without fmt.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}

	var buf [20]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}

	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}

	if neg {
		pos--
		buf[pos] = '-'
	}

	return string(buf[pos:])
}
