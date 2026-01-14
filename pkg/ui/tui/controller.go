// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	projectcontext "github.com/odvcencio/buckley/pkg/context"
	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/cost"
	"github.com/odvcencio/buckley/pkg/embeddings"
	"github.com/odvcencio/buckley/pkg/diagnostics"
	"github.com/odvcencio/buckley/pkg/execution"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/session"
	"github.com/odvcencio/buckley/pkg/skill"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/telemetry"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
	"github.com/odvcencio/buckley/pkg/ui/progress"
	"github.com/odvcencio/buckley/pkg/ui/theme"
	"github.com/odvcencio/buckley/pkg/ui/toast"
	"github.com/odvcencio/buckley/pkg/ui/widgets"
	"gopkg.in/yaml.v3"
)

// Controller connects the TUI to Buckley's backend services.
type Controller struct {
	mu sync.Mutex

	// App is the TUI application
	app *WidgetApp

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
	telemetryBridge *TelemetryUIBridge

	// Backend diagnostics collector
	diagnostics *diagnostics.Collector

	// State
	workDir string

	// Multi-session support - each session runs independently
	sessions       []*SessionState // Active sessions for this project
	currentSession int             // Index into sessions
}

// QueuedMessage represents a user message queued during streaming.
type QueuedMessage struct {
	Content      string
	Timestamp    time.Time
	Acknowledged bool
}

// SessionState holds the state for a single session.
type SessionState struct {
	ID            string
	Conversation  *conversation.Conversation
	ToolRegistry  *tool.Registry
	SkillRegistry *skill.Registry
	SkillState    *skill.RuntimeState
	Compactor     *conversation.CompactionManager
	Streaming     bool
	Cancel        context.CancelFunc
	MessageQueue  []QueuedMessage // Messages queued while streaming
}

// ControllerConfig configures the controller.
type ControllerConfig struct {
	Config       *config.Config
	ModelManager *model.Manager
	Store        *storage.Store
	ProjectCtx   *projectcontext.ProjectContext
	Telemetry    *telemetry.Hub
	SessionID    string // Resume session, empty for new
}

func newSessionState(cfg *config.Config, store *storage.Store, workDir string, hub *telemetry.Hub, modelMgr *model.Manager, sessionID string, loadMessages bool, progressMgr *progress.ProgressManager, toastMgr *toast.ToastManager) (*SessionState, error) {
	sess := &SessionState{
		ID:           sessionID,
		Conversation: conversation.New(sessionID),
	}

	if loadMessages && store != nil {
		if msgs, err := store.GetMessages(sessionID, 1000, 0); err == nil {
			for _, msg := range msgs {
				content := msg.Content
				if msg.ContentJSON != "" {
					content = msg.ContentJSON
				}
				switch msg.Role {
				case "user":
					sess.Conversation.AddUserMessage(content)
				case "assistant":
					sess.Conversation.AddAssistantMessage(content)
				}
			}
		}
	}

	skills := skill.NewRegistry()
	if err := skills.LoadAll(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load skills: %v\n", err)
	}

	skillState := skill.NewRuntimeState(sess.Conversation.AddSystemMessage)
	registry := buildRegistry(cfg, store, workDir, hub, sessionID, progressMgr, toastMgr)
	registry.Register(&builtin.SkillActivationTool{
		Registry:     skills,
		Conversation: skillState,
	})
	createTool := &builtin.CreateSkillTool{Registry: skills}
	if strings.TrimSpace(workDir) != "" {
		createTool.SetWorkDir(workDir)
	}
	registry.Register(createTool)

	sess.ToolRegistry = registry
	sess.SkillRegistry = skills
	sess.SkillState = skillState
	compactor := conversation.NewCompactionManager(modelMgr, cfg)
	compactor.SetConversation(sess.Conversation)
	if store != nil {
		compactor.SetOnComplete(func(_ *conversation.CompactionResult) {
			if err := sess.Conversation.SaveAllMessages(store); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to persist compaction: %v\n", err)
			}
		})
	}
	sess.Compactor = compactor
	if sess.ToolRegistry != nil {
		sess.ToolRegistry.SetCompactionManager(compactor)
	}

	return sess, nil
}

// NewController creates a new TUI controller.
func NewController(cfg ControllerConfig) (*Controller, error) {
	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}

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

	// Collect all active sessions for this project and load their messages
	var projectSessions []*SessionState
	allSessions, _ := cfg.Store.ListSessions(100)
	for _, s := range allSessions {
		if s.ProjectPath == workDir && s.Status == storage.SessionStatusActive {
			sess, err := newSessionState(cfg.Config, cfg.Store, workDir, cfg.Telemetry, cfg.ModelManager, s.ID, true, progressMgr, toastMgr)
			if err != nil {
				return nil, err
			}
			projectSessions = append(projectSessions, sess)
		}
	}

	// Get or create session
	sessionID := cfg.SessionID
	currentIdx := 0
	if sessionID == "" {
		if len(projectSessions) > 0 {
			// Resume most recent active session - sessionID is available via projectSessions[0].ID
			// currentIdx is already 0
		} else {
			// Create a new session
			baseID := session.DetermineSessionID(workDir)
			timestamp := time.Now().Format("0102-150405") // MMDD-HHMMSS
			sessionID = fmt.Sprintf("%s-%s", baseID, timestamp)

			now := time.Now()
			sess := &storage.Session{
				ID:          sessionID,
				ProjectPath: workDir,
				CreatedAt:   now,
				LastActive:  now,
				Status:      storage.SessionStatusActive,
			}
			if err := cfg.Store.CreateSession(sess); err != nil {
				return nil, fmt.Errorf("create session: %w", err)
			}
			sessState, err := newSessionState(cfg.Config, cfg.Store, workDir, cfg.Telemetry, cfg.ModelManager, sessionID, false, progressMgr, toastMgr)
			if err != nil {
				return nil, err
			}
			projectSessions = []*SessionState{sessState}
		}
	} else {
		// Find index of specified session
		found := false
		for i, s := range projectSessions {
			if s.ID == sessionID {
				currentIdx = i
				found = true
				break
			}
		}
		if !found && len(projectSessions) == 0 {
			now := time.Now()
			sess := &storage.Session{
				ID:          sessionID,
				ProjectPath: workDir,
				CreatedAt:   now,
				LastActive:  now,
				Status:      storage.SessionStatusActive,
			}
			if err := cfg.Store.CreateSession(sess); err != nil {
				return nil, fmt.Errorf("create session: %w", err)
			}
			sessState, err := newSessionState(cfg.Config, cfg.Store, workDir, cfg.Telemetry, cfg.ModelManager, sessionID, false, progressMgr, toastMgr)
			if err != nil {
				return nil, err
			}
			projectSessions = []*SessionState{sessState}
			currentIdx = 0
		}
	}

	// Determine project root
	projectRoot := workDir

	// Create TUI app
	app, err := NewWidgetApp(WidgetAppConfig{
		Theme:       theme.DefaultTheme(),
		ModelName:   cfg.Config.Models.Execution,
		WorkDir:     workDir,
		ProjectRoot: projectRoot,
	})
	if err != nil {
		return nil, fmt.Errorf("create TUI app: %w", err)
	}

	ctrl := &Controller{
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
	}

	// Initialize execution strategy based on config
	execMode := config.DefaultExecutionMode
	if cfg.Config != nil {
		execMode = cfg.Config.ExecutionMode()
	}
	strategyFactory := execution.NewFactory(
		cfg.ModelManager,
		projectSessions[currentIdx].ToolRegistry,
		cfg.Store,
		cfg.Telemetry,
		execution.FactoryConfig{
			DefaultMaxIterations: 25,
			ConfidenceThreshold:  0.7,
			UseTOON:              cfg.Config != nil && cfg.Config.Encoding.UseToon,
			EnableReasoning:      true,
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
	if setter, ok := ctrl.execStrategy.(interface {
		SetProgressManager(*progress.ProgressManager)
	}); ok {
		setter.SetProgressManager(progressMgr)
	}
	if setter, ok := ctrl.execStrategy.(interface{ SetToastManager(*toast.ToastManager) }); ok {
		setter.SetToastManager(toastMgr)
	}
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
		ctrl.telemetryBridge = NewTelemetryUIBridge(cfg.Telemetry, app)

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

	return ctrl, nil
}

// Run starts the TUI controller.
func (c *Controller) Run() error {
	// Start telemetry bridge for sidebar updates
	if c.telemetryBridge != nil {
		c.telemetryBridge.Start(context.Background())
	}

	// Show welcome
	c.app.WelcomeScreen()

	// Add system context if available
	if c.projectCtx != nil && c.projectCtx.Loaded {
		c.app.AddMessage("Project context loaded from AGENTS.md", "system")
	}

	// Load existing conversation history for current session
	sess := c.sessions[c.currentSession]
	if len(sess.Conversation.Messages) > 0 {
		c.app.AddMessage(fmt.Sprintf("Resuming session: %s (%d messages)", sess.ID, len(sess.Conversation.Messages)), "system")
		for _, msg := range sess.Conversation.Messages {
			content := ""
			if s, ok := msg.Content.(string); ok {
				content = s
			}
			c.app.AddMessage(content, msg.Role)
		}
	}

	c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))

	// Run the app
	return c.app.Run()
}

