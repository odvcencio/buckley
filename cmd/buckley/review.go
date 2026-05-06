package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/model"
	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/oneshot/commands"
	"github.com/odvcencio/buckley/pkg/terminal"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// runReviewCommand performs code review on a branch or project.
func runReviewCommand(args []string) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	projectMode := fs.Bool("project", false, "review the entire project instead of branch diff")
	baseBranch := fs.String("base", "", "base branch to compare against (default: auto-detect main/master)")
	includeUnstaged := fs.Bool("unstaged", true, "include unstaged changes in review")
	verbose := fs.Bool("verbose", false, "show full context and reasoning")
	showCost := fs.Bool("cost", true, "show token/cost breakdown")
	modelFlag := fs.String("model", "", "model to use (default: BUCKLEY_MODEL_REVIEW, models.review, or execution model)")
	timeout := fs.Duration("timeout", 5*time.Minute, "timeout for model request")
	outputFile := fs.String("output", "", "write review to file instead of stdout")
	interactive := fs.Bool("interactive", true, "show interactive menu to fix findings")
	noInteractive := fs.Bool("no-interactive", false, "disable interactive mode")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Handle -no-interactive flag
	if *noInteractive {
		*interactive = false
	}

	restoreModelOverride := applyCommandModelOverride(*modelFlag)
	defer restoreModelOverride()

	// Initialize dependencies
	cfg, mgr, store, err := initDependenciesFn()
	if store != nil {
		defer store.Close()
	}
	if err != nil {
		return fmt.Errorf("init dependencies: %w", err)
	}

	// Determine model
	modelID := strings.TrimSpace(modelOverrideFlag)
	if modelID == "" {
		modelID = normalizeModelIDWithReasoning(cfg, os.Getenv("BUCKLEY_MODEL_REVIEW"))
	}
	if modelID == "" && cfg != nil {
		modelID = cfg.Models.Review
	}
	if modelID == "" && cfg != nil {
		modelID = cfg.Models.Execution
	}
	if modelID == "" {
		return fmt.Errorf("no model configured (set BUCKLEY_MODEL_REVIEW or configure models.review)")
	}
	reasoningEffort := model.ResolveReasoningEffort(cfg, mgr, nil, modelID, "review")

	// Create cost ledger
	ledger := transparency.NewCostLedger()

	// Create tool registry for full RLM access
	registry := tool.NewRegistry()
	if cwd, err := os.Getwd(); err == nil {
		registry.ConfigureContainers(cfg, cwd)
	}

	// Create RLM runner for the framework
	rlmRunner := oneshot.NewRLMRunner(oneshot.RLMRunnerConfig{
		Models:          mgr,
		Registry:        registry,
		Ledger:          ledger,
		ModelID:         modelID,
		ReasoningEffort: reasoningEffort,
	})

	// Create unified framework with RLM support
	framework := oneshot.NewFramework(nil, nil).WithRLMRunner(rlmRunner)

	// Run with timeout
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// Show what we're doing
	if !quietMode {
		termOut.Dim("Using model: %s", modelID)
	}

	var reviewText string
	var parsed *commands.ParsedReview
	var trace *transparency.Trace
	var contextAudit *transparency.ContextAudit

	if *projectMode {
		// Review entire project
		spinner := terminal.NewSpinner("Analyzing project...")
		spinner.Start()

		opts := commands.DefaultProjectContextOptions()
		projectCtx, audit, err := commands.AssembleProjectContext(opts)
		if err != nil {
			spinner.StopWithError(err.Error())
			return fmt.Errorf("assemble context: %w", err)
		}
		contextAudit = audit

		userPrompt := commands.BuildProjectPrompt(projectCtx)
		fwResult, runErr := framework.RunRLM(ctx, commands.ReviewProjectDef{}, oneshot.RLMRunOpts{
			UserPrompt: userPrompt,
			Audit:      audit,
		})

		if runErr != nil {
			spinner.StopWithError(runErr.Error())
			return fmt.Errorf("review failed: %w", runErr)
		}

		spinner.StopWithSuccess("Project review complete")

		if rlmResult, ok := fwResult.Value.(*commands.ReviewRLMResult); ok {
			reviewText = rlmResult.Review
			parsed = rlmResult.Parsed
		}
		trace = fwResult.Trace
	} else {
		// Review branch against base
		spinner := terminal.NewSpinner("Analyzing branch changes...")
		spinner.Start()

		opts := commands.DefaultBranchContextOptions()
		opts.BaseBranch = *baseBranch
		opts.IncludeUnstaged = *includeUnstaged

		branchCtx, audit, err := commands.AssembleBranchContext(opts)
		if err != nil {
			spinner.StopWithError(err.Error())
			return fmt.Errorf("assemble context: %w", err)
		}
		contextAudit = audit

		userPrompt := commands.BuildBranchPrompt(branchCtx)
		fwResult, runErr := framework.RunRLM(ctx, commands.ReviewBranchDef{}, oneshot.RLMRunOpts{
			UserPrompt: userPrompt,
			Audit:      audit,
		})

		if runErr != nil {
			spinner.StopWithError(runErr.Error())
			return fmt.Errorf("review failed: %w", runErr)
		}

		spinner.StopWithSuccess("Branch review complete")

		if rlmResult, ok := fwResult.Value.(*commands.ReviewRLMResult); ok {
			reviewText = rlmResult.Review
			parsed = rlmResult.Parsed
		}
		trace = fwResult.Trace
	}

	// Show context audit if verbose
	if *verbose && contextAudit != nil {
		printReviewContextAudit(contextAudit)
	}

	// Output review
	if reviewText == "" {
		return fmt.Errorf("no review generated")
	}

	// Write to file or stdout
	if *outputFile != "" {
		if err := os.WriteFile(*outputFile, []byte(reviewText), 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		termOut.Success("Review written to %s", *outputFile)
	} else {
		printReview(reviewText)
	}

	// Show cost
	if *showCost && trace != nil {
		printReviewCost(trace, ledger)
	}

	// Interactive menu for fixing findings
	if *interactive && *outputFile == "" {
		if parsed == nil && reviewText != "" {
			parsed = commands.ParseReview(reviewText)
		}
		if parsed != nil && len(parsed.Findings) > 0 {
			runReviewMenu(ctx, parsed, mgr, registry, modelID, reasoningEffort, ledger, *timeout)
		}
	}

	return nil
}

