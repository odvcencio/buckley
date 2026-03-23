package commands

import (
	"time"

	"github.com/odvcencio/buckley/pkg/prompts"
)

// ReviewBranchDef implements oneshot.RLMDefinition for branch code review.
//
// Unlike commit/PR which use single-tool invoke+retry, review runs a full
// RLM sub-agent with multi-turn tool access (read, write, bash, grep, glob).
// The agent produces free-form markdown which is parsed into structured data.
type ReviewBranchDef struct{}

func (ReviewBranchDef) Name() string { return "review" }

func (ReviewBranchDef) SystemPrompt() string {
	return prompts.ReviewBranchWithToolsPrompt(time.Now())
}

func (ReviewBranchDef) AllowedTools() []string {
	return []string{"read", "glob", "grep", "bash", "write"}
}

func (ReviewBranchDef) ParseResult(response string) (any, error) {
	return &ReviewRLMResult{
		Review: response,
		Parsed: ParseReview(response),
	}, nil
}

// ReviewProjectDef implements oneshot.RLMDefinition for project-wide review.
type ReviewProjectDef struct{}

func (ReviewProjectDef) Name() string { return "review-project" }

func (ReviewProjectDef) SystemPrompt() string {
	return prompts.ReviewProjectPrompt(time.Now())
}

func (ReviewProjectDef) AllowedTools() []string {
	return []string{"read", "glob", "grep", "bash"}
}

func (ReviewProjectDef) ParseResult(response string) (any, error) {
	return &ReviewRLMResult{
		Review: response,
		Parsed: ParseReview(response),
	}, nil
}

// ReviewPRDef implements oneshot.RLMDefinition for PR review.
type ReviewPRDef struct{}

func (ReviewPRDef) Name() string { return "review-pr" }

func (ReviewPRDef) SystemPrompt() string {
	return prompts.ReviewPRPrompt(time.Now())
}

func (ReviewPRDef) AllowedTools() []string {
	return []string{"read", "glob", "grep", "bash"}
}

func (ReviewPRDef) ParseResult(response string) (any, error) {
	return &ReviewRLMResult{
		Review: response,
		Parsed: ParseReview(response),
	}, nil
}

// FixFindingDef implements oneshot.RLMDefinition for applying a fix to a finding.
type FixFindingDef struct{}

func (FixFindingDef) Name() string { return "fix-finding" }

func (FixFindingDef) SystemPrompt() string {
	return `You are a code fixer. Your job is to apply precise fixes to code based on review findings.

RULES:
1. Read the file first to understand context
2. Apply the MINIMUM change needed to fix the issue
3. Do NOT refactor unrelated code
4. Do NOT add features or improvements beyond the fix
5. Verify the fix compiles (run 'go build ./...' or equivalent)
6. Report exactly what you changed

TOOLS:
- read: Read file contents
- write: Write file (use sparingly - only for the fix)
- bash: Run commands (build, test, etc.)
- glob: Find files
- grep: Search code

OUTPUT:
After applying the fix, summarize:
1. What file(s) you changed
2. What the change was (brief)
3. Whether it compiles

Be concise. The user knows the context.`
}

func (FixFindingDef) AllowedTools() []string {
	return []string{"read", "glob", "grep", "bash", "write"}
}

func (FixFindingDef) ParseResult(response string) (any, error) {
	return &FixResult{
		Summary: response,
	}, nil
}

// ReviewRLMResult is the typed output from a review RLM execution.
type ReviewRLMResult struct {
	// Review is the full markdown review text.
	Review string

	// Parsed is the structured review data extracted from the markdown.
	Parsed *ParsedReview
}

// FixResult contains the result of fixing a finding.
type FixResult struct {
	// Summary describes what was changed.
	Summary string

	// FilesChanged lists files that were modified.
	FilesChanged []string
}
