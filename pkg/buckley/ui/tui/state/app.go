package state

import (
	buckleywidgets "github.com/odvcencio/buckley/pkg/buckley/ui/widgets"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/state"
	"github.com/odvcencio/fluffyui/toast"
)

// AppState holds TUI state as reactive signals.
type AppState struct {
	// Status
	StatusText     *state.Signal[string]
	StatusOverride *state.Signal[string]
	StatusMode     *state.Signal[string]
	StatusTokens   *state.Signal[int]
	StatusCost     *state.Signal[float64]

	// Context
	ContextUsed   *state.Signal[int]
	ContextBudget *state.Signal[int]
	ContextWindow *state.Signal[int]
	ScrollPos     *state.Signal[string]

	// Progress + Toasts
	ProgressItems *state.Signal[[]progress.Progress]
	Toasts        *state.Signal[[]*toast.Toast]

	// Streaming
	IsStreaming *state.Signal[bool]

	// UI Settings
	ThemeName      *state.Signal[string]
	StylesheetPath *state.Signal[string]
	ReduceMotion   *state.Signal[bool]
	HighContrast   *state.Signal[bool]
	EffectsEnabled *state.Signal[bool]

	// Chat
	ChatMessages        *state.Signal[[]buckleywidgets.ChatMessage]
	ChatThinking        *state.Signal[bool]
	ReasoningText       *state.Signal[string]
	ReasoningPreview    *state.Signal[string]
	ReasoningVisible    *state.Signal[bool]
	MessageMetadata     *state.Signal[string]
	ChatSearchQuery     *state.Signal[string]
	ChatSearchMatches   *state.Signal[buckleywidgets.SearchMatchState]
	ChatSelectionText   *state.Signal[string]
	ChatSelectionActive *state.Signal[bool]

	// Input
	InputText *state.Signal[string]
	InputMode *state.Signal[buckleywidgets.InputMode]

	// Sidebar
	SidebarCurrentTask        *state.Signal[string]
	SidebarTaskProgress       *state.Signal[int]
	SidebarPlanTasks          *state.Signal[[]buckleywidgets.PlanTask]
	SidebarRunningTools       *state.Signal[[]buckleywidgets.RunningTool]
	SidebarToolHistory        *state.Signal[[]buckleywidgets.ToolHistoryEntry]
	SidebarActiveTouches      *state.Signal[[]buckleywidgets.TouchSummary]
	SidebarRecentFiles        *state.Signal[[]string]
	SidebarRLMStatus          *state.Signal[*buckleywidgets.RLMStatus]
	SidebarRLMScratchpad      *state.Signal[[]buckleywidgets.RLMScratchpadEntry]
	SidebarCircuitStatus      *state.Signal[*buckleywidgets.CircuitStatus]
	SidebarExperiment         *state.Signal[string]
	SidebarExperimentStatus   *state.Signal[string]
	SidebarExperimentVariants *state.Signal[[]buckleywidgets.ExperimentVariant]
	SidebarVisible            *state.Signal[bool]
	SidebarWidth              *state.Signal[int]
	SidebarTabIndex           *state.Signal[int]
	SidebarProjectPath        *state.Signal[string]
	SidebarShowCurrentTask    *state.Signal[bool]
	SidebarShowPlan           *state.Signal[bool]
	SidebarShowTools          *state.Signal[bool]
	SidebarShowContext        *state.Signal[bool]
	SidebarShowTouches        *state.Signal[bool]
	SidebarShowRecentFiles    *state.Signal[bool]
	SidebarShowExperiment     *state.Signal[bool]
	SidebarShowRLM            *state.Signal[bool]
	SidebarShowCircuit        *state.Signal[bool]

	// Header
	ModelName *state.Signal[string]
	SessionID *state.Signal[string]

	// Sidebar visibility for machine panels
	SidebarShowAgents *state.Signal[bool]
	SidebarShowLocks  *state.Signal[bool]

	// Machine
	MachineAgents   *state.Signal[[]buckleywidgets.AgentSummary]
	MachineFileLocks *state.Signal[[]buckleywidgets.FileLockSummary]
	MachineModality *state.Signal[string]
}