func printReviewContextAudit(audit *transparency.ContextAudit) {
	termOut.Newline()
	termOut.Header(fmt.Sprintf("CONTEXT (%d tokens)", audit.TotalTokens()))

	var items []string
	for _, source := range audit.Sources() {
		pct := source.Percentage(audit.TotalTokens())
		truncated := ""
		if source.Truncated {
			truncated = " (truncated)"
		}
		items = append(items, fmt.Sprintf("%-40s %4d tok (%2.0f%%)%s", source.Name, source.Tokens, pct, truncated))
	}
	termOut.List(items)
}

func printReview(review string) {
	termOut.Newline()
	termOut.Header("CODE REVIEW")
	termOut.Newline()
	fmt.Println(review)
}

func printReviewCost(trace *transparency.Trace, ledger *transparency.CostLedger) {
	termOut.Newline()

	tokens := trace.Tokens
	var tokensLine string
	if tokens.Reasoning > 0 {
		tokensLine = fmt.Sprintf("Tokens: %d in · %d out · %d reasoning = %d total",
			tokens.Input, tokens.Output, tokens.Reasoning, tokens.Total())
	} else {
		tokensLine = fmt.Sprintf("Tokens: %d in · %d out = %d total",
			tokens.Input, tokens.Output, tokens.Total())
	}

	summary := ledger.Summary()
	costLine := fmt.Sprintf("Cost: $%.4f · Session: $%.4f", trace.Cost, summary.SessionCost)

	termOut.Dim("%s", tokensLine)
	termOut.Dim("%s", costLine)
}

