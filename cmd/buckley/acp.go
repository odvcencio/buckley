package main

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"m31labs.dev/buckley/pkg/acp"
	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/config"
	projectcontext "m31labs.dev/buckley/pkg/context"
	"m31labs.dev/buckley/pkg/conversation"
	"m31labs.dev/buckley/pkg/durability/modelstep"
	"m31labs.dev/buckley/pkg/mcp"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/prompts"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/skill"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/subagent"
	"m31labs.dev/buckley/pkg/telemetry"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
	"m31labs.dev/buckley/pkg/tooloutcome"
	"m31labs.dev/buckley/pkg/types"
)

const defaultACPSystemPrompt = prompts.DefaultToolUseSystemPrompt + "\n\nUse create_skill when the user explicitly asks to create a new skill."

const (
	acpModePrefix  = "model:"
	acpDefaultMode = "default"
)

func runACPCommand(args []string) error {
	fs := flag.NewFlagSet("acp", flag.ContinueOnError)
	workdir := fs.String("workdir", "", "Working directory (defaults to current directory)")
	logFile := fs.String("log", "", "Log file for debugging (default: no logging)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Change to workdir if specified
	if *workdir != "" {
		if err := os.Chdir(*workdir); err != nil {
			return fmt.Errorf("change to workdir: %w", err)
		}
	}

	// Set up logging if specified
	var logger *os.File
	if *logFile != "" {
		var err error
		logger, err = os.OpenFile(*logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return fmt.Errorf("open log file: %w", err)
		}
		defer logger.Close()
		fmt.Fprintf(logger, "=== ACP agent started ===\n")
	}

	logf := func(format string, args ...interface{}) {
		if logger != nil {
			fmt.Fprintf(logger, format+"\n", args...)
		}
	}

	// Initialize Buckley
	cfg, mgr, store, err := initDependenciesFn()
	if err != nil {
		logf("init error: %v", err)
		return err
	}
	defer store.Close()

	// ACP owns one process-lifetime telemetry hub. Controller lifecycle events
	// are best-effort metadata projections; they are not ACP session updates.
	telemetryHub := telemetry.NewHub()
	defer telemetryHub.Close()
	lifecycleObserver := telemetry.NewAgentLoopObserver(telemetryHub)

	// Load project context
	cwd, err := os.Getwd()
	if err != nil {
		logf("getwd error: %v", err)
		return err
	}

	loader := projectcontext.NewLoader(cwd)
	projectContext, err := loader.Load()
	if err != nil {
		logf("load context error: %v", err)
		// Non-fatal, continue without context
	}

	// Create the ACP agent. agent is declared before NewAgent so the prompt
	// handler closure (built by makePromptHandler, below) can hold a
	// pointer to this variable and see the fully constructed *acp.Agent by
	// the time a prompt actually runs -- prompts only fire once agent.Serve
	// is reading messages, well after this assignment completes.
	var agent *acp.Agent
	promptHandler, closeACPSessions := makePromptHandler(cfg, mgr, store, projectContext, cwd, logf, &agent, lifecycleObserver)
	// S5: every session that spawned its own MCP servers (via session/new's
	// mcpServers) tears them down when this ACP connection ends -- Buckley
	// does not implement session/close, so process/connection teardown is
	// the only "session end" signal available.
	defer closeACPSessions()

	agent = acp.NewAgent("Buckley", version, acp.AgentHandlers{
		OnSessionModes: func(ctx context.Context, session *acp.AgentSession) (*acp.SessionModeState, error) {
			return buildACPModelModes(cfg, mgr), nil
		},
		// S8: models are also exposed as a session config option
		// (category "model") alongside the existing modes advertisement --
		// CodeCompanion picks models ONLY via session/set_config_option, so
		// models-as-modes alone are invisible there. Both mechanisms share
		// session.Mode as their one source of truth (see
		// buildACPModelConfigOptions/applyACPSetModelConfigOption), so a
		// modes-only client and a config-option-only client never disagree
		// about which model is active. Permission modes stay as modes;
		// this only adds a second surface for the model selector.
		OnSessionConfigOptions: func(ctx context.Context, session *acp.AgentSession) ([]acp.SessionConfigOption, error) {
			return buildACPSessionConfigOptions(cfg, mgr, session), nil
		},
		OnSetConfigOption: func(ctx context.Context, session *acp.AgentSession, configID string, value acp.ConfigOptionValue) ([]acp.SessionConfigOption, error) {
			switch configID {
			case acpModelConfigID:
				return applyACPSetModelConfigOption(cfg, mgr, session, value)
			case acpTaskIntentConfigID:
				if err := applyACPSetTaskIntentConfigOption(session, value); err != nil {
					return nil, err
				}
				return buildACPSessionConfigOptions(cfg, mgr, session), nil
			default:
				return nil, fmt.Errorf("unknown config option %q", configID)
			}
		},
		// OnSessionCommands (S6) loads the skill registry fresh from disk
		// rather than reusing the per-prompt session state (getACPSessionState
		// builds that lazily on the first prompt, after session/new has
		// already returned). Skill files on disk are the same source of
		// truth either way, so this stays accurate without forcing eager
		// session setup at session/new time.
		OnSessionCommands: func(ctx context.Context, session *acp.AgentSession) ([]acp.AvailableCommand, error) {
			skills := skill.NewRegistry()
			if err := skills.LoadAll(); err != nil {
				logf("load skills warning (session commands): %v", err)
			}
			return buildACPAvailableCommands(skills), nil
		},
		OnPrompt: promptHandler,
		OnReadFile: func(ctx context.Context, path string, startLine, endLine int) (string, error) {
			logf("read file: %s (lines %d-%d)", path, startLine, endLine)
			data, err := os.ReadFile(path)
			if err != nil {
				return "", err
			}
			content := string(data)

			// Handle line ranges
			if startLine > 0 || endLine > 0 {
				lines := strings.Split(content, "\n")
				if startLine < 1 {
					startLine = 1
				}
				if endLine < 1 || endLine > len(lines) {
					endLine = len(lines)
				}
				if startLine > len(lines) {
					return "", nil
				}
				content = strings.Join(lines[startLine-1:endLine], "\n")
			}
			return content, nil
		},
		OnWriteFile: func(ctx context.Context, path string, content string) error {
			logf("write file: %s (%d bytes)", path, len(content))
			return os.WriteFile(path, []byte(content), 0644)
		},
		// OnRequestPermission is Buckley's local, risk-based fallback --
		// it never talks to the client. The live client flow is
		// session/request_permission (see requestACPToolPermission), which
		// this backs up when the client can't be reached or times out
		// (M3).
		OnRequestPermission: func(ctx context.Context, toolName, description string, args json.RawMessage, risk string) (bool, bool, error) {
			logf("permission request: %s (%s risk)", toolName, risk)
			return acpFallbackPermissionDecision(risk), false, nil
		},
	})

	// Set up signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logf("received shutdown signal")
		cancel()
	}()

	logf("serving on stdio")

	// Serve on stdin/stdout
	return agent.Serve(ctx, os.Stdin, os.Stdout)
}

func makePromptHandler(
	cfg *config.Config,
	mgr *model.Manager,
	store *storage.Store,
	projectContext *projectcontext.ProjectContext,
	defaultWorkDir string,
	logf func(string, ...interface{}),
	agentRef **acp.Agent,
	lifecycleObserver agentloop.LifecycleObserver,
) (handler func(context.Context, *acp.AgentSession, []acp.ContentBlock, acp.StreamFunc) (*acp.PromptResult, error), cleanup func()) {
	sessions := make(map[string]*acpSessionState)
	var sessionsMu sync.Mutex

	handler = func(ctx context.Context, session *acp.AgentSession, content []acp.ContentBlock, stream acp.StreamFunc) (*acp.PromptResult, error) {
		prompt := extractACPPrompt(content)
		if strings.TrimSpace(prompt) == "" {
			return nil, fmt.Errorf("empty prompt")
		}
		logf("prompt: %s", truncate(prompt, 100))

		state := getACPSessionState(ctx, &sessionsMu, sessions, session, projectContext, cfg, defaultWorkDir, logf)
		state.mu.Lock()
		defer state.mu.Unlock()

		state.conv.AddUserMessage(prompt)
		if store != nil {
			if err := state.conv.SaveMessage(store, state.conv.Messages[len(state.conv.Messages)-1]); err != nil {
				logf("save user message warning: %v", err)
			}
		}

		if handled, responseText := handleACPUserSkillCommand(prompt, state); handled {
			if responseText != "" {
				state.conv.AddAssistantMessage(responseText)
				stream(acp.NewAgentMessageChunk(responseText))
			}
			return &acp.PromptResult{StopReason: "end_turn"}, nil
		}

		var agent *acp.Agent
		if agentRef != nil {
			agent = *agentRef
		}

		modelOverride := resolveACPModelOverride(cfg, mgr, session.Mode)
		// S6: create_skill (reachable as a tool call inside runACPLoop) is
		// the only thing that changes the set of available commands
		// mid-session; a before/after count catches it without threading
		// the skill registry through the whole tool-call loop.
		skillsBefore := 0
		if state.skills != nil {
			skillsBefore = len(state.skills.List())
		}

		// S1: runACPLoop streams accepted messages as agent_message_chunk
		// notifications while the model generates them (see streamACPTurn).
		// On incomplete turns, however, retained public text can return after
		// buffered delivery was discarded; stream that text only when the
		// loop's delivery metadata says this candidate was not attempted.
		taskIntent, intentErr := consumeACPNextTaskIntent(session)
		if intentErr != nil {
			return nil, intentErr
		}
		if taskIntent != agentloop.UnknownIntent && stream != nil {
			stream(acp.NewConfigOptionUpdate(buildACPSessionConfigOptions(cfg, mgr, session)))
		}
		turnID := state.nextLifecycleTurnID()
		text, err := runACPLoopWithLimits(ctx, cfg, mgr, state.conv, state.registry, state.skillState, state.engine, modelOverride, state.workDir, session.ID, agent, logf, stream, acpLoopLimits{
			LifecycleObserver:  lifecycleObserver,
			LifecycleSessionID: state.lifecycleSessionID,
			TurnID:             turnID,
			TaskIntent:         taskIntent,
		})
		if err != nil {
			projected := newACPProjectedError(err)
			logACPProjectedPromptError(logf, projected)
			if stream != nil && shouldStreamACPIncompleteDraft(text, err) {
				stream(acp.NewAgentMessageChunk("\n\nIncomplete draft (not accepted):\n\n" + text))
			}
			if stream != nil {
				if notice := agentloop.PresentIncompleteResult(err); notice.Code != "incomplete_turn" || notice.Reason != "the turn stopped before a conclusive answer was produced" {
					stream(acp.NewAgentMessageChunk("\n\n" + notice.Message))
				} else {
					stream(acp.NewAgentMessageChunk("\n\nError: " + projected.Error()))
				}
			}
			return nil, projected
		}

		if state.skills != nil && len(state.skills.List()) != skillsBefore {
			sendACPAvailableCommandsUpdate(stream, state.skills)
		}

		return &acp.PromptResult{StopReason: "end_turn"}, nil
	}

	cleanup = func() {
		sessionsMu.Lock()
		defer sessionsMu.Unlock()
		for id, state := range sessions {
			if state == nil || state.mcpManager == nil {
				continue
			}
			if err := state.mcpManager.Close(); err != nil {
				logf("mcp: session %s: close error: %v", id, err)
			}
		}
	}

	return handler, cleanup
}

func shouldStreamACPIncompleteDraft(text string, err error) bool {
	if strings.TrimSpace(text) == "" || err == nil {
		return false
	}
	var draft *acpIncompleteDraftError
	if !errors.As(err, &draft) || draft == nil {
		return false
	}
	return !draft.deliveryAttempted
}

type acpSessionState struct {
	mu                      sync.Mutex
	conv                    *conversation.Conversation
	registry                *tool.Registry
	skills                  *skill.Registry
	skillState              *skill.RuntimeState
	engine                  *rules.Engine
	lifecycleSessionID      string
	lifecycleTurnGeneration uint64
	// workDir is the session's working directory, used to resolve tool-call
	// locations and diff paths -- never the ACP process's own cwd, which may
	// differ from the editor's session/new cwd (S2, S9).
	workDir string
	// mcpManager holds the session's own MCP servers, spawned from
	// session/new's mcpServers array (S5). Nil when the session declared no
	// (supported) MCP servers. Torn down by makePromptHandler's cleanup
	// func when the ACP connection ends.
	mcpManager *mcp.Manager
}

// nextLifecycleTurnID is called while the session mutex is held. A prompt's
// controller and all of its continuation/nudge Run calls share this ID; the
// next prompt advances the generation and therefore starts a new sequence
// scope without losing the stable ACP session lineage.
func (s *acpSessionState) nextLifecycleTurnID() string {
	if s == nil {
		return ""
	}
	s.lifecycleTurnGeneration++
	return "turn_" + runledger.StableEventID(
		"acp-lifecycle-turn",
		s.lifecycleSessionID,
		strconv.FormatUint(s.lifecycleTurnGeneration, 10),
	)
}

func acpLifecycleSessionID(sessionID string) string {
	return "acp_" + runledger.StableEventID("acp-lifecycle-session", sessionID)
}

// acpProjectedError keeps the raw cause available to in-process classifiers
// while exposing only a bounded, redacted string at the ACP protocol boundary.
type acpProjectedError struct {
	cause      error
	projection string
}

func newACPProjectedError(cause error) *acpProjectedError {
	projection := modelstep.NormalizeError(cause)
	if projection == "" {
		projection = "request failed"
	}
	return &acpProjectedError{cause: cause, projection: projection}
}

func (e *acpProjectedError) Error() string {
	if e == nil || e.projection == "" {
		return "request failed"
	}
	return e.projection
}