// handleSubmit processes user input submission.
func (c *Controller) handleSubmit(text string) {
	if text == "" {
		return
	}

	// Handle commands
	if strings.HasPrefix(text, "/") {
		c.handleCommand(text)
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Get current session
	sess := c.sessions[c.currentSession]

	// If session is streaming, queue the message instead of starting new stream
	if sess.Streaming {
		sess.MessageQueue = append(sess.MessageQueue, QueuedMessage{
			Content:   text,
			Timestamp: time.Now(),
		})
		// Show queued message with indicator
		c.app.AddMessage(text+" (queued)", "user")
		c.updateQueueIndicator(sess)
		return
	}

	// Add user message to display
	c.app.AddMessage(text, "user")

	// Create context with cancellation for this session
	ctx, cancel := context.WithCancel(context.Background())
	sess.Cancel = cancel
	sess.Streaming = true
	c.app.SetStreaming(true)
	c.emitStreaming(sess.ID, true)

	// Start streaming response for this session
	go c.streamResponse(ctx, text, sess)
}

// handleCommand processes slash commands.
func (c *Controller) handleCommand(text string) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return
	}

	cmd := strings.ToLower(parts[0])
	args := []string{}
	if len(parts) > 1 {
		args = parts[1:]
	}
	start := time.Now()
	c.emitUICommandEvent(cmd, args, "start", 0)
	defer func() {
		c.emitUICommandEvent(cmd, args, "end", time.Since(start))
	}()

	switch cmd {
	case "/new", "/clear", "/reset":
		c.newSession()

	case "/sessions":
		if len(args) > 0 {
			c.handleSessionsCommand(args)
			return
		}
		c.listSessions()

	case "/tabs":
		c.listSessions()

	case "/next", "/n":
		c.nextSession()

	case "/prev", "/p":
		c.prevSession()

	case "/model", "/models":
		if len(parts) > 1 {
			sub := strings.ToLower(parts[1])
			if sub == "curate" || sub == "curated" {
				c.handleModelCurate(parts[2:])
				return
			}
			modelID := strings.TrimSpace(strings.Join(parts[1:], " "))
			c.setExecutionModel(modelID)
		} else {
			c.showModelPicker()
		}

	case "/mode":
		if len(args) == 0 {
			c.app.AddMessage("Usage: /mode [classic|rlm]", "system")
			return
		}
		if err := c.handleModeCommand(args[0]); err != nil {
			c.app.AddMessage("Failed to switch mode: "+err.Error(), "system")
		}

	case "/help":
		c.app.AddMessage(`Commands:
  /new, /clear, /reset - Start a new session
  /sessions, /tabs     - List active sessions
  /sessions complete   - Mark session completed (soft delete)
  /next, /n            - Switch to next session
  /prev, /p            - Switch to previous session
  /model [id]          - Pick or set the execution model
  /mode [classic|rlm]   - Switch execution mode
  /model curate        - Curate models for ACP/editor pickers
  /skill [name|list]   - List or activate a skill
  /context             - Show context budget details
  /search [query]      - Search conversation history
  /export [options]    - Export current session
  /import [file]       - Import conversation file
  /review              - Review current git diff
  /commit              - Generate commit message for staged changes
  /help                - Show this help
  /quit, /exit         - Exit Buckley

Shortcuts: Alt+Right (next), Alt+Left (prev), Ctrl+F (search)`, "system")

	case "/quit", "/exit":
		c.app.Quit()

	case "/review":
		c.handleReview()

	case "/commit":
		c.handleCommit()

	case "/skill", "/skills":
		c.handleSkillCommand(parts[1:])

	case "/context":
		c.showContextBudget()

	case "/search":
		c.handleSearchCommand(args)

	case "/export":
		c.handleExportCommand(args)

	case "/import":
		c.handleImportCommand(args)

	default:
		c.app.AddMessage("Unknown command: "+cmd+". Type /help for available commands.", "system")
	}
}

func (c *Controller) showModelPickerLocked() {
	items, _ := c.collectModelPickerItemsLocked(nil)
	if len(items) == 0 {
		return
	}

	c.app.ShowModelPicker(items, func(item widgets.PaletteItem) {
		modelID := item.ID
		if id, ok := item.Data.(string); ok && strings.TrimSpace(id) != "" {
			modelID = id
		}
		c.setExecutionModel(modelID)
	})
}

func (c *Controller) showModelPicker() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.showModelPickerLocked()
}

func (c *Controller) collectModelPickerItemsLocked(curated map[string]struct{}) ([]widgets.PaletteItem, map[string]model.ModelInfo) {
	if c.modelMgr == nil {
		c.app.AddMessage("Model catalog unavailable in this session.", "system")
		return nil, nil
	}

	catalog := c.modelMgr.GetCatalog()
	if catalog == nil || len(catalog.Data) == 0 {
		c.app.AddMessage("No models available from configured providers.", "system")
		return nil, nil
	}

	execID := strings.TrimSpace(c.cfg.Models.Execution)
	planID := strings.TrimSpace(c.cfg.Models.Planning)
	reviewID := strings.TrimSpace(c.cfg.Models.Review)

	catalogIndex := make(map[string]model.ModelInfo, len(catalog.Data))
	grouped := make(map[string][]model.ModelInfo)
	for _, info := range catalog.Data {
		catalogIndex[info.ID] = info
		group := modelGroupKey(info.ID, c.modelMgr)
		grouped[group] = append(grouped[group], info)
	}

	groups := make([]string, 0, len(grouped))
	for group := range grouped {
		groups = append(groups, group)
	}
	sort.Strings(groups)

	items := make([]widgets.PaletteItem, 0, len(catalog.Data))
	pinnedIDs := preferredModelIDs(execID, planID, reviewID, catalogIndex)
	pinnedSet := make(map[string]struct{}, len(pinnedIDs))
	if len(pinnedIDs) > 0 {
		for _, modelID := range pinnedIDs {
			info, ok := catalogIndex[modelID]
			if !ok {
				continue
			}
			pinnedSet[modelID] = struct{}{}
			tags := modelRoleTags(modelID, execID, planID, reviewID)
			tags = appendModelTag(tags, curated, modelID)
			items = append(items, widgets.PaletteItem{
				ID:          modelID,
				Category:    "Pinned",
				Label:       "  " + modelID,
				Description: info.ID,
				Shortcut:    strings.Join(tags, ","),
				Data:        modelID,
			})
		}
	}
	for _, group := range groups {
		models := grouped[group]
		sort.Slice(models, func(i, j int) bool {
			return models[i].ID < models[j].ID
		})

		for _, info := range models {
			if _, ok := pinnedSet[info.ID]; ok {
				continue
			}
			label := modelLabel(info.ID, group)
			tags := modelRoleTags(info.ID, execID, planID, reviewID)
			tags = appendModelTag(tags, curated, info.ID)
			items = append(items, widgets.PaletteItem{
				ID:          info.ID,
				Category:    group,
				Label:       "  " + label,
				Description: info.ID,
				Shortcut:    strings.Join(tags, ","),
				Data:        info.ID,
			})
		}
	}

	return items, catalogIndex
}

func (c *Controller) handleModelCurate(args []string) {
	if len(args) == 0 {
		c.showModelCuratePicker()
		return
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "list":
		c.showCuratedModels()
	case "clear":
		c.mu.Lock()
		c.cfg.Models.Curated = nil
		c.mu.Unlock()
		c.app.AddMessage("Cleared curated models. Use /model curate save to persist.", "system")
	case "save":
		target := "project"
		if len(args) > 1 {
			target = strings.ToLower(args[1])
		}
		c.saveCuratedModels(target)
	case "add":
		modelID := strings.TrimSpace(strings.Join(args[1:], " "))
		if modelID == "" {
			c.app.AddMessage("Model ID required. Usage: /model curate add <id>", "system")
			return
		}
		c.addCuratedModel(modelID)
	case "remove", "rm":
		modelID := strings.TrimSpace(strings.Join(args[1:], " "))
		if modelID == "" {
			c.app.AddMessage("Model ID required. Usage: /model curate remove <id>", "system")
			return
		}
		c.removeCuratedModel(modelID)
	default:
		c.app.AddMessage("Usage: /model curate [list|add|remove|clear|save]", "system")
	}
}

func (c *Controller) showModelCuratePickerLocked() {
	curatedSet := curatedModelSet(c.cfg.Models.Curated)
	items, _ := c.collectModelPickerItemsLocked(curatedSet)
	if len(items) == 0 {
		return
	}

	c.app.ShowModelPicker(items, func(item widgets.PaletteItem) {
		modelID := item.ID
		if id, ok := item.Data.(string); ok && strings.TrimSpace(id) != "" {
			modelID = id
		}
		changed := c.toggleCuratedModel(modelID)
		if changed {
			c.app.AddMessage("Curated models updated. Use /model curate save to persist.", "system")
		}
	})
}

func (c *Controller) showModelCuratePicker() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.showModelCuratePickerLocked()
}

func (c *Controller) toggleCuratedModel(modelID string) bool {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return false
	}
	if c.modelMgr != nil && !catalogHasModel(c.modelMgr, modelID) {
		c.app.AddMessage("Model not found in catalog: "+modelID, "system")
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	curated := append([]string{}, c.cfg.Models.Curated...)
	for i, id := range curated {
		if id == modelID {
			curated = append(curated[:i], curated[i+1:]...)
			c.cfg.Models.Curated = curated
			return true
		}
	}
	curated = append(curated, modelID)
	c.cfg.Models.Curated = curated
	return true
}

func (c *Controller) addCuratedModel(modelID string) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return
	}
	if c.modelMgr != nil && !catalogHasModel(c.modelMgr, modelID) {
		c.app.AddMessage("Model not found in catalog: "+modelID, "system")
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, id := range c.cfg.Models.Curated {
		if id == modelID {
			c.app.AddMessage("Model already in curated list: "+modelID, "system")
			return
		}
	}
	c.cfg.Models.Curated = append(c.cfg.Models.Curated, modelID)
	c.app.AddMessage("Added model to curated list. Use /model curate save to persist.", "system")
}

func (c *Controller) removeCuratedModel(modelID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	curated := append([]string{}, c.cfg.Models.Curated...)
	for i, id := range curated {
		if id == modelID {
			curated = append(curated[:i], curated[i+1:]...)
			c.cfg.Models.Curated = curated
			c.app.AddMessage("Removed model from curated list. Use /model curate save to persist.", "system")
			return
		}
	}
	c.app.AddMessage("Model not in curated list: "+modelID, "system")
}

func (c *Controller) showCuratedModelsLocked() {
	curated := append([]string{}, c.cfg.Models.Curated...)

	if len(curated) == 0 {
		c.app.AddMessage("Curated list is empty. ACP will use execution/planning/review defaults.", "system")
		return
	}
	c.app.AddMessage("Curated models:\n- "+strings.Join(curated, "\n- "), "system")
}

func (c *Controller) showCuratedModels() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.showCuratedModelsLocked()
}

func (c *Controller) saveCuratedModels(target string) {
	c.mu.Lock()
	curated := append([]string{}, c.cfg.Models.Curated...)
	c.mu.Unlock()

	path, err := curatedConfigPath(c.workDir, target)
	if err != nil {
		c.app.AddMessage("Could not resolve config path: "+err.Error(), "system")
		return
	}
	if err := writeCuratedModels(path, curated); err != nil {
		c.app.AddMessage("Failed to write curated models: "+err.Error(), "system")
		return
	}
	c.app.AddMessage("Saved curated models to "+path, "system")
}

func curatedConfigPath(workDir, target string) (string, error) {
	target = strings.TrimSpace(strings.ToLower(target))
	switch target {
	case "", "project":
		return filepath.Join(workDir, ".buckley", "config.yaml"), nil
	case "user", "global":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".buckley", "config.yaml"), nil
	default:
		return "", fmt.Errorf("unknown target %q (use project or user)", target)
	}
}

func writeCuratedModels(path string, curated []string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	var raw map[string]any
	data, err := os.ReadFile(path)
	if err == nil {
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if raw == nil {
		raw = make(map[string]any)
	}

	modelsRaw, ok := raw["models"].(map[string]any)
	if !ok {
		modelsRaw = make(map[string]any)
	}
	modelsRaw["curated"] = curated
	raw["models"] = modelsRaw

	out, err := yaml.Marshal(raw)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func curatedModelSet(models []string) map[string]struct{} {
	set := make(map[string]struct{}, len(models))
	for _, id := range models {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = struct{}{}
		}
	}
	return set
}

func appendModelTag(tags []string, curated map[string]struct{}, modelID string) []string {
	if curated == nil {
		return tags
	}
	if _, ok := curated[modelID]; ok {
		if len(tags) == 0 {
			return []string{"curated"}
		}
		return append(tags, "curated")
	}
	return tags
}

func (c *Controller) setExecutionModel(modelID string) {
	c.mu.Lock()
	c.setExecutionModelLocked(modelID)
	sess := (*SessionState)(nil)
	if len(c.sessions) > 0 && c.currentSession >= 0 && c.currentSession < len(c.sessions) {
		sess = c.sessions[c.currentSession]
	}
	c.mu.Unlock()
	if sess != nil {
		c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))
	}
}

