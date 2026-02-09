package main

import (
	"github.com/odvcencio/buckley/pkg/config"
	projectcontext "github.com/odvcencio/buckley/pkg/context"
	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/tool"
)

func promptBudget(cfg *config.Config, mgr *model.Manager, modelID string) int {
	return projectcontext.PromptBudget(cfg, mgr, modelID)
}

func estimateMessageTokens(role, content string) int {
	return projectcontext.EstimateMessageTokens(role, content)
}

func estimateConversationMessageTokens(msg conversation.Message) int {
	return projectcontext.EstimateConversationMessageTokens(msg)
}

func estimateToolTokens(registry *tool.Registry, allowedTools []string) int {
	return projectcontext.EstimateToolTokens(registry, allowedTools)
}

func buildProjectContextSummary(ctx *projectcontext.ProjectContext) string {
	return projectcontext.BuildProjectContextSummary(ctx)
}
