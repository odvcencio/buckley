package context

import (
	"encoding/json"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
)

const (
	DefaultContextWindow     = 8192
	DefaultPromptBudgetRatio = 0.9
	MessageOverheadTokens    = 4
)

// PromptBudget computes the token budget for a prompt given the model's
// context window and the configured auto-compact threshold.
func PromptBudget(cfg *config.Config, mgr *model.Manager, modelID string) int {
	contextWindow := DefaultContextWindow
	if mgr != nil {
		if info, err := mgr.GetModelInfo(modelID); err == nil && info != nil && info.ContextLength > 0 {
			contextWindow = info.ContextLength
		}
	}
	ratio := DefaultPromptBudgetRatio
	if cfg != nil && cfg.Memory.AutoCompactThreshold > 0 && cfg.Memory.AutoCompactThreshold <= 1 {
		ratio = cfg.Memory.AutoCompactThreshold
	}
	budget := int(float64(contextWindow) * ratio)
	if budget <= 0 {
		budget = contextWindow
	}
	return budget
}

// EstimateMessageTokens estimates the token count for a single message
// with the given role and content, including per-message overhead.
func EstimateMessageTokens(role, content string) int {
	if strings.TrimSpace(content) == "" {
		return 0
	}
	return conversation.CountTokens(content) + conversation.CountTokens(role) + MessageOverheadTokens
}

// EstimateConversationMessageTokens estimates the token count for a
// conversation.Message, using the cached token count when available.
func EstimateConversationMessageTokens(msg conversation.Message) int {
	if msg.Tokens > 0 {
		return msg.Tokens + MessageOverheadTokens
	}
	content := conversation.GetContentAsString(msg.Content)
	if msg.Role == "assistant" && strings.TrimSpace(content) == "" && strings.TrimSpace(msg.Reasoning) != "" {
		content = msg.Reasoning
	}
	return EstimateMessageTokens(msg.Role, content)
}

// EstimateToolTokens estimates the token count for the tool definitions
// that would be sent with a request, filtered by the allowed tool list.
func EstimateToolTokens(registry *tool.Registry, allowedTools []string) int {
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

// BuildProjectContextSummary formats a ProjectContext into a compact
// text summary suitable for inclusion in prompts.
func BuildProjectContextSummary(ctx *ProjectContext) string {
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
