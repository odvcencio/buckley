package widgets

import (
	"strings"

	"github.com/odvcencio/fluffy-ui/accessibility"
	"github.com/odvcencio/fluffy-ui/backend"
	"github.com/odvcencio/fluffy-ui/clipboard"
	"github.com/odvcencio/fluffy-ui/runtime"
	"github.com/odvcencio/fluffy-ui/terminal"
	uiwidgets "github.com/odvcencio/fluffy-ui/widgets"
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

	textarea  *uiwidgets.TextArea
	modeLabel *uiwidgets.Label
	panel     *uiwidgets.Panel
	layout    *runtime.Flex

	mode InputMode

	// History navigation
	history      []string
	historyIndex int
	historyMax   int
	savedText    string

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

	suppressChange bool
	textBounds     runtime.Rect
}

// NewInputArea creates a new input area widget.
func NewInputArea() *InputArea {
	input := &InputArea{
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

	input.textarea = uiwidgets.NewTextArea()
	input.textarea.SetLabel("Input")
	input.textarea.SetStyle(input.textStyle)
	input.textarea.SetFocusStyle(input.textStyle)
	input.textarea.OnChange(func(text string) {
		input.handleTextChange(text)
	})

	input.modeLabel = uiwidgets.NewLabel("")
	input.layout = runtime.HBox(
		runtime.Fixed(input.modeLabel),
		runtime.Expanded(input.textarea),
	)
	input.panel = uiwidgets.NewPanel(input.layout)
	input.panel.WithBorder(input.borderStyle)
	input.panel.SetStyle(input.bgStyle)
	input.syncModeIndicator()

	return input
}

// AddToHistory adds an entry to the input history.
func (i *InputArea) AddToHistory(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if len(i.history) > 0 && i.history[len(i.history)-1] == text {
		return
	}
	i.history = append(i.history, text)
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
		i.savedText = i.Text()
		i.historyIndex = len(i.history) - 1
	} else if i.historyIndex > 0 {
		i.historyIndex--
	} else {
		return false
	}
	i.SetText(i.history[i.historyIndex])
	return true
}

// historyNext navigates to the next history entry.
func (i *InputArea) historyNext() bool {
	if i.historyIndex == -1 {
		return false
	}
	if i.historyIndex < len(i.history)-1 {
		i.historyIndex++
		i.SetText(i.history[i.historyIndex])
	} else {
		i.historyIndex = -1
		i.SetText(i.savedText)
	}
	return true
}

func (i *InputArea) resetHistoryNav() {
	i.historyIndex = -1
	i.savedText = ""
}

// SetStyles sets the input area styles.
func (i *InputArea) SetStyles(bg, text, border backend.Style) {
	i.bgStyle = bg
	i.textStyle = text
	i.borderStyle = border
	if i.panel != nil {
		i.panel.SetStyle(bg)
		i.panel.WithBorder(border)
	}
	if i.textarea != nil {
		i.textarea.SetStyle(text)
		i.textarea.SetFocusStyle(text)
	}
	if i.modeLabel != nil {
		i.syncModeIndicator()
	}
}

// SetModeStyles sets the mode indicator styles.
func (i *InputArea) SetModeStyles(normal, shell, env, search backend.Style) {
	i.normalMode = normal
	i.shellMode = shell
	i.envMode = env
	i.searchMode = search
	i.syncModeIndicator()
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
	if i.textarea == nil {
		return ""
	}
	return i.textarea.Text()
}

// SetText sets the input text without firing change callbacks.
func (i *InputArea) SetText(text string) {
	if i.textarea == nil {
		return
	}
	i.suppressChange = true
	i.textarea.SetText(text)
	i.suppressChange = false
}

// Clear clears the input.
func (i *InputArea) Clear() {
	i.SetText("")
	i.setMode(ModeNormal)
}

// HasText returns true if there's text in the input.
func (i *InputArea) HasText() bool {
	return i.Text() != ""
}

// InsertText inserts text at the cursor position.
func (i *InputArea) InsertText(s string) {
	if i.textarea == nil || s == "" {
		return
	}
	text := []rune(i.textarea.Text())
	offset := i.textarea.CursorOffset()
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		offset = len(text)
	}
	insert := []rune(s)
	updated := append(text[:offset], append(insert, text[offset:]...)...)
	i.suppressChange = true
	i.textarea.SetText(string(updated))
	i.textarea.SetCursorOffset(offset + len(insert))
	i.suppressChange = false
	i.notifyChange(string(updated))
}