func printReviewError(err error, trace *transparency.Trace) {
	termOut.Newline()
	termOut.Error("%s", err.Error())

	if trace != nil {
		termOut.Dim("Tokens used: %d · Cost: $%.4f (still charged)",
			trace.Tokens.Total(), trace.Cost)
	}
}

// runReviewMenu displays an interactive menu to fix findings.
func runReviewMenu(ctx context.Context, parsed *commands.ParsedReview, mgr *model.Manager, registry *tool.Registry, modelID, reasoningEffort string, ledger *transparency.CostLedger, timeout time.Duration) {
	// Print grade summary
	printGradeSummary(parsed)

	for {
		// Build menu items
		items := buildReviewMenuItems(parsed)
		if len(items) == 0 {
			termOut.Success("All findings resolved!")
			return
		}

		// Show menu
		choice := termOut.Menu("What would you like to do?", items)
		if choice == "" {
			termOut.Warn("Invalid choice")
			continue
		}

		switch choice {
		case "q":
			return
		case "a":
			// Fix all blockers
			blockers := parsed.BlockingFindings()
			if len(blockers) == 0 {
				termOut.Info("No blocking findings to fix")
				continue
			}
			for _, f := range blockers {
				if err := fixFinding(ctx, &f, mgr, registry, modelID, reasoningEffort, ledger, timeout); err != nil {
					termOut.Error("Failed to fix %s: %v", f.ID, err)
				}
			}
		case "m":
			// Fix all minor
			minor := parsed.MinorFindings()
			if len(minor) == 0 {
				termOut.Info("No minor findings to fix")
				continue
			}
			for _, f := range minor {
				if err := fixFinding(ctx, &f, mgr, registry, modelID, reasoningEffort, ledger, timeout); err != nil {
					termOut.Error("Failed to fix %s: %v", f.ID, err)
				}
			}
		default:
			// Fix specific finding by number
			if idx, err := strconv.Atoi(choice); err == nil && idx > 0 && idx <= len(parsed.Findings) {
				finding := &parsed.Findings[idx-1]
				if err := fixFinding(ctx, finding, mgr, registry, modelID, reasoningEffort, ledger, timeout); err != nil {
					termOut.Error("Failed to fix %s: %v", finding.ID, err)
				}
			}
		}
	}
}

// printGradeSummary shows the grade and finding counts.
func printGradeSummary(parsed *commands.ParsedReview) {
	termOut.Newline()
	termOut.Divider()

	// Grade with color
	gradeColor := ""
	switch parsed.Grade {
	case commands.GradeA:
		gradeColor = "✓"
	case commands.GradeB:
		gradeColor = "●"
	case commands.GradeC, commands.GradeD:
		gradeColor = "!"
	case commands.GradeF:
		gradeColor = "✗"
	}

	termOut.Bold("Review Grade: %s %s", gradeColor, parsed.Grade)
	termOut.Newline()

	critical := len(parsed.CriticalFindings())
	major := len(parsed.MajorFindings())
	minor := len(parsed.MinorFindings())

	if critical > 0 {
		termOut.Error("Critical: %d", critical)
	}
	if major > 0 {
		termOut.Warn("Major: %d", major)
	}
	if minor > 0 {
		termOut.Dim("Minor: %d", minor)
	}

	if critical == 0 && major == 0 && minor == 0 {
		termOut.Success("No findings!")
	}
}

