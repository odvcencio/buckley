package filepicker

import (
	"fmt"
	"strings"

	"github.com/odvcencio/fluffy-ui/compositor"
)

// RenderConfig holds styling configuration.
type RenderConfig struct {
	// Colors
	BorderStyle    compositor.Style
	TitleStyle     compositor.Style
	QueryStyle     compositor.Style
	MatchStyle     compositor.Style
	HighlightStyle compositor.Style
	SelectedStyle  compositor.Style
	DimStyle       compositor.Style

	// Characters
	SelectedPrefix string
	NormalPrefix   string
}

// DefaultRenderConfig returns default styling.
func DefaultRenderConfig() RenderConfig {
	return RenderConfig{
		BorderStyle: compositor.DefaultStyle().WithFG(compositor.ColorCyan),
		TitleStyle:  compositor.DefaultStyle().WithFG(compositor.ColorCyan).WithBold(true),
		QueryStyle:  compositor.DefaultStyle().WithFG(compositor.ColorYellow),
		MatchStyle:  compositor.DefaultStyle(),
		HighlightStyle: compositor.DefaultStyle().
			WithFG(compositor.ColorGreen).WithBold(true),
		SelectedStyle: compositor.DefaultStyle().
			WithBG(compositor.Color256(238)).WithBold(true),
		DimStyle: compositor.DefaultStyle().WithDim(true),

		SelectedPrefix: "▸ ",
		NormalPrefix:   "  ",
	}
}

// Render draws the file picker to a compositor screen.
func Render(fp *FilePicker, screen *compositor.Screen, cfg RenderConfig) {
	if fp == nil || screen == nil || !fp.IsActive() {
		return
	}

	width, height := fp.Dimensions()
	offsetX, offsetY := fp.Offset()

	// Draw border
	drawBorder(screen, offsetX, offsetY, width, height, cfg.BorderStyle)

	// Draw title
	title := " @ Files "
	query := fp.Query()
	if query != "" {
		title = fmt.Sprintf(" @%s ", query)
	}
	titleX := offsetX + (width-len(title))/2
	screen.SetString(titleX, offsetY, title, cfg.TitleStyle)

	// Draw matches
	matches := fp.GetMatches()
	selected := fp.SelectedIndex()

	innerX := offsetX + 1
	innerY := offsetY + 1
	innerWidth := width - 2

	for i, match := range matches {
		if i >= height-2 {
			break
		}

		y := innerY + i
		style := cfg.MatchStyle

		// Selection highlighting
		prefix := cfg.NormalPrefix
		if i == selected {
			prefix = cfg.SelectedPrefix
			style = cfg.SelectedStyle
			// Fill row background for selected
			screen.FillRect(innerX, y, innerWidth, 1, ' ', style)
		}

		// Draw prefix
		screen.SetString(innerX, y, prefix, style)

		// Draw path with highlights
		x := innerX + len(prefix)
		renderHighlightedPath(screen, x, y, innerWidth-len(prefix), match, style, cfg.HighlightStyle)
	}

	// Show file count if no matches or empty query
	if len(matches) == 0 {
		msg := "No matches"
		if !fp.IsIndexReady() {
			msg = "Indexing files..."
		} else if fp.Query() == "" {
			msg = fmt.Sprintf("%d files (type to filter)", fp.FileCount())
		}
		screen.SetString(innerX+1, innerY, msg, cfg.DimStyle)
	}

	// Draw scroll indicator if needed
	if len(matches) > height-2 {
		screen.SetString(offsetX+width-3, offsetY+height-1, "▼", cfg.DimStyle)
	}
}

// drawBorder draws a box border.
func drawBorder(screen *compositor.Screen, x, y, w, h int, style compositor.Style) {
	if w < 2 || h < 2 {
		return
	}

	// Corners
	screen.Set(x, y, '╭', style)
	screen.Set(x+w-1, y, '╮', style)
	screen.Set(x, y+h-1, '╰', style)
	screen.Set(x+w-1, y+h-1, '╯', style)

	// Horizontal lines
	for col := x + 1; col < x+w-1; col++ {
		screen.Set(col, y, '─', style)
		screen.Set(col, y+h-1, '─', style)
	}

	// Vertical lines
	for row := y + 1; row < y+h-1; row++ {
		screen.Set(x, row, '│', style)
		screen.Set(x+w-1, row, '│', style)
	}
}

// renderHighlightedPath draws a path with matched characters highlighted.
func renderHighlightedPath(screen *compositor.Screen, x, y, maxWidth int, match FileMatch, normalStyle, highlightStyle compositor.Style) {
	path := match.Path
	runes := []rune(path)

	// Truncate if needed
	if len(runes) > maxWidth {
		// Show end of path (most relevant part)
		truncated := string(runes[len(runes)-maxWidth+3:])
		path = "..." + truncated
		runes = []rune(path)

		// Adjust highlight indices
		offset := len([]rune(match.Path)) - len(runes) + 3
		newHighlights := make([]int, 0, len(match.Highlights))
		for _, h := range match.Highlights {
			adjusted := h - offset
			if adjusted >= 3 { // After "..."
				newHighlights = append(newHighlights, adjusted)
			}
		}
		match.Highlights = newHighlights
	}

	// Create highlight set for O(1) lookup
	highlightSet := make(map[int]bool)
	for _, h := range match.Highlights {
		highlightSet[h] = true
	}

	// Draw each character
	col := x
	for i, r := range runes {
		if col >= x+maxWidth {
			break
		}

		style := normalStyle
		if highlightSet[i] {
			style = highlightStyle
		}

		screen.Set(col, y, r, style)
		col++
	}
}

// RenderToString renders the picker to a string (for bubbletea compatibility).
func RenderToString(fp *FilePicker, cfg RenderConfig) string {
	if fp == nil || !fp.IsActive() {
		return ""
	}

	width, height := fp.Dimensions()
	screen := compositor.NewScreen(width, height)

	// Render to screen at (0, 0)
	fp.SetOffset(0, 0)
	Render(fp, screen, cfg)

	// Convert to string
	return compositor.RenderToString(width, height, func(s *compositor.Screen) {
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				cell := screen.Get(x, y)
				s.Set(x, y, cell.Rune, cell.Style)
			}
		}
	})
}

// CompactView returns a minimal single-line view for inline display.
func CompactView(fp *FilePicker) string {
	if fp == nil || !fp.IsActive() {
		return ""
	}

	var b strings.Builder
	b.WriteString("@")
	b.WriteString(fp.Query())

	if selected := fp.GetSelected(); selected != "" {
		b.WriteString(" → ")
		// Show just filename
		parts := strings.Split(selected, "/")
		if len(parts) > 0 {
			b.WriteString(parts[len(parts)-1])
		}
	}

	return b.String()
}