func (c *Controller) setExecutionModelLocked(modelID string) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		c.app.AddMessage("Model ID required. Try /model to open the picker.", "system")
		return
	}

	if c.modelMgr != nil && !catalogHasModel(c.modelMgr, modelID) {
		c.app.AddMessage("Model not found in catalog: "+modelID, "system")
		return
	}

	c.cfg.Models.Execution = modelID
	c.app.SetModelName(modelID)
	c.app.AddMessage("Execution model set to "+modelID, "system")
}

func (c *Controller) handleModeCommand(mode string) error {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return fmt.Errorf("mode required")
	}
	if c.strategyFactory == nil {
		return fmt.Errorf("execution strategy factory unavailable")
	}
	strategy, err := c.strategyFactory.Create(mode)
	if err != nil {
		return err
	}
	c.execStrategy = strategy
	if c.execStrategy != nil {
		c.app.SetExecutionMode(c.execStrategy.Name())
		if c.cfg != nil {
			c.cfg.Execution.Mode = c.execStrategy.Name()
		}
	}
	if setter, ok := c.execStrategy.(interface {
		SetProgressManager(*progress.ProgressManager)
	}); ok {
		setter.SetProgressManager(c.progressMgr)
	}
	if setter, ok := c.execStrategy.(interface{ SetToastManager(*toast.ToastManager) }); ok {
		setter.SetToastManager(c.toastMgr)
	}
	c.app.AddMessage(fmt.Sprintf("Switched to %s mode", c.execStrategy.Name()), "system")
	return nil
}

func catalogHasModel(mgr *model.Manager, modelID string) bool {
	if mgr == nil {
		return true
	}
	catalog := mgr.GetCatalog()
	if catalog == nil {
		return false
	}
	for _, info := range catalog.Data {
		if info.ID == modelID {
			return true
		}
	}
	return false
}

func modelGroupKey(modelID string, mgr *model.Manager) string {
	if parts := strings.SplitN(modelID, "/", 2); len(parts) == 2 {
		return parts[0]
	}
	if mgr != nil {
		if provider := mgr.ProviderIDForModel(modelID); provider != "" {
			return provider
		}
	}
	return "other"
}

func modelLabel(modelID, group string) string {
	label := modelID
	prefix := group + "/"
	if group != "" && group != "other" && strings.HasPrefix(modelID, prefix) {
		label = strings.TrimPrefix(modelID, prefix)
	}
	if strings.TrimSpace(label) == "" {
		return modelID
	}
	return label
}

func modelRoleTags(modelID, execID, planID, reviewID string) []string {
	var tags []string
	if execID != "" && modelID == execID {
		tags = append(tags, "exec")
	}
	if planID != "" && modelID == planID {
		tags = append(tags, "plan")
	}
	if reviewID != "" && modelID == reviewID {
		tags = append(tags, "review")
	}
	return tags
}

func preferredModelIDs(execID, planID, reviewID string, catalog map[string]model.ModelInfo) []string {
	ids := make([]string, 0, 4)
	seen := make(map[string]struct{})
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		if catalog != nil {
			if _, ok := catalog[id]; !ok {
				return
			}
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	add(execID)
	add(planID)
	add(reviewID)
	add("moonshotai/kimi-k2-thinking")
	return ids
}

// newSession creates a new session, clearing the current conversation.
func (c *Controller) newSession() {
	c.mu.Lock()
	var oldSess *SessionState
	if len(c.sessions) > 0 && c.currentSession >= 0 && c.currentSession < len(c.sessions) {
		oldSess = c.sessions[c.currentSession]
	}
	c.mu.Unlock()

	// Mark old session as completed
	if oldSess != nil {
		if oldSess.ID != "" {
			_ = c.store.SetSessionStatus(oldSess.ID, storage.SessionStatusCompleted)
		}
	}

	newSess, err := c.createNewSessionState()
	if err != nil {
		c.app.AddMessage("Error creating session: "+err.Error(), "system")
		return
	}
	c.mu.Lock()
	c.sessions = append([]*SessionState{newSess}, c.sessions...)
	c.currentSession = 0
	c.conversation = newSess.Conversation
	c.registry = newSess.ToolRegistry
	c.mu.Unlock()

	// Clear scrollback and show fresh welcome
	c.app.ClearScrollback()
	c.app.WelcomeScreen()
	c.app.AddMessage("New session started: "+newSess.ID, "system")
	c.app.SetStatus("Ready")
	c.app.SetStreaming(false)
	c.updateContextIndicator(newSess, c.executionModelID(), "", allowedToolsForSession(newSess))
}

func (c *Controller) createNewSessionState() (*SessionState, error) {
	if c.store == nil {
		return nil, fmt.Errorf("session store unavailable")
	}
	baseID := session.DetermineSessionID(c.workDir)
	timestamp := time.Now().Format("0102-150405")
	sessionID := fmt.Sprintf("%s-%s", baseID, timestamp)

	now := time.Now()
	storageSess := &storage.Session{
		ID:          sessionID,
		ProjectPath: c.workDir,
		CreatedAt:   now,
		LastActive:  now,
		Status:      storage.SessionStatusActive,
	}
	if err := c.store.CreateSession(storageSess); err != nil {
		return nil, err
	}

	return newSessionState(c.cfg, c.store, c.workDir, c.telemetry, c.modelMgr, sessionID, false, c.progressMgr, c.toastMgr)
}

// streamResponse handles the AI response streaming for a specific session.
func (c *Controller) streamResponse(ctx context.Context, prompt string, sess *SessionState) {
	defer func() {
		c.mu.Lock()
		sess.Streaming = false
		sess.Cancel = nil
		isCurrent := c.currentSession >= 0 && c.currentSession < len(c.sessions) && c.sessions[c.currentSession] == sess
		c.mu.Unlock()
		c.emitStreaming(sess.ID, false)
		if isCurrent {
			c.app.SetStreaming(false)
		}
	}()

	c.app.SetStatus("Thinking...")
	c.app.ShowThinkingIndicator()

	// Add user message to session's conversation and persist
	sess.Conversation.AddUserMessage(prompt)
	c.saveMessage(sess.ID, "user", prompt)

	modelID := c.executionModelID()
	contextPrompt := ""
	if c.execStrategy != nil {
		contextPrompt = prompt
	}
	c.updateContextIndicator(sess, modelID, contextPrompt, allowedToolsForSession(sess))

	fullResponse, usage, err := c.runToolLoop(ctx, sess, modelID)
	c.app.RemoveThinkingIndicator()
	if err != nil {
		if ctx.Err() == context.Canceled {
			c.app.SetStatus("Cancelled")
			return
		}
		c.app.AddMessage(fmt.Sprintf("Error: %v", err), "system")
		c.app.SetStatus("Error")
		return
	}

	if fullResponse != "" {
		c.app.AddMessage(fullResponse, "assistant")
	}

	// Update token count and cost
	var tokens int
	var costCents float64

	if usage != nil {
		// Use actual usage from API response
		tokens = usage.TotalTokens
		if c.modelMgr != nil {
			if cost, err := c.modelMgr.CalculateCost(modelID, *usage); err == nil {
				costCents = cost * 100 // Convert dollars to cents
			}
		}
		c.recordCost(sess.ID, modelID, usage)
	} else {
		// Fallback: estimate tokens from response length
		tokens = len(fullResponse) / 4
		// Estimate cost using model pricing if available
		if c.modelMgr != nil {
			if cost, err := c.modelMgr.CalculateCostFromTokens(modelID, 0, tokens); err == nil {
				costCents = cost * 100
			}
		}
	}
	c.app.SetTokenCount(tokens, costCents)
	c.updateContextIndicator(sess, modelID, "", allowedToolsForSession(sess))

	// Check for queued messages and process them
	if c.processMessageQueue(sess) {
		// processMessageQueue started a new stream, don't set Ready status
		return
	}

	// Update status only if no more queued messages
	c.app.SetStatus("Ready")
}

func (c *Controller) runToolLoop(ctx context.Context, sess *SessionState, modelID string) (string, *model.Usage, error) {
	if c.modelMgr == nil {
		return "", nil, fmt.Errorf("model manager unavailable")
	}
	if sess == nil || sess.Conversation == nil {
		return "", nil, fmt.Errorf("session unavailable")
	}

	// Use execution strategy if available (the one true path)
	if c.execStrategy != nil {
		return c.runWithStrategy(ctx, sess)
	}

	// Legacy fallback (should not reach here in normal operation)
	useTools := sess.ToolRegistry != nil
	toolChoice := "auto"
	maxIterations := 10
	totalUsage := model.Usage{}

	for iter := 0; iter < maxIterations; iter++ {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}

		allowedTools := []string{}
		if sess.SkillState != nil {
			allowedTools = sess.SkillState.ToolFilter()
		}

		req := model.ChatRequest{
			Model:    modelID,
			Messages: c.buildMessagesForSession(sess, modelID, allowedTools),
		}
		if useTools && sess.ToolRegistry != nil {
			tools := sess.ToolRegistry.ToOpenAIFunctionsFiltered(allowedTools)
			if len(tools) > 0 {
				req.Tools = tools
				req.ToolChoice = toolChoice
			} else {
				useTools = false
			}
		}
		if reasoning := strings.TrimSpace(c.cfg.Models.Reasoning); reasoning != "" && c.modelMgr.SupportsReasoning(modelID) {
			req.Reasoning = &model.ReasoningConfig{Effort: reasoning}
		}

		resp, err := c.modelMgr.ChatCompletion(ctx, req)
		if err != nil {
			if useTools && isToolUnsupportedError(err) {
				useTools = false
				continue
			}
			return "", nil, err
		}
		totalUsage = addUsage(totalUsage, resp.Usage)

		if len(resp.Choices) == 0 {
			return "", nil, fmt.Errorf("no response choices")
		}

		msg := resp.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			text, err := model.ExtractTextContent(msg.Content)
			if err != nil {
				return "", nil, err
			}
			sess.Conversation.AddAssistantMessageWithReasoning(text, msg.Reasoning)
			c.saveMessage(sess.ID, "assistant", text)
			c.warnIfTruncatedResponse(resp.Choices[0].FinishReason)
			return text, &totalUsage, nil
		}

		for i := range msg.ToolCalls {
			if msg.ToolCalls[i].ID == "" {
				msg.ToolCalls[i].ID = fmt.Sprintf("tool-%d", i+1)
			}
		}
		sess.Conversation.AddToolCallMessage(msg.ToolCalls)

		for _, tc := range msg.ToolCalls {
			params, err := parseToolParams(tc.Function.Arguments)
			if err != nil {
				toolText := fmt.Sprintf("Error: invalid tool arguments: %v", err)
				sess.Conversation.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)
				continue
			}
			if sess.ToolRegistry == nil {
				toolText := "Error: tool registry unavailable"
				sess.Conversation.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)
				continue
			}
			if !tool.IsToolAllowed(tc.Function.Name, allowedTools) {
				toolText := fmt.Sprintf("Error: tool %s not allowed by active skills", tc.Function.Name)
				sess.Conversation.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)
				continue
			}
			if params == nil {
				params = make(map[string]any)
			}
			if tc.ID != "" {
				params[tool.ToolCallIDParam] = tc.ID
			}

			result, execErr := sess.ToolRegistry.Execute(tc.Function.Name, params)
			toolText := formatToolResultForModel(result, execErr)
			sess.Conversation.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)

			if display := toolDisplayMessage(tc.Function.Name, result, execErr); display != "" {
				c.app.AddMessage(display, "system")
			}
		}
	}

	return "", &totalUsage, fmt.Errorf("max tool calling iterations (%d) exceeded", maxIterations)
}

