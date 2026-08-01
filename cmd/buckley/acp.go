package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"m31labs.dev/buckley/v2/pkg/acp"
	"m31labs.dev/buckley/v2/pkg/config"
	projectcontext "m31labs.dev/buckley/v2/pkg/context"
	"m31labs.dev/buckley/v2/pkg/conversation"
	"m31labs.dev/buckley/v2/pkg/model"
	"m31labs.dev/buckley/v2/pkg/prompts"
	"m31labs.dev/buckley/v2/pkg/rules"
	"m31labs.dev/buckley/v2/pkg/skill"
	"m31labs.dev/buckley/v2/pkg/storage"
	"m31labs.dev/buckley/v2/pkg/tool"
	"m31labs.dev/buckley/v2/pkg/tool/builtin"
)

const defaultACPSystemPrompt = `You are Buckley, an AI development assistant with access to tools.

CRITICAL BEHAVIOR:
- Use tools to complete tasks, not just describe what you would do.
- Continue calling tools until the task is fully complete.
- Do not stop after one tool call if more work is needed.
- After each tool result, evaluate if more actions are required.

TOOL USAGE:
- Use search_text to find files and code locations.
- Use read_file to examine file contents.
- Use edit_file to make changes.
- Use run_shell for commands, builds, and tests.
- Use create_skill to generate new SKILL.md files when the user requests a new skill.
- Chain multiple tool calls as needed.

ANTI-PATTERNS TO AVOID:
- Do not respond with just text when tools are needed.
- Do not stop after acknowledging a task without executing it.
- Do not describe what you would do without actually doing it.

Always take action with tools. If you are uncertain, use tools to investigate.`

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
		logger, err = os.OpenFile(*logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
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
	agent = acp.NewAgent("Buckley", version, acp.AgentHandlers{
		OnSessionModes: func(ctx context.Context, session *acp.AgentSession) (*acp.SessionModeState, error) {
			return buildACPModelModes(cfg, mgr), nil
		},
		OnPrompt: makePromptHandler(cfg, mgr, store, projectContext, cwd, logf, &agent),
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
) func(context.Context, *acp.AgentSession, []acp.ContentBlock, acp.StreamFunc) (*acp.PromptResult, error) {
	sessions := make(map[string]*acpSessionState)
	var sessionsMu sync.Mutex

	return func(ctx context.Context, session *acp.AgentSession, content []acp.ContentBlock, stream acp.StreamFunc) (*acp.PromptResult, error) {
		prompt := extractACPPrompt(content)
		if strings.TrimSpace(prompt) == "" {
			return nil, fmt.Errorf("empty prompt")
		}
		logf("prompt: %s", truncate(prompt, 100))

		state := getACPSessionState(&sessionsMu, sessions, session, projectContext, cfg, defaultWorkDir, logf)
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
		responseText, err := runACPLoop(ctx, cfg, mgr, state.conv, state.registry, state.skillState, state.engine, modelOverride, state.workDir, session.ID, agent, logf, stream)
		if err != nil {
			logf("prompt error: %v", err)
			stream(acp.NewAgentMessageChunk(fmt.Sprintf("\n\nError: %v", err)))
			return nil, err
		}

		if responseText != "" {
			stream(acp.NewAgentMessageChunk(responseText))
		}

		return &acp.PromptResult{StopReason: "end_turn"}, nil
	}
}

type acpSessionState struct {
	mu         sync.Mutex
	conv       *conversation.Conversation
	registry   *tool.Registry
	skills     *skill.Registry
	skillState *skill.RuntimeState
	engine     *rules.Engine
	// workDir is the session's working directory, used to resolve tool-call
	// locations and diff paths -- never the ACP process's own cwd, which may
	// differ from the editor's session/new cwd (S2, S9).
	workDir string
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
	mu *sync.Mutex,
	sessions map[string]*acpSessionState,
	session *acp.AgentSession,
	projectContext *projectcontext.ProjectContext,
	cfg *config.Config,
	defaultWorkDir string,
	logf func(string, ...interface{}),
) *acpSessionState {
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

	conv.AddSystemMessage(buildACPSystemPrompt(projectContext, workDir, skills, engine, ""))

	state := &acpSessionState{
		conv:       conv,
		registry:   registry,
		skills:     skills,
		skillState: skillState,
		engine:     engine,
		workDir:    workDir,
	}
	sessions[session.ID] = state
	return state
}

func buildACPSystemPrompt(projectContext *projectcontext.ProjectContext, workDir string, skills *skill.Registry, engine *rules.Engine, agentProfile string) string {
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
	modelID := resolveACPExecutionModel(cfg, mgr, engine, modelOverride)
	evaluator := newACPEvaluator(engine)
	useTools := acpModelCanUseTools(registry, mgr, modelID)
	nudgeCount := 0
	finalizeNudgeCount := 0
	toolsExecuted := false
	lastPhase := ""
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		lastPhase = sendACPPhaseUpdate(stream, lastPhase, "Thinking…")

		toolTurn := buildACPToolTurn(registry, skillState, evaluator, useTools)
		useTools = toolTurn.UseTools
		req := buildACPChatRequest(cfg, mgr, engine, conv, modelID, toolTurn)

		resp, err := mgr.ChatCompletion(ctx, req)
		if err != nil {
			if useTools && isToolUnsupportedError(err) {
				useTools = false
				continue
			}
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", model.NoResponseChoicesError(req, resp)
		}

		msg := resp.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			text, err := model.ExtractTextContent(msg.Content)
			if err != nil {
				return "", err
			}
			if strings.TrimSpace(text) == "" && toolsExecuted {
				if finalizeNudgeCount == 0 {
					finalizeNudgeCount++
					conv.AddUserMessage("Return the final answer now using the completed tool results. Do not leave the response empty.")
					continue
				}
				return "", fmt.Errorf("model returned an empty final response after tool execution")
			}
			if shouldNudgeACPToolUse(useTools, toolTurn.Enabled, nudgeCount, text) {
				nudgeCount++
				conv.AddUserMessage("Use tools to take action now. Pick a tool and call it; do not answer with prose alone.")
				continue
			}
			if strings.TrimSpace(text) == "" {
				return "", fmt.Errorf("model returned an empty response")
			}
			sendACPPhaseUpdate(stream, lastPhase, "Finalizing response…")
			conv.AddAssistantMessageWithReasoningDetails(text, msg.Reasoning, msg.ReasoningDetails)
			return text, nil
		}

		lastPhase = sendACPPhaseUpdate(stream, lastPhase, fmt.Sprintf("Executing %d tool call(s)…", len(msg.ToolCalls)))
		normalizeACPToolCallIDs(msg.ToolCalls)
		conv.AddToolCallMessageWithReasoning(msg.ToolCalls, msg.Reasoning, msg.ReasoningDetails)
		lastPhase = executeACPToolCalls(ctx, conv, registry, stream, msg.ToolCalls, toolTurn.AllowedTools, lastPhase, workDir, sessionID, agent, logf)
		toolsExecuted = true
	}
}

