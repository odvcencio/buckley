// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"fmt"
	"os"
	"os/exec"
	stdruntime "runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"m31labs.dev/buckley/pkg/ui/filepicker"
	"m31labs.dev/buckley/pkg/ui/widgets"
	"m31labs.dev/fluffyui/backend"
	"m31labs.dev/fluffyui/backend/tcell"
	"m31labs.dev/fluffyui/markdown"
	"m31labs.dev/fluffyui/runtime"
	"m31labs.dev/fluffyui/terminal"
	"m31labs.dev/fluffyui/theme"
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
	minInputHeight    = 2
	minChatHeight     = 4
	processStatusTick = 250 * time.Millisecond
)

var processSpinnerFrames = [...]string{"-", "\\", "|", "/"}

// expandFromZero avoids treating a fill widget's unconstrained measurement as
// its flex basis. Nested flex layouts otherwise measure the chat view at the
// maximum integer size and collapse it when distributing the real viewport.
func expandFromZero(widget runtime.Widget) runtime.FlexChild {
	return runtime.FlexChild{Widget: widget, Grow: 1, Shrink: 1, Basis: 0}
}

// WidgetApp is the main TUI application using the widget tree architecture.
type WidgetApp struct {
	screen   *runtime.Screen
	backend  backend.Backend
	runState atomic.Uint32
	finiOnce sync.Once
	quitOnce sync.Once

	// Widget tree
	header        *widgets.Header
	chatView      *widgets.ChatView
	inputArea     *widgets.InputArea
	statusBar     *widgets.StatusBar
	sidebar       *widgets.Sidebar
	activityPanel *widgets.ActivityPanel
	root          runtime.Widget
	mainArea      *runtime.Flex // HBox containing navigator + chat + inspector

	// Workspace panel state
	sidebarVisible       bool
	sidebarWanted        bool
	sidebarWidth         int
	minWidthForSidebar   int // Auto-hide below this width
	activityPanelVisible bool
	activityPanelWanted  bool
	activityPanelTouched bool
	activityPanelWidth   int

	// File picker
	filePicker *filepicker.FilePicker

	// Message loop
	messages              chan Message
	delivery              eventDeliveryQueue
	coalescer             *Coalescer
	streamMu              sync.Mutex
	streamGenerations     map[string]uint64
	streamSequence        atomic.Uint64
	streamGenerationBytes int
	approvalLayerMu       sync.Mutex
	approvalLayers        map[string]*widgets.ApprovalWidget

	// Frame timing
	frameTicker *time.Ticker
	lastRender  time.Time
	dirty       bool

	// Render metrics
	metrics RenderMetrics

	// Ctrl+C handling
	ctrlCArmedUntil     time.Time
	statusText          string
	statusOverride      string
	statusOverrideUntil time.Time
	processActive       bool
	processText         string
	processStarted      time.Time
	processLastTick     time.Time
	processFrame        int
	unreadCount         int
	scrollIndicator     string
	inputMeasuredHeight int
	selectionActive     bool
	selectionLastLine   int
	selectionLastCol    int
	selectionLastValid  bool

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
	onSubmit            func(text string)
	onQuit              func()
	onFileSelect        func(path string)
	onShellCmd          func(cmd string) string
	onNextSession       func()
	onPrevSession       func()
	onCycleVariant      func()
	onCycleRecentModel  func()
	onApproval          func(requestID string, approved, alwaysAllow bool)
	onInterrupt         func()
	modelPickerMu       sync.Mutex
	modelPickerSequence atomic.Uint64
	modelPickerToken    uint64
	modelPickerAction   ModelPickerAction
	modelPickerLayer    *modelPickerOverlay
	modelPickerPending  *ModelPickerMsg
	onModelPickerAction func(action ModelPickerAction, id string, data any)

	// Configuration
	theme       *theme.Theme
	workDir     string
	projectRoot string

	// Render synchronization
	renderMu sync.Mutex
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
		th = defaultBuckleyTheme()
	}

	// Create widgets
	header := widgets.NewHeader()
	header.SetModelName(cfg.ModelName)
	header.SetStyles(
		themeToBackendStyle(th.Surface),
		themeToBackendStyle(th.Logo),
		themeToBackendStyle(th.TextSecondary),
	)

	chatView := widgets.NewChatView()
	chatView.SetStyles(
		themeToBackendStyle(th.User),
		themeToBackendStyle(th.Assistant),
		themeToBackendStyle(th.System),
		themeToBackendStyle(th.Tool),
		themeToBackendStyle(th.Thinking),
	)
	chatView.SetUIStyles(
		themeToBackendStyle(th.Scrollbar),
		themeToBackendStyle(th.ScrollThumb),
		themeToBackendStyle(th.Selection),
		themeToBackendStyle(th.SearchMatch),
	)
	mdRenderer := markdown.NewRenderer(th)
	chatView.SetMarkdownRenderer(mdRenderer, themeToBackendStyle(mdRenderer.CodeBlockBackground()))
	chatView.SetCodeSyntaxStyles(
		themeToBackendStyle(th.TextPrimary),
		themeToBackendStyle(th.TextMuted),
		themeToBackendStyle(th.Accent),
		themeToBackendStyle(th.AccentDim),
		themeToBackendStyle(th.Success),
		themeToBackendStyle(th.Warning),
		themeToBackendStyle(th.Info),
		themeToBackendStyle(th.Error),
	)

	inputArea := widgets.NewInputArea()
	inputArea.SetStyles(
		themeToBackendStyle(th.SurfaceRaised),
		themeToBackendStyle(th.TextPrimary),
		themeToBackendStyle(th.Border),
	)
	inputArea.SetModeStyles(
		themeToBackendStyle(th.ModeNormal),
		themeToBackendStyle(th.ModeShell),
		themeToBackendStyle(th.ModeEnv),
		themeToBackendStyle(th.ModeSearch),
	)
	inputArea.SetHeightLimits(minInputHeight, maxInputHeight(h))

	statusBar := widgets.NewStatusBar()
	statusBar.SetStatus("Ready")
	statusBar.SetStyles(
		themeToBackendStyle(th.Surface),
		themeToBackendStyle(th.TextMuted),
	)

	// Create sidebar
	sidebar := widgets.NewSidebar()
	sidebar.SetStyles(
		themeToBackendStyle(th.Border),
		themeToBackendStyle(th.TextSecondary),
		themeToBackendStyle(th.TextPrimary),
		themeToBackendStyle(th.Accent),
		themeToBackendStyle(th.TextMuted),
	)

	// Create the persistent activity/detail inspector. It starts collapsed
	// and opens automatically when the first inspectable activity arrives.
	activityPanel := widgets.NewActivityPanel()
	activityPanel.SetStyles(
		themeToBackendStyle(th.Border),
		themeToBackendStyle(th.TextSecondary),
		themeToBackendStyle(th.TextPrimary),
		themeToBackendStyle(th.Selection),
		themeToBackendStyle(th.TextMuted),
	)

	mainArea := workspaceMainArea(
		chatView,
		sidebar,
		activityPanel,
		workspaceVisibility{},
		defaultSidebarWidth,
		defaultActivityPanelWidth,
	)

	// Build widget tree using VBox layout
	// Header (fixed 1 row)
	// MainArea (HBox: Navigator + ChatView + Inspector)
	// InputArea (fixed 2+ rows)
	// StatusBar (fixed 1 row)
	root := runtime.VBox(
		runtime.Fixed(header),
		expandFromZero(mainArea),
		runtime.Fixed(inputArea),
		runtime.Fixed(statusBar),
	)

	// Create screen
	screen := runtime.NewScreen(w, h)

	// Add root layer (focus scope is created internally)
	screen.PushLayer(root, false)

	// Focus the input area
	inputArea.Focus()

	// Create file picker
	fp := filepicker.NewFilePicker(projectRoot)

	app := &WidgetApp{
		screen:               screen,
		backend:              be,
		header:               header,
		chatView:             chatView,
		inputArea:            inputArea,
		statusBar:            statusBar,
		sidebar:              sidebar,
		activityPanel:        activityPanel,
		root:                 root,
		mainArea:             mainArea,
		sidebarVisible:       false,
		sidebarWanted:        false,
		sidebarWidth:         defaultSidebarWidth,
		minWidthForSidebar:   100,
		activityPanelVisible: false,
		activityPanelWanted:  false,
		activityPanelWidth:   defaultActivityPanelWidth,
		filePicker:           fp,
		theme:                th,
		workDir:              workDir,
		projectRoot:          projectRoot,
		onSubmit:             cfg.OnSubmit,
		onQuit:               cfg.OnQuit,
		messages:             make(chan Message, 256),
		delivery:             newEventDeliveryQueue(),
		streamGenerations:    make(map[string]uint64),
		approvalLayers:       make(map[string]*widgets.ApprovalWidget),
		statusText:           "Ready",
		cursorPulseStart:     time.Now(),
		cursorPulsePeriod:    2600 * time.Millisecond,
		cursorPulseInterval:  50 * time.Millisecond,
		inputMeasuredHeight:  inputArea.Measure(runtime.Constraints{MaxWidth: w, MaxHeight: h}).Height,
	}

	// Create coalescer for smooth streaming
	app.coalescer = NewCoalescer(DefaultCoalescerConfig(), app.postCoalescerPublication)
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
		themeToBackendStyle(a.theme.Surface),
		themeToBackendStyle(a.theme.Border),
		themeToBackendStyle(a.theme.TextPrimary),
		themeToBackendStyle(a.theme.Accent),
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
		themeToBackendStyle(a.theme.Surface),
		themeToBackendStyle(a.theme.Border),
		themeToBackendStyle(a.theme.TextPrimary),
		themeToBackendStyle(a.theme.Selection),
		themeToBackendStyle(a.theme.Accent),
		themeToBackendStyle(a.theme.TextPrimary),
	)
	pickerWidget.Focus()

	// Push as modal overlay
	a.screen.PushLayer(pickerWidget, true)
	a.dirty = true
}