func (e *acpProjectedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// logACPProjectedPromptError accepts only the already-sanitized boundary
// value, so a caller cannot accidentally hand the debug logger a raw provider
// chain containing credentials or an unbounded response body.
func logACPProjectedPromptError(logf func(string, ...interface{}), projected *acpProjectedError) {
	if logf == nil || projected == nil {
		return
	}
	logf("prompt error: %s", projected.Error())
}

type acpEmbeddedResource struct {
	URI      string `json:"uri"`
	Text     string `json:"text"`
	Blob     string `json:"blob"`
	MimeType string `json:"mimeType"`
}

func extractACPPrompt(blocks []acp.ContentBlock) string {
	var parts []string
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
		case "resource_link":
			name := strings.TrimSpace(block.Title)
			if name == "" {
				name = strings.TrimSpace(block.Name)
			}
			if name == "" {
				name = "Resource"
			}
			if block.URI != "" {
				parts = append(parts, fmt.Sprintf("%s: %s", name, block.URI))
			}
		case "resource":
			if len(block.Resource) == 0 {
				continue
			}
			var embedded acpEmbeddedResource
			if err := json.Unmarshal(block.Resource, &embedded); err != nil {
				continue
			}
			if strings.TrimSpace(embedded.Text) != "" {
				label := embedded.URI
				if label == "" {
					label = embedded.MimeType
				}
				if label != "" {
					parts = append(parts, fmt.Sprintf("Resource (%s):\n%s", label, embedded.Text))
				} else {
					parts = append(parts, embedded.Text)
				}
			} else if embedded.URI != "" {
				parts = append(parts, fmt.Sprintf("Resource: %s", embedded.URI))
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func getACPSessionState(
	ctx context.Context,
	mu *sync.Mutex,
	sessions map[string]*acpSessionState,
	session *acp.AgentSession,
	projectContext *projectcontext.ProjectContext,
	cfg *config.Config,
	defaultWorkDir string,
	logf func(string, ...interface{}),
) *acpSessionState {
	if mu == nil || sessions == nil {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()

	if state, ok := sessions[session.ID]; ok {
		return state
	}

	workDir := strings.TrimSpace(session.WorkingDirectory)
	if workDir == "" {
		workDir = strings.TrimSpace(defaultWorkDir)
	}

	conv := conversation.New(session.ID)
	skills := skill.NewRegistry()
	if err := skills.LoadAll(); err != nil && logf != nil {
		logf("load skills warning: %v", err)
	}
	skillState := skill.NewRuntimeState(conv.AddSystemMessage)

	registry := tool.NewRegistry()
	tool.ApplyToolMiddlewareConfig(registry, cfg)
	if err := registry.LoadDefaultPlugins(); err != nil && logf != nil {
		logf("load plugins warning: %v", err)
	}
	if workDir != "" {
		registry.ConfigureContainers(cfg, workDir)
		registry.SetWorkDir(workDir)
	}
	registry.Register(&builtin.SkillActivationTool{
		Registry:     skills,
		Conversation: skillState,
	})
	createTool := &builtin.CreateSkillTool{Registry: skills}
	if strings.TrimSpace(workDir) != "" {
		createTool.SetWorkDir(workDir)
	}
	registry.Register(createTool)
	registry.EnableDynamicDiscovery(nil)

	// S5: spawn the session's own MCP servers (session/new's mcpServers,
	// previously parsed then discarded) and bridge their tools into this
	// session's registry.
	mcpManager := attachACPMcpServers(ctx, registry, session.McpServers, logf)

	// Wire todo persistence for the ACP session
	registry.SetTodoStore(&acpTodoStoreAdapter{sessionID: session.ID})

	var engine *rules.Engine
	if e, err := rules.NewDefaultEngine(); err != nil {
		if logf != nil {
			logf("rules engine warning: %v", err)
		}
	} else {
		engine = e
	}
	if candidate, ok := registry.Get("spawn_subagent"); ok {
		if subagents, ok := candidate.(*builtin.SubagentTool); ok {
			subagents.SetEvaluator(newACPEvaluator(engine))
		}
	}

	conv.AddSystemMessage(buildACPSystemPrompt(projectContext, workDir, skills, engine, "", hyphaeProjectKnowledgeContext(cfg, workDir)))

	state := &acpSessionState{
		conv:               conv,
		registry:           registry,
		skills:             skills,
		skillState:         skillState,
		engine:             engine,
		lifecycleSessionID: acpLifecycleSessionID(session.ID),
		workDir:            workDir,
		mcpManager:         mcpManager,
	}
	sessions[session.ID] = state
	return state
}

// attachACPMcpServers spawns and bridges the stdio MCP servers a client
// declared in session/new's mcpServers array (S5). Only the stdio
// transport is supported -- Buckley advertises mcpCapabilities.http/sse as
// false at initialize (see handleInitialize), so http/sse declarations are
// logged and skipped rather than attempted. A server that fails to connect
// is logged and skipped too: one misconfigured server must not block the
// rest of the session's tools, MCP or otherwise. Returns nil when no
// (supported) server was configured or none connected.
func attachACPMcpServers(ctx context.Context, registry *tool.Registry, declared []acp.McpServer, logf func(string, ...interface{})) *mcp.Manager {
	if len(declared) == 0 {
		return nil
	}

	mcpCfg := config.MCPConfig{Enabled: true}
	for _, srv := range declared {
		name := strings.TrimSpace(srv.Name)
		if srv.Type != acp.McpServerKindStdio {
			if logf != nil {
				logf("mcp: server %q declares unsupported transport %q (only stdio is supported); skipping", name, srv.Type)
			}
			continue
		}
		if name == "" || strings.TrimSpace(srv.Command) == "" {
			if logf != nil {
				logf("mcp: skipping server with missing name or command: %+v", srv)
			}
			continue
		}
		env := make(map[string]string, len(srv.Env))
		for _, e := range srv.Env {
			env[e.Name] = e.Value
		}
		mcpCfg.Servers = append(mcpCfg.Servers, config.MCPServerConfig{
			Name:    name,
			Command: srv.Command,
			Args:    append([]string{}, srv.Args...),
			Env:     config.ExpandMCPEnv(env),
			Enabled: true,
		})
	}
	if len(mcpCfg.Servers) == 0 {
		return nil
	}

	manager, err := mcp.ManagerFromConfig(ctx, mcpCfg)
	if err != nil && logf != nil {
		logf("mcp: session server setup: %v", err)
	}
	if manager == nil {
		return nil
	}

	registered := tool.RegisterMCPTools(registry, manager, mcpCfg)
	if logf != nil {
		logf("mcp: bridged %d tool(s) from %d session server(s): %v", len(registered), len(mcpCfg.Servers), registered)
	}
	return manager
}

func buildACPSystemPrompt(projectContext *projectcontext.ProjectContext, workDir string, skills *skill.Registry, engine *rules.Engine, agentProfile, knowledgeContext string) string {
	var evaluator *rules.EngineAdapter
	if engine != nil {
		evaluator = rules.NewEngineAdapter(engine)
	}

	projectRaw := ""
	if projectContext != nil {
		projectRaw = projectContext.RawContent
	}
	skillDescriptions := ""
	if skills != nil {
		skillDescriptions = skills.GetDescriptions()
	}

	return prompts.BuildRuntimeSystemPrompt(prompts.RuntimePromptInput{
		Evaluator:         evaluator,
		BasePrompt:        defaultACPSystemPrompt,
		AgentProfile:      agentProfile,
		ProjectContext:    projectRaw,
		KnowledgeContext:  knowledgeContext,
		WorkDir:           workDir,
		RootDir:           workDir,
		SkillsDescription: skillDescriptions,
		TaskType:          "coding",
	})
}

type acpUserSkillCommand struct {
	list bool
	name string
}

func handleACPUserSkillCommand(prompt string, state *acpSessionState) (bool, string) {
	if handled, text := handleACPSkillNameCommand(prompt, state); handled {
		return true, text
	}
	cmd, ok := parseACPUserSkillCommand(prompt)
	if !ok {
		return false, ""
	}
	if state == nil || state.skills == nil {
		return true, "Skill system unavailable in this session."
	}
	if cmd.list {
		return true, formatACPAvailableSkills(state.skills)
	}
	if cmd.name == "" {
		return true, "Usage: /skill <name>."
	}
	return true, activateACPUserSkill(cmd.name, state)
}

// handleACPSkillNameCommand recognizes "/<skill-name>" as shorthand for
// "/skill <skill-name>" (S6): buildACPAvailableCommands advertises each
// registered skill as its own named command, so a client's command
// palette can invoke "/code-review" directly rather than the generic
// "/skill code-review" form -- otherwise an advertised command would not
// actually do anything when picked. It returns false for anything other
// than a single "/<token>" that names a registered skill, so ordinary
// prose starting with "/" and the generic "/skill"/"/skills" commands
// fall through to parseACPUserSkillCommand unchanged.
func handleACPSkillNameCommand(prompt string, state *acpSessionState) (bool, string) {
	if state == nil || state.skills == nil {
		return false, ""
	}
	trimmed := strings.TrimSpace(prompt)
	if !strings.HasPrefix(trimmed, "/") || strings.ContainsAny(trimmed, " \t\n") {
		return false, ""
	}
	name := strings.TrimPrefix(trimmed, "/")
	if name == "" || strings.EqualFold(name, "skill") || strings.EqualFold(name, "skills") {
		return false, ""
	}
	if state.skills.GetSkill(name) == nil {
		return false, ""
	}
	return true, activateACPUserSkill(name, state)
}

func parseACPUserSkillCommand(prompt string) (acpUserSkillCommand, bool) {
	parts := strings.Fields(strings.TrimSpace(prompt))
	if len(parts) == 0 {
		return acpUserSkillCommand{}, false
	}
	cmd := strings.ToLower(parts[0])
	if cmd != "/skill" && cmd != "/skills" {
		return acpUserSkillCommand{}, false
	}
	if cmd == "/skills" || len(parts) == 1 || strings.EqualFold(parts[1], "list") {
		return acpUserSkillCommand{list: true}, true
	}
	return acpUserSkillCommand{name: strings.TrimSpace(strings.Join(parts[1:], " "))}, true
}

func formatACPAvailableSkills(registry *skill.Registry) string {
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

// buildACPAvailableCommands converts a skill registry into the ACP
// available_commands_update list (S6): one command per registered skill,
// named after the skill itself so a client's command palette can invoke it
// directly (handleACPSkillNameCommand recognizes "/<skill-name>" the same
// way it recognizes the generic "/skill <name>" form).
func buildACPAvailableCommands(registry *skill.Registry) []acp.AvailableCommand {
	if registry == nil {
		return nil
	}
	list := registry.List()
	if len(list) == 0 {
		return nil
	}
	commands := make([]acp.AvailableCommand, 0, len(list))
	for _, s := range list {
		name := strings.TrimSpace(s.GetName())
		if name == "" {
			continue
		}
		commands = append(commands, acp.AvailableCommand{
			Name:        name,
			Description: s.GetDescription(),
		})
	}
	if len(commands) == 0 {
		return nil
	}
	sort.Slice(commands, func(i, j int) bool { return commands[i].Name < commands[j].Name })
	return commands
}

// sendACPAvailableCommandsUpdate builds and sends an
// available_commands_update notification (S6) from registry's current
// skill set.
func sendACPAvailableCommandsUpdate(stream acp.StreamFunc, registry *skill.Registry) {
	if stream == nil {
		return
	}
	_ = stream(acp.NewAvailableCommandsUpdate(buildACPAvailableCommands(registry)))
}

func activateACPUserSkill(name string, state *acpSessionState) string {
	tool := &builtin.SkillActivationTool{
		Registry:     state.skills,
		Conversation: state.skillState,
	}
	result, err := tool.Execute(map[string]any{
		"action": "activate",
		"skill":  name,
		"scope":  "user request",
	})
	if err != nil {
		return fmt.Sprintf("Error activating skill %q: %v", name, err)
	}
	if result == nil || !result.Success {
		if result != nil && result.Error != "" {
			return fmt.Sprintf("Error activating skill %q: %s", name, result.Error)
		}
		return fmt.Sprintf("Error activating skill %q.", name)
	}
	return formatACPSkillActivationResult(name, result.Data)
}

func formatACPSkillActivationResult(name string, data map[string]any) string {
	message, _ := data["message"].(string)
	content, _ := data["content"].(string)
	if content != "" && message != "" {
		return message + "\n\n" + content
	}
	if content != "" {
		return content
	}
	if message != "" {
		return message
	}
	return fmt.Sprintf("Skill %q activated.", name)
}

func buildACPModelModes(cfg *config.Config, mgr *model.Manager) *acp.SessionModeState {
	curated := curatedModelIDs(cfg, mgr)
	if len(curated) == 0 {
		return nil
	}

	modes := make([]acp.SessionMode, 0, len(curated))
	for _, modelID := range curated {
		if strings.TrimSpace(modelID) == "" {
			continue
		}
		name := modelID
		desc := modelID
		if mgr != nil {
			if info, err := mgr.GetModelInfo(modelID); err == nil {
				if strings.TrimSpace(info.Name) != "" {
					name = info.Name
				}
				if strings.TrimSpace(info.Description) != "" {
					desc = info.Description
				}
			}
		}

		modes = append(modes, acp.SessionMode{
			ID:          acpModePrefix + modelID,
			Name:        name,
			Description: desc,
		})
	}
	if len(modes) == 0 {
		return nil
	}

	current := modes[0].ID
	return &acp.SessionModeState{
		CurrentModeID:  current,
		AvailableModes: modes,
	}
}

func curatedModelIDs(cfg *config.Config, mgr *model.Manager) []string {
	var base []string
	if cfg != nil && len(cfg.Models.Curated) > 0 {
		base = append([]string{}, cfg.Models.Curated...)
	} else if cfg != nil {
		base = []string{
			cfg.Models.Execution,
			cfg.Models.Planning,
			cfg.Models.Review,
		}
	}

	execID := ""
	if cfg != nil {
		execID = strings.TrimSpace(cfg.Models.Execution)
	}
	if execID != "" && (len(base) == 0 || strings.TrimSpace(base[0]) != execID) {
		base = append([]string{execID}, base...)
	}

	return filterCuratedModels(base, mgr)
}

func filterCuratedModels(ids []string, mgr *model.Manager) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		if mgr != nil && !acpCatalogHasModel(mgr, id) {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if len(out) == 0 && len(ids) > 0 {
		id := strings.TrimSpace(ids[0])
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

func acpCatalogHasModel(mgr *model.Manager, modelID string) bool {
	if mgr == nil {
		return true
	}
	catalog := mgr.GetCatalog()
	if catalog == nil {
		return true
	}
	for _, info := range catalog.Data {
		if info.ID == modelID {
			return true
		}
	}
	return false
}

func resolveACPModelOverride(cfg *config.Config, mgr *model.Manager, modeID string) string {
	modeID = strings.TrimSpace(modeID)
	if modeID == "" || modeID == acpDefaultMode {
		return ""
	}
	if strings.HasPrefix(modeID, acpModePrefix) {
		modeID = strings.TrimPrefix(modeID, acpModePrefix)
	}
	if mgr != nil && !acpCatalogHasModel(mgr, modeID) {
		return ""
	}
	return modeID
}

// acpModelConfigID is the SessionConfigOption.ID for Buckley's model
// picker (S8) -- the ID a client sends back as configId on
// session/set_config_option.
const acpModelConfigID = "model"
const acpTaskIntentConfigID = "task_intent"
const acpTaskIntentEnvKey = "buckley.task_intent"

func buildACPSessionConfigOptions(cfg *config.Config, mgr *model.Manager, session *acp.AgentSession) []acp.SessionConfigOption {
	options := buildACPModelConfigOptions(cfg, mgr, session)
	options = append(options, buildACPTaskIntentConfigOption(acpSessionTaskIntent(session)))
	return options
}

func acpSessionTaskIntent(session *acp.AgentSession) agentloop.TaskIntent {
	intent, err := acpSessionTaskIntentStrict(session)
	if err != nil {
		return agentloop.UnknownIntent
	}
	return intent
}

func acpSessionTaskIntentStrict(session *acp.AgentSession) (agentloop.TaskIntent, error) {
	if session == nil || session.Environment == nil {
		return agentloop.UnknownIntent, nil
	}
	intent, err := agentloop.ParseTaskIntent(session.Environment[acpTaskIntentEnvKey])
	if err != nil {
		return "", err
	}
	return intent, nil
}

func consumeACPNextTaskIntent(session *acp.AgentSession) (agentloop.TaskIntent, error) {
	intent, err := acpSessionTaskIntentStrict(session)
	if err != nil {
		return "", err
	}
	if session != nil && session.Environment != nil {
		delete(session.Environment, acpTaskIntentEnvKey)
	}
	return intent, nil
}

func buildACPTaskIntentConfigOption(current agentloop.TaskIntent) acp.SessionConfigOption {
	if current == "" {
		current = agentloop.UnknownIntent
	}
	values := []acp.SessionConfigSelectOption{
		{Value: string(agentloop.UnknownIntent), Name: "Automatic", Description: "Use the default completion rules for open-ended chat."},
		{Value: string(agentloop.ReadOnlyIntent), Name: "No change required", Description: "A useful answer can complete the next request without requiring an edit."},
		{Value: string(agentloop.MutationIntent), Name: "Change required", Description: "The next request is complete only after an observable change and verification."},
	}
	return acp.SessionConfigOption{
		ID:           acpTaskIntentConfigID,
		Name:         "Next request",
		Description:  "Result contract for the next prompt only.",
		Category:     acp.SessionConfigCategoryMode,
		Type:         acp.SessionConfigKindSelect,
		CurrentValue: string(current),
		Options:      values,
	}
}

func applyACPSetTaskIntentConfigOption(session *acp.AgentSession, value acp.ConfigOptionValue) error {
	raw := strings.TrimSpace(value.ValueID)
	if raw == "" {
		return fmt.Errorf("task intent config option requires a value id: unknown, read_only, or mutation")
	}
	intent, err := agentloop.ParseTaskIntent(raw)
	if err != nil {
		return err
	}
	if session == nil {
		return nil
	}
	if session.Environment == nil {
		session.Environment = make(map[string]string)
	}
	session.Environment[acpTaskIntentEnvKey] = string(intent)
	return nil
}

// buildACPModelConfigOptions builds the "model" SessionConfigOption (S8):
// a select-style config option listing every curated model, alongside the
// existing modes-based model selector (buildACPModelModes). Both read the
// same curated list and both read/write session.Mode as their one shared
// source of truth for "which model is active", so a config-option-only
// client (CodeCompanion) and a modes-only client never disagree.
func buildACPModelConfigOptions(cfg *config.Config, mgr *model.Manager, session *acp.AgentSession) []acp.SessionConfigOption {
	curated := curatedModelIDs(cfg, mgr)
	if len(curated) == 0 {
		return nil
	}

	options := make([]acp.SessionConfigSelectOption, 0, len(curated))
	for _, modelID := range curated {
		if strings.TrimSpace(modelID) == "" {
			continue
		}
		name := modelID
		desc := ""
		if mgr != nil {
			if info, err := mgr.GetModelInfo(modelID); err == nil {
				if strings.TrimSpace(info.Name) != "" {
					name = info.Name
				}
				desc = info.Description
			}
		}
		options = append(options, acp.SessionConfigSelectOption{Value: modelID, Name: name, Description: desc})
	}
	if len(options) == 0 {
		return nil
	}

	// Only trust session.Mode as a model selection when it actually carries
	// the "model:" prefix -- resolveACPModelOverride treats any other
	// non-empty, non-"default" string as a literal model override, and
	// AgentSession's own zero-state default ("normal") is neither empty
	// nor "default", so it must never reach that path.
	current := options[0].Value
	if session != nil && strings.HasPrefix(session.Mode, acpModePrefix) {
		if override := resolveACPModelOverride(cfg, mgr, session.Mode); override != "" {
			current = override
		}
	}

	return []acp.SessionConfigOption{acp.NewModelConfigOption(acpModelConfigID, "Model", current, options)}
}

// applyACPSetModelConfigOption handles a session/set_config_option change
// to the "model" option (S8): it validates the chosen model against the
// curated catalog and writes session.Mode in place -- the same field
// resolveACPModelOverride reads for every turn -- so the change takes
// effect immediately regardless of which selector (modes or config
// options) the client uses.
func applyACPSetModelConfigOption(cfg *config.Config, mgr *model.Manager, session *acp.AgentSession, value acp.ConfigOptionValue) ([]acp.SessionConfigOption, error) {
	modelID := strings.TrimSpace(value.ValueID)
	if modelID == "" {
		return nil, fmt.Errorf("model config option requires a value id")
	}
	if mgr != nil && !acpCatalogHasModel(mgr, modelID) {
		return nil, fmt.Errorf("unknown model %q", modelID)
	}
	if session != nil {
		session.Mode = acpModePrefix + modelID
	}
	return buildACPModelConfigOptions(cfg, mgr, session), nil
}

// acpLoopState carries the per-prompt-turn state the Controller hooks
// share: whether tools are still offered (a tools-unsupported provider
// error clears it mid-turn), the tool filter the current round advertised,
// whether any round has executed tools yet (the finalize-nudge gate), and
// the last phase update sent (sendACPPhaseUpdate dedupes on it).
type acpLoopState struct {
	useTools        bool
	toolTurnEnabled bool
	allowedTools    []string
	// route is resolved once after execution-model selection. Every request
	// built during this prompt carries it so streaming dispatch can reject a
	// hook/routing change before an upstream provider sees the request.
	route model.ModelRoute
	// toolsCatalogConfirmedUnavailable distinguishes an intentional catalog
	// decision from the reactive provider tool-unsupported fallback below.
	toolsCatalogConfirmedUnavailable bool
	// requestHadToolSchemas records the exact just-completed request. A model
	// can emit a structured call even when schemas were omitted; that output is
	// retained as a failed control outcome, never executed locally.
	requestHadToolSchemas bool
	toolsExecuted         bool
	lastPhase             string
	codeModeRecovery      *tool.CodeModeRecoveryState
	childMailbox          *subagent.FileMailboxReader
	// contextWindow is resolved once per prompt turn (the model does not
	// change mid-turn) and paired with each round's model.Usage to report
	// usage_update as "tokens used out of this context window" (N1).
	contextWindow int
	// Cost-bounded turns hold provider deltas and usage until either Controller
	// accepts a tool-bearing assistant message or the ACP loop accepts a final
	// no-tool candidate. A rejected/nudged response is never presented to the
	// ACP client as accepted assistant output.
	pendingModelUpdates           []acp.SessionUpdate
	pendingModelUsage             *model.Usage
	currentDraftDeliveryAttempted bool
	currentObservedToolDelta      bool
}

// runACPLoop drives one ACP prompt turn through the shared turn engine
// (pkg/agentloop.Controller): the engine owns the round loop, tool-call ID
// backfill, history ordering, and -- new with this migration -- a Governor
// backstop the hand-rolled loop never had, so a repeating tool loop now
// stops with an explanation instead of running until the client cancels.
// The ACP-specific pieces (per-delta streaming (S1), per-round usage
// updates (N1), phase updates, the client permission flow, and the
// tool-use/finalize nudges) stay exactly as they were, wired in as
// Controller hooks or applied between Controller.Run attempts. See
// newACPLoopController.
func runACPLoop(
	ctx context.Context,
	cfg *config.Config,
	mgr *model.Manager,
	conv *conversation.Conversation,
	registry *tool.Registry,
	skillState *skill.RuntimeState,
	engine *rules.Engine,
	modelOverride string,
	workDir string,
	sessionID string,
	agent *acp.Agent,
	logf func(string, ...interface{}),
	stream acp.StreamFunc,
) (string, error) {
	return runACPLoopWithStepCap(ctx, cfg, mgr, conv, registry, skillState, engine, modelOverride, workDir, sessionID, agent, logf, stream, 0)
}

type acpLoopLimits struct {
	ValidateFinalResponse func(string) error
	SubmittedResponse     func() (string, bool)
	RequiredSourceText    []string
	SourceScope           *agentcoord.SourceScope
	// executionRoute is supplied only by a one-shot output contract that
	// negotiated from this exact route. ACP otherwise resolves its own route.
	executionRoute          model.ModelRoute
	FinalizationInstruction string
	StepCap                 int
	MaxOutputTokens         int
	ProtocolReasoning       *model.ReasoningConfig
	MaxToolCalls            int
	MaxModelRequests        int
	ReadOnlyWarningAt       int
	ReadOnlyActionAt        int
	MaxReadOnlyCalls        int
	VerificationDepth       string
	MaxVerificationAttempts int
	MaxElapsedSeconds       int
	MaxCostUSD              float64
	LifecycleObserver       agentloop.LifecycleObserver
	LifecycleSessionID      string
	TurnID                  string
	RunID                   string
	ParentRunID             string
	TaskID                  string
	ParentSessionID         string
	ChildContract           bool
	TaskIntent              agentloop.TaskIntent
}

// runACPLoopWithStepCap is the internal one-shot variant that applies a
// resolved persona's iteration ceiling. ACP sessions use the zero-value cap
// and therefore retain their existing behavior.
func runACPLoopWithStepCap(
	ctx context.Context,
	cfg *config.Config,
	mgr *model.Manager,
	conv *conversation.Conversation,
	registry *tool.Registry,
	skillState *skill.RuntimeState,
	engine *rules.Engine,
	modelOverride string,
	workDir string,
	sessionID string,
	agent *acp.Agent,
	logf func(string, ...interface{}),
	stream acp.StreamFunc,
	stepCap int,
) (string, error) {
	return runACPLoopWithLimits(ctx, cfg, mgr, conv, registry, skillState, engine, modelOverride, workDir, sessionID, agent, logf, stream, acpLoopLimits{StepCap: stepCap})
}

func runACPLoopWithLimits(
	ctx context.Context,
	cfg *config.Config,
	mgr *model.Manager,
	conv *conversation.Conversation,
	registry *tool.Registry,
	skillState *skill.RuntimeState,
	engine *rules.Engine,
	modelOverride string,
	workDir string,
	sessionID string,
	agent *acp.Agent,
	logf func(string, ...interface{}),
	stream acp.StreamFunc,
	limits acpLoopLimits,
) (text string, runErr error) {
	modelID, modelErr := resolveACPExecutionModel(cfg, mgr, engine, modelOverride)
	if modelErr != nil {
		return "", modelErr
	}
	route := limits.executionRoute
	if route != (model.ModelRoute{}) {
		if strings.TrimSpace(route.RequestedModel) == "" || strings.TrimSpace(route.SelectedModel) == "" || strings.TrimSpace(route.ProviderID) == "" {
			return "", fmt.Errorf("retained execution route requires requested model, selected model, and provider")
		}
		if route.RequestedModel != modelID {
			return "", fmt.Errorf("retained execution route requested model %q does not match resolved execution model %q", route.RequestedModel, modelID)
		}
	} else {
		var routeErr error
		route, routeErr = mgr.ResolveModelRoute(modelID)
		if routeErr != nil {
			return "", routeErr
		}
	}
	childMailbox, mailboxPresent, err := subagent.OpenChildMailboxFromEnv()
	if err != nil {
		return "", err
	}
	if mailboxPresent {
		defer childMailbox.Close()
	}
	state := &acpLoopState{
		useTools:                         acpModelCanUseTools(registry, mgr, route),
		route:                            route,
		toolsCatalogConfirmedUnavailable: mgr.ToolsCatalogConfirmedUnavailableForRoute(route),
		codeModeRecovery:                 &tool.CodeModeRecoveryState{},
		childMailbox:                     childMailbox,
	}
	if mgr != nil {
		state.contextWindow, _ = mgr.GetContextLengthForRoute(route)
	}

	ctrl, err := newACPLoopController(cfg, mgr, conv, registry, skillState, engine, modelID, workDir, sessionID, agent, logf, stream, state, limits)
	if err != nil {
		return "", err
	}
	var lifecycleResult *agentloop.Result
	defer func() {
		discardPendingACPModelDelivery(state)
		ctrl.CompleteLifecycleTurn(lifecycleResult, runErr)
	}()

	nudgeCount := 0
	toolMarkupRecoveryCount := 0
	finalizeNudgeCount := 0
	// The nudge and tools-unsupported paths re-run the same Controller:
	// its per-turn state lives in state and the Governor, so a re-run
	// continues the turn (round count included) rather than starting over.
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		result, err := ctrl.Run(ctx)
		lifecycleResult = result
		if err != nil {
			var incomplete *agentloop.IncompleteTurnError
			if errors.As(err, &incomplete) && result != nil {
				return result.Content, acpIncompleteDraftErrorForResult(result.Content, err, result, state, registry)
			}
			var partial *partialStreamTurnError
			if errors.As(err, &partial) {
				return partial.text, acpIncompleteDraftErrorForPartial(partial, err, state, registry)
			}
			if state.useTools && model.IsToolUnsupportedError(err) {
				state.useTools = false
				continue
			}
			return "", err
		}

		if completionErr := result.RequireConclusive(); completionErr != nil {
			return result.Content, acpIncompleteDraftErrorForResult(result.Content, completionErr, result, state, registry)
		}
		harnessFinal := result.FinishReason == agentloop.FinishReasonLoopGuard || result.FinishReason == agentloop.FinishReasonStepCap
		switch result.FinishReason {
		case agentloop.FinishReasonEmptyChoices:
			// streamACPTurn already turns an empty stream into
			// model.NoResponseChoicesError before Controller sees it; this
			// branch is defensive.
			return "", fmt.Errorf("model returned an empty response")
		}
		if !harnessFinal {
			commands, err := drainChildMailbox(conv, state.childMailbox)
			if err != nil {
				return "", err
			}
			if commands > 0 {
				continue
			}
		}

		msg := result.Message
		text, err := model.ExtractTextContent(msg.Content)
		if err != nil {
			return "", err
		}
		if !harnessFinal && strings.TrimSpace(text) == "" && state.toolsExecuted {
			if finalizeNudgeCount == 0 {
				finalizeNudgeCount++
				conv.AddUserMessage("Return the final answer now using the completed tool results. Do not leave the response empty.")
				continue
			}
			return "", fmt.Errorf("model returned an empty final response after tool execution")
		}
		if toolName, attempted := acpToolInvocationMarkup(text, registry); attempted {
			if !harnessFinal && toolMarkupRecoveryCount == 0 {
				toolMarkupRecoveryCount++
				if state.toolTurnEnabled {
					conv.AddUserMessage(fmt.Sprintf(
						"Your prior response emitted %s tool-control markup as text. If a tool is needed, use the structured tool-call channel; otherwise answer directly in ordinary prose. Do not emit textual tool-call markup.",
						toolName,
					))
				} else {
					conv.AddUserMessage(fmt.Sprintf(
						"Tools are unavailable for this turn. Your prior response attempted to call %s as markup instead of answering. Answer directly in ordinary prose using only the available context; do not emit tool-call markup.",
						toolName,
					))
				}
				continue
			}
			reason := fmt.Sprintf("model returned %s invocation markup as text", toolName)
			if !state.toolTurnEnabled {
				reason = fmt.Sprintf("model returned %s invocation markup while no tools were available", toolName)
			}
			return text, &agentloop.IncompleteTurnError{
				FinishReason: result.FinishReason,
				Reason:       reason,
			}
		}
		if !harnessFinal && shouldNudgeACPToolUse(state.useTools, state.toolTurnEnabled, nudgeCount, text) {
			nudgeCount++
			conv.AddUserMessage("Use tools to take action now. Pick a tool and call it; do not answer with prose alone.")
			continue
		}
		if strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("model returned an empty response")
		}
		flushPendingACPModelDelivery(stream, state)
		sendACPPhaseUpdate(stream, state.lastPhase, "Finalizing response…")
		conv.AddAssistantMessageWithReasoningDetails(text, msg.Reasoning, msg.ReasoningDetails)
		return text, nil
	}
}

type acpIncompleteDraftError struct {
	cause             error
	deliveryAttempted bool
}

func (e *acpIncompleteDraftError) Error() string {
	if e == nil || e.cause == nil {
		return "incomplete draft"
	}
	return e.cause.Error()
}

func (e *acpIncompleteDraftError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func acpIncompleteDraftErrorForPartial(partial *partialStreamTurnError, err error, state *acpLoopState, registry *tool.Registry) error {
	if err == nil || partial == nil || strings.TrimSpace(partial.text) == "" {
		return err
	}
	if len(partial.turn.Message.ToolCalls) > 0 || partial.turn.ObservedToolDelta {
		return err
	}
	if _, attempted := acpToolInvocationMarkup(partial.text, registry); attempted {
		return err
	}
	deliveryAttempted := state != nil && state.currentDraftDeliveryAttempted
	return &acpIncompleteDraftError{cause: err, deliveryAttempted: deliveryAttempted}
}

func acpIncompleteDraftErrorForResult(text string, err error, result *agentloop.Result, state *acpLoopState, registry *tool.Registry) error {
	if err == nil || result == nil || strings.TrimSpace(text) == "" {
		return err
	}
	if len(result.Message.ToolCalls) > 0 || (state != nil && state.currentObservedToolDelta) {
		return err
	}
	if _, attempted := acpToolInvocationMarkup(text, registry); attempted {
		return err
	}
	deliveryAttempted := state != nil && state.currentDraftDeliveryAttempted
	return &acpIncompleteDraftError{cause: err, deliveryAttempted: deliveryAttempted}
}

// partialStreamTurnError preserves the accumulated stream when an SSE
// connection ends after the provider has started its response. The complete
// fragment is retained for evidence/accounting, while Controller still
// refuses to append it to history or execute any unfinished tool calls.
type partialStreamTurnError struct {
	cause error
	text  string
	turn  acpStreamTurn
	// attempts is populated when a safe no-tools retry was exhausted. The
	// first attempt is retained as evidence even though neither attempt's
	// partial deltas are released to the ACP client.
	attempts []acpStreamAttemptEvidence
}

type acpStreamAttemptEvidence struct {
	turn acpStreamTurn
	err  error
}

func (e *partialStreamTurnError) Error() string {
	if e == nil {
		return "agent stream is incomplete"
	}
	if len(e.attempts) < 2 {
		if e.cause == nil {
			return "agent stream is incomplete"
		}
		return e.cause.Error()
	}
	details := make([]string, 0, len(e.attempts))
	for i, attempt := range e.attempts {
		detail := fmt.Sprintf("attempt %d", i+1)
		if attempt.err != nil {
			detail += ": " + attempt.err.Error()
		}
		if text := model.ExtractTextContentOrEmpty(attempt.turn.Message.Content); text != "" {
			detail += fmt.Sprintf(" (content_bytes=%d)", len(text))
		}
		if finish := strings.TrimSpace(attempt.turn.FinishReason); finish != "" {
			detail += ", finish=" + finish
		}
		if len(attempt.turn.Message.ToolCalls) > 0 {
			detail += fmt.Sprintf(", tool_calls=%d", len(attempt.turn.Message.ToolCalls))
		}
		details = append(details, detail)
	}
	return "stream retry failed: " + strings.Join(details, "; ")
}

func (e *partialStreamTurnError) Unwrap() error { return e.cause }

// newACPLoopController wires the shared turn engine for one ACP prompt
// turn. Every hook delegates to the same ACP-specific logic this file
// always used, so the migration changes only who drives the round loop:
//
//   - BuildRequest sends the round's "Thinking…" phase update, re-derives
//     the governed tool turn (the skill tool filter can change mid-turn via
//     activate_skill), and builds the request via buildACPChatRequest --
//     whose single CompactModelMessagesForRequest pass stays the one
//     ACP-side compaction; Controller's own projection on top of that
//     already-bounded list is a no-op, matching the TUI migration's shape.
//   - CallModel is streamACPTurn (S1 per-delta streaming) plus the round's
//     usage_update (N1), adapted to the non-streaming response shape
//     Controller consumes.
//   - DispatchTools executes each call via dispatchACPToolCall: phase
//     updates, tool_call/tool_call_update notifications, the skill
//     allowlist, and the client permission flow (M3), unchanged.
//   - History persists exactly what the old loop persisted: the assistant
//     tool-call message and each tool result. The turn's final answer is
//     not appended here -- runACPLoop still owns it, because a nudged
//     (empty or prose-only) response is dropped from the transcript
//     entirely, exactly as before.
func newACPLoopController(
	cfg *config.Config,
	mgr *model.Manager,
	conv *conversation.Conversation,
	registry *tool.Registry,
	skillState *skill.RuntimeState,
	engine *rules.Engine,
	modelID string,
	workDir string,
	sessionID string,
	agent *acp.Agent,
	logf func(string, ...interface{}),
	stream acp.StreamFunc,
	state *acpLoopState,
	limits acpLoopLimits,
) (*agentloop.Controller, error) {
	evaluator := newACPEvaluator(engine)
	governor := newACPToolLoopGovernorWithLimits(cfg, limits)

	buildRequest := func(ctx context.Context, round int) (model.ChatRequest, error) {
		if _, err := drainChildMailbox(conv, state.childMailbox); err != nil {
			return model.ChatRequest{}, err
		}
		state.lastPhase = sendACPPhaseUpdate(stream, state.lastPhase, "Thinking…")
		toolTurn := buildACPToolTurn(registry, skillState, evaluator, state.useTools, agent != nil, governor.ActionRequired(), limits.TaskIntent)
		state.useTools = toolTurn.UseTools
		state.toolTurnEnabled = toolTurn.Enabled
		state.allowedTools = toolTurn.AllowedTools
		req := buildACPChatRequestForRoute(cfg, mgr, engine, conv, modelID, state.route, toolTurn, limits)
		if state.toolsCatalogConfirmedUnavailable {
			req.ToolsCatalogConfirmedUnavailable = true
			req.Messages = append(req.Messages, model.Message{
				Role:    "system",
				Content: "No local tools are available in this request. Do not claim to have inspected, changed, or verified external state unless it is already present in the conversation.",
			})
		}
		return req, nil
	}

	completionContract := acpCompletionContract(limits)
	callModel := agentloop.ModelCallerFunc(func(ctx context.Context, req model.ChatRequest, _ bool) (*model.ChatResponse, error) {
		// Controller may turn a tool-bearing round into a final synthesis
		// request. Record this post-controller shape, not the earlier build
		// shape, so surprise calls are evaluated against the request actually
		// delivered to the provider.
		state.requestHadToolSchemas = len(req.Tools) > 0
		state.pendingModelUpdates = nil
		state.pendingModelUsage = nil
		state.currentDraftDeliveryAttempted = false
		state.currentObservedToolDelta = false
		deferModelDelivery := limits.MaxCostUSD > 0 || completionContract != nil
		modelStream := stream
		if !deferModelDelivery && stream != nil {
			modelStream = func(update acp.SessionUpdate) error {
				if update.SessionUpdate == acp.SessionUpdateAgentMessageChunk {
					if block, ok := update.Content.(acp.ContentBlock); ok && strings.TrimSpace(block.Text) != "" {
						state.currentDraftDeliveryAttempted = true
					}
				}
				return stream(update)
			}
		}
		turn, err := streamACPTurnWithDelivery(ctx, mgr, req, modelStream, deferModelDelivery)
		state.currentObservedToolDelta = turn.ObservedToolDelta
		resp := buildACPStreamChatResponse(req, turn)
		usageForUpdate := turn.Usage
		if usageForUpdate == nil && resp.Usage.Estimated {
			estimated := resp.Usage
			usageForUpdate = &estimated
		}
		// N1: report provider usage, or Buckley's marked local estimate when
		// the provider omitted it, immediately after the round rather than
		// waiting for the whole prompt turn to finish. Multi-round tool turns
		// therefore still show live context-window growth.
		if deferModelDelivery {
			state.pendingModelUpdates = turn.Updates
			state.pendingModelUsage = usageForUpdate
		} else {
			sendACPUsageUpdate(stream, usageForUpdate, state.contextWindow)
		}
		if err != nil {
			// A provider can charge for a stream that ends after emitting
			// content, reasoning, tool fragments, or usage. Return the
			// accumulated response alongside the error so Controller can
			// persist/account it before surfacing an incomplete turn.
			if acpStreamTurnHasMaterial(turn) {
				return resp, err
			}
			return nil, err
		}
		return resp, nil
	})

	dispatch := agentloop.ToolDispatcherFunc(func(ctx context.Context, calls []model.ToolCall) ([]agentloop.ToolOutcome, error) {
		if !state.requestHadToolSchemas {
			outcomes := make([]agentloop.ToolOutcome, 0, len(calls))
			for i, tc := range calls {
				outcomes = append(outcomes, rejectACPUnexpectedToolCall(stream, tc, i+1, len(calls), state, workDir))
			}
			return outcomes, nil
		}
		state.lastPhase = sendACPPhaseUpdate(stream, state.lastPhase, fmt.Sprintf("Executing %d tool call(s)…", len(calls)))
		outcomes := make([]agentloop.ToolOutcome, 0, len(calls))
		for i, tc := range calls {
			if ctx.Err() != nil {
				return outcomes, ctx.Err()
			}
			outcomes = append(outcomes, dispatchACPToolCall(ctx, registry, evaluator, stream, tc, i+1, len(calls), state, workDir, sessionID, agent, logf))
		}
		state.toolsExecuted = len(calls) > 0
		return outcomes, nil
	})

	history := agentloop.HistorySinkFunc(func(msg model.Message) {
		switch {
		case msg.Role == "assistant" && len(msg.ToolCalls) > 0:
			flushPendingACPModelDelivery(stream, state)
			conv.AddToolCallMessageWithReasoning(msg.ToolCalls, msg.Reasoning, msg.ReasoningDetails)
		case msg.Role == "tool":
			conv.AddToolResponseMessage(msg.ToolCallID, msg.Name, model.ExtractTextContentOrEmpty(msg.Content))
		case msg.Role == "user":
			conv.AddUserMessage(model.ExtractTextContentOrEmpty(msg.Content))
		}
	})

	lifecycleSessionID := strings.TrimSpace(limits.LifecycleSessionID)
	if lifecycleSessionID == "" {
		lifecycleSessionID = strings.TrimSpace(sessionID)
	}
	controllerConfig := agentloop.ControllerConfig{
		Governor:                governor,
		StepCap:                 limits.StepCap,
		FinalizeOnStop:          true,
		FinalizationInstruction: limits.FinalizationInstruction,
		MaxCostUSD:              limits.MaxCostUSD,
		MaxModelRequests:        limits.MaxModelRequests,
		// Progress carries only operator-wide emergency fuses. Child task
		// ceilings are enforced by the governor/context/cost fields, so a zero
		// child budget never synthesizes a second hidden cap here.
		Progress:           newACPProgressController(cfg),
		CompletionContract: completionContract,
		BuildRequest:       buildRequest,
		CallModel:          callModel,
		DispatchTools:      dispatch,
		History:            history,
		// The route and its context window were resolved before Controller
		// construction. Reusing that value keeps ordinary and finalization
		// projections pinned to the same provider/model evidence without a
		// second alias lookup or routing-hook invocation.
		ContextWindow:         func(string) int { return state.contextWindow },
		RunID:                 strings.TrimSpace(limits.RunID),
		SessionID:             lifecycleSessionID,
		TaskID:                strings.TrimSpace(limits.TaskID),
		TurnID:                strings.TrimSpace(limits.TurnID),
		LifecycleObserver:     limits.LifecycleObserver,
		DeferLifecycleTurnEnd: true,
	}
	if limits.MaxCostUSD > 0 && mgr != nil {
		controllerConfig.CostForUsage = func(usage model.Usage) (float64, error) {
			return mgr.CalculateBoundedCost(modelID, usage)
		}
		controllerConfig.NormalizeCostBoundedRequest = mgr.NormalizeCostBoundedRequest
	}
	return agentloop.NewController(controllerConfig)
}

func newACPToolLoopGovernor(cfg *config.Config) *agentloop.Governor {
	return newACPToolLoopGovernorWithLimits(cfg, acpLoopLimits{})
}

func acpCompletionContract(limits acpLoopLimits) *agentloop.CompletionContract {
	depth := strings.ToLower(strings.TrimSpace(limits.VerificationDepth))
	if depth == "legacy" && limits.ValidateFinalResponse == nil && limits.SubmittedResponse == nil {
		return nil
	}
	requireVerification := depth != "none" && depth != "off" && depth != "legacy"
	requireChange := depth != "legacy" && limits.TaskIntent == agentloop.MutationIntent
	if !requireVerification && !requireChange && limits.ValidateFinalResponse == nil && limits.SubmittedResponse == nil {
		return nil
	}
	attempts := limits.MaxVerificationAttempts
	if attempts <= 0 {
		attempts = 1
	}
	contract := &agentloop.CompletionContract{
		RequirePostChangeVerification: requireVerification,
		RequireObservableChange:       requireChange,
		MaxRepairAttempts:             attempts,
		TaskIntent:                    limits.TaskIntent,
		ValidateFinalResponse:         limits.ValidateFinalResponse,
		SubmittedResponse:             limits.SubmittedResponse,
	}
	return contract
}

func newACPToolLoopGovernorWithLimits(cfg *config.Config, limits acpLoopLimits) *agentloop.Governor {
	governorConfig := agentloop.DefaultConfig()
	if limits.ChildContract {
		// Remove the legacy 32/96 per-turn defaults. Explicitly configured
		// operator emergency fuses below remain global runaway protection; they
		// are distinct from the optional child task budget.
		governorConfig.MaxRounds = math.MaxInt
		governorConfig.MaxToolCalls = math.MaxInt
	}
	if cfg != nil {
		if limit := cfg.AgentController.EmergencyFuse.ModelRequests; limit > 0 {
			governorConfig.MaxRounds = limit
		}
		if limit := cfg.AgentController.EmergencyFuse.ToolExecutions; limit > 0 {
			governorConfig.MaxToolCalls = limit
		}
	}
	if limits.MaxReadOnlyCalls > 0 {
		governorConfig.ReadOnlyWarningAt = limits.ReadOnlyWarningAt
		governorConfig.ReadOnlyActionAt = limits.ReadOnlyActionAt
		governorConfig.MaxReadOnlyCalls = limits.MaxReadOnlyCalls
	}
	if limits.ChildContract {
		// An explicit task value narrows the global fuse; absence/zero adds no
		// task ceiling. Repetition and cycle detectors stay enabled.
		if limits.MaxModelRequests > 0 {
			governorConfig.MaxRounds = minPositiveLimit(governorConfig.MaxRounds, limits.MaxModelRequests)
		}
		if limits.MaxToolCalls > 0 {
			governorConfig.MaxToolCalls = minPositiveLimit(governorConfig.MaxToolCalls, limits.MaxToolCalls)
		}
	} else {
		if limits.MaxModelRequests > 0 && (governorConfig.MaxRounds <= 0 || limits.MaxModelRequests < governorConfig.MaxRounds) {
			governorConfig.MaxRounds = limits.MaxModelRequests
		}
		if limits.MaxToolCalls > 0 && (governorConfig.MaxToolCalls <= 0 || limits.MaxToolCalls < governorConfig.MaxToolCalls) {
			governorConfig.MaxToolCalls = limits.MaxToolCalls
		}
	}
	return agentloop.New(governorConfig)
}

func newACPProgressController(cfg *config.Config) *agentloop.ProgressController {
	if cfg == nil {
		return nil
	}
	return agentloop.NewProgressController(
		cfg.AgentController.Mode,
		cfg.AgentController.PolicyVersion,
		agentloop.Fuses{
			ModelRequests:  cfg.AgentController.EmergencyFuse.ModelRequests,
			ToolExecutions: cfg.AgentController.EmergencyFuse.ToolExecutions,
			WallTime:       cfg.AgentController.EmergencyFuse.WallTime,
		},
	)
}

// streamACPTurn drains a streaming chat completion for one runACPLoop
// iteration, forwarding each content delta as its own agent_message_chunk
// and each reasoning delta as its own agent_thought_chunk to the client as
// they arrive (S1), instead of buffering the whole turn and sending one
// chunk at the end. It mirrors mgr.ChatCompletion's return shape -- one
// accumulated Message plus Usage -- once the stream ends, so callers built
// around the non-streaming response shape do not have to change.
//
// It drains both chunkChan and errChan to completion (looping until both
// are nil) rather than returning on the first ready channel: ChatCompletion
// providers close both channels at the end of a successful stream, and a
// select is not guaranteed to observe a still-buffered chunk before an
// already-closed error channel. See pkg/ui/tui/tool_loop.go's
// callToolLoopModel for the same drain pattern.
func streamACPTurn(ctx context.Context, mgr *model.Manager, req model.ChatRequest, stream acp.StreamFunc) (model.Message, *model.Usage, error) {
	turn, err := streamACPTurnWithDelivery(ctx, mgr, req, stream, false)
	return turn.Message, turn.Usage, err
}

type acpStreamTurn struct {
	Message           model.Message
	Usage             *model.Usage
	FinishReason      string
	ExecutionIdentity *model.ExecutionIdentity
	Updates           []acp.SessionUpdate
	Attempts          []acpStreamAttemptEvidence
	ObservedToolDelta bool
}

func acpStreamTurnHasMaterial(turn acpStreamTurn) bool {
	if turn.Usage != nil {
		return true
	}
	if strings.TrimSpace(model.ExtractTextContentOrEmpty(turn.Message.Content)) != "" || strings.TrimSpace(turn.Message.Reasoning) != "" {
		return true
	}
	return len(turn.Message.ToolCalls) > 0 || len(turn.Message.ReasoningDetails) > 0 || turn.ObservedToolDelta
}

func usageValue(usage *model.Usage) model.Usage {
	if usage == nil {
		return model.Usage{}
	}
	return *usage
}

func cloneModelExecutionIdentity(identity *model.ExecutionIdentity) *model.ExecutionIdentity {
	if identity == nil {
		return nil
	}
	cloned := *identity
	return &cloned
}

func aggregateACPStreamAttemptUsage(attempts []acpStreamAttemptEvidence) *model.Usage {
	var aggregate *model.Usage
	for _, attempt := range attempts {
		if attempt.turn.Usage == nil {
			continue
		}
		combined := model.AddUsage(usageValue(aggregate), *attempt.turn.Usage)
		aggregate = &combined
	}
	return aggregate
}

func acpModelAttemptEvidence(attempts []acpStreamAttemptEvidence) []model.ModelAttemptEvidence {
	if len(attempts) == 0 {
		return nil
	}
	evidence := make([]model.ModelAttemptEvidence, 0, len(attempts))
	for _, attempt := range attempts {
		item := model.ModelAttemptEvidence{
			UsagePresent:      attempt.turn.Usage != nil,
			FinishReason:      strings.TrimSpace(attempt.turn.FinishReason),
			Incomplete:        attempt.err != nil,
			ExecutionIdentity: cloneModelExecutionIdentity(attempt.turn.ExecutionIdentity),
		}
		if attempt.turn.Usage != nil {
			item.Usage = model.AddUsage(model.Usage{}, *attempt.turn.Usage)
		}
		evidence = append(evidence, item)
	}
	return evidence
}

func buildACPStreamChatResponse(req model.ChatRequest, turn acpStreamTurn) *model.ChatResponse {
	response := &model.ChatResponse{
		Model:             req.Model,
		Choices:           []model.Choice{{Message: turn.Message, FinishReason: turn.FinishReason}},
		AttemptEvidence:   acpModelAttemptEvidence(turn.Attempts),
		ExecutionIdentity: cloneModelExecutionIdentity(turn.ExecutionIdentity),
	}
	if turn.Usage != nil {
		response.Usage = model.AddUsage(model.Usage{}, *turn.Usage)
		response.UsagePresent = true
	} else {
		response.Usage = model.EstimateChatUsage(req, turn.Message)
	}
	return response
}

// streamACPTurnWithDelivery optionally defers assistant deltas so a caller can
// release them only after the shared Controller accepts the response. Provider
// finish_reason is retained alongside the accumulated message for conclusive
// completion validation. A no-tools request is buffered through its terminal
// decision because an inconclusive first attempt is allowed one safe replay;
// releasing its deltas early would duplicate them if the replay succeeds.
func streamACPTurnWithDelivery(ctx context.Context, mgr *model.Manager, req model.ChatRequest, stream acp.StreamFunc, deferDelivery bool) (acpStreamTurn, error) {
	req.Stream = true
	retryAllowed := acpStreamRetryAllowed(req)
	bufferDelivery := deferDelivery || retryAllowed

	first, firstErr := streamACPTurnAttempt(ctx, mgr, req, stream, bufferDelivery)
	if firstErr == nil {
		if bufferDelivery && !deferDelivery {
			flushACPStreamUpdates(stream, first.Updates)
		}
		return first, nil
	}

	if !retryAllowed || !acpStreamRetryCandidate(ctx, first, firstErr) {
		if bufferDelivery && !deferDelivery {
			flushACPStreamUpdates(stream, first.Updates)
		}
		return first, firstErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if bufferDelivery && !deferDelivery {
			flushACPStreamUpdates(stream, first.Updates)
		}
		return first, errors.Join(ctxErr, firstErr)
	}

	// Keep the second attempt buffered too. If it fails, no fragment from
	// either inference is presented as an accepted assistant answer.
	second, secondErr := streamACPTurnAttempt(ctx, mgr, req, stream, true)
	if secondErr == nil {
		second.Attempts = []acpStreamAttemptEvidence{
			{turn: first, err: firstErr},
			{turn: second},
		}
		second.Usage = aggregateACPStreamAttemptUsage(second.Attempts)
		if !deferDelivery {
			flushACPStreamUpdates(stream, second.Updates)
		}
		return second, nil
	}

	second.Attempts = []acpStreamAttemptEvidence{
		{turn: first, err: firstErr},
		{turn: second, err: secondErr},
	}
	second.Usage = aggregateACPStreamAttemptUsage(second.Attempts)
	return second, &partialStreamTurnError{
		cause:    errors.Join(firstErr, secondErr),
		text:     model.ExtractTextContentOrEmpty(second.Message.Content),
		turn:     second,
		attempts: second.Attempts,
	}
}

func streamACPTurnAttempt(ctx context.Context, mgr *model.Manager, req model.ChatRequest, stream acp.StreamFunc, deferDelivery bool) (acpStreamTurn, error) {
	// A provider can leave its HTTP body or trailer goroutine open after the
	// semantic finish. Canceling this child context prevents that provider-side
	// work from outliving the attempt while keeping the caller's turn context
	// intact for a possible safe retry.
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	route := req.Route
	// streamACPTurn remains a reusable test/helper boundary. Production ACP
	// always supplies the prompt-bound route above; direct helper callers that
	// predate route pinning resolve one immediately before their lone dispatch.
	if strings.TrimSpace(route.RequestedModel) == "" {
		resolved, err := mgr.ResolveModelRoute(req.Model)
		if err != nil {
			return acpStreamTurn{}, err
		}
		route = resolved
		req.Route = route
	}
	chunks, errs := mgr.ChatCompletionStreamForRoute(streamCtx, req, route)
	return drainACPStreamTurn(streamCtx, req, chunks, errs, stream, deferDelivery)
}

func acpStreamRetryAllowed(req model.ChatRequest) bool {
	if len(req.Tools) > 0 {
		return false
	}
	if choice := strings.ToLower(strings.TrimSpace(req.ToolChoice)); choice != "" && choice != "none" {
		return false
	}
	// A no-tools request can still be carrying an earlier tool transaction in
	// its transcript. Treat that as side-effect-adjacent and do not replay it.
	for _, message := range req.Messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "tool") || len(message.ToolCalls) > 0 {
			return false
		}
	}
	return true
}

func acpStreamRetryCandidate(ctx context.Context, turn acpStreamTurn, err error) bool {
	if err == nil || ctx.Err() != nil || strings.TrimSpace(turn.FinishReason) != "" {
		return false
	}
	if len(turn.Message.ToolCalls) > 0 || turn.ObservedToolDelta || isContextCancellationError(err) {
		return false
	}
	safe := acpEveryErrorLeaf(err, func(leaf error) bool {
		return leaf == io.ErrUnexpectedEOF || isUsageTrackingUnavailableError(leaf)
	})
	return safe && ctx.Err() == nil
}

const (
	acpErrorTreeMaxDepth = 64
	acpErrorTreeMaxNodes = 1024
)

// acpEveryErrorLeaf fails closed unless every leaf in a bounded unwrap tree
// satisfies predicate. Depth and node bounds also terminate cyclic custom
// errors without comparing potentially non-comparable dynamic error values.
func acpEveryErrorLeaf(root error, predicate func(error) bool) bool {
	if root == nil || predicate == nil {
		return false
	}
	nodes := 0
	leaves := 0
	var walk func(error, int) bool
	walk = func(current error, depth int) bool {
		if current == nil || depth > acpErrorTreeMaxDepth {
			return false
		}
		nodes++
		if nodes > acpErrorTreeMaxNodes {
			return false
		}
		switch wrapped := current.(type) {
		case interface{ Unwrap() []error }:
			children := wrapped.Unwrap()
			if len(children) == 0 {
				leaves++
				return predicate(current)
			}
			for _, child := range children {
				if !walk(child, depth+1) {
					return false
				}
			}
			return true
		case interface{ Unwrap() error }:
			if child := wrapped.Unwrap(); child != nil {
				return walk(child, depth+1)
			}
			leaves++
			return predicate(current)
		default:
			leaves++
			return predicate(current)
		}
	}
	return walk(root, 0) && leaves > 0
}

func isContextCancellationError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

const acpUsageTrackingUnavailableCode = "usage_tracking_unavailable"

func isUsageTrackingUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *model.APIError
	if errors.As(err, &apiErr) {
		code := strings.ToLower(strings.TrimSpace(apiErr.Code))
		if code != "" {
			return code == acpUsageTrackingUnavailableCode
		}
		// An unstructured compatible provider may omit Code. Keep a narrow
		// compatibility fallback, but never let message text override a
		// different structured code.
		text := strings.ToLower(strings.TrimSpace(apiErr.Message))
		return strings.Contains(text, "usage tracking") && strings.Contains(text, "unavailable")
	}
	return false
}

const (
	acpPostFinishDrainLimit      = 64
	acpRequiredUsageTrailerGrace = 250 * time.Millisecond
)

func drainACPStreamTurn(ctx context.Context, req model.ChatRequest, chunks <-chan model.StreamChunk, errs <-chan error, stream acp.StreamFunc, deferDelivery bool) (acpStreamTurn, error) {
	acc := model.NewStreamAccumulator()
	receivedChoice := false
	finishReason := ""
	var terminalErr error
	terminalErrorCount := 0
	terminalErrorAfterFinish := false
	var updates []acp.SessionUpdate
	observedToolDelta := false
	deltaStream := stream
	if deferDelivery {
		deltaStream = func(update acp.SessionUpdate) error {
			updates = append(updates, update)
			return nil
		}
	}
	turnSnapshot := func() acpStreamTurn {
		message := acc.Message()
		if message.Role == "" && receivedChoice {
			message.Role = "assistant"
		}
		return acpStreamTurn{
			Message:           message,
			Usage:             acc.Usage(),
			FinishReason:      finishReason,
			ExecutionIdentity: acc.ExecutionIdentity(),
			Updates:           updates,
			ObservedToolDelta: observedToolDelta,
		}
	}
	recordTerminalError := func(err error) {
		if err == nil {
			return
		}
		terminalErrorCount++
		if terminalErr == nil {
			terminalErr = err
			terminalErrorAfterFinish = strings.TrimSpace(finishReason) != ""
			return
		}
		terminalErr = errors.Join(terminalErr, err)
		terminalErrorAfterFinish = false
	}
	finishTurn := func() (acpStreamTurn, error) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			turn := turnSnapshot()
			if acpStreamTurnHasMaterial(turn) {
				cause := ctxErr
				if terminalErr != nil {
					cause = errors.Join(ctxErr, terminalErr)
				}
				return turn, newPartialStreamTurnError(turn, cause)
			}
			return acpStreamTurn{}, ctxErr
		}
		turn := turnSnapshot()
		if terminalErr == nil {
			return turn, nil
		}
		if acpAcceptsUsageTrailerError(req, turn, terminalErr, terminalErrorAfterFinish, terminalErrorCount) {
			return turn, nil
		}
		if !acpStreamTurnHasMaterial(turn) {
			return acpStreamTurn{}, terminalErr
		}
		return turn, newPartialStreamTurnError(turn, terminalErr)
	}
	consumeChunk := func(chunk model.StreamChunk) {
		if chunk.Error != nil {
			recordTerminalError(newACPStreamAPIError(chunk.Error))
		}
		acc.Add(chunk)
		for _, choice := range chunk.Choices {
			receivedChoice = true
			if len(choice.Delta.ToolCalls) > 0 {
				observedToolDelta = true
			}
			if choice.FinishReason != nil && strings.TrimSpace(*choice.FinishReason) != "" {
				finishReason = strings.TrimSpace(*choice.FinishReason)
			}
			forwardACPStreamDelta(deltaStream, choice.Delta)
		}
	}
	for chunks != nil || errs != nil {
		select {
		case <-ctx.Done():
			return finishTurn()
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			consumeChunk(chunk)
			if strings.TrimSpace(finishReason) != "" {
				// Once the provider has declared a semantic finish, ordinary streams
				// consume only values already available. A request that explicitly
				// requires authoritative usage gets a short grace period because the
				// producer can enqueue the stop chunk just before its usage-only
				// trailer. The grace is local, bounded, and disabled as soon as usage
				// arrives, so an optional or absent trailer never stalls normal turns.
				var requiredUsageTimer *time.Timer
				var requiredUsageC <-chan time.Time
				if req.StreamOptions != nil && req.StreamOptions.IncludeUsage && acc.Usage() == nil {
					requiredUsageTimer = time.NewTimer(acpRequiredUsageTrailerGrace)
					requiredUsageC = requiredUsageTimer.C
					defer requiredUsageTimer.Stop()
				}
				drainedChunks := 0
				for chunks != nil || errs != nil {
					select {
					case <-ctx.Done():
						return finishTurn()
					default:
					}

					// A terminal error that is already ready must win over the
					// bounded chunk drain. Otherwise a final buffered chunk can
					// consume the limit and incorrectly turn a provider failure
					// into a successful completion.
					if errs != nil {
						select {
						case err, ok := <-errs:
							if !ok {
								errs = nil
							} else {
								recordTerminalError(err)
							}
							continue
						default:
						}
					}

					if requiredUsageC != nil && acc.Usage() != nil {
						requiredUsageTimer.Stop()
						requiredUsageC = nil
					}
					if drainedChunks >= acpPostFinishDrainLimit {
						return finishTurn()
					}
					if requiredUsageC != nil {
						select {
						case <-ctx.Done():
							return finishTurn()
						case <-requiredUsageC:
							requiredUsageC = nil
							// Preserve ready-error priority at the grace boundary too.
							if errs != nil {
								select {
								case err, ok := <-errs:
									if !ok {
										errs = nil
									} else {
										recordTerminalError(err)
									}
								default:
								}
							}
							return finishTurn()
						case err, ok := <-errs:
							if !ok {
								errs = nil
								continue
							}
							recordTerminalError(err)
						case chunk, ok := <-chunks:
							if !ok {
								chunks = nil
								continue
							}
							consumeChunk(chunk)
							drainedChunks++
						}
						continue
					}
					select {
					case <-ctx.Done():
						return finishTurn()
					case err, ok := <-errs:
						if !ok {
							errs = nil
							continue
						}
						recordTerminalError(err)
					case chunk, ok := <-chunks:
						if !ok {
							chunks = nil
							continue
						}
						consumeChunk(chunk)
						drainedChunks++
					default:
						return finishTurn()
					}
				}
				return finishTurn()
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				recordTerminalError(err)
				errs = nil
				continue
			}
		}
	}

	if terminalErr != nil {
		return finishTurn()
	}

	if !receivedChoice {
		turn := turnSnapshot()
		if ctx.Err() != nil {
			return finishTurn()
		}
		noChoices := model.NoResponseChoicesError(req, &model.ChatResponse{Model: req.Model, Usage: usageValue(turn.Usage)})
		if acpStreamTurnHasMaterial(turn) {
			return turn, newPartialStreamTurnError(turn, noChoices)
		}
		return acpStreamTurn{}, noChoices
	}

	return finishTurn()
}

func newPartialStreamTurnError(turn acpStreamTurn, cause error) *partialStreamTurnError {
	return &partialStreamTurnError{
		cause: cause,
		text:  model.ExtractTextContentOrEmpty(turn.Message.Content),
		turn:  turn,
	}
}

func acpFinishReasonIsStop(reason string) bool {
	return strings.EqualFold(strings.TrimSpace(reason), "stop")
}

func acpAcceptsUsageTrailerError(req model.ChatRequest, turn acpStreamTurn, err error, observedAfterFinish bool, terminalErrorCount int) bool {
	return observedAfterFinish &&
		terminalErrorCount == 1 &&
		acpFinishReasonIsStop(turn.FinishReason) &&
		strings.TrimSpace(model.ExtractTextContentOrEmpty(turn.Message.Content)) != "" &&
		len(turn.Message.ToolCalls) == 0 &&
		acpStreamRetryAllowed(req) &&
		acpEveryErrorLeaf(err, isUsageTrackingUnavailableError)
}

func newACPStreamAPIError(detail *model.ErrorDetail) error {
	if detail == nil {
		return nil
	}
	message := strings.TrimSpace(detail.Message)
	if message == "" {
		message = "provider returned a streaming error"
	}
	return &model.APIError{
		StatusCode: 200,
		Message:    message,
		Type:       detail.Type,
		Code:       detail.Code,
	}
}

func flushACPStreamUpdates(stream acp.StreamFunc, updates []acp.SessionUpdate) {
	if stream == nil {
		return
	}
	for _, update := range updates {
		_ = stream(update)
	}
}

func flushPendingACPModelDelivery(stream acp.StreamFunc, state *acpLoopState) {
	if state == nil || (len(state.pendingModelUpdates) == 0 && state.pendingModelUsage == nil) {
		return
	}
	updates := state.pendingModelUpdates
	usage := state.pendingModelUsage
	// Transfer ownership before invoking client callbacks. Re-entrancy or a
	// repeated acceptance path therefore cannot emit the same model delta twice.
	state.pendingModelUpdates = nil
	state.pendingModelUsage = nil
	if stream != nil && acpSessionUpdatesContainMessageText(updates) {
		state.currentDraftDeliveryAttempted = true
	}
	flushACPStreamUpdates(stream, updates)
	sendACPUsageUpdate(stream, usage, state.contextWindow)
}

func discardPendingACPModelDelivery(state *acpLoopState) {
	if state == nil {
		return
	}
	state.pendingModelUpdates = nil
	state.pendingModelUsage = nil
}

func acpSessionUpdatesContainMessageText(updates []acp.SessionUpdate) bool {
	for _, update := range updates {
		if update.SessionUpdate != acp.SessionUpdateAgentMessageChunk {
			continue
		}
		if block, ok := update.Content.(acp.ContentBlock); ok && strings.TrimSpace(block.Text) != "" {
			return true
		}
	}
	return false
}

// forwardACPStreamDelta streams one chunk's delta immediately: content as an
// agent_message_chunk, reasoning as an agent_thought_chunk. OpenRouter's
// reasoning_details blocks take precedence over the legacy reasoning field
// when both are present on the same delta (they carry the same content in
// a richer shape). Buckley sends one ACP update per provider delta rather
// than re-tokenizing -- the provider's own SSE chunking is already the
// "per token" granularity ACP clients render incrementally.
func forwardACPStreamDelta(stream acp.StreamFunc, delta model.MessageDelta) {
	if stream == nil {
		return
	}
	if len(delta.ReasoningDetails) > 0 {
		for _, rd := range delta.ReasoningDetails {
			text := rd.Text
			if text == "" {
				text = rd.Summary
			}
			if text != "" {
				_ = stream(acp.NewAgentThoughtChunk(text))
			}
		}
	} else if delta.Reasoning != "" {
		_ = stream(acp.NewAgentThoughtChunk(delta.Reasoning))
	}
	if delta.Content != "" {
		_ = stream(acp.NewAgentMessageChunk(delta.Content))
	}
}

// sendACPUsageUpdate emits a usage_update session update (N1) for one model
// round-trip. Used is the provider-reported total when available, otherwise
// Buckley's marked local estimate; size is the model's context window. It is a
// no-op when usage or the context window is unavailable rather than sending a
// misleading zero/zero update. Buckley does not track a per-request USD cost
// here, so the optional cost field is left unset.
func sendACPUsageUpdate(stream acp.StreamFunc, usage *model.Usage, contextWindow int) {
	if stream == nil || usage == nil || contextWindow <= 0 {
		return
	}
	_ = stream(acp.NewUsageUpdate(uint64(usage.TotalTokens), uint64(contextWindow), nil))
}

const acpMaxToolNudges = 2

type acpToolTurn struct {
	Tools        []map[string]any
	AllowedTools []string
	UseTools     bool
	Enabled      bool
}

func resolveACPExecutionModel(cfg *config.Config, mgr *model.Manager, engine *rules.Engine, modelOverride string) (string, error) {
	return model.ResolvePhaseModelRequired(cfg, acpReasoningChecker(mgr), engine, "execution", modelOverride)
}

func acpReasoningChecker(mgr *model.Manager) model.ReasoningChecker {
	if mgr == nil {
		return nil
	}
	return mgr
}

func newACPEvaluator(engine *rules.Engine) types.RuleEvaluator {
	if engine == nil {
		return nil
	}
	return rules.NewEngineAdapter(engine)
}

func acpModelCanUseTools(registry *tool.Registry, mgr *model.Manager, route model.ModelRoute) bool {
	return registry != nil && (mgr == nil || mgr.OfferToolsForRoute(route))
}

func buildACPToolTurn(registry *tool.Registry, skillState *skill.RuntimeState, evaluator types.RuleEvaluator, useTools bool, permissionAvailable bool, actionRequired bool, intent agentloop.TaskIntent) acpToolTurn {
	turn := acpToolTurn{UseTools: useTools}
	if skillState != nil {
		turn.AllowedTools = skillState.ToolFilter()
	}
	if actionRequired && intent != agentloop.ReadOnlyIntent && registry != nil {
		turn.AllowedTools = acpActionToolNames(registry, turn.AllowedTools)
	}
	if !permissionAvailable && registry != nil {
		turn.AllowedTools = acpFallbackAllowedToolNames(registry, turn.AllowedTools)
	}
	if !useTools || registry == nil {
		turn.UseTools = false
		return turn
	}
	turn.Tools = registry.ToOpenAIFunctionsGoverned(evaluator, "interactive", "coding", turn.AllowedTools, 0)
	if len(turn.Tools) == 0 {
		turn.UseTools = false
		return turn
	}
	turn.Enabled = true
	return turn
}

// acpFallbackAllowedToolNames keeps a noninteractive request aligned with the
// permission decision it will receive at dispatch time. Advertising a tool
// that the fallback always denies wastes model turns and invites denial loops.
func acpFallbackAllowedToolNames(registry *tool.Registry, allowed []string) []string {
	hasFilter := allowed != nil
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	names := make([]string, 0, registry.Count())
	for _, registered := range registry.List() {
		name := registered.Name()
		if hasFilter {
			if _, ok := allowedSet[name]; !ok {
				continue
			}
		}
		if acpFallbackPermissionDecision(acpRiskLabel(tool.GetMetadata(registered).Impact)) {
			names = append(names, name)
		}
	}
	return names
}

// acpActionToolNames keeps state-changing tools plus the bounded control and
// verification surfaces needed to finish honestly after discovery is parked.
// It deliberately excludes general read/search and destructive escape hatches.
func acpActionToolNames(registry *tool.Registry, allowed []string) []string {
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = struct{}{}
	}
	names := make([]string, 0)
	for _, registered := range registry.List() {
		name := registered.Name()
		if len(allowedSet) > 0 {
			if _, ok := allowedSet[name]; !ok {
				continue
			}
		}
		metadata := tool.GetMetadata(registered)
		verificationOrControl := metadata.Impact == tool.ImpactReadOnly && (metadata.Verification || isACPActionSupportTool(name))
		if metadata.Impact == tool.ImpactModifying || verificationOrControl {
			names = append(names, name)
		}
	}
	return names
}

