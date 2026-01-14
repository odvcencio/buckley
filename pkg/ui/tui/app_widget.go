// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/diagnostics"
	"github.com/odvcencio/buckley/pkg/ui/backend"
	"github.com/odvcencio/buckley/pkg/ui/backend/tcell"
	"github.com/odvcencio/buckley/pkg/ui/compositor"
	"github.com/odvcencio/buckley/pkg/ui/filepicker"
	"github.com/odvcencio/buckley/pkg/ui/markdown"
	"github.com/odvcencio/buckley/pkg/ui/progress"
	"github.com/odvcencio/buckley/pkg/ui/runtime"
	"github.com/odvcencio/buckley/pkg/ui/terminal"
	"github.com/odvcencio/buckley/pkg/ui/theme"
	"github.com/odvcencio/buckley/pkg/ui/toast"
	"github.com/odvcencio/buckley/pkg/ui/widgets"
)

// RenderMetrics tracks rendering performance statistics.
type RenderMetrics struct {
	FrameCount      int64         // Total frames rendered
	DroppedFrames   int64         // Frames skipped due to being too slow
	TotalRenderTime time.Duration // Total time spent rendering
	LastFrameTime   time.Duration // Duration of last frame
	CellsUpdated    int64         // Cells updated in last frame
	FullRedraws     int64         // Number of full screen redraws
	PartialRedraws  int64         // Number of partial redraws
}

const (
	minInputHeight = 2
	minChatHeight  = 4
)

// WidgetApp is the main TUI application using the widget tree architecture.
type WidgetApp struct {
	screen  *runtime.Screen
	backend backend.Backend
	running bool

	// Widget tree
	header     *widgets.Header
	chatView   *widgets.ChatView
	inputArea  *widgets.InputArea
	statusBar  *widgets.StatusBar
	sidebar    *widgets.Sidebar
	toastStack *widgets.ToastStack
	root       runtime.Widget
	mainArea   *runtime.Flex // HBox containing chatView + sidebar

	// Sidebar state
	sidebarVisible      bool
	sidebarUserOverride bool // User manually toggled, don't auto-hide
	minWidthForSidebar  int  // Auto-hide below this width

	// File picker
	filePicker *filepicker.FilePicker

	// Message loop
	messages  chan Message
	coalescer *Coalescer

	// Frame timing
	frameTicker *time.Ticker
	lastRender  time.Time
	dirty       bool

	// Render metrics
	metrics     RenderMetrics
	styleCache  *StyleCache
	debugRender bool

	// Ctrl+C handling
	ctrlCArmedUntil     time.Time
	statusText          string
	statusOverride      string
	statusOverrideUntil time.Time
	unreadCount         int
	scrollIndicator     string
	contextUsed         int
	contextBudget       int
	contextWindow       int
	executionMode       string
	inputMeasuredHeight int
	selectionActive     bool
	selectionLastLine   int
	selectionLastCol    int
	selectionLastValid  bool
	streaming           bool
	streamAnim          int
	streamAnimLast      time.Time
	streamAnimInterval  time.Duration

	// Soft cursor animation
	cursorPulseStart    time.Time
	cursorPulsePeriod   time.Duration
	cursorPulseInterval time.Duration
	cursorPulseLast     time.Time
	cursorStyle         backend.Style
	cursorBGLow         backend.Color
	cursorBGHigh        backend.Color
	cursorFG            backend.Color

	// Callbacks
	onSubmit      func(text string)
	onQuit        func()
	onFileSelect  func(path string)
	onShellCmd    func(cmd string) string
	onNextSession func()
	onPrevSession func()
	onApproval    func(requestID string, approved, alwaysAllow bool)

	// Configuration
	theme       *theme.Theme
	workDir     string
	projectRoot string

	// Render synchronization
	renderMu sync.Mutex

	// Backend diagnostics
	diagnostics *diagnostics.Collector
}

// WidgetAppConfig configures the widget-based TUI application.
type WidgetAppConfig struct {
	Theme       *theme.Theme
	ModelName   string
	WorkDir     string
	ProjectRoot string
	OnSubmit    func(text string)
	OnQuit      func()
	Backend     backend.Backend // Optional: for testing
}