// showApprovalDialog creates and displays an approval dialog.
func (a *WidgetApp) showApprovalDialog(msg ApprovalRequestMsg) {
	a.approvalLayerMu.Lock()
	defer a.approvalLayerMu.Unlock()
	if a.delivery.closed.Load() || !a.approvalOutstanding(msg.ID) {
		return
	}
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
		themeToBackendStyle(a.theme.Surface),
		themeToBackendStyle(a.theme.Border),
		themeToBackendStyle(a.theme.Accent),
		themeToBackendStyle(a.theme.TextPrimary),
	)
	approvalWidget.Focus()

	// Push as modal overlay
	a.screen.PushLayer(approvalWidget, true)
	a.approvalLayers[msg.ID] = approvalWidget
	a.dirty = true
}

func (a *WidgetApp) forgetApprovalDialog(requestID string) {
	if a == nil || requestID == "" {
		return
	}
	a.approvalLayerMu.Lock()
	delete(a.approvalLayers, requestID)
	a.approvalLayerMu.Unlock()
}

func (a *WidgetApp) removeApprovalDialog(requestID string) bool {
	if a == nil || requestID == "" {
		return false
	}
	a.approvalLayerMu.Lock()
	layer := a.approvalLayers[requestID]
	if layer != nil {
		delete(a.approvalLayers, requestID)
	}
	removed := layer != nil && a.removeExactLayer(layer)
	a.approvalLayerMu.Unlock()
	if removed {
		a.handleModelPickerOverlayPopped()
		a.dirty = true
	}
	return removed
}

