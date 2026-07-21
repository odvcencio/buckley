// Package tui provides the integrated terminal user interface for Buckley.
package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"m31labs.dev/buckley/pkg/config"
	projectcontext "m31labs.dev/buckley/pkg/context"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/diffsignal"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/prompts"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/session"
	"m31labs.dev/buckley/pkg/skill"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/types"
	"m31labs.dev/buckley/pkg/ui/widgets"
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
	rulesEngine  *rules.Engine
	evaluator    types.RuleEvaluator
	resolver     *model.Resolver

	// Event bridge for sidebar updates
	telemetryBridge *TelemetryUIBridge

	// State
	workDir       string
	agentProfile  string
	modelOverride string

	// Multi-session support - each session runs independently
	sessions       []*SessionState // Active sessions for this project
	currentSession int             // Index into sessions
}

// QueuedMessage represents a user message queued during streaming.
type QueuedMessage struct {
	Content      string
	Timestamp    time.Time
	Acknowledged bool
	DisableTools bool
	Steering     bool
}

// SessionState holds the state for a single session.
type SessionState struct {
	ID            string
	Conversation  *conversation.Conversation
	ToolRegistry  *tool.Registry
	SkillRegistry *skill.Registry
	SkillState    *skill.RuntimeState
	Streaming     bool
	Compacting    bool
	Cancel        context.CancelFunc
	MessageQueue  []QueuedMessage // Messages queued while streaming

	DisableToolsNextTurn bool
}

// ControllerConfig configures the controller.
type ControllerConfig struct {
	Config        *config.Config
	ModelManager  *model.Manager
	Store         *storage.Store
	ProjectCtx    *projectcontext.ProjectContext
	Telemetry     *telemetry.Hub
	SessionID     string // Resume session, empty for new
	AgentProfile  string
	ModelOverride string // CLI --model override, takes precedence over routing rules
}

func newSessionState(cfg *config.Config, store *storage.Store, workDir string, hub *telemetry.Hub, sessionID string, loadMessages bool) (*SessionState, error) {
	sess := &SessionState{
		ID:           sessionID,
		Conversation: conversation.New(sessionID),
	}

	if loadMessages && store != nil {
		if err := sess.Conversation.LoadFromStorage(store); err != nil {
			return nil, fmt.Errorf("load session %s messages: %w", sessionID, err)
		}
	}

	skills := skill.NewRegistry()
	if err := skills.LoadAll(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load skills: %v\n", err)
	}

	skillState := skill.NewRuntimeState(sess.Conversation.AddSystemMessage)
	registry := buildRegistry(cfg, store, workDir, hub, sessionID)
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

	return sess, nil
}

