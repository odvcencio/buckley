package pr

import (
	"fmt"
	"strings"
)

// SystemPrompt returns the system prompt for PR generation.
func SystemPrompt() string {
	return `You are a pull request generator. Your job is to create clear, informative PR descriptions.

IMPORTANT: You MUST call the generate_pull_request tool with your response. Do not output plain text.

Guidelines for good PRs:
- Title should be action-oriented and concise (50-72 chars ideal)
- Summary should explain the "why" - what problem does this solve?
- Changes should be concrete - what did you actually do?
- Testing should be actionable - how can a reviewer verify this works?
- Call out breaking changes explicitly
- Link related issues when applicable

Focus on helping reviewers understand and validate the changes efficiently.`
}

// BuildPrompt creates the user prompt from the assembled context.
func BuildPrompt(ctx *Context) string {
	var b strings.Builder

	b.WriteString("Generate a pull request for the following changes.\n\n")

	// Branch info
	b.WriteString(fmt.Sprintf("Branch: %s → %s\n", ctx.Branch, ctx.BaseBranch))
	b.WriteString(fmt.Sprintf("Commits: %d\n\n", len(ctx.Commits)))

	// Commit summary
	if len(ctx.Commits) > 0 {
		b.WriteString("## Commits\n\n")
		for _, c := range ctx.Commits {
			b.WriteString(fmt.Sprintf("- %s %s\n", c.Hash[:7], c.Subject))
			if c.Body != "" {
				// Include first few lines of commit body for context
				lines := strings.Split(c.Body, "\n")
				for i, line := range lines {
					if i >= 3 {
						break
					}
					if strings.TrimSpace(line) != "" {
						b.WriteString(fmt.Sprintf("    %s\n", line))
					}
				}
			}
		}
		b.WriteString("\n")
	}

	// Diff stats
	if ctx.DiffSummary != "" {
		b.WriteString("## Diff Summary\n\n")
		b.WriteString("```\n")
		b.WriteString(ctx.DiffSummary)
		b.WriteString("\n```\n\n")
	}

	// Full diff
	if ctx.FullDiff != "" {
		b.WriteString("## Full Diff\n\n")
		b.WriteString("```diff\n")
		b.WriteString(ctx.FullDiff)
		b.WriteString("\n```\n\n")
	}

	// Project context
	if ctx.AgentsMD != "" {
		b.WriteString("## Project Context (AGENTS.md)\n\n")
		b.WriteString(ctx.AgentsMD)
		b.WriteString("\n\n")
	}

	b.WriteString("Call the generate_pull_request tool with the PR details.")

	return b.String()
}