// NewWidgetApp creates and initializes the widget-based TUI application.
func NewWidgetApp(cfg WidgetAppConfig) (*WidgetApp, error) {
	// Determine working directory
	workDir := cfg.WorkDir
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	projectRoot := cfg.ProjectRoot
	if projectRoot == "" {
		projectRoot = workDir
	}

	// Create or use provided backend
	var be backend.Backend
	if cfg.Backend != nil {
		be = cfg.Backend
	} else {
		var err error
		be, err = tcell.New()
		if err != nil {
			return nil, fmt.Errorf("create backend: %w", err)
		}
	}

	// Initialize backend to get size
	if err := be.Init(); err != nil {
		return nil, fmt.Errorf("init backend: %w", err)
	}
	be.HideCursor()
	w, h := be.Size()

	// Get theme
	th := cfg.Theme
	if th == nil {
		th = theme.DefaultTheme()
	}
	styleCache := NewStyleCache()
	debugRender := strings.TrimSpace(os.Getenv("BUCKLEY_RENDER_DEBUG")) != ""

	// Create widgets
	header := widgets.NewHeader()
	header.SetModelName(cfg.ModelName)
	header.SetStyles(
		styleCache.Get(th.Surface),
		styleCache.Get(th.Logo),
		styleCache.Get(th.TextSecondary),
	)

	chatView := widgets.NewChatView()
	chatView.SetStyles(
		styleCache.Get(th.User),
		styleCache.Get(th.Assistant),
		styleCache.Get(th.System),
		styleCache.Get(th.Tool),
		styleCache.Get(th.Thinking),
	)
	chatView.SetUIStyles(
		styleCache.Get(th.Scrollbar),
		styleCache.Get(th.ScrollThumb),
		styleCache.Get(th.Selection),
		styleCache.Get(th.SearchMatch),
		styleCache.Get(th.Background),
	)
	mdRenderer := markdown.NewRenderer(th)
	chatView.SetMarkdownRenderer(mdRenderer, styleCache.Get(mdRenderer.CodeBlockBackground()))

	inputArea := widgets.NewInputArea()
	inputArea.SetStyles(
		styleCache.Get(th.SurfaceRaised),
		styleCache.Get(th.TextPrimary),
		styleCache.Get(th.Border),
	)
	inputArea.SetModeStyles(
		styleCache.Get(th.ModeNormal),
		styleCache.Get(th.ModeShell),
		styleCache.Get(th.ModeEnv),
		styleCache.Get(th.ModeSearch),
	)
	inputArea.SetHeightLimits(minInputHeight, maxInputHeight(h))

	statusBar := widgets.NewStatusBar()
	statusBar.SetStatus("Ready")
	statusBar.SetStyles(
		styleCache.Get(th.Surface),
		styleCache.Get(th.TextMuted),
	)

	toastStack := widgets.NewToastStack()
	toastStack.SetStyles(
		styleCache.Get(th.SurfaceRaised),
		styleCache.Get(th.TextPrimary),
		styleCache.Get(th.Info),
		styleCache.Get(th.Success),
		styleCache.Get(th.Warning),
		styleCache.Get(th.Error),
	)

	// Create sidebar
	sidebar := widgets.NewSidebar()
	sidebar.SetStyles(
		styleCache.Get(th.Border),
		styleCache.Get(th.TextSecondary),
		styleCache.Get(th.TextPrimary),
		styleCache.Get(th.Accent),
		styleCache.Get(th.TextMuted),
		styleCache.Get(th.Surface),
	)

	// Determine if sidebar should be visible based on terminal width and content
	sidebarVisible := w >= 80 && sidebar.HasContent()

	// Build main content area with HBox (ChatView + Sidebar)
	var mainArea *runtime.Flex
	if sidebarVisible {
		mainArea = runtime.HBox(
			runtime.Flexible(chatView, 3),           // 75% for chat
			runtime.Sized(sidebar, sidebar.Width()), // Dynamic sidebar width
		)
	} else {
		mainArea = runtime.HBox(
			runtime.Expanded(chatView), // Full width when sidebar hidden
		)
	}

	// Build widget tree using VBox layout
	// Header (fixed 1 row)
	// MainArea (HBox: ChatView + Sidebar)
	// InputArea (fixed 2+ rows)
	// StatusBar (fixed 1 row)
	root := runtime.VBox(
		runtime.Fixed(header),
		runtime.Expanded(mainArea),
		runtime.Fixed(inputArea),
		runtime.Fixed(statusBar),
	)

	// Create screen
	screen := runtime.NewScreen(w, h, th)

	// Add root layer (focus scope is created internally)
	screen.PushLayer(root, false)
	screen.PushLayer(toastStack, false)

	// Focus the input area
	inputArea.Focus()

	// Create file picker
	fp := filepicker.NewFilePicker(projectRoot)

	app := &WidgetApp{
		screen:              screen,
		backend:             be,
		header:              header,
		chatView:            chatView,
		inputArea:           inputArea,
		statusBar:           statusBar,
		sidebar:             sidebar,
		toastStack:          toastStack,
		root:                root,
		mainArea:            mainArea,
		sidebarVisible:      sidebarVisible,
		minWidthForSidebar:  80, // Lower threshold for sidebar visibility
		filePicker:          fp,
		theme:               th,
		workDir:             workDir,
		projectRoot:         projectRoot,
		onSubmit:            cfg.OnSubmit,
		onQuit:              cfg.OnQuit,
		messages:            make(chan Message, 256),
		statusText:          "Ready",
		cursorPulseStart:    time.Now(),
		cursorPulsePeriod:   2600 * time.Millisecond,
		cursorPulseInterval: 50 * time.Millisecond,
		streamAnimInterval:  120 * time.Millisecond,
		inputMeasuredHeight: inputArea.Measure(runtime.Constraints{MaxWidth: w, MaxHeight: h}).Height,
		styleCache:          styleCache,
		debugRender:         debugRender,
	}

	// Create coalescer for smooth streaming
	app.coalescer = NewCoalescer(DefaultCoalescerConfig(), app.Post)
	app.initSoftCursor()
	app.applyScrollStatus(chatView.ScrollPosition())

	chatView.OnScrollChange(func(top, total, viewHeight int) {
		if app.applyScrollStatus(top, total, viewHeight) {
			app.dirty = true
		}
	})

	// Set up input callbacks
	inputArea.OnSubmit(func(text string, mode widgets.InputMode) {
		app.handleSubmit(text, mode)
	})
	inputArea.OnChange(func(text string) {
		app.refreshInputLayout()
	})

	// Wire file picker trigger
	inputArea.OnTriggerPicker(func() {
		app.showFilePicker()
	})

	// Wire search trigger
	inputArea.OnTriggerSearch(func() {
		app.showSearchOverlay()
	})

	// Wire slash command trigger
	inputArea.OnTriggerSlashCommand(func() {
		app.showSlashCommandPalette()
	})

	return app, nil
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
	pickerWidget := widgets.NewFilePickerWidget(a.filePicker)
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

// showApprovalDialog creates and displays an approval dialog.
func (a *WidgetApp) showApprovalDialog(msg ApprovalRequestMsg) {
	// Convert message diff lines to widget diff lines
	diffLines := make([]widgets.DiffLine, len(msg.DiffLines))
	for i, line := range msg.DiffLines {
		diffLines[i] = widgets.DiffLine{
			Type:    widgets.DiffLineType(line.Type),
			Content: line.Content,
		}
	}

	// Create approval request for widget
	req := widgets.ApprovalRequest{
		ID:           msg.ID,
		Tool:         msg.Tool,
		Operation:    msg.Operation,
		Description:  msg.Description,
		Command:      msg.Command,
		FilePath:     msg.FilePath,
		DiffLines:    diffLines,
		AddedLines:   msg.AddedLines,
		RemovedLines: msg.RemovedLines,
	}

	// Create widget
	approvalWidget := widgets.NewApprovalWidget(req)
	approvalWidget.SetStyles(
		a.style(a.theme.Surface),
		a.style(a.theme.Border),
		a.style(a.theme.Accent),
		a.style(a.theme.TextPrimary),
	)
	approvalWidget.Focus()

	// Push as modal overlay
	a.screen.PushLayer(approvalWidget, true)
	a.dirty = true
}

// showCommandPalette creates and displays the command palette overlay.
func (a *WidgetApp) showCommandPalette() {
	palette := widgets.NewPaletteWidget("Commands")
	palette.SetPlaceholder("> ")
	palette.SetMaxVisible(12)

	// Default command items
	items := []widgets.PaletteItem{
		// Session commands
		{ID: "new", Category: "Session", Label: "New Conversation", Shortcut: "/new"},
		{ID: "clear", Category: "Session", Label: "Clear Messages", Shortcut: "/clear"},
		{ID: "history", Category: "Session", Label: "View History", Shortcut: "/history"},
		{ID: "search", Category: "Session", Label: "Search Conversation", Shortcut: "/search"},
		{ID: "export", Category: "Session", Label: "Export Conversation", Shortcut: "/export"},
		{ID: "import", Category: "Session", Label: "Import Conversation", Shortcut: "/import"},

		// Navigation commands
		{ID: "toggle-sidebar", Category: "View", Label: "Toggle Sidebar", Shortcut: "Ctrl+B"},
		{ID: "file-picker", Category: "View", Label: "Open File Picker", Shortcut: "@"},
		{ID: "scroll-top", Category: "View", Label: "Scroll to Top"},
		{ID: "scroll-bottom", Category: "View", Label: "Scroll to Bottom", Shortcut: "Alt+End"},
		{ID: "copy-code", Category: "View", Label: "Copy Last Code Block", Shortcut: "Alt+C"},

		// Model commands
		{ID: "models", Category: "Model", Label: "Select Model", Shortcut: "/model"},
		{ID: "usage", Category: "Model", Label: "Show Usage Stats", Shortcut: "/usage"},

		// Plan commands
		{ID: "plans", Category: "Plan", Label: "List Plans", Shortcut: "/plans"},
		{ID: "status", Category: "Plan", Label: "Plan Status", Shortcut: "/status"},

		// System commands
		{ID: "help", Category: "System", Label: "Show Help", Shortcut: "/help"},
		{ID: "config", Category: "System", Label: "View Config", Shortcut: "/config"},
		{ID: "quit", Category: "System", Label: "Quit Buckley", Shortcut: "Ctrl+C×2"},
	}
	palette.SetItems(items)

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

// ShowModelPicker displays a model picker overlay.
func (a *WidgetApp) ShowModelPicker(items []widgets.PaletteItem, onSelect func(item widgets.PaletteItem)) {
	palette := widgets.NewPaletteWidget("Models")
	palette.SetPlaceholder("search models...")
	palette.SetMaxVisible(16)
	palette.SetItems(items)
	if onSelect != nil {
		palette.SetOnSelect(onSelect)
	}
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

	a.screen.PushLayer(palette, true)
	a.dirty = true
}

// handleSubmit processes input submission based on mode.
func (a *WidgetApp) handleSubmit(text string, mode widgets.InputMode) {
	switch mode {
	case widgets.ModeShell:
		if a.onShellCmd != nil {
			// Remove leading ! if present
			cmd := text
			if len(cmd) > 0 && cmd[0] == '!' {
				cmd = cmd[1:]
			}
			result := a.onShellCmd(cmd)
			a.AddMessage("$ "+cmd, "system")
			if result != "" {
				a.AddMessage(result, "tool")
			}
		}
	case widgets.ModeEnv:
		// Handle env var lookup
		varName := text
		if len(varName) > 0 && varName[0] == '$' {
			varName = varName[1:]
		}
		value := os.Getenv(varName)
		a.AddMessage("$"+varName+" = "+value, "system")
	default:
		if a.onSubmit != nil {
			a.onSubmit(text)
		}
	}
	a.inputArea.Clear()
}

// Post sends a message to the event loop.
// Safe to call from any goroutine.
func (a *WidgetApp) Post(msg Message) {
	select {
	case a.messages <- msg:
	default:
		// Channel full - drop message
	}
}

// Run starts the TUI event loop.
func (a *WidgetApp) Run() error {
	defer a.backend.Fini()

	a.running = true
	a.dirty = true

	// Frame ticker for 60 FPS rendering
	a.frameTicker = time.NewTicker(16 * time.Millisecond)
	defer a.frameTicker.Stop()

	// Start terminal event poller in background
	go a.pollEvents()

	// Main event loop
	for a.running {
		select {
		case msg := <-a.messages:
			if a.update(msg) {
				a.dirty = true
			}

		case now := <-a.frameTicker.C:
			a.coalescer.Tick()
			if a.updateAnimations(now) {
				a.dirty = true
			}
			if a.dirty {
				a.render()
				a.dirty = false
			}
		}
	}

	return nil
}

// pollEvents reads terminal events and posts them as messages.
func (a *WidgetApp) pollEvents() {
	for a.running {
		ev := a.backend.PollEvent()
		if ev == nil {
			continue
		}

		switch e := ev.(type) {
		case terminal.KeyEvent:
			a.Post(KeyMsg{
				Key:   int(e.Key),
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
			})
		case terminal.PasteEvent:
			a.Post(PasteMsg{Text: e.Text})
		}
	}
}

func (a *WidgetApp) updateAnimations(now time.Time) bool {
	dirty := false

	if a.statusOverride != "" && !now.Before(a.statusOverrideUntil) {
		a.statusOverride = ""
		a.statusOverrideUntil = time.Time{}
		a.statusBar.SetStatus(a.statusText)
		dirty = true
	}
	if !a.ctrlCArmedUntil.IsZero() && !now.Before(a.ctrlCArmedUntil) {
		a.ctrlCArmedUntil = time.Time{}
	}

	if a.inputArea.IsFocused() {
		if a.cursorPulsePeriod <= 0 {
			a.cursorPulsePeriod = 2600 * time.Millisecond
		}
		if a.cursorPulseInterval <= 0 {
			a.cursorPulseInterval = 50 * time.Millisecond
		}
		if a.cursorPulseStart.IsZero() {
			a.cursorPulseStart = now
		}

		if now.Sub(a.cursorPulseLast) >= a.cursorPulseInterval {
			phase := a.cursorPhase(now)
			style := a.cursorStyleForPhase(phase)
			if style != a.cursorStyle {
				a.cursorStyle = style
				a.inputArea.SetCursorStyle(style)
				dirty = true
			}
			a.cursorPulseLast = now
		}
	}

	if a.streaming {
		if a.streamAnimInterval <= 0 {
			a.streamAnimInterval = 120 * time.Millisecond
		}
		if a.streamAnimLast.IsZero() {
			a.streamAnimLast = now
		}
		if now.Sub(a.streamAnimLast) >= a.streamAnimInterval {
			a.streamAnim++
			a.statusBar.SetStreamAnim(a.streamAnim)
			a.streamAnimLast = now
			dirty = true
		}
	}

	return dirty
}

// update processes a message and returns true if a render is needed.
func (a *WidgetApp) update(msg Message) bool {
	switch m := msg.(type) {
	case KeyMsg:
		return a.handleKeyMsg(m)

	case ResizeMsg:
		a.applyInputHeightLimit(m.Height)
		a.inputMeasuredHeight = a.inputArea.Measure(runtime.Constraints{MaxWidth: m.Width, MaxHeight: m.Height}).Height
		a.screen.Resize(m.Width, m.Height)
		// The ChatView widget handles its own scrollback resizing in Layout()
		a.updateSidebarVisibility()
		return true

	case StreamChunk:
		a.coalescer.Add(m.SessionID, m.Text)
		return false

	case StreamFlush:
		a.chatView.AppendText(m.Text)
		a.updateScrollStatus()
		return true

	case StreamDone:
		a.coalescer.Flush(m.SessionID)
		a.coalescer.Clear(m.SessionID)
		return true

	case AddMessageMsg:
		wasFollowing := a.isFollowing()
		a.chatView.AddMessage(m.Content, m.Source)
		if !wasFollowing && m.Source != "thinking" {
			a.unreadCount++
		}
		a.updateScrollStatus()
		return true

	case AppendMsg:
		a.chatView.AppendText(m.Text)
		a.updateScrollStatus()
		return true

	case StatusMsg:
		a.statusText = m.Text
		if a.statusOverride == "" || time.Now().After(a.statusOverrideUntil) {
			a.statusOverride = ""
			a.statusOverrideUntil = time.Time{}
			a.statusBar.SetStatus(m.Text)
			return true
		}
		return false

	case TokensMsg:
		a.statusBar.SetTokens(m.Tokens, m.CostCent)
		return true

	case ContextMsg:
		a.contextUsed = m.Used
		a.contextBudget = m.Budget
		a.contextWindow = m.Window
		a.statusBar.SetContextUsage(m.Used, m.Budget, m.Window)
		return true
	case ExecutionModeMsg:
		a.executionMode = m.Mode
		a.statusBar.SetExecutionMode(m.Mode)
		return true

	case ProgressMsg:
		a.statusBar.SetProgress(m.Items)
		return true

	case ToastsMsg:
		if a.toastStack != nil {
			a.toastStack.SetToasts(m.Toasts)
		}
		return true

	case StreamingMsg:
		a.streaming = m.Active
		a.statusBar.SetStreaming(m.Active)
		if !m.Active {
			a.streamAnim = 0
			a.statusBar.SetStreamAnim(0)
			a.streamAnimLast = time.Time{}
		}
		return true

	case ModelMsg:
		a.header.SetModelName(m.Name)
		return true

	case ThinkingMsg:
		if m.Show {
			a.chatView.AddMessage("", "thinking")
		} else {
			a.chatView.RemoveThinkingIndicator()
		}
		a.updateScrollStatus()
		return true

	case RefreshMsg:
		return true

	case QuitMsg:
		a.Quit()
		return false

	case ApprovalRequestMsg:
		a.showApprovalDialog(m)
		return true

	case MouseMsg:
		return a.handleMouseMsg(m)

	case PasteMsg:
		a.inputArea.InsertText(m.Text)
		return true

	default:
		return false
	}
}

// handleKeyMsg processes keyboard input.
func (a *WidgetApp) handleKeyMsg(m KeyMsg) bool {
	key := terminal.Key(m.Key)

	// Alt+Home/End: jump to bounds without stealing input focus
	if m.Alt && key == terminal.KeyHome {
		a.chatView.ScrollToTop()
		a.updateScrollStatus()
		a.setStatusOverride("Top of conversation", 2*time.Second)
		return true
	}
	if m.Alt && key == terminal.KeyEnd {
		a.jumpToLatest()
		return true
	}

	// Ctrl+C: clear input first, second press within 3s exits
	if key == terminal.KeyCtrlC {
		now := time.Now()
		if now.Before(a.ctrlCArmedUntil) {
			a.Quit()
			return false
		}
		hadText := a.inputArea.HasText()
		a.inputArea.Clear()
		a.ctrlCArmedUntil = now.Add(3 * time.Second)
		if hadText {
			a.setStatusOverride("Input cleared · Press Ctrl+C again to quit", 3*time.Second)
		} else {
			a.setStatusOverride("Press Ctrl+C again to quit", 3*time.Second)
		}
		return true
	}

	// Ctrl+B: toggle sidebar
	if key == terminal.KeyCtrlB || (m.Ctrl && m.Rune == 'b') {
		a.toggleSidebar()
		return true
	}

	// Ctrl+P: command palette
	if key == terminal.KeyCtrlP || (m.Ctrl && m.Rune == 'p') {
		a.showCommandPalette()
		return true
	}

	// Ctrl+F: search
	if key == terminal.KeyCtrlF || (m.Ctrl && m.Rune == 'f') {
		a.showSearchOverlay()
		return true
	}

	// Alt+C: copy last code block
	if m.Alt && (key == terminal.KeyRune && (m.Rune == 'c' || m.Rune == 'C')) {
		a.copyLatestCodeBlock()
		return true
	}

	// Ctrl+D: debug dump screen state
	if key == terminal.KeyCtrlD || (m.Ctrl && m.Rune == 'd') {
		a.debugDumpScreen()
		return true
	}

	// Ctrl+Left: toggle sidebar (alternative to Ctrl+B)
	if m.Ctrl && key == terminal.KeyLeft {
		a.toggleSidebar()
		return true
	}

	// Alt+Left/Right: session navigation
	if m.Alt {
		switch key {
		case terminal.KeyLeft:
			if a.onPrevSession != nil {
				a.onPrevSession()
			}
			return true
		case terminal.KeyRight:
			if a.onNextSession != nil {
				a.onNextSession()
			}
			return true
		}
	}

	// Convert to runtime.KeyMsg and dispatch through screen
	runtimeMsg := runtime.KeyMsg{
		Key:   key,
		Rune:  m.Rune,
		Alt:   m.Alt,
		Ctrl:  m.Ctrl,
		Shift: m.Shift,
	}

	if a.inputArea.IsFocused() {
		switch key {
		case terminal.KeyUp, terminal.KeyDown, terminal.KeyHome, terminal.KeyEnd:
			result := a.inputArea.HandleMessage(runtimeMsg)
			if result.Handled {
				for _, cmd := range result.Commands {
					a.handleCommand(cmd)
				}
				return true
			}
		}
	}

	result := a.screen.HandleMessage(runtimeMsg)

	// Process commands that bubble up from widgets
	for _, cmd := range result.Commands {
		a.handleCommand(cmd)
	}

	return result.Handled
}

// handleMouseMsg processes mouse input.
func (a *WidgetApp) handleMouseMsg(m MouseMsg) bool {
	if a.sidebarVisible && a.sidebar != nil {
		if a.sidebar.Bounds().Contains(m.X, m.Y) {
			return a.dispatchRuntimeMouse(m)
		}
	}

	// Handle scroll wheel on the chat view
	switch m.Button {
	case MouseWheelUp:
		a.chatView.ScrollUp(3)
		return true
	case MouseWheelDown:
		a.chatView.ScrollDown(3)
		return true
	}

	if m.Action == MouseRelease && a.selectionActive {
		line, col, ok := a.chatView.PositionForPoint(m.X, m.Y)
		if ok {
			a.chatView.UpdateSelection(line, col)
			a.selectionLastLine = line
			a.selectionLastCol = col
			a.selectionLastValid = true
		} else if a.selectionLastValid {
			a.chatView.UpdateSelection(a.selectionLastLine, a.selectionLastCol)
		}

		a.chatView.EndSelection()
		a.selectionActive = false
		a.selectionLastValid = false
		if a.chatView.HasSelection() {
			text := a.chatView.SelectionText()
			if text == "" {
				a.chatView.ClearSelection()
			} else if err := copyToClipboard(text); err != nil {
				a.setStatusOverride("Copy failed: "+err.Error(), 3*time.Second)
			} else {
				a.setStatusOverride("Selection copied", 2*time.Second)
			}
		}
		a.dirty = true
		return true
	}

	if m.Action == MousePress && m.Button == MouseRight {
		if _, _, ok := a.chatView.PositionForPoint(m.X, m.Y); ok {
			a.chatView.ClearSelection()
			a.selectionActive = false
			a.selectionLastValid = false
			a.setStatusOverride("Selection cleared", 2*time.Second)
			a.dirty = true
			return true
		}
	}

	if m.Action == MousePress && m.Button == MouseLeft {
		line, col, ok := a.chatView.PositionForPoint(m.X, m.Y)
		if ok {
			if !a.selectionActive {
				a.chatView.ClearSelection()
				a.chatView.StartSelection(line, col)
				a.selectionActive = true
			} else {
				a.chatView.UpdateSelection(line, col)
			}
			a.selectionLastLine = line
			a.selectionLastCol = col
			a.selectionLastValid = true
			a.dirty = true
			return true
		}
	}

	return a.dispatchRuntimeMouse(m)
}

func (a *WidgetApp) dispatchRuntimeMouse(m MouseMsg) bool {
	runtimeMsg := runtime.MouseMsg{
		X:      m.X,
		Y:      m.Y,
		Button: runtime.MouseButton(m.Button),
		Action: runtime.MouseAction(m.Action),
	}

	result := a.screen.HandleMessage(runtimeMsg)
	for _, cmd := range result.Commands {
		a.handleCommand(cmd)
	}

	return result.Handled
}

func (a *WidgetApp) applyInputHeightLimit(screenHeight int) {
	a.inputArea.SetHeightLimits(minInputHeight, maxInputHeight(screenHeight))
}

func (a *WidgetApp) refreshInputLayout() {
	w, h := a.screen.Size()
	desired := a.inputArea.Measure(runtime.Constraints{MaxWidth: w, MaxHeight: h}).Height
	if desired == a.inputMeasuredHeight {
		return
	}
	a.inputMeasuredHeight = desired
	a.screen.Resize(w, h)
	a.dirty = true
}

func (a *WidgetApp) updateSidebarVisibility() {
	// Don't auto-hide if user manually toggled the sidebar
	if a.sidebarUserOverride {
		return
	}
	w, _ := a.screen.Size()
	shouldShow := w >= a.minWidthForSidebar && a.sidebar.HasContent()
	if shouldShow == a.sidebarVisible {
		return
	}
	a.sidebarVisible = shouldShow
	a.rebuildLayout()
}

func (a *WidgetApp) updateScrollStatus() bool {
	top, total, viewHeight := a.chatView.ScrollPosition()
	return a.applyScrollStatus(top, total, viewHeight)
}

func (a *WidgetApp) applyScrollStatus(top, total, viewHeight int) bool {
	indicator := a.scrollIndicatorFor(top, total, viewHeight)
	if indicator != a.scrollIndicator {
		a.scrollIndicator = indicator
		a.statusBar.SetScrollPosition(indicator)
		return true
	}
	return false
}

func (a *WidgetApp) scrollIndicatorFor(top, total, viewHeight int) string {
	if total <= 0 || viewHeight <= 0 {
		return ""
	}

	atTop := top <= 0
	atBottom := top+viewHeight >= total
	if atBottom && a.unreadCount > 0 {
		a.unreadCount = 0
	}

	scrollRange := total - viewHeight
	var pos string
	switch {
	case atTop && scrollRange > 0:
		pos = "TOP"
	case atBottom || scrollRange <= 0:
		pos = "END"
	default:
		pct := (top * 100) / max(1, scrollRange)
		pos = fmt.Sprintf("%d%%", pct)
	}

	if a.unreadCount > 0 && !atBottom {
		pos = fmt.Sprintf("%s  %d new", pos, a.unreadCount)
	}

	return pos
}

func (a *WidgetApp) isFollowing() bool {
	top, total, viewHeight := a.chatView.ScrollPosition()
	return total == 0 || top+viewHeight >= total
}

func (a *WidgetApp) jumpToLatest() {
	a.chatView.ScrollToBottom()
	a.unreadCount = 0
	a.updateScrollStatus()
	a.setStatusOverride("Jumped to latest", 2*time.Second)
}

func (a *WidgetApp) copyLatestCodeBlock() {
	language, code, ok := a.chatView.LatestCodeBlock()
	if !ok {
		a.setStatusOverride("No code block to copy", 3*time.Second)
		return
	}
	if err := copyToClipboard(code); err != nil {
		a.setStatusOverride("Copy failed: "+err.Error(), 3*time.Second)
		return
	}
	if language == "" {
		a.setStatusOverride("Copied code block", 2*time.Second)
	} else {
		a.setStatusOverride("Copied "+language+" code block", 2*time.Second)
	}
}

// handleCommand processes commands emitted by widgets.
func (a *WidgetApp) handleCommand(cmd runtime.Command) {
	switch c := cmd.(type) {
	case runtime.FileSelected:
		// Insert file path into input or notify callback
		if a.onFileSelect != nil {
			a.onFileSelect(c.Path)
		}
	case runtime.ApprovalResponse:
		// Notify callback with approval decision
		if a.onApproval != nil {
			a.onApproval(c.RequestID, c.Approved, c.AlwaysAllow)
		}
	case runtime.PaletteSelected:
		a.handlePaletteCommand(c.ID)
	case runtime.Quit:
		a.Quit()
	case runtime.Submit:
		// Already handled by inputArea callback
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
	case "export":
		if a.onSubmit != nil {
			a.onSubmit("/export")
		}

	// View commands
	case "toggle-sidebar":
		a.toggleSidebar()
	case "file-picker":
		a.showFilePicker()
	case "scroll-top":
		a.chatView.ScrollToTop()
		a.updateScrollStatus()
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

// Quit stops the application.
func (a *WidgetApp) Quit() {
	a.running = false
	if a.onQuit != nil {
		a.onQuit()
	}
}

// render draws the UI to the backend using partial redraws.
func (a *WidgetApp) render() {
	a.renderMu.Lock()
	defer a.renderMu.Unlock()

	start := time.Now()

	// Render to buffer
	a.screen.Render()
	buf := a.screen.Buffer()

	// Track cells updated
	var cellsUpdated int64

	// Use partial redraw if only some cells changed
	if buf.IsDirty() {
		dirtyCount := buf.DirtyCount()
		w, h := buf.Size()
		totalCells := w * h

		// If more than half the cells are dirty, do a full redraw
		// (more efficient than many individual SetContent calls)
		if dirtyCount > totalCells/2 {
			for y := 0; y < h; y++ {
				for x := 0; x < w; x++ {
					cell := buf.Get(x, y)
					a.backend.SetContent(x, y, cell.Rune, nil, cell.Style)
				}
			}
			cellsUpdated = int64(totalCells)
			a.metrics.FullRedraws++
		} else {
			// Partial redraw - only dirty cells
			buf.ForEachDirtyCell(func(x, y int, cell runtime.Cell) {
				a.backend.SetContent(x, y, cell.Rune, nil, cell.Style)
				cellsUpdated++
			})
			a.metrics.PartialRedraws++
		}
		buf.ClearDirty()
	}

	// Show the screen
	a.backend.Show()

	// Update metrics
	elapsed := time.Since(start)
	a.metrics.FrameCount++
	a.metrics.TotalRenderTime += elapsed
	a.metrics.LastFrameTime = elapsed
	a.metrics.CellsUpdated = cellsUpdated

	// Check for dropped frames (if render took longer than 16ms)
	if elapsed > 16*time.Millisecond {
		a.metrics.DroppedFrames++
	}

	if a.debugRender && a.metrics.FrameCount%60 == 0 {
		avg := time.Duration(0)
		if a.metrics.FrameCount > 0 {
			avg = a.metrics.TotalRenderTime / time.Duration(a.metrics.FrameCount)
		}
		dropPct := 0.0
		if a.metrics.FrameCount > 0 {
			dropPct = float64(a.metrics.DroppedFrames) / float64(a.metrics.FrameCount) * 100
		}
		log.Printf("[render] frames=%d avg=%v dropped=%.1f%% cells=%d full=%d partial=%d",
			a.metrics.FrameCount,
			avg,
			dropPct,
			a.metrics.CellsUpdated,
			a.metrics.FullRedraws,
			a.metrics.PartialRedraws)
	}

	a.lastRender = time.Now()
}

func (a *WidgetApp) initSoftCursor() {
	accent := a.style(a.theme.Accent).FG()
	accentDim := a.style(a.theme.AccentDim).FG()
	surface := a.style(a.theme.SurfaceRaised).BG()
	textInverse := a.style(a.theme.TextInverse).FG()

	a.cursorBGHigh = accent
	a.cursorBGLow = blendColor(surface, accentDim, 0.35)
	a.cursorFG = textInverse
	a.cursorStyle = a.cursorStyleForPhase(0.2)
	a.inputArea.SetCursorStyle(a.cursorStyle)
}

func (a *WidgetApp) cursorPhase(now time.Time) float64 {
	if a.cursorPulsePeriod <= 0 {
		return 1
	}
	elapsed := now.Sub(a.cursorPulseStart)
	phase := float64(elapsed%a.cursorPulsePeriod) / float64(a.cursorPulsePeriod)
	return 0.5 - 0.5*math.Cos(2*math.Pi*phase)
}

func (a *WidgetApp) cursorStyleForPhase(phase float64) backend.Style {
	bg := blendColor(a.cursorBGLow, a.cursorBGHigh, phase)
	style := backend.DefaultStyle().Foreground(a.cursorFG).Background(bg)
	if phase < 0.35 {
		style = style.Dim(true)
	} else if phase > 0.75 {
		style = style.Bold(true)
	}
	return style
}

func (a *WidgetApp) setStatusOverride(text string, duration time.Duration) {
	if duration <= 0 {
		duration = 3 * time.Second
	}
	a.statusOverride = text
	a.statusOverrideUntil = time.Now().Add(duration)
	a.statusBar.SetStatus(text)
}

// Public API methods

// Refresh forces a re-render.
func (a *WidgetApp) Refresh() {
	a.Post(RefreshMsg{})
}

// AddMessage adds a message. Thread-safe via message passing.
func (a *WidgetApp) AddMessage(content, source string) {
	a.Post(AddMessageMsg{Content: content, Source: source})
}

// RemoveThinkingIndicator removes the thinking indicator.
func (a *WidgetApp) RemoveThinkingIndicator() {
	a.Post(ThinkingMsg{Show: false})
}

// ShowThinkingIndicator shows the thinking indicator.
func (a *WidgetApp) ShowThinkingIndicator() {
	a.Post(ThinkingMsg{Show: true})
}

// AppendToLastMessage appends text. Thread-safe via message passing.
func (a *WidgetApp) AppendToLastMessage(text string) {
	a.Post(AppendMsg{Text: text})
}

// StreamChunk sends a streaming chunk through the coalescer.
func (a *WidgetApp) StreamChunk(sessionID, text string) {
	a.Post(StreamChunk{SessionID: sessionID, Text: text})
}

// StreamEnd signals the end of a streaming session.
func (a *WidgetApp) StreamEnd(sessionID, fullText string) {
	a.Post(StreamDone{SessionID: sessionID, FullText: fullText})
}

// SetStatus updates status. Thread-safe via message passing.
func (a *WidgetApp) SetStatus(text string) {
	a.Post(StatusMsg{Text: text})
}

// SetTokenCount updates token display. Thread-safe via message passing.
func (a *WidgetApp) SetTokenCount(tokens int, costCents float64) {
	a.Post(TokensMsg{Tokens: tokens, CostCent: costCents})
}

// SetContextUsage updates context usage display. Thread-safe via message passing.
func (a *WidgetApp) SetContextUsage(used, budget, window int) {
	a.Post(ContextMsg{Used: used, Budget: budget, Window: window})
}

// SetExecutionMode updates execution mode display. Thread-safe via message passing.
func (a *WidgetApp) SetExecutionMode(mode string) {
	a.Post(ExecutionModeMsg{Mode: mode})
}

// SetProgress updates progress indicators. Thread-safe via message passing.
func (a *WidgetApp) SetProgress(items []progress.Progress) {
	a.Post(ProgressMsg{Items: items})
}

// SetToasts updates toast notifications. Thread-safe via message passing.
func (a *WidgetApp) SetToasts(toasts []*toast.Toast) {
	a.Post(ToastsMsg{Toasts: toasts})
}

// SetStreaming updates streaming indicator state. Thread-safe via message passing.
func (a *WidgetApp) SetStreaming(active bool) {
	a.Post(StreamingMsg{Active: active})
}

// SetModelName updates model display. Thread-safe via message passing.
func (a *WidgetApp) SetModelName(name string) {
	a.Post(ModelMsg{Name: name})
}

// SetCallbacks sets the event handlers.
func (a *WidgetApp) SetCallbacks(onSubmit func(string), onFileSelect func(string), onShellCmd func(string) string) {
	a.onSubmit = onSubmit
	a.onFileSelect = onFileSelect
	a.onShellCmd = onShellCmd
}

// SetSessionCallbacks sets the session navigation callbacks.
func (a *WidgetApp) SetSessionCallbacks(onNext, onPrev func()) {
	a.onNextSession = onNext
	a.onPrevSession = onPrev
}

// SetApprovalCallback sets the callback for tool approval decisions.
func (a *WidgetApp) SetApprovalCallback(onApproval func(requestID string, approved, alwaysAllow bool)) {
	a.onApproval = onApproval
}

// SetToastDismissHandler sets the handler to dismiss toasts from the UI.
func (a *WidgetApp) SetToastDismissHandler(onDismiss func(string)) {
	if a.toastStack == nil {
		return
	}
	a.toastStack.SetOnDismiss(onDismiss)
}

// RequestApproval displays an approval dialog for a tool operation.
// The callback set via SetApprovalCallback will be called with the decision.
func (a *WidgetApp) RequestApproval(req ApprovalRequestMsg) {
	a.Post(req)
}

// toggleSidebar toggles the sidebar visibility and rebuilds the layout.
func (a *WidgetApp) toggleSidebar() {
	a.sidebarVisible = !a.sidebarVisible
	a.sidebarUserOverride = true // User manually toggled, don't auto-hide
	if a.sidebarVisible {
		a.setStatusOverride("Sidebar shown", 2*time.Second)
	} else {
		a.setStatusOverride("Sidebar hidden", 2*time.Second)
	}
	a.rebuildLayout()
}

// rebuildLayout rebuilds the main area layout based on sidebar visibility.
func (a *WidgetApp) rebuildLayout() {
	// Get current screen size
	w, h := a.screen.Size()

	// Rebuild main area with or without sidebar
	if a.sidebarVisible {
		a.mainArea = runtime.HBox(
			runtime.Flexible(a.chatView, 3),
			runtime.Sized(a.sidebar, a.sidebar.Width()),
		)
	} else {
		a.mainArea = runtime.HBox(
			runtime.Expanded(a.chatView),
		)
	}

	// Rebuild root with new main area
	a.root = runtime.VBox(
		runtime.Fixed(a.header),
		runtime.Expanded(a.mainArea),
		runtime.Fixed(a.inputArea),
		runtime.Fixed(a.statusBar),
	)

	// Update screen with new root
	a.screen.SetRoot(a.root)
	a.screen.Resize(w, h)

	a.dirty = true
}

// SetSidebarVisible sets the sidebar visibility.
// Respects user override - won't change visibility if user manually toggled.
func (a *WidgetApp) SetSidebarVisible(visible bool) {
	if a.sidebarUserOverride {
		return // Don't override user's manual choice
	}
	if a.sidebarVisible != visible {
		a.sidebarVisible = visible
		a.rebuildLayout()
	}
}

// IsSidebarVisible returns the sidebar visibility state.
func (a *WidgetApp) IsSidebarVisible() bool {
	return a.sidebarVisible
}

// SetCurrentTask updates the sidebar's current task display.
func (a *WidgetApp) SetCurrentTask(name string, progress int) {
	a.sidebar.SetCurrentTask(name, progress)
	a.updateSidebarVisibility()
	a.dirty = true
}

// SetPlanTasks updates the sidebar's plan task list.
func (a *WidgetApp) SetPlanTasks(tasks []widgets.PlanTask) {
	a.sidebar.SetPlanTasks(tasks)
	a.updateSidebarVisibility()
	a.dirty = true
}

// SetRunningTools updates the sidebar's running tools list.
func (a *WidgetApp) SetRunningTools(tools []widgets.RunningTool) {
	a.sidebar.SetRunningTools(tools)
	a.updateSidebarVisibility()
	a.dirty = true
}

// SetActiveTouches updates the sidebar's active touches list.
func (a *WidgetApp) SetActiveTouches(touches []widgets.TouchSummary) {
	a.sidebar.SetActiveTouches(touches)
	a.updateSidebarVisibility()
	a.dirty = true
}

// SetRLMStatus updates the sidebar's RLM status display.
// Auto-shows sidebar when RLM content arrives (unless user manually hid it).
func (a *WidgetApp) SetRLMStatus(status *widgets.RLMStatus, scratchpad []widgets.RLMScratchpadEntry) {
	a.sidebar.SetRLMStatus(status, scratchpad)
	// Auto-show sidebar for RLM mode if user hasn't manually hidden it
	if !a.sidebarUserOverride && !a.sidebarVisible && (status != nil || len(scratchpad) > 0) {
		a.sidebarVisible = true
		a.rebuildLayout()
	}
	a.dirty = true
}

// ClearScrollback clears all messages.
func (a *WidgetApp) ClearScrollback() {
	a.chatView.Clear()
	a.unreadCount = 0
	a.updateScrollStatus()
}

// HasInput returns true if there's text in the input.
func (a *WidgetApp) HasInput() bool {
	return a.inputArea.HasText()
}

// ClearInput clears the input.
func (a *WidgetApp) ClearInput() {
	a.inputArea.Clear()
}

// Metrics returns a copy of the current render metrics.
func (a *WidgetApp) Metrics() RenderMetrics {
	a.renderMu.Lock()
	defer a.renderMu.Unlock()
	return a.metrics
}

// SetDiagnostics sets the backend diagnostics collector for debug dumps.
func (a *WidgetApp) SetDiagnostics(collector *diagnostics.Collector) {
	a.diagnostics = collector
}

// WelcomeScreen displays a beautiful welcome screen.
func (a *WidgetApp) WelcomeScreen() {
	// ASCII art logo
	logo := []string{
		"",
		"   ╭──────────────────────────────────────╮",
		"   │                                      │",
		"   │   ●  B U C K L E Y                   │",
		"   │      AI Development Assistant        │",
		"   │                                      │",
		"   ╰──────────────────────────────────────╯",
		"",
	}

	for _, line := range logo {
		a.chatView.AddMessage(line, "system")
	}

	// Tips
	tips := []string{
		"  Quick tips:",
		"  • Type your question or task to get started",
		"  • Use @ to search and attach files",
		"  • Use ! to run shell commands",
		"  • Use $ to view environment variables",
		"  • Use /help to see available commands",
		"  • Use Ctrl+F to search conversation",
		"  • Use Alt+End to jump to latest",
		"  • Press Ctrl+C twice to quit",
		"",
	}

	for _, tip := range tips {
		a.chatView.AddMessage(tip, "system")
	}
}

func (a *WidgetApp) style(cs compositor.Style) backend.Style {
	if a == nil || a.styleCache == nil {
		return themeToBackendStyle(cs)
	}
	return a.styleCache.Get(cs)
}

// themeToBackendStyle converts a compositor.Style to backend.Style.
func themeToBackendStyle(cs compositor.Style) backend.Style {
	style := backend.DefaultStyle()

	// Convert foreground
	if cs.FG.Mode == compositor.ColorModeRGB {
		r := uint8((cs.FG.Value >> 16) & 0xFF)
		g := uint8((cs.FG.Value >> 8) & 0xFF)
		b := uint8(cs.FG.Value & 0xFF)
		style = style.Foreground(backend.ColorRGB(r, g, b))
	} else if cs.FG.Mode != compositor.ColorModeDefault {
		style = style.Foreground(backend.Color(cs.FG.Value & 0xFF))
	}

	// Convert background
	if cs.BG.Mode == compositor.ColorModeRGB {
		r := uint8((cs.BG.Value >> 16) & 0xFF)
		g := uint8((cs.BG.Value >> 8) & 0xFF)
		b := uint8(cs.BG.Value & 0xFF)
		style = style.Background(backend.ColorRGB(r, g, b))
	} else if cs.BG.Mode != compositor.ColorModeDefault {
		style = style.Background(backend.Color(cs.BG.Value & 0xFF))
	}

	// Attributes
	if cs.Bold {
		style = style.Bold(true)
	}
	if cs.Italic {
		style = style.Italic(true)
	}
	if cs.Underline {
		style = style.Underline(true)
	}
	if cs.Dim {
		style = style.Dim(true)
	}

	return style
}

func blendColor(a, b backend.Color, t float64) backend.Color {
	if t <= 0 {
		return a
	}
	if t >= 1 {
		return b
	}
	if !a.IsRGB() || !b.IsRGB() {
		if t < 0.5 {
			return a
		}
		return b
	}

	ar, ag, ab := a.RGB()
	br, bg, bb := b.RGB()

	r := uint8(float64(ar) + (float64(br)-float64(ar))*t + 0.5)
	g := uint8(float64(ag) + (float64(bg)-float64(ag))*t + 0.5)
	bv := uint8(float64(ab) + (float64(bb)-float64(ab))*t + 0.5)
	return backend.ColorRGB(r, g, bv)
}

func copyToClipboard(text string) error {
	cmds := [][]string{
		{"pbcopy"},                           // macOS
		{"xclip", "-selection", "clipboard"}, // Linux X11
		{"xsel", "--clipboard", "--input"},   // Linux X11 alt
		{"wl-copy"},                          // Linux Wayland
		{"clip.exe"},                         // WSL/Windows
	}

	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}

	return fmt.Errorf("no clipboard command available")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxInputHeight(screenHeight int) int {
	maxInput := screenHeight - 2 - minChatHeight
	if maxInput < minInputHeight {
		return minInputHeight
	}
	return maxInput
}

// debugDumpScreen dumps the current screen state to a file for debugging.
func (a *WidgetApp) debugDumpScreen() {
	w, h := a.screen.Size()
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	filename := fmt.Sprintf("buckley_debug_%s.txt", timestamp)

	var sb strings.Builder
	sb.WriteString("=== Buckley Debug Dump ===\n")
	sb.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("Screen Size: %d x %d\n", w, h))
	sb.WriteString(fmt.Sprintf("Sidebar Visible: %v\n", a.sidebarVisible))
	sb.WriteString(fmt.Sprintf("Sidebar User Override: %v\n", a.sidebarUserOverride))
	sb.WriteString(fmt.Sprintf("Status: %s\n", a.statusText))
	sb.WriteString(fmt.Sprintf("Scroll Position: %s\n", a.scrollIndicator))
	if a.contextWindow > 0 {
		sb.WriteString(fmt.Sprintf("Context Usage: %d/%d (window %d)\n", a.contextUsed, a.contextBudget, a.contextWindow))
	}
	sb.WriteString("\n")

	// Dump render metrics
	sb.WriteString("=== Render Metrics ===\n")
	a.renderMu.Lock()
	sb.WriteString(fmt.Sprintf("Frame Count: %d\n", a.metrics.FrameCount))
	sb.WriteString(fmt.Sprintf("Dropped Frames: %d\n", a.metrics.DroppedFrames))
	sb.WriteString(fmt.Sprintf("Last Frame Time: %v\n", a.metrics.LastFrameTime))
	a.renderMu.Unlock()
	sb.WriteString("\n")

	// Dump chat view content (last 50 lines)
	sb.WriteString("=== Chat View Content (last 50 lines) ===\n")
	if a.chatView != nil {
		lines := a.chatView.GetContent(50)
		for i, line := range lines {
			sb.WriteString(fmt.Sprintf("%4d: %s\n", i+1, line))
		}
	}
	sb.WriteString("\n")

	// Dump sidebar state
	sb.WriteString("=== Sidebar State ===\n")
	if a.sidebar != nil {
		sb.WriteString(fmt.Sprintf("Has Content: %v\n", a.sidebar.HasContent()))
		sb.WriteString(fmt.Sprintf("Width: %d\n", a.sidebar.Width()))
	}
	sb.WriteString("\n")

	// Dump input area
	sb.WriteString("=== Input Area ===\n")
	if a.inputArea != nil {
		sb.WriteString(fmt.Sprintf("Has Text: %v\n", a.inputArea.HasText()))
		sb.WriteString(fmt.Sprintf("Text: %q\n", a.inputArea.Text()))
	}
	sb.WriteString("\n")

	// Dump backend diagnostics
	if a.diagnostics != nil {
		sb.WriteString(a.diagnostics.Dump())
	} else {
		sb.WriteString("=== Backend Diagnostics ===\n")
		sb.WriteString("(not available - diagnostics collector not configured)\n\n")
	}

	sb.WriteString("=== End Debug Dump ===\n")

	// Write to file
	err := os.WriteFile(filename, []byte(sb.String()), 0644)
	if err != nil {
		a.setStatusOverride(fmt.Sprintf("Debug dump failed: %v", err), 3*time.Second)
		return
	}

	a.setStatusOverride(fmt.Sprintf("Debug dump saved to %s", filename), 3*time.Second)
}
