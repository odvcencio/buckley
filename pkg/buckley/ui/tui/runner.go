// Package tui provides the integrated terminal user interface for Buckley.
// This file implements the fluffyui-native runner using runtime.App.

package tui

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/buckley/ui/filepicker"
	uiservices "github.com/odvcencio/buckley/pkg/buckley/ui/tui/services"
	uitstate "github.com/odvcencio/buckley/pkg/buckley/ui/tui/state"
	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/buckley/pkg/diagnostics"
	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/fluffy"
	"github.com/odvcencio/fluffyui/keybind"
	"github.com/odvcencio/fluffyui/markdown"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/theme"
	"github.com/odvcencio/fluffyui/toast"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// RunnerConfig configures the fluffyui-native TUI runner.
type RunnerConfig struct {
	Theme           *theme.Theme
	ModelName       string
	SessionID       string
	WorkDir         string
	ProjectRoot     string
	ReduceMotion    bool
	HighContrast    bool
	UseTextLabels   bool
	MessageMetadata string
	WebBaseURL      string
	Audio           AudioConfig
	AgentSocket     string

	// Callbacks
	OnSubmit      func(text string)
	OnQuit        func()
	OnFileSelect  func(path string)
	OnShellCmd    func(cmd string) string
	OnNextSession func()
	OnPrevSession func()
	OnApproval    func(requestID string, approved, alwaysAllow bool)

	// Testing
	Backend backend.Backend
}

// Compile-time interface check
var _ App = (*Runner)(nil)

// Runner is the fluffyui-native TUI runner.
type Runner struct {
	app    *runtime.App
	bundle *fluffy.Bundle

	// Widget references
	header       *buckleywidgets.Header
	chatView     *buckleywidgets.ChatView
	inputArea    *buckleywidgets.InputArea
	statusBar    *buckleywidgets.StatusBar
	sidebar      *buckleywidgets.Sidebar
	presence     *buckleywidgets.PresenceStrip
	toastStack   *buckleywidgets.SignalToastStack
	filePicker   *filepicker.FilePicker
	modelPalette *uiwidgets.PaletteWidget

	// State
	state            *uitstate.AppState
	statusService    *uiservices.StatusService
	chatService      *uiservices.ChatService
	sidebarService   *uiservices.SidebarService
	coalescer        *Coalescer
	reasoningBuilder strings.Builder
	styleCache       *StyleCache

	// Config
	theme       *theme.Theme
	workDir     string
	projectRoot string
	webBaseURL  string
	sessionID   string

	// Accessibility
	reduceMotion bool

	// Callbacks
	onSubmit      func(text string)
	onQuit        func()
	onFileSelect  func(path string)
	onShellCmd    func(cmd string) string
	onApproval    func(requestID string, approved, alwaysAllow bool)
	onNextSession func()
	onPrevSession func()

	// Agent server for real-time control
	agentServer *AgentServer
}

