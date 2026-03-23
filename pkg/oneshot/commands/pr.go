package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/tools"
)

// PRDefinition implements oneshot.Definition for pull request generation.
type PRDefinition struct {
	// BaseBranch overrides automatic base branch detection.
	BaseBranch string
}

// PRResult is the structured output from the generate_pull_request tool.
type PRResult struct {
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	Changes       []string `json:"changes"`
	Testing       []string `json:"testing"`
	Breaking      bool     `json:"breaking,omitempty"`
	Issues        []string `json:"issues,omitempty"`
	ReviewersHint string   `json:"reviewers_hint,omitempty"`
}

// FormatBody generates the PR body in markdown format.
func (pr PRResult) FormatBody() string {
	var b strings.Builder

	b.WriteString("## Summary\n\n")
	b.WriteString(pr.Summary)
	b.WriteString("\n\n")

	b.WriteString("## Changes\n\n")
	for _, change := range pr.Changes {
		b.WriteString("- ")
		b.WriteString(change)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("## Testing\n\n")
	for _, test := range pr.Testing {
		b.WriteString("- ")
		b.WriteString(test)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if pr.Breaking {
		b.WriteString("## Breaking Changes\n\n")
		b.WriteString("This PR contains breaking changes. Please review carefully.\n\n")
	}

	if len(pr.Issues) > 0 {
		b.WriteString("## Related Issues\n\n")
		for _, issue := range pr.Issues {
			b.WriteString("- Closes #")
			b.WriteString(issue)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if pr.ReviewersHint != "" {
		b.WriteString("## Review Focus\n\n")
		b.WriteString(pr.ReviewersHint)
		b.WriteString("\n\n")
	}

	return strings.TrimSpace(b.String())
}

func (PRDefinition) Name() string { return "pr" }

func (PRDefinition) Tool() tools.Definition {
	return tools.Definition{
		Name:        "generate_pull_request",
		Description: "Generate a structured pull request title and description based on the branch changes",
		Parameters: tools.ObjectSchema(
			map[string]tools.Property{
				"title": tools.StringProperty(
					"PR title: concise summary of changes, typically 50-72 chars",
				),
				"summary": tools.StringProperty(
					"2-4 sentence high-level summary of what this PR accomplishes and why",
				),
				"changes": tools.ArrayProperty(
					"Bullet points describing the key changes made",
					tools.Property{Type: "string"},
				),
				"testing": tools.ArrayProperty(
					"How to test these changes. Include specific commands, URLs, or manual steps.",
					tools.Property{Type: "string"},
				),
				"breaking": tools.BoolProperty(
					"Whether this PR contains breaking changes",
				),
				"issues": tools.ArrayProperty(
					"Related issue numbers (without # prefix)",
					tools.Property{Type: "string"},
				),
				"reviewers_hint": tools.StringProperty(
					"Optional hint about what reviewers should focus on",
				),
			},
			"title", "summary", "changes", "testing",
		),
	}
}

func (d PRDefinition) ContextSources() []oneshot.ContextSource {
	base := d.BaseBranch
	if base == "" {
		base = "main"
	}
	return []oneshot.ContextSource{
		{Type: "git_diff", Params: map[string]string{"base": base}},
		{Type: "git_log", Params: map[string]string{"base": base}},
		{Type: "git_files", Params: map[string]string{"base": base}},
		{Type: "agents_md"},
	}
}

func (PRDefinition) SystemPrompt() string {
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

func (PRDefinition) BuildPrompt(ctx *oneshot.Context) string {
	var b strings.Builder

	b.WriteString("Generate a pull request for the following changes.\n\n")

	if log, ok := ctx.Sources["git_log:main"]; ok && log != "" {
		b.WriteString("## Commits\n\n```\n")
		b.WriteString(log)
		b.WriteString("\n```\n\n")
	}

	if files, ok := ctx.Sources["git_files:main"]; ok && files != "" {
		b.WriteString("## Changed Files\n\n")
		b.WriteString(files)
		b.WriteString("\n\n")
	}

	if diff, ok := ctx.Sources["git_diff:main"]; ok && diff != "" {
		b.WriteString("## Full Diff\n\n```diff\n")
		b.WriteString(diff)
		b.WriteString("\n```\n\n")
	}

	if agents, ok := ctx.Sources["agents_md"]; ok && agents != "" {
		b.WriteString("## Project Context (AGENTS.md)\n\n")
		b.WriteString(agents)
		b.WriteString("\n\n")
	}

	b.WriteString("Call the generate_pull_request tool with the PR details.")

	return b.String()
}

func (PRDefinition) Validate(result json.RawMessage) error {
	var pr PRResult
	if err := json.Unmarshal(result, &pr); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	if strings.TrimSpace(pr.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if len(pr.Title) > 100 {
		return fmt.Errorf("title too long: %d chars (max 100)", len(pr.Title))
	}
	if strings.TrimSpace(pr.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if len(pr.Changes) == 0 {
		return fmt.Errorf("at least one change is required")
	}
	if len(pr.Testing) == 0 {
		return fmt.Errorf("at least one testing instruction is required")
	}
	return nil
}

func (PRDefinition) Unmarshal(result json.RawMessage) (any, error) {
	var pr PRResult
	if err := json.Unmarshal(result, &pr); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &pr, nil
}
