package runtime

import (
	"time"

	"github.com/odvcencio/buckley/pkg/ui/terminal"
)

// Message represents an event flowing into the UI.
// Messages come from terminal input, timers, or background goroutines.
type Message interface {
	isMessage()
}

// KeyMsg represents a keyboard input event.
type KeyMsg struct {
	Key   terminal.Key
	Rune  rune
	Alt   bool
	Ctrl  bool
	Shift bool
}

func (KeyMsg) isMessage() {}

// ResizeMsg indicates the terminal size changed.
type ResizeMsg struct {
	Width  int
	Height int
}

func (ResizeMsg) isMessage() {}

// MouseMsg represents a mouse input event.
type MouseMsg struct {
	X, Y   int
	Button MouseButton
	Action MouseAction
	Alt    bool
	Ctrl   bool
	Shift  bool
}

func (MouseMsg) isMessage() {}

// PasteMsg represents pasted text from bracketed paste mode.
type PasteMsg struct {
	Text string
}

func (PasteMsg) isMessage() {}

// MouseButton identifies which mouse button was involved.
type MouseButton int

const (
	MouseNone MouseButton = iota
	MouseLeft
	MouseMiddle
	MouseRight
	MouseWheelUp
	MouseWheelDown
)

// MouseAction identifies what happened with the mouse.
type MouseAction int

const (
	MousePress MouseAction = iota
	MouseRelease
	MouseMove
)

// TickMsg is sent on each frame tick for animations.
type TickMsg struct {
	Time time.Time
}

func (TickMsg) isMessage() {}

// StreamChunk contains a piece of streaming text.
type StreamChunk struct {
	SessionID string
	Text      string
}

func (StreamChunk) isMessage() {}

// StreamFlush is sent when buffered stream content should be rendered.
type StreamFlush struct {
	SessionID string
	Text      string
}

func (StreamFlush) isMessage() {}

// StreamDone signals the end of a streaming session.
type StreamDone struct {
	SessionID string
	FullText  string
}

func (StreamDone) isMessage() {}

// ToolStart indicates a tool has begun execution.
type ToolStart struct {
	ToolID   string
	ToolName string
	Args     map[string]any
}

func (ToolStart) isMessage() {}

// ToolResult indicates a tool has completed.
type ToolResult struct {
	ToolID string
	Result any
	Err    error
}

func (ToolResult) isMessage() {}

// AddMessageMsg requests adding a message to the conversation.
type AddMessageMsg struct {
	Content string
	Source  string
}

func (AddMessageMsg) isMessage() {}

// AppendTextMsg requests appending text to the last message.
type AppendTextMsg struct {
	Text string
}

func (AppendTextMsg) isMessage() {}

// StatusMsg updates the status bar.
type StatusMsg struct {
	Text string
}

func (StatusMsg) isMessage() {}

// TokensMsg updates the token count display.
type TokensMsg struct {
	Tokens   int
	CostCent float64
}

func (TokensMsg) isMessage() {}

// ModelMsg updates the model name display.
type ModelMsg struct {
	Name string
}

func (ModelMsg) isMessage() {}

// ThinkingMsg controls the thinking indicator.
type ThinkingMsg struct {
	Show bool
}

func (ThinkingMsg) isMessage() {}

// RefreshMsg requests a screen redraw.
type RefreshMsg struct{}

func (RefreshMsg) isMessage() {}

// QuitMsg requests the application exit.
type QuitMsg struct{}

func (QuitMsg) isMessage() {}
