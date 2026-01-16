package buckley

import (
	"strings"

	"github.com/odvcencio/buckley/pkg/ui/backend"
	"github.com/odvcencio/buckley/pkg/ui/runtime"
	"github.com/odvcencio/buckley/pkg/ui/terminal"
	uiwidgets "github.com/odvcencio/buckley/pkg/ui/widgets"
)

// InputMode represents the current input mode.
type InputMode int

const (
	ModeNormal InputMode = iota
	ModeShell            // !
	ModeEnv              // $
	ModeSearch           // /
	ModePicker           // @
)

// InputArea is a mode-aware input widget.
type InputArea struct {
	uiwidgets.FocusableBase

	text      strings.Builder
	cursorPos int
	mode      InputMode

	// History navigation
	history      []string // Past inputs (oldest first)
	historyIndex int      // Current history position (-1 = not navigating)
	historyMax   int      // Max history entries to keep
	savedText    string   // Saved current text when navigating history

	// Minimum and maximum height
	minHeight int
	maxHeight int

	// Styles
	bgStyle     backend.Style
	textStyle   backend.Style
	borderStyle backend.Style
	normalMode  backend.Style
	shellMode   backend.Style
	envMode     backend.Style
	searchMode  backend.Style

	// Mode symbols
	normalSymbol string
	shellSymbol  string
	envSymbol    string
	searchSymbol string

	// Cursor styling (soft cursor)
	cursorStyle    backend.Style
	cursorStyleSet bool

	// Callbacks
	onSubmit              func(text string, mode InputMode)
	onChange              func(text string)
	onModeChange          func(mode InputMode)
	onTriggerPicker       func()
	onTriggerSearch       func()
	onTriggerSlashCommand func()
}

type inputLine struct {
	text  string
	start int
	end   int
}

// NewInputArea creates a new input area widget.
func NewInputArea() *InputArea {
	return &InputArea{
		minHeight:    2,
		maxHeight:    10,
		historyIndex: -1,
		historyMax:   100,
		bgStyle:      backend.DefaultStyle(),
		textStyle:    backend.DefaultStyle(),
		borderStyle:  backend.DefaultStyle(),
		normalMode:   backend.DefaultStyle(),
		shellMode:    backend.DefaultStyle().Bold(true),
		envMode:      backend.DefaultStyle().Bold(true),
		searchMode:   backend.DefaultStyle().Bold(true),
		normalSymbol: "λ",
		shellSymbol:  "!",
		envSymbol:    "$",
		searchSymbol: "/",
	}
}

// AddToHistory adds an entry to the input history.
func (i *InputArea) AddToHistory(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	// Don't add duplicates of the most recent entry
	if len(i.history) > 0 && i.history[len(i.history)-1] == text {
		return
	}
	i.history = append(i.history, text)
	// Trim to max size
	if len(i.history) > i.historyMax {
		i.history = i.history[len(i.history)-i.historyMax:]
	}
	i.historyIndex = -1
}

// historyPrev navigates to the previous history entry.
func (i *InputArea) historyPrev() bool {
	if len(i.history) == 0 {
		return false
	}
	if i.historyIndex == -1 {
		// Save current text before navigating
		i.savedText = i.text.String()
		i.historyIndex = len(i.history) - 1
	} else if i.historyIndex > 0 {
		i.historyIndex--
	} else {
		return false // Already at oldest
	}
	i.SetText(i.history[i.historyIndex])
	return true
}

// historyNext navigates to the next history entry.
func (i *InputArea) historyNext() bool {
	if i.historyIndex == -1 {
		return false // Not in history mode
	}
	if i.historyIndex < len(i.history)-1 {
		i.historyIndex++
		i.SetText(i.history[i.historyIndex])
	} else {
		// Return to saved text
		i.historyIndex = -1
		i.SetText(i.savedText)
	}
	return true
}

// ResetHistory resets the history navigation state (call when text is modified).
func (i *InputArea) resetHistoryNav() {
	i.historyIndex = -1
	i.savedText = ""
}

// SetStyles sets the input area styles.
func (i *InputArea) SetStyles(bg, text, border backend.Style) {
	i.bgStyle = bg
	i.textStyle = text
	i.borderStyle = border
}

// SetModeStyles sets the mode indicator styles.
func (i *InputArea) SetModeStyles(normal, shell, env, search backend.Style) {
	i.normalMode = normal
	i.shellMode = shell
	i.envMode = env
	i.searchMode = search
}

// SetCursorStyle sets the style used for the soft cursor.
func (i *InputArea) SetCursorStyle(style backend.Style) {
	i.cursorStyle = style
	i.cursorStyleSet = true
}

