package execution

import (
	"encoding/json"
	"strings"

	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
)

const executionMessageOverheadTokens = 4

func contextBudgetForRequest(models ModelClient, registry *tool.Registry, req ExecutionRequest, modelID string) int {
	contextWindow := contextWindowForModel(models, modelID)
	if contextWindow <= 0 {
		return 0
	}

	budget := contextWindow
	if req.SystemPrompt != "" {
		budget -= estimateMessageTokens("system", req.SystemPrompt)
	}
	if req.Prompt != "" {
		budget -= estimateMessageTokens("user", req.Prompt)
	}
	budget -= estimateToolTokens(registry, req.AllowedTools)

	if budget < 0 {
		budget = 0
	}
	return budget
}

func contextWindowForModel(models ModelClient, modelID string) int {
	if models == nil {
		return 0
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return 0
	}
	if provider, ok := models.(interface{ GetContextLength(string) (int, error) }); ok {
		if length, err := provider.GetContextLength(modelID); err == nil && length > 0 {
			return length
		}
	}
	if provider, ok := models.(interface{ GetModelInfo(string) (*model.ModelInfo, error) }); ok {
		if info, err := provider.GetModelInfo(modelID); err == nil && info != nil && info.ContextLength > 0 {
			return info.ContextLength
		}
	}
	return 0
}

func estimateMessageTokens(role, content string) int {
	if strings.TrimSpace(content) == "" {
		return 0
	}
	return conversation.CountTokens(content) +
		conversation.CountTokens(role) +
		executionMessageOverheadTokens
}

func estimateToolTokens(registry *tool.Registry, allowed []string) int {
	if registry == nil {
		return 0
	}

	tools := registry.List()
	if len(tools) == 0 {
		return 0
	}

	if len(allowed) > 0 {
		allowedSet := make(map[string]struct{}, len(allowed))
		for _, name := range allowed {
			allowedSet[strings.TrimSpace(name)] = struct{}{}
		}
		filtered := make([]tool.Tool, 0, len(tools))
		for _, t := range tools {
			if _, ok := allowedSet[t.Name()]; ok {
				filtered = append(filtered, t)
			}
		}
		tools = filtered
	}

	if len(tools) == 0 {
		return 0
	}

	defs := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		defs = append(defs, tool.ToOpenAIFunction(t))
	}

	raw, err := json.Marshal(defs)
	if err != nil {
		return 0
	}

	return conversation.CountTokens(string(raw))
}