// runWithStrategy executes using the configured execution strategy.
// This is the one true path for tool execution.
func (c *Controller) runWithStrategy(ctx context.Context, sess *SessionState) (string, *model.Usage, error) {
	// Get the last user message as the prompt
	prompt := ""
	if sess.Conversation != nil {
		messages := sess.Conversation.Messages
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				if content, ok := messages[i].Content.(string); ok {
					prompt = content
				} else {
					prompt = fmt.Sprintf("%v", messages[i].Content)
				}
				break
			}
		}
	}

	// Build allowed tools from skill state
	var allowedTools []string
	if sess.SkillState != nil {
		allowedTools = sess.SkillState.ToolFilter()
	}

	modelID := c.executionModelID()

	// Build system prompt + trimmed context
	systemPrompt, trimmedConv, budget := c.buildRequestContext(sess, modelID, prompt, allowedTools)

	// Create execution request
	req := execution.ExecutionRequest{
		Prompt:         prompt,
		Conversation:   trimmedConv,
		SessionID:      sess.ID,
		SystemPrompt:   systemPrompt,
		AllowedTools:   allowedTools,
		MaxIterations:  25,
		ContextBuilder: conversation.NewContextBuilder(sess.Compactor),
		ContextBudget:  budget,
	}

	// Set up stream handler for TUI updates
	if runner, ok := c.execStrategy.(interface{ SetStreamHandler(execution.StreamHandler) }); ok {
		runner.SetStreamHandler(&tuiStreamHandler{
			app:  c.app,
			sess: sess,
			ctrl: c,
		})
	}

	// Execute
	result, err := c.execStrategy.Execute(ctx, req)
	if err != nil {
		return "", nil, err
	}

	// Update conversation with result
	if result.Content != "" {
		sess.Conversation.AddAssistantMessageWithReasoning(result.Content, result.Reasoning)
		c.saveMessage(sess.ID, "assistant", result.Content)
	}
	c.warnIfTruncatedResponse(result.FinishReason)

	usage := &model.Usage{
		PromptTokens:     result.Usage.PromptTokens,
		CompletionTokens: result.Usage.CompletionTokens,
		TotalTokens:      result.Usage.TotalTokens,
	}

	return result.Content, usage, nil
}

// tuiStreamHandler bridges execution events to the TUI display.
type tuiStreamHandler struct {
	app  *WidgetApp
	sess *SessionState
	ctrl *Controller
}

func (h *tuiStreamHandler) OnText(text string) {
	// Text is handled in OnComplete
}

func (h *tuiStreamHandler) OnReasoning(reasoning string) {
	// Could display thinking indicator
	h.app.SetStatus("Thinking...")
}

func (h *tuiStreamHandler) OnToolStart(name string, arguments string) {
	h.app.SetStatus(fmt.Sprintf("Running %s...", name))
}

func (h *tuiStreamHandler) OnToolEnd(name string, result string, err error) {
	if err != nil {
		h.app.AddMessage(fmt.Sprintf("Error running %s: %v", name, err), "system")
	}
	// Tool results are handled internally by the strategy
}

func (h *tuiStreamHandler) OnComplete(result *execution.ExecutionResult) {
	h.app.SetStatus("Ready")
}

func parseToolParams(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var params map[string]any
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, err
	}
	if params == nil {
		params = make(map[string]any)
	}
	return params, nil
}

func formatToolResultForModel(result *builtin.Result, execErr error) string {
	if execErr != nil {
		return fmt.Sprintf("Error: %v", execErr)
	}
	if result == nil {
		return "No result"
	}
	encoded, err := tool.ToJSON(result)
	if err != nil {
		return fmt.Sprintf("{\"success\":%t}", result.Success)
	}
	return encoded
}

func toolDisplayMessage(name string, result *builtin.Result, execErr error) string {
	if execErr != nil {
		return fmt.Sprintf("Error running %s: %v", name, execErr)
	}
	if result == nil {
		return ""
	}
	if !result.Success {
		if result.Error != "" {
			return fmt.Sprintf("Error: %s", result.Error)
		}
		return "Error"
	}
	if name == "activate_skill" {
		if msg, ok := result.Data["message"].(string); ok && msg != "" {
			return msg
		}
	}
	if msg, ok := result.DisplayData["message"].(string); ok && msg != "" {
		return msg
	}
	if summary, ok := result.DisplayData["summary"].(string); ok && summary != "" {
		return summary
	}
	return ""
}

func addUsage(total model.Usage, next model.Usage) model.Usage {
	total.PromptTokens += next.PromptTokens
	total.CompletionTokens += next.CompletionTokens
	total.TotalTokens += next.TotalTokens
	return total
}

func isToolUnsupportedError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "tool") && strings.Contains(lower, "not support") {
		return true
	}
	if strings.Contains(lower, "tool") && strings.Contains(lower, "unsupported") {
		return true
	}
	if strings.Contains(lower, "does not support tool calling") {
		return true
	}
	if strings.Contains(lower, "does not support tool response") {
		return true
	}
	return false
}

func (c *Controller) warnIfTruncatedResponse(finishReason string) {
	if c.app == nil || !finishReasonIndicatesTruncation(finishReason) {
		return
	}
	c.app.AddMessage("Warning: response may be truncated (model hit output limit). Ask to continue or reduce context.", "system")
}

func finishReasonIndicatesTruncation(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "length", "max_tokens", "max_tokens_exceeded", "context_length_exceeded":
		return true
	default:
		return false
	}
}

func (c *Controller) emitStreaming(sessionID string, streaming bool) {
	if c.telemetry == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	eventType := telemetry.EventModelStreamEnded
	if streaming {
		eventType = telemetry.EventModelStreamStarted
	}
	c.telemetry.Publish(telemetry.Event{
		Type:      eventType,
		SessionID: sessionID,
	})
}

func (c *Controller) emitUICommandEvent(cmd string, args []string, phase string, duration time.Duration) {
	if c.telemetry == nil || strings.TrimSpace(cmd) == "" {
		return
	}
	data := map[string]any{
		"command": cmd,
		"phase":   phase,
	}
	if len(args) > 0 {
		data["args"] = args
	}
	if duration > 0 {
		data["duration_ms"] = duration.Milliseconds()
	}
	sessionID := ""
	if c.mu.TryLock() {
		if len(c.sessions) > 0 && c.currentSession >= 0 && c.currentSession < len(c.sessions) {
			sessionID = c.sessions[c.currentSession].ID
		}
		c.mu.Unlock()
	}
	c.telemetry.Publish(telemetry.Event{
		Type:      telemetry.EventUICommand,
		SessionID: sessionID,
		Data:      data,
	})
}

const (
	defaultContextWindow     = 8192
	defaultPromptBudgetRatio = 0.9
	messageOverheadTokens    = 4
)

type ContextBudgetStats struct {
	ModelID            string
	ContextWindow      int
	PromptBudget       int
	PromptBudgetRatio  float64
	ToolTokens         int
	SystemTokens       int
	ConversationTokens int
	PromptTokens       int
	UsedTokens         int
	RemainingTokens    int
	TotalMessages      int
	TrimmedMessages    int
	ProjectContextMode string
}

func (c *Controller) executionModelID() string {
	modelID := ""
	if c.cfg != nil {
		modelID = strings.TrimSpace(c.cfg.Models.Execution)
	}
	if modelID == "" {
		modelID = "openai/gpt-4o"
	}
	return modelID
}

func (c *Controller) contextWindow(modelID string) int {
	contextWindow := defaultContextWindow
	if c.modelMgr != nil {
		if info, err := c.modelMgr.GetModelInfo(modelID); err == nil && info != nil && info.ContextLength > 0 {
			contextWindow = info.ContextLength
		}
	}
	return contextWindow
}

func (c *Controller) promptBudgetRatio() float64 {
	ratio := defaultPromptBudgetRatio
	if c.cfg != nil && c.cfg.Memory.AutoCompactThreshold > 0 && c.cfg.Memory.AutoCompactThreshold <= 1 {
		ratio = c.cfg.Memory.AutoCompactThreshold
	}
	return ratio
}

func (c *Controller) promptBudget(modelID string) int {
	contextWindow := c.contextWindow(modelID)
	ratio := c.promptBudgetRatio()
	budget := int(float64(contextWindow) * ratio)
	if budget <= 0 {
		budget = contextWindow
	}
	return budget
}

func allowedToolsForSession(sess *SessionState) []string {
	if sess == nil || sess.SkillState == nil {
		return nil
	}
	return sess.SkillState.ToolFilter()
}

func estimateMessageTokens(role, content string) int {
	if strings.TrimSpace(content) == "" {
		return 0
	}
	return conversation.CountTokens(content) + conversation.CountTokens(role) + messageOverheadTokens
}

func estimateConversationMessageTokens(msg conversation.Message) int {
	if msg.Tokens > 0 {
		return msg.Tokens + messageOverheadTokens
	}
	content := conversation.GetContentAsString(msg.Content)
	if msg.Role == "assistant" && strings.TrimSpace(content) == "" && strings.TrimSpace(msg.Reasoning) != "" {
		content = msg.Reasoning
	}
	return estimateMessageTokens(msg.Role, content)
}

func estimateToolTokens(registry *tool.Registry, allowedTools []string) int {
	if registry == nil {
		return 0
	}
	tools := registry.ToOpenAIFunctionsFiltered(allowedTools)
	if len(tools) == 0 {
		return 0
	}
	raw, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return conversation.CountTokens(string(raw))
}

