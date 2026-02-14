package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/tool"
)

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
	c.mu.Lock()
	cfg := c.cfg
	c.mu.Unlock()
	modelID := ""
	if cfg != nil {
		modelID = strings.TrimSpace(cfg.Models.Execution)
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

	// Simulate trimming for stats display.
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
	if !c.isCurrentSession(sess) {
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