func isACPActionSupportTool(name string) bool {
	switch name {
	case "exec_program", "git_diff", "git_status", "submit_artifact":
		return true
	default:
		return false
	}
}

func buildACPChatRequest(cfg *config.Config, mgr *model.Manager, engine *rules.Engine, conv *conversation.Conversation, modelID string, turn acpToolTurn, limits acpLoopLimits) model.ChatRequest {
	return buildACPChatRequestForRoute(cfg, mgr, engine, conv, modelID, model.ModelRoute{}, turn, limits)
}

// buildACPChatRequestForRoute builds the production ACP request from an
// already-governed execution route. The empty-route path preserves the legacy
// direct/helper behavior for callers that do not have a prompt-bound route.
func buildACPChatRequestForRoute(cfg *config.Config, mgr *model.Manager, engine *rules.Engine, conv *conversation.Conversation, modelID string, route model.ModelRoute, turn acpToolTurn, limits acpLoopLimits) model.ChatRequest {
	req := model.ChatRequest{
		Model:     modelID,
		MaxTokens: limits.MaxOutputTokens,
		// ToModelMessages returns the full portable transcript. Compaction
		// runs once below via CompactModelMessagesForRequest -- an earlier
		// ToEfficientModelMessages pass here would be a redundant compaction
		// on top of that projection.
		Messages:  conv.ToModelMessages(),
		SessionID: conv.SessionID,
	}
	hasRoute := strings.TrimSpace(route.SelectedModel) != "" && strings.TrimSpace(route.ProviderID) != ""
	if hasRoute {
		req.Route = route
	}
	if turn.UseTools {
		req.Tools = turn.Tools
		req.ToolChoice = "auto"
		parallelToolCalls := false
		if mgr != nil {
			if hasRoute {
				parallelToolCalls = mgr.SupportsParameterForRoute(route, "parallel_tool_calls")
			} else {
				parallelToolCalls = mgr.SupportsParameter(modelID, "parallel_tool_calls")
			}
		}
		if parallelToolCalls {
			sequential := false
			req.ParallelToolCalls = &sequential
		}
	}
	contextWindow := 0
	if mgr != nil {
		if hasRoute {
			contextWindow, _ = mgr.GetContextLengthForRoute(route)
		} else {
			contextWindow, _ = mgr.GetContextLength(modelID)
		}
	}
	if hasRoute {
		req.Reasoning = resolveACPRequestReasoningForRoute(cfg, mgr, engine, route, limits.ProtocolReasoning)
	} else {
		req.Reasoning = resolveACPRequestReasoning(cfg, mgr, engine, modelID, limits.ProtocolReasoning)
	}
	req.Messages = conversation.CompactModelMessagesForRequest(req.Messages, req, contextWindow)
	return req
}