// Mode returns the current input mode.
func (i *InputArea) Mode() InputMode {
	return i.mode
}

// CursorPosition returns the cursor position in characters.
func (i *InputArea) CursorPosition() (x, y int) {
	if i.textarea == nil {
		return 0, 0
	}
	return i.textarea.CursorPosition()
}

// Measure returns the preferred size based on content.
func (i *InputArea) Measure(constraints runtime.Constraints) runtime.Size {
	width := constraints.MaxWidth
	if width <= 0 {
		width = constraints.MinWidth
	}
	if width <= 0 {
		width = 1
	}
	borderPad := 2
	labelSize := runtime.Size{Width: 0, Height: 1}
	if i.modeLabel != nil {
		labelSize = i.modeLabel.Measure(runtime.Constraints{MaxWidth: width, MaxHeight: constraints.MaxHeight})
	}
	textWidth := width - labelSize.Width - borderPad
	if textWidth < 1 {
		textWidth = 1
	}
	textHeight := 1
	if i.textarea != nil {
		textHeight = i.textarea.Measure(runtime.Constraints{MaxWidth: textWidth, MaxHeight: maxConstraint}).Height
	}
	height := textHeight + borderPad
	if height < i.minHeight {
		height = i.minHeight
	}
	if i.maxHeight > 0 && height > i.maxHeight {
		height = i.maxHeight
	}
	return runtime.Size{Width: width, Height: height}
}

// Layout positions the input area.
func (i *InputArea) Layout(bounds runtime.Rect) {
	i.FocusableBase.Layout(bounds)
	if i.panel != nil {
		i.panel.Layout(bounds)
	}
	if i.textarea != nil {
		i.textBounds = i.textarea.Bounds()
	}
}

// Render draws the input area.
func (i *InputArea) Render(ctx runtime.RenderContext) {
	if i.panel == nil {
		return
	}
	i.syncModeIndicator()
	i.panel.Render(ctx)

	if i.cursorStyleSet && i.IsFocused() && i.textarea != nil {
		cx, cy := i.textarea.CursorPosition()
		absX := i.textBounds.X + cx
		absY := i.textBounds.Y + cy
		if absX >= i.textBounds.X && absX < i.textBounds.X+i.textBounds.Width && absY >= i.textBounds.Y && absY < i.textBounds.Y+i.textBounds.Height {
			cell := ctx.Buffer.Get(absX, absY)
			r := cell.Rune
			if r == 0 {
				r = ' '
			}
			ctx.Buffer.Set(absX, absY, r, i.cursorStyle)
		}
	}
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
		text := strings.TrimSpace(i.Text())
		if text == "" {
			return runtime.Handled()
		}
		i.AddToHistory(text)
		mode := i.mode
		if i.onSubmit != nil {
			i.onSubmit(text, mode)
		}
		return runtime.WithCommand(runtime.Submit{Text: text})

	case terminal.KeyUp:
		if i.canMoveVertical(-1) {
			return i.textarea.HandleMessage(msg)
		}
		if i.historyPrev() {
			return runtime.Handled()
		}
		return runtime.Unhandled()

	case terminal.KeyDown:
		if i.canMoveVertical(1) {
			return i.textarea.HandleMessage(msg)
		}
		if i.historyNext() {
			return runtime.Handled()
		}
		return runtime.Unhandled()

	case terminal.KeyEscape:
		i.setMode(ModeNormal)
		return runtime.WithCommand(runtime.Cancel{})

	case terminal.KeyCtrlC:
		if i.HasText() {
			i.Clear()
			return runtime.Handled()
		}
		return runtime.Unhandled()

	case terminal.KeyRune:
		if i.textarea != nil && strings.TrimSpace(i.textarea.Text()) == "" {
			switch key.Rune {
			case '!':
				i.setMode(ModeShell)
			case '$':
				i.setMode(ModeEnv)
			case '/':
				if i.onTriggerSlashCommand != nil {
					i.onTriggerSlashCommand()
					return runtime.Handled()
				}
				i.setMode(ModeNormal)
			case '@':
				if i.onTriggerPicker != nil {
					i.onTriggerPicker()
					return runtime.Handled()
				}
			default:
				i.setMode(ModeNormal)
			}
		}
	}

	if i.textarea == nil {
		return runtime.Unhandled()
	}
	return i.textarea.HandleMessage(msg)
}