// removeExactLayer rotates layer values toward the removed slot, preserving
// every unrelated overlay's root, focus scope, order, and mounted lifecycle.
// PopLayer therefore unmounts only the requested target.
func (a *WidgetApp) removeExactLayer(root runtime.Widget) bool {
	if a == nil || root == nil {
		return false
	}
	target := -1
	top := a.screen.LayerCount() - 1
	for i := 1; i <= top; i++ {
		if layer := a.screen.Layer(i); layer != nil && layer.Root == root {
			target = i
			break
		}
	}
	if target < 0 {
		return false
	}
	if target == top {
		return a.screen.PopLayer()
	}
	removed := *a.screen.Layer(target)
	for i := target; i < top; i++ {
		*a.screen.Layer(i) = *a.screen.Layer(i + 1)
	}
	*a.screen.Layer(top) = removed
	return a.screen.PopLayer()
}

func (a *WidgetApp) closeApprovalLayerState() {
	if a == nil {
		return
	}
	a.approvalLayerMu.Lock()
	clear(a.approvalLayers)
	a.approvalLayerMu.Unlock()
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
		{ID: "export", Category: "Session", Label: "Export Conversation", Shortcut: "/export"},
		{ID: "compact", Category: "Session", Label: "Compact Context", Shortcut: "/compact"},
		{ID: "cancel", Category: "Session", Label: "Cancel Response", Shortcut: "/cancel"},
		{ID: "steer", Category: "Session", Label: "Steer Active Response", Shortcut: "/steer"},
		{ID: "queue", Category: "Session", Label: "Queue Follow-up", Shortcut: "/queue"},

		// Navigation commands
		{ID: "toggle-sidebar", Category: "View", Label: "Toggle Navigator", Shortcut: "Ctrl+B"},
		{ID: "toggle-activity", Category: "View", Label: "Toggle Activity Inspector", Shortcut: "Ctrl+Shift+B"},
		{ID: "navigator-narrower", Category: "View", Label: "Narrow Navigator", Shortcut: "Alt+["},
		{ID: "navigator-wider", Category: "View", Label: "Widen Navigator", Shortcut: "Alt+]"},
		{ID: "inspector-narrower", Category: "View", Label: "Narrow Inspector", Shortcut: "Alt+{"},
		{ID: "inspector-wider", Category: "View", Label: "Widen Inspector", Shortcut: "Alt+}"},
		{ID: "file-picker", Category: "View", Label: "Open File Picker", Shortcut: "@"},
		{ID: "scroll-top", Category: "View", Label: "Scroll to Top"},
		{ID: "scroll-bottom", Category: "View", Label: "Scroll to Bottom", Shortcut: "Alt+End"},
		{ID: "copy-code", Category: "View", Label: "Copy Last Code Block", Shortcut: "Alt+C"},

		// Model commands
		{ID: "models", Category: "Model", Label: "Select Model", Shortcut: "/model"},
		{ID: "usage", Category: "Model", Label: "Show Context Tokens", Shortcut: "/tokens"},

		// Plan commands
		{ID: "plans", Category: "Plan", Label: "List Plans", Shortcut: "/plans"},
		{ID: "status", Category: "Plan", Label: "Session Status", Shortcut: "/status"},

		// System commands
		{ID: "help", Category: "System", Label: "Show Help", Shortcut: "/help"},
		{ID: "config", Category: "System", Label: "View Config", Shortcut: "/config"},
		{ID: "quit", Category: "System", Label: "Quit Buckley", Shortcut: "Ctrl+C×2"},
	}
	palette.SetItems(items)

	palette.SetStyles(
		themeToBackendStyle(a.theme.Surface),
		themeToBackendStyle(a.theme.Border),
		themeToBackendStyle(a.theme.Accent),
		themeToBackendStyle(a.theme.TextPrimary),
		themeToBackendStyle(a.theme.TextPrimary),
		themeToBackendStyle(a.theme.Selection),
		themeToBackendStyle(a.theme.TextSecondary),
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
	palette.SetMaxVisible(14)

	// Slash command items
	items := []widgets.PaletteItem{
		{ID: "/review", Label: "/review", Description: "Review current git diff"},
		{ID: "/commit", Label: "/commit", Description: "Generate commit message"},
		{ID: "/new", Label: "/new", Description: "Start a new session"},
		{ID: "/clear", Label: "/clear", Description: "Clear current session"},
		{ID: "/tokens", Label: "/tokens", Description: "Show context and token budget"},
		{ID: "/compact", Label: "/compact", Description: "Summarize older context"},
		{ID: "/history", Label: "/history", Description: "Show recent turns"},
		{ID: "/export", Label: "/export", Description: "Export conversation to Markdown"},
		{ID: "/cancel", Label: "/cancel", Description: "Cancel current response"},
		{ID: "/steer ", Label: "/steer", Description: "Interrupt and redirect the active response"},
		{ID: "/queue ", Label: "/queue", Description: "Queue a follow-up without interrupting"},
		{ID: "/agents", Label: "/agents", Description: "List subagent runs"},
		{ID: "/agent spawn ", Label: "/agent spawn", Description: "Start a generic or named subagent"},
		{ID: "/agent send ", Label: "/agent send", Description: "Command one, a group, or all subagents"},
		{ID: "/agent steer ", Label: "/agent steer", Description: "Steer one, a group, or all subagents"},
		{ID: "/agent cancel ", Label: "/agent cancel", Description: "Cancel one, a group, or all subagents"},
		{ID: "/sessions", Label: "/sessions", Description: "List saved sessions"},
		{ID: "/resume ", Label: "/resume", Description: "Resume a saved session"},
		{ID: "/next", Label: "/next", Description: "Switch to next session"},
		{ID: "/prev", Label: "/prev", Description: "Switch to previous session"},
		{ID: "/model", Label: "/model", Description: "Select execution model"},
		{ID: "/model curate", Label: "/model curate", Description: "Curate models for ACP/editor pickers"},
		{ID: "/plans", Label: "/plans", Description: "List saved plans"},
		{ID: "/config", Label: "/config", Description: "Show config summary"},
		{ID: "/help", Label: "/help", Description: "Show available commands"},
		{ID: "/quit", Label: "/quit", Description: "Exit Buckley"},
	}
	palette.SetItems(items)

	palette.SetOnSelect(func(item widgets.PaletteItem) {
		if strings.HasSuffix(item.ID, " ") {
			a.prefillInput(item.ID)
			return
		}
		// Execute the slash command
		if a.onSubmit != nil {
			a.onSubmit(item.ID)
		}
	})

	palette.SetStyles(
		themeToBackendStyle(a.theme.Surface),
		themeToBackendStyle(a.theme.Border),
		themeToBackendStyle(a.theme.Accent),
		themeToBackendStyle(a.theme.TextPrimary),
		themeToBackendStyle(a.theme.TextPrimary),
		themeToBackendStyle(a.theme.Selection),
		themeToBackendStyle(a.theme.TextSecondary),
	)
	palette.Focus()

	// Push as modal overlay
	a.screen.PushLayer(palette, true)
	a.dirty = true
}

