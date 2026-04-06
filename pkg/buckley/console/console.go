// Package console provides styled terminal output using the fur library.
// This is a thin wrapper for CLI output that doesn't require the full TUI.
package console

import (
	"io"
	"os"
	"sync"

	"github.com/odvcencio/fluffyui/fur"
)

var (
	defaultConsole *Console
	initOnce       sync.Once
)

// Console wraps fur.Console for styled CLI output.
type Console struct {
	*fur.Console
	noColor bool
}

// Default returns the default console for standard output.
func Default() *Console {
	initOnce.Do(func() {
		defaultConsole = New(os.Stdout)
	})
	return defaultConsole
}

// New creates a console writing to the given writer.
func New(w io.Writer) *Console {
	return &Console{
		Console: fur.New(fur.WithOutput(w)),
	}
}

// NewWithOptions creates a console with custom options.
func NewWithOptions(w io.Writer, noColor bool, width int) *Console {
	opts := []fur.Option{fur.WithOutput(w)}
	if noColor {
		opts = append(opts, fur.WithNoColor())
	}
	if width > 0 {
		opts = append(opts, fur.WithWidth(width))
	}
	return &Console{
		Console: fur.New(opts...),
		noColor: noColor,
	}
}

// Error prints a styled error message.
func (c *Console) Error(msg string) {
	c.Println("[bold red]Error:[/] " + msg)
}

// Errorf prints a formatted styled error message.
func (c *Console) Errorf(format string, args ...any) {
	c.Printf("[bold red]Error:[/] "+format+"\n", args...)
}

// Warning prints a styled warning message.
func (c *Console) Warning(msg string) {
	c.Println("[bold yellow]Warning:[/] " + msg)
}

// Warningf prints a formatted styled warning message.
func (c *Console) Warningf(format string, args ...any) {
	c.Printf("[bold yellow]Warning:[/] "+format+"\n", args...)
}

// Success prints a styled success message.
func (c *Console) Success(msg string) {
	c.Println("[bold green]✓[/] " + msg)
}

// Successf prints a formatted styled success message.
func (c *Console) Successf(format string, args ...any) {
	c.Printf("[bold green]✓[/] "+format+"\n", args...)
}

// Info prints a styled info message.
func (c *Console) Info(msg string) {
	c.Println("[bold blue]ℹ[/] " + msg)
}

// Infof prints a formatted styled info message.
func (c *Console) Infof(format string, args ...any) {
	c.Printf("[bold blue]ℹ[/] "+format+"\n", args...)
}

// Header prints a section header with a rule.
func (c *Console) Header(title string) {
	c.Rule(title)
}

// Section prints a panel-wrapped section.
func (c *Console) Section(title string, content string) {
	c.Render(fur.PanelWith(
		fur.Markup(content),
		fur.PanelOpts{Title: title},
	))
}

// Pretty prints any Go value with pretty formatting.
func (c *Console) Pretty(value any) {
	c.Render(fur.Pretty(value))
}

// PrettyPanel prints a value in a titled panel.
func (c *Console) PrettyPanel(title string, value any) {
	c.Render(fur.PanelWith(
		fur.Pretty(value),
		fur.PanelOpts{Title: title},
	))
}

// Columns prints multiple renderables side by side.
func (c *Console) Columns(items ...fur.Renderable) {
	c.Render(fur.Columns(items...))
}

// Text creates a text renderable.
func Text(s string) fur.Renderable {
	return fur.Text(s)
}

// Markup creates a markup renderable with BBCode-style tags.
func Markup(s string) fur.Renderable {
	return fur.Markup(s)
}

// Panel creates a paneled renderable.
func Panel(r fur.Renderable) fur.Renderable {
	return fur.Panel(r)
}

// PanelWith creates a panel with options.
func PanelWith(r fur.Renderable, opts fur.PanelOpts) fur.Renderable {
	return fur.PanelWith(r, opts)
}

// Traceback formats an error with stack trace.
func Traceback(err error) fur.Renderable {
	return fur.Traceback(err)
}

// TracebackWith formats an error with options.
func TracebackWith(err error, opts fur.TracebackOpts) fur.Renderable {
	return fur.TracebackWith(err, opts)
}

// Progress creates a progress bar.
func Progress(total int) *fur.Progress {
	return fur.NewProgress(total)
}

// Inspect creates an object inspector.
func Inspect(value any) fur.Renderable {
	return fur.Inspect(value)
}

// InspectWith creates an object inspector with options.
func InspectWith(value any, opts fur.InspectOpts) fur.Renderable {
	return fur.InspectWith(value, opts)
}

// Live creates a live updating display.
func Live(r fur.Renderable) *fur.Live {
	return fur.NewLive(r)
}
