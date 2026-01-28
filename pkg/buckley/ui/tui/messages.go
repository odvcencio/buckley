// Package tui provides the terminal user interface.
package tui

import (
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/toast"
)

// Message is the interface for all events flowing through the UI.
// All UI state mutations happen through message processing.
type Message interface {
	isMessage()
}

// --- Input Events ---

// KeyMsg wraps a terminal key event as a message.
type KeyMsg struct {
	Key   int  // terminal.Key value
	Rune  rune // Character for KeyRune
	Alt   bool
	Ctrl  bool
	Shift bool
}

func (KeyMsg) isMessage() {}

// ResizeMsg signals terminal resize.
type ResizeMsg struct {
	Width  int
	Height int
}

func (ResizeMsg) isMessage() {}

// PasteMsg delivers pasted text from bracketed paste mode.
type PasteMsg struct {
	Text string
}

func (PasteMsg) isMessage() {}

// --- Streaming Events ---

// StreamChunk delivers a piece of streaming content.
type StreamChunk struct {
	SessionID string
	Text      string
}

func (StreamChunk) isMessage() {}

// StreamDone signals streaming completion.
type StreamDone struct {
	SessionID string
	FullText  string
}

func (StreamDone) isMessage() {}

// StreamFlush is sent by the coalescer to flush buffered content.
type StreamFlush struct {
	SessionID string
	Text      string
}

func (StreamFlush) isMessage() {}

// ReasoningFlush is sent by the reasoning coalescer to flush buffered reasoning.
type ReasoningFlush struct {
	Text string
}

func (ReasoningFlush) isMessage() {}

// --- Tool Events ---

// ToolStart signals a tool is beginning execution.
type ToolStart struct {
	ToolID   string
	ToolName string
	Args     map[string]any
}

func (ToolStart) isMessage() {}

// ToolResult delivers tool execution result.
type ToolResult struct {
	ToolID string
	Result any
	Err    error
}

func (ToolResult) isMessage() {}

// --- System Events ---

// TickMsg is sent on the frame clock for animations/coalescing.
type TickMsg struct {
	Time time.Time
}

func (TickMsg) isMessage() {}

// QuitMsg signals the app should exit.
type QuitMsg struct{}

func (QuitMsg) isMessage() {}

// RefreshMsg forces a full redraw.
type RefreshMsg struct{}

func (RefreshMsg) isMessage() {}

// --- UI Events ---

// StatusMsg updates the status bar text.
type StatusMsg struct {
	Text string
}

func (StatusMsg) isMessage() {}

// StatusOverrideMsg temporarily overrides the status bar text.
type StatusOverrideMsg struct {
	Text     string
	Duration time.Duration
}

func (StatusOverrideMsg) isMessage() {}

// TokensMsg updates token/cost display.
type TokensMsg struct {
	Tokens   int
	CostCent float64
}

func (TokensMsg) isMessage() {}

// ContextMsg updates context usage display.
type ContextMsg struct {
	Used   int
	Budget int
	Window int
}

func (ContextMsg) isMessage() {}

// ExecutionModeMsg updates execution mode display.
type ExecutionModeMsg struct {
	Mode string
}

func (ExecutionModeMsg) isMessage() {}

// ProgressMsg updates active progress indicators.
type ProgressMsg struct {
	Items []progress.Progress
}

func (ProgressMsg) isMessage() {}

// ToastsMsg updates active toast notifications.
type ToastsMsg struct {
	Toasts []*toast.Toast
}

func (ToastsMsg) isMessage() {}

// StreamingMsg updates streaming indicator state.
type StreamingMsg struct {
	Active bool
}

func (StreamingMsg) isMessage() {}

// ModelMsg updates the active model name.
type ModelMsg struct {
	Name string
}

func (ModelMsg) isMessage() {}

// SessionMsg updates session display metadata.
type SessionMsg struct {
	ID string
}

func (SessionMsg) isMessage() {}

// AddMessageMsg adds a new message to the conversation.
type AddMessageMsg struct {
	Content string
	Source  string // "user", "assistant", "system", "tool", "thinking"
}

func (AddMessageMsg) isMessage() {}

// AppendMsg appends text to the last message.
type AppendMsg struct {
	Text string
}

func (AppendMsg) isMessage() {}

// ThinkingMsg shows/hides the thinking indicator.
type ThinkingMsg struct {
	Show bool
}

func (ThinkingMsg) isMessage() {}

// ReasoningMsg streams reasoning content to the display.
type ReasoningMsg struct {
	Text string // Incremental reasoning text to append
}

func (ReasoningMsg) isMessage() {}

// ReasoningEndMsg signals reasoning block is complete and should collapse.
type ReasoningEndMsg struct {
	Preview string // First ~40 chars for collapsed view
	Full    string // Full reasoning content
}

func (ReasoningEndMsg) isMessage() {}

// --- Overlay/Mode Events ---

// ModeChangeMsg signals input mode change.
type ModeChangeMsg struct {
	Mode string // "normal", "shell", "env", "search", "file"
}

func (ModeChangeMsg) isMessage() {}

// OverlayMsg shows/hides an overlay.
type OverlayMsg struct {
	Show bool
	Name string // "file_picker", "command_palette", etc.
}

func (OverlayMsg) isMessage() {}

// SubmitMsg is sent when user submits input.
type SubmitMsg struct {
	Text string
}

func (SubmitMsg) isMessage() {}

// --- Mouse Events ---

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

// --- Approval Events ---

// ApprovalRequestMsg requests user approval for a tool operation.
type ApprovalRequestMsg struct {
	ID           string     // Unique request identifier
	Tool         string     // Tool name (e.g., "run_shell", "write_file")
	Operation    string     // Operation type (e.g., "shell:write", "write")
	Description  string     // Human-readable explanation
	Command      string     // For shell operations
	FilePath     string     // For file operations
	DiffLines    []DiffLine // For file edits
	AddedLines   int        // Lines added
	RemovedLines int        // Lines removed
}

func (ApprovalRequestMsg) isMessage() {}

// DiffLine represents a single line in a diff preview.
type DiffLine struct {
	Type    DiffLineType // Add, Remove, Context
	Content string
}

// DiffLineType indicates the type of diff line.
type DiffLineType int

const (
	DiffContext DiffLineType = iota
	DiffAdd
	DiffRemove
)

// --- Audio Events ---

// AudioSFXMsg schedules a sound effect cue.
type AudioSFXMsg struct {
	Cue string
}

func (AudioSFXMsg) isMessage() {}

// --- Sidebar Events ---

// SidebarSnapshot captures the complete sidebar state for restoration.
type SidebarSnapshot struct {
	CurrentTask        string
	TaskProgress       int
	PlanTasks          []buckleywidgets.PlanTask
	RunningTools       []buckleywidgets.RunningTool
	ToolHistory        []buckleywidgets.ToolHistoryEntry
	ActiveTouches      []buckleywidgets.TouchSummary
	RecentFiles        []string
	RLMStatus          *buckleywidgets.RLMStatus
	RLMScratchpad      []buckleywidgets.RLMScratchpadEntry
	CircuitStatus      *buckleywidgets.CircuitStatus
	Experiment         string
	ExperimentStatus   string
	ExperimentVariants []buckleywidgets.ExperimentVariant
}

// SidebarStateMsg updates the sidebar with a complete state snapshot.
type SidebarStateMsg struct {
	Snapshot SidebarSnapshot
}

func (SidebarStateMsg) isMessage() {}