func buildProjectContextSummary(ctx *projectcontext.ProjectContext) string {
	if ctx == nil || !ctx.Loaded {
		return ""
	}
	var b strings.Builder
	if strings.TrimSpace(ctx.Summary) != "" {
		b.WriteString("Summary: " + strings.TrimSpace(ctx.Summary) + "\n")
	}
	if len(ctx.Rules) > 0 {
		b.WriteString("Development Rules:\n")
		for _, rule := range ctx.Rules {
			rule = strings.TrimSpace(rule)
			if rule != "" {
				b.WriteString("- " + rule + "\n")
			}
		}
	}
	if len(ctx.Guidelines) > 0 {
		b.WriteString("Agent Guidelines:\n")
		for _, guideline := range ctx.Guidelines {
			guideline = strings.TrimSpace(guideline)
			if guideline != "" {
				b.WriteString("- " + guideline + "\n")
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func (c *Controller) buildSystemPromptWithBudget(sess *SessionState, budgetTokens int) string {
	base := "You are Buckley, an AI development assistant. "
	base += "You help users with software engineering tasks including writing code, debugging, and explaining concepts. "
	base += "Match the user's requested level of detail. "
	base += "If asked to validate or cite code, use tools and include file paths and code snippets. "
	base += "Put user-facing details in the final response, not hidden reasoning.\n\n"
	var b strings.Builder
	used := 0
	appendSection := func(content string, required bool) {
		if strings.TrimSpace(content) == "" {
			return
		}
		if !required && budgetTokens <= 0 {
			return
		}
		tokens := conversation.CountTokens(content)
		if budgetTokens > 0 && !required && used+tokens > budgetTokens {
			return
		}
		b.WriteString(content)
		used += tokens
	}

	appendSection(base, true)

	rawProject := ""
	projectSummary := ""
	if c.projectCtx != nil && c.projectCtx.Loaded {
		rawProject = strings.TrimSpace(c.projectCtx.RawContent)
		projectSummary = buildProjectContextSummary(c.projectCtx)
	}

	if budgetTokens > 0 && (rawProject != "" || projectSummary != "") {
		projectSection := ""
		if rawProject != "" {
			projectSection = "Project Context:\n" + rawProject + "\n\n"
		}
		summarySection := ""
		if projectSummary != "" {
			summarySection = "Project Context (summary):\n" + projectSummary + "\n\n"
		}

		remaining := budgetTokens - used
		if remaining > 0 {
			if projectSection != "" && conversation.CountTokens(projectSection) <= remaining {
				appendSection(projectSection, false)
			} else if summarySection != "" && conversation.CountTokens(summarySection) <= remaining {
				appendSection(summarySection, false)
			}
		}
	}

	workDir := fmt.Sprintf("Working directory: %s\n", c.workDir)
	appendSection(workDir, true)

	appendSection("If the user asks to create a new skill, draft name/description/body and call create_skill to save it.\n", true)

	if sess != nil && sess.SkillRegistry != nil {
		if desc := strings.TrimSpace(sess.SkillRegistry.GetDescriptions()); desc != "" {
			appendSection("\n"+desc+"\n", false)
		}
	}

	return strings.TrimSpace(b.String()) + "\n"
}

func trimConversationToBudget(conv *conversation.Conversation, budgetTokens int) *conversation.Conversation {
	if conv == nil {
		return nil
	}
	if budgetTokens <= 0 || len(conv.Messages) == 0 {
		return &conversation.Conversation{SessionID: conv.SessionID}
	}

	used := 0
	start := len(conv.Messages)
	lastIdx := len(conv.Messages) - 1

	for i := lastIdx; i >= 0; i-- {
		msgTokens := estimateConversationMessageTokens(conv.Messages[i])
		if i == lastIdx && msgTokens > budgetTokens {
			start = i
			used = msgTokens
			break
		}
		if used+msgTokens > budgetTokens {
			break
		}
		used += msgTokens
		start = i
	}

	if start == len(conv.Messages) {
		return &conversation.Conversation{SessionID: conv.SessionID}
	}

	// Ensure we don't orphan tool response messages.
	// Tool responses must have their corresponding assistant message with tool_calls.
	start, used = adjustStartForToolCallPairs(conv.Messages, start, used)

	trimmed := &conversation.Conversation{
		SessionID:  conv.SessionID,
		Messages:   append([]conversation.Message{}, conv.Messages[start:]...),
		TokenCount: used,
	}
	return trimmed
}

// adjustStartForToolCallPairs moves start backwards to include assistant messages
// that contain tool_calls for any tool responses we're keeping.
// This prevents orphaned tool responses which break API contracts.
func adjustStartForToolCallPairs(messages []conversation.Message, start, used int) (int, int) {
	if start >= len(messages) {
		return start, used
	}

	// Collect tool_call_ids from tool responses we're keeping
	neededToolCallIDs := make(map[string]bool)
	for i := start; i < len(messages); i++ {
		if messages[i].Role == "tool" && messages[i].ToolCallID != "" {
			neededToolCallIDs[messages[i].ToolCallID] = true
		}
	}

	if len(neededToolCallIDs) == 0 {
		return start, used
	}

	// Find assistant messages with matching tool_calls that we need to include
	for i := start - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}

		// Check if this assistant message has tool_calls we need
		hasNeeded := false
		for _, tc := range msg.ToolCalls {
			if neededToolCallIDs[tc.ID] {
				hasNeeded = true
				delete(neededToolCallIDs, tc.ID)
			}
		}

		if hasNeeded {
			// Include this message and all between it and current start
			for j := i; j < start; j++ {
				used += estimateConversationMessageTokens(messages[j])
			}
			start = i

			// Also track tool_calls from this assistant message
			for _, tc := range msg.ToolCalls {
				neededToolCallIDs[tc.ID] = true
			}
		}

		if len(neededToolCallIDs) == 0 {
			break
		}
	}

	// If we still have orphaned tool responses (assistant message not found),
	// skip those tool responses entirely
	if len(neededToolCallIDs) > 0 {
		for start < len(messages) {
			if messages[start].Role == "tool" && neededToolCallIDs[messages[start].ToolCallID] {
				used -= estimateConversationMessageTokens(messages[start])
				start++
			} else {
				break
			}
		}
	}

	return start, used
}

func (c *Controller) buildRequestContext(sess *SessionState, modelID, prompt string, allowedTools []string) (string, *conversation.Conversation, int) {
	budget := c.promptBudget(modelID)
	registry := c.registry
	if sess != nil && sess.ToolRegistry != nil {
		registry = sess.ToolRegistry
	}
	if budget > 0 {
		budget -= estimateToolTokens(registry, allowedTools)
		if prompt != "" {
			budget -= estimateMessageTokens("user", prompt)
		}
		if budget < 0 {
			budget = 0
		}
	}

	systemPrompt := c.buildSystemPromptWithBudget(sess, budget)
	if budget > 0 {
		budget -= estimateMessageTokens("system", systemPrompt)
		if budget < 0 {
			budget = 0
		}
	}

	// Return full conversation - let the strategy's ContextBuilder handle trimming
	// to avoid double-trimming and ensure tool call fields are preserved
	var conv *conversation.Conversation
	if sess != nil {
		conv = sess.Conversation
	}

	return systemPrompt, conv, budget
}

func projectContextMode(systemPrompt string) string {
	if strings.Contains(systemPrompt, "Project Context (summary):") {
		return "summary"
	}
	if strings.Contains(systemPrompt, "Project Context:") {
		return "raw"
	}
	return "none"
}

func (c *Controller) buildContextBudgetStats(sess *SessionState, modelID, prompt string, allowedTools []string) ContextBudgetStats {
	stats := ContextBudgetStats{
		ModelID:           modelID,
		ContextWindow:     c.contextWindow(modelID),
		PromptBudget:      c.promptBudget(modelID),
		PromptBudgetRatio: c.promptBudgetRatio(),
	}
	if sess == nil || sess.Conversation == nil {
		return stats
	}

	registry := c.registry
	if sess.ToolRegistry != nil {
		registry = sess.ToolRegistry
	}

	systemPrompt, conv, budget := c.buildRequestContext(sess, modelID, prompt, allowedTools)

	stats.ToolTokens = estimateToolTokens(registry, allowedTools)
	stats.PromptTokens = estimateMessageTokens("user", prompt)
	stats.SystemTokens = estimateMessageTokens("system", systemPrompt)

	// Simulate trimming for stats display
	trimmed := trimConversationToBudget(conv, budget)
	if trimmed != nil {
		stats.ConversationTokens = trimmed.TokenCount
		stats.TrimmedMessages = len(trimmed.Messages)
	}
	stats.TotalMessages = len(sess.Conversation.Messages)
	stats.ProjectContextMode = projectContextMode(systemPrompt)

	stats.UsedTokens = stats.ToolTokens + stats.PromptTokens + stats.SystemTokens + stats.ConversationTokens
	stats.RemainingTokens = stats.PromptBudget - stats.UsedTokens
	if stats.RemainingTokens < 0 {
		stats.RemainingTokens = 0
	}

	return stats
}

func (c *Controller) updateContextIndicator(sess *SessionState, modelID, prompt string, allowedTools []string) {
	if c.app == nil || sess == nil {
		return
	}
	stats := c.buildContextBudgetStats(sess, modelID, prompt, allowedTools)
	c.app.SetContextUsage(stats.UsedTokens, stats.PromptBudget, stats.ContextWindow)
}

func formatContextBudgetStats(stats ContextBudgetStats) string {
	var b strings.Builder
	b.WriteString("Context budget:\n")
	if stats.ModelID != "" {
		b.WriteString(fmt.Sprintf("- Model: %s\n", stats.ModelID))
	}
	if stats.ContextWindow > 0 {
		b.WriteString(fmt.Sprintf("- Context window: %d tokens\n", stats.ContextWindow))
	}
	if stats.PromptBudget > 0 {
		b.WriteString(fmt.Sprintf("- Prompt budget: %d tokens (ratio %.2f)\n", stats.PromptBudget, stats.PromptBudgetRatio))
	}
	b.WriteString(fmt.Sprintf("- Used: %d tokens (system %d, conversation %d, tools %d, prompt %d)\n",
		stats.UsedTokens, stats.SystemTokens, stats.ConversationTokens, stats.ToolTokens, stats.PromptTokens))
	if stats.RemainingTokens > 0 {
		b.WriteString(fmt.Sprintf("- Remaining: %d tokens\n", stats.RemainingTokens))
	}
	if stats.TotalMessages > 0 {
		b.WriteString(fmt.Sprintf("- Messages kept: %d/%d\n", stats.TrimmedMessages, stats.TotalMessages))
	}
	if stats.ProjectContextMode != "" {
		b.WriteString(fmt.Sprintf("- Project context: %s\n", stats.ProjectContextMode))
	}
	b.WriteString("Note: token counts are estimates; excludes current input unless specified.")
	return b.String()
}

// buildMessagesForSession constructs the message list for the API using a specific session.
func (c *Controller) buildMessagesForSession(sess *SessionState, modelID string, allowedTools []string) []model.Message {
	messages := []model.Message{}

	systemPrompt, conv, budget := c.buildRequestContext(sess, modelID, "", allowedTools)
	messages = append(messages, model.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	// Trim conversation to budget before converting to model messages
	trimmed := trimConversationToBudget(conv, budget)
	if trimmed != nil {
		messages = append(messages, trimmed.ToModelMessages()...)
	}

	return messages
}

// handleFileSelect processes file selection from the picker.
func (c *Controller) handleFileSelect(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Read file and add to context
	fullPath := filepath.Join(c.workDir, path)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		c.app.AddMessage(fmt.Sprintf("Error reading file: %v", err), "system")
		return
	}

	// Add file content as context
	msg := fmt.Sprintf("File: %s\n```\n%s\n```", path, string(content))
	c.app.AddMessage(msg, "system")
}

// handleShellCmd executes a shell command.
func (c *Controller) handleShellCmd(cmd string) string {
	// For now, just indicate what would be executed
	// Full shell execution would need sandboxing considerations
	return fmt.Sprintf("Would execute: %s", cmd)
}

// defaultTUIMaxOutputBytes limits tool output in TUI mode.
const defaultTUIMaxOutputBytes = 100_000

// buildRegistry creates the tool registry with all available tools.
func buildRegistry(cfg *config.Config, store *storage.Store, workDir string, hub *telemetry.Hub, sessionID string, progressMgr *progress.ProgressManager, toastMgr *toast.ToastManager) *tool.Registry {
	registry := tool.NewRegistry()
	registry.SetMaxOutputBytes(defaultTUIMaxOutputBytes)

	// Configure container execution if enabled
	if cfg != nil && workDir != "" {
		registry.ConfigureContainers(cfg, workDir)
	}

	// Enable todo tracking
	if store != nil {
		registry.SetTodoStore(&todoStoreAdapter{store: store})
		registry.EnableCodeIndex(store)
	}

	// Enable telemetry
	if hub != nil && sessionID != "" {
		registry.EnableTelemetry(hub, sessionID)
	}

	if progressMgr != nil {
		registry.Use(tool.Progress(progressMgr, tool.DefaultLongRunningTools))
	}
	if toastMgr != nil {
		registry.Use(tool.ToastNotifications(toastMgr))
	}

	// Load user plugins from ~/.buckley/plugins/ and ./.buckley/plugins/
	if err := registry.LoadDefaultPlugins(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load some plugins: %v\n", err)
	}

	// Set working directory for file tools
	if workDir != "" {
		registry.SetWorkDir(workDir)
	}

	return registry
}

func (c *Controller) handleSessionsCommand(args []string) {
	if len(args) == 0 {
		c.listSessions()
		return
	}
	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "complete", "close", "archive":
		c.completeSessionsCommand(args[1:])
	default:
		c.app.AddMessage("Usage: /sessions [complete <id|index|all|current>]", "system")
	}
}

func (c *Controller) completeSessionsCommand(args []string) {
	if len(args) == 0 {
		c.app.AddMessage("Usage: /sessions complete <id|index|all|current>", "system")
		return
	}
	if c.store == nil {
		c.app.AddMessage("Session store unavailable.", "system")
		return
	}
	target := strings.TrimSpace(args[0])
	switch strings.ToLower(target) {
	case "all":
		c.completeAllSessions()
		return
	case "current":
		c.completeCurrentSession()
		return
	}

	if idx, err := strconv.Atoi(target); err == nil {
		c.completeSessionByIndex(idx - 1)
		return
	}
	c.completeSessionByID(target)
}

func (c *Controller) completeAllSessions() {
	if !c.mu.TryLock() {
		c.app.AddMessage("Session list busy; try again.", "system")
		return
	}
	if len(c.sessions) <= 1 {
		c.mu.Unlock()
		c.app.AddMessage("No other sessions to complete.", "system")
		return
	}

	currentIdx := c.currentSession
	current := c.sessions[currentIdx]
	toComplete := make([]*SessionState, 0, len(c.sessions)-1)
	for i, sess := range c.sessions {
		if i == currentIdx {
			continue
		}
		toComplete = append(toComplete, sess)
	}
	c.sessions = []*SessionState{current}
	c.currentSession = 0
	c.conversation = current.Conversation
	c.registry = current.ToolRegistry
	c.mu.Unlock()

	failed := 0
	for _, sess := range toComplete {
		if err := c.store.SetSessionStatus(sess.ID, storage.SessionStatusCompleted); err != nil {
			failed++
		}
	}
	if failed > 0 {
		c.app.AddMessage(fmt.Sprintf("Completed %d sessions (%d failed).", len(toComplete)-failed, failed), "system")
	} else {
		c.app.AddMessage(fmt.Sprintf("Completed %d sessions.", len(toComplete)), "system")
	}
	c.updateContextIndicator(current, c.executionModelID(), "", allowedToolsForSession(current))
}

func (c *Controller) completeCurrentSession() {
	c.mu.Lock()
	idx := c.currentSession
	c.mu.Unlock()
	c.completeSessionByIndex(idx)
}

func (c *Controller) completeSessionByID(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		c.app.AddMessage("Session ID required.", "system")
		return
	}
	if !c.mu.TryLock() {
		c.app.AddMessage("Session list busy; try again.", "system")
		return
	}
	idx := -1
	for i, sess := range c.sessions {
		if sess.ID == sessionID {
			idx = i
			break
		}
	}
	c.mu.Unlock()
	if idx < 0 {
		c.app.AddMessage("Session not found: "+sessionID, "system")
		return
	}
	c.completeSessionByIndex(idx)
}