// ShowModelPicker displays a model picker overlay with a bounded typed action.
func (a *WidgetApp) ShowModelPicker(items []widgets.PaletteItem, action ModelPickerAction) {
	a.Post(ModelPickerMsg{Items: items, Action: action})
}

type modelPickerSelectionData struct {
	token uint64
	data  any
}

type modelPickerPopOverlay struct {
	token uint64
}

func (modelPickerPopOverlay) Command() {}

type modelPickerOverlay struct {
	*widgets.PaletteWidget
	token uint64
}

func (m *modelPickerOverlay) HandleMessage(msg runtime.Message) runtime.HandleResult {
	result := m.PaletteWidget.HandleMessage(msg)
	for i, command := range result.Commands {
		switch typed := command.(type) {
		case runtime.PaletteSelected:
			typed.Data = modelPickerSelectionData{token: m.token, data: typed.Data}
			result.Commands[i] = typed
		case runtime.PopOverlay:
			result.Commands[i] = modelPickerPopOverlay{token: m.token}
		}
	}
	return result
}

func (a *WidgetApp) showModelPicker(items []widgets.PaletteItem, action ModelPickerAction) {
	a.modelPickerMu.Lock()
	defer a.modelPickerMu.Unlock()
	if a.delivery.closed.Load() {
		return
	}
	if a.modelPickerToken != 0 {
		if !a.modelPickerLayerPresentLocked() {
			a.clearActiveModelPickerLocked()
		} else if !a.modelPickerIsTopLayerLocked() {
			pending := ModelPickerMsg{Items: items, Action: action}
			a.modelPickerPending = &pending
			return
		} else if !a.screen.PopLayer() {
			pending := ModelPickerMsg{Items: items, Action: action}
			a.modelPickerPending = &pending
			return
		} else {
			a.clearActiveModelPickerLocked()
		}
	}
	a.modelPickerPending = nil
	a.pushModelPickerLocked(items, action)
}

