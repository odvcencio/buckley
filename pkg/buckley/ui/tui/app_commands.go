// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"strconv"
	"strings"
	"time"

	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/fluffyui/clipboard"
	"github.com/odvcencio/fluffyui/keybind"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/widgets"
)

// ============================================================================
// FILE: app_commands.go
// PURPOSE: Command palette, handlers, and slash commands
// FUNCTIONS:
//   - initKeybindings
//   - baseKeymap
//   - registerCommands
//   - executeCommandID
//   - handleCommand
//   - handlePaletteCommand
//   - showCommandPalette
//   - showSlashCommandPalette
//   - showSearchOverlay
//   - showFilePicker
//   - showApprovalDialog
// ============================================================================

func (a *WidgetApp) initKeybindings() {
	registry := keybind.NewRegistry()
	a.registerCommands(registry)

	keymaps := &keybind.KeymapStack{}
	keymaps.Push(a.baseKeymap())

	a.keyRegistry = registry
	a.keymaps = keymaps
	a.keyRouter = keybind.NewKeyRouter(registry, nil, keymaps)

	palette := widgets.NewEnhancedPalette(registry)
	palette.SetKeymapStack(keymaps)
	palette.Pin("new")
	palette.Pin("search")
	palette.Pin("toggle-sidebar")
	palette.Pin("file-picker")
	palette.Pin("ui-settings")
	a.commandPalette = palette
}

func (a *WidgetApp) baseKeymap() *keybind.Keymap {
	return &keybind.Keymap{
		Name: "buckley",
		Bindings: []keybind.Binding{
			{Key: keybind.MustParseKeySequence("ctrl+p"), Command: "palette"},
			{Key: keybind.MustParseKeySequence("ctrl+b"), Command: "toggle-sidebar"},
			{Key: keybind.MustParseKeySequence("ctrl+left"), Command: "toggle-sidebar"},
			{Key: keybind.MustParseKeySequence("ctrl+,"), Command: "ui-settings"},
			{Key: keybind.MustParseKeySequence("ctrl+f"), Command: "search"},
			{Key: keybind.MustParseKeySequence("ctrl+shift+f"), Command: "focus-mode"},
			{Key: keybind.MustParseKeySequence("alt+home"), Command: "scroll-top"},
			{Key: keybind.MustParseKeySequence("alt+end"), Command: "scroll-bottom"},
			{Key: keybind.MustParseKeySequence("alt+c"), Command: "copy-code"},
			{Key: keybind.MustParseKeySequence("alt+w"), Command: "open-web-session"},
			{Key: keybind.MustParseKeySequence("alt+shift+w"), Command: "open-web-dashboard"},
			{Key: keybind.MustParseKeySequence("alt+left"), Command: "session-prev"},
			{Key: keybind.MustParseKeySequence("alt+right"), Command: "session-next"},
			{Key: keybind.MustParseKeySequence("ctrl+d"), Command: "debug"},
			{Key: keybind.MustParseKeySequence("tab"), Command: "focus.next"},
			{Key: keybind.MustParseKeySequence("shift+tab"), Command: "focus.prev"},
			{Key: keybind.MustParseKeySequence("pgup"), Command: "scroll.pageUp"},
			{Key: keybind.MustParseKeySequence("pgdn"), Command: "scroll.pageDown"},
			{Key: keybind.MustParseKeySequence("ctrl+shift+c"), Command: clipboard.CommandCopy, When: keybind.WhenFocusedClipboardTarget()},
			{Key: keybind.MustParseKeySequence("ctrl+shift+x"), Command: clipboard.CommandCut, When: keybind.WhenFocusedClipboardTarget()},
			{Key: keybind.MustParseKeySequence("ctrl+shift+v"), Command: clipboard.CommandPaste, When: keybind.WhenFocusedClipboardTarget()},
		},
	}
}

