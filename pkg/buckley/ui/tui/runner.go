// Package tui provides the integrated terminal user interface for Buckley.
// This file implements the fluffyui-native runner using runtime.App.

package tui

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/buckley/ui/filepicker"
	uiservices "github.com/odvcencio/buckley/pkg/buckley/ui/tui/services"
	uitstate "github.com/odvcencio/buckley/pkg/buckley/ui/tui/state"
	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/buckley/pkg/diagnostics"
	"github.com/odvcencio/fluffyui/accessibility"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/dragdrop"
	"github.com/odvcencio/fluffyui/fluffy"
	"github.com/odvcencio/fluffyui/keybind"
	"github.com/odvcencio/fluffyui/markdown"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/scroll"
	fstate "github.com/odvcencio/fluffyui/state"
	"github.com/odvcencio/fluffyui/terminal"
	"github.com/odvcencio/fluffyui/theme"
	"github.com/odvcencio/fluffyui/toast"
	uiwidgets "github.com/odvcencio/fluffyui/widgets"
)

// RunnerConfig configures the fluffyui-native TUI runner.
type RunnerConfig struct {
	Theme             *theme.Theme
	ThemeName         string
	StylesheetPath    string
	ModelName         string
	SessionID         string
	WorkDir           string
	ProjectRoot       string
	ReduceMotion      bool
	HighContrast      bool
	EffectsEnabled    bool
	EffectsEnabledSet bool
	UseTextLabels     bool
	MessageMetadata   string
	WebBaseURL        string
	Audio             AudioConfig
	AgentSocket       string
	SidebarWidth      int
	SidebarMinWidth   int
	SidebarMaxWidth   int

	// Callbacks
	OnSubmit      func(text string)
	OnQuit        func()
	OnFileSelect  func(path string)
	OnShellCmd    func(cmd string) string
	OnNextSession func()
	OnPrevSession func()
	OnApproval    func(requestID string, approved, alwaysAllow bool)
	OnSettings    func(settings UISettings)

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
	toastStack   *buckleywidgets.SignalToastStack
	filePicker   *filepicker.FilePicker
	modelPalette *buckleywidgets.InteractivePalette

	// State
	state            *uitstate.AppState
	statusService    *uiservices.StatusService
	chatService      *uiservices.ChatService
	sidebarService   *uiservices.SidebarService
	coalescer        *Coalescer
	styleCache       *StyleCache
	layoutSubs       fstate.Subscriptions
	settingsSubs     fstate.Subscriptions
	focusInitialized bool
	styleWatchStop   func()

	// Config
	theme       *theme.Theme
	workDir     string
	projectRoot string

	// Callbacks
	onSubmit      func(text string)
	onQuit        func()
	onFileSelect  func(path string)
	onShellCmd    func(cmd string) string
	onApproval    func(requestID string, approved, alwaysAllow bool)
	onNextSession func()
	onPrevSession func()
	onSettings    func(settings UISettings)

	// Agent server for real-time control
	agentServer *AgentServer

	approvalMu     sync.Mutex
	approvalQueue  []buckleywidgets.ApprovalRequest
	approvalActive bool

	overlayKeymap       *keybind.Keymap
	overlayKeymapActive bool

	dragCandidate *dragCandidate
	dragging      bool
	dragData      dragdrop.DragData
	dragSource    dragdrop.Draggable
	dragTarget    dragdrop.DropTarget
}

const overlayNoopCommand = "overlay.noop"

type keyBindingDef struct {
	sequence     string
	command      string
	when         keybind.Condition
	allowInModal bool
}