func (a *WidgetApp) pushModelPickerLocked(items []widgets.PaletteItem, action ModelPickerAction) {
	token := a.modelPickerSequence.Add(1)
	if token == 0 {
		token = a.modelPickerSequence.Add(1)
	}
	palette := widgets.NewPaletteWidget("Models")
	palette.SetPlaceholder("search models...")
	palette.SetMaxVisible(16)
	palette.SetItems(items)
	palette.SetStyles(
		themeToBackendStyle(a.theme.Surface),
		themeToBackendStyle(a.theme.Border),
		themeToBackendStyle(a.theme.Accent),
		themeToBackendStyle(a.theme.TextPrimary),
		themeToBackendStyle(a.theme.TextPrimary),
		themeToBackendStyle(a.theme.Selection),
		themeToBackendStyle(a.theme.TextSecondary),
	)
	palette.Focus()
	overlay := &modelPickerOverlay{PaletteWidget: palette, token: token}

	a.modelPickerToken = token
	a.modelPickerAction = action
	a.modelPickerLayer = overlay
	a.screen.PushLayer(overlay, true)
	a.dirty = true
}

func (a *WidgetApp) modelPickerLayerPresentLocked() bool {
	if a.modelPickerLayer == nil {
		return false
	}
	for i := 1; i < a.screen.LayerCount(); i++ {
		if layer := a.screen.Layer(i); layer != nil && layer.Root == a.modelPickerLayer {
			return true
		}
	}
	return false
}

func (a *WidgetApp) modelPickerIsTopLayerLocked() bool {
	top := a.screen.TopLayer()
	return top != nil && top.Root == a.modelPickerLayer
}

func (a *WidgetApp) clearActiveModelPickerLocked() {
	a.modelPickerToken = 0
	a.modelPickerAction = ModelPickerActionNone
	a.modelPickerLayer = nil
}

func (a *WidgetApp) applyPendingModelPickerLocked() {
	if a.modelPickerPending == nil || a.delivery.closed.Load() {
		return
	}
	if a.modelPickerToken != 0 {
		if !a.modelPickerLayerPresentLocked() {
			a.clearActiveModelPickerLocked()
		} else if !a.modelPickerIsTopLayerLocked() || !a.screen.PopLayer() {
			return
		} else {
			a.clearActiveModelPickerLocked()
		}
	}
	if a.screen.OverlayCount() != 0 {
		return
	}
	pending := *a.modelPickerPending
	a.modelPickerPending = nil
	a.pushModelPickerLocked(pending.Items, pending.Action)
}

func (a *WidgetApp) handleModelPickerOverlayPopped() {
	if a == nil {
		return
	}
	a.modelPickerMu.Lock()
	defer a.modelPickerMu.Unlock()
	if a.modelPickerToken != 0 && !a.modelPickerLayerPresentLocked() {
		a.clearActiveModelPickerLocked()
	}
	a.applyPendingModelPickerLocked()
}

func (a *WidgetApp) handleModelPickerPopOverlay(token uint64) {
	if a == nil {
		return
	}
	a.modelPickerMu.Lock()
	defer a.modelPickerMu.Unlock()
	if token == 0 || token != a.modelPickerToken || !a.modelPickerIsTopLayerLocked() {
		return
	}
	if !a.screen.PopLayer() {
		return
	}
	a.clearActiveModelPickerLocked()
	a.applyPendingModelPickerLocked()
}

func (a *WidgetApp) closeModelPickerState() {
	if a == nil {
		return
	}
	a.modelPickerMu.Lock()
	a.clearActiveModelPickerLocked()
	a.modelPickerPending = nil
	a.onModelPickerAction = nil
	a.modelPickerMu.Unlock()
}