func (a *WidgetApp) registerCommands(registry *keybind.CommandRegistry) {
	if registry == nil {
		return
	}

	registry.RegisterAll(
		keybind.Command{
			ID:          "palette",
			Title:       "Command Palette",
			Description: "Open the command palette",
			Category:    "View",
			Handler: func(ctx keybind.Context) {
				a.showCommandPalette()
			},
		},
		keybind.Command{
			ID:          "new",
			Title:       "New Conversation",
			Description: "Start a new conversation",
			Category:    "Session",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("new")
			},
		},
		keybind.Command{
			ID:          "clear",
			Title:       "Clear Messages",
			Description: "Clear the current conversation",
			Category:    "Session",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("clear")
			},
		},
		keybind.Command{
			ID:          "history",
			Title:       "View History",
			Description: "Show conversation history",
			Category:    "Session",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("history")
			},
		},
		keybind.Command{
			ID:          "search",
			Title:       "Search Conversation",
			Description: "Search the conversation",
			Category:    "Session",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("search")
			},
		},
		keybind.Command{
			ID:          "export",
			Title:       "Export Conversation",
			Description: "Export the current conversation",
			Category:    "Session",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("export")
			},
		},
		keybind.Command{
			ID:          "import",
			Title:       "Import Conversation",
			Description: "Import a conversation file",
			Category:    "Session",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("import")
			},
		},
		keybind.Command{
			ID:          "session-next",
			Title:       "Next Session",
			Description: "Switch to the next session",
			Category:    "Session",
			Enabled: func(ctx keybind.Context) bool {
				return a.onNextSession != nil
			},
			Handler: func(ctx keybind.Context) {
				if a.onNextSession != nil {
					a.onNextSession()
				}
			},
		},
		keybind.Command{
			ID:          "session-prev",
			Title:       "Previous Session",
			Description: "Switch to the previous session",
			Category:    "Session",
			Enabled: func(ctx keybind.Context) bool {
				return a.onPrevSession != nil
			},
			Handler: func(ctx keybind.Context) {
				if a.onPrevSession != nil {
					a.onPrevSession()
				}
			},
		},
		keybind.Command{
			ID:          "toggle-sidebar",
			Title:       "Toggle Sidebar",
			Description: "Show or hide the sidebar",
			Category:    "View",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("toggle-sidebar")
			},
		},
		keybind.Command{
			ID:          "ui-settings",
			Title:       "UI Settings",
			Description: "Adjust UI preferences",
			Category:    "View",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("ui-settings")
			},
		},
		keybind.Command{
			ID:          "file-picker",
			Title:       "Open File Picker",
			Description: "Open the file picker",
			Category:    "View",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("file-picker")
			},
		},
		keybind.Command{
			ID:          "scroll-top",
			Title:       "Scroll to Top",
			Description: "Jump to the top of the conversation",
			Category:    "View",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("scroll-top")
			},
		},
		keybind.Command{
			ID:          "scroll-bottom",
			Title:       "Scroll to Bottom",
			Description: "Jump to the latest message",
			Category:    "View",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("scroll-bottom")
			},
		},
		keybind.Command{
			ID:          "copy-code",
			Title:       "Copy Last Code Block",
			Description: "Copy the most recent code block",
			Category:    "View",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("copy-code")
			},
		},
		keybind.Command{
			ID:          "focus-mode",
			Title:       "Toggle Focus Mode",
			Description: "Hide chrome and focus on chat",
			Category:    "View",
			Handler: func(ctx keybind.Context) {
				a.toggleFocusMode()
			},
		},
		keybind.Command{
			ID:          "open-web-session",
			Title:       "Open Web Session",
			Description: "Open the current session in the web UI",
			Category:    "View",
			Handler: func(ctx keybind.Context) {
				a.openWebTarget("session", false)
			},
		},
		keybind.Command{
			ID:          "open-web-dashboard",
			Title:       "Open Web Dashboard",
			Description: "Open the web dashboard",
			Category:    "View",
			Handler: func(ctx keybind.Context) {
				a.openWebTarget("dashboard", false)
			},
		},
		keybind.Command{
			ID:          "models",
			Title:       "Select Model",
			Description: "Pick an execution model",
			Category:    "Model",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("models")
			},
		},
		keybind.Command{
			ID:          "usage",
			Title:       "Show Usage Stats",
			Description: "Show token usage",
			Category:    "Model",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("usage")
			},
		},
		keybind.Command{
			ID:          "plans",
			Title:       "List Plans",
			Description: "List available plans",
			Category:    "Plan",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("plans")
			},
		},
		keybind.Command{
			ID:          "status",
			Title:       "Plan Status",
			Description: "Show plan status",
			Category:    "Plan",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("status")
			},
		},
		keybind.Command{
			ID:          "help",
			Title:       "Show Help",
			Description: "Show available commands",
			Category:    "System",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("help")
			},
		},
		keybind.Command{
			ID:          "config",
			Title:       "View Config",
			Description: "Open configuration view",
			Category:    "System",
			Handler: func(ctx keybind.Context) {
				a.handlePaletteCommand("config")
			},
		},
		keybind.Command{
			ID:          "quit",
			Title:       "Quit Buckley",
			Description: "Exit Buckley",
			Category:    "System",
			Handler: func(ctx keybind.Context) {
				a.Quit()
			},
		},
		keybind.Command{
			ID:          "debug",
			Title:       "Dump Screen",
			Description: "Dump the current screen state",
			Category:    "System",
			Handler: func(ctx keybind.Context) {
				a.debugDumpScreen()
			},
		},
		keybind.Command{
			ID:          "focus.next",
			Title:       "Next Focus",
			Description: "Move focus to the next widget",
			Category:    "Focus",
			Handler: func(ctx keybind.Context) {
				if scope := a.screen.BaseFocusScope(); scope != nil {
					scope.FocusNext()
					a.dirty = true
				}
			},
		},
		keybind.Command{
			ID:          "focus.prev",
			Title:       "Previous Focus",
			Description: "Move focus to the previous widget",
			Category:    "Focus",
			Handler: func(ctx keybind.Context) {
				if scope := a.screen.BaseFocusScope(); scope != nil {
					scope.FocusPrev()
					a.dirty = true
				}
			},
		},
		keybind.Command{
			ID:          "scroll.pageUp",
			Title:       "Page Up",
			Description: "Scroll up by one page",
			Category:    "View",
			Handler: func(ctx keybind.Context) {
				a.scrollPage(ctx, -1)
			},
		},
		keybind.Command{
			ID:          "scroll.pageDown",
			Title:       "Page Down",
			Description: "Scroll down by one page",
			Category:    "View",
			Handler: func(ctx keybind.Context) {
				a.scrollPage(ctx, 1)
			},
		},
		keybind.Command{
			ID:          clipboard.CommandCopy,
			Title:       "Copy",
			Description: "Copy from the focused widget",
			Category:    "Clipboard",
			Enabled: func(ctx keybind.Context) bool {
				if a.clipboard == nil || !a.clipboard.Available() {
					return false
				}
				_, ok := ctx.Focused.(clipboard.Target)
				return ok
			},
			Handler: func(ctx keybind.Context) {
				target, ok := ctx.Focused.(clipboard.Target)
				if !ok || a.clipboard == nil || !a.clipboard.Available() {
					return
				}
				text, ok := target.ClipboardCopy()
				if !ok {
					return
				}
				if err := a.clipboard.Write(text); err != nil {
					a.setStatusOverride("Copy failed: "+err.Error(), 3*time.Second)
					return
				}
				a.setStatusOverride("Copied to clipboard", 2*time.Second)
			},
		},
		keybind.Command{
			ID:          clipboard.CommandCut,
			Title:       "Cut",
			Description: "Cut from the focused widget",
			Category:    "Clipboard",
			Enabled: func(ctx keybind.Context) bool {
				if a.clipboard == nil || !a.clipboard.Available() {
					return false
				}
				_, ok := ctx.Focused.(clipboard.Target)
				return ok
			},
			Handler: func(ctx keybind.Context) {
				target, ok := ctx.Focused.(clipboard.Target)
				if !ok || a.clipboard == nil || !a.clipboard.Available() {
					return
				}
				text, ok := target.ClipboardCut()
				if !ok {
					return
				}
				if err := a.clipboard.Write(text); err != nil {
					a.setStatusOverride("Cut failed: "+err.Error(), 3*time.Second)
					return
				}
				a.setStatusOverride("Cut to clipboard", 2*time.Second)
				a.dirty = true
			},
		},
		keybind.Command{
			ID:          clipboard.CommandPaste,
			Title:       "Paste",
			Description: "Paste into the focused widget",
			Category:    "Clipboard",
			Enabled: func(ctx keybind.Context) bool {
				if a.clipboard == nil || !a.clipboard.Available() {
					return false
				}
				_, ok := ctx.Focused.(clipboard.Target)
				return ok
			},
			Handler: func(ctx keybind.Context) {
				target, ok := ctx.Focused.(clipboard.Target)
				if !ok || a.clipboard == nil || !a.clipboard.Available() {
					return
				}
				text, err := a.clipboard.Read()
				if err != nil {
					a.setStatusOverride("Paste failed: "+err.Error(), 3*time.Second)
					return
				}
				if !target.ClipboardPaste(text) {
					return
				}
				a.dirty = true
			},
		},
	)
}

