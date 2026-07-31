package widgets

import (
	"fmt"
	"strconv"

	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/runtime"
)

// StatusBar is the Buckley status bar widget.
type StatusBar struct {
	Base
	status    string
	tokens    int
	costCents float64
	scrollPos string // "TOP", "END", or percentage
	bgStyle   backend.Style
	textStyle backend.Style
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

// Status returns the current status text.
func (s *StatusBar) Status() string {
	return s.status
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

	ctx.Buffer.Fill(bounds, ' ', s.bgStyle)
	left := s.renderLeftSegment(ctx.Buffer, bounds)
	right := s.renderRightSegment(ctx.Buffer, bounds, left)
	s.renderScrollSegment(ctx.Buffer, bounds, left, right)
}

func (s *StatusBar) renderLeftSegment(buf *runtime.Buffer, bounds runtime.Rect) statusSegment {
	text := truncateString(" "+s.status, bounds.Width)
	segment := newStatusSegment(bounds.X, text)
	buf.SetString(segment.x, bounds.Y, segment.text, s.textStyle)
	return segment
}

func (s *StatusBar) renderRightSegment(buf *runtime.Buffer, bounds runtime.Rect, left statusSegment) statusSegment {
	text := s.rightText()
	if text == "" {
		return statusSegment{}
	}

	segment := newStatusSegment(bounds.X+bounds.Width-displayWidth(text), text)
	if segment.x <= left.end() {
		return statusSegment{}
	}

	buf.SetString(segment.x, bounds.Y, segment.text, s.textStyle)
	return segment
}

func (s *StatusBar) renderScrollSegment(buf *runtime.Buffer, bounds runtime.Rect, left, right statusSegment) {
	if s.scrollPos == "" {
		return
	}

	text := truncateString(s.scrollPos, bounds.Width)
	segment := newStatusSegment(bounds.X+bounds.Width/2-displayWidth(text)/2, text)
	rightEdge := bounds.X + bounds.Width
	if right.text != "" {
		rightEdge = right.x
	}
	if segment.x <= left.end() || segment.end() >= rightEdge {
		return
	}

	buf.SetString(segment.x, bounds.Y, segment.text, s.textStyle)
}

func (s *StatusBar) rightText() string {
	if s.tokens <= 0 {
		return ""
	}

	text := formatTokens(s.tokens)
	if s.costCents > 0 {
		text += " · $" + formatCost(s.costCents)
	}
	return text + " "
}

type statusSegment struct {
	x     int
	text  string
	width int
}

func newStatusSegment(x int, text string) statusSegment {
	return statusSegment{x: x, text: text, width: displayWidth(text)}
}

func (s statusSegment) end() int {
	return s.x + s.width
}

// formatTokens formats a token count with K/M suffixes.
func formatTokens(n int) string {
	if n >= 1000000 {
		return strconv.Itoa(n/1000000) + "." + strconv.Itoa((n%1000000)/100000) + "M"
	}
	if n >= 1000 {
		return strconv.Itoa(n/1000) + "." + strconv.Itoa((n%1000)/100) + "K"
	}
	return strconv.Itoa(n)
}

// formatCost formats cents as dollars.
func formatCost(cents float64) string {
	wholeCents := int(cents)
	return fmt.Sprintf("%d.%02d", wholeCents/100, wholeCents%100)
}
