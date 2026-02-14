package tui

import (
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/buckley/ui/filepicker"
	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/fluffyui/markdown"
	"github.com/odvcencio/fluffyui/runtime"
)

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
			ActiveAgents:       r.state.MachineAgents,
			FileLocks:          r.state.MachineFileLocks,
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
			ShowAgents:         r.state.SidebarShowAgents,
			ShowLocks:          r.state.SidebarShowLocks,
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
		// Check for modality prefix commands
		if modality, rest, ok := parseModalityPrefix(text); ok {
			r.setModality(modality)
			text = strings.TrimSpace(rest)
			if text == "" {
				r.inputArea.Clear()
				return
			}
		}
		if r.onSubmit != nil {
			r.onSubmit(text)
		}
	}

	r.inputArea.Clear()
}

// parseModalityPrefix checks for /classic, /rlm, /ralph prefixes.
// Returns (modality, remaining text, matched).
func parseModalityPrefix(text string) (string, string, bool) {
	lower := strings.ToLower(text)
	for _, prefix := range []string{"/classic", "/rlm", "/ralph"} {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		rest := text[len(prefix):]
		if rest == "" || rest[0] == ' ' {
			modality := prefix[1:] // strip leading /
			return modality, rest, true
		}
	}
	return "", text, false
}

// setModality updates the machine modality signal and shows a status override.
func (r *Runner) setModality(modality string) {
	if r.state != nil && r.state.MachineModality != nil {
		r.state.MachineModality.Set(modality)
	}
	if r.statusService != nil {
		r.statusService.SetStatusOverride("Mode: "+modality, 2*time.Second)
	}
}
