package widgets

import (
	"strings"

	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/clipboard"
	"github.com/odvcencio/fluffyui/dragdrop"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/state"
	"github.com/odvcencio/fluffyui/terminal"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
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

// InputAreaConfig provides external state bindings for the input area.
type InputAreaConfig struct {
	Text state.Writable[string]
	Mode state.Writable[InputMode]
}

// InputArea is a mode-aware input widget.
// It delegates all focus operations to its internal TextArea to avoid focus conflicts.
type InputArea struct {
	// Embed Base for Layout/Bounds/styling, but NOT FocusableBase.
	// Focus operations are delegated to the internal textarea to prevent
	// the dual-focus problem where InputArea and textarea have separate focus states.
	uiwidgets.Base

	textarea  *uiwidgets.TextArea
	modeLabel *uiwidgets.Label
	panel     *uiwidgets.Panel
	layout    *runtime.Flex

	mode InputMode

	services runtime.Services
	subs     state.Subscriptions

	textSig      state.Readable[string]
	textWritable state.Writable[string]
	modeSig      state.Readable[InputMode]
	modeWritable state.Writable[InputMode]

	ownedTextSig *state.Signal[string]
	ownedModeSig *state.Signal[InputMode]

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
	return NewInputAreaWithConfig(InputAreaConfig{})
}

// NewInputAreaWithConfig creates a new input area widget with external state.
func NewInputAreaWithConfig(cfg InputAreaConfig) *InputArea {
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

	input.ownedTextSig = state.NewSignal("")
	input.ownedModeSig = state.NewSignal(ModeNormal)
	if cfg.Text != nil {
		input.textSig = cfg.Text
		input.textWritable = cfg.Text
	} else {
		input.textSig = input.ownedTextSig
		input.textWritable = input.ownedTextSig
	}
	if cfg.Mode != nil {
		input.modeSig = cfg.Mode
		input.modeWritable = cfg.Mode
	} else {
		input.modeSig = input.ownedModeSig
		input.modeWritable = input.ownedModeSig
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
	if input.modeSig != nil {
		input.mode = input.modeSig.Get()
	}
	if input.textSig != nil {
		input.suppressChange = true
		input.textarea.SetText(input.textSig.Get())
		input.suppressChange = false
	}
	input.syncModeIndicator()
	input.subscribe()

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
// This forwards the style to the internal textarea so it can render its own cursor.
func (i *InputArea) SetCursorStyle(style backend.Style) {
	i.cursorStyle = style
	i.cursorStyleSet = true
	// Forward to textarea so it renders its cursor with this style
	if i.textarea != nil {
		i.textarea.SetFocusStyle(style)
	}
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

func (i *InputArea) subscribe() {
	i.subs.Clear()
	if i.textSig != nil {
		i.subs.Observe(i.textSig, i.onTextSignalChanged)
	}
	if i.modeSig != nil {
		i.subs.Observe(i.modeSig, i.onModeSignalChanged)
	}
	i.onTextSignalChanged()
	i.onModeSignalChanged()
}

func (i *InputArea) onTextSignalChanged() {
	if i.textSig == nil || i.textarea == nil {
		return
	}
	text := i.textSig.Get()
	if i.textarea.Text() == text {
		return
	}
	i.suppressChange = true
	i.textarea.SetText(text)
	i.suppressChange = false
	if i.services != (runtime.Services{}) {
		i.services.Invalidate()
	}
}

func (i *InputArea) onModeSignalChanged() {
	if i.modeSig == nil {
		return
	}
	mode := i.modeSig.Get()
	if i.mode == mode {
		return
	}
	i.mode = mode
	i.syncModeIndicator()
	if i.services != (runtime.Services{}) {
		i.services.Invalidate()
	}
}

// Text returns the current input text.
func (i *InputArea) Text() string {
	if i.textarea == nil {
		if i.textSig != nil {
			return i.textSig.Get()
		}
		return ""
	}
	return i.textarea.Text()
}

// SetText sets the input text without firing change callbacks.
func (i *InputArea) SetText(text string) {
	i.setTextSignal(text)
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
	i.setTextSignal(string(updated))
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
		textHeight = measureTextHeight(i.textarea.Text(), textWidth)
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
	i.Base.Layout(bounds)
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
	// The panel renders the textarea, which handles its own cursor rendering
	// when focused and SetFocusStyle has been called
	i.panel.Render(ctx)
}

// HandleMessage processes keyboard input.
func (i *InputArea) HandleMessage(msg runtime.Message) runtime.HandleResult {
	if mouse, ok := msg.(runtime.MouseMsg); ok {
		if i.panel == nil {
			return runtime.Unhandled()
		}
		if i.Bounds().Contains(mouse.X, mouse.Y) {
			if mouse.Button == runtime.MouseLeft && (mouse.Action == runtime.MousePress || mouse.Action == runtime.MouseRelease) {
				if !i.IsFocused() {
					i.Focus()
				}
			}
			if i.textarea != nil {
				if result := i.textarea.HandleMessage(msg); result.Handled {
					return result
				}
			}
			return runtime.Handled()
		}
		return runtime.Unhandled()
	}

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
	i.services = services
	i.subs.SetScheduler(services.Scheduler())
	i.subscribe()
	if i.textarea != nil {
		i.textarea.Bind(services)
	}
}

// Unbind releases app services.
func (i *InputArea) Unbind() {
	i.subs.Clear()
	i.services = runtime.Services{}
	if i.textarea != nil {
		i.textarea.Unbind()
	}
}

// CanFocus returns true if this widget can receive focus.
// Delegates to the internal textarea.
func (i *InputArea) CanFocus() bool {
	if i.textarea == nil {
		return false
	}
	return i.textarea.CanFocus()
}

// Focus forwards focus to the text area.
// Delegates entirely to textarea - no separate FocusableBase.
func (i *InputArea) Focus() {
	if i.textarea != nil {
		i.textarea.Focus()
	}
}

// Blur clears focus from the text area.
// Delegates entirely to textarea - no separate FocusableBase.
func (i *InputArea) Blur() {
	if i.textarea != nil {
		i.textarea.Blur()
	}
}

// IsFocused returns true if the internal textarea has focus.
// Delegates entirely to textarea - no separate FocusableBase.
func (i *InputArea) IsFocused() bool {
	if i.textarea == nil {
		return false
	}
	return i.textarea.IsFocused()
}

func (i *InputArea) handleTextChange(text string) {
	if i.suppressChange {
		return
	}
	i.resetHistoryNav()
	i.setTextSignal(text)
	i.checkModeChange(text)
	i.notifyChange(text)
}

func (i *InputArea) notifyChange(text string) {
	if i.onChange != nil {
		i.onChange(text)
	}
}

func (i *InputArea) setTextSignal(text string) {
	if i.textWritable == nil {
		return
	}
	i.textWritable.Set(text)
}

func (i *InputArea) setModeSignal(mode InputMode) {
	if i.modeWritable == nil {
		return
	}
	i.modeWritable.Set(mode)
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
	i.setModeSignal(mode)
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

// CanDrop reports whether the input accepts a drag payload.
func (i *InputArea) CanDrop(data dragdrop.DragData) bool {
	if i == nil {
		return false
	}
	switch data.Kind {
	case "path", "text":
		return true
	default:
		return false
	}
}

// Drop inserts the payload text into the input.
func (i *InputArea) Drop(data dragdrop.DragData, position dragdrop.DropPosition) {
	if i == nil {
		return
	}
	var text string
	switch data.Kind {
	case "path":
		if value, ok := data.Payload.(string); ok {
			text = value
		}
	case "text":
		if value, ok := data.Payload.(string); ok {
			text = value
		}
	}
	if strings.TrimSpace(text) == "" {
		return
	}
	if i.Text() != "" {
		text = " " + text
	}
	i.InsertText(text)
	i.Focus()
}

// DropPreview is a no-op for now.
func (i *InputArea) DropPreview(data dragdrop.DragData, position dragdrop.DropPosition) {}

func measureTextHeight(text string, width int) int {
	if width <= 0 {
		return 1
	}
	if text == "" {
		return 1
	}
	lines := strings.Split(text, "\n")
	height := 0
	for _, line := range lines {
		w := textWidth(line)
		if w == 0 {
			height++
			continue
		}
		height += (w-1)/width + 1
	}
	if height < 1 {
		return 1
	}
	return height
}

const maxConstraint = 10000

var _ accessibility.Accessible = (*InputArea)(nil)
var _ dragdrop.DropTarget = (*InputArea)(nil)
