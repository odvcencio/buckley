package widgets

import (
	"strings"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/ui/backend"
	"m31labs.dev/buckley/pkg/ui/runtime"
	"m31labs.dev/buckley/pkg/ui/terminal"
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

// InputArea is the Buckley input widget with mode support.
type InputArea struct {
	FocusableBase

	text      strings.Builder
	cursorPos int
	mode      InputMode

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
	i.cursorPos = len(text)
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
	cursor := clampByteOffset(current, i.cursorPos)
	i.text.Reset()
	i.text.WriteString(current[:cursor])
	i.text.WriteString(s)
	i.text.WriteString(current[cursor:])
	i.cursorPos = cursor + len(s)
	i.notifyChange()
}

// Mode returns the current input mode.
func (i *InputArea) Mode() InputMode {
	return i.mode
}

// CursorPosition returns screen coordinates for the cursor.
func (i *InputArea) CursorPosition() (x, y int) {
	bounds := i.bounds
	lines := i.inputLines(inputContentWidth(bounds.Width))
	cursorLine, cursorCol := i.cursorLineCol(lines)

	// Account for scrolling in tall input
	maxVisibleLines := inputVisibleLineCount(bounds.Height)
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
	availWidth := inputContentWidth(i.bounds.Width)
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
	if col > runeLen(target.text) {
		col = runeLen(target.text)
	}
	i.cursorPos = target.start + byteOffsetForRuneCol(target.text, col)
	return true
}

// Measure returns the preferred size based on content.
func (i *InputArea) Measure(constraints runtime.Constraints) runtime.Size {
	lines := i.inputLines(inputContentWidth(constraints.MaxWidth))
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
	bounds := i.bounds
	if bounds.Width == 0 || bounds.Height == 0 {
		return
	}

	ctx.Buffer.Fill(bounds, ' ', i.bgStyle)
	i.renderTopBorder(ctx.Buffer, bounds)
	i.renderModeIndicator(ctx.Buffer, bounds)

	lines := i.inputLines(inputContentWidth(bounds.Width))
	startLine := i.visibleStartLine(lines, bounds.Height)
	i.renderInputLines(ctx.Buffer, bounds, lines, startLine)

	if i.focused {
		i.renderCursor(ctx.Buffer, bounds)
	}
}

func (i *InputArea) renderTopBorder(buf *runtime.Buffer, bounds runtime.Rect) {
	for x := 0; x < bounds.Width; x++ {
		buf.Set(bounds.X+x, bounds.Y, '─', i.borderStyle)
	}
}

func (i *InputArea) renderModeIndicator(buf *runtime.Buffer, bounds runtime.Rect) {
	modeStyle, modeChar := i.modeIndicator()
	buf.Set(bounds.X+1, bounds.Y+1, firstRune(modeChar), modeStyle)
}

func (i *InputArea) visibleStartLine(lines []inputLine, height int) int {
	maxVisibleLines := inputVisibleLineCount(height)
	if maxVisibleLines <= 0 {
		return 0
	}
	cursorLine, _ := i.cursorLineCol(lines)
	if cursorLine >= maxVisibleLines {
		return cursorLine - maxVisibleLines + 1
	}
	return 0
}

func (i *InputArea) renderInputLines(buf *runtime.Buffer, bounds runtime.Rect, lines []inputLine, startLine int) {
	maxVisibleLines := inputVisibleLineCount(bounds.Height)
	for j := 0; j < maxVisibleLines && startLine+j < len(lines); j++ {
		line := lines[startLine+j]
		y := bounds.Y + 1 + j
		buf.SetString(bounds.X+3, y, line.text, i.textStyle)
	}
}

func (i *InputArea) renderCursor(buf *runtime.Buffer, bounds runtime.Rect) {
	cursorX, cursorY := i.CursorPosition()
	if cursorX < bounds.X || cursorX >= bounds.X+bounds.Width ||
		cursorY < bounds.Y || cursorY >= bounds.Y+bounds.Height {
		return
	}
	buf.Set(cursorX, cursorY, i.cursorRune(), i.currentCursorStyle())
}

func (i *InputArea) cursorRune() rune {
	if r, ok := runeAtByteOffset(i.text.String(), i.cursorPos); ok && r != '\n' {
		return r
	}
	return ' '
}

func (i *InputArea) currentCursorStyle() backend.Style {
	if i.cursorStyleSet {
		return i.cursorStyle
	}
	return i.textStyle.Reverse(true)
}

func inputContentWidth(totalWidth int) int {
	width := totalWidth - 4
	if width < 10 {
		return 10
	}
	return width
}

func inputVisibleLineCount(height int) int {
	if height <= 1 {
		return 0
	}
	return height - 1
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
			lines = appendWrappedInputSegment(lines, segment, lineStart, width)
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
			col = runeColForByteOffset(line.text, pos-line.start)
			if col < 0 {
				col = 0
			}
			if col > runeLen(line.text) {
				col = runeLen(line.text)
			}
			return idx, col
		}
	}

	last := lines[len(lines)-1]
	return len(lines) - 1, runeLen(last.text)
}

