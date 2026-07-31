package widgets

import (
	"strconv"

	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/terminal"
)

// SearchWidget provides a search input overlay for the chat view.
type SearchWidget struct {
	FocusableBase

	query        string
	matchCount   int
	currentMatch int

	// Callbacks
	onSearch func(query string)
	onClose  func()
	onNext   func()
	onPrev   func()

	// Styles
	bgStyle     backend.Style
	borderStyle backend.Style
	textStyle   backend.Style
	matchStyle  backend.Style
}

type searchKeyHandler func(*SearchWidget, runtime.KeyMsg) runtime.HandleResult

var searchKeyHandlers = map[terminal.Key]searchKeyHandler{
	terminal.KeyEscape:    (*SearchWidget).handleEscapeKey,
	terminal.KeyEnter:     (*SearchWidget).handleEnterKey,
	terminal.KeyUp:        (*SearchWidget).handleUpKey,
	terminal.KeyDown:      (*SearchWidget).handleDownKey,
	terminal.KeyBackspace: (*SearchWidget).handleBackspaceKey,
}

// NewSearchWidget creates a new search widget.
func NewSearchWidget() *SearchWidget {
	return &SearchWidget{
		bgStyle:     backend.DefaultStyle(),
		borderStyle: backend.DefaultStyle(),
		textStyle:   backend.DefaultStyle(),
		matchStyle:  backend.DefaultStyle().Foreground(backend.ColorYellow),
	}
}

// SetOnSearch sets the search callback.
func (s *SearchWidget) SetOnSearch(fn func(query string)) {
	s.onSearch = fn
}

// SetOnClose sets the close callback.
func (s *SearchWidget) SetOnClose(fn func()) {
	s.onClose = fn
}

// SetOnNavigate sets callbacks for navigating search matches.
func (s *SearchWidget) SetOnNavigate(next, prev func()) {
	s.onNext = next
	s.onPrev = prev
}

// SetStyles configures appearance.
func (s *SearchWidget) SetStyles(bg, border, text, match backend.Style) {
	s.bgStyle = bg
	s.borderStyle = border
	s.textStyle = text
	s.matchStyle = match
}

// SetMatchInfo updates the match count display.
func (s *SearchWidget) SetMatchInfo(current, total int) {
	s.currentMatch = current
	s.matchCount = total
}

// Query returns the current search query.
func (s *SearchWidget) Query() string {
	return s.query
}

// Measure returns the preferred size (fixed height bar).
func (s *SearchWidget) Measure(constraints runtime.Constraints) runtime.Size {
	return runtime.Size{
		Width:  constraints.MaxWidth,
		Height: 1,
	}
}

// Layout positions at the bottom of the screen.
func (s *SearchWidget) Layout(bounds runtime.Rect) {
	s.bounds = runtime.Rect{
		X:      bounds.X,
		Y:      bounds.Y + bounds.Height - 1,
		Width:  bounds.Width,
		Height: 1,
	}
}

// Render draws the search bar.
func (s *SearchWidget) Render(ctx runtime.RenderContext) {
	b := s.bounds
	buf := ctx.Buffer

	s.renderBackground(buf, b)
	s.renderPrefix(buf, b)
	query := s.renderQuery(buf, b)
	s.renderCursor(buf, b, query)
	s.renderMatchInfo(buf, b)
}

func (s *SearchWidget) renderBackground(buf *runtime.Buffer, b runtime.Rect) {
	for x := b.X; x < b.X+b.Width; x++ {
		buf.Set(x, b.Y, ' ', s.bgStyle)
	}
}

func (s *SearchWidget) renderPrefix(buf *runtime.Buffer, b runtime.Rect) {
	buf.SetString(b.X, b.Y, "/ ", s.borderStyle)
}

func (s *SearchWidget) renderQuery(buf *runtime.Buffer, b runtime.Rect) string {
	queryX := b.X + 2
	query := suffixDisplayWidth(s.query, searchQueryWidth(b.Width))
	buf.SetString(queryX, b.Y, query, s.textStyle)
	return query
}

func (s *SearchWidget) renderCursor(buf *runtime.Buffer, b runtime.Rect, query string) {
	queryX := b.X + 2
	cursorX := queryX + displayWidth(query)
	if cursorX < b.X+b.Width-15 && s.focused {
		buf.Set(cursorX, b.Y, '█', s.textStyle)
	}
}

func (s *SearchWidget) renderMatchInfo(buf *runtime.Buffer, b runtime.Rect) {
	info := s.matchInfoText()
	if info == "" {
		return
	}
	infoX := b.X + b.Width - displayWidth(info) - 2
	if infoX >= b.X+2 {
		buf.SetString(infoX, b.Y, info, s.matchStyle)
	}
}

func (s *SearchWidget) matchInfoText() string {
	if s.matchCount > 0 {
		return strconv.Itoa(s.currentMatch+1) + "/" + strconv.Itoa(s.matchCount)
	}
	if s.query != "" {
		return "No matches"
	}
	return ""
}

func searchQueryWidth(width int) int {
	return max(0, width-20)
}

func suffixRunes(value string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxWidth {
		return value
	}
	return string(runes[len(runes)-maxWidth:])
}

// HandleMessage processes keyboard input.
func (s *SearchWidget) HandleMessage(msg runtime.Message) runtime.HandleResult {
	key, ok := msg.(runtime.KeyMsg)
	if !ok {
		return runtime.Unhandled()
	}

	if key.Key == terminal.KeyRune {
		return s.handleRuneKey(key)
	}
	if handler, ok := searchKeyHandlers[key.Key]; ok {
		return handler(s, key)
	}

	return runtime.Unhandled()
}

func (s *SearchWidget) handleEscapeKey(_ runtime.KeyMsg) runtime.HandleResult {
	s.query = ""
	s.notifySearch()
	s.notifyClose()
	return runtime.WithCommand(runtime.PopOverlay{})
}

func (s *SearchWidget) handleEnterKey(_ runtime.KeyMsg) runtime.HandleResult {
	s.notifyClose()
	return runtime.WithCommand(runtime.PopOverlay{})
}

func (s *SearchWidget) handleUpKey(_ runtime.KeyMsg) runtime.HandleResult {
	if s.onPrev != nil {
		s.onPrev()
	}
	return runtime.Handled()
}

func (s *SearchWidget) handleDownKey(_ runtime.KeyMsg) runtime.HandleResult {
	if s.onNext != nil {
		s.onNext()
	}
	return runtime.Handled()
}

func (s *SearchWidget) handleBackspaceKey(_ runtime.KeyMsg) runtime.HandleResult {
	if s.query != "" {
		s.query = dropLastRune(s.query)
		s.notifySearch()
	}
	return runtime.Handled()
}

func (s *SearchWidget) handleRuneKey(key runtime.KeyMsg) runtime.HandleResult {
	s.query += string(key.Rune)
	s.notifySearch()
	return runtime.Handled()
}

func (s *SearchWidget) notifySearch() {
	if s.onSearch != nil {
		s.onSearch(s.query)
	}
}

func (s *SearchWidget) notifyClose() {
	if s.onClose != nil {
		s.onClose()
	}
}