// buildReviewMenuItems creates menu items from findings.
func buildReviewMenuItems(parsed *commands.ParsedReview) []terminal.MenuItem {
	var items []terminal.MenuItem

	// Add individual findings
	for i, f := range parsed.Findings {
		severity := string(f.Severity)
		desc := f.Title
		if f.File != "" {
			desc = fmt.Sprintf("%s (%s)", f.Title, f.File)
		}
		items = append(items, terminal.MenuItem{
			Key:         strconv.Itoa(i + 1),
			Label:       fmt.Sprintf("[%s] %s", severity, f.ID),
			Description: desc,
		})
	}

	// Add bulk actions
	blockers := parsed.BlockingFindings()
	if len(blockers) > 0 {
		items = append(items, terminal.MenuItem{
			Key:         "a",
			Label:       "Fix all blockers",
			Description: fmt.Sprintf("%d critical/major findings", len(blockers)),
		})
	}

	minor := parsed.MinorFindings()
	if len(minor) > 0 {
		items = append(items, terminal.MenuItem{
			Key:         "m",
			Label:       "Fix all minor",
			Description: fmt.Sprintf("%d minor findings", len(minor)),
		})
	}

	items = append(items, terminal.MenuItem{
		Key:   "q",
		Label: "Done",
	})

	return items
}

// fixFinding uses the framework's RLM execution to apply a fix for a finding.
func fixFinding(ctx context.Context, finding *commands.Finding, mgr *model.Manager, registry *tool.Registry, modelID, reasoningEffort string, ledger *transparency.CostLedger, timeout time.Duration) error {
	termOut.Newline()
	termOut.Header(fmt.Sprintf("Fixing %s: %s", finding.ID, finding.Title))

	spinner := terminal.NewSpinner("Applying fix...")
	spinner.Start()

	// Create context with timeout
	fixCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build fix prompt
	prompt := buildFixPrompt(finding)

	// Create RLM runner for fix
	rlmRunner := oneshot.NewRLMRunner(oneshot.RLMRunnerConfig{
		Models:          mgr,
		Registry:        registry,
		ModelID:         modelID,
		ReasoningEffort: reasoningEffort,
		Ledger:          ledger,
	})
	framework := oneshot.NewFramework(nil, nil).WithRLMRunner(rlmRunner)

	// Execute fix using framework's RLM path
	fwResult, err := framework.RunRLM(fixCtx, commands.FixFindingDef{}, oneshot.RLMRunOpts{
		UserPrompt: prompt,
	})
	if err != nil {
		spinner.StopWithError(err.Error())
		return err
	}

	spinner.StopWithSuccess("Fix applied")

	// Show what was done
	if fixResult, ok := fwResult.Value.(*commands.FixResult); ok && fixResult.Summary != "" {
		termOut.Newline()
		termOut.Info("Changes made:")
		termOut.Println(fixResult.Summary)
	}

	return nil
}

// buildFixPrompt creates the prompt for fixing a finding.
func buildFixPrompt(finding *commands.Finding) string {
	var sb strings.Builder

	sb.WriteString("Fix the following code review finding:\n\n")
	sb.WriteString(fmt.Sprintf("## %s: [%s] %s\n\n", finding.ID, finding.Severity, finding.Title))

	if finding.File != "" {
		sb.WriteString(fmt.Sprintf("**File**: %s", finding.File))
		if finding.Line > 0 {
			sb.WriteString(fmt.Sprintf(":%d", finding.Line))
		}
		sb.WriteString("\n\n")
	}

	if finding.Evidence != "" {
		sb.WriteString(fmt.Sprintf("**Evidence**: %s\n\n", finding.Evidence))
	}

	if finding.Impact != "" {
		sb.WriteString(fmt.Sprintf("**Impact**: %s\n\n", finding.Impact))
	}

	if finding.Fix != "" {
		sb.WriteString(fmt.Sprintf("**Required Fix**: %s\n\n", finding.Fix))
	}

	if finding.SuggestedFix != "" {
		sb.WriteString("**Suggested Code**:\n```\n")
		sb.WriteString(finding.SuggestedFix)
		sb.WriteString("\n```\n\n")
	}

	sb.WriteString("Instructions:\n")
	sb.WriteString("1. Read the file to understand the context\n")
	sb.WriteString("2. Apply the fix as described\n")
	sb.WriteString("3. Verify the fix compiles (run 'go build ./...')\n")
	sb.WriteString("4. Summarize what you changed\n")

	return sb.String()
}