func resolveACPRequestReasoning(cfg *config.Config, mgr *model.Manager, engine *rules.Engine, modelID string, protocolReasoning *model.ReasoningConfig) *model.ReasoningConfig {
	checker := acpReasoningChecker(mgr)
	if checker == nil || !checker.SupportsReasoning(modelID) {
		return nil
	}

	configured := ""
	if cfg != nil {
		configured = strings.ToLower(strings.TrimSpace(cfg.Models.Reasoning))
	}
	switch configured {
	case "off", "none":
		enabled := false
		return &model.ReasoningConfig{Enabled: &enabled}
	case "minimal", "low", "medium", "high", "xhigh":
		return &model.ReasoningConfig{Effort: configured}
	}

	if protocolReasoning != nil {
		return model.NormalizeReasoningConfig(protocolReasoning)
	}
	if effort := model.ResolveReasoningEffort(cfg, checker, engine, modelID, "execution"); effort != "" {
		return &model.ReasoningConfig{Effort: effort}
	}
	return nil
}

// resolveACPRequestReasoningForRoute keeps reasoning capability evidence on
// the same route that governed tool eligibility and provider dispatch.
func resolveACPRequestReasoningForRoute(cfg *config.Config, mgr *model.Manager, engine *rules.Engine, route model.ModelRoute, protocolReasoning *model.ReasoningConfig) *model.ReasoningConfig {
	if mgr == nil || !mgr.SupportsReasoningForRoute(route) {
		return nil
	}
	configured := ""
	if cfg != nil {
		configured = strings.ToLower(strings.TrimSpace(cfg.Models.Reasoning))
	}
	switch configured {
	case "off", "none":
		enabled := false
		return &model.ReasoningConfig{Enabled: &enabled}
	case "minimal", "low", "medium", "high", "xhigh":
		return &model.ReasoningConfig{Effort: configured}
	}
	if protocolReasoning != nil {
		return model.NormalizeReasoningConfig(protocolReasoning)
	}
	if effort := model.ResolveReasoningEffort(cfg, acpRouteReasoningChecker{mgr: mgr, route: route}, engine, route.SelectedModel, "execution"); effort != "" {
		return &model.ReasoningConfig{Effort: effort}
	}
	return nil
}

