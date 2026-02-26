package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

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

func makePromptHandler(
	cfg *config.Config,
	mgr *model.Manager,
	store *storage.Store,
	projectContext *projectcontext.ProjectContext,
	defaultWorkDir string,
	logf func(string, ...any),
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
	logf func(string, ...any),
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
		registry.ConfigureDockerSandbox(cfg, workDir)
		registry.SetWorkDir(workDir)
	}
	if cfg != nil {
		registry.SetSandboxConfig(cfg.Sandbox.ToSandboxConfig(workDir))
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
	pb := newPromptBuilder(budgetTokens)

	pb.appendSection(defaultACPSystemPrompt, true)

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

		remaining := pb.remaining()
		if remaining > 0 {
			if projectSection != "" && conversation.CountTokens(projectSection) <= remaining {
				pb.appendSection(projectSection, false)
			} else if summarySection != "" && conversation.CountTokens(summarySection) <= remaining {
				pb.appendSection(summarySection, false)
			}
		}
	}

	if strings.TrimSpace(workDir) != "" {
		pb.appendSection("\n\nWorking directory: "+workDir, true)
	}
	if skills != nil {
		if desc := strings.TrimSpace(skills.GetDescriptions()); desc != "" {
			pb.appendSection("\n\n"+desc, false)
		}
	}

	return strings.TrimSpace(pb.String())
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
