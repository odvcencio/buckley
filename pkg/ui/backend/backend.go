// Package backend defines the terminal backend interface for the TUI.
// This abstraction allows swapping between tcell (real terminals) and
// simulation backends (testing), enabling golden-frame tests.
package backend

import "m31labs.dev/buckley/pkg/ui/terminal"

// Backend is the terminal abstraction layer.
// Implementations handle terminal I/O, input events, and screen rendering.
type Backend interface {
	// Init initializes the backend (enters alt screen, raw mode, etc).
	Init() error

	// Fini cleans up the backend (restores terminal state).
	Fini()

	// Size returns the current terminal dimensions.
	Size() (width, height int)

	// SetContent sets a cell at position (x, y) with the given rune and style.
	// The comb parameter contains combining characters (can be nil).
	SetContent(x, y int, mainc rune, comb []rune, style Style)

	// Show synchronizes the internal buffer to the terminal.
	// This is where actual output happens.
	Show()

	// Clear clears the screen.
	Clear()

	// HideCursor hides the terminal cursor.
	HideCursor()

	// ShowCursor shows the terminal cursor.
	ShowCursor()

	// SetCursorPos sets the cursor position.
	SetCursorPos(x, y int)

	// PollEvent blocks until an event is available and returns it.
	// Returns nil if the backend is shutting down.
	PollEvent() terminal.Event

	// PostEvent injects an event into the event queue.
	// Useful for testing and for posting timer/internal events.
	PostEvent(ev terminal.Event) error

	// Beep emits an audible bell.
	Beep()

	// Sync forces a full redraw on next Show().
	Sync()
}