func (a *WidgetApp) consumeModelPickerAction(token uint64) (ModelPickerAction, func(ModelPickerAction, string, any), bool) {
	a.modelPickerMu.Lock()
	defer a.modelPickerMu.Unlock()
	if token == 0 || token != a.modelPickerToken {
		return ModelPickerActionNone, nil, false
	}
	action := a.modelPickerAction
	if action == ModelPickerActionNone {
		return ModelPickerActionNone, nil, false
	}
	callback := a.onModelPickerAction
	a.modelPickerAction = ModelPickerActionNone
	return action, callback, true
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

// Run starts the TUI event loop.
func (a *WidgetApp) Run() error {
	if a.delivery.closed.Load() {
		return fmt.Errorf("tui app is closed")
	}
	if !a.runState.CompareAndSwap(widgetAppNew, widgetAppRunning) {
		return fmt.Errorf("tui app can only be run once")
	}
	// Quit may win immediately after the first closed check. In that case it
	// leaves finalization to the Run owner that acquired widgetAppRunning.
	if a.delivery.closed.Load() || a.runState.Load() != widgetAppRunning {
		a.runState.Store(widgetAppStopped)
		a.finalizeBackend()
		return fmt.Errorf("tui app is closed")
	}
	defer func() {
		a.runState.Store(widgetAppStopped)
		a.closeEventDelivery()
		a.finalizeBackend()
	}()

	a.dirty = true

	// Frame ticker for 60 FPS rendering
	a.frameTicker = time.NewTicker(16 * time.Millisecond)
	defer a.frameTicker.Stop()

	// Start terminal event poller in background
	go a.pollEvents()

	// Main event loop
	for a.isRunning() {
		if a.processReadyFrame() {
			continue
		}

		select {
		case msg := <-a.messages:
			a.processMessage(msg)
			if a.isRunning() {
				a.drainEventWork(eventWorkBudget - 1)
			}

		case <-a.delivery.wake:
			a.drainEventWork(eventWorkBudget)

		case now := <-a.frameTicker.C:
			a.processFrameTick(now)
		}
	}

	return nil
}

// pollEvents reads terminal events and posts them as messages.
func (a *WidgetApp) pollEvents() {
	for !a.delivery.closed.Load() {
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

// handleMouseMsg processes mouse input.
func (a *WidgetApp) handleMouseMsg(m MouseMsg) bool {
	if a.handleChatScrollbarMouse(m) {
		return true
	}
	if a.handleMouseWheel(m) {
		return true
	}
	if a.handleSelectionRelease(m) {
		return true
	}
	if a.handleRightMousePress(m) {
		return true
	}
	if a.handleLeftMousePress(m) {
		return true
	}
	return a.dispatchMouseToScreen(m)
}

func (a *WidgetApp) handleMouseWheel(m MouseMsg) bool {
	if _, _, ok := a.chatView.PositionForPoint(m.X, m.Y); !ok {
		return false
	}
	switch m.Button {
	case MouseWheelUp:
		a.chatView.ScrollUp(3)
		return true
	case MouseWheelDown:
		a.chatView.ScrollDown(3)
		return true
	}
	return false
}

func (a *WidgetApp) handleSelectionRelease(m MouseMsg) bool {
	if m.Action != MouseRelease || !a.selectionActive {
		return false
	}

	line, col, ok := a.chatView.PositionForPoint(m.X, m.Y)
	if ok {
		a.chatView.UpdateSelection(line, col)
		a.rememberSelectionPoint(line, col)
	} else if a.selectionLastValid {
		a.chatView.UpdateSelection(a.selectionLastLine, a.selectionLastCol)
	}

	a.chatView.EndSelection()
	a.selectionActive = false
	a.selectionLastValid = false
	a.copyFinishedSelection()
	a.dirty = true
	return true
}

func (a *WidgetApp) copyFinishedSelection() {
	if !a.chatView.HasSelection() {
		return
	}

	text := a.chatView.SelectionText()
	if text == "" {
		a.chatView.ClearSelection()
		return
	}

	if err := copyToClipboard(text); err != nil {
		a.setStatusOverride("Copy failed: "+err.Error(), 3*time.Second)
		return
	}

	a.setStatusOverride("Selection copied", 2*time.Second)
}

func (a *WidgetApp) handleRightMousePress(m MouseMsg) bool {
	if m.Action != MousePress || m.Button != MouseRight {
		return false
	}

	if _, _, ok := a.chatView.PositionForPoint(m.X, m.Y); !ok {
		return false
	}

	a.chatView.ClearSelection()
	a.selectionActive = false
	a.selectionLastValid = false
	a.setStatusOverride("Selection cleared", 2*time.Second)
	a.dirty = true
	return true
}

func (a *WidgetApp) handleLeftMousePress(m MouseMsg) bool {
	if m.Action != MousePress || m.Button != MouseLeft {
		return false
	}

	line, col, ok := a.chatView.PositionForPoint(m.X, m.Y)
	if !ok {
		return false
	}

	if !a.selectionActive {
		a.chatView.ClearSelection()
		a.chatView.StartSelection(line, col)
		a.selectionActive = true
	} else {
		a.chatView.UpdateSelection(line, col)
	}
	a.rememberSelectionPoint(line, col)
	a.dirty = true
	return true
}

func (a *WidgetApp) rememberSelectionPoint(line, col int) {
	a.selectionLastLine = line
	a.selectionLastCol = col
	a.selectionLastValid = true
}

func (a *WidgetApp) dispatchMouseToScreen(m MouseMsg) bool {
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
	a.updateWorkspaceVisibility()
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
	case widgets.ApprovalResponse:
		a.forgetApprovalDialog(c.RequestID)
		a.resolveApproval(c.RequestID, c.Approved, c.AlwaysAllow)
	case runtime.PaletteSelected:
		if selection, ok := c.Data.(modelPickerSelectionData); ok {
			if action, callback, matches := a.consumeModelPickerAction(selection.token); matches && callback != nil {
				callback(action, c.ID, selection.data)
			}
			return
		}
		a.handlePaletteCommand(c.ID)
	case runtime.PopOverlay:
		a.handleModelPickerOverlayPopped()
	case modelPickerPopOverlay:
		a.handleModelPickerPopOverlay(c.token)
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
		if a.onSubmit != nil {
			a.onSubmit("/new")
		}
	case "clear":
		if a.onSubmit != nil {
			a.onSubmit("/clear")
		}
	case "history":
		// Submit /history command
		if a.onSubmit != nil {
			a.onSubmit("/history")
		}
	case "export":
		if a.onSubmit != nil {
			a.onSubmit("/export")
		}
	case "compact":
		if a.onSubmit != nil {
			a.onSubmit("/compact")
		}
	case "cancel":
		if a.onSubmit != nil {
			a.onSubmit("/cancel")
		}
	case "steer":
		a.prefillInput("/steer ")
	case "queue":
		a.prefillInput("/queue ")

	// View commands
	case "toggle-sidebar":
		a.toggleSidebar()
	case "toggle-activity":
		a.toggleActivityPanel()
	case "navigator-narrower":
		a.resizeSidebar(-panelResizeStep)
	case "navigator-wider":
		a.resizeSidebar(panelResizeStep)
	case "inspector-narrower":
		a.resizeActivityPanel(-panelResizeStep)
	case "inspector-wider":
		a.resizeActivityPanel(panelResizeStep)
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
			a.onSubmit("/tokens")
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

func (a *WidgetApp) prefillInput(text string) {
	a.inputArea.SetText(text)
	a.inputArea.Focus()
	a.refreshInputLayout()
	a.dirty = true
}

// Quit stops the application.
func (a *WidgetApp) Quit() {
	previous := a.runState.Swap(widgetAppStopped)
	a.closeEventDelivery()
	if previous == widgetAppNew {
		a.finalizeBackend()
	}
	a.quitOnce.Do(func() {
		if a.onQuit != nil {
			a.onQuit()
		}
	})
}

func (a *WidgetApp) isRunning() bool {
	return a != nil && a.runState.Load() == widgetAppRunning
}

func (a *WidgetApp) finalizeBackend() {
	if a == nil {
		return
	}
	a.finiOnce.Do(a.backend.Fini)
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

	a.lastRender = time.Now()
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

// addMessageImmediately hydrates transcript content before the next frame.
// Callers must already be on the UI goroutine or before Run starts.
func (a *WidgetApp) addMessageImmediately(content, source string) {
	a.chatView.AddMessage(content, source)
	a.updateScrollStatus()
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

// ReplaceLastMessage replaces the current message content.
func (a *WidgetApp) ReplaceLastMessage(content string) {
	a.Post(ReplaceLastMessageMsg{Content: content})
}

// StreamChunk sends a streaming chunk through the coalescer.
func (a *WidgetApp) StreamChunk(sessionID, text string) {
	if a == nil {
		return
	}
	if a.delivery.closed.Load() {
		a.recordPostAfterClose()
		return
	}
	if len(sessionID) > coalescerBufferMaxBytes {
		a.postEvent(streamOverloadMsg{})
		return
	}
	sessionID = strings.Clone(sessionID)
	a.streamMu.Lock()
	if a.delivery.closed.Load() {
		a.streamMu.Unlock()
		a.recordPostAfterClose()
		return
	}
	generation, ok := a.streamGenerationLocked(sessionID)
	if !ok {
		a.streamMu.Unlock()
		a.postEvent(streamOverloadMsg{})
		return
	}
	a.Post(StreamChunk{SessionID: sessionID, Generation: generation, Text: text})
	a.streamMu.Unlock()
}

// StreamEnd signals the end of a streaming session.
func (a *WidgetApp) StreamEnd(sessionID, fullText string) {
	if a == nil {
		return
	}
	if a.delivery.closed.Load() {
		a.recordPostAfterClose()
		return
	}
	if len(sessionID) > coalescerBufferMaxBytes {
		a.postEvent(streamOverloadMsg{})
		return
	}
	sessionID = strings.Clone(sessionID)
	a.streamMu.Lock()
	if a.delivery.closed.Load() {
		a.streamMu.Unlock()
		a.recordPostAfterClose()
		return
	}
	generation, ok := a.streamGenerationLocked(sessionID)
	if !ok {
		a.streamMu.Unlock()
		a.postEvent(streamOverloadMsg{})
		return
	}
	delete(a.streamGenerations, sessionID)
	a.streamGenerationBytes -= len(sessionID)
	a.Post(StreamDone{SessionID: sessionID, Generation: generation, FullText: fullText})
	a.streamMu.Unlock()
}

func (a *WidgetApp) streamGenerationLocked(sessionID string) (uint64, bool) {
	generation := a.streamGenerations[sessionID]
	if generation == 0 {
		if len(a.streamGenerations) >= coalescerBufferMaxCount || a.streamGenerationBytes+len(sessionID) > coalescerBufferMaxBytes {
			return 0, false
		}
		generation = a.streamSequence.Add(1)
		if generation == 0 {
			generation = a.streamSequence.Add(1)
		}
		a.streamGenerations[sessionID] = generation
		a.streamGenerationBytes += len(sessionID)
	}
	return generation, true
}

// SetStatus updates status. Thread-safe via message passing.
func (a *WidgetApp) SetStatus(text string) {
	a.Post(StatusMsg{Text: text})
}

// StartProcessStatus starts an animated status with elapsed time.
func (a *WidgetApp) StartProcessStatus(text string) {
	a.Post(ProcessStatusMsg{Text: text, Active: true, ResetElapsed: true})
}

// UpdateProcessStatus updates the animated status label.
func (a *WidgetApp) UpdateProcessStatus(text string) {
	a.Post(ProcessStatusMsg{Text: text, Active: true})
}

// StopProcessStatus stops the animated status and restores the base status.
func (a *WidgetApp) StopProcessStatus() {
	a.Post(ProcessStatusMsg{Active: false})
}

// SetTokenCount updates token display. Thread-safe via message passing.
func (a *WidgetApp) SetTokenCount(tokens int, costCents float64) {
	a.Post(TokensMsg{Tokens: tokens, CostCent: costCents})
}

// SetModelName updates model display. Thread-safe via message passing.
func (a *WidgetApp) SetModelName(name string) {
	a.Post(ModelMsg{Name: name})
}

// SetModelVariant updates the active model variant preset display.
// Thread-safe via message passing.
func (a *WidgetApp) SetModelVariant(name string) {
	a.Post(ModelVariantMsg{Name: name})
}

// SetSessionNav replaces the navigator's Sessions section content.
// Thread-safe via message passing.
func (a *WidgetApp) SetSessionNav(nodes []widgets.SessionNavNode) {
	a.Post(SessionNavMsg{Nodes: nodes})
}

// SetSessionNavCallback wires the callback fired when a Sessions row is
// selected. Call once during setup, before Run starts; the sidebar itself
// invokes it directly from its mouse handler on the UI goroutine.
func (a *WidgetApp) SetSessionNavCallback(cb func(widgets.SessionNavNode)) {
	a.sidebar.SetOnSessionSelect(cb)
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

// SetModelVariantCallbacks sets the model-variant and recent-model cycling
// callbacks, triggered by keybind (see app_widget_keys.go).
func (a *WidgetApp) SetModelVariantCallbacks(onCycleVariant, onCycleRecentModel func()) {
	a.onCycleVariant = onCycleVariant
	a.onCycleRecentModel = onCycleRecentModel
}

// SetInterruptCallback sets the callback used to stop an active model or tool process.
func (a *WidgetApp) SetInterruptCallback(onInterrupt func()) {
	a.onInterrupt = onInterrupt
}

// SetApprovalCallback sets the callback for tool approval decisions.
func (a *WidgetApp) SetApprovalCallback(onApproval func(requestID string, approved, alwaysAllow bool)) {
	a.onApproval = onApproval
}

// SetModelPickerActionCallback installs the long-lived typed picker handler.
// Per-request closures are never retained in the event queue.
func (a *WidgetApp) SetModelPickerActionCallback(callback func(ModelPickerAction, string, any)) {
	a.modelPickerMu.Lock()
	a.onModelPickerAction = callback
	a.modelPickerMu.Unlock()
}

// RequestApproval displays an approval dialog for a tool operation.
// The callback set via SetApprovalCallback will be called with the decision.
func (a *WidgetApp) RequestApproval(req ApprovalRequestMsg) bool {
	return a.postApprovalRequest(req)
}

// toggleSidebar toggles the navigator visibility and rebuilds the layout.
func (a *WidgetApp) toggleSidebar() {
	a.sidebarWanted = !a.sidebarWanted
	a.updateSidebarVisibility()
	if a.sidebarVisible {
		a.setStatusOverride("Navigator shown", 2*time.Second)
	} else {
		a.setStatusOverride("Navigator hidden", 2*time.Second)
	}
}

// rebuildLayout rebuilds the three-region workspace based on panel state.
func (a *WidgetApp) rebuildLayout() {
	w, h := a.screen.Size()
	a.mainArea = a.buildWorkspaceMainArea()
	a.root = runtime.VBox(
		runtime.Fixed(a.header),
		expandFromZero(a.mainArea),
		runtime.Fixed(a.inputArea),
		runtime.Fixed(a.statusBar),
	)
	a.screen.SetRoot(a.root)
	a.screen.Resize(w, h)
	a.dirty = true
}

// SetSidebarVisible sets the sidebar visibility.
func (a *WidgetApp) SetSidebarVisible(visible bool) {
	a.sidebarWanted = visible
	a.updateSidebarVisibility()
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
func (a *WidgetApp) SetRLMStatus(status *widgets.RLMStatus, scratchpad []widgets.RLMScratchpadEntry) {
	a.sidebar.SetRLMStatus(status, scratchpad)
	a.updateSidebarVisibility()
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
		"  • Use Ctrl+B / Ctrl+Shift+B to toggle workspace panels",
		"  • Use Alt+[ and Alt+] to resize the navigator",
		"  • Use Alt+{ and Alt+} to resize the inspector",
		"  • Use Alt+End to jump to latest",
		"  • Press Shift+Enter to add a line without sending",
		"  • Press Ctrl+C twice to quit",
		"",
	}

	for _, tip := range tips {
		a.chatView.AddMessage(tip, "system")
	}
}

func copyToClipboard(text string) error {
	cmds := clipboardCommands()
	var failures []string
	for _, args := range cmds {
		if !clipboardCommandAvailable(args[0]) {
			continue
		}
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return nil
		} else {
			failures = append(failures, fmt.Sprintf("%s: %v", args[0], err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("clipboard command failed: %s", strings.Join(failures, "; "))
	}
	return fmt.Errorf("clipboard unavailable (install wl-clipboard, xclip, or xsel)")
}

func clipboardCommands() [][]string {
	return clipboardCommandsFor(stdruntime.GOOS)
}

func clipboardCommandsFor(goos string) [][]string {
	switch goos {
	case "darwin":
		return [][]string{{"pbcopy"}}
	case "windows":
		return [][]string{{"clip.exe"}}
	default:
		return [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard"},
			{"xsel", "--clipboard", "--input"},
			{"clip.exe"},
			{"/mnt/c/Windows/System32/clip.exe"},
		}
	}
}

func clipboardCommandAvailable(name string) bool {
	if strings.ContainsRune(name, os.PathSeparator) {
		info, err := os.Stat(name)
		return err == nil && !info.IsDir()
	}
	_, err := exec.LookPath(name)
	return err == nil
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