type acpRouteReasoningChecker struct {
	mgr   *model.Manager
	route model.ModelRoute
}

func (c acpRouteReasoningChecker) SupportsReasoning(string) bool {
	return c.mgr != nil && c.mgr.SupportsReasoningForRoute(c.route)
}

func (c acpRouteReasoningChecker) ResolveReasoningCapability(string) model.CapabilityResolution {
	if c.mgr == nil {
		return model.CapabilityResolution{Model: c.route.SelectedModel, ProviderID: c.route.ProviderID, Capability: "reasoning", State: model.CapabilityUnknown, Source: "checker_unavailable"}
	}
	return c.mgr.ResolveReasoningCapabilityForRoute(c.route)
}

func shouldNudgeACPToolUse(useTools, toolsEnabled bool, nudgeCount int, text string) bool {
	return useTools && toolsEnabled && nudgeCount < acpMaxToolNudges &&
		(strings.TrimSpace(text) == "" || shouldNudgeForTools(text))
}

func acpToolInvocationMarkup(text string, registry *tool.Registry) (string, bool) {
	if toolName, attempted := soleKnownACPToolInvocationMarkup(text, registry); attempted {
		return toolName, true
	}
	return malformedACPToolControlMarkup(text, registry)
}

// soleKnownACPToolInvocationMarkup recognizes a provider's attempted tool call
// only when the entire response is one XML element named for a registered
// Buckley tool and its payload contains at least one parameter from that
// tool's schema. Requiring a sole document root keeps prose and fenced examples
// out of this compatibility path.
func soleKnownACPToolInvocationMarkup(text string, registry *tool.Registry) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" || registry == nil || strings.Contains(text, "```") {
		return "", false
	}

	decoder := xml.NewDecoder(strings.NewReader(text))
	depth := 0
	rootName := ""
	rootClosed := false
	argumentNames := make([]string, 0, 4)
	var directPayload strings.Builder

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", false
		}
		switch value := token.(type) {
		case xml.StartElement:
			if rootClosed || value.Name.Space != "" || len(value.Attr) > 0 {
				return "", false
			}
			if depth == 0 {
				if rootName != "" {
					return "", false
				}
				rootName = value.Name.Local
			} else if depth == 1 {
				argumentNames = append(argumentNames, value.Name.Local)
			}
			depth++
		case xml.EndElement:
			depth--
			if depth < 0 {
				return "", false
			}
			if depth == 0 {
				rootClosed = true
			}
		case xml.CharData:
			if depth == 0 {
				if strings.TrimSpace(string(value)) != "" {
					return "", false
				}
			} else if depth == 1 {
				directPayload.Write(value)
			}
		case xml.Comment, xml.Directive, xml.ProcInst:
			return "", false
		}
	}
	if depth != 0 || rootName == "" || !rootClosed {
		return "", false
	}

	knownTool, ok := registry.Get(rootName)
	if !ok {
		return "", false
	}
	properties := knownTool.Parameters().Properties
	if len(argumentNames) > 0 {
		if strings.TrimSpace(directPayload.String()) != "" {
			return "", false
		}
		for _, name := range argumentNames {
			if _, known := properties[name]; known || len(properties) == 0 {
				return rootName, true
			}
		}
		return "", false
	}

	var arguments map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(directPayload.String())), &arguments); err != nil || len(arguments) == 0 {
		return "", false
	}
	for name := range arguments {
		if _, known := properties[name]; known || len(properties) == 0 {
			return rootName, true
		}
	}
	return "", false
}

func malformedACPToolControlMarkup(text string, registry *tool.Registry) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "```") || !strings.HasPrefix(text, "<tool_call>") {
		return "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, "<tool_call>"))
	toolName := readACPToolControlMarkupName(rest)
	if toolName == "" {
		return "tool_call", true
	}
	if registry != nil {
		if _, ok := registry.Get(toolName); ok {
			return toolName, true
		}
	}
	return "tool_call", true
}