// NewRunner creates a new fluffyui-native TUI runner.
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	th := cfg.Theme
	if th == nil {
		th = theme.DefaultTheme()
	}
	effectsEnabled := cfg.EffectsEnabled
	if !cfg.EffectsEnabledSet {
		effectsEnabled = true
	}
	cfg.EffectsEnabled = effectsEnabled

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
	sidebarCfg := resolveSidebarConfig(cfg)
	statusService := uiservices.NewStatusService(appState, nil)
	chatService := uiservices.NewChatService(appState)
	sidebarService := uiservices.NewSidebarService(appState)
	appState.ModelName.Set(strings.TrimSpace(cfg.ModelName))
	appState.SessionID.Set(strings.TrimSpace(cfg.SessionID))
	if appState.MessageMetadata != nil {
		meta := strings.TrimSpace(cfg.MessageMetadata)
		if meta != "" {
			appState.MessageMetadata.Set(meta)
		}
	}
	if appState.ThemeName != nil {
		themeName := strings.TrimSpace(cfg.ThemeName)
		if themeName == "" {
			themeName = "dark"
		}
		appState.ThemeName.Set(themeName)
	}
	if appState.StylesheetPath != nil {
		appState.StylesheetPath.Set(strings.TrimSpace(cfg.StylesheetPath))
	}
	if appState.ReduceMotion != nil {
		appState.ReduceMotion.Set(cfg.ReduceMotion)
	}
	if appState.HighContrast != nil {
		appState.HighContrast.Set(cfg.HighContrast)
	}
	if appState.EffectsEnabled != nil {
		appState.EffectsEnabled.Set(cfg.EffectsEnabled)
	}
	if appState.SidebarWidth != nil {
		appState.SidebarWidth.Set(sidebarCfg.Width)
	}
	if sidebarService != nil {
		sidebarService.SetProjectPath(projectRoot)
	}

	r := &Runner{
		theme:          th,
		workDir:        workDir,
		projectRoot:    projectRoot,
		state:          appState,
		statusService:  statusService,
		chatService:    chatService,
		sidebarService: sidebarService,
		styleCache:     styleCache,
		onSubmit:       cfg.OnSubmit,
		onQuit:         cfg.OnQuit,
		onFileSelect:   cfg.OnFileSelect,
		onShellCmd:     cfg.OnShellCmd,
		onApproval:     cfg.OnApproval,
		onSettings:     cfg.OnSettings,
	}

	// Build widgets
	r.buildWidgets(cfg, styleCache)

	// Build widget tree
	root := r.buildLayout()

	whenScrollable := keybind.WhenFocusedWidget(func(w runtime.Widget) bool {
		_, ok := w.(scroll.Controller)
		return ok
	})
	whenSidebarFocused := keybind.WhenFocusedWidget(func(w runtime.Widget) bool {
		return r.sidebar != nil && widgetInTree(r.sidebar, w)
	})

	defs := buckleyKeyBindings(whenScrollable, whenSidebarFocused)
	keymap := buildKeymap(defs)
	r.overlayKeymap = newOverlayKeymap(defs)

	// Build app options
	opts := []fluffy.AppOption{
		fluffy.WithRoot(root),
		fluffy.WithUpdate(r.update),
		fluffy.WithCommandHandler(r.handleCommand),
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

	r.bindLayoutSignals()
	r.bindSettingsSignals()

	r.coalescer = NewCoalescer(DefaultCoalescerConfig())

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

func resolveSidebarConfig(cfg RunnerConfig) buckleywidgets.SidebarConfig {
	sidebarCfg := buckleywidgets.DefaultSidebarConfig()
	if cfg.SidebarMinWidth > 0 {
		sidebarCfg.MinWidth = cfg.SidebarMinWidth
	}
	if cfg.SidebarMaxWidth > 0 {
		sidebarCfg.MaxWidth = cfg.SidebarMaxWidth
	}
	if cfg.SidebarWidth > 0 {
		sidebarCfg.Width = cfg.SidebarWidth
	}
	if sidebarCfg.Width < sidebarCfg.MinWidth {
		sidebarCfg.Width = sidebarCfg.MinWidth
	}
	if sidebarCfg.Width > sidebarCfg.MaxWidth {
		sidebarCfg.Width = sidebarCfg.MaxWidth
	}
	return sidebarCfg
}

func (r *Runner) handleCodeAction(action, language, code string) {
	if strings.TrimSpace(code) == "" {
		return
	}
	switch action {
	case "copy":
		r.copyToClipboard(code, "Copied code block")
	case "open":
		if r.app == nil {
			r.copyToClipboard(code, "Copied code block")
			return
		}
		r.showCodePreview(language, code)
	}
}

func (r *Runner) copyToClipboard(text, success string) {
	if r.app == nil {
		return
	}
	cb := r.app.Services().Clipboard()
	if cb == nil || !cb.Available() {
		if r.statusService != nil {
			r.statusService.SetStatusOverride("Clipboard unavailable", 2*time.Second)
		}
		return
	}
	if err := cb.Write(text); err != nil {
		if r.statusService != nil {
			r.statusService.SetStatusOverride("Clipboard unavailable", 2*time.Second)
		}
		return
	}
	if r.statusService != nil {
		r.statusService.SetStatusOverride(success, 2*time.Second)
	}
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
		ModelName:        r.state.ModelName,
		MetadataMode:     r.state.MessageMetadata,
		SearchQuery:      r.state.ChatSearchQuery,
		SearchMatches:    r.state.ChatSearchMatches,
		SelectionText:    r.state.ChatSelectionText,
		SelectionActive:  r.state.ChatSelectionActive,
	})
	r.chatView.SetStyles(
		styleCache.Get(th.User),
		styleCache.Get(th.Assistant),
		styleCache.Get(th.System),
		styleCache.Get(th.Tool),
		styleCache.Get(th.Thinking),
	)
	r.chatView.SetMetadataStyle(styleCache.Get(th.TextMuted))
	r.chatView.SetUIStyles(
		styleCache.Get(th.Scrollbar),
		styleCache.Get(th.ScrollThumb),
		styleCache.Get(th.Selection),
		styleCache.Get(th.SearchMatch),
		styleCache.Get(th.Background),
	)
	mdRenderer := markdown.NewRenderer(th)
	r.chatView.SetMarkdownRenderer(mdRenderer, styleCache.Get(mdRenderer.CodeBlockBackground()))
	r.chatView.OnCodeAction(r.handleCodeAction)
	r.chatView.OnScrollChange(func(top, total, viewHeight int) {
		if r.statusService == nil {
			return
		}
		r.statusService.SetScrollPosition(scrollStatusText(top, total, viewHeight))
	})
	if r.statusService != nil {
		top, total, viewHeight := r.chatView.ScrollPosition()
		r.statusService.SetScrollPosition(scrollStatusText(top, total, viewHeight))
	}

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
		EffectsEnabled: r.state.EffectsEnabled,
		ReduceMotion:   r.state.ReduceMotion,
		BGStyle:        styleCache.Get(th.Surface),
		TextStyle:      styleCache.Get(th.TextMuted),
		ModeStyle:      styleCache.Get(th.Accent),
	})

	// Sidebar
	r.sidebar = buckleywidgets.NewSidebarWithBindings(
		resolveSidebarConfig(cfg),
		buckleywidgets.SidebarBindings{
			CurrentTask:        r.state.SidebarCurrentTask,
			TaskProgress:       r.state.SidebarTaskProgress,
			PlanTasks:          r.state.SidebarPlanTasks,
			RunningTools:       r.state.SidebarRunningTools,
			ToolHistory:        r.state.SidebarToolHistory,
			ActiveTouches:      r.state.SidebarActiveTouches,
			RecentFiles:        r.state.SidebarRecentFiles,
			RLMStatus:          r.state.SidebarRLMStatus,
			RLMScratchpad:      r.state.SidebarRLMScratchpad,
			CircuitStatus:      r.state.SidebarCircuitStatus,
			Experiment:         r.state.SidebarExperiment,
			ExperimentStatus:   r.state.SidebarExperimentStatus,
			ExperimentVariants: r.state.SidebarExperimentVariants,
			ContextUsed:        r.state.ContextUsed,
			ContextBudget:      r.state.ContextBudget,
			ContextWindow:      r.state.ContextWindow,
			ProjectPath:        r.state.SidebarProjectPath,
			Width:              r.state.SidebarWidth,
			TabIndex:           r.state.SidebarTabIndex,
			ShowCurrentTask:    r.state.SidebarShowCurrentTask,
			ShowPlan:           r.state.SidebarShowPlan,
			ShowTools:          r.state.SidebarShowTools,
			ShowContext:        r.state.SidebarShowContext,
			ShowTouches:        r.state.SidebarShowTouches,
			ShowRecentFiles:    r.state.SidebarShowRecentFiles,
			ShowExperiment:     r.state.SidebarShowExperiment,
			ShowRLM:            r.state.SidebarShowRLM,
			ShowCircuit:        r.state.SidebarShowCircuit,
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

	// Toast stack
	r.toastStack = buckleywidgets.NewSignalToastStack(r.state.Toasts)
	r.toastStack.SetAnimationsEnabled(!cfg.ReduceMotion && cfg.EffectsEnabled)
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
	r.modelPalette = buckleywidgets.NewInteractivePalette("Select Model")
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
	// Main content area: ChatView + optional Sidebar
	mainArea := runtime.HBox(
		runtime.Flexible(r.chatView, 3),
	)
	if r.sidebar != nil && r.state != nil && r.state.SidebarVisible.Get() {
		width := 0
		if r.state.SidebarWidth != nil {
			width = r.state.SidebarWidth.Get()
		}
		if width > 0 {
			mainArea.Add(runtime.Sized(r.sidebar, width))
		}
	}

	// Full layout: Header, Main, Input, Status
	root := runtime.VBox(
		runtime.Fixed(r.header),
		runtime.Expanded(mainArea),
		runtime.Fixed(r.inputArea),
		runtime.Fixed(r.statusBar),
	)

	return root
}

func (r *Runner) bindLayoutSignals() {
	if r == nil || r.app == nil || r.state == nil {
		return
	}
	r.layoutSubs.Clear()
	r.layoutSubs.SetScheduler(r.app.Services().Scheduler())
	if r.state.SidebarVisible != nil {
		r.layoutSubs.Observe(r.state.SidebarVisible, func() {
			r.rebuildLayout()
		})
	}
	if r.state.SidebarWidth != nil {
		r.layoutSubs.Observe(r.state.SidebarWidth, func() {
			r.rebuildLayout()
		})
	}
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
				if r.chatService != nil {
					r.chatService.AddMessage(result, "system")
				}
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

	registry.Register(keybind.Command{
		ID:    overlayNoopCommand,
		Title: "Overlay No-op",
		Handler: func(ctx keybind.Context) {
		},
	})

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
		ID:    "buckley.file-picker",
		Title: "File Picker",
		Handler: func(ctx keybind.Context) {
			r.showFilePicker()
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.settings",
		Title: "Settings",
		Handler: func(ctx keybind.Context) {
			r.showSettingsOverlay()
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.focus-input",
		Title: "Focus Input",
		Handler: func(ctx keybind.Context) {
			r.ensureInputFocus()
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.toggle-task",
		Title: "Toggle Current Task",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.ToggleCurrentTask()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.toggle-plan",
		Title: "Toggle Plan",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.TogglePlan()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.toggle-tools",
		Title: "Toggle Tools",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.ToggleTools()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.toggle-context",
		Title: "Toggle Context",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.ToggleContext()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.toggle-touches",
		Title: "Toggle Touches",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.ToggleTouches()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.toggle-files",
		Title: "Toggle Recent Files",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.ToggleRecentFiles()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.prev-tab",
		Title: "Sidebar Previous Tab",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.PrevTab()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.next-tab",
		Title: "Sidebar Next Tab",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.NextTab()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.shrink",
		Title: "Sidebar Shrink",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.Shrink(2)
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.sidebar.grow",
		Title: "Sidebar Grow",
		Handler: func(ctx keybind.Context) {
			if r.sidebarService != nil {
				r.sidebarService.Grow(2)
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.scroll-top",
		Title: "Scroll Top",
		Handler: func(ctx keybind.Context) {
			if r.chatView == nil {
				return
			}
			r.chatView.ScrollToTop()
			if ctx.App != nil {
				ctx.App.Invalidate()
			}
		},
	})

	registry.Register(keybind.Command{
		ID:    "buckley.scroll-bottom",
		Title: "Scroll Bottom",
		Handler: func(ctx keybind.Context) {
			if r.chatView == nil {
				return
			}
			r.chatView.ScrollToBottom()
			if ctx.App != nil {
				ctx.App.Invalidate()
			}
		},
	})
}

func buckleyKeyBindings(whenScrollable, whenSidebarFocused keybind.Condition) []keyBindingDef {
	return []keyBindingDef{
		{sequence: "ctrl+q", command: "app.quit", allowInModal: true},
		{sequence: "f5", command: "app.refresh", allowInModal: true},
		{sequence: "tab", command: "focus.next", allowInModal: true},
		{sequence: "shift+tab", command: "focus.prev", allowInModal: true},
		{sequence: "ctrl+up", command: "scroll.up", when: whenScrollable, allowInModal: true},
		{sequence: "ctrl+down", command: "scroll.down", when: whenScrollable, allowInModal: true},
		{sequence: "pgup", command: "scroll.pageUp", when: whenScrollable, allowInModal: true},
		{sequence: "pgdn", command: "scroll.pageDown", when: whenScrollable, allowInModal: true},
		{sequence: "home", command: "scroll.home", when: whenScrollable, allowInModal: true},
		{sequence: "end", command: "scroll.end", when: whenScrollable, allowInModal: true},
		{sequence: "ctrl+shift+c", command: "clipboard.copy", when: keybind.WhenFocusedClipboardTarget(), allowInModal: true},
		{sequence: "ctrl+shift+x", command: "clipboard.cut", when: keybind.WhenFocusedClipboardTarget(), allowInModal: true},
		{sequence: "ctrl+shift+v", command: "clipboard.paste", when: keybind.WhenFocusedClipboardTarget(), allowInModal: true},
		{sequence: "ctrl+b", command: "buckley.toggle-sidebar"},
		{sequence: "alt+right", command: "buckley.next-session"},
		{sequence: "alt+left", command: "buckley.prev-session"},
		{sequence: "ctrl+f", command: "buckley.search"},
		{sequence: "ctrl+p", command: "buckley.file-picker"},
		{sequence: "ctrl+i", command: "buckley.focus-input"},
		{sequence: "1", command: "buckley.sidebar.toggle-task", when: whenSidebarFocused},
		{sequence: "2", command: "buckley.sidebar.toggle-plan", when: whenSidebarFocused},
		{sequence: "3", command: "buckley.sidebar.toggle-tools", when: whenSidebarFocused},
		{sequence: "4", command: "buckley.sidebar.toggle-context", when: whenSidebarFocused},
		{sequence: "5", command: "buckley.sidebar.toggle-touches", when: whenSidebarFocused},
		{sequence: "6", command: "buckley.sidebar.toggle-files", when: whenSidebarFocused},
		{sequence: "ctrl+g g", command: "buckley.scroll-top"},
		{sequence: "ctrl+g b", command: "buckley.scroll-bottom"},
		{sequence: "ctrl+g s", command: "buckley.toggle-sidebar"},
		{sequence: "ctrl+g f", command: "buckley.search"},
		{sequence: "ctrl+g p", command: "buckley.file-picker"},
		{sequence: "ctrl+g i", command: "buckley.focus-input"},
		{sequence: "ctrl+g t", command: "buckley.settings"},
		{sequence: "ctrl+g ,", command: "buckley.sidebar.prev-tab"},
		{sequence: "ctrl+g .", command: "buckley.sidebar.next-tab"},
		{sequence: "ctrl+g [", command: "buckley.sidebar.shrink"},
		{sequence: "ctrl+g ]", command: "buckley.sidebar.grow"},
	}
}

func buildKeymap(defs []keyBindingDef) *keybind.Keymap {
	bindings := make([]keybind.Binding, 0, len(defs))
	for _, def := range defs {
		bindings = append(bindings, keybind.Binding{
			Key:     keybind.MustParseKeySequence(def.sequence),
			Command: def.command,
			When:    def.when,
		})
	}
	return &keybind.Keymap{Name: "buckley", Bindings: bindings}
}

func newOverlayKeymap(defs []keyBindingDef) *keybind.Keymap {
	bindings := make([]keybind.Binding, 0, len(defs))
	for _, def := range defs {
		if def.allowInModal {
			continue
		}
		bindings = append(bindings, keybind.Binding{
			Key:     keybind.MustParseKeySequence(def.sequence),
			Command: overlayNoopCommand,
		})
	}
	return &keybind.Keymap{Name: "buckley.overlay", Bindings: bindings}
}

func normalizeCtrlGChord(msg runtime.KeyMsg) runtime.KeyMsg {
	if msg.Key != terminal.KeyNone || !msg.Ctrl {
		return msg
	}
	if msg.Rune != 'g' && msg.Rune != 'G' {
		return msg
	}
	msg.Key = terminal.KeyRune
	msg.Rune = 'g'
	return msg
}

// update handles messages in the event loop.
// This is the Buckley-specific update function that processes domain messages.
func (r *Runner) update(app *runtime.App, msg runtime.Message) bool {
	if mouse, ok := msg.(runtime.MouseMsg); ok {
		r.handleDragDrop(app, mouse)
		r.handleMouseFocus(app, mouse)
	}
	if custom, ok := msg.(runtime.CustomMsg); ok {
		if update, ok := custom.Value.(stylesheetUpdateMsg); ok {
			r.handleStylesheetUpdate(update)
			return true
		}
	}
	if key, ok := msg.(runtime.KeyMsg); ok {
		msg = normalizeCtrlGChord(key)
	}
	r.showNextApproval()
	r.initFocusIfNeeded(app)

	// First, let the default handler process terminal events
	if runtime.DefaultUpdate(app, msg) {
		return true
	}

	// Handle tick for coalescer
	if _, ok := msg.(runtime.TickMsg); ok {
		r.coalescer.Tick()
	}

	return r.flushPending()
}

func (r *Runner) handleCommand(cmd runtime.Command) bool {
	switch c := cmd.(type) {
	case runtime.PushOverlay:
		if r.app != nil {
			if screen := r.app.Screen(); screen != nil {
				screen.PushLayer(c.Widget, c.Modal)
				r.syncOverlayKeymap(screen)
				r.focusTopLayer(screen)
				return true
			}
		}
		return false
	case runtime.PopOverlay:
		if r.app != nil {
			if screen := r.app.Screen(); screen != nil {
				if overlayCount(screen) == 0 {
					r.syncOverlayKeymap(screen)
					r.focusTopLayer(screen)
					return true
				}
				if screen.PopLayer() {
					r.syncOverlayKeymap(screen)
					r.focusTopLayer(screen)
					return true
				}
				r.syncOverlayKeymap(screen)
				r.focusTopLayer(screen)
				return false
			}
		}
		return false
	case runtime.FileSelected:
		if r.onFileSelect != nil {
			r.onFileSelect(c.Path)
		}
		return false
	case buckleywidgets.ApprovalResponse:
		return r.handleApprovalResponse(c)
	default:
		return false
	}
}

func (r *Runner) handleApprovalResponse(resp buckleywidgets.ApprovalResponse) bool {
	if r == nil {
		return false
	}
	if r.onApproval != nil {
		r.onApproval(resp.RequestID, resp.Approved, resp.AlwaysAllow)
	}

	r.approvalMu.Lock()
	r.approvalActive = false
	r.approvalMu.Unlock()
	r.showNextApproval()
	return true
}

func (r *Runner) showNextApproval() {
	if r == nil || r.app == nil || r.app.Screen() == nil {
		return
	}
	r.approvalMu.Lock()
	if r.approvalActive || len(r.approvalQueue) == 0 {
		r.approvalMu.Unlock()
		return
	}
	request := r.approvalQueue[0]
	r.approvalQueue = r.approvalQueue[1:]
	r.approvalActive = true
	r.approvalMu.Unlock()
	r.showApprovalOverlay(request)
}

func (r *Runner) flushPending() bool {
	if r == nil || r.coalescer == nil || r.chatService == nil {
		return false
	}
	flushed := r.coalescer.Drain()
	if len(flushed) == 0 {
		return false
	}
	for _, item := range flushed {
		r.chatService.AppendToLastMessage(item.Text)
	}
	return true
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
// Public API methods (matching App interface)
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

// SetChatMessages replaces the chat history.
func (r *Runner) SetChatMessages(messages []buckleywidgets.ChatMessage) {
	if r == nil || r.chatService == nil {
		return
	}
	r.chatService.SetMessages(messages)
}

// StreamChunk sends streaming text chunk.
func (r *Runner) StreamChunk(sessionID, text string) {
	if r == nil || r.coalescer == nil {
		return
	}
	r.coalescer.Add(sessionID, text)
}

// StreamEnd signals end of streaming.
func (r *Runner) StreamEnd(sessionID, fullText string) {
	if r == nil || r.coalescer == nil {
		return
	}
	// Flush all pending content to ensure nothing is lost on session switch.
	r.coalescer.FlushAll()
	r.coalescer.Clear(sessionID)
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
	if r == nil || r.app == nil {
		return
	}
	if r.onQuit != nil {
		r.onQuit()
	}
	r.app.ExecuteCommand(runtime.Quit{})
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
}

// SetSessionID updates the displayed session ID.
func (r *Runner) SetSessionID(id string) {
	if r == nil || r.state == nil {
		return
	}
	id = strings.TrimSpace(id)
	r.state.SessionID.Set(id)
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

// ShowModelPicker displays the model picker palette.
func (r *Runner) ShowModelPicker(items []uiwidgets.PaletteItem, onSelect func(item uiwidgets.PaletteItem)) {
	if r.app == nil || r.modelPalette == nil || len(items) == 0 {
		return
	}

	// Set the items
	r.modelPalette.SetItems(items)

	// Set the callback that dismisses the palette after selection
	r.modelPalette.SetOnSelect(func(item uiwidgets.PaletteItem) {
		// Then call the user's callback
		if onSelect != nil {
			onSelect(item)
		}
	})

	// Show as overlay via command
	overlay := r.wrapModalOverlay(r.modelPalette, nil)
	r.app.ExecuteCommand(runtime.PushOverlay{
		Widget: overlay,
		Modal:  true,
	})
}

// ShowApproval displays a modal approval dialog.
func (r *Runner) ShowApproval(request buckleywidgets.ApprovalRequest) {
	if r == nil || strings.TrimSpace(request.ID) == "" {
		return
	}
	r.approvalMu.Lock()
	r.approvalQueue = append(r.approvalQueue, request)
	r.approvalMu.Unlock()
	r.showNextApproval()
}

// ShowSettings displays the settings dialog.
func (r *Runner) ShowSettings() {
	if r == nil {
		return
	}
	r.showSettingsOverlay()
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
	// Runner doesn't use diagnostics directly in the same way the legacy app did
	// The telemetry bridge handles this through events
}

// SidebarSignals exposes writable signals for sidebar state.
func (r *Runner) SidebarSignals() SidebarSignals {
	if r == nil || r.state == nil {
		return SidebarSignals{}
	}
	return SidebarSignals{
		CurrentTask:        r.state.SidebarCurrentTask,
		TaskProgress:       r.state.SidebarTaskProgress,
		PlanTasks:          r.state.SidebarPlanTasks,
		RunningTools:       r.state.SidebarRunningTools,
		ToolHistory:        r.state.SidebarToolHistory,
		ActiveTouches:      r.state.SidebarActiveTouches,
		RecentFiles:        r.state.SidebarRecentFiles,
		RLMStatus:          r.state.SidebarRLMStatus,
		RLMScratchpad:      r.state.SidebarRLMScratchpad,
		CircuitStatus:      r.state.SidebarCircuitStatus,
		Experiment:         r.state.SidebarExperiment,
		ExperimentStatus:   r.state.SidebarExperimentStatus,
		ExperimentVariants: r.state.SidebarExperimentVariants,
	}
}