// ClearCursorStyle resets to the default cursor styling.
func (i *InputArea) ClearCursorStyle() {
	i.cursorStyleSet = false
}

// SetHeightLimits sets min/max height.
func (i *InputArea) SetHeightLimits(min, max int) {
	i.minHeight = min
	i.maxHeight = max
}

// OnSubmit sets the submit callback.
func (i *InputArea) OnSubmit(fn func(text string, mode InputMode)) {
	i.onSubmit = fn
}

// OnChange sets the text change callback.
func (i *InputArea) OnChange(fn func(text string)) {
	i.onChange = fn
}

// OnModeChange sets the mode change callback.
func (i *InputArea) OnModeChange(fn func(mode InputMode)) {
	i.onModeChange = fn
}

// OnTriggerPicker sets the file picker trigger callback.
func (i *InputArea) OnTriggerPicker(fn func()) {
	i.onTriggerPicker = fn
}

// OnTriggerSearch sets the search trigger callback.
func (i *InputArea) OnTriggerSearch(fn func()) {
	i.onTriggerSearch = fn
}

// OnTriggerSlashCommand sets the slash command trigger callback.
func (i *InputArea) OnTriggerSlashCommand(fn func()) {
	i.onTriggerSlashCommand = fn
}

// Text returns the current input text.
func (i *InputArea) Text() string {
	return i.text.String()
}

// SetText sets the input text.
func (i *InputArea) SetText(text string) {
	i.text.Reset()
	i.text.WriteString(text)
	i.cursorPos = i.text.Len()
}

// Clear clears the input.
func (i *InputArea) Clear() {
	i.text.Reset()
	i.cursorPos = 0
	i.mode = ModeNormal
}

// HasText returns true if there's text in the input.
func (i *InputArea) HasText() bool {
	return i.text.Len() > 0
}

// InsertText inserts text at the cursor position.
// Used for paste operations.
func (i *InputArea) InsertText(s string) {
	if s == "" {
		return
	}
	current := i.text.String()
	i.text.Reset()
	i.text.WriteString(current[:i.cursorPos])
	i.text.WriteString(s)
	i.text.WriteString(current[i.cursorPos:])
	i.cursorPos += len(s)
	i.notifyChange()
}

// Mode returns the current input mode.
func (i *InputArea) Mode() InputMode {
	return i.mode
}

// CursorPosition returns screen coordinates for the cursor.
func (i *InputArea) CursorPosition() (x, y int) {
	bounds := i.Bounds()
	availWidth := bounds.Width - 4 // mode indicator + padding
	if availWidth < 10 {
		availWidth = 10
	}

	lines := i.inputLines(availWidth)
	cursorLine, cursorCol := i.cursorLineCol(lines)

	// Account for scrolling in tall input
	maxVisibleLines := bounds.Height - 1 // minus border
	if maxVisibleLines < 1 {
		return bounds.X + 3, bounds.Y + 1
	}
	startLine := 0
	if cursorLine >= startLine+maxVisibleLines {
		startLine = cursorLine - maxVisibleLines + 1
	}

	visibleCursorLine := cursorLine - startLine
	if visibleCursorLine < 0 {
		visibleCursorLine = 0
	}
	if visibleCursorLine >= maxVisibleLines {
		visibleCursorLine = maxVisibleLines - 1
	}

	return bounds.X + 3 + cursorCol, bounds.Y + 1 + visibleCursorLine
}

func (i *InputArea) moveCursorVertical(delta int) bool {
	if delta == 0 {
		return false
	}
	bounds := i.Bounds()
	availWidth := bounds.Width - 4
	if availWidth < 10 {
		availWidth = 10
	}
	if availWidth <= 0 {
		return false
	}

	lines := i.inputLines(availWidth)
	if len(lines) < 2 {
		return false
	}
	line, col := i.cursorLineCol(lines)
	targetLine := line + delta
	if targetLine < 0 || targetLine >= len(lines) {
		return false
	}
	target := lines[targetLine]
	if col > len(target.text) {
		col = len(target.text)
	}
	i.cursorPos = target.start + col
	return true
}

// Measure returns the preferred size based on content.
func (i *InputArea) Measure(constraints runtime.Constraints) runtime.Size {
	availWidth := constraints.MaxWidth - 4
	if availWidth < 10 {
		availWidth = 10
	}

	lines := i.inputLines(availWidth)
	lineCount := len(lines)
	if lineCount == 0 {
		lineCount = 1
	}

	// Add 1 for border, clamp to limits
	height := lineCount + 1
	if height < i.minHeight {
		height = i.minHeight
	}
	if height > i.maxHeight {
		height = i.maxHeight
	}

	return runtime.Size{
		Width:  constraints.MaxWidth,
		Height: height,
	}
}