func readACPToolControlMarkupName(text string) string {
	const maxToolControlNameLen = 80
	for idx, r := range text {
		if idx >= maxToolControlNameLen {
			return text[:idx]
		}
		if !(r == '_' || r == '-' || r == '.' || r == ':' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') {
			if idx == 0 {
				return ""
			}
			return text[:idx]
		}
	}
	return text
}

// dispatchACPToolCall runs one tool call for the Controller's dispatcher:
// phase update, tool_call start notification, the skill allowlist, the
// client permission flow (M3), execution, and the tool_call_update result
// notification. It returns the model-facing text as an agentloop.ToolOutcome
// -- the Controller appends it to the conversation via the History sink, so
// this function no longer writes to the transcript itself.
func dispatchACPToolCall(ctx context.Context, registry *tool.Registry, evaluator types.RuleEvaluator, stream acp.StreamFunc, tc model.ToolCall, index, total int, state *acpLoopState, workDir string, sessionID string, agent *acp.Agent, logf func(string, ...interface{})) agentloop.ToolOutcome {
	params, err := parseACPToolParams(tc.Function.Arguments)
	if err != nil {
		rawParams := map[string]any{"raw": tc.Function.Arguments}
		state.lastPhase = sendACPPhaseUpdate(stream, state.lastPhase, fmt.Sprintf("Running %s (%d/%d)…", toolCallTitle(tc.Function.Name, nil), index, total))
		sendACPToolCallStart(stream, tc, rawParams, workDir)
		toolText := fmt.Sprintf("Error: invalid tool arguments: %v", err)
		sendACPToolCallUpdate(stream, tc, rawParams, acp.ToolCallStatusFailed, toolText, map[string]any{
			"error": err.Error(),
		}, nil, workDir)
		return agentloop.ToolOutcome{Content: toolText, Error: err.Error(), EffectClass: "control"}
	}

	effectClass := string(acpToolRiskImpact(registry, tc.Function.Name, params))
	state.lastPhase = sendACPPhaseUpdate(stream, state.lastPhase, fmt.Sprintf("Running %s (%d/%d)…", toolCallTitle(tc.Function.Name, params), index, total))
	sendACPToolCallStart(stream, tc, params, workDir)

	if !tool.IsToolAllowed(tc.Function.Name, state.allowedTools) {
		toolText := fmt.Sprintf("Error: tool %s not allowed by active skills", tc.Function.Name)
		sendACPToolCallUpdate(stream, tc, params, acp.ToolCallStatusFailed, toolText, map[string]any{
			"error": toolText,
		}, nil, workDir)
		return agentloop.ToolOutcome{Content: toolText, Error: toolText, EffectClass: effectClass}
	}

	if allowed, reason := requestACPToolPermission(ctx, agent, registry, sessionID, tc, params, workDir, logf); !allowed {
		toolText := fmt.Sprintf("Permission denied for %s: %s", tc.Function.Name, reason)
		result := &builtin.Result{Success: false, Error: toolText}
		sendACPToolCallUpdate(stream, tc, params, acp.ToolCallStatusFailed, toolText, map[string]any{
			"error":  toolText,
			"denied": true,
		}, result, workDir)
		return agentloop.ToolOutcome{Content: toolText, Error: toolText, EffectClass: effectClass}
	}

	metadata := tool.DefaultMetadata()
	if registry != nil {
		if registered, ok := registry.Get(tc.Function.Name); ok {
			metadata = tool.GetMetadata(registered)
		}
	}
	observation := tooloutcome.BeginWithMetadata(ctx, workDir, metadata)
	result, execErr := executeACPToolCall(ctx, registry, tc.Function.Name, params, tc.ID)
	toolText := formatACPToolResult(result, execErr)
	toolText = tool.AppendCodeModeRecoveryGuidance(toolText, evaluator, registry, state.allowedTools, tc.Function.Name, result, execErr, state.codeModeRecovery)
	displayText := formatACPToolDisplay(result, execErr)
	yield := tool.ResultYieldForTool(tc.Function.Name, result, execErr)

	status := acp.ToolCallStatusCompleted
	if execErr != nil || (result != nil && !result.Success) {
		status = acp.ToolCallStatusFailed
	}
	sendACPToolCallUpdate(stream, tc, params, status, displayText, toolCallRawOutput(result, execErr), result, workDir)
	errorText := ""
	if execErr != nil {
		errorText = execErr.Error()
	} else if result != nil {
		errorText = result.Error
	}
	return observation.Finish(ctx, agentloop.ToolOutcome{
		Content:       toolText,
		Success:       execErr == nil && result != nil && result.Success,
		EffectClass:   effectClass,
		Error:         errorText,
		Stderr:        toolResultString(result, "stderr"),
		YieldObserved: yield.Observed,
		YieldCount:    yield.Count,
		YieldUnit:     yield.Unit,
	}, metadata, result, execErr)
}