func (a *WidgetApp) executeCommandID(id string) bool {
	if a.keyRegistry == nil || id == "" {
		return false
	}
	if !a.keyRegistry.Execute(id, a.keyContext()) {
		return false
	}
	if a.commandPalette != nil && id != "palette" {
		a.commandPalette.Record(id)
	}
	return true
}

// handleCommand processes commands emitted by widgets.
func (a *WidgetApp) handleCommand(cmd runtime.Command) {
	switch c := cmd.(type) {
	case runtime.FileSelected:
		// Insert file path into input or notify callback
		if a.onFileSelect != nil {
			a.onFileSelect(c.Path)
		}
	case buckleywidgets.ApprovalResponse:
		// Notify callback with approval decision
		if a.onApproval != nil {
			a.onApproval(c.RequestID, c.Approved, c.AlwaysAllow)
		}
	case runtime.PaletteSelected:
		a.executeCommandID(c.ID)
	case runtime.Quit:
		a.Quit()
	case runtime.Submit:
		// Submission is handled directly by inputArea's onSubmit callback
		// This command is just a notification that bubbles up from the widget
	case runtime.Cancel:
		// Overlay dismissal handled by Screen
	}
}

// handlePaletteCommand handles command palette selections.
func (a *WidgetApp) handlePaletteCommand(id string) {
	switch id {
	// Session commands
	case "new":
		a.ClearScrollback()
		a.AddMessage("Started new conversation", "system")
	case "clear":
		a.ClearScrollback()
	case "history":
		// Submit /history command
		if a.onSubmit != nil {
			a.onSubmit("/history")
		}
	case "search":
		a.showSearchOverlay()
	case "export":
		if a.onSubmit != nil {
			a.onSubmit("/export")
		}
	case "import":
		if a.onSubmit != nil {
			a.onSubmit("/import")
		}

	// View commands
	case "toggle-sidebar":
		a.toggleSidebar()
	case "ui-settings":
		a.showSettingsDialog()
	case "file-picker":
		a.showFilePicker()
	case "scroll-top":
		a.chatView.ScrollToTop()
		a.updateScrollStatus()
		a.setStatusOverride("Top of conversation", 2*time.Second)
	case "scroll-bottom":
		a.jumpToLatest()
	case "copy-code":
		a.copyLatestCodeBlock()

	// Model commands
	case "models":
		if a.onSubmit != nil {
			a.onSubmit("/model")
		}
	case "usage":
		if a.onSubmit != nil {
			a.onSubmit("/usage")
		}

	// Plan commands
	case "plans":
		if a.onSubmit != nil {
			a.onSubmit("/plans")
		}
	case "status":
		if a.onSubmit != nil {
			a.onSubmit("/status")
		}

	// System commands
	case "help":
		if a.onSubmit != nil {
			a.onSubmit("/help")
		}
	case "config":
		if a.onSubmit != nil {
			a.onSubmit("/config")
		}
	case "quit":
		a.Quit()
	}
}