// Render draws the input area.
func (i *InputArea) Render(ctx runtime.RenderContext) {
	bounds := i.Bounds()
	if bounds.Width == 0 || bounds.Height == 0 {
		return
	}

	// Fill background
	ctx.Buffer.Fill(bounds, ' ', i.bgStyle)

	// Draw top border
	for x := 0; x < bounds.Width; x++ {
		ctx.Buffer.Set(bounds.X+x, bounds.Y, '─', i.borderStyle)
	}

	// Draw mode indicator
	modeStyle, modeChar := i.modeIndicator()
	ctx.Buffer.Set(bounds.X+1, bounds.Y+1, rune(modeChar[0]), modeStyle)

	// Draw text with wrapping
	availWidth := bounds.Width - 4
	if availWidth < 10 {
		availWidth = 10
	}

	lines := i.inputLines(availWidth)
	if len(lines) == 0 {
		lines = []inputLine{{text: "", start: 0, end: 0}}
	}

	// Calculate scroll
	maxVisibleLines := bounds.Height - 1
	cursorLine, _ := i.cursorLineCol(lines)
	startLine := 0
	if cursorLine >= startLine+maxVisibleLines {
		startLine = cursorLine - maxVisibleLines + 1
	}

	// Render visible lines
	for j := 0; j < maxVisibleLines && startLine+j < len(lines); j++ {
		line := lines[startLine+j]
		y := bounds.Y + 1 + j
		ctx.Buffer.SetString(bounds.X+3, y, line.text, i.textStyle)
	}

	// Draw cursor if focused
	if i.IsFocused() {
		cursorX, cursorY := i.CursorPosition()
		if cursorX >= bounds.X && cursorX < bounds.X+bounds.Width &&
			cursorY >= bounds.Y && cursorY < bounds.Y+bounds.Height {
			var ch rune = ' '
			if i.cursorPos < i.text.Len() {
				ch = rune(i.text.String()[i.cursorPos])
				if ch == '\n' {
					ch = ' '
				}
			}
			cursorStyle := i.textStyle.Reverse(true)
			if i.cursorStyleSet {
				cursorStyle = i.cursorStyle
			}
			ctx.Buffer.Set(cursorX, cursorY, ch, cursorStyle)
		}
	}
}

func (i *InputArea) inputLines(width int) []inputLine {
	if width <= 0 {
		width = 1
	}
	text := i.text.String()
	if text == "" {
		return []inputLine{{text: "", start: 0, end: 0}}
	}

	lines := make([]inputLine, 0, (len(text)/width)+1)
	lineStart := 0
	for idx := 0; idx <= len(text); idx++ {
		if idx == len(text) || text[idx] == '\n' {
			segment := text[lineStart:idx]
			if segment == "" {
				lines = append(lines, inputLine{text: "", start: lineStart, end: lineStart})
			} else {
				for segStart := 0; segStart < len(segment); segStart += width {
					segEnd := segStart + width
					if segEnd > len(segment) {
						segEnd = len(segment)
					}
					lines = append(lines, inputLine{
						text:  segment[segStart:segEnd],
						start: lineStart + segStart,
						end:   lineStart + segEnd,
					})
				}
			}
			lineStart = idx + 1
		}
	}

	if len(lines) == 0 {
		return []inputLine{{text: "", start: 0, end: 0}}
	}
	return lines
}

func (i *InputArea) cursorLineCol(lines []inputLine) (line, col int) {
	if len(lines) == 0 {
		return 0, 0
	}
	textLen := i.text.Len()
	pos := i.cursorPos
	if pos < 0 {
		pos = 0
	}
	if pos > textLen {
		pos = textLen
	}

	for idx, line := range lines {
		if pos <= line.end {
			col = pos - line.start
			if col < 0 {
				col = 0
			}
			if col > len(line.text) {
				col = len(line.text)
			}
			return idx, col
		}
	}

	last := lines[len(lines)-1]
	return len(lines) - 1, len(last.text)
}

