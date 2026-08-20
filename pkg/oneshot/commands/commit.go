package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/tools"
)

// commitActions are the allowed action verbs for commits.
var commitActions = commitmsg.AllowedActions

// CommitDefinition implements oneshot.Definition for commit message generation.
type CommitDefinition struct{}

// CommitResult is the strongly-typed result of generate_commit.
type CommitResult struct {
	Action         string   `json:"action"`
	Scope          string   `json:"scope,omitempty"`
	Subject        string   `json:"subject"`
	Body           []string `json:"body"`
	Breaking       bool     `json:"breaking,omitempty"`
	BreakingReason string   `json:"breaking_reason,omitempty"`
	Issues         []string `json:"issues,omitempty"`
}

// Header formats the commit header line.
func (cr CommitResult) Header() string {
	if cr.Scope != "" {
		return cr.Action + "(" + cr.Scope + "): " + cr.Subject
	}
	return cr.Action + ": " + cr.Subject
}

// Format returns the full commit message.
//
// Related issues render as references ("Refs #N"), never as GitHub close
// directives, and body bullets are sanitized for stray close keywords.
// See pkg/commitmsg for why.
func (cr CommitResult) Format() string {
	cr.Action = commitmsg.NormalizeAction(cr.Action)
	cr.Scope = strings.TrimSpace(cr.Scope)
	cr.Subject = strings.TrimSpace(cr.Subject)
	msg := cr.Header() + "\n\n"
	for _, bullet := range cr.Body {
		bullet = commitmsg.NormalizeBullet(bullet)
		if bullet == "" {
			continue
		}
		msg += "- " + commitmsg.NeutralizeCloseDirectives(bullet) + "\n"
	}
	if cr.Breaking {
		msg += "\nBREAKING CHANGE: " + breakingDescription(cr.BreakingReason, cr.Subject, cr.Body) + "\n"
	}
	if len(cr.Issues) > 0 {
		msg += "\n"
		for _, issue := range cr.Issues {
			if line := commitmsg.IssueRefLine(issue); line != "" {
				msg += line + "\n"
			}
		}
	}
	return msg
}

func breakingDescription(reason, subject string, body []string) string {
	if reason = commitmsg.NormalizeBullet(reason); reason != "" {
		return commitmsg.NeutralizeCloseDirectives(reason)
	}
	for _, bullet := range body {
		if bullet = commitmsg.NormalizeBullet(bullet); bullet != "" {
			return commitmsg.NeutralizeCloseDirectives(bullet)
		}
	}
	return strings.TrimSpace(subject)
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
					Description: "Short summary of the change, imperative mood, no period; do not repeat the action, scope, or breaking marker prefix; action and scope belong only in the structured header; keep the full header within 72 characters",
					MaxLength:   72,
				},
				"body": tools.ArrayProperty(
					"Bullet points explaining WHAT changed and WHY (not how)",
					tools.StringProperty("A single bullet point"),
				),
				"breaking": tools.BoolProperty(
					"Whether this commit introduces a breaking change",
				),
				"breaking_reason": tools.StringProperty(
					"If breaking is true, briefly describe the compatibility impact",
				),
				"issues": tools.ArrayProperty(
					"Issue numbers this change RELATES TO, without # prefix. Rendered as "+
						"references ('Refs #N'), never as close directives — this does not close "+
						"any issue. Do not add a number just because it appears in the diff text.",
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
- The subject/summary is summary text only. Do not repeat an action, scope, or breaking marker prefix in it; action and scope belong only in the structured header (for example, use "avoid the panic", not "fix(ui): avoid the panic")
- body: Bullet points explaining WHAT changed and WHY
- breaking_reason: If breaking is true, briefly explain the compatibility impact

Guidelines:
- Focus on the "what" and "why", not the "how"
- Be specific but concise; use durable high-level wording and do not copy secrets, tokens, private URLs, or user data
- Match body detail to change size
- Group related changes into single bullets
- Use imperative mood ("Add feature" not "Added feature")
- If breaking is true, include a useful breaking_reason instead of repeating the subject`
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
	return commitmsg.ValidateCommitFields(cr.Action, cr.Scope, cr.Subject, cr.Body, cr.Issues)
}

func (CommitDefinition) Unmarshal(result json.RawMessage) (any, error) {
	var cr CommitResult
	if err := json.Unmarshal(result, &cr); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	cr.Action = commitmsg.NormalizeAction(cr.Action)
	cr.Scope = strings.TrimSpace(cr.Scope)
	cr.Subject = strings.TrimSpace(cr.Subject)
	cr.BreakingReason = commitmsg.NormalizeBullet(cr.BreakingReason)
	for i, bullet := range cr.Body {
		cr.Body[i] = commitmsg.NormalizeBullet(bullet)
	}
	issues := cr.Issues[:0]
	for _, issue := range cr.Issues {
		if normalized := commitmsg.NormalizeIssueRef(issue); normalized != "" {
			issues = append(issues, normalized)
		}
	}
	cr.Issues = issues
	return &cr, nil
}
