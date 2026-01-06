package widgets

import (
	"github.com/odvcencio/buckley/pkg/ui/backend"
	"github.com/odvcencio/buckley/pkg/ui/runtime"
)

// StatusBar is the Buckley status bar widget.
type StatusBar struct {
	Base
	status     string
	tokens     int
	costCents  float64
	scrollPos  string // "TOP", "END", or percentage
	bgStyle    backend.Style
	textStyle  backend.Style
}

// NewStatusBar creates a new status bar widget.
func NewStatusBar() *StatusBar {
	return &StatusBar{
		status:    "Ready",
		bgStyle:   backend.DefaultStyle(),
		textStyle: backend.DefaultStyle(),
	}
}

// SetStatus updates the status text.
func (s *StatusBar) SetStatus(text string) {
	s.status = text
}

// SetTokens updates the token count and cost.
func (s *StatusBar) SetTokens(tokens int, costCents float64) {
	s.tokens = tokens
	s.costCents = costCents
}

// SetScrollPosition updates the scroll position indicator.
func (s *StatusBar) SetScrollPosition(pos string) {
	s.scrollPos = pos
}

// SetStyles sets the status bar styles.
func (s *StatusBar) SetStyles(bg, text backend.Style) {
	s.bgStyle = bg
	s.textStyle = text
}

// Measure returns the status bar size (1 row tall, full width).
func (s *StatusBar) Measure(constraints runtime.Constraints) runtime.Size {
	return runtime.Size{
		Width:  constraints.MaxWidth,
		Height: 1,
	}
}

// Render draws the status bar.
func (s *StatusBar) Render(ctx runtime.RenderContext) {
	bounds := s.bounds
	if bounds.Width == 0 || bounds.Height == 0 {
		return
	}

	// Fill background
	ctx.Buffer.Fill(bounds, ' ', s.bgStyle)

	// Left side: status
	left := " " + s.status
	ctx.Buffer.SetString(bounds.X, bounds.Y, left, s.textStyle)

	// Right side: tokens + cost
	right := ""
	if s.tokens > 0 {
		right = formatTokens(s.tokens)
		if s.costCents > 0 {
			right += " · $" + formatCost(s.costCents)
		}
		right += " "
	}
	if right != "" {
		x := bounds.X + bounds.Width - len(right)
		if x > bounds.X+len(left) {
			ctx.Buffer.SetString(x, bounds.Y, right, s.textStyle)
		}
	}

	// Center: scroll position
	if s.scrollPos != "" {
		center := bounds.X + bounds.Width/2 - len(s.scrollPos)/2
		if center > bounds.X+len(left) && center+len(s.scrollPos) < bounds.X+bounds.Width-len(right) {
			ctx.Buffer.SetString(center, bounds.Y, s.scrollPos, s.textStyle)
		}
	}
}

// formatTokens formats a token count with K/M suffixes.
func formatTokens(n int) string {
	if n >= 1000000 {
		return itoa(n/1000000) + "." + itoa((n%1000000)/100000) + "M"
	}
	if n >= 1000 {
		return itoa(n/1000) + "." + itoa((n%1000)/100) + "K"
	}
	return itoa(n)
}

// formatCost formats cents as dollars.
func formatCost(cents float64) string {
	if cents >= 100 {
		return itoa(int(cents/100)) + "." + padZero(int(cents)%100)
	}
	return "0." + padZero(int(cents))
}

func padZero(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

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
