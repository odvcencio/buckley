package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/ui/backend"
	"github.com/odvcencio/buckley/pkg/ui/terminal"
	"github.com/odvcencio/buckley/pkg/ui/theme"
)

// UpdateFunc handles a message and returns true if a render is needed.
type UpdateFunc func(app *App, msg Message) bool

// CommandHandler handles commands emitted by widgets.
// Return true if the command requires a render.
type CommandHandler func(cmd Command) bool

// AppConfig configures a runtime App.
type AppConfig struct {
	Backend        backend.Backend
	Root           Widget
	Theme          *theme.Theme
	Update         UpdateFunc
	CommandHandler CommandHandler
	MessageBuffer  int
	TickRate       time.Duration
}

// App runs a widget tree against a terminal backend.
type App struct {
	backend        backend.Backend
	screen         *Screen
	root           Widget
	theme          *theme.Theme
	update         UpdateFunc
	commandHandler CommandHandler
	messages       chan Message
	tickRate       time.Duration

	running  bool
	dirty    bool
	renderMu sync.Mutex
}

// NewApp creates a new App from config.
func NewApp(cfg AppConfig) *App {
	bufferSize := cfg.MessageBuffer
	if bufferSize <= 0 {
		bufferSize = 128
	}
	return &App{
		backend:        cfg.Backend,
		root:           cfg.Root,
		theme:          cfg.Theme,
		update:         cfg.Update,
		commandHandler: cfg.CommandHandler,
		messages:       make(chan Message, bufferSize),
		tickRate:       cfg.TickRate,
	}
}

// Screen returns the active screen, if initialized.
func (a *App) Screen() *Screen {
	return a.screen
}

// SetRoot swaps the root widget.
func (a *App) SetRoot(root Widget) {
	a.root = root
	if a.screen != nil {
		a.screen.SetRoot(root)
		a.dirty = true
	}
}

// SetTheme swaps the active theme.
func (a *App) SetTheme(th *theme.Theme) {
	a.theme = th
	if a.screen != nil {
		a.screen.SetTheme(th)
		a.dirty = true
	}
}

// Post sends a message to the event loop.
func (a *App) Post(msg Message) {
	select {
	case a.messages <- msg:
	default:
	}
}

// Run starts the event loop until quit or context cancellation.
func (a *App) Run(ctx context.Context) error {
	if a.backend == nil {
		return errors.New("backend is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := a.backend.Init(); err != nil {
		return fmt.Errorf("init backend: %w", err)
	}
	defer a.backend.Fini()

	a.backend.HideCursor()
	w, h := a.backend.Size()
	if a.theme == nil {
		a.theme = theme.DefaultTheme()
	}
	a.screen = NewScreen(w, h, a.theme)
	if a.root != nil {
		a.screen.SetRoot(a.root)
	}

	if a.update == nil {
		a.update = DefaultUpdate
	}

	a.running = true
	a.dirty = true

	go a.pollEvents()

	var ticker *time.Ticker
	var ticks <-chan time.Time
	if a.tickRate > 0 {
		ticker = time.NewTicker(a.tickRate)
		defer ticker.Stop()
		ticks = ticker.C
	}

	for a.running {
		select {
		case <-ctx.Done():
			a.running = false
		case msg := <-a.messages:
			if a.update(a, msg) {
				a.dirty = true
			}
		case now := <-ticks:
			if a.update(a, TickMsg{Time: now}) {
				a.dirty = true
			}
		}

		if a.dirty {
			a.render()
			a.dirty = false
		}
	}

	return ctx.Err()
}

// DefaultUpdate handles input messages and widget commands.
func DefaultUpdate(app *App, msg Message) bool {
	if app == nil || app.screen == nil {
		return false
	}

	switch m := msg.(type) {
	case ResizeMsg:
		app.screen.Resize(m.Width, m.Height)
		return true
	default:
		result := app.screen.HandleMessage(msg)
		dirty := result.Handled
		for _, cmd := range result.Commands {
			if app.handleCommand(cmd) {
				dirty = true
			}
		}
		return dirty
	}
}

func (a *App) handleCommand(cmd Command) bool {
	switch cmd.(type) {
	case Quit:
		a.running = false
		return false
	case Refresh:
		if a.screen != nil {
			a.screen.Buffer().MarkAllDirty()
		}
		return true
	default:
		if a.commandHandler != nil {
			return a.commandHandler(cmd)
		}
		return false
	}
}

func (a *App) pollEvents() {
	for a.running {
		ev := a.backend.PollEvent()
		if ev == nil {
			continue
		}

		switch e := ev.(type) {
		case terminal.KeyEvent:
			a.Post(KeyMsg{
				Key:   e.Key,
				Rune:  e.Rune,
				Alt:   e.Alt,
				Ctrl:  e.Ctrl,
				Shift: e.Shift,
			})
		case terminal.ResizeEvent:
			a.Post(ResizeMsg{Width: e.Width, Height: e.Height})
		case terminal.MouseEvent:
			a.Post(MouseMsg{
				X:      e.X,
				Y:      e.Y,
				Button: MouseButton(e.Button),
				Action: MouseAction(e.Action),
				Alt:    e.Alt,
				Ctrl:   e.Ctrl,
				Shift:  e.Shift,
			})
		case terminal.PasteEvent:
			a.Post(PasteMsg{Text: e.Text})
		}
	}
}

func (a *App) render() {
	a.renderMu.Lock()
	defer a.renderMu.Unlock()

	if a.screen == nil {
		return
	}

	a.screen.Render()
	buf := a.screen.Buffer()

	if buf.IsDirty() {
		dirtyCount := buf.DirtyCount()
		w, h := buf.Size()
		totalCells := w * h

		if dirtyCount > totalCells/2 {
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					cell := buf.Get(x, y)
					a.backend.SetContent(x, y, cell.Rune, nil, cell.Style)
				}
			}
		} else {
			buf.ForEachDirtyCell(func(x, y int, cell Cell) {
				a.backend.SetContent(x, y, cell.Rune, nil, cell.Style)
			})
		}
		buf.ClearDirty()
	}

	a.backend.Show()
}