// showCommandPalette creates and displays the command palette overlay.
func (a *WidgetApp) showCommandPalette() {
	if a.commandPalette == nil {
		return
	}
	a.commandPalette.Refresh()
	a.commandPalette.SetKeymapStack(a.keymaps)
	palette := a.commandPalette.Widget
	palette.SetPlaceholder("> ")
	palette.SetMaxVisible(12)
	palette.SetStyles(
		a.style(a.theme.Surface),
		a.style(a.theme.Border),
		a.style(a.theme.Accent),
		a.style(a.theme.TextPrimary),
		a.style(a.theme.TextPrimary),
		a.style(a.theme.Selection),
		a.style(a.theme.TextSecondary),
	)
	palette.Focus()

	// Push as modal overlay
	a.screen.PushLayer(palette, true)
	a.dirty = true
}

// showSlashCommandPalette creates and displays the slash command palette.
func (a *WidgetApp) showSlashCommandPalette() {
	palette := widgets.NewPaletteWidget("Commands")
	palette.SetPlaceholder("/")
	palette.SetMaxVisible(10)

	// Slash command items
	items := []widgets.PaletteItem{
		{ID: "/review", Label: "/review", Description: "Review current git diff"},
		{ID: "/commit", Label: "/commit", Description: "Generate commit message"},
		{ID: "/new", Label: "/new", Description: "Start a new session"},
		{ID: "/clear", Label: "/clear", Description: "Clear current session"},
		{ID: "/search", Label: "/search", Description: "Search conversation history"},
		{ID: "/export", Label: "/export", Description: "Export current session"},
		{ID: "/import", Label: "/import", Description: "Import conversation file"},
		{ID: "/sessions", Label: "/sessions", Description: "List active sessions"},
		{ID: "/next", Label: "/next", Description: "Switch to next session"},
		{ID: "/prev", Label: "/prev", Description: "Switch to previous session"},
		{ID: "/model", Label: "/model", Description: "Select execution model"},
		{ID: "/model curate", Label: "/model curate", Description: "Curate models for ACP/editor pickers"},
		{ID: "/context", Label: "/context", Description: "Show context budget details"},
		{ID: "/help", Label: "/help", Description: "Show available commands"},
		{ID: "/quit", Label: "/quit", Description: "Exit Buckley"},
	}
	palette.SetItems(items)

	palette.SetOnSelect(func(item widgets.PaletteItem) {
		// Execute the slash command
		if a.onSubmit != nil {
			a.onSubmit(item.ID)
		}
	})

	palette.SetStyles(
		a.style(a.theme.Surface),
		a.style(a.theme.Border),
		a.style(a.theme.Accent),
		a.style(a.theme.TextPrimary),
		a.style(a.theme.TextPrimary),
		a.style(a.theme.Selection),
		a.style(a.theme.TextSecondary),
	)
	palette.Focus()

	// Push as modal overlay
	a.screen.PushLayer(palette, true)
	a.dirty = true
}

