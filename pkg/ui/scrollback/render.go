package scrollback

import (
	"strconv"

	"m31labs.dev/buckley/pkg/ui/compositor"
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

	lines := buf.GetVisibleLines()
	contentWidth := max(0, width-1)
	for _, vl := range lines {
		if vl.RowIndex >= height {
			break
		}
		renderVisibleLine(screen, vl, x, y+vl.RowIndex, contentWidth, cfg)
	}

	fillEmptyRows(screen, x, y+len(lines), contentWidth, height-len(lines))
	renderScrollbar(buf, screen, x+width-1, y, height, cfg)
}

func renderVisibleLine(screen *compositor.Screen, line VisibleLine, x, y, width int, cfg RenderConfig) {
	style := visibleLineStyle(line, cfg)
	highlights := searchHighlightSet(line.SearchHighlights)

	col := 0
	for i, r := range line.Content {
		if col >= width {
			break
		}
		charStyle := style
		if highlights[i] {
			charStyle = cfg.SearchStyle
		}
		screen.Set(x+col, y, r, charStyle)
		col++
	}

	fillRow(screen, x+col, y, width-col, style)
}

func visibleLineStyle(line VisibleLine, cfg RenderConfig) compositor.Style {
	if line.Selected {
		return cfg.SelectionStyle
	}
	if hasExplicitLineStyle(line.Style) {
		return explicitLineStyle(line.Style)
	}
	return getStyleForSource(line.Source, cfg)
}

func hasExplicitLineStyle(style LineStyle) bool {
	return style.FG != 0 || style.BG != 0 || style.Bold || style.Italic || style.Dim
}

func explicitLineStyle(style LineStyle) compositor.Style {
	result := compositor.DefaultStyle()
	if style.FG != 0 {
		result = result.WithFG(compositor.Hex(style.FG))
	}
	if style.BG != 0 {
		result = result.WithBG(compositor.Hex(style.BG))
	}
	return result.WithBold(style.Bold).WithItalic(style.Italic).WithDim(style.Dim)
}

func searchHighlightSet(highlights []int) map[int]bool {
	set := make(map[int]bool, len(highlights))
	for _, highlight := range highlights {
		set[highlight] = true
	}
	return set
}

func fillEmptyRows(screen *compositor.Screen, x, y, width, rows int) {
	for row := 0; row < rows; row++ {
		fillRow(screen, x, y+row, width, compositor.DefaultStyle())
	}
}

func fillRow(screen *compositor.Screen, x, y, width int, style compositor.Style) {
	for col := 0; col < width; col++ {
		screen.Set(x+col, y, ' ', style)
	}
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
		status = strconv.Itoa(pct) + "%"
	}

	// Right-align status
	startX := x + width - len(status) - 1
	screen.SetString(startX, y, status, style)

	// Show search status if active
	if current, total := buf.SearchMatches(); total > 0 {
		searchStatus := strconv.Itoa(current) + "/" + strconv.Itoa(total)
		screen.SetString(x+1, y, searchStatus, style)
	}
}
