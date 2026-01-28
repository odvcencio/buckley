// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/odvcencio/buckley/pkg/buckley/ui/filepicker"
	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/buckley/pkg/diagnostics"
	"github.com/odvcencio/fluffy-ui/accessibility"
	"github.com/odvcencio/fluffy-ui/agent"
	"github.com/odvcencio/fluffy-ui/animation"
	"github.com/odvcencio/fluffy-ui/audio"
	"github.com/odvcencio/fluffy-ui/backend"
	"github.com/odvcencio/fluffy-ui/backend/tcell"
	"github.com/odvcencio/fluffy-ui/clipboard"
	"github.com/odvcencio/fluffy-ui/keybind"
	"github.com/odvcencio/fluffy-ui/markdown"
	"github.com/odvcencio/fluffy-ui/runtime"
	"github.com/odvcencio/fluffy-ui/state"
	"github.com/odvcencio/fluffy-ui/terminal"
	"github.com/odvcencio/fluffy-ui/theme"
	"github.com/odvcencio/fluffy-ui/toast"
	"github.com/odvcencio/fluffy-ui/widgets"
)

const (
	defaultMessageQueueSize    = 1024
	defaultMessageQueueTimeout = 100 * time.Millisecond
	defaultFrameInterval       = 16 * time.Millisecond
)

// ============================================================================
// FILE: app_core.go
// PURPOSE: WidgetApp struct definition, constructor (NewWidgetApp), and main Run loop
// FUNCTIONS:
//   - NewWidgetApp
//   - Run
//   - pollEvents
//   - update
//   - Quit
// ============================================================================

// WidgetApp is the main TUI application using the widget tree architecture.
type WidgetApp struct {
	screen  *runtime.Screen
	backend backend.Backend
	running bool

	// Widget tree
	header             *buckleywidgets.Header
	chatView           *buckleywidgets.ChatView
	inputArea          *buckleywidgets.InputArea
	statusBar          *buckleywidgets.StatusBar
	sidebar            *buckleywidgets.Sidebar
	presence           *buckleywidgets.PresenceStrip
	toastStack         *widgets.ToastStack
	alertBanner        *buckleywidgets.AlertBanner
	alertWidget        *widgets.Alert
	contextMenu        *widgets.Menu
	contextMenuOverlay *buckleywidgets.PositionedOverlay
	contextMenuPanel   *widgets.Panel
	root               runtime.Widget
	mainArea           *runtime.Flex // HBox containing chatView + sidebar

	// Sidebar state
	sidebarVisible      bool
	sidebarUserOverride bool // User manually toggled, don't auto-hide
	sidebarAutoHidden   bool
	presenceVisible     bool
	headerVisible       bool
	statusVisible       bool
	focusMode           bool

	// File picker
	filePicker     *filepicker.FilePicker
	commandPalette *widgets.EnhancedPalette

	// Keybindings
	keyRegistry *keybind.CommandRegistry
	keymaps     *keybind.KeymapStack
	keyRouter   *keybind.KeyRouter
	clipboard   clipboard.Clipboard

	// Message loop
	messages            chan Message
	messageQueueSize    int
	messageQueueTimeout time.Duration
	quitCh              chan struct{}
	quitOnce            sync.Once
	inUpdate            int32
	coalescer           *Coalescer
	reasoningBuffer     strings.Builder
	reasoningLastFlush  time.Time
	reasoningMu         sync.Mutex

	// Frame timing
	frameTicker   *time.Ticker
	frameInterval time.Duration
	lastRender    time.Time
	dirty         bool

	// Animation framework
	animator           *animation.Animator
	cursorPulseSpring  *animation.Spring
	contextMeterSpring *animation.Spring

	// Services
	serviceApp     *runtime.App
	stateQueue     *state.Queue
	stateScheduler state.Scheduler
	audioService   audio.Service

	// Render metrics
	metrics     RenderMetrics
	styleCache  *StyleCache
	debugRender bool

	// Ctrl+C handling
	ctrlCArmedUntil     time.Time
	statusText          string
	statusOverride      string
	statusOverrideUntil time.Time
	alertUntil          time.Time
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
	sidebarAnimFrame    int
	sidebarAnimLast     time.Time
	sidebarAnimInterval time.Duration

	// Soft cursor animation
	cursorPulseStart    time.Time
	cursorPulsePeriod   time.Duration
	cursorPulseInterval time.Duration
	cursorPulseLast     time.Time
	cursorStyle         backend.Style
	cursorBGLow         backend.Color
	cursorBGHigh        backend.Color
	cursorFG            backend.Color

	// Presence strip state
	runningToolCount  int
	planTotal         int
	planCompleted     int
	presenceAlert     bool
	currentTaskActive bool
	reduceMotion      bool
	highContrast      bool
	useTextLabels     bool
	messageMetadata   string

	// Accessibility
	announcer  accessibility.Announcer
	focusStyle *accessibility.FocusStyle

	// Callbacks
	onSubmit       func(text string)
	onQuit         func()
	onFileSelect   func(path string)
	onShellCmd     func(cmd string) string
	onNextSession  func()
	onPrevSession  func()
	onApproval     func(requestID string, approved, alwaysAllow bool)
	onToastDismiss func(string)

	// Configuration
	theme       *theme.Theme
	workDir     string
	projectRoot string
	webBaseURL  string
	sessionID   string

	// Render synchronization
	renderMu sync.Mutex

	// Backend diagnostics
	diagnostics *diagnostics.Collector

	// Recording
	recorder   runtime.Recorder
	recordPath string

	// Agent API for external debugging/testing
	agent       *agent.Agent
	agentServer *agent.Server
	agentCtx    context.Context
	agentCancel context.CancelFunc
	agentDone   chan struct{}
}