// NewRunner creates a new fluffyui-native TUI runner.
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	th := cfg.Theme
	if th == nil {
		th = theme.DefaultTheme()
	}

	workDir := cfg.WorkDir
	if workDir == "" {
		workDir = "."
	}
	projectRoot := cfg.ProjectRoot
	if projectRoot == "" {
		projectRoot = workDir
	}

	styleCache := NewStyleCache()
	appState := uitstate.NewAppState()
	statusService := uiservices.NewStatusService(appState, nil)
	chatService := uiservices.NewChatService(appState)
	sidebarService := uiservices.NewSidebarService(appState)
	appState.ModelName.Set(strings.TrimSpace(cfg.ModelName))
	appState.SessionID.Set(strings.TrimSpace(cfg.SessionID))
	chatService.SetModelName(cfg.ModelName)

	r := &Runner{
		theme:          th,
		workDir:        workDir,
		projectRoot:    projectRoot,
		webBaseURL:     normalizeWebBaseURL(cfg.WebBaseURL),
		sessionID:      strings.TrimSpace(cfg.SessionID),
		state:          appState,
		statusService:  statusService,
		chatService:    chatService,
		sidebarService: sidebarService,
		styleCache:     styleCache,
		reduceMotion:   cfg.ReduceMotion,
		onSubmit:       cfg.OnSubmit,
		onQuit:         cfg.OnQuit,
		onFileSelect:   cfg.OnFileSelect,
		onShellCmd:     cfg.OnShellCmd,
		onApproval:     cfg.OnApproval,
	}

	// Build widgets
	r.buildWidgets(cfg, styleCache)

	// Build widget tree
	root := r.buildLayout()

	// Create keymap with Buckley-specific bindings
	// Note: Standard commands (quit, scroll, clipboard) are registered automatically
	keymap := &keybind.Keymap{
		Name: "buckley",
		Bindings: []keybind.Binding{
			// App commands (from RegisterStandardCommands)
			{Key: keybind.MustParseKeySequence("ctrl+q"), Command: "app.quit"},
			{Key: keybind.MustParseKeySequence("f5"), Command: "app.refresh"},
			// Focus commands (from RegisterStandardCommands)
			{Key: keybind.MustParseKeySequence("tab"), Command: "focus.next"},
			{Key: keybind.MustParseKeySequence("shift+tab"), Command: "focus.prev"},
			// Scroll commands (from RegisterScrollCommands)
			{Key: keybind.MustParseKeySequence("ctrl+up"), Command: "scroll.up"},
			{Key: keybind.MustParseKeySequence("ctrl+down"), Command: "scroll.down"},
			{Key: keybind.MustParseKeySequence("pgup"), Command: "scroll.pageUp"},
			{Key: keybind.MustParseKeySequence("pgdn"), Command: "scroll.pageDown"},
			{Key: keybind.MustParseKeySequence("home"), Command: "scroll.top"},
			{Key: keybind.MustParseKeySequence("end"), Command: "scroll.bottom"},
			// Clipboard commands (from RegisterClipboardCommands)
			{Key: keybind.MustParseKeySequence("ctrl+shift+c"), Command: "clipboard.copy", When: keybind.WhenFocusedClipboardTarget()},
			{Key: keybind.MustParseKeySequence("ctrl+shift+x"), Command: "clipboard.cut", When: keybind.WhenFocusedClipboardTarget()},
			{Key: keybind.MustParseKeySequence("ctrl+shift+v"), Command: "clipboard.paste", When: keybind.WhenFocusedClipboardTarget()},
			// Buckley-specific commands
			{Key: keybind.MustParseKeySequence("ctrl+b"), Command: "buckley.toggle-sidebar"},
			{Key: keybind.MustParseKeySequence("alt+right"), Command: "buckley.next-session"},
			{Key: keybind.MustParseKeySequence("alt+left"), Command: "buckley.prev-session"},
			{Key: keybind.MustParseKeySequence("ctrl+f"), Command: "buckley.search"},
			{Key: keybind.MustParseKeySequence("ctrl+i"), Command: "buckley.focus-input"},
		},
	}

	// Build app options
	opts := []fluffy.AppOption{
		fluffy.WithRoot(root),
		fluffy.WithUpdate(r.update),
		fluffy.WithTickRate(16 * time.Millisecond),
		fluffy.WithKeyBindings(r.registerKeyBindings),
		fluffy.WithKeymap(keymap),
	}

	if cfg.Backend != nil {
		opts = append(opts, fluffy.WithBackend(cfg.Backend))
	}

	audioService := buildAudioService(cfg.Audio)
	if audioService != nil {
		opts = append(opts, fluffy.WithAudio(audioService))
	}

	// Create bundle (app + keybindings)
	bundle, err := fluffy.NewBundle(opts...)
	if err != nil {
		return nil, err
	}

	r.app = bundle.App
	r.bundle = bundle

	// Create coalescer that posts to app via CustomMsg
	r.coalescer = NewCoalescer(DefaultCoalescerConfig(), func(msg Message) {
		r.app.Post(runtime.CustomMsg{Value: msg})
	})

	// Wire input callbacks
	r.wireInputCallbacks()

	// Initialize agent server if configured
	if cfg.AgentSocket != "" {
		if err := r.initAgentServer(cfg.AgentSocket); err != nil {
			// Log but don't fail - agent server is optional
			log.Printf("warning: failed to start agent server: %v", err)
		}
	}

	return r, nil
}

// Run starts the TUI event loop.
// Implements App interface.
func (r *Runner) Run() error {
	if r == nil || r.app == nil {
		return nil
	}
	defer r.stopAgentServer() // Ensure cleanup on exit
	return r.app.Run(context.Background())
}

// RunWithContext starts the TUI event loop with a cancellation context.
func (r *Runner) RunWithContext(ctx context.Context) error {
	if r == nil || r.app == nil {
		return nil
	}
	defer r.stopAgentServer() // Ensure cleanup on exit
	return r.app.Run(ctx)
}

