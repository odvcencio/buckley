package main

import (
	"encoding/json"
	"strings"

	"github.com/odvcencio/buckley/pkg/config"
	projectcontext "github.com/odvcencio/buckley/pkg/context"
	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
)

const (
	defaultContextWindow     = 8192
	defaultPromptBudgetRatio = 0.9
	messageOverheadTokens    = 4
)

func promptBudget(cfg *config.Config, mgr *model.Manager, modelID string) int {
	contextWindow := defaultContextWindow
	if mgr != nil {
		if info, err := mgr.GetModelInfo(modelID); err == nil && info != nil && info.ContextLength > 0 {
			contextWindow = info.ContextLength
		}
	}
	ratio := defaultPromptBudgetRatio
	if cfg != nil && cfg.Memory.AutoCompactThreshold > 0 && cfg.Memory.AutoCompactThreshold <= 1 {
		ratio = cfg.Memory.AutoCompactThreshold
	}
	budget := int(float64(contextWindow) * ratio)
	if budget <= 0 {
		budget = contextWindow
	}
	return budget
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