// rejectACPUnexpectedToolCall records a model's structured call as a visible
// failed ACP control outcome when the exact request contained no schemas. It
// intentionally does not inspect the registry, request approval, begin an
// observation, signal an executor, or update toolsExecuted: the model must
// recover with a direct answer using the resulting tool message.
func rejectACPUnexpectedToolCall(stream acp.StreamFunc, tc model.ToolCall, index, total int, state *acpLoopState, workDir string) agentloop.ToolOutcome {
	params := map[string]any{}
	if parsed, err := parseACPToolParams(tc.Function.Arguments); err == nil {
		params = parsed
	} else if strings.TrimSpace(tc.Function.Arguments) != "" {
		params["raw"] = tc.Function.Arguments
	}
	state.lastPhase = sendACPPhaseUpdate(stream, state.lastPhase, fmt.Sprintf("Rejecting unexpected tool call %d/%d…", index, total))
	sendACPToolCallStart(stream, tc, params, workDir)
	toolText := fmt.Sprintf("Error: tool %s was not offered for this request and was not executed. Answer directly using only the available conversation context.", tc.Function.Name)
	sendACPToolCallUpdate(stream, tc, params, acp.ToolCallStatusFailed, toolText, map[string]any{
		"error": "tool call rejected because this request did not include tool schemas",
	}, nil, workDir)
	return agentloop.ToolOutcome{Content: toolText, EffectClass: "control"}
}

// acpPermissionRequestTimeout bounds how long Buckley waits for a live
// client to answer a session/request_permission request. A human decision
// can take a while, but the turn must never deadlock (M3): once this
// elapses without an answer, requestACPToolPermission falls back to the
// default risk policy rather than blocking forever.
const acpPermissionRequestTimeout = 5 * time.Minute

// acpPermissionOptions is the fixed choice set Buckley offers on a
// session/request_permission prompt: allow this one operation, or reject
// it. Buckley does not currently support "remember this choice" (allow/
// reject_always), since ACP sessions are short-lived and per-turn.
var acpPermissionOptions = []acp.PermissionOption{
	{OptionID: "allow", Name: "Allow", Kind: acp.PermissionOptionKindAllowOnce},
	{OptionID: "reject", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce},
}

// acpToolRiskImpact classifies a tool call using the registry's existing
// danger/approval classification (tool.GetMetadata's Impact: read-only,
// modifying, destructive) -- the same classification Buckley already uses
// elsewhere for approval gating, not a new ACP-specific notion. run_shell's
// static metadata tags every command destructive ("shell commands can do
// anything"), which is correct for arbitrary commands but wrongly caught
// routine build/vet/test/lint verification too (H10); params lets the
// caller's actual command downgrade that classification when it matches a
// known workspace verification command (see isWorkspaceVerificationCommand).
func acpToolRiskImpact(registry *tool.Registry, name string, params map[string]any) tool.Impact {
	if registry == nil {
		return tool.ImpactDestructive
	}
	t, ok := registry.Get(name)
	if !ok {
		return tool.ImpactDestructive
	}
	impact := tool.GetMetadata(t).Impact
	if impact == tool.ImpactDestructive && isWorkspaceVerificationToolCall(name, params) {
		return tool.ImpactReadOnly
	}
	return impact
}

// verificationShellCommandPrefixes lists build/vet/test/lint commands that
// check the workspace without mutating it. Buckley's default risk policy
// (used when no live ACP client is attached -- e.g. the headless oneshot
// CLI path) treats a run_shell command matching one of these prefixes as
// non-destructive, so a correct edit is not stranded "incomplete" for want
// of its own post-change verification (H10).
var verificationShellCommandPrefixes = []string{
	"go build",
	"go vet",
	"go test",
	"gofmt -l",
	"golangci-lint",
	"staticcheck",
	"go lint",
	"npm test",
	"npm run test",
	"npm run build",
	"npm run lint",
	"cargo build",
	"cargo test",
	"cargo check",
	"cargo clippy",
	"pytest",
	"make test",
	"make build",
	"make vet",
	"make lint",
}

// isWorkspaceVerificationToolCall reports whether a tool call is a
// build/vet/test/lint check of the workspace. Only run_shell is eligible:
// run_code executes arbitrary caller-supplied code, not a fixed command, so
// it keeps its destructive classification.
func isWorkspaceVerificationToolCall(name string, params map[string]any) bool {
	if name != "run_shell" {
		return false
	}
	command, _ := params["command"].(string)
	return isWorkspaceVerificationCommand(command)
}

// isWorkspaceVerificationCommand reports whether command is a known
// build/vet/test/lint prefix with nothing chained after it. Any shell
// control operator (&&, ||, ;, |, redirection, command substitution)
// disqualifies the match, since that could smuggle a destructive command
// alongside a safe-looking prefix.
func isWorkspaceVerificationCommand(command string) bool {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return false
	}
	if strings.ContainsAny(trimmed, "&|;><`$(){}") {
		return false
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range verificationShellCommandPrefixes {
		if lower == prefix || strings.HasPrefix(lower, prefix+" ") {
			return true
		}
	}
	return false
}