// Post sends a Buckley domain message to the event loop.
func (r *Runner) Post(msg Message) {
	if r == nil || r.app == nil {
		return
	}
	r.app.Post(runtime.CustomMsg{Value: msg})
}

// buildWidgets creates all the Buckley widgets.
func (r *Runner) buildWidgets(cfg RunnerConfig, styleCache *StyleCache) {
	th := r.theme

	// Header
	r.header = buckleywidgets.NewHeaderWithConfig(buckleywidgets.HeaderConfig{
		ModelName: r.state.ModelName,
		SessionID: r.state.SessionID,
	})
	r.header.SetStyles(
		styleCache.Get(th.Surface),
		styleCache.Get(th.Logo),
		styleCache.Get(th.TextSecondary),
	)

	// ChatView
	r.chatView = buckleywidgets.NewChatViewWithConfig(buckleywidgets.ChatViewConfig{
		Messages:         r.state.ChatMessages,
		Thinking:         r.state.ChatThinking,
		ReasoningText:    r.state.ReasoningText,
		ReasoningPreview: r.state.ReasoningPreview,
		ReasoningVisible: r.state.ReasoningVisible,
	})
	r.chatView.SetStyles(
		styleCache.Get(th.User),
		styleCache.Get(th.Assistant),
		styleCache.Get(th.System),
		styleCache.Get(th.Tool),
		styleCache.Get(th.Thinking),
	)
	r.chatView.SetModelName(cfg.ModelName)
	r.chatView.SetMetadataStyle(styleCache.Get(th.TextMuted))
	r.chatView.SetMessageMetadataMode(cfg.MessageMetadata)
	r.chatView.SetUIStyles(
		styleCache.Get(th.Scrollbar),
		styleCache.Get(th.ScrollThumb),
		styleCache.Get(th.Selection),
		styleCache.Get(th.SearchMatch),
		styleCache.Get(th.Background),
	)
	mdRenderer := markdown.NewRenderer(th)
	r.chatView.SetMarkdownRenderer(mdRenderer, styleCache.Get(mdRenderer.CodeBlockBackground()))

	// InputArea
	r.inputArea = buckleywidgets.NewInputAreaWithConfig(buckleywidgets.InputAreaConfig{
		Text: r.state.InputText,
		Mode: r.state.InputMode,
	})
	r.inputArea.SetStyles(
		styleCache.Get(th.SurfaceRaised),
		styleCache.Get(th.TextPrimary),
		styleCache.Get(th.Border),
	)
	r.inputArea.SetModeStyles(
		styleCache.Get(th.ModeNormal),
		styleCache.Get(th.ModeShell),
		styleCache.Get(th.ModeEnv),
		styleCache.Get(th.ModeSearch),
	)

	// StatusBar
	r.statusBar = buckleywidgets.NewStatusBar(buckleywidgets.StatusBarConfig{
		StatusText:     r.state.StatusText,
		StatusOverride: r.state.StatusOverride,
		StatusMode:     r.state.StatusMode,
		Tokens:         r.state.StatusTokens,
		CostCents:      r.state.StatusCost,
		ContextUsed:    r.state.ContextUsed,
		ContextBudget:  r.state.ContextBudget,
		ContextWindow:  r.state.ContextWindow,
		ScrollPos:      r.state.ScrollPos,
		ProgressItems:  r.state.ProgressItems,
		IsStreaming:    r.state.IsStreaming,
		BGStyle:        styleCache.Get(th.Surface),
		TextStyle:      styleCache.Get(th.TextMuted),
		ModeStyle:      styleCache.Get(th.Accent),
	})

	// Sidebar
	r.sidebar = buckleywidgets.NewSidebarWithBindings(
		buckleywidgets.DefaultSidebarConfig(),
		buckleywidgets.SidebarBindings{
			State:         r.state.SidebarState,
			ContextUsed:   r.state.ContextUsed,
			ContextBudget: r.state.ContextBudget,
			ContextWindow: r.state.ContextWindow,
		},
	)
	r.sidebar.SetStyles(
		styleCache.Get(th.Border),
		styleCache.Get(th.TextSecondary),
		styleCache.Get(th.TextPrimary),
		styleCache.Get(th.Accent),
		styleCache.Get(th.TextMuted),
		styleCache.Get(th.Surface),
	)
	r.sidebar.SetProgressEdgeStyle(styleCache.Get(th.AccentGlow))
	r.sidebar.SetStatusStyles(
		styleCache.Get(th.Success),
		styleCache.Get(th.ElectricBlue).Bold(true),
		styleCache.Get(th.TextMuted),
		styleCache.Get(th.Coral),
	)
	r.sidebar.SetContextStyles(
		styleCache.Get(th.Teal),
		styleCache.Get(th.Accent),
		styleCache.Get(th.Coral),
		styleCache.Get(th.TextMuted),
	)
	r.sidebar.SetSpinnerStyle(styleCache.Get(th.ElectricBlue))
	r.sidebar.SetProjectPath(r.projectRoot)

	// Presence strip
	r.presence = buckleywidgets.NewPresenceStrip()
	r.presence.SetStyles(
		styleCache.Get(th.Border),
		styleCache.Get(th.TextMuted),
		styleCache.Get(th.ElectricBlue),
		styleCache.Get(th.Coral),
		styleCache.Get(th.Accent),
		styleCache.Get(th.Background),
	)

	// Toast stack
	r.toastStack = buckleywidgets.NewSignalToastStack(r.state.Toasts)
	r.toastStack.SetAnimationsEnabled(!cfg.ReduceMotion)
	r.toastStack.SetStyles(
		styleCache.Get(th.SurfaceRaised),
		styleCache.Get(th.TextPrimary),
		styleCache.Get(th.Info),
		styleCache.Get(th.Success),
		styleCache.Get(th.Warning),
		styleCache.Get(th.Error),
	)

	// File picker
	r.filePicker = filepicker.NewFilePicker(r.projectRoot)

	// Model palette (used by ShowModelPicker)
	r.modelPalette = uiwidgets.NewPaletteWidget("Select Model")
	r.modelPalette.SetStyles(
		styleCache.Get(th.SurfaceRaised), // bg
		styleCache.Get(th.Border),        // border
		styleCache.Get(th.TextPrimary),   // title
		styleCache.Get(th.TextPrimary),   // query
		styleCache.Get(th.TextSecondary), // item
		styleCache.Get(th.Accent),        // selected
		styleCache.Get(th.TextMuted),     // category
	)
}

