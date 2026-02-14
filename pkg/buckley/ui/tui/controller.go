// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"context"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	projectcontext "github.com/odvcencio/buckley/pkg/context"
	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/cost"
	"github.com/odvcencio/buckley/pkg/diagnostics"
	"github.com/odvcencio/buckley/pkg/execution"
	"github.com/odvcencio/buckley/pkg/mission"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/skill"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/fluffyui/progress"
	"github.com/odvcencio/fluffyui/toast"
)

// Controller connects the TUI to Buckley's backend services.
type Controller struct {
	mu sync.Mutex

	// App is the TUI application
	app App

	// Backend services
	cfg          *config.Config
	modelMgr     *model.Manager
	store        *storage.Store
	projectCtx   *projectcontext.ProjectContext
	registry     *tool.Registry
	conversation *conversation.Conversation
	telemetry    *telemetry.Hub
	progressMgr  *progress.ProgressManager
	toastMgr     *toast.ToastManager
	budgetAlerts *cost.BudgetNotifier
	costTrackers map[string]*cost.Tracker

	// Execution strategy for tool calling (classic, rlm)
	execStrategy    execution.ExecutionStrategy
	strategyFactory execution.StrategyFactory

	// Event bridge for sidebar updates
	telemetryBridge TelemetryBridge

	// Backend diagnostics collector
	diagnostics *diagnostics.Collector

	// State
	workDir string

	approvalMu      sync.Mutex
	approvalSeen    map[string]bool
	approvalAllowMu sync.Mutex
	approvalAllow   []approvalAllowRule

	missionStore *mission.Store
	missionMu    sync.Mutex
	missionSeen  map[string]bool

	ctx    context.Context
	cancel context.CancelFunc

	// Multi-session support - each session runs independently
	sessions       []*SessionState // Active sessions for this project
	currentSession int             // Index into sessions
}

// QueuedMessage represents a user message queued during streaming.
type QueuedMessage struct {
	Content      string
	Timestamp    time.Time
	Acknowledged bool
	Attachments  []string
}

// SessionState holds the state for a single session.
type SessionState struct {
	ID                 string
	Conversation       *conversation.Conversation
	ToolRegistry       *tool.Registry
	SkillRegistry      *skill.Registry
	SkillState         *skill.RuntimeState
	Compactor          *conversation.CompactionManager
	Streaming          bool
	Cancel             context.CancelFunc
	MessageQueue       []QueuedMessage // Messages queued while streaming
	PendingAttachments []string
}

// ControllerConfig configures the controller.
type ControllerConfig struct {
	Config       *config.Config
	ModelManager *model.Manager
	Store        *storage.Store
	ProjectCtx   *projectcontext.ProjectContext
	Telemetry    *telemetry.Hub
	SessionID    string // Resume session, empty for new
	AgentSocket  string // unix:/path or tcp:host:port for agent API
	Context      context.Context
}
