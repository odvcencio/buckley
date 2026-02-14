package tui

import (
	"fmt"
	"strings"

	projectcontext "github.com/odvcencio/buckley/pkg/context"
	"github.com/odvcencio/buckley/pkg/conversation"
	"github.com/odvcencio/buckley/pkg/model"
)

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

func (c *Controller) buildSystemPromptWithBudget(sess *SessionState, budgetTokens int) string {
	base := "You are Buckley, an AI development assistant. "
	base += "You help users with software engineering tasks including writing code, debugging, and explaining concepts. "
	base += "Match the user's requested level of detail. "
	base += "If asked to validate or cite code, use tools and include file paths and code snippets. "
	base += "Put user-facing details in the final response, not hidden reasoning.\n\n"
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

	appendSection(base, true)

	rawProject := ""
	projectSummary := ""
	if c.projectCtx != nil && c.projectCtx.Loaded {
		rawProject = strings.TrimSpace(c.projectCtx.RawContent)
		projectSummary = buildProjectContextSummary(c.projectCtx)
	}

	if budgetTokens > 0 && (rawProject != "" || projectSummary != "") {
		projectSection := ""
		if rawProject != "" {
			projectSection = "Project Context:\n" + rawProject + "\n\n"
		}
		summarySection := ""
		if projectSummary != "" {
			summarySection = "Project Context (summary):\n" + projectSummary + "\n\n"
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

	workDir := fmt.Sprintf("Working directory: %s\n", c.workDir)
	appendSection(workDir, true)

	appendSection("If the user asks to create a new skill, draft name/description/body and call create_skill to save it.\n", true)

	if sess != nil && sess.SkillRegistry != nil {
		if desc := strings.TrimSpace(sess.SkillRegistry.GetDescriptions()); desc != "" {
			appendSection("\n"+desc+"\n", false)
		}
	}

	return strings.TrimSpace(b.String()) + "\n"
}

func (c *Controller) buildRequestContext(sess *SessionState, modelID, prompt string, allowedTools []string) (string, *conversation.Conversation, int) {
	budget := c.promptBudget(modelID)
	registry := c.registry
	if sess != nil && sess.ToolRegistry != nil {
		registry = sess.ToolRegistry
	}
	if budget > 0 {
		budget -= estimateToolTokens(registry, allowedTools)
		if prompt != "" {
			budget -= estimateMessageTokens("user", prompt)
		}
		if budget < 0 {
			budget = 0
		}
	}

	systemPrompt := c.buildSystemPromptWithBudget(sess, budget)
	if budget > 0 {
		budget -= estimateMessageTokens("system", systemPrompt)
		if budget < 0 {
			budget = 0
		}
	}

	// Return full conversation - let the strategy's ContextBuilder handle trimming
	// to avoid double-trimming and ensure tool call fields are preserved.
	var conv *conversation.Conversation
	if sess != nil {
		conv = sess.Conversation
	}

	return systemPrompt, conv, budget
}

// buildMessagesForSession constructs the message list for the API using a specific session.
func (c *Controller) buildMessagesForSession(sess *SessionState, modelID string, allowedTools []string) []model.Message {
	messages := []model.Message{}

	systemPrompt, conv, budget := c.buildRequestContext(sess, modelID, "", allowedTools)
	messages = append(messages, model.Message{
		Role:    "system",
		Content: systemPrompt,
	})

	// Trim conversation to budget before converting to model messages.
	trimmed := trimConversationToBudget(conv, budget)
	if trimmed != nil {
		messages = append(messages, trimmed.ToModelMessages()...)
	}

	return messages
}
