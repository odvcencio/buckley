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
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/fluffyui/backend"
	"github.com/odvcencio/fluffyui/dragdrop"
	"github.com/odvcencio/fluffyui/fluffy"
	"github.com/odvcencio/fluffyui/keybind"
	"github.com/odvcencio/fluffyui/runtime"
	"github.com/odvcencio/fluffyui/scroll"
	fstate "github.com/odvcencio/fluffyui/state"
	"github.com/odvcencio/fluffyui/theme"
	"github.com/odvcencio/fluffyui/toast"
)

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

	// Machine integration
	Hub *telemetry.Hub

	// Callbacks
	OnSubmit      func(text string)
	OnQuit        func()
	OnFileSelect  func(path string)
	OnShellCmd    func(cmd string) string
	OnNextSession func()
	OnPrevSession func()
	OnApproval    func(requestID string, approved, alwaysAllow bool)
	OnSettings    func(settings UISettings)

	// Toast manager (optional, shared with controller)
	ToastManager *toast.ToastManager

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
	toastManager *toast.ToastManager
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

	// Machine event subscription
	machineHub   *telemetry.Hub
	machineUnsub func()

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

	toastManager := cfg.ToastManager
	if toastManager == nil {
		toastManager = toast.NewToastManager()
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
		toastManager:   toastManager,
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
	// Ctrl+C should quit only when input is empty (otherwise, let widget handle it to clear text)
	whenInputEmpty := func(ctx keybind.Context) bool {
		return r.inputArea == nil || !r.inputArea.HasText()
	}

	defs := buckleyKeyBindings(whenScrollable, whenSidebarFocused, whenInputEmpty)
	keymap := buildKeymap(defs)
	r.overlayKeymap = newOverlayKeymap(defs)

	if traceBackend := maybeWrapBackendForKeyTrace(&cfg); traceBackend != nil {
		cfg.Backend = traceBackend
	}

	// Build app options
	opts := []fluffy.AppOption{
		fluffy.WithRoot(root),
		fluffy.WithUpdate(r.update),
		fluffy.WithCommandHandler(r.handleCommand),
		fluffy.WithTickRate(16 * time.Millisecond),
		fluffy.WithKeyBindings(r.registerKeyBindings),
		fluffy.WithKeymap(keymap),
		// Enable automatic focus registration so focusable widgets are discovered.
		fluffy.WithFocusRegistration(runtime.FocusRegistrationAuto),
		// Prefer the last focusable (InputArea) while keeping fallback focus logic.
		fluffy.WithAutoFocusPolicy(runtime.AutoFocusLast),
		// OnReady initializes focus once the screen exists.
		fluffy.WithOnReady(r.onAppReady),
		// Toast layer is pushed automatically during OnReady.
		fluffy.WithToastLayer(toastManager),
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

	// Style the toast stack created by WithToastLayer
	if bundle.ToastStack != nil && r.styleCache != nil {
		th := r.theme
		bundle.ToastStack.SetStyles(
			styleCache.Get(th.SurfaceRaised),
			styleCache.Get(th.TextPrimary),
			styleCache.Get(th.Info),
			styleCache.Get(th.Success),
			styleCache.Get(th.Warning),
			styleCache.Get(th.Error),
		)
		bundle.ToastStack.SetAnimationsEnabled(!cfg.ReduceMotion && cfg.EffectsEnabled)
	}

	r.bindWidgets()
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

	// Subscribe to machine events if Hub is provided
	if cfg.Hub != nil {
		r.machineHub = cfg.Hub
		r.subscribeToMachineEvents(cfg.Hub)
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
	defer r.stopMachineSubscription()
	return r.app.Run(context.Background())
}

// RunWithContext starts the TUI event loop with a cancellation context.
func (r *Runner) RunWithContext(ctx context.Context) error {
	if r == nil || r.app == nil {
		return nil
	}
	defer r.stopAgentServer() // Ensure cleanup on exit
	defer r.stopMachineSubscription()
	return r.app.Run(ctx)
}