func (c *Controller) completeSessionByIndex(idx int) {
	if !c.mu.TryLock() {
		c.app.AddMessage("Session list busy; try again.", "system")
		return
	}
	if idx < 0 || idx >= len(c.sessions) {
		c.mu.Unlock()
		c.app.AddMessage("Session index out of range.", "system")
		return
	}

	sess := c.sessions[idx]
	wasCurrent := idx == c.currentSession
	onlySession := len(c.sessions) == 1

	if !onlySession {
		c.sessions = append(c.sessions[:idx], c.sessions[idx+1:]...)
		if idx < c.currentSession {
			c.currentSession--
		}
		if wasCurrent {
			if idx >= len(c.sessions) {
				idx = len(c.sessions) - 1
			}
			c.currentSession = idx
			c.switchToSessionLocked(idx)
		}
	}
	c.mu.Unlock()

	if err := c.store.SetSessionStatus(sess.ID, storage.SessionStatusCompleted); err != nil {
		c.app.AddMessage("Failed to complete session "+sess.ID+": "+err.Error(), "system")
	} else {
		c.app.AddMessage("Session completed: "+sess.ID, "system")
	}

	if wasCurrent {
		if onlySession {
			newSess, err := c.createNewSessionState()
			if err != nil {
				c.app.AddMessage("Error creating session: "+err.Error(), "system")
				return
			}
			c.mu.Lock()
			c.sessions = []*SessionState{newSess}
			c.currentSession = 0
			c.conversation = newSess.Conversation
			c.registry = newSess.ToolRegistry
			c.mu.Unlock()

			c.app.ClearScrollback()
			c.app.WelcomeScreen()
			c.app.AddMessage("New session started: "+newSess.ID, "system")
			c.app.SetStatus("Ready")
			c.updateContextIndicator(newSess, c.executionModelID(), "", allowedToolsForSession(newSess))
			return
		}
		c.mu.Lock()
		if c.currentSession >= 0 && c.currentSession < len(c.sessions) {
			current := c.sessions[c.currentSession]
			c.mu.Unlock()
			c.updateContextIndicator(current, c.executionModelID(), "", allowedToolsForSession(current))
		} else {
			c.mu.Unlock()
		}
	}
}

// listSessions shows all active sessions for this project.
func (c *Controller) listSessions() {
	if !c.mu.TryLock() {
		c.app.AddMessage("Session list busy; try again.", "system")
		return
	}
	current := c.currentSession
	snapshots := make([]struct {
		id        string
		streaming bool
	}, 0, len(c.sessions))
	for _, sess := range c.sessions {
		snapshots = append(snapshots, struct {
			id        string
			streaming bool
		}{
			id:        sess.ID,
			streaming: sess.Streaming,
		})
	}
	c.mu.Unlock()

	if len(snapshots) == 0 {
		c.app.AddMessage("No active sessions", "system")
		return
	}

	var sb strings.Builder
	sb.WriteString("Active sessions:\n")
	for i, sess := range snapshots {
		marker := "  "
		if i == current {
			marker = "→ "
		}
		status := ""
		if sess.streaming {
			status = " (streaming...)"
		}
		sb.WriteString(fmt.Sprintf("%s[%d] %s%s\n", marker, i+1, sess.id, status))
	}
	sb.WriteString("\nUse /next or /prev to switch (Alt+Right/Left)")
	sb.WriteString("\nUse /sessions complete <id|index|all> to archive sessions")
	c.app.AddMessage(sb.String(), "system")
}

// nextSession switches to the next session.
func (c *Controller) nextSession() {
	c.mu.Lock()
	if len(c.sessions) <= 1 {
		c.mu.Unlock()
		c.app.AddMessage("No other sessions to switch to", "system")
		return
	}

	c.currentSession = (c.currentSession + 1) % len(c.sessions)
	c.switchToSessionLocked(c.currentSession)
	sess := c.sessions[c.currentSession]
	c.mu.Unlock()

	c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))
}

// prevSession switches to the previous session.
func (c *Controller) prevSession() {
	c.mu.Lock()
	if len(c.sessions) <= 1 {
		c.mu.Unlock()
		c.app.AddMessage("No other sessions to switch to", "system")
		return
	}

	c.currentSession = (c.currentSession - 1 + len(c.sessions)) % len(c.sessions)
	c.switchToSessionLocked(c.currentSession)
	sess := c.sessions[c.currentSession]
	c.mu.Unlock()

	c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))
}

// switchToSessionLocked loads a session by index.
// Must be called with c.mu held.
func (c *Controller) switchToSessionLocked(idx int) {
	if idx < 0 || idx >= len(c.sessions) {
		return
	}

	sess := c.sessions[idx]
	c.conversation = sess.Conversation
	c.registry = sess.ToolRegistry

	// Clear and rebuild display
	c.app.ClearScrollback()
	c.app.WelcomeScreen()

	statusMsg := fmt.Sprintf("Switched to session: %s", sess.ID)
	if sess.Streaming {
		statusMsg += " (response in progress)"
	}
	c.app.AddMessage(statusMsg, "system")

	// Replay conversation to display
	for _, msg := range sess.Conversation.Messages {
		content := ""
		if s, ok := msg.Content.(string); ok {
			content = s
		}
		c.app.AddMessage(content, msg.Role)
	}

	if sess.Streaming {
		c.app.SetStatus("Streaming...")
	} else {
		c.app.SetStatus("Ready")
	}
	c.app.SetStreaming(sess.Streaming)
}

// Stop gracefully stops the controller.
func (c *Controller) Stop() {
	// Stop telemetry bridge
	if c.telemetryBridge != nil {
		c.telemetryBridge.Stop()
	}

	// Close diagnostics collector
	if c.diagnostics != nil {
		c.diagnostics.Close()
	}

	c.mu.Lock()
	// Cancel all streaming sessions
	for _, sess := range c.sessions {
		if sess.Cancel != nil {
			sess.Cancel()
		}
	}
	c.mu.Unlock()
	c.app.Quit()
}

// saveMessage persists a message to storage.
func (c *Controller) saveMessage(sessionID, role, content string) {
	if c.store == nil {
		return
	}
	msg := &storage.Message{
		SessionID: sessionID,
		Role:      role,
		Content:   content,
		Timestamp: time.Now(),
	}
	_ = c.store.SaveMessage(msg) // Ignore errors for now
}

