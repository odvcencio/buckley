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

	"github.com/odvcencio/buckley/pkg/acp"
	"github.com/odvcencio/buckley/pkg/config"
	projectcontext "github.com/odvcencio/buckley/pkg/context"
	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/skill"
	"github.com/odvcencio/buckley/pkg/storage"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/tool/builtin"
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

	// Create the ACP agent
	agent := acp.NewAgent("Buckley", version, acp.AgentHandlers{
		OnSessionModes: func(ctx context.Context, session *acp.AgentSession) (*acp.SessionModeState, error) {
			return buildACPModelModes(cfg, mgr), nil
		},
		OnPrompt: makePromptHandler(cfg, mgr, store, projectContext, cwd, logf),
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
		OnRequestPermission: func(ctx context.Context, toolName, description string, args json.RawMessage, risk string) (bool, bool, error) {
			logf("permission request: %s (%s risk)", toolName, risk)
			// In ACP mode, the editor handles permission requests
			// For now, auto-approve low-risk, deny high-risk
			switch risk {
			case "low":
				return true, false, nil
			case "medium":
				return true, false, nil
			case "high", "destructive":
				return false, false, nil
			default:
				return true, false, nil
			}
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

		modelOverride := resolveACPModelOverride(cfg, mgr, session.Mode)
		responseText, err := runACPLoop(ctx, cfg, mgr, state, modelOverride, stream)
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
	projectCtx *projectcontext.ProjectContext
	workDir    string
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
	registerMCPTools(cfg, registry)

	// Wire todo persistence for the ACP session
	registry.SetTodoStore(&acpTodoStoreAdapter{sessionID: session.ID})

	state := &acpSessionState{
		conv:       conv,
		registry:   registry,
		skills:     skills,
		skillState: skillState,
		projectCtx: projectContext,
		workDir:    workDir,
	}
	sessions[session.ID] = state
	return state
}

func buildACPSystemPromptWithBudget(projectContext *projectcontext.ProjectContext, workDir string, skills *skill.Registry, budgetTokens int) string {
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

	appendSection(defaultACPSystemPrompt, true)

	rawProject := ""
	projectSummary := ""
	if projectContext != nil && projectContext.Loaded {
		rawProject = strings.TrimSpace(projectContext.RawContent)
		projectSummary = buildProjectContextSummary(projectContext)
	}

	if budgetTokens > 0 && (rawProject != "" || projectSummary != "") {
		projectSection := ""
		if rawProject != "" {
			projectSection = "\n\nProject Context:\n" + rawProject
		}
		summarySection := ""
		if projectSummary != "" {
			summarySection = "\n\nProject Context (summary):\n" + projectSummary
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

	if strings.TrimSpace(workDir) != "" {
		appendSection("\n\nWorking directory: "+workDir, true)
	}
	if skills != nil {
		if desc := strings.TrimSpace(skills.GetDescriptions()); desc != "" {
			appendSection("\n\n"+desc, false)
		}
	}

	return strings.TrimSpace(b.String())
}

func trimACPConversationToBudget(conv *conversation.Conversation, budgetTokens int) *conversation.Conversation {
	if conv == nil {
		return nil
	}
	if budgetTokens <= 0 || len(conv.Messages) == 0 {
		return &conversation.Conversation{SessionID: conv.SessionID}
	}

	var systemMsgs []conversation.Message
	var otherMsgs []conversation.Message
	for _, msg := range conv.Messages {
		content := conversation.GetContentAsString(msg.Content)
		if msg.Role == "system" && strings.HasPrefix(strings.TrimSpace(content), strings.TrimSpace(defaultACPSystemPrompt)) {
			continue
		}
		if msg.Role == "system" {
			systemMsgs = append(systemMsgs, msg)
			continue
		}
		otherMsgs = append(otherMsgs, msg)
	}

	used := 0
	kept := make([]conversation.Message, 0, len(systemMsgs)+len(otherMsgs))
	for _, msg := range systemMsgs {
		tokens := estimateConversationMessageTokens(msg)
		if used+tokens > budgetTokens {
			break
		}
		used += tokens
		kept = append(kept, msg)
	}

	start := len(otherMsgs)
	lastIdx := len(otherMsgs) - 1
	for i := lastIdx; i >= 0; i-- {
		tokens := estimateConversationMessageTokens(otherMsgs[i])
		if i == lastIdx && tokens > budgetTokens {
			start = i
			used += tokens
			break
		}
		if used+tokens > budgetTokens {
			break
		}
		used += tokens
		start = i
	}

	if start < len(otherMsgs) {
		kept = append(kept, otherMsgs[start:]...)
	}

	return &conversation.Conversation{
		SessionID:  conv.SessionID,
		Messages:   kept,
		TokenCount: used,
	}
}

func buildACPRequestMessages(cfg *config.Config, mgr *model.Manager, state *acpSessionState, modelID string, allowedTools []string, includeTools bool) []model.Message {
	messages := []model.Message{}

	budget := promptBudget(cfg, mgr, modelID)
	if includeTools && state != nil {
		budget -= estimateToolTokens(state.registry, allowedTools)
		if budget < 0 {
			budget = 0
		}
	}

	var systemPrompt string
	if state != nil {
		systemPrompt = buildACPSystemPromptWithBudget(state.projectCtx, state.workDir, state.skills, budget)
	} else {
		systemPrompt = strings.TrimSpace(defaultACPSystemPrompt)
	}
	if budget > 0 {
		budget -= estimateMessageTokens("system", systemPrompt)
		if budget < 0 {
			budget = 0
		}
	}

	messages = append(messages, model.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	if state != nil {
		trimmed := trimACPConversationToBudget(state.conv, budget)
		if trimmed != nil {
			messages = append(messages, trimmed.ToModelMessages()...)
		}
	}

	return messages
}

func handleACPUserSkillCommand(prompt string, state *acpSessionState) (bool, string) {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" {
		return false, ""
	}
	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		return false, ""
	}
	cmd := strings.ToLower(parts[0])
	if cmd != "/skill" && cmd != "/skills" {
		return false, ""
	}
	if state == nil || state.skills == nil {
		return true, "Skill system unavailable in this session."
	}
	if cmd == "/skills" || len(parts) == 1 || strings.EqualFold(parts[1], "list") {
		names := make([]string, 0)
		for _, s := range state.skills.List() {
			names = append(names, s.GetName())
		}
		sort.Strings(names)
		if len(names) == 0 {
			return true, "No skills available."
		}
		var b strings.Builder
		b.WriteString("Available skills:\n")
		for _, name := range names {
			b.WriteString("- " + name + "\n")
		}
		return true, strings.TrimSpace(b.String())
	}

	name := strings.TrimSpace(strings.Join(parts[1:], " "))
	if name == "" {
		return true, "Usage: /skill <name>."
	}

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
		return true, fmt.Sprintf("Error activating skill %q: %v", name, err)
	}
	if result == nil || !result.Success {
		if result != nil && result.Error != "" {
			return true, fmt.Sprintf("Error activating skill %q: %s", name, result.Error)
		}
		return true, fmt.Sprintf("Error activating skill %q.", name)
	}

	message, _ := result.Data["message"].(string)
	content, _ := result.Data["content"].(string)
	if content != "" && message != "" {
		return true, message + "\n\n" + content
	}
	if content != "" {
		return true, content
	}
	if message != "" {
		return true, message
	}
	return true, fmt.Sprintf("Skill %q activated.", name)
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
	state *acpSessionState,
	modelOverride string,
	stream acp.StreamFunc,
) (string, error) {
	if state == nil {
		return "", fmt.Errorf("session state unavailable")
	}
	conv := state.conv
	registry := state.registry
	skillState := state.skillState
	modelID := strings.TrimSpace(modelOverride)
	if modelID == "" {
		modelID = cfg.Models.Execution
		if modelID == "" {
			modelID = cfg.Models.Planning
		}
	}

	useTools := registry != nil
	toolChoice := "auto"

	maxNudges := 2
	nudgeCount := 0
	lastPhase := ""
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		lastPhase = sendACPPhaseUpdate(stream, lastPhase, "Thinking…")

		var tools []map[string]any
		allowedTools := []string{}
		if skillState != nil {
			allowedTools = skillState.ToolFilter()
		}
		if useTools && registry != nil {
			tools = registry.ToOpenAIFunctionsFiltered(allowedTools)
			if len(tools) == 0 {
				useTools = false
			}
		}
		toolsEnabled := len(tools) > 0

		req := model.ChatRequest{
			Model:    modelID,
			Messages: buildACPRequestMessages(cfg, mgr, state, modelID, allowedTools, useTools),
		}
		if useTools {
			req.Tools = tools
			req.ToolChoice = toolChoice
		}
		if reasoning := strings.TrimSpace(cfg.Models.Reasoning); reasoning != "" && mgr.SupportsReasoning(modelID) {
			req.Reasoning = &model.ReasoningConfig{Effort: reasoning}
		}

		resp, err := mgr.ChatCompletion(ctx, req)
		if err != nil {
			if useTools && isToolUnsupportedError(err) {
				useTools = false
				continue
			}
			return "", err
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no response choices")
		}

		msg := resp.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			text, err := model.ExtractTextContent(msg.Content)
			if err != nil {
				return "", err
			}
			if useTools && toolsEnabled && nudgeCount < maxNudges && shouldNudgeForTools(text) {
				nudgeCount++
				conv.AddUserMessage("Use tools to take action now. Pick a tool and call it; do not answer with prose alone.")
				continue
			}
			sendACPPhaseUpdate(stream, lastPhase, "Finalizing response…")
			conv.AddAssistantMessageWithReasoning(text, msg.Reasoning)
			return text, nil
		}

		lastPhase = sendACPPhaseUpdate(stream, lastPhase, fmt.Sprintf("Executing %d tool call(s)…", len(msg.ToolCalls)))

		for i := range msg.ToolCalls {
			if msg.ToolCalls[i].ID == "" {
				msg.ToolCalls[i].ID = fmt.Sprintf("tool-%d", i+1)
			}
		}
		conv.AddToolCallMessage(msg.ToolCalls)

		for i, tc := range msg.ToolCalls {
			params, err := parseACPToolParams(tc.Function.Arguments)
			if err != nil {
				rawParams := map[string]any{"raw": tc.Function.Arguments}
				lastPhase = sendACPPhaseUpdate(stream, lastPhase, fmt.Sprintf("Running %s (%d/%d)…", toolCallTitle(tc.Function.Name, nil), i+1, len(msg.ToolCalls)))
				sendACPToolCallStart(stream, tc, rawParams)
				toolText := fmt.Sprintf("Error: invalid tool arguments: %v", err)
				conv.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)
				sendACPToolCallUpdate(stream, tc, rawParams, acp.ToolCallStatusFailed, toolText, map[string]any{
					"error": err.Error(),
				}, nil)
				continue
			}

			lastPhase = sendACPPhaseUpdate(stream, lastPhase, fmt.Sprintf("Running %s (%d/%d)…", toolCallTitle(tc.Function.Name, params), i+1, len(msg.ToolCalls)))
			sendACPToolCallStart(stream, tc, params)

			if !tool.IsToolAllowed(tc.Function.Name, allowedTools) {
				toolText := fmt.Sprintf("Error: tool %s not allowed by active skills", tc.Function.Name)
				conv.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)
				sendACPToolCallUpdate(stream, tc, params, acp.ToolCallStatusFailed, toolText, map[string]any{
					"error": toolText,
				}, nil)
				continue
			}

			result, execErr := executeACPToolCall(registry, tc.Function.Name, params, tc.ID)
			toolText := formatACPToolResult(result, execErr)
			displayText := formatACPToolDisplay(result, execErr)
			conv.AddToolResponseMessage(tc.ID, tc.Function.Name, toolText)

			status := acp.ToolCallStatusCompleted
			if execErr != nil || (result != nil && !result.Success) {
				status = acp.ToolCallStatusFailed
			}
			sendACPToolCallUpdate(stream, tc, params, status, displayText, toolCallRawOutput(result, execErr), result)
		}
	}
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

