package widgets

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/terminal"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// InteractiveSearch provides a search input overlay with mouse support.
type InteractiveSearch struct {
	uiwidgets.FocusableBase

	query        string
	matchCount   int
	currentMatch int
	label        string

	services runtime.Services

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

// NewInteractiveSearch creates a new search widget.
func NewInteractiveSearch() *InteractiveSearch {
	w := &InteractiveSearch{
		label:       "Search",
		bgStyle:     backend.DefaultStyle(),
		borderStyle: backend.DefaultStyle(),
		textStyle:   backend.DefaultStyle(),
		matchStyle:  backend.DefaultStyle().Foreground(backend.ColorYellow),
	}
	w.Base.Role = accessibility.RoleTextbox
	w.syncA11y()
	return w
}

// StyleType returns the selector type name.
func (s *InteractiveSearch) StyleType() string {
	return "Search"
}

// SetOnSearch sets the search callback.
func (s *InteractiveSearch) SetOnSearch(fn func(query string)) {
	if s == nil {
		return
	}
	s.onSearch = fn
}

// SetOnClose sets the close callback.
func (s *InteractiveSearch) SetOnClose(fn func()) {
	if s == nil {
		return
	}
	s.onClose = fn
}

// SetOnNavigate sets callbacks for navigating search matches.
func (s *InteractiveSearch) SetOnNavigate(next, prev func()) {
	if s == nil {
		return
	}
	s.onNext = next
	s.onPrev = prev
}

// SetStyles configures appearance.
func (s *InteractiveSearch) SetStyles(bg, border, text, match backend.Style) {
	if s == nil {
		return
	}
	s.bgStyle = bg
	s.borderStyle = border
	s.textStyle = text
	s.matchStyle = match
}

// SetMatchInfo updates the match count display.
func (s *InteractiveSearch) SetMatchInfo(current, total int) {
	if s == nil {
		return
	}
	s.currentMatch = current
	s.matchCount = total
	s.syncA11y()
}

// SetLabel updates the accessibility label.
func (s *InteractiveSearch) SetLabel(label string) {
	if s == nil {
		return
	}
	s.label = label
	s.syncA11y()
}

// Query returns the current search query.
func (s *InteractiveSearch) Query() string {
	return s.query
}

// SetQuery updates the search query and triggers callbacks.
func (s *InteractiveSearch) SetQuery(query string) {
	if s == nil {
		return
	}
	s.query = query
	s.syncA11y()
	if s.onSearch != nil {
		s.onSearch(query)
	}
}

// Measure returns the preferred size (fixed height bar).
func (s *InteractiveSearch) Measure(constraints runtime.Constraints) runtime.Size {
	if s == nil {
		return constraints.MinSize()
	}
	return runtime.Size{
		Width:  constraints.MaxWidth,
		Height: 1,
	}
}

// Layout positions at the bottom of the screen.
func (s *InteractiveSearch) Layout(bounds runtime.Rect) {
	size := s.Measure(runtime.Constraints{
		MinWidth:  bounds.Width,
		MaxWidth:  bounds.Width,
		MinHeight: 0,
		MaxHeight: bounds.Height,
	})
	height := size.Height
	if height <= 0 {
		height = 1
	}
	if height > bounds.Height {
		height = bounds.Height
	}
	newBounds := runtime.Rect{
		X:      bounds.X,
		Y:      bounds.Y + bounds.Height - height,
		Width:  bounds.Width,
		Height: height,
	}
	s.Base.Layout(newBounds)
}

// Render draws the search bar.
func (s *InteractiveSearch) Render(ctx runtime.RenderContext) {
	outer := s.Bounds()
	b := s.ContentBounds()
	buf := ctx.Buffer
	s.syncA11y()

	baseStyle := resolveBaseStyle(ctx, s, backend.DefaultStyle(), false)
	bgStyle := mergeBackendStyles(baseStyle, s.bgStyle)
	borderStyle := mergeBackendStyles(baseStyle, s.borderStyle)
	textStyle := mergeBackendStyles(baseStyle, s.textStyle)
	matchStyle := mergeBackendStyles(baseStyle, s.matchStyle)

	ctx.Buffer.Fill(outer, ' ', bgStyle)
	if b.Width <= 0 || b.Height <= 0 {
		return
	}

	buf.SetString(b.X, b.Y, "/ ", borderStyle)

	queryX := b.X + 2
	maxQuery := b.Width - 20
	query := s.query
	if textWidth(query) > maxQuery {
		query = clipStringRight(query, maxQuery)
	}
	buf.SetString(queryX, b.Y, query, textStyle)

	cursorX := queryX + textWidth(query)
	if cursorX < b.X+b.Width-15 && s.IsFocused() {
		buf.Set(cursorX, b.Y, '█', textStyle)
	}

	if s.matchCount > 0 {
		matchInfo := strconv.Itoa(s.currentMatch+1) + "/" + strconv.Itoa(s.matchCount)
		infoX := b.X + b.Width - textWidth(matchInfo) - 2
		buf.SetString(infoX, b.Y, matchInfo, matchStyle)
	} else if s.query != "" {
		noMatch := "No matches"
		infoX := b.X + b.Width - textWidth(noMatch) - 2
		buf.SetString(infoX, b.Y, noMatch, matchStyle)
	}
}

// HandleMessage processes keyboard and mouse input.
func (s *InteractiveSearch) HandleMessage(msg runtime.Message) runtime.HandleResult {
	switch ev := msg.(type) {
	case runtime.MouseMsg:
		return s.handleMouse(ev)
	case runtime.KeyMsg:
		switch ev.Key {
		case terminal.KeyEscape:
			s.query = ""
			s.syncA11y()
			if s.onSearch != nil {
				s.onSearch("")
			}
			if s.onClose != nil {
				s.onClose()
			}
			return runtime.WithCommand(runtime.PopOverlay{})
		case terminal.KeyEnter:
			if s.onClose != nil {
				s.onClose()
			}
			return runtime.WithCommand(runtime.PopOverlay{})
		case terminal.KeyUp:
			if s.onPrev != nil {
				s.onPrev()
			}
			return runtime.Handled()
		case terminal.KeyDown:
			if s.onNext != nil {
				s.onNext()
			}
			return runtime.Handled()
		case terminal.KeyBackspace:
			if len(s.query) > 0 {
				s.query = s.query[:len(s.query)-1]
				s.syncA11y()
				if s.onSearch != nil {
					s.onSearch(s.query)
				}
			}
			return runtime.Handled()
		case terminal.KeyRune:
			s.query += string(ev.Rune)
			s.syncA11y()
			if s.onSearch != nil {
				s.onSearch(s.query)
			}
			return runtime.Handled()
		}
	}
	return runtime.Unhandled()
}

func (s *InteractiveSearch) handleMouse(ev runtime.MouseMsg) runtime.HandleResult {
	if s == nil {
		return runtime.Unhandled()
	}
	bounds := s.Bounds()
	if !bounds.Contains(ev.X, ev.Y) {
		return runtime.Unhandled()
	}
	switch ev.Button {
	case runtime.MouseWheelUp:
		if s.onPrev != nil {
			s.onPrev()
		}
		return runtime.Handled()
	case runtime.MouseWheelDown:
		if s.onNext != nil {
			s.onNext()
		}
		return runtime.Handled()
	case runtime.MouseLeft:
		if ev.Action != runtime.MousePress {
			return runtime.Unhandled()
		}
		if !s.IsFocused() {
			s.Focus()
		}
		return runtime.Handled()
	}
	return runtime.Unhandled()
}

func (s *InteractiveSearch) syncA11y() {
	if s == nil {
		return
	}
	if s.Base.Role == "" {
		s.Base.Role = accessibility.RoleTextbox
	}
	label := strings.TrimSpace(s.label)
	if label == "" {
		label = "Search"
	}
	s.Base.Label = label
	if s.query != "" {
		s.Base.Value = &accessibility.ValueInfo{Text: s.query}
	} else {
		s.Base.Value = nil
	}
	s.Base.Description = fmt.Sprintf("%d matches", s.matchCount)
}

// Bind attaches app services.
func (s *InteractiveSearch) Bind(services runtime.Services) {
	if s == nil {
		return
	}
	s.services = services
}

// Unbind releases app services.
func (s *InteractiveSearch) Unbind() {
	if s == nil {
		return
	}
	s.services = runtime.Services{}
}

var _ runtime.Widget = (*InteractiveSearch)(nil)
var _ runtime.Focusable = (*InteractiveSearch)(nil)
