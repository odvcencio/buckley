package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/tools"
)

// commitActions are the allowed action verbs for commits.
var commitActions = []string{
	"add", "fix", "update", "refactor", "remove", "improve",
	"rename", "move", "revert", "merge", "bump", "release",
	"format", "optimize", "simplify", "extract", "inline",
	"document", "test", "build", "ci",
}

// CommitDefinition implements oneshot.Definition for commit message generation.
type CommitDefinition struct{}

// CommitResult is the strongly-typed result of generate_commit.
type CommitResult struct {
	Action   string   `json:"action"`
	Scope    string   `json:"scope,omitempty"`
	Subject  string   `json:"subject"`
	Body     []string `json:"body"`
	Breaking bool     `json:"breaking,omitempty"`
	Issues   []string `json:"issues,omitempty"`
}

// Header formats the commit header line.
func (cr CommitResult) Header() string {
	if cr.Scope != "" {
		return cr.Action + "(" + cr.Scope + "): " + cr.Subject
	}
	return cr.Action + ": " + cr.Subject
}

// Format returns the full commit message.
func (cr CommitResult) Format() string {
	msg := cr.Header() + "\n\n"
	for _, bullet := range cr.Body {
		msg += "- " + bullet + "\n"
	}
	if cr.Breaking {
		msg += "\nBREAKING CHANGE: " + cr.Subject + "\n"
	}
	if len(cr.Issues) > 0 {
		msg += "\n"
		for _, issue := range cr.Issues {
			msg += "Closes #" + issue + "\n"
		}
	}
	return msg
}

func (CommitDefinition) Name() string { return "commit" }

func (CommitDefinition) Tool() tools.Definition {
	return tools.Definition{
		Name:        "generate_commit",
		Description: "Generate a structured git commit message based on staged changes. Returns action-style commit with header and body bullets.",
		Parameters: tools.ObjectSchema(
			map[string]tools.Property{
				"action": tools.StringEnumProperty(
					"The action verb describing what this commit does",
					commitActions...,
				),
				"scope": tools.StringProperty(
					"The component, package, or area affected (optional)",
				),
				"subject": {
					Type:        "string",
					Description: "Short summary of the change, imperative mood, no period, max 50 chars",
					MaxLength:   72,
				},
				"body": tools.ArrayProperty(
					"Bullet points explaining WHAT changed and WHY (not how)",
					tools.StringProperty("A single bullet point"),
				),
				"breaking": tools.BoolProperty(
					"Whether this commit introduces a breaking change",
				),
				"issues": tools.ArrayProperty(
					"Related issue numbers without # prefix",
					tools.StringProperty("Issue number"),
				),
			},
			"action", "subject", "body",
		),
	}
}

func (CommitDefinition) ContextSources() []oneshot.ContextSource {
	return []oneshot.ContextSource{
		{Type: "git_diff", Params: map[string]string{"staged": "true"}},
		{Type: "git_files", Params: map[string]string{"staged": "true"}},
		{Type: "agents_md"},
	}
}

func (CommitDefinition) SystemPrompt() string {
	return `You are a git commit message generator. Analyze the staged changes and generate a clear, informative commit message.

Use the generate_commit tool to produce your response. The tool expects:
- action: The verb describing what this commit does (add, fix, update, refactor, etc.)
- scope: Optional component/area (e.g., "api", "ui", "config")
- subject: Short summary, imperative mood, no period, ~50 chars
- body: Bullet points explaining WHAT changed and WHY

Guidelines:
- Focus on the "what" and "why", not the "how"
- Be specific but concise
- Match body detail to change size
- Group related changes into single bullets
- Use imperative mood ("Add feature" not "Added feature")`
}

func (CommitDefinition) BuildPrompt(ctx *oneshot.Context) string {
	var b strings.Builder

	if agents, ok := ctx.Sources["agents_md"]; ok && agents != "" {
		b.WriteString("## Project Guidelines\n\n")
		b.WriteString(agents)
		b.WriteString("\n\n")
	}

	if files, ok := ctx.Sources["git_files:staged"]; ok && files != "" {
		b.WriteString("## Staged Files\n\n")
		b.WriteString(files)
		b.WriteString("\n\n")
	}

	if diff, ok := ctx.Sources["git_diff:staged"]; ok && diff != "" {
		b.WriteString("## Diff\n\n```diff\n")
		b.WriteString(diff)
		b.WriteString("\n```\n")
	}

	return b.String()
}

func (CommitDefinition) Validate(result json.RawMessage) error {
	var cr CommitResult
	if err := json.Unmarshal(result, &cr); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	if cr.Action == "" {
		return fmt.Errorf("action is required")
	}
	if cr.Subject == "" {
		return fmt.Errorf("subject is required")
	}
	if len(cr.Body) == 0 {
		return fmt.Errorf("body requires at least one bullet")
	}
	return nil
}

func (CommitDefinition) Unmarshal(result json.RawMessage) (any, error) {
	var cr CommitResult
	if err := json.Unmarshal(result, &cr); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	return &cr, nil
}
