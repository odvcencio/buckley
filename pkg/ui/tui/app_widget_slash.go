package tui

import (
	"strings"

	"m31labs.dev/buckley/pkg/ui/widgets"
	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/terminal"
)

type slashCommandOverlay struct {
	*widgets.PaletteWidget
	app *WidgetApp
}

func (p *slashCommandOverlay) HandleMessage(msg runtime.Message) runtime.HandleResult {
	key, ok := msg.(runtime.KeyMsg)
	if !ok || key.Key != terminal.KeyEnter && (key.Key != terminal.KeyRune || key.Rune != ' ') {
		return p.PaletteWidget.HandleMessage(msg)
	}
	query := strings.TrimSpace(p.Query())
	command := "/" + strings.TrimPrefix(query, "/")
	item := p.SelectedItem()
	if key.Key == terminal.KeyRune && key.Rune == ' ' && query != "" {
		if item == nil || strings.EqualFold(item.Label, command) {
			if item != nil {
				command = item.Label
			}
			p.app.prefillInput(command + " ")
			return runtime.WithCommand(runtime.PopOverlay{})
		}
	}
	if key.Key == terminal.KeyEnter && item == nil && query != "" {
		if p.app.onSubmit != nil {
			p.app.onSubmit(command)
		}
		return runtime.WithCommand(runtime.PopOverlay{})
	}
	return p.PaletteWidget.HandleMessage(msg)
}