// acpRiskLabel maps a tool.Impact to the risk vocabulary Buckley's local
// fallback policy (acpFallbackPermissionDecision) already understands.
func acpRiskLabel(impact tool.Impact) string {
	switch impact {
	case tool.ImpactDestructive:
		return "destructive"
	case tool.ImpactModifying:
		return "medium"
	default:
		return "low"
	}
}

// acpFallbackPermissionDecision is Buckley's default policy when no live
// client answer is available: auto-approve low/medium risk, deny high/
// destructive. It backs both the AgentHandlers.OnRequestPermission
// callback and requestACPToolPermission's no-client-response fallback, so
// the two paths can never diverge.
func acpFallbackPermissionDecision(risk string) bool {
	switch risk {
	case "high", "destructive":
		return false
	default:
		return true
	}
}

// requestACPToolPermission decides whether a tool call may proceed, using
// the default client-response timeout (acpPermissionRequestTimeout). See
// requestACPToolPermissionWithTimeout for the full behavior.
func requestACPToolPermission(ctx context.Context, agent *acp.Agent, registry *tool.Registry, sessionID string, tc model.ToolCall, params map[string]any, workDir string, logf func(string, ...interface{})) (allowed bool, reason string) {
	return requestACPToolPermissionWithTimeout(ctx, agent, registry, sessionID, tc, params, workDir, logf, acpPermissionRequestTimeout)
}

// requestACPToolPermissionWithTimeout decides whether a tool call may
// proceed. Read-only tools proceed without asking (they carry nothing to
// approve). For modifying/destructive tools ("shell/write/destructive" per
// the registry's existing classification), when a live ACP client is
// attached (agent != nil -- i.e. this is an interactive editor session, not
// the headless oneshot CLI path), it sends a spec-shape
// session/request_permission request and honors the client's
// allow/deny/cancelled decision. If the client can't be reached (no
// attached agent, send failure, or timeout) it logs a warning and falls
// back to Buckley's default risk policy rather than blocking the turn
// forever or crashing the tool call. A prompt-turn cancellation (ctx done)
// does not fall back to auto-approval -- it stops the tool call outright.
// The timeout parameter exists mainly so tests don't have to wait out
// acpPermissionRequestTimeout to exercise the fallback path.
func requestACPToolPermissionWithTimeout(ctx context.Context, agent *acp.Agent, registry *tool.Registry, sessionID string, tc model.ToolCall, params map[string]any, workDir string, logf func(string, ...interface{}), timeout time.Duration) (allowed bool, reason string) {
	impact := acpToolRiskImpact(registry, tc.Function.Name, params)
	if impact == tool.ImpactReadOnly {
		return true, ""
	}

	if agent != nil {
		toolCall := acp.ToolCallUpdate{
			ToolCallID: tc.ID,
			Title:      toolCallTitle(tc.Function.Name, params),
			Kind:       toolCallKind(tc.Function.Name),
			Status:     acp.ToolCallStatusPending,
			RawInput:   params,
			Locations:  toolCallLocations(params, workDir),
		}
		outcome, err := agent.RequestClientPermission(ctx, sessionID, toolCall, acpPermissionOptions, timeout)
		if err == nil {
			if outcome.Outcome == acp.RequestPermissionOutcomeCancelled {
				return false, "permission request cancelled by client"
			}
			if outcome.OptionID == "allow" {
				return true, ""
			}
			return false, "denied by user"
		}
		if ctx.Err() != nil {
			// The prompt turn itself was cancelled (e.g. session/cancel) --
			// stop the tool call, do not fall back to auto-approval.
			return false, "prompt cancelled before permission was granted"
		}
		if logf != nil {
			logf("session/request_permission failed for %s: %v; falling back to default risk policy", tc.Function.Name, err)
		}
	}

	risk := acpRiskLabel(impact)
	if !acpFallbackPermissionDecision(risk) {
		return false, fmt.Sprintf("denied by default risk policy (%s risk, no client response)", risk)
	}
	return true, ""
}

func parseACPToolParams(raw string) (map[string]any, error) {
	return tool.DecodeArguments(raw)
}

func sendACPToolCallStart(stream acp.StreamFunc, call model.ToolCall, params map[string]any, workDir string) {
	if stream == nil {
		return
	}
	update := acp.SessionUpdate{
		SessionUpdate: acp.SessionUpdateToolCall,
		ToolCallID:    call.ID,
		Title:         toolCallTitle(call.Function.Name, params),
		Kind:          toolCallKind(call.Function.Name),
		Status:        acp.ToolCallStatusInProgress,
		RawInput:      params,
		Locations:     toolCallLocations(params, workDir),
	}
	_ = stream(update)
}

func sendACPToolCallUpdate(stream acp.StreamFunc, call model.ToolCall, params map[string]any, status, text string, rawOutput any, result *builtin.Result, workDir string) {
	if stream == nil {
		return
	}
	update := acp.SessionUpdate{
		SessionUpdate: acp.SessionUpdateToolCallUpdate,
		ToolCallID:    call.ID,
		Status:        status,
		RawOutput:     rawOutput,
	}
	contents := buildToolCallContents(text, result, workDir)
	if len(contents) > 0 {
		update.Content = contents
	}
	_ = stream(update)
}

func sendACPPhaseUpdate(stream acp.StreamFunc, last, message string) string {
	message = strings.TrimSpace(message)
	if stream == nil || message == "" || message == last {
		return last
	}
	_ = stream(acp.NewAgentThoughtChunk(message))
	return message
}

func buildToolCallContents(text string, result *builtin.Result, workDir string) []acp.ToolCallContent {
	const maxText = 8000
	var contents []acp.ToolCallContent
	if trimmed := strings.TrimSpace(text); trimmed != "" {
		contents = append(contents, acp.ToolCallContent{
			Type:    "content",
			Content: &acp.ContentBlock{Type: "text", Text: truncateWithLimit(trimmed, maxText)},
		})
	}

	if result != nil {
		contents = append(contents, toolCallOutputContents(result, maxText)...)
		if result.DiffPreview != nil {
			diff := result.DiffPreview
			path := resolveACPPath(diff.FilePath, workDir)
			if preview := strings.TrimSpace(diff.Preview); preview != "" {
				contents = append(contents, acp.ToolCallContent{
					Type:    "content",
					Content: &acp.ContentBlock{Type: "text", Text: truncateWithLimit("[DIFF]\n"+preview, maxText)},
				})
			}
			// The ACP diff content variant requires "path" and "newText"
			// verbatim -- an empty string is valid JSON and a legitimate
			// value (e.g. a delete-all edit produces newText ""). Neither
			// field may be trimmed or truncated: the client renders this
			// against the real file and a partial diff would not match it.
			// "oldText" stays nullable and is only omitted for new files.
			newText := diff.NewContent
			var oldPtr *string
			if !diff.IsNew {
				oldText := diff.OldContent
				oldPtr = &oldText
			}
			contents = append(contents, acp.ToolCallContent{
				Type:    "diff",
				Path:    path,
				OldText: oldPtr,
				NewText: &newText,
			})
		}
	}

	return contents
}

func toolCallOutputContents(result *builtin.Result, maxText int) []acp.ToolCallContent {
	if result == nil || len(result.Data) == 0 {
		return nil
	}

	var contents []acp.ToolCallContent
	for _, key := range []string{"stdout", "stderr", "output"} {
		val, ok := result.Data[key]
		if !ok {
			continue
		}
		if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
			label := strings.ToUpper(key)
			text := fmt.Sprintf("[%s]\n%s", label, truncateWithLimit(s, maxText))
			contents = append(contents, acp.ToolCallContent{
				Type:    "content",
				Content: &acp.ContentBlock{Type: "text", Text: text},
			})
		}
	}
	return contents
}

// toolCallKind maps a Buckley tool name to an ACP tool_call kind. The names
// here must match pkg/tool/builtin's actual Tool.Name() values -- verified
// against the registry, not the older tool names some clients may remember
// (shell_command, patch_file, headless_browse, terminal_editor, and
// mark_resolved were all renamed and no longer exist as registered tools).
func toolCallKind(name string) string {
	switch name {
	case "read_file", "list_directory", "find_files", "file_exists",
		"git_status", "git_diff", "git_log", "git_blame", "list_merge_conflicts":
		return acp.ToolKindRead
	case "search_text", "search_replace", "find_symbol", "find_references", "analyze_complexity", "find_duplicates":
		return acp.ToolKindSearch
	case "write_file", "edit_file", "insert_text", "delete_lines", "apply_patch", "rename_symbol", "extract_function", "mark_conflict_resolved":
		return acp.ToolKindEdit
	case "run_shell", "run_tests", "edit_file_terminal":
		return acp.ToolKindExecute
	case "browse_url":
		return acp.ToolKindFetch
	default:
		return acp.ToolKindOther
	}
}

func toolCallTitle(name string, params map[string]any) string {
	switch name {
	case "read_file":
		if path := toolCallParamString(params, "path"); path != "" {
			return "Read " + path
		}
	case "write_file":
		if path := toolCallParamString(params, "path"); path != "" {
			return "Write " + path
		}
	case "edit_file", "insert_text", "delete_lines", "apply_patch":
		if path := toolCallParamString(params, "path"); path != "" {
			return "Edit " + path
		}
	case "search_text":
		if query := toolCallParamString(params, "query"); query != "" {
			return "Search: " + truncate(query, 80)
		}
	case "search_replace":
		if query := toolCallParamString(params, "query"); query != "" {
			return "Search/replace: " + truncate(query, 80)
		}
	case "run_shell":
		if cmd := toolCallParamString(params, "command"); cmd != "" {
			return "Run: " + truncate(cmd, 80)
		}
	case "run_tests":
		if target := toolCallParamString(params, "target"); target != "" {
			return "Run tests: " + truncate(target, 80)
		}
	}
	return name
}

func toolCallLocations(params map[string]any, workDir string) []acp.ToolCallLocation {
	path := toolCallParamString(params, "path")
	if path == "" {
		return nil
	}
	return []acp.ToolCallLocation{{Path: resolveACPPath(path, workDir)}}
}

// resolveACPPath resolves a tool-provided path to an absolute path against
// the session's working directory -- never the ACP process's own cwd, which
// can differ from the editor's session/new cwd when serving multiple
// sessions or workspaces (S2, S9). An already-absolute path is returned
// cleaned and unchanged.
func resolveACPPath(path, workDir string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return path
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		if abs, err := filepath.Abs(path); err == nil {
			return abs
		}
		return path
	}
	return filepath.Clean(filepath.Join(workDir, path))
}

func toolCallParamString(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	if val, ok := params[key]; ok {
		if s, ok := val.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func toolCallRawOutput(result *builtin.Result, err error) any {
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	if result == nil {
		return nil
	}
	payload := map[string]any{
		"success": result.Success,
	}
	if result.Error != "" {
		payload["error"] = result.Error
	}
	if len(result.Data) > 0 {
		payload["data"] = result.Data
	}
	if len(result.DisplayData) > 0 {
		payload["display"] = result.DisplayData
	}
	return payload
}

func shouldNudgeForTools(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	intentPhrases := []string{
		"i'll", "i will", "let me", "i can", "i'm going to", "i am going to",
	}
	actionPhrases := []string{
		"search", "check", "look", "scan", "read", "open", "inspect", "review", "browse", "find",
		"run", "execute", "test",
	}
	intent := false
	for _, phrase := range intentPhrases {
		if strings.Contains(lower, phrase) {
			intent = true
			break
		}
	}
	if !intent {
		return false
	}
	for _, phrase := range actionPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

func executeACPToolCall(ctx context.Context, registry *tool.Registry, name string, params map[string]any, callID string) (*builtin.Result, error) {
	if registry == nil {
		return nil, fmt.Errorf("tool registry unavailable")
	}
	if params == nil {
		params = make(map[string]any)
	}
	if callID != "" {
		params[tool.ToolCallIDParam] = callID
	}
	return registry.ExecuteWithContext(ctx, name, params)
}

func formatACPToolResult(result *builtin.Result, err error) string {
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if result == nil {
		return "No result"
	}
	output, outputErr := tool.ToModelOutput(result)
	if outputErr != nil {
		return fmt.Sprintf("Error: encoding tool result: %v", outputErr)
	}
	return output
}

func formatACPToolDisplay(result *builtin.Result, err error) string {
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	if result == nil {
		return "No result"
	}
	if !result.Success {
		if result.Error != "" {
			return fmt.Sprintf("Error: %s", result.Error)
		}
		return "Error"
	}
	if len(result.DisplayData) > 0 {
		if msg, ok := result.DisplayData["message"].(string); ok && msg != "" && len(result.DisplayData) == 1 {
			return msg
		}
		if data, err := json.MarshalIndent(result.DisplayData, "", "  "); err == nil {
			return string(data)
		}
	}
	if len(result.Data) > 0 {
		data, err := json.MarshalIndent(result.Data, "", "  ")
		if err == nil {
			return string(data)
		}
	}
	return "Success"
}

func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func truncateWithLimit(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}

// acpTodoStoreAdapter is an in-memory todo store for ACP sessions.
// Todo items are ephemeral per-session since ACP sessions are short-lived.
type acpTodoStoreAdapter struct {
	sessionID string
	mu        sync.Mutex
	todos     []builtin.TodoItem
	nextID    int64
}

func (a *acpTodoStoreAdapter) CreateTodo(todo *builtin.TodoItem) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nextID++
	todo.ID = a.nextID
	todo.SessionID = a.sessionID
	a.todos = append(a.todos, *todo)
	return nil
}

func (a *acpTodoStoreAdapter) UpdateTodoStatus(id int64, status string, errorMessage string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := range a.todos {
		if a.todos[i].ID == id {
			a.todos[i].Status = status
			a.todos[i].ErrorMessage = errorMessage
			return nil
		}
	}
	return nil
}

func (a *acpTodoStoreAdapter) GetTodos(sessionID string) ([]builtin.TodoItem, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if sessionID != a.sessionID {
		return nil, nil
	}
	result := make([]builtin.TodoItem, len(a.todos))
	copy(result, a.todos)
	return result, nil
}

func (a *acpTodoStoreAdapter) GetActiveTodo(sessionID string) (*builtin.TodoItem, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if sessionID != a.sessionID {
		return nil, nil
	}
	for i := range a.todos {
		if a.todos[i].Status == "in_progress" {
			return &a.todos[i], nil
		}
	}
	return nil, nil
}

func (a *acpTodoStoreAdapter) DeleteTodos(sessionID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if sessionID == a.sessionID {
		a.todos = nil
	}
	return nil
}

func (a *acpTodoStoreAdapter) CreateCheckpoint(checkpoint *builtin.TodoCheckpointData) error {
	// Checkpoints are not persisted for ephemeral ACP sessions
	return nil
}

func (a *acpTodoStoreAdapter) GetLatestCheckpoint(sessionID string) (*builtin.TodoCheckpointData, error) {
	return nil, nil
}

func (a *acpTodoStoreAdapter) EnsureSession(sessionID string) error {
	return nil
}