// buildLayout creates the widget tree layout.
func (r *Runner) buildLayout() runtime.Widget {
	// Main content area: ChatView + Sidebar
	mainArea := runtime.HBox(
		runtime.Flexible(r.chatView, 3),
		runtime.Sized(r.sidebar, r.sidebar.Width()),
	)

	// Full layout: Header, Main, Input, Status
	root := runtime.VBox(
		runtime.Fixed(r.header),
		runtime.Expanded(mainArea),
		runtime.Fixed(r.inputArea),
		runtime.Fixed(r.statusBar),
	)

	return root
}

// wireInputCallbacks connects input area callbacks.
func (r *Runner) wireInputCallbacks() {
	r.inputArea.OnSubmit(func(text string, mode buckleywidgets.InputMode) {
		r.handleSubmit(text, mode)
	})

	r.inputArea.OnChange(func(text string) {
		// Trigger relayout for auto-resize
		r.app.Invalidate()
	})

	r.inputArea.OnTriggerPicker(func() {
		r.showFilePicker()
	})

	r.inputArea.OnTriggerSearch(func() {
		r.showSearchOverlay()
	})

	r.inputArea.OnTriggerSlashCommand(func() {
		r.showSlashCommandPalette()
	})
}

// handleSubmit processes input submission.
func (r *Runner) handleSubmit(text string, mode buckleywidgets.InputMode) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}

	switch mode {
	case buckleywidgets.ModeShell:
		if r.onShellCmd != nil {
			result := r.onShellCmd(text)
			if result != "" {
				r.chatView.AddMessage(result, "system")
			}
		}
	default:
		if r.onSubmit != nil {
			r.onSubmit(text)
		}
	}

	r.inputArea.Clear()
}

