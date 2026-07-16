package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/tools"
)

// PRDefinition implements oneshot.Definition for pull request generation.
type PRDefinition struct {
	// BaseBranch overrides automatic base branch detection.
	BaseBranch string
}

// PRResult is the structured output from the generate_pull_request tool.
//
// Titles follow the same action(scope): subject grammar as commit headers
// (see CommitResult), so a branch's PR reads like its commits. Issue links
// render reference-only ("Refs #N") — never as GitHub close directives.
// See pkg/commitmsg for why.
type PRResult struct {
	Action        string   `json:"action"`
	Scope         string   `json:"scope,omitempty"`
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	Changes       []string `json:"changes"`
	Testing       []string `json:"testing,omitempty"`
	Breaking      bool     `json:"breaking,omitempty"`
	Issues        []string `json:"issues,omitempty"`
	ReviewersHint string   `json:"reviewers_hint,omitempty"`
}

// Header composes the PR title in the shared commit-header grammar:
// "action(scope): subject". When the model supplied no action (older
// stored results), the raw title is returned unchanged.
func (pr PRResult) Header() string {
	if pr.Action == "" {
		return pr.Title
	}
	if pr.Scope != "" {
		return pr.Action + "(" + pr.Scope + "): " + pr.Title
	}
	return pr.Action + ": " + pr.Title
}