const acpMaxToolNudges = 2

type acpToolTurn struct {
	Tools        []map[string]any
	AllowedTools []string
	UseTools     bool
	Enabled      bool
}

func resolveACPExecutionModel(cfg *config.Config, mgr *model.Manager, engine *rules.Engine, modelOverride string) string {
	modelID := model.ResolvePhaseModel(cfg, acpReasoningChecker(mgr), engine, "execution", modelOverride)
	if modelID == "" {
		return "openai/gpt-4o"
	}
	return modelID
}

func acpReasoningChecker(mgr *model.Manager) model.ReasoningChecker {
	if mgr == nil {
		return nil
	}
	return mgr
}

func newACPEvaluator(engine *rules.Engine) *rules.EngineAdapter {
	if engine == nil {
		return nil
	}
	return rules.NewEngineAdapter(engine)
}

func acpModelCanUseTools(registry *tool.Registry, mgr *model.Manager, modelID string) bool {
	return registry != nil && (mgr == nil || mgr.SupportsTools(modelID))
}

func buildACPToolTurn(registry *tool.Registry, skillState *skill.RuntimeState, evaluator *rules.EngineAdapter, useTools bool) acpToolTurn {
	turn := acpToolTurn{UseTools: useTools}
	if skillState != nil {
		turn.AllowedTools = skillState.ToolFilter()
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

func buildACPChatRequest(cfg *config.Config, mgr *model.Manager, engine *rules.Engine, conv *conversation.Conversation, modelID string, turn acpToolTurn) model.ChatRequest {
	req := model.ChatRequest{
		Model: modelID,
		// ToModelMessages returns the full portable transcript. Compaction
		// runs once below via CompactModelMessagesForRequest -- an earlier
		// ToEfficientModelMessages pass here would be a redundant compaction
		// on top of that projection.
		Messages:  conv.ToModelMessages(),
		SessionID: conv.SessionID,
	}
	if turn.UseTools {
		req.Tools = turn.Tools
		req.ToolChoice = "auto"
		if mgr != nil && mgr.SupportsParameter(modelID, "parallel_tool_calls") {
			sequential := false
			req.ParallelToolCalls = &sequential
		}
	}
	if effort := model.ResolveReasoningEffort(cfg, acpReasoningChecker(mgr), engine, modelID, "execution"); effort != "" {
		req.Reasoning = &model.ReasoningConfig{Effort: effort}
	}
	contextWindow := 0
	if mgr != nil {
		contextWindow, _ = mgr.GetContextLength(modelID)
	}
	req.Messages = conversation.CompactModelMessagesForRequest(req.Messages, req, contextWindow)
	return req
}

func shouldNudgeACPToolUse(useTools, toolsEnabled bool, nudgeCount int, text string) bool {
	return useTools && toolsEnabled && nudgeCount < acpMaxToolNudges &&
		(strings.TrimSpace(text) == "" || shouldNudgeForTools(text))
}

func normalizeACPToolCallIDs(calls []model.ToolCall) {
	for i := range calls {
		if calls[i].ID == "" {
			calls[i].ID = fmt.Sprintf("tool-%d", i+1)
		}
	}
}

func executeACPToolCalls(ctx context.Context, conv *conversation.Conversation, registry *tool.Registry, stream acp.StreamFunc, calls []model.ToolCall, allowedTools []string, lastPhase string, workDir string, sessionID string, agent *acp.Agent, logf func(string, ...interface{})) string {
	for i, tc := range calls {
		if ctx.Err() != nil {
			return lastPhase
		}
		params, err := parseACPToolParams(tc.Function.Arguments)
		if err != nil {
			rawParams := map[string]any{"raw": tc.Function.Arguments}
			lastPhase = sendACPPhaseUpdate(stream, lastPhase, fmt.Sprintf("Running %s (%d/%d)…", toolCallTitle(tc.Function.Name, nil), i+1, len(calls)))
			sendACPToolCallStart(stream, tc, rawParams, workDir)
			toolText := fmt.Sprintf("Error: invalid tool arguments: %v", err)
			conv.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)
			sendACPToolCallUpdate(stream, tc, rawParams, acp.ToolCallStatusFailed, toolText, map[string]any{
				"error": err.Error(),
			}, nil, workDir)
			continue
		}

		lastPhase = sendACPPhaseUpdate(stream, lastPhase, fmt.Sprintf("Running %s (%d/%d)…", toolCallTitle(tc.Function.Name, params), i+1, len(calls)))
		sendACPToolCallStart(stream, tc, params, workDir)

		if !tool.IsToolAllowed(tc.Function.Name, allowedTools) {
			toolText := fmt.Sprintf("Error: tool %s not allowed by active skills", tc.Function.Name)
			conv.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)
			sendACPToolCallUpdate(stream, tc, params, acp.ToolCallStatusFailed, toolText, map[string]any{
				"error": toolText,
			}, nil, workDir)
			continue
		}

		if allowed, reason := requestACPToolPermission(ctx, agent, registry, sessionID, tc, params, workDir, logf); !allowed {
			toolText := fmt.Sprintf("Permission denied for %s: %s", tc.Function.Name, reason)
			result := &builtin.Result{Success: false, Error: toolText}
			conv.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)
			sendACPToolCallUpdate(stream, tc, params, acp.ToolCallStatusFailed, toolText, map[string]any{
				"error":  toolText,
				"denied": true,
			}, result, workDir)
			continue
		}

		result, execErr := executeACPToolCall(ctx, registry, tc.Function.Name, params, tc.ID)
		toolText := formatACPToolResult(result, execErr)
		displayText := formatACPToolDisplay(result, execErr)
		conv.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)

		status := acp.ToolCallStatusCompleted
		if execErr != nil || (result != nil && !result.Success) {
			status = acp.ToolCallStatusFailed
		}
		sendACPToolCallUpdate(stream, tc, params, status, displayText, toolCallRawOutput(result, execErr), result, workDir)
	}
	return lastPhase
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
// elsewhere for approval gating, not a new ACP-specific notion.
func acpToolRiskImpact(registry *tool.Registry, name string) tool.Impact {
	if registry == nil {
		return tool.ImpactReadOnly
	}
	t, ok := registry.Get(name)
	if !ok {
		return tool.ImpactReadOnly
	}
	return tool.GetMetadata(t).Impact
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
	impact := acpToolRiskImpact(registry, tc.Function.Name)
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
	if !result.Success {
		return fmt.Sprintf("Error: %s", result.Error)
	}
	if msg, shows := result.DisplayData["message"].(string); shows && msg != "" {
		return msg
	}
	if len(result.Data) > 0 {
		data, err := json.MarshalIndent(result.Data, "", "  ")
		if err == nil {
			return string(data)
		}
	}
	return "Success"
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
