package review

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/prompts"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// Runner executes the review flow.
type Runner struct {
	// RLM-based execution (preferred - full tool access)
	rlmRunner *oneshot.RLMRunner

	// Legacy invoker (fallback if RLM not configured)
	invoker *oneshot.DefaultInvoker
	ledger  *transparency.CostLedger
}

// RunnerConfig configures the review runner.
type RunnerConfig struct {
	// For RLM-based execution (preferred)
	Models   *model.Manager
	Registry *tool.Registry
	ModelID  string

	// Legacy config (used if Models not provided)
	Invoker *oneshot.DefaultInvoker
	Ledger  *transparency.CostLedger
}

// NewRunner creates a review runner.
// If Models and Registry are provided, uses RLM for full tool access.
// Otherwise falls back to legacy invoker with limited tools.
func NewRunner(cfg RunnerConfig) *Runner {
	r := &Runner{
		invoker: cfg.Invoker,
		ledger:  cfg.Ledger,
	}

	// Prefer RLM if configured
	if cfg.Models != nil && cfg.Registry != nil && cfg.ModelID != "" {
		r.rlmRunner = oneshot.NewRLMRunner(oneshot.RLMRunnerConfig{
			Models:   cfg.Models,
			Registry: cfg.Registry,
			Ledger:   cfg.Ledger,
			ModelID:  cfg.ModelID,
		})
	}

	return r
}

// RunResult contains the results of a review.
type RunResult struct {
	// Review is the generated review (markdown)
	Review string

	// Parsed contains the structured review data (populated by Parse())
	Parsed *ParsedReview

	// Trace contains transparency data
	Trace *transparency.Trace

	// ContextAudit shows what context was sent
	ContextAudit *transparency.ContextAudit

	// PRInfo contains PR metadata (for PR reviews)
	PRInfo *PRInfo

	// Error if generation failed
	Error error
}

// Parse parses the review markdown into structured data.
func (r *RunResult) Parse() *ParsedReview {
	if r.Parsed == nil && r.Review != "" {
		r.Parsed = ParseReview(r.Review)
	}
	return r.Parsed
}

// ReviewBranch reviews the current branch against base using the full tool ecosystem.
func (r *Runner) ReviewBranch(ctx context.Context, opts BranchContextOptions) (*RunResult, error) {
	// Assemble context
	branchCtx, audit, err := AssembleBranchContext(opts)
	if err != nil {
		return nil, fmt.Errorf("assemble context: %w", err)
	}

	// Build prompts
	systemPrompt := prompts.ReviewBranchWithToolsPrompt(time.Now())
	userPrompt := buildBranchPrompt(branchCtx)

	// Prefer RLM for full tool access
	if r.rlmRunner != nil {
		return r.reviewWithRLM(ctx, systemPrompt, userPrompt, audit)
	}

	// Fallback to legacy invoker (should not happen in production)
	return r.reviewWithLegacyInvoker(ctx, systemPrompt, userPrompt, audit)
}

// reviewWithRLM runs a review using the full RLM tool ecosystem.
func (r *Runner) reviewWithRLM(ctx context.Context, systemPrompt, userPrompt string, audit *transparency.ContextAudit) (*RunResult, error) {
	result := &RunResult{ContextAudit: audit}

	// Run with full tool access
	// Allow read, glob, grep, bash for verification
	allowedTools := []string{"read", "glob", "grep", "bash", "write"}

	rlmResult, err := r.rlmRunner.Run(ctx, systemPrompt, userPrompt, allowedTools)
	if err != nil {
		result.Error = err
		return result, nil
	}

	result.Review = rlmResult.Response
	result.Trace = rlmResult.Trace
	return result, nil
}

// reviewWithLegacyInvoker runs a review using the legacy invoker with limited tools.
// This is a fallback for when RLM is not configured.
func (r *Runner) reviewWithLegacyInvoker(ctx context.Context, systemPrompt, userPrompt string, audit *transparency.ContextAudit) (*RunResult, error) {
	result := &RunResult{ContextAudit: audit}

	if r.invoker == nil {
		result.Error = fmt.Errorf("no invoker configured")
		return result, nil
	}

	// Use simple text response without tools
	response, trace, err := r.invoker.InvokeText(ctx, systemPrompt, userPrompt, audit)
	result.Trace = trace

	if err != nil {
		result.Error = err
		return result, nil
	}

	result.Review = response
	return result, nil
}

// ReviewProject reviews the project as a whole using the full tool ecosystem.
func (r *Runner) ReviewProject(ctx context.Context, opts ProjectContextOptions) (*RunResult, error) {
	// Assemble context
	projectCtx, audit, err := AssembleProjectContext(opts)
	if err != nil {
		return nil, fmt.Errorf("assemble context: %w", err)
	}

	// Build prompts
	systemPrompt := prompts.ReviewProjectPrompt(time.Now())
	userPrompt := buildProjectPrompt(projectCtx)

	// Prefer RLM for full tool access
	if r.rlmRunner != nil {
		return r.reviewWithRLM(ctx, systemPrompt, userPrompt, audit)
	}

	// Fallback to legacy invoker
	return r.reviewWithLegacyInvoker(ctx, systemPrompt, userPrompt, audit)
}

