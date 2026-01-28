package headless

import (
	"encoding/json"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
)

const (
	headlessDefaultContextWindow     = 0
	headlessDefaultPromptBudgetRatio = 0.9
	headlessMessageOverheadTokens    = 4
	headlessFallbackContextWindow    = 128000
)

func headlessPromptBudget(cfg *config.Config, mgr *model.Manager, modelID string) int {
	contextWindow := headlessDefaultContextWindow
	if mgr != nil {
		if info, err := mgr.GetModelInfo(modelID); err == nil && info != nil && info.ContextLength > 0 {
			contextWindow = info.ContextLength
		}
	}
	if contextWindow <= 0 {
		contextWindow = headlessFallbackContextWindow
	}
	ratio := headlessDefaultPromptBudgetRatio
	if cfg != nil && cfg.Memory.AutoCompactThreshold > 0 && cfg.Memory.AutoCompactThreshold <= 1 {
		ratio = cfg.Memory.AutoCompactThreshold
	}
	budget := int(float64(contextWindow) * ratio)
	if budget <= 0 {
		budget = contextWindow
	}
	return budget
}

func headlessEstimateMessageTokens(role, content string) int {
	if strings.TrimSpace(content) == "" {
		return 0
	}
	return conversation.CountTokens(content) + conversation.CountTokens(role) + headlessMessageOverheadTokens
}

func headlessEstimateConversationMessageTokens(msg conversation.Message) int {
	if msg.Tokens > 0 {
		return msg.Tokens + headlessMessageOverheadTokens
	}
	content := conversation.GetContentAsString(msg.Content)
	if msg.Role == "assistant" && strings.TrimSpace(content) == "" && strings.TrimSpace(msg.Reasoning) != "" {
		content = msg.Reasoning
	}
	return headlessEstimateMessageTokens(msg.Role, content)
}

func headlessEstimateToolTokens(registry *tool.Registry) int {
	if registry == nil {
		return 0
	}
	tools := registry.ToOpenAIFunctions()
	if len(tools) == 0 {
		return 0
	}
	raw, err := json.Marshal(tools)
	if err != nil {
		return 0
	}
	return conversation.CountTokens(string(raw))
}

func headlessSystemPrompt(conv *conversation.Conversation) string {
	if conv != nil {
		for _, msg := range conv.Messages {
			if msg.Role != "system" {
				continue
			}
			content := strings.TrimSpace(conversation.GetContentAsString(msg.Content))
			if content != "" {
				return content
			}
		}
	}
	return defaultHeadlessSystemPrompt
}

func filterNonSystemMessages(conv *conversation.Conversation) *conversation.Conversation {
	if conv == nil {
		return nil
	}
	if len(conv.Messages) == 0 {
		return &conversation.Conversation{SessionID: conv.SessionID}
	}
	msgs := make([]conversation.Message, 0, len(conv.Messages))
	for _, msg := range conv.Messages {
		if msg.Role == "system" {
			continue
		}
		msgs = append(msgs, msg)
	}
	return &conversation.Conversation{
		SessionID: conv.SessionID,
		Messages:  msgs,
	}
}

func trimHeadlessConversationToBudget(conv *conversation.Conversation, budgetTokens int) *conversation.Conversation {
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
		tokens := headlessEstimateConversationMessageTokens(conv.Messages[i])
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

	if start == len(conv.Messages) {
		return &conversation.Conversation{SessionID: conv.SessionID}
	}

	return &conversation.Conversation{
		SessionID:  conv.SessionID,
		Messages:   append([]conversation.Message{}, conv.Messages[start:]...),
		TokenCount: used,
	}
}

func buildHeadlessMessages(r *Runner, modelID string) []model.Message {
	messages := []model.Message{}

	budget := headlessPromptBudget(r.config, r.modelManager, modelID)
	budget -= headlessEstimateToolTokens(r.tools)
	if budget < 0 {
		budget = 0
	}

	systemPrompt := headlessSystemPrompt(r.conv)
	if budget > 0 {
		budget -= headlessEstimateMessageTokens("system", systemPrompt)
		if budget < 0 {
			budget = 0
		}
	}

	messages = append(messages, model.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	filtered := filterNonSystemMessages(r.conv)
	trimmed := trimHeadlessConversationToBudget(filtered, budget)
	if trimmed != nil {
		messages = append(messages, trimmed.ToModelMessages()...)
	}

	return messages
}