// ClipboardCopy returns the current text.
func (i *InputArea) ClipboardCopy() (string, bool) {
	if i.textarea == nil {
		return "", false
	}
	return i.textarea.ClipboardCopy()
}

// ClipboardCut returns the current text and clears it.
func (i *InputArea) ClipboardCut() (string, bool) {
	if i.textarea == nil {
		return "", false
	}
	text, ok := i.textarea.ClipboardCut()
	if ok {
		i.setMode(ModeNormal)
	}
	return text, ok
}

// ClipboardPaste inserts text from clipboard.
func (i *InputArea) ClipboardPaste(text string) bool {
	if text == "" {
		return false
	}
	i.InsertText(text)
	return true
}

// Bind attaches app services.
func (i *InputArea) Bind(services runtime.Services) {
	if i.textarea != nil {
		i.textarea.Bind(services)
	}
}

// Unbind releases app services.
func (i *InputArea) Unbind() {
	if i.textarea != nil {
		i.textarea.Unbind()
	}
}

// Focus forwards focus to the text area.
func (i *InputArea) Focus() {
	i.FocusableBase.Focus()
	if i.textarea != nil {
		i.textarea.Focus()
	}
}

// Blur clears focus from the text area.
func (i *InputArea) Blur() {
	i.FocusableBase.Blur()
	if i.textarea != nil {
		i.textarea.Blur()
	}
}

func (i *InputArea) handleTextChange(text string) {
	if i.suppressChange {
		return
	}
	i.resetHistoryNav()
	i.checkModeChange(text)
	i.notifyChange(text)
}

func (i *InputArea) notifyChange(text string) {
	if i.onChange != nil {
		i.onChange(text)
	}
}

func (i *InputArea) checkModeChange(text string) {
	if strings.TrimSpace(text) == "" {
		i.setMode(ModeNormal)
	}
}

func (i *InputArea) setMode(mode InputMode) {
	if i.mode == mode {
		return
	}
	i.mode = mode
	i.syncModeIndicator()
	if i.onModeChange != nil {
		i.onModeChange(mode)
	}
}

func (i *InputArea) syncModeIndicator() {
	if i.modeLabel == nil {
		return
	}
	style, symbol := i.modeIndicator()
	if symbol == "" {
		symbol = " "
	}
	text := symbol
	if !strings.HasSuffix(text, " ") {
		text += " "
	}
	i.modeLabel.SetText(text)
	i.modeLabel.SetStyle(style)
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

func (i *InputArea) canMoveVertical(delta int) bool {
	if i.textarea == nil {
		return false
	}
	_, line := i.textarea.CursorPosition()
	lines := strings.Count(i.textarea.Text(), "\n")
	if delta < 0 {
		return line > 0
	}
	return line < lines
}

var _ clipboard.Target = (*InputArea)(nil)

// AccessibleRole returns the accessibility role.
func (i *InputArea) AccessibleRole() accessibility.Role {
	return accessibility.RoleTextbox
}

// AccessibleLabel returns the accessibility label.
func (i *InputArea) AccessibleLabel() string {
	return "Input"
}

// AccessibleDescription returns the accessibility description.
func (i *InputArea) AccessibleDescription() string {
	switch i.mode {
	case ModeShell:
		return "Shell command input"
	case ModeEnv:
		return "Environment lookup input"
	case ModeSearch:
		return "Search input"
	default:
		return "Message input"
	}
}

// AccessibleState returns the accessibility state set.
func (i *InputArea) AccessibleState() accessibility.StateSet {
	return accessibility.StateSet{}
}

// AccessibleValue returns the accessibility value.
func (i *InputArea) AccessibleValue() *accessibility.ValueInfo {
	text := i.Text()
	return &accessibility.ValueInfo{Text: text}
}

const maxConstraint = 10000

var _ accessibility.Accessible = (*InputArea)(nil)