func appendWrappedInputSegment(lines []inputLine, segment string, baseOffset, width int) []inputLine {
	if width <= 0 {
		width = 1
	}
	if segment == "" {
		return append(lines, inputLine{text: "", start: baseOffset, end: baseOffset})
	}

	chunkStart := 0
	runeCount := 0
	for byteOffset := range segment {
		if runeCount == width {
			lines = append(lines, inputLine{
				text:  segment[chunkStart:byteOffset],
				start: baseOffset + chunkStart,
				end:   baseOffset + byteOffset,
			})
			chunkStart = byteOffset
			runeCount = 0
		}
		runeCount++
	}
	if chunkStart < len(segment) {
		lines = append(lines, inputLine{
			text:  segment[chunkStart:],
			start: baseOffset + chunkStart,
			end:   baseOffset + len(segment),
		})
	}
	return lines
}

func firstRune(s string) rune {
	r, _ := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError && s == "" {
		return ' '
	}
	return r
}

func previousRuneOffset(s string, pos int) int {
	pos = clampByteOffset(s, pos)
	if pos <= 0 {
		return 0
	}
	previous := 0
	for offset := range s {
		if offset >= pos {
			break
		}
		previous = offset
	}
	return previous
}

func nextRuneOffset(s string, pos int) int {
	pos = clampByteOffset(s, pos)
	if pos >= len(s) {
		return len(s)
	}
	for offset := range s {
		if offset > pos {
			return offset
		}
	}
	return len(s)
}

func clampByteOffset(s string, pos int) int {
	if pos <= 0 {
		return 0
	}
	if pos >= len(s) {
		return len(s)
	}
	for pos > 0 && !utf8.RuneStart(s[pos]) {
		pos--
	}
	return pos
}

func runeAtByteOffset(s string, pos int) (rune, bool) {
	pos = clampByteOffset(s, pos)
	if pos >= len(s) {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(s[pos:])
	return r, true
}

func runeColForByteOffset(s string, pos int) int {
	pos = clampByteOffset(s, pos)
	return runeLen(s[:pos])
}

func byteOffsetForRuneCol(s string, col int) int {
	if col <= 0 {
		return 0
	}
	runeCount := 0
	for offset := range s {
		if runeCount == col {
			return offset
		}
		runeCount++
	}
	return len(s)
}

// HandleMessage processes keyboard input.
func (i *InputArea) HandleMessage(msg runtime.Message) runtime.HandleResult {
	if !i.focused {
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
		mode := i.mode
		if i.onSubmit != nil {
			i.onSubmit(text, mode)
		}
		return runtime.WithCommand(runtime.Submit{Text: text})

	case terminal.KeyBackspace:
		if i.cursorPos > 0 {
			text := i.text.String()
			prev := previousRuneOffset(text, i.cursorPos)
			i.text.Reset()
			i.text.WriteString(text[:prev])
			i.text.WriteString(text[i.cursorPos:])
			i.cursorPos = prev
			i.checkModeChange()
			i.notifyChange()
		}
		return runtime.Handled()

	case terminal.KeyDelete:
		text := i.text.String()
		if i.cursorPos < len(text) {
			next := nextRuneOffset(text, i.cursorPos)
			i.text.Reset()
			i.text.WriteString(text[:i.cursorPos])
			i.text.WriteString(text[next:])
			i.notifyChange()
		}
		return runtime.Handled()

	case terminal.KeyLeft:
		if i.cursorPos > 0 {
			i.cursorPos = previousRuneOffset(i.text.String(), i.cursorPos)
		}
		return runtime.Handled()

	case terminal.KeyRight:
		if i.cursorPos < i.text.Len() {
			i.cursorPos = nextRuneOffset(i.text.String(), i.cursorPos)
		}
		return runtime.Handled()

	case terminal.KeyUp:
		if i.moveCursorVertical(-1) {
			return runtime.Handled()
		}
		return runtime.Unhandled()

	case terminal.KeyDown:
		if i.moveCursorVertical(1) {
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
	cursor := clampByteOffset(text, i.cursorPos)
	i.text.Reset()
	i.text.WriteString(text[:cursor])
	i.text.WriteRune(r)
	i.text.WriteString(text[cursor:])
	i.cursorPos = cursor + len(string(r))
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