// showSearchOverlay creates and displays the search bar overlay.
func (a *WidgetApp) showSearchOverlay() {
	search := widgets.NewSearchWidget()
	search.SetStyles(
		a.style(a.theme.Surface),
		a.style(a.theme.Border),
		a.style(a.theme.TextPrimary),
		a.style(a.theme.Accent),
	)
	search.SetOnSearch(func(query string) {
		a.chatView.Search(query)
		current, total := a.chatView.SearchMatches()
		search.SetMatchInfo(current, total)
		a.updateScrollStatus()
		a.dirty = true
	})
	search.SetOnNavigate(func() {
		a.chatView.NextMatch()
		current, total := a.chatView.SearchMatches()
		search.SetMatchInfo(current, total)
		a.updateScrollStatus()
		a.dirty = true
	}, func() {
		a.chatView.PrevMatch()
		current, total := a.chatView.SearchMatches()
		search.SetMatchInfo(current, total)
		a.updateScrollStatus()
		a.dirty = true
	})
	search.Focus()

	// Push as modal overlay
	a.screen.PushLayer(search, true)
	a.dirty = true
}

// showFilePicker creates and displays the file picker overlay.
func (a *WidgetApp) showFilePicker() {
	// Activate the picker (position doesn't matter for centered overlay)
	a.filePicker.Activate(0)

	// Create widget wrapper
	pickerWidget := buckleywidgets.NewFilePickerWidget(a.filePicker)
	pickerWidget.SetStyles(
		a.style(a.theme.Surface),
		a.style(a.theme.Border),
		a.style(a.theme.TextPrimary),
		a.style(a.theme.Selection),
		a.style(a.theme.Accent),
		a.style(a.theme.TextPrimary),
	)
	pickerWidget.Focus()

	// Push as modal overlay
	a.screen.PushLayer(pickerWidget, true)
	a.dirty = true
}

