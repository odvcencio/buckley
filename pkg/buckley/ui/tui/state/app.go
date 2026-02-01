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

	// Chat
	ChatMessages     *state.Signal[[]buckleywidgets.ChatMessage]
	ChatThinking     *state.Signal[bool]
	ReasoningText    *state.Signal[string]
	ReasoningPreview *state.Signal[string]
	ReasoningVisible *state.Signal[bool]

	// Input
	InputText *state.Signal[string]
	InputMode *state.Signal[buckleywidgets.InputMode]

	// Sidebar
	SidebarState           *state.Signal[buckleywidgets.SidebarState]
	SidebarVisible         *state.Signal[bool]
	SidebarWidth           *state.Signal[int]
	SidebarTabIndex        *state.Signal[int]
	SidebarShowCurrentTask *state.Signal[bool]
	SidebarShowPlan        *state.Signal[bool]
	SidebarShowTools       *state.Signal[bool]
	SidebarShowContext     *state.Signal[bool]
	SidebarShowTouches     *state.Signal[bool]
	SidebarShowRecentFiles *state.Signal[bool]
	SidebarShowExperiment  *state.Signal[bool]
	SidebarShowRLM         *state.Signal[bool]
	SidebarShowCircuit     *state.Signal[bool]

	// Header
	ModelName *state.Signal[string]
	SessionID *state.Signal[string]
}

// NewAppState creates a new application state with defaults.
func NewAppState() *AppState {
	return &AppState{
		StatusText:             state.NewSignal("Ready"),
		StatusOverride:         state.NewSignal(""),
		StatusMode:             state.NewSignal(""),
		StatusTokens:           state.NewSignal(0),
		StatusCost:             state.NewSignal(0.0),
		ContextUsed:            state.NewSignal(0),
		ContextBudget:          state.NewSignal(0),
		ContextWindow:          state.NewSignal(0),
		ScrollPos:              state.NewSignal(""),
		ProgressItems:          state.NewSignal([]progress.Progress{}),
		Toasts:                 state.NewSignal([]*toast.Toast{}),
		IsStreaming:            state.NewSignal(false),
		ChatMessages:           state.NewSignal([]buckleywidgets.ChatMessage{}),
		ChatThinking:           state.NewSignal(false),
		ReasoningText:          state.NewSignal(""),
		ReasoningPreview:       state.NewSignal(""),
		ReasoningVisible:       state.NewSignal(false),
		InputText:              state.NewSignal(""),
		InputMode:              state.NewSignal(buckleywidgets.ModeNormal),
		SidebarState:           state.NewSignal(buckleywidgets.SidebarState{}),
		SidebarVisible:         state.NewSignal(true),
		SidebarWidth:           state.NewSignal(buckleywidgets.DefaultSidebarConfig().Width),
		SidebarTabIndex:        state.NewSignal(0),
		SidebarShowCurrentTask: state.NewSignal(true),
		SidebarShowPlan:        state.NewSignal(true),
		SidebarShowTools:       state.NewSignal(true),
		SidebarShowContext:     state.NewSignal(true),
		SidebarShowTouches:     state.NewSignal(true),
		SidebarShowRecentFiles: state.NewSignal(true),
		SidebarShowExperiment:  state.NewSignal(true),
		SidebarShowRLM:         state.NewSignal(true),
		SidebarShowCircuit:     state.NewSignal(true),
		ModelName:              state.NewSignal(""),
		SessionID:              state.NewSignal(""),
	}
}
