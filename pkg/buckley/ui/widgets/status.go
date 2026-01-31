package widgets

import (
	"fmt"
	"strings"

	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/state"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// StatusBar displays status information at the bottom of the screen.
type StatusBar struct {
	uiwidgets.Component

	statusText     state.Readable[string]
	statusOverride state.Readable[string]
	statusMode     state.Readable[string]
	tokens         state.Readable[int]
	costCents      state.Readable[float64]
	contextUsed    state.Readable[int]
	contextBudget  state.Readable[int]
	contextWindow  state.Readable[int]
	scrollPos      state.Readable[string]
	progressItems  state.Readable[[]progress.Progress]
	isStreaming    state.Readable[bool]

	bgStyle   backend.Style
	textStyle backend.Style
	modeStyle backend.Style
}

// StatusBarConfig configures the status bar.
type StatusBarConfig struct {
	StatusText     state.Readable[string]
	StatusOverride state.Readable[string]
	StatusMode     state.Readable[string]
	Tokens         state.Readable[int]
	CostCents      state.Readable[float64]
	ContextUsed    state.Readable[int]
	ContextBudget  state.Readable[int]
	ContextWindow  state.Readable[int]
	ScrollPos      state.Readable[string]
	ProgressItems  state.Readable[[]progress.Progress]
	IsStreaming    state.Readable[bool]

	BGStyle   backend.Style
	TextStyle backend.Style
	ModeStyle backend.Style
}

// NewStatusBar creates a new status bar.
func NewStatusBar(cfg StatusBarConfig) *StatusBar {
	defaultStyle := backend.DefaultStyle()
	bg := cfg.BGStyle
	if bg == (backend.Style{}) {
		bg = defaultStyle
	}
	text := cfg.TextStyle
	if text == (backend.Style{}) {
		text = defaultStyle
	}
	mode := cfg.ModeStyle
	if mode == (backend.Style{}) {
		mode = text
	}

	return &StatusBar{
		statusText:     readableOrDefault(cfg.StatusText, "Ready"),
		statusOverride: readableOrDefault(cfg.StatusOverride, ""),
		statusMode:     readableOrDefault(cfg.StatusMode, ""),
		tokens:         readableOrDefault(cfg.Tokens, 0),
		costCents:      readableOrDefault(cfg.CostCents, 0.0),
		contextUsed:    readableOrDefault(cfg.ContextUsed, 0),
		contextBudget:  readableOrDefault(cfg.ContextBudget, 0),
		contextWindow:  readableOrDefault(cfg.ContextWindow, 0),
		scrollPos:      readableOrDefault(cfg.ScrollPos, ""),
		progressItems:  readableOrDefault(cfg.ProgressItems, []progress.Progress{}),
		isStreaming:    readableOrDefault(cfg.IsStreaming, false),
		bgStyle:        bg,
		textStyle:      text,
		modeStyle:      mode,
	}
}

// Bind attaches app services and subscriptions.
func (s *StatusBar) Bind(services runtime.Services) {
	s.Component.Bind(services)
	s.Subs.Clear()
	invalidate := func() {
		if s.Services != (runtime.Services{}) {
			s.Services.Invalidate()
		}
	}
	s.Observe(s.statusText, invalidate)
	s.Observe(s.statusOverride, invalidate)
	s.Observe(s.statusMode, invalidate)
	s.Observe(s.tokens, invalidate)
	s.Observe(s.costCents, invalidate)
	s.Observe(s.contextUsed, invalidate)
	s.Observe(s.contextBudget, invalidate)
	s.Observe(s.contextWindow, invalidate)
	s.Observe(s.scrollPos, invalidate)
	s.Observe(s.progressItems, invalidate)
	s.Observe(s.isStreaming, invalidate)
}

// Unbind releases subscriptions.
func (s *StatusBar) Unbind() {
	s.Component.Unbind()
}

// Measure returns the preferred size.
func (s *StatusBar) Measure(constraints runtime.Constraints) runtime.Size {
	return runtime.Size{
		Width:  constraints.MaxWidth,
		Height: 1,
	}
}

// Layout positions the status bar.
func (s *StatusBar) Layout(bounds runtime.Rect) {
	s.Base.Layout(bounds)
}

// Render draws the status bar.
func (s *StatusBar) Render(ctx runtime.RenderContext) {
	bounds := s.Bounds()
	if bounds.Width == 0 || bounds.Height == 0 {
		return
	}

	// Fill background
	ctx.Buffer.Fill(bounds, ' ', s.bgStyle)

	status := strings.TrimSpace(s.statusText.Get())
	if override := strings.TrimSpace(s.statusOverride.Get()); override != "" {
		status = override
	}
	if status == "" {
		status = "Ready"
	}

	mode := strings.TrimSpace(s.statusMode.Get())
	activity := formatActivity(s.progressItems.Get(), s.isStreaming.Get())

	left := " " + status
	if mode != "" {
		left += " · " + mode
	}
	if activity != "" {
		left += " · " + activity
	}

	ctx.Buffer.SetString(bounds.X, bounds.Y, left, s.textStyle)

	if mode != "" {
		prefix := " " + status + " · "
		ctx.Buffer.SetString(bounds.X+len(prefix), bounds.Y, mode, s.modeStyle)
	}

	ctxSegment := formatContextUsage(s.contextUsed.Get(), s.contextBudget.Get(), s.contextWindow.Get())
	tokenSegment := formatTokenSegment(s.tokens.Get(), s.costCents.Get())
	right := joinRightSegments(ctxSegment, tokenSegment, bounds, left)
	if right != "" {
		drawText := right + " "
		x := bounds.X + bounds.Width - len(drawText)
		if x > bounds.X+len(left) {
			ctx.Buffer.SetString(x, bounds.Y, drawText, s.textStyle)
		}
	}

	if scroll := strings.TrimSpace(s.scrollPos.Get()); scroll != "" {
		center := bounds.X + bounds.Width/2 - len(scroll)/2
		if center > bounds.X+len(left) && center+len(scroll) < bounds.X+bounds.Width-len(right) {
			ctx.Buffer.SetString(center, bounds.Y, scroll, s.textStyle)
		}
	}
}

func joinRightSegments(ctxSegment, tokenSegment string, bounds runtime.Rect, left string) string {
	if ctxSegment == "" && tokenSegment == "" {
		return ""
	}
	if ctxSegment != "" && tokenSegment != "" {
		combined := ctxSegment + " · " + tokenSegment
		if fitsRight(bounds, left, combined+" ") {
			return combined
		}
		return ctxSegment
	}
	if ctxSegment != "" {
		return ctxSegment
	}
	return tokenSegment
}

func formatTokenSegment(tokens int, costCents float64) string {
	if tokens <= 0 {
		return ""
	}
	segment := formatTokens(tokens)
	if costCents > 0 {
		segment += " · $" + formatCost(costCents)
	}
	return segment
}

func fitsRight(bounds runtime.Rect, left, right string) bool {
	x := bounds.X + bounds.Width - len(right)
	return x > bounds.X+len(left)
}

func formatActivity(items []progress.Progress, streaming bool) string {
	var parts []string
	if progress := formatProgress(items); progress != "" {
		parts = append(parts, progress)
	}
	if streaming {
		parts = append(parts, "stream")
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

func readableOrDefault[T any](sig state.Readable[T], fallback T) state.Readable[T] {
	if sig != nil {
		return sig
	}
	return state.NewSignal(fallback)
}

var _ runtime.Widget = (*StatusBar)(nil)
var _ accessibility.Accessible = (*StatusBar)(nil)

// AccessibleRole returns the accessibility role for the status bar.
func (s *StatusBar) AccessibleRole() accessibility.Role {
	return accessibility.RoleStatus
}

// AccessibleLabel returns the current status text.
func (s *StatusBar) AccessibleLabel() string {
	status := strings.TrimSpace(s.statusText.Get())
	if override := strings.TrimSpace(s.statusOverride.Get()); override != "" {
		status = override
	}
	if status == "" {
		return "Status"
	}
	return status
}

// AccessibleDescription returns detailed status information.
func (s *StatusBar) AccessibleDescription() string {
	var parts []string
	if s.isStreaming.Get() {
		parts = append(parts, "streaming")
	}
	if mode := strings.TrimSpace(s.statusMode.Get()); mode != "" {
		parts = append(parts, mode)
	}
	if s.contextBudget.Get() > 0 {
		parts = append(parts, fmt.Sprintf("ctx %d/%d", s.contextUsed.Get(), s.contextBudget.Get()))
	}
	if s.tokens.Get() > 0 {
		parts = append(parts, fmt.Sprintf("%d tokens", s.tokens.Get()))
	}
	return strings.Join(parts, " · ")
}

// AccessibleState returns the current state of the status bar.
func (s *StatusBar) AccessibleState() accessibility.StateSet {
	return accessibility.StateSet{
		ReadOnly: true,
	}
}

// AccessibleValue returns nil (status bar doesn't have a numeric value).
func (s *StatusBar) AccessibleValue() *accessibility.ValueInfo {
	return nil
}