func sendACPToolCallStart(stream acp.StreamFunc, call model.ToolCall, params map[string]any) {
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
		Locations:     toolCallLocations(params),
	}
	_ = stream(update)
}

func sendACPToolCallUpdate(stream acp.StreamFunc, call model.ToolCall, params map[string]any, status, text string, rawOutput any, result *builtin.Result) {
	if stream == nil {
		return
	}
	update := acp.SessionUpdate{
		SessionUpdate: acp.SessionUpdateToolCallUpdate,
		ToolCallID:    call.ID,
		Status:        status,
		RawOutput:     rawOutput,
	}
	contents := buildToolCallContents(text, result)
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

func buildToolCallContents(text string, result *builtin.Result) []acp.ToolCallContent {
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
			path := strings.TrimSpace(diff.FilePath)
			if path != "" {
				if abs, err := filepath.Abs(path); err == nil {
					path = abs
				}
			}
			oldText := strings.TrimSpace(diff.OldContent)
			newText := strings.TrimSpace(diff.NewContent)
			var oldPtr *string
			var newPtr *string
			if oldText != "" {
				oldText = truncateWithLimit(oldText, maxText)
				oldPtr = &oldText
			}
			if newText != "" {
				newText = truncateWithLimit(newText, maxText)
				newPtr = &newText
			}
			if preview := strings.TrimSpace(diff.Preview); preview != "" {
				contents = append(contents, acp.ToolCallContent{
					Type:    "content",
					Content: &acp.ContentBlock{Type: "text", Text: truncateWithLimit("[DIFF]\n"+preview, maxText)},
				})
			}
			contents = append(contents, acp.ToolCallContent{
				Type:    "diff",
				Path:    path,
				OldText: oldPtr,
				NewText: newPtr,
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

func toolCallKind(name string) string {
	switch name {
	case "read_file", "list_directory", "find_files", "file_exists", "get_file_info",
		"git_status", "git_diff", "git_log", "git_blame", "list_merge_conflicts":
		return acp.ToolKindRead
	case "search_text", "search_replace", "find_symbol", "find_references", "analyze_complexity", "find_duplicates":
		return acp.ToolKindSearch
	case "write_file", "edit_file", "insert_text", "delete_lines", "patch_file", "rename_symbol", "extract_function", "mark_resolved":
		return acp.ToolKindEdit
	case "shell_command", "run_tests", "terminal_editor":
		return acp.ToolKindExecute
	case "headless_browse":
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
	case "edit_file", "insert_text", "delete_lines", "patch_file":
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
	case "shell_command":
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

func toolCallLocations(params map[string]any) []acp.ToolCallLocation {
	path := toolCallParamString(params, "path")
	if path == "" {
		return nil
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return []acp.ToolCallLocation{{Path: path}}
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

func executeACPToolCall(registry *tool.Registry, name string, params map[string]any, callID string) (*builtin.Result, error) {
	if registry == nil {
		return nil, fmt.Errorf("tool registry unavailable")
	}
	if params == nil {
		params = make(map[string]any)
	}
	if callID != "" {
		params[tool.ToolCallIDParam] = callID
	}
	return registry.Execute(name, params)
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