// HandleMessage processes keyboard input.
func (i *InputArea) HandleMessage(msg runtime.Message) runtime.HandleResult {
	if !i.IsFocused() {
		return runtime.Unhandled()
	}

	key, ok := msg.(runtime.KeyMsg)
	if !ok {
		return runtime.Unhandled()
	}

	switch key.Key {
	case terminal.KeyEnter:
		text := strings.TrimSpace(i.text.String())
		if text == "" {
			return runtime.Handled()
		}
		// Add to history before submitting
		i.AddToHistory(text)
		mode := i.mode
		if i.onSubmit != nil {
			i.onSubmit(text, mode)
		}
		return runtime.WithCommand(runtime.Submit{Text: text})

	case terminal.KeyBackspace:
		if i.cursorPos > 0 {
			text := i.text.String()
			i.text.Reset()
			i.text.WriteString(text[:i.cursorPos-1])
			i.text.WriteString(text[i.cursorPos:])
			i.cursorPos--
			i.resetHistoryNav()
			i.checkModeChange()
			i.notifyChange()
		}
		return runtime.Handled()

	case terminal.KeyDelete:
		text := i.text.String()
		if i.cursorPos < len(text) {
			i.text.Reset()
			i.text.WriteString(text[:i.cursorPos])
			i.text.WriteString(text[i.cursorPos+1:])
			i.resetHistoryNav()
			i.notifyChange()
		}
		return runtime.Handled()

	case terminal.KeyLeft:
		if i.cursorPos > 0 {
			i.cursorPos--
		}
		return runtime.Handled()

	case terminal.KeyRight:
		if i.cursorPos < i.text.Len() {
			i.cursorPos++
		}
		return runtime.Handled()

	case terminal.KeyUp:
		// First try moving cursor up in multiline text
		if i.moveCursorVertical(-1) {
			return runtime.Handled()
		}
		// If can't move up (at first line), navigate history
		if i.historyPrev() {
			return runtime.Handled()
		}
		return runtime.Unhandled()

	case terminal.KeyDown:
		// First try moving cursor down in multiline text
		if i.moveCursorVertical(1) {
			return runtime.Handled()
		}
		// If can't move down (at last line), navigate history
		if i.historyNext() {
			return runtime.Handled()
		}
		return runtime.Unhandled()

	case terminal.KeyHome:
		i.cursorPos = 0
		return runtime.Handled()

	case terminal.KeyEnd:
		i.cursorPos = i.text.Len()
		return runtime.Handled()

	case terminal.KeyEscape:
		i.mode = ModeNormal
		if i.onModeChange != nil {
			i.onModeChange(i.mode)
		}
		return runtime.WithCommand(runtime.Cancel{})

	case terminal.KeyRune:
		return i.handleRune(key.Rune)

	case terminal.KeyCtrlC:
		if i.HasText() {
			i.Clear()
			return runtime.Handled()
		}
		return runtime.Unhandled()
	}

	return runtime.Unhandled()
}

func (i *InputArea) handleRune(r rune) runtime.HandleResult {
	// Check for mode triggers at start of input
	if i.text.Len() == 0 {
		switch r {
		case '!':
			i.mode = ModeShell
			if i.onModeChange != nil {
				i.onModeChange(i.mode)
			}
		case '$':
			i.mode = ModeEnv
			if i.onModeChange != nil {
				i.onModeChange(i.mode)
			}
		case '/':
			// Trigger slash command palette for command selection
			if i.onTriggerSlashCommand != nil {
				i.onTriggerSlashCommand()
				return runtime.Handled()
			}
			i.mode = ModeNormal
		case '@':
			if i.onTriggerPicker != nil {
				i.onTriggerPicker()
				return runtime.Handled()
			}
		default:
			i.mode = ModeNormal
		}
	}

	// Insert character
	text := i.text.String()
	i.text.Reset()
	i.text.WriteString(text[:i.cursorPos])
	i.text.WriteRune(r)
	i.text.WriteString(text[i.cursorPos:])
	i.cursorPos++
	i.resetHistoryNav() // Reset history navigation when text is modified
	i.notifyChange()

	return runtime.Handled()
}

func (i *InputArea) checkModeChange() {
	// Reset mode if input is now empty
	if i.text.Len() == 0 {
		i.mode = ModeNormal
		if i.onModeChange != nil {
			i.onModeChange(i.mode)
		}
	}
}

func (i *InputArea) modeIndicator() (backend.Style, string) {
	switch i.mode {
	case ModeShell:
		return i.shellMode, i.shellSymbol
	case ModeEnv:
		return i.envMode, i.envSymbol
	case ModeSearch:
		return i.searchMode, i.searchSymbol
	default:
		return i.normalMode, i.normalSymbol
	}
}

func (i *InputArea) notifyChange() {
	if i.onChange != nil {
		i.onChange(i.text.String())
	}
}