// NewAppState creates a new application state with defaults.
func NewAppState() *AppState {
	return &AppState{
		StatusText:                state.NewSignal("Ready"),
		StatusOverride:            state.NewSignal(""),
		StatusMode:                state.NewSignal(""),
		StatusTokens:              state.NewSignal(0),
		StatusCost:                state.NewSignal(0.0),
		ContextUsed:               state.NewSignal(0),
		ContextBudget:             state.NewSignal(0),
		ContextWindow:             state.NewSignal(0),
		ScrollPos:                 state.NewSignal(""),
		ProgressItems:             state.NewSignal([]progress.Progress{}),
		Toasts:                    state.NewSignal([]*toast.Toast{}),
		IsStreaming:               state.NewSignal(false),
		ThemeName:                 state.NewSignal("dark"),
		StylesheetPath:            state.NewSignal(""),
		ReduceMotion:              state.NewSignal(false),
		HighContrast:              state.NewSignal(false),
		EffectsEnabled:            state.NewSignal(true),
		ChatMessages:              state.NewSignal([]buckleywidgets.ChatMessage{}),
		ChatThinking:              state.NewSignal(false),
		ReasoningText:             state.NewSignal(""),
		ReasoningPreview:          state.NewSignal(""),
		ReasoningVisible:          state.NewSignal(false),
		MessageMetadata:           state.NewSignal("always"),
		ChatSearchQuery:           state.NewSignal(""),
		ChatSearchMatches:         state.NewSignal(buckleywidgets.SearchMatchState{}),
		ChatSelectionText:         state.NewSignal(""),
		ChatSelectionActive:       state.NewSignal(false),
		InputText:                 state.NewSignal(""),
		InputMode:                 state.NewSignal(buckleywidgets.ModeNormal),
		SidebarCurrentTask:        state.NewSignal(""),
		SidebarTaskProgress:       state.NewSignal(0),
		SidebarPlanTasks:          state.NewSignal([]buckleywidgets.PlanTask(nil)),
		SidebarRunningTools:       state.NewSignal([]buckleywidgets.RunningTool(nil)),
		SidebarToolHistory:        state.NewSignal([]buckleywidgets.ToolHistoryEntry(nil)),
		SidebarActiveTouches:      state.NewSignal([]buckleywidgets.TouchSummary(nil)),
		SidebarRecentFiles:        state.NewSignal([]string(nil)),
		SidebarRLMStatus:          state.NewSignal[*buckleywidgets.RLMStatus](nil),
		SidebarRLMScratchpad:      state.NewSignal([]buckleywidgets.RLMScratchpadEntry(nil)),
		SidebarCircuitStatus:      state.NewSignal[*buckleywidgets.CircuitStatus](nil),
		SidebarExperiment:         state.NewSignal(""),
		SidebarExperimentStatus:   state.NewSignal(""),
		SidebarExperimentVariants: state.NewSignal([]buckleywidgets.ExperimentVariant(nil)),
		SidebarVisible:            state.NewSignal(true),
		SidebarWidth:              state.NewSignal(buckleywidgets.DefaultSidebarConfig().Width),
		SidebarTabIndex:           state.NewSignal(0),
		SidebarProjectPath:        state.NewSignal(""),
		SidebarShowCurrentTask:    state.NewSignal(true),
		SidebarShowPlan:           state.NewSignal(true),
		SidebarShowTools:          state.NewSignal(true),
		SidebarShowContext:        state.NewSignal(true),
		SidebarShowTouches:        state.NewSignal(true),
		SidebarShowRecentFiles:    state.NewSignal(true),
		SidebarShowExperiment:     state.NewSignal(true),
		SidebarShowRLM:            state.NewSignal(true),
		SidebarShowCircuit:        state.NewSignal(true),
		SidebarShowAgents:         state.NewSignal(true),
		SidebarShowLocks:          state.NewSignal(true),
		ModelName:                 state.NewSignal(""),
		SessionID:                 state.NewSignal(""),
		MachineAgents:             state.NewSignal([]buckleywidgets.AgentSummary(nil)),
		MachineFileLocks:          state.NewSignal([]buckleywidgets.FileLockSummary(nil)),
		MachineModality:           state.NewSignal("classic"),
	}
}