// WidgetAppConfig configures the widget-based TUI application.
type WidgetAppConfig struct {
	Theme               *theme.Theme
	ModelName           string
	SessionID           string
	WorkDir             string
	ProjectRoot         string
	ReduceMotion        bool
	HighContrast        bool
	UseTextLabels       bool
	MessageMetadata     string
	WebBaseURL          string
	MessageQueueSize    int
	MessageQueueTimeout time.Duration
	FrameInterval       time.Duration
	Audio               AudioConfig
	OnSubmit            func(text string)
	OnQuit              func()
	Backend             backend.Backend // Optional: for testing
	AgentSocket         string          // Optional: unix:/path or tcp:host:port for agent API
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
	messageQueueSize := cfg.MessageQueueSize
	if messageQueueSize <= 0 {
		messageQueueSize = defaultMessageQueueSize
	}
	messageQueueTimeout := cfg.MessageQueueTimeout
	switch {
	case messageQueueTimeout == 0:
		messageQueueTimeout = defaultMessageQueueTimeout
	case messageQueueTimeout < 0:
		messageQueueTimeout = 0
	}
	frameInterval := cfg.FrameInterval
	if frameInterval <= 0 {
		frameInterval = defaultFrameInterval
	}
	sysClipboard := newSystemClipboard()
	appClipboard := clipboard.Clipboard(sysClipboard)
	if sysClipboard == nil || !sysClipboard.Available() {
		appClipboard = &clipboard.MemoryClipboard{}
	}
	focusStyle := &accessibility.FocusStyle{
		Indicator:    ">",
		Style:        styleCache.Get(th.Accent).Bold(true),
		HighContrast: styleCache.Get(th.TextPrimary).Reverse(true),
	}
	announcer := &accessibility.SimpleAnnouncer{}
	audioService := buildAudioService(cfg.Audio)
	animator := animation.NewAnimator()
	stateQueue := state.NewQueue()

	// Create widgets
	header := buckleywidgets.NewHeader()
	header.SetModelName(cfg.ModelName)
	header.SetSessionID(cfg.SessionID)
	header.SetStyles(
		styleCache.Get(th.Surface),
		styleCache.Get(th.Logo),
		styleCache.Get(th.TextSecondary),
	)

	chatView := buckleywidgets.NewChatView()
	chatView.SetStyles(
		styleCache.Get(th.User),
		styleCache.Get(th.Assistant),
		styleCache.Get(th.System),
		styleCache.Get(th.Tool),
		styleCache.Get(th.Thinking),
	)
	chatView.SetModelName(cfg.ModelName)
	chatView.SetMetadataStyle(styleCache.Get(th.TextMuted))
	chatView.SetMessageMetadataMode(cfg.MessageMetadata)
	chatView.SetUIStyles(
		styleCache.Get(th.Scrollbar),
		styleCache.Get(th.ScrollThumb),
		styleCache.Get(th.Selection),
		styleCache.Get(th.SearchMatch),
		styleCache.Get(th.Background),
	)
	mdRenderer := markdown.NewRenderer(th)
	chatView.SetMarkdownRenderer(mdRenderer, styleCache.Get(mdRenderer.CodeBlockBackground()))

	inputArea := buckleywidgets.NewInputArea()
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

	statusBar := buckleywidgets.NewStatusBar()
	statusBar.SetStatus("Ready")
	statusBar.SetStyles(
		styleCache.Get(th.Surface),
		styleCache.Get(th.TextMuted),
	)

	toastStack := widgets.NewToastStack()
	toastStack.SetAnimationsEnabled(!cfg.ReduceMotion)
	toastStack.SetStyles(
		styleCache.Get(th.SurfaceRaised),
		styleCache.Get(th.TextPrimary),
		styleCache.Get(th.Info),
		styleCache.Get(th.Success),
		styleCache.Get(th.Warning),
		styleCache.Get(th.Error),
	)

	alertWidget := widgets.NewAlert("", widgets.AlertInfo)
	alertWidget.SetStyle(styleCache.Get(th.TextPrimary))
	alertBanner := buckleywidgets.NewAlertBanner(alertWidget)
	alertBanner.SetBorderStyle(styleCache.Get(th.Border))

	// Create sidebar
	sidebar := buckleywidgets.NewSidebar()
	sidebar.SetStyles(
		styleCache.Get(th.Border),
		styleCache.Get(th.TextSecondary),
		styleCache.Get(th.TextPrimary),
		styleCache.Get(th.Accent),
		styleCache.Get(th.TextMuted),
		styleCache.Get(th.Surface),
	)
	sidebar.SetProgressEdgeStyle(styleCache.Get(th.AccentGlow))
	sidebar.SetStatusStyles(
		styleCache.Get(th.Success),
		styleCache.Get(th.ElectricBlue).Bold(true),
		styleCache.Get(th.TextMuted),
		styleCache.Get(th.Coral),
	)
	sidebar.SetContextStyles(
		styleCache.Get(th.Teal),
		styleCache.Get(th.Accent),
		styleCache.Get(th.Coral),
		styleCache.Get(th.TextMuted),
	)
	sidebar.SetSpinnerStyle(styleCache.Get(th.ElectricBlue))
	if projectRoot != "" {
		sidebar.SetProjectPath(projectRoot)
	} else {
		sidebar.SetProjectPath(workDir)
	}

	presence := buckleywidgets.NewPresenceStrip()
	presence.SetStyles(
		styleCache.Get(th.Border),
		styleCache.Get(th.TextMuted),
		styleCache.Get(th.ElectricBlue),
		styleCache.Get(th.Coral),
		styleCache.Get(th.Accent),
		styleCache.Get(th.Background),
	)

	layout := layoutForScreen(w, h, sidebar.HasContent(), false)
	if layout.sidebarWidth > 0 {
		sidebar.SetWidth(layout.sidebarWidth)
	}
	sidebarVisible := layout.sidebarVisible
	presenceVisible := layout.presenceVisible
	headerVisible := layout.showHeader
	statusVisible := layout.showStatus

	// Build main content area with HBox (ChatView + Sidebar)
	var mainArea *runtime.Flex
	if sidebarVisible {
		mainArea = runtime.HBox(
			runtime.Flexible(chatView, 3),           // 75% for chat
			runtime.Sized(sidebar, sidebar.Width()), // Dynamic sidebar width
		)
	} else if presenceVisible {
		mainArea = runtime.HBox(
			runtime.Expanded(chatView),
			runtime.Sized(presence, 2),
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
	children := make([]runtime.FlexChild, 0, 4)
	if headerVisible {
		children = append(children, runtime.Fixed(header))
	}
	children = append(children, runtime.Expanded(mainArea))
	children = append(children, runtime.Fixed(inputArea))
	if statusVisible {
		children = append(children, runtime.Fixed(statusBar))
	}
	root := runtime.VBox(children...)

	// Create screen
	screen := runtime.NewScreen(w, h)
	screen.SetAutoRegisterFocus(true)

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
		presence:            presence,
		toastStack:          toastStack,
		alertBanner:         alertBanner,
		alertWidget:         alertWidget,
		root:                root,
		mainArea:            mainArea,
		sidebarVisible:      sidebarVisible,
		presenceVisible:     presenceVisible,
		sidebarAutoHidden:   presenceVisible && !sidebarVisible,
		headerVisible:       headerVisible,
		statusVisible:       statusVisible,
		filePicker:          fp,
		theme:               th,
		workDir:             workDir,
		projectRoot:         projectRoot,
		webBaseURL:          normalizeWebBaseURL(cfg.WebBaseURL),
		sessionID:           strings.TrimSpace(cfg.SessionID),
		clipboard:           appClipboard,
		onSubmit:            cfg.OnSubmit,
		onQuit:              cfg.OnQuit,
		messages:            make(chan Message, messageQueueSize),
		messageQueueSize:    messageQueueSize,
		messageQueueTimeout: messageQueueTimeout,
		frameInterval:       frameInterval,
		quitCh:              make(chan struct{}),
		statusText:          "Ready",
		cursorPulseStart:    time.Now(),
		cursorPulsePeriod:   2600 * time.Millisecond,
		cursorPulseInterval: 50 * time.Millisecond,
		streamAnimInterval:  120 * time.Millisecond,
		sidebarAnimInterval: 120 * time.Millisecond,
		inputMeasuredHeight: inputArea.Measure(runtime.Constraints{MaxWidth: w, MaxHeight: h}).Height,
		styleCache:          styleCache,
		debugRender:         debugRender,
		reduceMotion:        cfg.ReduceMotion,
		highContrast:        cfg.HighContrast,
		useTextLabels:       cfg.UseTextLabels,
		messageMetadata:     cfg.MessageMetadata,
		announcer:           announcer,
		focusStyle:          focusStyle,
		animator:            animator,
		audioService:        audioService,
		stateQueue:          stateQueue,
	}

	app.stateScheduler = state.SchedulerFunc(func(fn func()) {
		if fn == nil {
			return
		}
		app.stateQueue.Schedule(fn)
		app.Post(RefreshMsg{})
	})
	app.serviceApp = runtime.NewApp(runtime.AppConfig{
		MessageBuffer: 1,
		Clipboard:     appClipboard,
		Announcer:     announcer,
		Audio:         audioService,
		Animator:      animator,
		ReducedMotion: cfg.ReduceMotion,
		FocusStyle:    focusStyle,
	})
	screen.SetServices(app.serviceApp.Services())

	// Add root layer (focus scope is created internally)
	screen.PushLayer(root, false)
	screen.PushLayer(alertBanner, false)
	screen.PushLayer(toastStack, false)

	recordPath := strings.TrimSpace(os.Getenv("BUCKLEY_TUI_RECORD"))
	if recordPath == "" {
		recordPath = resolveSessionRecordingPath(os.Getenv("BUCKLEY_RECORD_SESSIONS"), workDir, app.sessionID)
	}
	if recordPath != "" {
		recorder, err := buildRecorder(recordPath, app.sessionID)
		if err != nil {
			log.Printf("tui recording init failed: %v", err)
		} else {
			app.recorder = recorder
			app.recordPath = recordPath
		}
	}

	app.initKeybindings()
	app.initFocus()

	// Initialize animation framework
	app.initAnimations()

	// Create coalescer for smooth streaming
	app.coalescer = NewCoalescer(DefaultCoalescerConfig(), app.Post)
	app.initSoftCursor()
	app.applyScrollStatus(chatView.ScrollPosition())
	app.updatePresenceStrip()

	chatView.OnScrollChange(func(top, total, viewHeight int) {
		if app.applyScrollStatus(top, total, viewHeight) {
			app.dirty = true
		}
	})

	// Set up input callbacks
	inputArea.OnSubmit(func(text string, mode buckleywidgets.InputMode) {
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

	// Initialize agent API server if socket path provided
	if cfg.AgentSocket != "" {
		app.initAgentServer(cfg.AgentSocket)
	}

	return app, nil
}

// Run starts the TUI event loop.
func (a *WidgetApp) Run() error {
	if a == nil {
		return nil
	}
	defer a.backend.Fini()

	a.running = true
	a.dirty = true

	if a.recorder != nil {
		w, h := a.screen.Size()
		if err := a.recorder.Start(w, h, time.Now()); err != nil {
			log.Printf("tui recording start failed: %v", err)
			a.recorder = nil
		} else if a.recordPath != "" {
			a.setStatusOverride("Recording enabled", 3*time.Second)
		}
	}
	defer func() {
		if a.recorder != nil {
			_ = a.recorder.Close()
		}
	}()

	// Frame ticker for render cadence
	a.frameTicker = time.NewTicker(a.frameInterval)
	defer a.frameTicker.Stop()

	// Start terminal event poller in background
	go a.pollEvents()

	// Main event loop
	for a.running {
		select {
		case msg := <-a.messages:
			atomic.StoreInt32(&a.inUpdate, 1)
			updated := a.update(msg)
			atomic.StoreInt32(&a.inUpdate, 0)
			if updated {
				a.dirty = true
			}

		case now := <-a.frameTicker.C:
			a.coalescer.Tick()
			// Flush reasoning if it's been waiting (16ms+ since last content)
			a.reasoningMu.Lock()
			hasReasoning := a.reasoningBuffer.Len() > 0 && now.Sub(a.reasoningLastFlush) >= 16*time.Millisecond
			a.reasoningMu.Unlock()
			if hasReasoning {
				a.flushReasoningBuffer()
			}
			if a.updateAnimations(now) {
				a.dirty = true
			}
			a.updateAlert(now)
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
				Alt:    e.Alt,
				Ctrl:   e.Ctrl,
				Shift:  e.Shift,
			})
		case terminal.PasteEvent:
			a.Post(PasteMsg{Text: e.Text})
		}
	}
}

// update processes a message and returns true if a render is needed.
func (a *WidgetApp) update(msg Message) bool {
	if a.flushStateQueue() {
		a.dirty = true
	}
	switch m := msg.(type) {
	case KeyMsg:
		return a.handleKeyMsg(m)

	case ResizeMsg:
		a.applyInputHeightLimit(m.Height)
		a.inputMeasuredHeight = a.inputArea.Measure(runtime.Constraints{MaxWidth: m.Width, MaxHeight: m.Height}).Height
		a.screen.Resize(m.Width, m.Height)
		if a.recorder != nil {
			if err := a.recorder.Resize(m.Width, m.Height); err != nil {
				log.Printf("tui recording resize failed: %v", err)
				a.recorder = nil
			}
		}
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
		switch m.Source {
		case "assistant":
			a.playSFX(audioCueMessage)
			a.Announce("AI response complete", accessibility.PriorityPolite)
		case "tool":
			a.playSFX(audioCueToolComplete)
		case "system":
			if isErrorMessage(m.Content) {
				a.playSFX(audioCueError)
				a.Announce("Error: "+truncateAnnouncement(m.Content), accessibility.PriorityAssertive)
				a.showAlert(m.Content, widgets.AlertError, 5*time.Second)
			}
		}
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
	case StatusOverrideMsg:
		a.setStatusOverride(m.Text, m.Duration)
		return true

	case TokensMsg:
		a.statusBar.SetTokens(m.Tokens, m.CostCent)
		return true

	case ContextMsg:
		a.contextUsed = m.Used
		a.contextBudget = m.Budget
		a.contextWindow = m.Window
		a.statusBar.SetContextUsage(m.Used, m.Budget, m.Window)
		a.sidebar.SetContextUsage(m.Used, m.Budget, m.Window)
		a.updatePresenceStrip()
		a.updateSidebarVisibility()
		return true
	case ExecutionModeMsg:
		a.executionMode = m.Mode
		a.statusBar.SetExecutionMode(m.Mode)
		return true

	case ProgressMsg:
		a.statusBar.SetProgress(m.Items)
		return true

	case ToastsMsg:
		wasAlert := a.presenceAlert
		if a.toastStack != nil {
			a.toastStack.SetToasts(m.Toasts)
		}
		alert := false
		var alertText string
		var alertVariant widgets.AlertVariant
		for _, t := range m.Toasts {
			if t == nil {
				continue
			}
			if t.Level == toast.ToastWarning || t.Level == toast.ToastError {
				alert = true
				if alertText == "" {
					alertVariant = widgets.AlertWarning
					if t.Level == toast.ToastError {
						alertVariant = widgets.AlertError
					}
					if strings.TrimSpace(t.Title) != "" {
						alertText = t.Title
					} else {
						alertText = t.Message
					}
				}
				break
			}
		}
		a.presenceAlert = alert
		if alert && !wasAlert {
			a.playSFX(audioCueError)
		}
		if alert && alertText != "" {
			a.showAlert(alertText, alertVariant, 4*time.Second)
		}
		a.updatePresenceStrip()
		return true

	case AudioSFXMsg:
		a.playSFX(m.Cue)
		return false

	case SidebarStateMsg:
		a.applySidebarSnapshot(m.Snapshot)
		return true

	case StreamingMsg:
		if m.Active && !a.streaming {
			a.playSFX(audioCueStreamStart)
		}
		a.streaming = m.Active
		a.statusBar.SetStreaming(m.Active)
		if a.reduceMotion && m.Active {
			a.streamAnim = 0
			a.statusBar.SetStreamAnim(0)
			a.streamAnimLast = time.Time{}
		}
		if !m.Active {
			a.streamAnim = 0
			a.statusBar.SetStreamAnim(0)
			a.streamAnimLast = time.Time{}
		}
		a.updatePresenceStrip()
		return true

	case ModelMsg:
		a.header.SetModelName(m.Name)
		a.chatView.SetModelName(m.Name)
		return true

	case SessionMsg:
		a.sessionID = strings.TrimSpace(m.ID)
		a.header.SetSessionID(a.sessionID)
		return true

	case ThinkingMsg:
		if m.Show {
			a.chatView.AddMessage("", "thinking")
		} else {
			a.chatView.RemoveThinkingIndicator()
		}
		a.updateScrollStatus()
		return true

	case ReasoningMsg:
		// Legacy: direct reasoning (bypassed by coalesced path)
		a.chatView.AppendReasoning(m.Text)
		a.statusBar.SetStatus("Thinking...")
		return true

	case ReasoningFlush:
		// Coalesced reasoning batch
		a.chatView.AppendReasoning(m.Text)
		a.statusBar.SetStatus("Thinking...")
		return true

	case ReasoningEndMsg:
		// Flush any remaining reasoning before collapsing
		a.flushReasoningBuffer()
		a.chatView.CollapseReasoning(m.Preview, m.Full)
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

// Quit stops the application.
func (a *WidgetApp) Quit() {
	if a == nil {
		return
	}
	a.running = false
	a.quitOnce.Do(func() {
		if a.quitCh != nil {
			close(a.quitCh)
		}
	})
	a.stopAgentServer()
	if a.onQuit != nil {
		a.onQuit()
	}
}

// Post sends a message to the event loop.
// Safe to call from any goroutine.
func (a *WidgetApp) Post(msg Message) {
	if a == nil || a.messages == nil {
		return
	}
	if atomic.LoadInt32(&a.inUpdate) == 1 {
		select {
		case a.messages <- msg:
		default:
			log.Printf("Warning: TUI message queue full during update, dropping message: %T", msg)
		}
		return
	}
	if a.messageQueueTimeout == 0 {
		select {
		case a.messages <- msg:
		case <-a.quitCh:
		}
		return
	}
	timer := time.NewTimer(a.messageQueueTimeout)
	defer timer.Stop()
	select {
	case a.messages <- msg:
	case <-a.quitCh:
	case <-timer.C:
		log.Printf("Warning: TUI message queue full, dropping message: %T", msg)
	}
}
