package pr

import (
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/tools"
)

// GeneratePRTool defines the structured contract for PR generation.
// The model calls this tool with the PR details - no parsing needed.
var GeneratePRTool = tools.Definition{
	Name:        "generate_pull_request",
	Description: "Generate a structured pull request title and description based on the branch changes",
	Parameters: tools.ObjectSchema(
		map[string]tools.Property{
			"title": tools.StringProperty(
				"PR title: concise summary of changes, typically 50-72 chars. " +
					"Format: 'action(scope): summary' or just 'action: summary'",
			),
			"summary": tools.StringProperty(
				"2-4 sentence high-level summary of what this PR accomplishes and why",
			),
			"changes": tools.ArrayProperty(
				"Bullet points describing the key changes made. Each should explain WHAT changed.",
				tools.Property{Type: "string"},
			),
			"testing": tools.ArrayProperty(
				"How to test these changes. Include specific commands, URLs, or manual steps.",
				tools.Property{Type: "string"},
			),
			"breaking": tools.BoolProperty(
				"Whether this PR contains breaking changes that require migration or attention",
			),
			"issues": tools.ArrayProperty(
				"Related issue numbers (without # prefix) that this PR addresses",
				tools.Property{Type: "string"},
			),
			"reviewers_hint": tools.StringProperty(
				"Optional hint about what reviewers should focus on or areas of uncertainty",
			),
		},
		"title", "summary", "changes", "testing",
	),
}

// PRResult is the structured output from the generate_pull_request tool.
// Strongly typed - no parsing needed.
type PRResult struct {
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	Changes       []string `json:"changes"`
	Testing       []string `json:"testing"`
	Breaking      bool     `json:"breaking,omitempty"`
	Issues        []string `json:"issues,omitempty"`
	ReviewersHint string   `json:"reviewers_hint,omitempty"`
}

// Validate checks that the PR result meets basic requirements.
func (pr PRResult) Validate() error {
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

// FormatBody generates the PR body in markdown format.
func (pr PRResult) FormatBody() string {
	var b strings.Builder

	// Summary section
	b.WriteString("## Summary\n\n")
	b.WriteString(pr.Summary)
	b.WriteString("\n\n")

	// Changes section
	b.WriteString("## Changes\n\n")
	for _, change := range pr.Changes {
		b.WriteString("- ")
		b.WriteString(change)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Testing section
	b.WriteString("## Testing\n\n")
	for _, test := range pr.Testing {
		b.WriteString("- ")
		b.WriteString(test)
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Breaking changes
	if pr.Breaking {
		b.WriteString("## Breaking Changes\n\n")
		b.WriteString("This PR contains breaking changes. Please review carefully.\n\n")
	}

	// Issues
	if len(pr.Issues) > 0 {
		b.WriteString("## Related Issues\n\n")
		for _, issue := range pr.Issues {
			b.WriteString("- Closes #")
			b.WriteString(issue)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Reviewers hint
	if pr.ReviewersHint != "" {
		b.WriteString("## Review Focus\n\n")
		b.WriteString(pr.ReviewersHint)
		b.WriteString("\n\n")
	}

	return strings.TrimSpace(b.String())
}

func init() {
	// Register the tool in the tools registry
	tools.MustRegister(GeneratePRTool)

	// Register the command in the oneshot registry
	oneshot.MustRegisterBuiltin(
		"pr",
		"Generate a structured pull request from branch changes",
		GeneratePRTool,
	)
}