func (a *WidgetApp) showSettingsDialog() {
	if a == nil || a.screen == nil {
		return
	}

	reduce := widgets.NewCheckbox("Reduce motion")
	reduce.SetChecked(boolPtr(a.reduceMotion))
	reduce.SetOnChange(func(value *bool) {
		a.setReduceMotion(value != nil && *value)
	})

	contrast := widgets.NewCheckbox("High contrast")
	contrast.SetChecked(boolPtr(a.highContrast))
	contrast.SetOnChange(func(value *bool) {
		a.setHighContrast(value != nil && *value)
	})

	labels := widgets.NewCheckbox("Text labels for focus")
	labels.SetChecked(boolPtr(a.useTextLabels))
	labels.SetOnChange(func(value *bool) {
		a.setUseTextLabels(value != nil && *value)
	})

	muted := false
	if a.audioService != nil {
		muted = a.audioService.Muted()
	}
	audioMute := widgets.NewCheckbox("Mute audio cues")
	audioMute.SetChecked(boolPtr(muted))
	audioMute.SetOnChange(func(value *bool) {
		a.setAudioMuted(value != nil && *value)
	})

	baseStyle := a.style(a.theme.TextPrimary)
	focusStyle := a.style(a.theme.Selection)
	mutedStyle := a.style(a.theme.TextMuted)
	for _, cb := range []*widgets.Checkbox{reduce, contrast, labels, audioMute} {
		cb.SetStyle(baseStyle)
		cb.SetFocusStyle(focusStyle)
	}

	group := widgets.NewRadioGroup()
	always := widgets.NewRadio("Always show message metadata", group)
	hover := widgets.NewRadio("Show metadata on hover", group)
	never := widgets.NewRadio("Hide metadata", group)

	for _, radio := range []*widgets.Radio{always, hover, never} {
		radio.SetStyle(baseStyle)
		radio.SetFocusStyle(focusStyle)
	}
	group.SetSelected(metadataModeIndex(a.messageMetadata))
	group.OnChange(func(index int) {
		a.setMessageMetadataMode(metadataModeFromIndex(index))
	})

	appearance := runtime.VBox(
		runtime.Fixed(contrast),
		runtime.Fixed(reduce),
		runtime.Fixed(labels),
	).WithGap(1)
	appearancePanel := widgets.NewPanel(appearance).WithTitle("Appearance")
	appearancePanel.SetStyle(a.style(a.theme.SurfaceRaised))
	appearancePanel.WithBorder(a.style(a.theme.Border))

	audio := runtime.VBox(runtime.Fixed(audioMute)).WithGap(1)
	audioPanel := widgets.NewPanel(audio).WithTitle("Audio")
	audioPanel.SetStyle(a.style(a.theme.SurfaceRaised))
	audioPanel.WithBorder(a.style(a.theme.Border))

	hint := widgets.NewLabel("Metadata hints show on hover in chat view.")
	hint.SetStyle(mutedStyle)
	metadata := runtime.VBox(
		runtime.Fixed(always),
		runtime.Fixed(hover),
		runtime.Fixed(never),
		runtime.Fixed(hint),
	).WithGap(1)
	metadataPanel := widgets.NewPanel(metadata).WithTitle("Metadata")
	metadataPanel.SetStyle(a.style(a.theme.SurfaceRaised))
	metadataPanel.WithBorder(a.style(a.theme.Border))

	grid := widgets.NewGrid(2, 2)
	grid.Gap = 1
	grid.Add(appearancePanel, 0, 0, 1, 1)
	grid.Add(audioPanel, 0, 1, 1, 1)
	grid.Add(metadataPanel, 1, 0, 1, 2)

	dialog := widgets.NewDialog("UI Settings", "", widgets.DialogButton{
		Label: "Done",
		Key:   'D',
		OnClick: func() {
			a.screen.PopLayer()
			a.dirty = true
		},
	}).WithContent(grid).OnDismiss(func() {
		a.screen.PopLayer()
		a.dirty = true
	})
	dialog.SetStyle(a.style(a.theme.SurfaceRaised))

	overlay := buckleywidgets.NewCenteredOverlay(dialog)
	contrast.Focus()
	a.screen.PushLayer(overlay, true)
	a.dirty = true
}