// buildBranchPrompt builds the user prompt for branch review.
func buildBranchPrompt(ctx *BranchContext) string {
	var sb strings.Builder

	sb.WriteString("## Repository Information\n\n")
	sb.WriteString(fmt.Sprintf("- **Root**: %s\n", ctx.RepoRoot))
	sb.WriteString(fmt.Sprintf("- **Branch**: %s\n", ctx.Branch))
	sb.WriteString(fmt.Sprintf("- **Base Branch**: %s\n", ctx.BaseBranch))
	sb.WriteString("\n")

	if ctx.RecentLog != "" {
		sb.WriteString("## Commits on this Branch\n\n")
		sb.WriteString("```\n")
		sb.WriteString(ctx.RecentLog)
		sb.WriteString("\n```\n\n")
	}

	sb.WriteString("## Files Changed\n\n")
	sb.WriteString(fmt.Sprintf("**Summary**: %d files, +%d/-%d lines\n\n",
		ctx.Stats.Files, ctx.Stats.Insertions, ctx.Stats.Deletions))

	sb.WriteString("```\n")
	for _, f := range ctx.Files {
		sb.WriteString(fmt.Sprintf("%s\t%s\n", f.Status, f.Path))
	}
	sb.WriteString("```\n\n")

	sb.WriteString("## Full Diff\n\n")
	sb.WriteString("```diff\n")
	sb.WriteString(ctx.Diff)
	sb.WriteString("\n```\n\n")

	if ctx.Unstaged != "" {
		sb.WriteString("## Unstaged Changes (not yet committed)\n\n")
		sb.WriteString("```diff\n")
		sb.WriteString(ctx.Unstaged)
		sb.WriteString("\n```\n\n")
	}

	if ctx.AgentsMD != "" {
		sb.WriteString("## Project Guidelines (AGENTS.md)\n\n")
		sb.WriteString(ctx.AgentsMD)
		sb.WriteString("\n\n")
	}

	return sb.String()
}

// buildProjectPrompt builds the user prompt for project review.
func buildProjectPrompt(ctx *ProjectContext) string {
	var sb strings.Builder

	sb.WriteString("## Repository Information\n\n")
	sb.WriteString(fmt.Sprintf("- **Root**: %s\n", ctx.RepoRoot))
	sb.WriteString(fmt.Sprintf("- **Branch**: %s\n", ctx.Branch))
	sb.WriteString("\n")

	if ctx.Tree != "" {
		sb.WriteString("## Project Structure\n\n")
		sb.WriteString("```\n")
		sb.WriteString(ctx.Tree)
		sb.WriteString("\n```\n\n")
	}

	if ctx.GoMod != "" {
		sb.WriteString("## go.mod\n\n")
		sb.WriteString("```\n")
		sb.WriteString(ctx.GoMod)
		sb.WriteString("\n```\n\n")
	}

	if ctx.PackageJSON != "" {
		sb.WriteString("## package.json\n\n")
		sb.WriteString("```json\n")
		sb.WriteString(ctx.PackageJSON)
		sb.WriteString("\n```\n\n")
	}

	if ctx.ReadmeMD != "" {
		sb.WriteString("## README.md\n\n")
		sb.WriteString(ctx.ReadmeMD)
		sb.WriteString("\n\n")
	}

	if ctx.AgentsMD != "" {
		sb.WriteString("## AGENTS.md\n\n")
		sb.WriteString(ctx.AgentsMD)
		sb.WriteString("\n\n")
	}

	if ctx.RecentLog != "" {
		sb.WriteString("## Recent Git History\n\n")
		sb.WriteString("```\n")
		sb.WriteString(ctx.RecentLog)
		sb.WriteString("\n```\n\n")
	}

	return sb.String()
}

// FixResult contains the result of fixing a finding.
type FixResult struct {
	// Summary describes what was changed
	Summary string

	// FilesChanged lists files that were modified
	FilesChanged []string

	// Trace contains transparency data
	Trace *transparency.Trace

	// Error if fix failed
	Error error
}

// FixFinding applies a fix for a single finding using RLM.
func (r *Runner) FixFinding(ctx context.Context, finding *Finding, prompt string) (*FixResult, error) {
	result := &FixResult{}

	if r.rlmRunner == nil {
		result.Error = fmt.Errorf("rlm not configured - cannot apply fixes")
		return result, nil
	}

	// Build system prompt for fixing
	systemPrompt := buildFixSystemPrompt()

	// Run with full tool access for fixing
	allowedTools := []string{"read", "glob", "grep", "bash", "write"}

	rlmResult, err := r.rlmRunner.Run(ctx, systemPrompt, prompt, allowedTools)
	if err != nil {
		result.Error = err
		return result, nil
	}

	result.Summary = rlmResult.Response
	result.Trace = rlmResult.Trace

	// Extract changed files from tool calls
	for _, tc := range rlmResult.ToolCalls {
		if tc.Name == "write" {
			// Parse the file path from arguments
			// This is a simplification - actual parsing would depend on argument format
			result.FilesChanged = append(result.FilesChanged, tc.Arguments)
		}
	}

	return result, nil
}

// buildFixSystemPrompt creates the system prompt for fixing findings.
func buildFixSystemPrompt() string {
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