// registerKeyBindings adds Buckley-specific key bindings.
func (r *Runner) registerKeyBindings(registry *keybind.CommandRegistry) {
	// Register fluffyui standard commands (quit, refresh, focus, scroll, clipboard)
	keybind.RegisterStandardCommands(registry)
	keybind.RegisterScrollCommands(registry)
	keybind.RegisterClipboardCommands(registry)

	// Register Buckley-specific commands
	registry.Register(keybind.Command{
		ID:    "buckley.toggle-sidebar",
		Title: "Toggle Sidebar",
		Handler: func(ctx keybind.Context) {
			r.toggleSidebar()
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.next-session",
		Title: "Next Session",
		Handler: func(ctx keybind.Context) {
			if r.onNextSession != nil {
				r.onNextSession()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.prev-session",
		Title: "Previous Session",
		Handler: func(ctx keybind.Context) {
			if r.onPrevSession != nil {
				r.onPrevSession()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.search",
		Title: "Search",
		Handler: func(ctx keybind.Context) {
			r.showSearchOverlay()
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.focus-input",
		Title: "Focus Input",
		Handler: func(ctx keybind.Context) {
			r.ensureInputFocus()
		},
	})
}

// update handles messages in the event loop.
// This is the Buckley-specific update function that processes domain messages.
func (r *Runner) update(app *runtime.App, msg runtime.Message) bool {
	// First, let the default handler process terminal events
	if runtime.DefaultUpdate(app, msg) {
		return true
	}

	// Handle tick for coalescer
	if _, ok := msg.(runtime.TickMsg); ok {
		r.coalescer.Tick()
		return false
	}

	// Handle Buckley domain messages wrapped in CustomMsg
	custom, ok := msg.(runtime.CustomMsg)
	if !ok {
		return false
	}

	// Type switch on the inner value
	switch m := custom.Value.(type) {
	case StreamChunk:
		r.coalescer.Add(m.SessionID, m.Text)
		return false

	case StreamFlush:
		if r.chatService != nil {
			r.chatService.AppendToLastMessage(m.Text)
		}
		return true

	case StreamDone:
		// Flush all pending content to ensure nothing is lost
		// This handles session switches during streaming
		r.coalescer.FlushAll()
		r.coalescer.Clear(m.SessionID)
		return true

	case RefreshMsg:
		return true

	case QuitMsg:
		if r.onQuit != nil {
			r.onQuit()
		}
		app.ExecuteCommand(runtime.Quit{})
		return false

	default:
		return false
	}
}

// Announce sends an accessibility announcement.
func (r *Runner) Announce(text string, priority accessibility.Priority) {
	if r.app == nil {
		return
	}
	services := r.app.Services()
	if announcer := services.Announcer(); announcer != nil {
		announcer.Announce(text, priority)
	}
}

// =============================================================================
// Public API methods (matching WidgetApp interface)
// =============================================================================

// SetStatus updates the status bar text.
func (r *Runner) SetStatus(text string) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetStatus(text)
}

// AddMessage adds a message to the chat view.
func (r *Runner) AddMessage(content, source string) {
	if r == nil || r.chatService == nil {
		return
	}
	r.chatService.AddMessage(content, source)
}

// StreamChunk sends streaming text chunk.
func (r *Runner) StreamChunk(sessionID, text string) {
	r.Post(StreamChunk{SessionID: sessionID, Text: text})
}

// StreamEnd signals end of streaming.
func (r *Runner) StreamEnd(sessionID, fullText string) {
	r.Post(StreamDone{SessionID: sessionID, FullText: fullText})
}

// AppendToLastMessage appends text to the last message.
func (r *Runner) AppendToLastMessage(text string) {
	if r == nil || r.chatService == nil {
		return
	}
	r.chatService.AppendToLastMessage(text)
}

// AppendReasoning appends reasoning content.
func (r *Runner) AppendReasoning(text string) {
	if r == nil || r.chatService == nil {
		return
	}
	r.chatService.AppendReasoning(text)
}

// CollapseReasoning collapses the reasoning block with a preview.
func (r *Runner) CollapseReasoning(preview, full string) {
	if r == nil || r.chatService == nil {
		return
	}
	r.chatService.CollapseReasoning(preview, full)
}

// SetModel updates the displayed model name.
func (r *Runner) SetModel(name string) {
	r.SetModelName(name)
}

// SetSession updates the displayed session ID.
func (r *Runner) SetSession(id string) {
	r.SetSessionID(id)
}

// SetStreaming updates the streaming indicator.
func (r *Runner) SetStreaming(active bool) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetStreaming(active)
}

// IsStreaming returns true if currently streaming a response.
func (r *Runner) IsStreaming() bool {
	if r == nil || r.state == nil {
		return false
	}
	return r.state.IsStreaming.Get()
}

// SetContextUsage updates context usage display.
func (r *Runner) SetContextUsage(used, budget, window int) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetContextUsage(used, budget, window)
}

// Quit stops the application.
func (r *Runner) Quit() {
	r.Post(QuitMsg{})
}

// =============================================================================
// Additional API methods required by Controller
// =============================================================================

// SetModelName updates the displayed model name.
func (r *Runner) SetModelName(name string) {
	if r == nil || r.state == nil {
		return
	}
	name = strings.TrimSpace(name)
	r.state.ModelName.Set(name)
	if r.chatView != nil {
		r.chatView.SetModelName(name)
	}
	if r.chatService != nil {
		r.chatService.SetModelName(name)
	}
}

// SetSessionID updates the displayed session ID.
func (r *Runner) SetSessionID(id string) {
	if r == nil || r.state == nil {
		return
	}
	id = strings.TrimSpace(id)
	r.state.SessionID.Set(id)
	r.sessionID = id
}

// SetStatusOverride temporarily overrides the status bar text.
func (r *Runner) SetStatusOverride(text string, duration time.Duration) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetStatusOverride(text, duration)
}

// SetTokenCount updates the token count display.
func (r *Runner) SetTokenCount(tokens int, costCents float64) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetTokenCount(tokens, costCents)
}

// SetExecutionMode updates the execution mode display.
func (r *Runner) SetExecutionMode(mode string) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetMode(mode)
}

