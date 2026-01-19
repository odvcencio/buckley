package widgets

import (
	"fmt"
	"strings"

	"github.com/odvcencio/fluffy-ui/backend"
	"github.com/odvcencio/fluffy-ui/progress"
	"github.com/odvcencio/fluffy-ui/runtime"
	uiwidgets "github.com/odvcencio/fluffy-ui/widgets"
)

// StatusBar is the Buckley status bar widget.
type StatusBar struct {
	uiwidgets.Base
	status        string
	tokens        int
	costCents     float64
	contextUsed   int
	contextBudget int
	contextWindow int
	executionMode string
	scrollPos     string // "TOP", "END", or percentage
	progress      []progress.Progress
	streaming     bool
	streamAnim    int
	bgStyle       backend.Style
	textStyle     backend.Style
	contextBounds runtime.Rect
	tokenBounds   runtime.Rect
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

// SetContextUsage updates context usage display.
func (s *StatusBar) SetContextUsage(used, budget, window int) {
	s.contextUsed = used
	s.contextBudget = budget
	s.contextWindow = window
}

// SetExecutionMode updates execution mode display.
func (s *StatusBar) SetExecutionMode(mode string) {
	s.executionMode = mode
}

// SetScrollPosition updates the scroll position indicator.
func (s *StatusBar) SetScrollPosition(pos string) {
	s.scrollPos = pos
}

// SetProgress updates active progress indicators.
func (s *StatusBar) SetProgress(items []progress.Progress) {
	if len(items) == 0 {
		s.progress = nil
		return
	}
	s.progress = append([]progress.Progress(nil), items...)
}

// SetStreaming updates streaming state.
func (s *StatusBar) SetStreaming(streaming bool) {
	s.streaming = streaming
	if !streaming {
		s.streamAnim = 0
	}
}

// SetStreamAnim updates the streaming animation frame index.
func (s *StatusBar) SetStreamAnim(frame int) {
	if frame < 0 {
		frame = 0
	}
	s.streamAnim = frame
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
	bounds := s.Bounds()
	if bounds.Width == 0 || bounds.Height == 0 {
		return
	}

	// Fill background
	ctx.Buffer.Fill(bounds, ' ', s.bgStyle)

	// Left side: status + mode
	left := " " + s.status
	if strings.TrimSpace(s.executionMode) != "" {
		left += " · " + strings.TrimSpace(s.executionMode)
	}
	if activity := formatActivity(s.progress, s.streaming, s.streamAnim); activity != "" {
		left += " · " + activity
	}
	ctx.Buffer.SetString(bounds.X, bounds.Y, left, s.textStyle)

	// Right side: context + tokens/cost
	s.contextBounds = runtime.Rect{}
	s.tokenBounds = runtime.Rect{}
	ctxSegment := formatContextUsage(s.contextUsed, s.contextBudget, s.contextWindow)
	tokenSegment := ""
	if s.tokens > 0 {
		tokenSegment = formatTokens(s.tokens)
		if s.costCents > 0 {
			tokenSegment += " · $" + formatCost(s.costCents)
		}
	}
	right := ""
	if ctxSegment != "" {
		right = ctxSegment
		if tokenSegment != "" {
			combined := ctxSegment + " · " + tokenSegment
			if fitsRight(bounds, left, combined+" ") {
				right = combined
			}
		}
	} else if tokenSegment != "" {
		right = tokenSegment
	}
	if right != "" {
		drawText := right + " "
		x := bounds.X + bounds.Width - len(drawText)
		if x > bounds.X+len(left) {
			ctx.Buffer.SetString(x, bounds.Y, drawText, s.textStyle)
			if ctxSegment != "" && tokenSegment != "" && right == ctxSegment+" · "+tokenSegment {
				s.contextBounds = runtime.Rect{X: x, Y: bounds.Y, Width: len(ctxSegment), Height: 1}
				s.tokenBounds = runtime.Rect{
					X:      x + len(ctxSegment) + len(" · "),
					Y:      bounds.Y,
					Width:  len(tokenSegment),
					Height: 1,
				}
			} else if right == ctxSegment && ctxSegment != "" {
				s.contextBounds = runtime.Rect{X: x, Y: bounds.Y, Width: len(ctxSegment), Height: 1}
			} else if right == tokenSegment && tokenSegment != "" {
				s.tokenBounds = runtime.Rect{X: x, Y: bounds.Y, Width: len(tokenSegment), Height: 1}
			}
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

// WebLinkAt returns a status bar link target at the given point.
func (s *StatusBar) WebLinkAt(x, y int) (string, bool) {
	if s.contextBounds.Contains(x, y) {
		return "context", true
	}
	if s.tokenBounds.Contains(x, y) {
		return "usage", true
	}
	return "", false
}

func fitsRight(bounds runtime.Rect, left, right string) bool {
	x := bounds.X + bounds.Width - len(right)
	return x > bounds.X+len(left)
}

func formatActivity(items []progress.Progress, streaming bool, streamAnim int) string {
	var parts []string
	if progress := formatProgress(items); progress != "" {
		parts = append(parts, progress)
	}
	if streaming {
		parts = append(parts, "stream "+streamFrame(streamAnim))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

func formatProgress(items []progress.Progress) string {
	if len(items) == 0 {
		return ""
	}
	entry := items[0]
	label := strings.TrimSpace(entry.Label)
	if label == "" {
		label = "Working"
	}
	suffix := ""
	switch entry.Type {
	case progress.ProgressSteps:
		if entry.Total > 0 {
			suffix = fmt.Sprintf(" %d/%d", entry.Current, entry.Total)
		}
	case progress.ProgressDeterminate:
		if entry.Total > 0 {
			pct := int(entry.Percent*100 + 0.5)
			suffix = fmt.Sprintf(" %d%%", pct)
		}
	}
	if len(items) > 1 {
		suffix += fmt.Sprintf(" +%d", len(items)-1)
	}
	return label + suffix
}

func streamFrame(frame int) string {
	frames := []string{"|", "/", "-", "\\"}
	if len(frames) == 0 {
		return ""
	}
	if frame < 0 {
		frame = 0
	}
	return frames[frame%len(frames)]
}

func formatContextUsage(used, budget, window int) string {
	if used <= 0 && budget <= 0 && window <= 0 {
		return ""
	}
	usedStr := formatTokens(used)
	if budget > 0 && window > 0 && budget != window {
		return "ctx " + usedStr + "/" + formatTokens(budget) + " (" + formatTokens(window) + ")"
	}
	denom := budget
	if denom <= 0 {
		denom = window
	}
	if denom > 0 {
		return "ctx " + usedStr + "/" + formatTokens(denom)
	}
	return "ctx " + usedStr
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