// handleReview reviews the current git diff in conversation.
func (c *Controller) handleReview() {
	// Get git diff (staged + unstaged)
	diff, err := c.getGitDiff()
	if err != nil {
		c.app.AddMessage(fmt.Sprintf("Error getting diff: %v", err), "system")
		return
	}

	if strings.TrimSpace(diff) == "" {
		c.app.AddMessage("No changes to review. Stage some changes or make modifications first.", "system")
		return
	}

	// Build review prompt
	prompt := fmt.Sprintf(`Please review the following code changes and provide feedback:

%s

Focus on:
1. **Correctness** - Logic errors, edge cases, potential bugs
2. **Security** - Vulnerabilities, injection risks, auth issues
3. **Performance** - Inefficiencies, N+1 queries, memory leaks
4. **Style** - Naming, conventions, readability
5. **Architecture** - Design concerns, coupling, abstractions

Be specific with file:line references. Flag critical issues first.`, "```diff\n"+diff+"\n```")

	// Display as user message and stream response
	c.app.AddMessage("/review", "user")

	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session available.", "system")
		return
	}
	sess := c.sessions[c.currentSession]
	sess.Cancel = cancel
	sess.Streaming = true
	c.mu.Unlock()
	c.emitStreaming(sess.ID, true)
	c.app.SetStreaming(true)

	go c.streamResponse(ctx, prompt, sess)
}

// handleCommit generates a commit message for staged changes.
func (c *Controller) handleCommit() {
	// Get staged diff only
	diff, err := c.getGitDiffStaged()
	if err != nil {
		c.app.AddMessage(fmt.Sprintf("Error getting staged changes: %v", err), "system")
		return
	}

	if strings.TrimSpace(diff) == "" {
		c.app.AddMessage("No staged changes. Use `git add` to stage files first.", "system")
		return
	}

	// Get recent commit messages for style reference
	recentCommits := c.getRecentCommits(5)

	// Build commit message generation prompt
	prompt := fmt.Sprintf(`Generate a commit message for these staged changes:

%s

Recent commit style for reference:
%s

Requirements:
- Use conventional commit format: type(scope): description
- Types: feat, fix, refactor, docs, test, chore, perf, style
- First line under 72 chars
- Be specific about what changed and why
- Add body if changes are complex

Output ONLY the commit message, nothing else.`, "```diff\n"+diff+"\n```", recentCommits)

	// Display as user message and stream response
	c.app.AddMessage("/commit", "user")

	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session available.", "system")
		return
	}
	sess := c.sessions[c.currentSession]
	sess.Cancel = cancel
	sess.Streaming = true
	c.mu.Unlock()
	c.emitStreaming(sess.ID, true)
	c.app.SetStreaming(true)

	go c.streamResponse(ctx, prompt, sess)
}

func (c *Controller) handleSearchCommand(args []string) {
	if c.store == nil {
		c.app.AddMessage("Search unavailable: storage not configured.", "system")
		return
	}
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session available.", "system")
		return
	}
	sessionID := c.sessions[c.currentSession].ID
	c.mu.Unlock()

	mode := ""
	limit := 5
	scopeAll := false
	var queryParts []string
	for i := 0; i < len(args); i++ {
		switch strings.ToLower(strings.TrimSpace(args[i])) {
		case "--semantic", "-s":
			mode = "semantic"
		case "--fulltext", "--fts", "-f":
			mode = "fulltext"
		case "--all":
			scopeAll = true
		case "--limit", "-l":
			if i+1 >= len(args) {
				c.app.AddMessage("Usage: /search [--semantic|--fulltext] [--all] [--limit N] <query>", "system")
				return
			}
			value, err := strconv.Atoi(args[i+1])
			if err != nil || value <= 0 {
				c.app.AddMessage("Limit must be a positive integer.", "system")
				return
			}
			limit = value
			i++
		default:
			queryParts = append(queryParts, args[i])
		}
	}

	query := strings.TrimSpace(strings.Join(queryParts, " "))
	if query == "" {
		c.app.AddMessage("Usage: /search [--semantic|--fulltext] [--all] [--limit N] <query>", "system")
		return
	}

	var provider embeddings.EmbeddingProvider
	if mode == "" || mode == "semantic" {
		provider = c.newEmbeddingProvider()
		if mode == "" {
			if provider != nil {
				mode = "semantic"
			} else {
				mode = "fulltext"
			}
		}
	}

	if scopeAll {
		if mode == "semantic" {
			c.app.AddMessage("Semantic search is session-scoped; use /search --fulltext --all <query>.", "system")
			return
		}
		sessionID = ""
	}

	searcher := conversation.NewConversationSearcher(c.store, provider)
	opts := conversation.SearchOptions{SessionID: sessionID, Limit: limit}
	ctx := context.Background()

	var (
		results []conversation.SearchResult
		err     error
	)
	switch mode {
	case "semantic":
		if provider == nil {
			c.app.AddMessage("Semantic search unavailable: configure OPENROUTER_API_KEY or OPENAI_API_KEY.", "system")
			return
		}
		results, err = searcher.Search(ctx, query, opts)
	default:
		results, err = searcher.SearchFullText(ctx, query, opts)
		mode = "fulltext"
	}
	if err != nil {
		c.app.AddMessage("Search failed: "+err.Error(), "system")
		return
	}
	if len(results) == 0 {
		c.app.AddMessage("No matches found.", "system")
		return
	}

	scopeLabel := "current session"
	if sessionID == "" {
		scopeLabel = "all sessions"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Search results (%s, %s):\n", mode, scopeLabel))
	for i, res := range results {
		line := fmt.Sprintf("%d. [%.2f] %s", i+1, res.Score, res.Role)
		if strings.TrimSpace(res.SessionID) != "" {
			line += " (" + res.SessionID + ")"
		}
		if !res.Timestamp.IsZero() {
			line += " " + res.Timestamp.Format(time.RFC3339)
		}
		b.WriteString(line + "\n")
		snippet := strings.TrimSpace(res.Snippet)
		if snippet == "" {
			snippet = strings.TrimSpace(res.Content)
		}
		if len(snippet) > 240 {
			snippet = snippet[:240] + "..."
		}
		if snippet != "" {
			b.WriteString(snippet + "\n\n")
		}
	}
	c.app.AddMessage(strings.TrimSpace(b.String()), "system")
}

func (c *Controller) handleExportCommand(args []string) {
	if c.store == nil {
		c.app.AddMessage("Export unavailable: storage not configured.", "system")
		return
	}
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session available.", "system")
		return
	}
	sessionID := c.sessions[c.currentSession].ID
	c.mu.Unlock()

	opts := conversation.ExportOptions{Format: conversation.ExportMarkdown}
	outputPath := ""

	for i := 0; i < len(args); i++ {
		arg := strings.ToLower(strings.TrimSpace(args[i]))
		switch arg {
		case "--format", "-f":
			if i+1 >= len(args) {
				c.app.AddMessage("Usage: /export [--format json|markdown|html] [--output path] [--include-system] [--include-tools] [--include-metadata]", "system")
				return
			}
			format, ok := parseExportFormat(args[i+1])
			if !ok {
				c.app.AddMessage("Unsupported export format: "+args[i+1], "system")
				return
			}
			opts.Format = format
			i++
		case "--output", "-o":
			if i+1 >= len(args) {
				c.app.AddMessage("Usage: /export [--format json|markdown|html] [--output path] [--include-system] [--include-tools] [--include-metadata]", "system")
				return
			}
			outputPath = strings.TrimSpace(args[i+1])
			i++
		case "--include-system":
			opts.IncludeSystem = true
		case "--include-tools":
			opts.IncludeToolCalls = true
		case "--include-metadata":
			opts.IncludeMetadata = true
		default:
			if outputPath == "" {
				outputPath = strings.TrimSpace(args[i])
			}
		}
	}

	if outputPath == "" {
		ext := exportExtension(opts.Format)
		timestamp := time.Now().Format("20060102-150405")
		outputPath = fmt.Sprintf("buckley-export-%s-%s%s", sessionID, timestamp, ext)
	}
	if !filepath.IsAbs(outputPath) {
		outputPath = filepath.Join(c.workDir, outputPath)
	}

	exporter := conversation.NewExporter(c.store)
	data, err := exporter.Export(sessionID, opts)
	if err != nil {
		c.app.AddMessage("Export failed: "+err.Error(), "system")
		return
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		c.app.AddMessage("Export failed: "+err.Error(), "system")
		return
	}
	if err := os.WriteFile(outputPath, data, 0o644); err != nil {
		c.app.AddMessage("Export failed: "+err.Error(), "system")
		return
	}

	c.app.AddMessage(fmt.Sprintf("Exported session %s to %s", sessionID, outputPath), "system")
}

func (c *Controller) handleImportCommand(args []string) {
	if c.store == nil {
		c.app.AddMessage("Import unavailable: storage not configured.", "system")
		return
	}

	var (
		format    conversation.ExportFormat
		inputPath string
	)
	for i := 0; i < len(args); i++ {
		arg := strings.ToLower(strings.TrimSpace(args[i]))
		switch arg {
		case "--format", "-f":
			if i+1 >= len(args) {
				c.app.AddMessage("Usage: /import [--format json|markdown] <path>", "system")
				return
			}
			parsed, ok := parseExportFormat(args[i+1])
			if !ok {
				c.app.AddMessage("Unsupported import format: "+args[i+1], "system")
				return
			}
			format = parsed
			i++
		default:
			if inputPath == "" {
				inputPath = strings.TrimSpace(args[i])
			}
		}
	}
	if inputPath == "" {
		c.app.AddMessage("Usage: /import [--format json|markdown] <path>", "system")
		return
	}
	if !filepath.IsAbs(inputPath) {
		inputPath = filepath.Join(c.workDir, inputPath)
	}

	if format == "" {
		ext := strings.ToLower(filepath.Ext(inputPath))
		switch ext {
		case ".md", ".markdown":
			format = conversation.ExportMarkdown
		case ".json":
			format = conversation.ExportJSON
		case ".html", ".htm":
			c.app.AddMessage("HTML import not supported; use JSON or Markdown.", "system")
			return
		default:
			format = conversation.ExportJSON
		}
	}
	if format == conversation.ExportHTML {
		c.app.AddMessage("HTML import not supported; use JSON or Markdown.", "system")
		return
	}

	data, err := os.ReadFile(inputPath)
	if err != nil {
		c.app.AddMessage("Import failed: "+err.Error(), "system")
		return
	}

	importer := conversation.NewImporter(c.store)
	result, err := importer.Import(data, format)
	if err != nil {
		c.app.AddMessage("Import failed: "+err.Error(), "system")
		return
	}
	if result == nil || strings.TrimSpace(result.SessionID) == "" {
		c.app.AddMessage("Import completed but no session was created.", "system")
		return
	}

	if err := c.store.UpdateSessionProjectPath(result.SessionID, c.workDir); err != nil {
		c.app.AddMessage("Imported session; failed to set project path: "+err.Error(), "system")
	}

	newSess, err := newSessionState(c.cfg, c.store, c.workDir, c.telemetry, c.modelMgr, result.SessionID, true, c.progressMgr, c.toastMgr)
	if err != nil {
		c.app.AddMessage("Imported session but failed to load: "+err.Error(), "system")
		return
	}

	c.mu.Lock()
	c.sessions = append([]*SessionState{newSess}, c.sessions...)
	c.currentSession = 0
	c.switchToSessionLocked(0)
	c.mu.Unlock()

	c.app.AddMessage(fmt.Sprintf("Imported session: %s (%d messages)", result.SessionID, result.MessageCount), "system")
	if len(result.Warnings) > 0 {
		var b strings.Builder
		b.WriteString("Import warnings:\n")
		for _, warn := range result.Warnings {
			b.WriteString("- " + warn + "\n")
		}
		c.app.AddMessage(strings.TrimSpace(b.String()), "system")
	}
	c.updateContextIndicator(newSess, c.executionModelID(), "", allowedToolsForSession(newSess))
}

