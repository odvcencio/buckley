package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/cost"
	"github.com/odvcencio/buckley/pkg/diagnostics"
	"github.com/odvcencio/buckley/pkg/execution"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/toast"
)

// NewController creates a new TUI controller.
func NewController(cfg ControllerConfig) (*Controller, error) {
	if cfg.Config == nil {
		cfg.Config = config.DefaultConfig()
	}
	if cfg.Store == nil {
		return nil, fmt.Errorf("store required")
	}
	if cfg.ModelManager == nil {
		return nil, fmt.Errorf("model manager required")
	}

	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}
	baseCtx := cfg.Context
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	ctrlCtx, ctrlCancel := context.WithCancel(baseCtx)

	progressMgr := progress.NewProgressManager()
	toastMgr := toast.NewToastManager()
	budgetNotifier := cost.NewBudgetNotifier()
	budgetNotifier.OnAlert(func(alert cost.BudgetAlert) {
		if toastMgr == nil {
			return
		}
		level, title, message := formatBudgetToast(alert)
		if strings.TrimSpace(message) == "" {
			message = "budget threshold reached"
		}
		toastMgr.Show(level, title, message, toast.DefaultToastDuration)
	})

	projectSessions, currentIdx, err := loadOrCreateProjectSessions(cfg, ctrlCtx, workDir, progressMgr, toastMgr)
	if err != nil {
		ctrlCancel()
		return nil, err
	}

	// Determine project root
	projectRoot := workDir

	// Create TUI app
	currentSessionID := ""
	if currentIdx >= 0 && currentIdx < len(projectSessions) {
		currentSessionID = projectSessions[currentIdx].ID
	}
	webBaseURL := ""
	metadataMode := ""
	audioCfg := AudioConfig{}
	sidebarWidth := 0
	sidebarMinWidth := 0
	sidebarMaxWidth := 0
	uiSettings := UISettings{
		ThemeName:       "dark",
		StylesheetPath:  "",
		MessageMetadata: "",
		HighContrast:    false,
		ReduceMotion:    false,
		EffectsEnabled:  true,
	}
	if cfg.Config != nil {
		webBaseURL = resolveWebBaseURL(cfg.Config)
		metadataMode = cfg.Config.UI.MessageMetadata
		audioCfg = AudioConfig{
			Enabled:      cfg.Config.UI.Audio.Enabled,
			AssetsPath:   cfg.Config.UI.Audio.AssetsPath,
			MasterVolume: cfg.Config.UI.Audio.MasterVolume,
			SFXVolume:    cfg.Config.UI.Audio.SFXVolume,
			MusicVolume:  cfg.Config.UI.Audio.MusicVolume,
			Muted:        cfg.Config.UI.Audio.Muted,
		}
		sidebarWidth = cfg.Config.UI.SidebarWidth
		sidebarMinWidth = cfg.Config.UI.SidebarMinWidth
		sidebarMaxWidth = cfg.Config.UI.SidebarMaxWidth
		uiSettings.MessageMetadata = metadataMode
		uiSettings.HighContrast = cfg.Config.UI.HighContrast
		uiSettings.ReduceMotion = cfg.Config.UI.ReduceAnimation
	}
	if cfg.Store != nil {
		loaded, err := cfg.Store.GetSettings([]string{
			uiThemeSettingKey,
			uiStylesheetSettingKey,
			uiMetadataSettingKey,
			uiHighContrastSettingKey,
			uiReduceMotionSettingKey,
			uiEffectsSettingKey,
		})
		if err == nil {
			if value := strings.TrimSpace(loaded[uiThemeSettingKey]); value != "" {
				uiSettings.ThemeName = value
			}
			if value := strings.TrimSpace(loaded[uiStylesheetSettingKey]); value != "" {
				uiSettings.StylesheetPath = value
			}
			if value := strings.TrimSpace(loaded[uiMetadataSettingKey]); value != "" {
				uiSettings.MessageMetadata = value
			}
			uiSettings.HighContrast = parseSettingBool(loaded[uiHighContrastSettingKey], uiSettings.HighContrast)
			uiSettings.ReduceMotion = parseSettingBool(loaded[uiReduceMotionSettingKey], uiSettings.ReduceMotion)
			uiSettings.EffectsEnabled = parseSettingBool(loaded[uiEffectsSettingKey], uiSettings.EffectsEnabled)
		}
	}
	uiSettings = uiSettings.Normalized()
	th := resolveTheme(uiSettings)
	// Use fluffyui-native Runner by default (simpler, more maintainable)
	var app App
	var ctrl *Controller
	app, err = NewRunner(RunnerConfig{
		Theme:             th,
		ThemeName:         uiSettings.ThemeName,
		StylesheetPath:    uiSettings.StylesheetPath,
		ModelName:         cfg.Config.Models.Execution,
		SessionID:         currentSessionID,
		WorkDir:           workDir,
		ProjectRoot:       projectRoot,
		ReduceMotion:      uiSettings.ReduceMotion,
		HighContrast:      uiSettings.HighContrast,
		EffectsEnabled:    uiSettings.EffectsEnabled,
		EffectsEnabledSet: true,
		UseTextLabels:     cfg.Config != nil && cfg.Config.UI.UseTextLabels,
		MessageMetadata:   uiSettings.MessageMetadata,
		WebBaseURL:        webBaseURL,
		Audio:             audioCfg,
		AgentSocket:       cfg.AgentSocket,
		SidebarWidth:      sidebarWidth,
		SidebarMinWidth:   sidebarMinWidth,
		SidebarMaxWidth:   sidebarMaxWidth,
		ToastManager:      toastMgr,
		OnApproval: func(requestID string, approved, alwaysAllow bool) {
			if ctrl != nil {
				ctrl.handleApprovalDecision(requestID, approved, alwaysAllow)
			}
		},
		OnSettings: func(settings UISettings) {
			if ctrl != nil {
				ctrl.handleSettingsUpdate(settings)
			}
		},
	})
	if err != nil {
		ctrlCancel()
		return nil, fmt.Errorf("create TUI app: %w", err)
	}

	ctrl = &Controller{
		app:            app,
		cfg:            cfg.Config,
		modelMgr:       cfg.ModelManager,
		store:          cfg.Store,
		projectCtx:     cfg.ProjectCtx,
		registry:       projectSessions[currentIdx].ToolRegistry,
		conversation:   projectSessions[currentIdx].Conversation,
		telemetry:      cfg.Telemetry,
		progressMgr:    progressMgr,
		toastMgr:       toastMgr,
		budgetAlerts:   budgetNotifier,
		costTrackers:   map[string]*cost.Tracker{},
		workDir:        workDir,
		sessions:       projectSessions,
		currentSession: currentIdx,
		ctx:            ctrlCtx,
		cancel:         ctrlCancel,
	}

	// Initialize execution strategy based on config
	execMode := config.DefaultExecutionMode
	if cfg.Config != nil {
		execMode = cfg.Config.ExecutionMode()
	}
	rlmSubAgentMaxConcurrent := 0
	if cfg.Config != nil {
		rlmSubAgentMaxConcurrent = cfg.Config.RLM.SubAgent.MaxConcurrent
	}
	strategyFactory := execution.NewFactory(
		cfg.ModelManager,
		projectSessions[currentIdx].ToolRegistry,
		cfg.Store,
		cfg.Telemetry,
		execution.FactoryConfig{
			DefaultMaxIterations:     25,
			ConfidenceThreshold:      0.7,
			RLMSubAgentMaxConcurrent: rlmSubAgentMaxConcurrent,
			UseTOON:                  cfg.Config != nil && cfg.Config.Encoding.UseToon,
			EnableReasoning:          true,
		},
	)
	ctrl.strategyFactory = strategyFactory
	strategy, err := strategyFactory.Create(execMode)
	if err != nil {
		strategy, _ = strategyFactory.Create(config.ExecutionModeClassic)
	}
	ctrl.execStrategy = strategy
	if ctrl.execStrategy != nil {
		app.SetExecutionMode(ctrl.execStrategy.Name())
	}
	attachStrategyUIHooks(ctrl.execStrategy, progressMgr, toastMgr)
	app.SetStreaming(projectSessions[currentIdx].Streaming)

	progressMgr.SetOnChange(func(items []progress.Progress) {
		app.SetProgress(items)
	})
	toastMgr.SetOnChange(func(items []*toast.Toast) {
		app.SetToasts(items)
	})
	app.SetToastDismissHandler(toastMgr.Dismiss)

	// Create telemetry bridge for sidebar updates
	if cfg.Telemetry != nil {
		// Runner uses the simpler bridge without scheduler dependency
		ctrl.telemetryBridge = NewSimpleTelemetryBridge(cfg.Telemetry, app.SidebarSignals())

		// Wire UI-thread dispatch so signal mutations from the telemetry
		// goroutine are safe with respect to the render loop.
		if dispatcher, ok := app.(interface{ Dispatch(func()) }); ok {
			if bridge, ok := ctrl.telemetryBridge.(*SimpleTelemetryBridge); ok {
				bridge.SetDispatch(dispatcher.Dispatch)
			}
		}

		// Create and subscribe diagnostics collector
		ctrl.diagnostics = diagnostics.NewCollector()
		ctrl.diagnostics.Subscribe(cfg.Telemetry)
		app.SetDiagnostics(ctrl.diagnostics)
	}

	// Set up callbacks
	app.SetCallbacks(
		ctrl.handleSubmit,
		ctrl.handleFileSelect,
		ctrl.handleShellCmd,
	)
	app.SetSessionCallbacks(
		ctrl.nextSession,
		ctrl.prevSession,
	)

	ctrl.loadApprovalAllowRules()
	ctrl.initApprovalObserver()

	return ctrl, nil
}
