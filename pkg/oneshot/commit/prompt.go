package commit

import (
	"fmt"
	"strings"
)

// BuildPrompt constructs the prompt for commit generation.
// The prompt provides rich context but doesn't demand a specific format -
// the tool schema handles structure.
func BuildPrompt(ctx *Context) string {
	var b strings.Builder

	// Project context (if available)
	if ctx.AgentsMD != "" {
		b.WriteString("## Project Guidelines\n\n")
		b.WriteString(ctx.AgentsMD)
		b.WriteString("\n\n")
	}

	// Repository context
	b.WriteString("## Repository\n\n")
	b.WriteString(fmt.Sprintf("- **Root:** %s\n", ctx.RepoRoot))
	b.WriteString(fmt.Sprintf("- **Branch:** %s\n", ctx.Branch))
	if len(ctx.Areas) > 0 {
		b.WriteString(fmt.Sprintf("- **Affected areas:** %s\n", strings.Join(ctx.Areas, ", ")))
	}
	b.WriteString("\n")

	// Diff statistics
	b.WriteString("## Changes Summary\n\n")
	b.WriteString(fmt.Sprintf("- **Files changed:** %d\n", ctx.Stats.Files))
	b.WriteString(fmt.Sprintf("- **Insertions:** %d\n", ctx.Stats.Insertions))
	b.WriteString(fmt.Sprintf("- **Deletions:** %d\n", ctx.Stats.Deletions))
	if ctx.Stats.BinaryFiles > 0 {
		b.WriteString(fmt.Sprintf("- **Binary files:** %d\n", ctx.Stats.BinaryFiles))
	}

	// Detail guidance based on change size
	detail := suggestedDetail(ctx.Stats)
	b.WriteString(fmt.Sprintf("\n**Suggested body detail:** %d-%d bullet points\n\n", detail.Min, detail.Max))

	// Staged files
	b.WriteString("## Staged Files\n\n")
	for _, f := range ctx.Files {
		status := fileStatusDescription(f.Status)
		if f.OldPath != "" {
			b.WriteString(fmt.Sprintf("- %s: %s → %s\n", status, f.OldPath, f.Path))
		} else {
			b.WriteString(fmt.Sprintf("- %s: %s\n", status, f.Path))
		}
	}
	b.WriteString("\n")

	// Diff content
	b.WriteString("## Diff\n\n```diff\n")
	b.WriteString(ctx.Diff)
	b.WriteString("\n```\n")

	return b.String()
}

// DetailGuidance suggests how detailed the commit body should be.
type DetailGuidance struct {
	Min int
	Max int
}

// suggestedDetail returns guidance based on change size.
func suggestedDetail(stats DiffStats) DetailGuidance {
	total := stats.TotalChanges()
	files := stats.Files

	switch {
	case total <= 20 && files <= 2:
		return DetailGuidance{Min: 1, Max: 3}
	case total <= 80 && files <= 10:
		return DetailGuidance{Min: 2, Max: 5}
	case total <= 200 && files <= 25:
		return DetailGuidance{Min: 3, Max: 7}
	case total <= 500:
		return DetailGuidance{Min: 4, Max: 9}
	default:
		return DetailGuidance{Min: 5, Max: 12}
	}
}

// fileStatusDescription returns a human-readable status.
func fileStatusDescription(status string) string {
	switch status {
	case "A":
		return "Added"
	case "M":
		return "Modified"
	case "D":
		return "Deleted"
	case "R":
		return "Renamed"
	case "C":
		return "Copied"
	case "T":
		return "Type changed"
	case "U":
		return "Unmerged"
	default:
		return status
	}
}

// SystemPrompt returns the system prompt for commit generation.
// Note: This is minimal because the tool schema provides structure.
func SystemPrompt() string {
	return `You are a git commit message generator. Analyze the staged changes and generate a clear, informative commit message.

Use the generate_commit tool to produce your response. The tool expects:
- action: The verb describing what this commit does (add, fix, update, refactor, etc.)
- scope: Optional component/area (e.g., "api", "ui", "config")
- subject: Short summary, imperative mood, no period
- body: Bullet points explaining WHAT changed and WHY

CRITICAL: The full header line (action + scope + subject) MUST be <= 72 characters.
Calculate your budget: "action(scope): " uses characters, so adjust subject length.
Examples:
- "add(ui): dark mode toggle" = 25 chars ✓
- "refactor(execution): unify execution patterns" = 46 chars ✓
- "refactor(execution): unify execution around ToolRunner pattern with modes" = 74 chars ✗ TOO LONG

Guidelines:
- Focus on the "what" and "why", not the "how"
- Be specific but concise
- Match body detail to change size
- Group related changes into single bullets
- Use imperative mood ("Add feature" not "Added feature")`
}