func (c *Controller) showContextBudget() {
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session available.", "system")
		return
	}
	sess := c.sessions[c.currentSession]
	modelID := c.executionModelID()
	allowedTools := allowedToolsForSession(sess)
	c.mu.Unlock()

	stats := c.buildContextBudgetStats(sess, modelID, "", allowedTools)
	c.app.AddMessage(formatContextBudgetStats(stats), "system")
}

func (c *Controller) recordCost(sessionID, modelID string, usage *model.Usage) {
	if c == nil || usage == nil {
		return
	}
	tracker := c.ensureCostTracker(sessionID)
	if tracker == nil {
		return
	}
	if _, err := tracker.RecordAPICall(modelID, usage.PromptTokens, usage.CompletionTokens); err != nil {
		return
	}
	if c.budgetAlerts != nil {
		c.budgetAlerts.Check(tracker.CheckBudget())
	}
}

func (c *Controller) ensureCostTracker(sessionID string) *cost.Tracker {
	if c == nil || c.store == nil || c.modelMgr == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}

	c.mu.Lock()
	if c.costTrackers == nil {
		c.costTrackers = map[string]*cost.Tracker{}
	}
	if tracker, ok := c.costTrackers[sessionID]; ok {
		c.mu.Unlock()
		return tracker
	}
	c.mu.Unlock()

	tracker, err := cost.New(sessionID, c.store, c.modelMgr)
	if err != nil {
		return nil
	}
	if c.cfg != nil {
		tracker.SetBudgets(
			c.cfg.CostManagement.SessionBudget,
			c.cfg.CostManagement.DailyBudget,
			c.cfg.CostManagement.MonthlyBudget,
			c.cfg.CostManagement.AutoStopAt,
		)
	}

	c.mu.Lock()
	if c.costTrackers == nil {
		c.costTrackers = map[string]*cost.Tracker{}
	}
	c.costTrackers[sessionID] = tracker
	c.mu.Unlock()
	return tracker
}

func formatBudgetToast(alert cost.BudgetAlert) (toast.ToastLevel, string, string) {
	level := toast.ToastInfo
	title := "Budget alert"
	switch alert.Level {
	case cost.BudgetAlertWarning:
		level = toast.ToastWarning
		title = "Budget warning"
	case cost.BudgetAlertCritical:
		level = toast.ToastError
		title = "Budget critical"
	case cost.BudgetAlertExceeded:
		level = toast.ToastError
		title = "Budget exceeded"
	}

	label := "Session"
	costValue := alert.Status.SessionCost
	budgetValue := alert.Status.SessionBudget
	switch strings.ToLower(alert.BudgetType) {
	case cost.BudgetTypeDaily:
		label = "Daily"
		costValue = alert.Status.DailyCost
		budgetValue = alert.Status.DailyBudget
	case cost.BudgetTypeMonthly:
		label = "Monthly"
		costValue = alert.Status.MonthlyCost
		budgetValue = alert.Status.MonthlyBudget
	}

	message := fmt.Sprintf("%s budget %.0f%% ($%.2f / $%.2f)", label, alert.Percent, costValue, budgetValue)
	return level, title, message
}

func (c *Controller) newEmbeddingProvider() embeddings.EmbeddingProvider {
	if c == nil || c.cfg == nil {
		return nil
	}
	cacheDir := ""
	if strings.TrimSpace(c.workDir) != "" {
		cacheDir = filepath.Join(c.workDir, ".buckley", "embeddings")
	}

	type providerCandidate struct {
		id       string
		settings config.ProviderSettings
		kind     embeddings.ProviderKind
	}
	candidates := []providerCandidate{
		{id: "openrouter", settings: c.cfg.Providers.OpenRouter, kind: embeddings.ProviderOpenRouter},
		{id: "openai", settings: c.cfg.Providers.OpenAI, kind: embeddings.ProviderOpenAI},
	}

	pick := func(candidate providerCandidate) embeddings.EmbeddingProvider {
		if !candidate.settings.Enabled || strings.TrimSpace(candidate.settings.APIKey) == "" {
			return nil
		}
		return embeddings.NewService(embeddings.ServiceOptions{
			APIKey:   candidate.settings.APIKey,
			Provider: candidate.kind,
			BaseURL:  embeddingsBaseURL(candidate.settings.BaseURL),
			CacheDir: cacheDir,
		})
	}

	preferred := strings.ToLower(strings.TrimSpace(c.cfg.Models.DefaultProvider))
	for _, candidate := range candidates {
		if candidate.id == preferred {
			if provider := pick(candidate); provider != nil {
				return provider
			}
			break
		}
	}
	for _, candidate := range candidates {
		if provider := pick(candidate); provider != nil {
			return provider
		}
	}
	return nil
}

func parseExportFormat(raw string) (conversation.ExportFormat, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "json":
		return conversation.ExportJSON, true
	case "markdown", "md":
		return conversation.ExportMarkdown, true
	case "html", "htm":
		return conversation.ExportHTML, true
	default:
		return "", false
	}
}

func exportExtension(format conversation.ExportFormat) string {
	switch format {
	case conversation.ExportJSON:
		return ".json"
	case conversation.ExportHTML:
		return ".html"
	default:
		return ".md"
	}
}

func embeddingsBaseURL(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	if strings.HasSuffix(base, "/embeddings") {
		return base
	}
	return strings.TrimRight(base, "/") + "/embeddings"
}

func (c *Controller) handleSkillCommand(args []string) {
	c.mu.Lock()
	if len(c.sessions) == 0 {
		c.mu.Unlock()
		c.app.AddMessage("No active session available.", "system")
		return
	}
	sess := c.sessions[c.currentSession]
	c.mu.Unlock()
	if sess == nil || sess.SkillRegistry == nil || sess.SkillState == nil {
		c.app.AddMessage("Skill system unavailable in this session.", "system")
		return
	}

	if len(args) == 0 || strings.EqualFold(args[0], "list") {
		names := make([]string, 0)
		for _, s := range sess.SkillRegistry.List() {
			names = append(names, s.GetName())
		}
		sort.Strings(names)
		if len(names) == 0 {
			c.app.AddMessage("No skills available.", "system")
			return
		}
		var b strings.Builder
		b.WriteString("Available skills:\n")
		for _, name := range names {
			b.WriteString("- " + name + "\n")
		}
		c.app.AddMessage(strings.TrimSpace(b.String()), "system")
		return
	}

	name := strings.TrimSpace(strings.Join(args, " "))
	if name == "" {
		c.app.AddMessage("Usage: /skill <name>.", "system")
		return
	}

	tool := &builtin.SkillActivationTool{
		Registry:     sess.SkillRegistry,
		Conversation: sess.SkillState,
	}
	result, err := tool.Execute(map[string]any{
		"action": "activate",
		"skill":  name,
		"scope":  "user request",
	})
	if err != nil {
		c.app.AddMessage(fmt.Sprintf("Error activating skill %q: %v", name, err), "system")
		return
	}
	if result == nil || !result.Success {
		if result != nil && result.Error != "" {
			c.app.AddMessage(fmt.Sprintf("Error activating skill %q: %s", name, result.Error), "system")
			return
		}
		c.app.AddMessage(fmt.Sprintf("Error activating skill %q.", name), "system")
		return
	}

	message, _ := result.Data["message"].(string)
	content, _ := result.Data["content"].(string)
	if content != "" && message != "" {
		c.app.AddMessage(message+"\n\n"+content, "system")
		c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))
		return
	}
	if content != "" {
		c.app.AddMessage(content, "system")
		c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))
		return
	}
	if message != "" {
		c.app.AddMessage(message, "system")
		c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))
		return
	}
	c.app.AddMessage(fmt.Sprintf("Skill %q activated.", name), "system")
	c.updateContextIndicator(sess, c.executionModelID(), "", allowedToolsForSession(sess))
}

// getGitDiff returns the combined staged and unstaged diff.
func (c *Controller) getGitDiff() (string, error) {
	// Get unstaged changes
	cmd := exec.Command("git", "diff")
	cmd.Dir = c.workDir
	unstaged, _ := cmd.Output()

	// Get staged changes
	cmd = exec.Command("git", "diff", "--cached")
	cmd.Dir = c.workDir
	staged, _ := cmd.Output()

	combined := string(staged) + string(unstaged)
	return combined, nil
}

// getGitDiffStaged returns only staged changes.
func (c *Controller) getGitDiffStaged() (string, error) {
	cmd := exec.Command("git", "diff", "--cached")
	cmd.Dir = c.workDir
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

// getRecentCommits returns recent commit messages for style reference.
func (c *Controller) getRecentCommits(n int) string {
	cmd := exec.Command("git", "log", fmt.Sprintf("-%d", n), "--oneline")
	cmd.Dir = c.workDir
	output, err := cmd.Output()
	if err != nil {
		return "(no commits yet)"
	}
	return string(output)
}

// updateQueueIndicator updates the UI to show queued message count.
func (c *Controller) updateQueueIndicator(sess *SessionState) {
	count := len(sess.MessageQueue)
	if count > 0 {
		c.app.SetStatus(fmt.Sprintf("Streaming... [%d queued]", count))
	}
}

// processMessageQueue handles queued messages after streaming completes.
// Returns true if there are messages to process, starting a new stream.
func (c *Controller) processMessageQueue(sess *SessionState) bool {
	c.mu.Lock()
	if len(sess.MessageQueue) == 0 {
		c.mu.Unlock()
		return false
	}

	// Pop the first queued message
	queued := sess.MessageQueue[0]
	sess.MessageQueue = sess.MessageQueue[1:]

	// Mark as acknowledged
	queued.Acknowledged = true

	// Show acknowledgment in UI
	remaining := len(sess.MessageQueue)
	ackMsg := fmt.Sprintf("Processing queued message from %s", queued.Timestamp.Format("15:04:05"))
	if remaining > 0 {
		ackMsg += fmt.Sprintf(" (%d more queued)", remaining)
	}
	c.app.AddMessage(ackMsg, "system")
	c.mu.Unlock()

	// Start new stream for the queued message
	c.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	sess.Cancel = cancel
	sess.Streaming = true
	c.emitStreaming(sess.ID, true)
	c.mu.Unlock()
	c.app.SetStreaming(true)

	// Stream response (this will recursively process remaining queue)
	c.streamResponse(ctx, queued.Content, sess)
	return true
}
