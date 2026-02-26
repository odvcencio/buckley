package tui

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/odvcencio/fluffyui/backend"
	backendtcell "github.com/odvcencio/fluffyui/backend/tcell"
	"github.com/odvcencio/fluffyui/terminal"
)

const (
	keyTraceEnv     = "BUCKLEY_TCELL_TRACE_KEYS"
	keyTraceFileEnv = "BUCKLEY_TCELL_TRACE_FILE"
)

func maybeWrapBackendForKeyTrace(cfg *RunnerConfig) backend.Backend {
	if cfg == nil || !keyTraceEnabled() {
		return nil
	}
	if cfg.Backend == nil {
		if !shouldTraceDefaultBackend() {
			return nil
		}
		be, err := backendtcell.New()
		if err != nil {
			log.Printf("warning: failed to initialize tcell backend for key trace: %v", err)
			return nil
		}
		return newTraceBackend(be)
	}
	return newTraceBackend(cfg.Backend)
}

func keyTraceEnabled() bool {
	val := strings.TrimSpace(os.Getenv(keyTraceEnv))
	if val == "" {
		return false
	}
	switch strings.ToLower(val) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}

func shouldTraceDefaultBackend() bool {
	backendName := strings.ToLower(strings.TrimSpace(os.Getenv("FLUFFYUI_BACKEND")))
	switch backendName {
	case "sim", "simulation", "web":
		return false
	default:
		return true
	}
}

type traceBackend struct {
	inner  backend.Backend
	logger *log.Logger
	closer io.Closer
}

func newTraceBackend(inner backend.Backend) backend.Backend {
	logger, closer := keyTraceLogger()
	return &traceBackend{
		inner:  inner,
		logger: logger,
		closer: closer,
	}
}

func keyTraceLogger() (*log.Logger, io.Closer) {
	path := strings.TrimSpace(os.Getenv(keyTraceFileEnv))
	if path == "" {
		return log.New(os.Stderr, "tcell-key: ", log.LstdFlags), nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("warning: failed to open %s: %v", path, err)
		return log.New(os.Stderr, "tcell-key: ", log.LstdFlags), nil
	}
	return log.New(file, "tcell-key: ", log.LstdFlags), file
}

func (t *traceBackend) Init() error {
	if t == nil || t.inner == nil {
		return nil
	}
	return t.inner.Init()
}

func (t *traceBackend) Fini() {
	if t == nil {
		return
	}
	if t.inner != nil {
		t.inner.Fini()
	}
	if t.closer != nil {
		_ = t.closer.Close()
	}
}

func (t *traceBackend) Size() (width, height int) {
	if t == nil || t.inner == nil {
		return 0, 0
	}
	return t.inner.Size()
}

func (t *traceBackend) SetContent(x, y int, mainc rune, comb []rune, style backend.Style) {
	if t == nil || t.inner == nil {
		return
	}
	t.inner.SetContent(x, y, mainc, comb, style)
}

func (t *traceBackend) Show() {
	if t == nil || t.inner == nil {
		return
	}
	t.inner.Show()
}

func (t *traceBackend) Clear() {
	if t == nil || t.inner == nil {
		return
	}
	t.inner.Clear()
}

func (t *traceBackend) HideCursor() {
	if t == nil || t.inner == nil {
		return
	}
	t.inner.HideCursor()
}

func (t *traceBackend) ShowCursor() {
	if t == nil || t.inner == nil {
		return
	}
	t.inner.ShowCursor()
}

func (t *traceBackend) SetCursorPos(x, y int) {
	if t == nil || t.inner == nil {
		return
	}
	t.inner.SetCursorPos(x, y)
}

func (t *traceBackend) PollEvent() terminal.Event {
	if t == nil || t.inner == nil {
		return nil
	}
	ev := t.inner.PollEvent()
	t.logEvent("poll", ev)
	return ev
}

func (t *traceBackend) PostEvent(ev terminal.Event) error {
	if t == nil || t.inner == nil {
		return nil
	}
	t.logEvent("post", ev)
	return t.inner.PostEvent(ev)
}

func (t *traceBackend) Beep() {
	if t == nil || t.inner == nil {
		return
	}
	t.inner.Beep()
}

func (t *traceBackend) Sync() {
	if t == nil || t.inner == nil {
		return
	}
	t.inner.Sync()
}

func (t *traceBackend) logEvent(prefix string, ev terminal.Event) {
	if t == nil || t.logger == nil || ev == nil {
		return
	}
	switch e := ev.(type) {
	case terminal.KeyEvent:
		t.logger.Printf("%s key=%s(%d) rune=%q(0x%X) ctrl=%v alt=%v shift=%v", prefix, keyName(e.Key), e.Key, e.Rune, uint32(e.Rune), e.Ctrl, e.Alt, e.Shift)
	case terminal.PasteEvent:
		t.logger.Printf("%s paste bytes=%d", prefix, len(e.Text))
	case terminal.ResizeEvent:
		t.logger.Printf("%s resize %dx%d", prefix, e.Width, e.Height)
	case terminal.MouseEvent:
		t.logger.Printf("%s mouse x=%d y=%d button=%d action=%d ctrl=%v alt=%v shift=%v", prefix, e.X, e.Y, e.Button, e.Action, e.Ctrl, e.Alt, e.Shift)
	}
}

func keyName(key terminal.Key) string {
	switch key {
	case terminal.KeyNone:
		return "None"
	case terminal.KeyRune:
		return "Rune"
	case terminal.KeyEnter:
		return "Enter"
	case terminal.KeyBackspace:
		return "Backspace"
	case terminal.KeyTab:
		return "Tab"
	case terminal.KeyEscape:
		return "Escape"
	case terminal.KeyUp:
		return "Up"
	case terminal.KeyDown:
		return "Down"
	case terminal.KeyLeft:
		return "Left"
	case terminal.KeyRight:
		return "Right"
	case terminal.KeyHome:
		return "Home"
	case terminal.KeyEnd:
		return "End"
	case terminal.KeyPageUp:
		return "PageUp"
	case terminal.KeyPageDown:
		return "PageDown"
	case terminal.KeyDelete:
		return "Delete"
	case terminal.KeyInsert:
		return "Insert"
	case terminal.KeyCtrlB:
		return "CtrlB"
	case terminal.KeyCtrlC:
		return "CtrlC"
	case terminal.KeyCtrlD:
		return "CtrlD"
	case terminal.KeyCtrlF:
		return "CtrlF"
	case terminal.KeyCtrlP:
		return "CtrlP"
	case terminal.KeyCtrlV:
		return "CtrlV"
	case terminal.KeyCtrlX:
		return "CtrlX"
	case terminal.KeyCtrlY:
		return "CtrlY"
	case terminal.KeyCtrlZ:
		return "CtrlZ"
	case terminal.KeyF1:
		return "F1"
	case terminal.KeyF2:
		return "F2"
	case terminal.KeyF3:
		return "F3"
	case terminal.KeyF4:
		return "F4"
	case terminal.KeyF5:
		return "F5"
	case terminal.KeyF6:
		return "F6"
	case terminal.KeyF7:
		return "F7"
	case terminal.KeyF8:
		return "F8"
	case terminal.KeyF9:
		return "F9"
	case terminal.KeyF10:
		return "F10"
	case terminal.KeyF11:
		return "F11"
	case terminal.KeyF12:
		return "F12"
	default:
		return fmt.Sprintf("Key(%d)", key)
	}
}