// showApprovalDialog creates and displays an approval dialog.
func (a *WidgetApp) showApprovalDialog(msg ApprovalRequestMsg) {
	var details strings.Builder
	details.WriteString("Tool: ")
	details.WriteString(strings.TrimSpace(msg.Tool))
	if strings.TrimSpace(msg.Operation) != "" {
		details.WriteString("\nOperation: ")
		details.WriteString(strings.TrimSpace(msg.Operation))
	}
	if strings.TrimSpace(msg.Description) != "" {
		details.WriteString("\n\n")
		details.WriteString(strings.TrimSpace(msg.Description))
	}
	if strings.TrimSpace(msg.Command) != "" {
		details.WriteString("\n\nCommand:\n")
		details.WriteString(strings.TrimSpace(msg.Command))
	}
	if strings.TrimSpace(msg.FilePath) != "" {
		details.WriteString("\n\nFile: ")
		details.WriteString(strings.TrimSpace(msg.FilePath))
	}
	if len(msg.DiffLines) > 0 {
		details.WriteString("\nChanges:\n")
		limit := 12
		if len(msg.DiffLines) < limit {
			limit = len(msg.DiffLines)
		}
		for i := 0; i < limit; i++ {
			line := msg.DiffLines[i]
			prefix := "  "
			switch line.Type {
			case DiffAdd:
				prefix = "+ "
			case DiffRemove:
				prefix = "- "
			}
			details.WriteString(prefix)
			details.WriteString(line.Content)
			details.WriteString("\n")
		}
		if len(msg.DiffLines) > limit {
			details.WriteString("... (more changes)\n")
		}
	}
	if msg.AddedLines != 0 || msg.RemovedLines != 0 {
		details.WriteString("\nSummary: +")
		details.WriteString(strconv.Itoa(msg.AddedLines))
		details.WriteString(" / -")
		details.WriteString(strconv.Itoa(msg.RemovedLines))
	}

	body := buckleywidgets.NewTextBlock(details.String())
	body.SetStyle(a.style(a.theme.TextPrimary))

	handleDecision := func(approved bool, always bool) {
		if a.onApproval != nil {
			a.onApproval(msg.ID, approved, always)
		}
		a.screen.PopLayer()
		a.dirty = true
	}

	dialog := widgets.NewDialog("Approval Required", "",
		widgets.DialogButton{Label: "Allow", Key: 'A', OnClick: func() { handleDecision(true, false) }},
		widgets.DialogButton{Label: "Deny", Key: 'D', OnClick: func() { handleDecision(false, false) }},
		widgets.DialogButton{Label: "Always Allow", Key: 'L', OnClick: func() { handleDecision(true, true) }},
	)
	dialog.WithContent(body)
	dialog.OnDismiss(func() {
		handleDecision(false, false)
	})
	dialog.Focus()
	overlay := buckleywidgets.NewCenteredOverlay(dialog)
	a.screen.PushLayer(overlay, true)
	a.dirty = true
}

func boolPtr(value bool) *bool {
	v := value
	return &v
}

func metadataModeIndex(mode string) int {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "hover":
		return 1
	case "never":
		return 2
	default:
		return 0
	}
}

func metadataModeFromIndex(index int) string {
	switch index {
	case 1:
		return "hover"
	case 2:
		return "never"
	default:
		return "always"
	}
}