// ShowThinkingIndicator shows the thinking indicator.
func (r *Runner) ShowThinkingIndicator() {
	if r == nil || r.chatService == nil {
		return
	}
	r.chatService.ShowThinkingIndicator()
}

// RemoveThinkingIndicator hides the thinking indicator.
func (r *Runner) RemoveThinkingIndicator() {
	if r == nil || r.chatService == nil {
		return
	}
	r.chatService.RemoveThinkingIndicator()
}

// ClearScrollback clears the chat view.
func (r *Runner) ClearScrollback() {
	if r == nil || r.chatService == nil {
		return
	}
	r.chatService.ClearMessages()
}

// WelcomeScreen shows the welcome message.
func (r *Runner) WelcomeScreen() {
	if r.chatView == nil {
		return
	}
	if r.chatService != nil {
		r.chatService.ClearMessages()
		r.chatService.AddMessage("Welcome to Buckley", "system")
		r.chatService.AddMessage("Type a message to get started, or use /help for commands.", "system")
	}
}

// ShowModelPicker displays the model picker palette.
func (r *Runner) ShowModelPicker(items []uiwidgets.PaletteItem, onSelect func(item uiwidgets.PaletteItem)) {
	if r.app == nil || r.modelPalette == nil || len(items) == 0 {
		return
	}

	// Set the items
	r.modelPalette.SetItems(items)

	// Set the callback that dismisses the palette after selection
	r.modelPalette.SetOnSelect(func(item uiwidgets.PaletteItem) {
		// Dismiss the overlay via command
		r.app.ExecuteCommand(runtime.PopOverlay{})
		// Then call the user's callback
		if onSelect != nil {
			onSelect(item)
		}
	})

	// Show as overlay via command
	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: r.modelPalette,
		Modal:  true,
	})
}

// =============================================================================
// Callbacks - App interface methods
// =============================================================================

// SetCallbacks sets the submit, file select, and shell command callbacks.
func (r *Runner) SetCallbacks(onSubmit func(string), onFileSelect func(string), onShellCmd func(string) string) {
	if r == nil {
		return
	}
	r.onSubmit = onSubmit
	r.onFileSelect = onFileSelect
	r.onShellCmd = onShellCmd
}

// SetSessionCallbacks sets the next/prev session callbacks.
func (r *Runner) SetSessionCallbacks(onNext, onPrev func()) {
	if r == nil {
		return
	}
	r.onNextSession = onNext
	r.onPrevSession = onPrev
}

// SetProgress updates the progress display.
func (r *Runner) SetProgress(items []progress.Progress) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetProgress(items)
}

// SetSidebarState updates sidebar snapshot state.
func (r *Runner) SetSidebarState(state buckleywidgets.SidebarState) {
	if r == nil || r.sidebarService == nil {
		return
	}
	r.sidebarService.SetSidebarState(state)
}

// SetToasts updates the toast display.
func (r *Runner) SetToasts(toasts []*toast.Toast) {
	if r == nil || r.statusService == nil {
		return
	}
	r.statusService.SetToasts(toasts)
}

// SetToastDismissHandler sets the handler for dismissing toasts.
func (r *Runner) SetToastDismissHandler(onDismiss func(string)) {
	if r == nil {
		return
	}
	if r.toastStack == nil {
		return
	}
	r.toastStack.SetOnDismiss(onDismiss)
}

// SetDiagnostics sets the diagnostics collector.
func (r *Runner) SetDiagnostics(collector *diagnostics.Collector) {
	// Runner doesn't use diagnostics directly in the same way WidgetApp does
	// The telemetry bridge handles this through events
}