// NewController creates a new TUI controller.
func NewController(cfg ControllerConfig) (*Controller, error) {
	if cfg.ModelManager != nil && cfg.Telemetry != nil {
		cfg.ModelManager.EnableTelemetry(cfg.Telemetry)
	}
	workDir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}

	projectSessions, currentIdx, err := loadOrCreateControllerSessions(cfg, workDir)
	if err != nil {
		return nil, err
	}

	// Determine project root
	projectRoot := workDir

	var rulesEngine *rules.Engine
	if engine, err := rules.NewDefaultEngine(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to initialize rules engine: %v\n", err)
	} else {
		rulesEngine = engine
	}
	var evaluator types.RuleEvaluator
	if rulesEngine != nil {
		evaluator = rules.NewEngineAdapter(rulesEngine)
	}
	resolver := model.NewResolver(rulesEngine, model.ResolverConfig{
		Planning:  cfg.Config.Models.Planning,
		Execution: cfg.Config.Models.Execution,
		Review:    cfg.Config.Models.Review,
	}, cfg.ModelManager)

	// Create TUI app
	app, err := NewWidgetApp(WidgetAppConfig{
		Theme:       defaultBuckleyTheme(),
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
		rulesEngine:    rulesEngine,
		evaluator:      evaluator,
		resolver:       resolver,
		workDir:        workDir,
		agentProfile:   strings.TrimSpace(cfg.AgentProfile),
		modelOverride:  strings.TrimSpace(cfg.ModelOverride),
		sessions:       projectSessions,
		currentSession: currentIdx,
	}

	// Create telemetry bridge for sidebar updates
	if cfg.Telemetry != nil {
		ctrl.telemetryBridge = NewTelemetryUIBridge(cfg.Telemetry, app)
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

func loadOrCreateControllerSessions(cfg ControllerConfig, workDir string) ([]*SessionState, int, error) {
	projectSessions, err := loadActiveProjectSessions(cfg, workDir)
	if err != nil {
		return nil, 0, err
	}
	return resolveControllerSession(cfg, workDir, projectSessions)
}

func loadActiveProjectSessions(cfg ControllerConfig, workDir string) ([]*SessionState, error) {
	var projectSessions []*SessionState
	allSessions, _ := cfg.Store.ListSessions(100)
	for _, s := range allSessions {
		if s.ProjectPath != workDir || s.Status != storage.SessionStatusActive {
			continue
		}
		sess, err := newSessionState(cfg.Config, cfg.Store, workDir, cfg.Telemetry, s.ID, true)
		if err != nil {
			return nil, err
		}
		projectSessions = append(projectSessions, sess)
	}
	return projectSessions, nil
}

func resolveControllerSession(cfg ControllerConfig, workDir string, projectSessions []*SessionState) ([]*SessionState, int, error) {
	sessionID := cfg.SessionID
	if sessionID == "" {
		if len(projectSessions) > 0 {
			return projectSessions, 0, nil
		}
		sess, err := createControllerSession(cfg, workDir, generatedControllerSessionID(workDir))
		if err != nil {
			return nil, 0, err
		}
		return []*SessionState{sess}, 0, nil
	}

	for i, s := range projectSessions {
		if s.ID == sessionID {
			return projectSessions, i, nil
		}
	}

	if len(projectSessions) > 0 {
		return projectSessions, 0, nil
	}

	sess, err := createControllerSession(cfg, workDir, sessionID)
	if err != nil {
		return nil, 0, err
	}
	return []*SessionState{sess}, 0, nil
}

func generatedControllerSessionID(workDir string) string {
	baseID := session.DetermineSessionID(workDir)
	timestamp := time.Now().Format("0102-150405") // MMDD-HHMMSS
	return fmt.Sprintf("%s-%s", baseID, timestamp)
}

func createControllerSession(cfg ControllerConfig, workDir, sessionID string) (*SessionState, error) {
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
	return newSessionState(cfg.Config, cfg.Store, workDir, cfg.Telemetry, sessionID, false)
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
		renderConversationHistory(c.app, sess.Conversation.Messages)
	}

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

	c.submitPrompt(text, true)
}

func (c *Controller) submitPrompt(text string, steering bool) {
	c.mu.Lock()

	// Get current session
	sess := c.sessions[c.currentSession]
	disableTools := shouldDisableToolsForPrompt(text)

	if sess.Compacting {
		c.mu.Unlock()
		c.app.AddMessage("Context compaction is running. Wait for it to finish before sending another message.", "system")
		return
	}

	// Steering cancels the active model request and becomes the next visible
	// turn. Explicit queueing preserves the current run and FIFO order.
	if sess.Streaming {
		sess.MessageQueue = append(sess.MessageQueue, QueuedMessage{
			Content:      text,
			Timestamp:    time.Now(),
			DisableTools: disableTools,
			Steering:     steering,
		})
		cancel := sess.Cancel
		queued := len(sess.MessageQueue)
		c.mu.Unlock()
		label := " (queued)"
		status := fmt.Sprintf("Streaming... [%d queued]", queued)
		if steering {
			label = " (steering)"
			status = fmt.Sprintf("Steering active run... [%d pending]", queued)
		}
		c.app.AddMessage(text+label, "user")
		c.app.SetStatus(status)
		if steering && cancel != nil {
			cancel()
		}
		return
	}

	// Add user message to display
	c.app.AddMessage(text, "user")
	if disableTools {
		sess.DisableToolsNextTurn = true
	}

	// Create context with cancellation for this session
	ctx, cancel := context.WithCancel(context.Background())
	sess.Cancel = cancel
	sess.Streaming = true
	c.emitStreaming(sess.ID, true)
	c.mu.Unlock()

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

	switch cmd {
	case "/new":
		c.newSession()

	case "/clear", "/reset":
		c.clearCurrentSession()

	case "/sessions", "/tabs":
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
			c.showLiveModelPicker()
		}

	case "/tokens", "/context", "/usage", "/status":
		c.showContextReport()

	case "/history":
		c.showHistory(parts[1:])

	case "/export":
		c.exportCurrentSession(parts[1:])

	case "/compact", "/summarize":
		c.compactCurrentSession()

	case "/cancel", "/stop":
		c.cancelCurrentStream()

	case "/queue":
		prompt := strings.TrimSpace(strings.TrimPrefix(text, parts[0]))
		if prompt == "" {
			c.app.AddMessage("Usage: /queue <message>", "system")
			return
		}
		c.submitPrompt(prompt, false)

	case "/steer":
		prompt := strings.TrimSpace(strings.TrimPrefix(text, parts[0]))
		if prompt == "" {
			c.app.AddMessage("Usage: /steer <message>", "system")
			return
		}
		c.submitPrompt(prompt, true)

	case "/plans":
		c.showPlans()

	case "/config":
		c.showConfigSummary()

	case "/help":
		c.app.AddMessage(`Commands:
  /new                 - Start a new session
  /clear, /reset       - Clear the current session
  /tokens, /context    - Show context, token, and tool-output budget
  /compact             - Summarize older context in the current session
  /history             - Show recent conversation turns
  /export [file]       - Export the current conversation to Markdown
  /cancel, /stop       - Cancel the current response and clear queued input
  /steer <message>     - Interrupt and redirect the active response
  /queue <message>     - Run a follow-up after the active response
  /sessions, /tabs     - List active sessions
  /next, /n            - Switch to next session
  /prev, /p            - Switch to previous session
  /model [id]          - Pick or set the execution model
  /model curate        - Curate models for ACP/editor pickers
  /skill [name|list]   - List or activate a skill
  /plans               - List saved plans
  /config              - Show active Buckley config summary
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

func (c *Controller) showLiveModelPicker() {
	if c.modelMgr == nil {
		c.app.AddMessage("Model catalog unavailable in this session.", "system")
		return
	}
	c.app.StartProcessStatus("Refreshing OpenRouter model catalog")
	go func() {
		err := c.modelMgr.RefreshProviderCatalog("openrouter")
		c.app.StopProcessStatus()
		if err != nil {
			c.app.AddMessage("OpenRouter catalog refresh failed; showing the last available catalog: "+err.Error(), "system")
		}
		c.mu.Lock()
		c.showModelPickerLocked()
		c.mu.Unlock()
	}()
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
	return buildModelPickerItems(catalog.Data, c.modelMgr, execID, planID, reviewID, curated)
}

func (c *Controller) handleModelCurate(args []string) {
	if len(args) == 0 {
		c.mu.Lock()
		c.showModelCuratePickerLocked()
		c.mu.Unlock()
		return
	}

	sub := strings.ToLower(args[0])
	switch sub {
	case "list":
		c.showCuratedModelsLocked()
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
	c.mu.Lock()
	curated := append([]string{}, c.cfg.Models.Curated...)
	c.mu.Unlock()

	if len(curated) == 0 {
		c.app.AddMessage("Curated list is empty. ACP will use execution/planning/review defaults.", "system")
		return
	}
	c.app.AddMessage("Curated models:\n- "+strings.Join(curated, "\n- "), "system")
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
	defer c.mu.Unlock()
	c.setExecutionModelLocked(modelID)
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
	c.modelOverride = modelID
	c.app.SetModelName(modelID)
	notice := "Execution model set to " + modelID
	if len(c.sessions) > 0 && c.sessions[c.currentSession].Streaming {
		notice += " (applies to the next model turn; the active request continues)"
	}
	c.app.AddMessage(notice, "system")
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
	ids := make([]string, 0, 5)
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
	add("moonshotai/kimi-k3")
	add("z-ai/glm-5.2")
	add("moonshotai/kimi-k2.7-code")
	add("qwen/qwen3.7-max")
	return ids
}

// newSession creates a new session, clearing the current conversation.
func (c *Controller) newSession() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Mark old session as completed
	oldSess := c.sessions[c.currentSession]
	if oldSess.Compacting {
		c.app.AddMessage("Context compaction is running. Wait for it to finish before starting a new session.", "system")
		return
	}
	if oldSess.Streaming {
		c.app.AddMessage("A response is still running. Use /cancel before starting a new session.", "system")
		return
	}
	if oldSess.ID != "" {
		_ = c.store.SetSessionStatus(oldSess.ID, storage.SessionStatusCompleted)
	}

	// Generate new session ID
	baseID := session.DetermineSessionID(c.workDir)
	timestamp := time.Now().Format("0102-150405")
	newSessionID := fmt.Sprintf("%s-%s", baseID, timestamp)

	// Create new session in store
	now := time.Now()
	storageSess := &storage.Session{
		ID:          newSessionID,
		ProjectPath: c.workDir,
		CreatedAt:   now,
		LastActive:  now,
		Status:      storage.SessionStatusActive,
	}
	if err := c.store.CreateSession(storageSess); err != nil {
		c.app.AddMessage("Error creating session: "+err.Error(), "system")
		return
	}

	// Create new session state and add to list
	newSess, err := newSessionState(c.cfg, c.store, c.workDir, c.telemetry, newSessionID, false)
	if err != nil {
		c.app.AddMessage("Error creating session: "+err.Error(), "system")
		return
	}
	c.sessions = append([]*SessionState{newSess}, c.sessions...)
	c.currentSession = 0
	c.conversation = newSess.Conversation
	c.registry = newSess.ToolRegistry

	// Clear scrollback and show fresh welcome
	c.app.ClearScrollback()
	c.app.WelcomeScreen()
	c.app.AddMessage("New session started: "+newSessionID, "system")
	c.app.SetStatus("Ready")
}

// buildMessagesForSession constructs the message list for the API using a specific session.
func (c *Controller) buildMessagesForSession(sess *SessionState) []model.Message {
	messages := []model.Message{}

	// System prompt
	systemPrompt := c.buildSystemPrompt(sess)
	messages = append(messages, model.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	// Add conversation history from session
	if sess != nil && sess.Conversation != nil {
		messages = append(messages, sess.Conversation.ToModelMessages()...)
	}

	return truncateModelToolMessages(messages, defaultTUIToolModelMaxBytes)
}

func truncateModelToolMessages(messages []model.Message, maxBytes int) []model.Message {
	if maxBytes <= 0 {
		return messages
	}
	for i := range messages {
		if messages[i].Role != "tool" {
			continue
		}
		content, ok := messages[i].Content.(string)
		if !ok {
			continue
		}
		messages[i].Content = truncateModelToolOutput(content, maxBytes)
	}
	return messages
}

func takePrefixBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	cut := 0
	for i := range s {
		if i > n {
			break
		}
		cut = i
	}
	if cut == 0 {
		return ""
	}
	return s[:cut]
}

func takeSuffixBytes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	start := len(s)
	for i := range s {
		if len(s)-i <= n {
			start = i
			break
		}
	}
	return s[start:]
}

// buildSystemPrompt constructs the system prompt.
func (c *Controller) buildSystemPrompt(sess *SessionState) string {
	basePrompt := prompts.DefaultToolUseSystemPrompt + "\n\nIf the user asks to create a new skill, call create_skill to save it."
	projectRaw := ""
	if c.projectCtx != nil {
		projectRaw = c.projectCtx.RawContent
	}
	skillDescriptions := ""
	if sess != nil && sess.SkillRegistry != nil {
		skillDescriptions = sess.SkillRegistry.GetDescriptions()
	}
	return prompts.BuildRuntimeSystemPrompt(prompts.RuntimePromptInput{
		Evaluator:         c.evaluator,
		BasePrompt:        basePrompt,
		AgentProfile:      c.agentProfile,
		ProjectContext:    projectRaw,
		WorkDir:           c.workDir,
		RootDir:           c.workDir,
		SkillsDescription: skillDescriptions,
		TaskType:          "coding",
		ModelTier:         model.InferModelTier(model.ResolvePhaseModel(c.cfg, c.modelMgr, c.rulesEngine, "execution", c.modelOverride)),
		GTSAvailable:      commandAvailable("gts"),
	})
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

func commandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// defaultTUIMaxOutputBytes limits tool output in TUI mode.
const defaultTUIMaxOutputBytes = 100_000

// defaultTUIToolModelMaxBytes limits each tool result sent back to the model.
const defaultTUIToolModelMaxBytes = 24 * 1024

// buildRegistry creates the tool registry with all available tools.
func buildRegistry(cfg *config.Config, store *storage.Store, workDir string, hub *telemetry.Hub, sessionID string) *tool.Registry {
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

// listSessions shows all active sessions for this project.
func (c *Controller) listSessions() {
	c.mu.Lock()
	sessions := c.sessions
	current := c.currentSession
	c.mu.Unlock()

	if len(sessions) == 0 {
		c.app.AddMessage("No active sessions", "system")
		return
	}

	var sb strings.Builder
	sb.WriteString("Active sessions:\n")
	for i, sess := range sessions {
		marker := "  "
		if i == current {
			marker = "→ "
		}
		status := ""
		if sess.Streaming {
			status = " (streaming...)"
		}
		sb.WriteString(fmt.Sprintf("%s[%d] %s%s\n", marker, i+1, sess.ID, status))
	}
	sb.WriteString("\nUse /next or /prev to switch (Alt+Right/Left)")
	c.app.AddMessage(sb.String(), "system")
}

// nextSession switches to the next session.
func (c *Controller) nextSession() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.sessions) <= 1 {
		c.app.AddMessage("No other sessions to switch to", "system")
		return
	}

	c.currentSession = (c.currentSession + 1) % len(c.sessions)
	c.switchToSessionLocked(c.currentSession)
}

// prevSession switches to the previous session.
func (c *Controller) prevSession() {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.sessions) <= 1 {
		c.app.AddMessage("No other sessions to switch to", "system")
		return
	}

	c.currentSession = (c.currentSession - 1 + len(c.sessions)) % len(c.sessions)
	c.switchToSessionLocked(c.currentSession)
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
	renderConversationHistory(c.app, sess.Conversation.Messages)

	if sess.Streaming {
		c.app.SetStatus("Streaming...")
	} else {
		c.app.SetStatus("Ready")
	}
}

// Stop gracefully stops the controller.
func (c *Controller) Stop() {
	// Stop telemetry bridge
	if c.telemetryBridge != nil {
		c.telemetryBridge.Stop()
	}

	c.mu.Lock()
	// Cancel all streaming sessions
	for _, sess := range c.sessions {
		if sess.Cancel != nil {
			sess.Cancel()
		}
		if sess.ToolRegistry != nil {
			_ = sess.ToolRegistry.Close()
		}
	}
	c.mu.Unlock()
	c.app.Quit()
}

// saveLatestConversationMessage persists the newest model-visible turn for a session.
func (c *Controller) saveLatestConversationMessage(sess *SessionState) {
	if c == nil || c.store == nil || sess == nil || sess.Conversation == nil {
		return
	}
	if len(sess.Conversation.Messages) == 0 {
		return
	}
	msg := sess.Conversation.Messages[len(sess.Conversation.Messages)-1]
	if err := sess.Conversation.SaveMessage(c.store, msg); err != nil {
		c.app.AddMessage("Error saving chat turn: "+err.Error(), "system")
	}
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

	// Shape through diffsignal: low-signal noise summarised, budget enforced.
	diff = shapeDiff(diff, diffsignal.ReviewDiffBudget)

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

	c.startSessionPrompt("/review", prompt)
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

	// Shape through diffsignal: low-signal noise summarised, budget enforced.
	diff = shapeDiff(diff, diffsignal.CommitDiffBudget)

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

	c.startSessionPrompt("/commit", prompt)
}

func (c *Controller) handleSkillCommand(args []string) {
	sess := c.currentSessionState()
	if sess == nil || sess.SkillRegistry == nil || sess.SkillState == nil {
		c.app.AddMessage("Skill system unavailable in this session.", "system")
		return
	}

	if len(args) == 0 || strings.EqualFold(args[0], "list") {
		c.app.AddMessage(formatSkillList(sess.SkillRegistry), "system")
		return
	}

	name := strings.TrimSpace(strings.Join(args, " "))
	if name == "" {
		c.app.AddMessage("Usage: /skill <name>.", "system")
		return
	}

	content, err := activateSessionSkill(sess, name)
	if err != nil {
		c.app.AddMessage(err.Error(), "system")
		return
	}
	c.app.AddMessage(content, "system")
}

func (c *Controller) currentSessionState() *SessionState {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.currentSession < 0 || c.currentSession >= len(c.sessions) {
		return nil
	}
	return c.sessions[c.currentSession]
}

func formatSkillList(registry *skill.Registry) string {
	names := make([]string, 0)
	for _, s := range registry.List() {
		names = append(names, s.GetName())
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "No skills available."
	}

	var b strings.Builder
	b.WriteString("Available skills:\n")
	for _, name := range names {
		b.WriteString("- " + name + "\n")
	}
	return strings.TrimSpace(b.String())
}

func activateSessionSkill(sess *SessionState, name string) (string, error) {
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
		return "", fmt.Errorf("Error activating skill %q: %v", name, err)
	}
	content, ok := formatSkillActivationResult(name, result)
	if !ok {
		return "", fmt.Errorf("Error activating skill %q.", name)
	}
	return content, nil
}

func formatSkillActivationResult(name string, result *builtin.Result) (string, bool) {
	if result == nil || !result.Success {
		if result != nil && result.Error != "" {
			return fmt.Sprintf("Error activating skill %q: %s", name, result.Error), true
		}
		return "", false
	}

	message, _ := result.Data["message"].(string)
	content, _ := result.Data["content"].(string)
	if content != "" && message != "" {
		return message + "\n\n" + content, true
	}
	if content != "" {
		return content, true
	}
	if message != "" {
		return message, true
	}
	return fmt.Sprintf("Skill %q activated.", name), true
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
	sess.DisableToolsNextTurn = queued.DisableTools
	// Show acknowledgment in UI
	remaining := len(sess.MessageQueue)
	ackMsg := fmt.Sprintf("Processing queued message from %s", queued.Timestamp.Format("15:04:05"))
	if queued.Steering {
		ackMsg = fmt.Sprintf("Applying steering from %s", queued.Timestamp.Format("15:04:05"))
	}
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

	// Stream response (this will recursively process remaining queue)
	c.streamResponse(ctx, queued.Content, sess)
	return true
}