// FormatBody generates the PR body in markdown format.
//
// Free-text bullets are sanitized for stray GitHub close directives and
// related issues render as references ("Refs #N"), matching the commit
// renderer's policy. Empty optional sections are omitted entirely — no
// filler headings.
func (pr PRResult) FormatBody() string {
	var b strings.Builder

	b.WriteString("## Summary\n\n")
	b.WriteString(commitmsg.NeutralizeCloseDirectives(strings.TrimSpace(pr.Summary)))
	b.WriteString("\n\n")

	b.WriteString("## Changes\n\n")
	for _, change := range pr.Changes {
		b.WriteString("- ")
		b.WriteString(commitmsg.NeutralizeCloseDirectives(trimBulletMarker(change)))
		b.WriteString("\n")
	}
	b.WriteString("\n")

	if len(pr.Testing) > 0 {
		b.WriteString("## Testing\n\n")
		for _, test := range pr.Testing {
			b.WriteString("- ")
			b.WriteString(commitmsg.NeutralizeCloseDirectives(trimBulletMarker(test)))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if pr.Breaking {
		b.WriteString("## Breaking Changes\n\n")
		b.WriteString("This PR contains breaking changes. Please review carefully.\n\n")
	}

	if len(pr.Issues) > 0 {
		b.WriteString("## Related Issues\n\n")
		for _, issue := range pr.Issues {
			b.WriteString("- ")
			b.WriteString(commitmsg.IssueRefLine(issue))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if pr.ReviewersHint != "" {
		b.WriteString("## Review Focus\n\n")
		b.WriteString(commitmsg.NeutralizeCloseDirectives(strings.TrimSpace(pr.ReviewersHint)))
		b.WriteString("\n\n")
	}

	return strings.TrimSpace(b.String())
}

func (PRDefinition) Name() string { return "pr" }

func (PRDefinition) Tool() tools.Definition {
	return tools.Definition{
		Name:        "generate_pull_request",
		Description: "Generate a structured pull request title and description based on the branch changes. The title follows the same action(scope): subject grammar as commit headers.",
		Parameters: tools.ObjectSchema(
			map[string]tools.Property{
				"action": tools.StringEnumProperty(
					"The action verb describing what this PR does overall",
					commitActions...,
				),
				"scope": tools.StringProperty(
					"The component, package, or area affected (optional)",
				),
				"title": {
					Type:        "string",
					Description: "PR subject: short summary of the branch, imperative mood, no period, max ~60 chars. Rendered as action(scope): title",
					MaxLength:   72,
				},
				"summary": tools.StringProperty(
					"2-4 sentence high-level summary of what this PR accomplishes and why",
				),
				"changes": tools.ArrayProperty(
					"Bullet points describing WHAT changed and WHY (not how). Match detail to branch size.",
					tools.Property{Type: "string"},
				),
				"testing": tools.ArrayProperty(
					"How a reviewer can actually verify these changes: real commands, URLs, or manual steps. "+
						"OMIT entirely when there is nothing concrete to run (docs-only or trivial changes) — never fabricate filler steps.",
					tools.Property{Type: "string"},
				),
				"breaking": tools.BoolProperty(
					"Whether this PR contains breaking changes",
				),
				"issues": tools.ArrayProperty(
					"Issue numbers this PR RELATES TO, without # prefix. Rendered as "+
						"references ('Refs #N'), never as close directives — merging does not close "+
						"any issue. Do not add a number just because it appears in the diff text.",
					tools.Property{Type: "string"},
				),
				"reviewers_hint": tools.StringProperty(
					"Optional hint about what reviewers should focus on",
				),
			},
			"action", "title", "summary", "changes",
		),
	}
}

func (d PRDefinition) ContextSources() []oneshot.ContextSource {
	base := d.baseOrDefault()
	return []oneshot.ContextSource{
		{Type: "git_diff", Params: map[string]string{"base": base}},
		{Type: "git_log", Params: map[string]string{"base": base}},
		{Type: "git_files", Params: map[string]string{"base": base}},
		{Type: "agents_md"},
	}
}

func (d PRDefinition) baseOrDefault() string {
	if d.BaseBranch != "" {
		return d.BaseBranch
	}
	return "main"
}

// isCommitAction reports whether action is one of the allowed commit verbs.
func isCommitAction(action string) bool {
	for _, a := range commitActions {
		if a == action {
			return true
		}
	}
	return false
}

// trimBulletMarker strips a leading markdown list marker from a model-supplied
// bullet. Renderers prepend their own "- ", and models intermittently include
// one in the string too, producing "- - item" (observed live).
func trimBulletMarker(s string) string {
	t := strings.TrimSpace(s)
	for _, marker := range []string{"- ", "* ", "• "} {
		if strings.HasPrefix(t, marker) {
			return strings.TrimSpace(strings.TrimPrefix(t, marker))
		}
	}
	return t
}

func (PRDefinition) SystemPrompt() string {
	return `You are a pull request generator. Analyze the branch's commits and diff and generate a clear, informative PR.

IMPORTANT: You MUST call the generate_pull_request tool with your response. Do not output plain text.

The tool expects:
- action: The verb describing what this PR does overall (add, fix, update, refactor, etc.)
- scope: Optional component/area (e.g., "api", "scene3d", "docs")
- title: PR subject, imperative mood, no period — rendered as action(scope): title, same grammar as commit headers
- summary: The "why" — what problem does this branch solve?
- changes: Concrete bullets — WHAT changed and WHY, detail proportional to branch size
- testing: ONLY real, runnable verification steps; omit entirely when none exist

Guidelines:
- Read the commit log first: the PR should synthesize the branch's story, not re-describe each commit
- Be specific but concise; group related changes into single bullets
- Use imperative mood ("Add feature" not "Added feature")
- Call out breaking changes explicitly
- Issues are references only — never phrase anything as closing/fixing an issue number
- Never add attribution, signatures, co-author lines, or "generated with" footers`
}

func (d PRDefinition) BuildPrompt(ctx *oneshot.Context) string {
	var b strings.Builder
	base := d.baseOrDefault()

	if agents, ok := ctx.Sources["agents_md"]; ok && agents != "" {
		b.WriteString("## Project Guidelines\n\n")
		b.WriteString(agents)
		b.WriteString("\n\n")
	}

	b.WriteString("Generate a pull request for the following branch changes (base: " + base + ").\n\n")

	if log, ok := ctx.Sources["git_log:"+base]; ok && log != "" {
		b.WriteString("## Commits\n\n```\n")
		b.WriteString(log)
		b.WriteString("\n```\n\n")
	}

	if files, ok := ctx.Sources["git_files:"+base]; ok && files != "" {
		b.WriteString("## Changed Files\n\n")
		b.WriteString(files)
		b.WriteString("\n\n")
	}

	if diff, ok := ctx.Sources["git_diff:"+base]; ok && diff != "" {
		b.WriteString("## Full Diff\n\n```diff\n")
		b.WriteString(diff)
		b.WriteString("\n```\n\n")
	}

	b.WriteString("Call the generate_pull_request tool with the PR details.")

	return b.String()
}

func (PRDefinition) Validate(result json.RawMessage) error {
	var pr PRResult
	if err := json.Unmarshal(result, &pr); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	if strings.TrimSpace(pr.Action) == "" {
		return fmt.Errorf("action is required")
	}
	// Providers do not all hard-enforce schema enums (observed: a model
	// returning "feat"), so membership is validated here and the retry loop
	// corrects the model.
	if !isCommitAction(pr.Action) {
		return fmt.Errorf("action %q is not an allowed verb (use one of: %s)", pr.Action, strings.Join(commitActions, ", "))
	}
	if strings.TrimSpace(pr.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if len(pr.Header()) > 100 {
		return fmt.Errorf("composed title too long: %d chars (max 100)", len(pr.Header()))
	}
	if strings.TrimSpace(pr.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if len(pr.Changes) == 0 {
		return fmt.Errorf("at least one change is required")
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
