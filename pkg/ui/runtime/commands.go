package runtime

// Command represents an action/intent emitted by widgets.
// Commands bubble up from widgets to the app for handling.
type Command interface {
	isCommand()
}

// Quit signals the application should exit.
type Quit struct{}

func (Quit) isCommand() {}

// Refresh requests a screen redraw.
type Refresh struct{}

func (Refresh) isCommand() {}

// Submit indicates text was submitted (e.g., from input widget).
type Submit struct {
	Text string
}

func (Submit) isCommand() {}

// Cancel indicates an operation was cancelled (e.g., Escape pressed).
type Cancel struct{}

func (Cancel) isCommand() {}

// FileSelected indicates a file was chosen in the file picker.
type FileSelected struct {
	Path string
}

func (FileSelected) isCommand() {}

// ShellCommand indicates a shell command should be executed.
type ShellCommand struct {
	Command string
}

func (ShellCommand) isCommand() {}

// EnvQuery indicates an environment variable lookup.
type EnvQuery struct {
	Name string
}

func (EnvQuery) isCommand() {}

// FocusNext requests focus move to the next focusable widget.
type FocusNext struct{}

func (FocusNext) isCommand() {}

// FocusPrev requests focus move to the previous focusable widget.
type FocusPrev struct{}

func (FocusPrev) isCommand() {}

// PushOverlay requests a modal overlay be pushed.
type PushOverlay struct {
	Widget Widget
	Modal  bool
}

func (PushOverlay) isCommand() {}

// PopOverlay requests the top overlay be dismissed.
type PopOverlay struct{}

func (PopOverlay) isCommand() {}

// ScrollUp requests scrolling up by n lines.
type ScrollUp struct {
	Lines int
}

func (ScrollUp) isCommand() {}

// ScrollDown requests scrolling down by n lines.
type ScrollDown struct {
	Lines int
}

func (ScrollDown) isCommand() {}

// PageUp requests scrolling up by one page.
type PageUp struct{}

func (PageUp) isCommand() {}

// PageDown requests scrolling down by one page.
type PageDown struct{}

func (PageDown) isCommand() {}

// ShowThinking requests the thinking indicator be shown.
type ShowThinking struct{}

func (ShowThinking) isCommand() {}

// HideThinking requests the thinking indicator be hidden.
type HideThinking struct{}

func (HideThinking) isCommand() {}

// UpdateStatus requests the status bar be updated.
type UpdateStatus struct {
	Text string
}

func (UpdateStatus) isCommand() {}

// UpdateTokens requests the token count display be updated.
type UpdateTokens struct {
	Tokens   int
	CostCent float64
}

func (UpdateTokens) isCommand() {}

// UpdateModel requests the model name display be updated.
type UpdateModel struct {
	Name string
}

func (UpdateModel) isCommand() {}

// NextSession requests switching to the next session.
type NextSession struct{}

func (NextSession) isCommand() {}

// PrevSession requests switching to the previous session.
type PrevSession struct{}

func (PrevSession) isCommand() {}

// ApprovalResponse represents the user's decision on a tool approval request.
type ApprovalResponse struct {
	RequestID   string // ID of the original request
	Approved    bool   // Whether the operation was approved
	AlwaysAllow bool   // Remember this decision for the tool
}

func (ApprovalResponse) isCommand() {}

// PaletteSelected indicates an item was chosen from a palette.
type PaletteSelected struct {
	ID   string // Item identifier
	Data any    // Custom data from the item
}

func (PaletteSelected) isCommand() {}
