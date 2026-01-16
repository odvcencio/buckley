package runtime

// Command represents an action/intent emitted by widgets.
// Commands bubble up from widgets to the app for handling.
type Command interface {
	Command()
}

// Quit signals the application should exit.
type Quit struct{}

func (Quit) Command() {}

// Refresh requests a screen redraw.
type Refresh struct{}

func (Refresh) Command() {}

// Submit indicates text was submitted (e.g., from input widget).
type Submit struct {
	Text string
}

func (Submit) Command() {}

// Cancel indicates an operation was cancelled (e.g., Escape pressed).
type Cancel struct{}

func (Cancel) Command() {}

// FileSelected indicates a file was chosen in the file picker.
type FileSelected struct {
	Path string
}

func (FileSelected) Command() {}

// FocusNext requests focus move to the next focusable widget.
type FocusNext struct{}

func (FocusNext) Command() {}

// FocusPrev requests focus move to the previous focusable widget.
type FocusPrev struct{}

func (FocusPrev) Command() {}

// PushOverlay requests a modal overlay be pushed.
type PushOverlay struct {
	Widget Widget
	Modal  bool
}

func (PushOverlay) Command() {}

// PopOverlay requests the top overlay be dismissed.
type PopOverlay struct{}

func (PopOverlay) Command() {}

// PaletteSelected indicates an item was chosen from a palette.
type PaletteSelected struct {
	ID   string // Item identifier
	Data any    // Custom data from the item
}

func (PaletteSelected) Command() {}
