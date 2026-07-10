package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/rules"
	"m31labs.dev/buckley/pkg/terminal"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/transparency"
)

type reviewCommandOptions struct {
	projectMode     bool
	scope           string
	baseBranch      string
	includeUnstaged bool
	untrackedPaths  []string
	verbose         bool
	showCost        bool
	model           string
	timeout         time.Duration
	outputFile      string
	interactive     bool
}

type reviewCommandRuntime struct {
	mgr             *model.Manager
	registry        *tool.Registry
	ledger          *transparency.CostLedger
	framework       *oneshot.Framework
	modelID         string
	reasoningEffort string
}

type reviewCommandResult struct {
	reviewText     string
	parsed         *commands.ParsedReview
	trace          *transparency.Trace
	contextAudit   *transparency.ContextAudit
	attempts       int
	primary        int
	criticAttempts int
}

func parseReviewCommandOptions(args []string) (reviewCommandOptions, error) {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	projectMode := fs.Bool("project", false, "review the entire project instead of branch diff")
	scope := fs.String("scope", commands.ReviewScopeWorktree, "review scope: worktree, branch, or changes")
	baseBranch := fs.String("base", "", "base branch to compare against (default: auto-detect main/master)")
	includeUnstaged := fs.Bool("unstaged", true, "include unstaged changes in review")
	var untrackedPaths stringSliceFlag
	fs.Var(&untrackedPaths, "include-untracked", "include one untracked repository-relative text path in model input (repeatable; review for secrets)")
	verbose := fs.Bool("verbose", false, "show full context and reasoning")
	showCost := fs.Bool("cost", true, "show token/cost breakdown")
	modelFlag := fs.String("model", "", "model to use (default: BUCKLEY_MODEL_REVIEW, models.review, or execution model)")
	timeout := fs.Duration("timeout", 5*time.Minute, "timeout for model request")
	outputFile := fs.String("output", "", "write review to file instead of stdout")
	interactive := fs.Bool("interactive", true, "show interactive menu to fix findings")
	noInteractive := fs.Bool("no-interactive", false, "disable interactive mode")

	if err := fs.Parse(args); err != nil {
		return reviewCommandOptions{}, err
	}

	opts := reviewCommandOptions{
		projectMode:     *projectMode,
		scope:           *scope,
		baseBranch:      *baseBranch,
		includeUnstaged: *includeUnstaged,
		untrackedPaths:  append([]string(nil), untrackedPaths...),
		verbose:         *verbose,
		showCost:        *showCost,
		model:           *modelFlag,
		timeout:         *timeout,
		outputFile:      *outputFile,
		interactive:     *interactive,
	}
	if *noInteractive {
		opts.interactive = false
	}
	if len(opts.untrackedPaths) > 0 && (opts.projectMode || normalizeReviewCommandScope(opts.scope) != commands.ReviewScopeWorktree || !opts.includeUnstaged) {
		return reviewCommandOptions{}, fmt.Errorf("--include-untracked requires --scope worktree, --unstaged=true, and non-project review")
	}
	return opts, nil
}

// runReviewCommand performs code review on a branch or project.
func runReviewCommand(args []string) error {
	opts, err := parseReviewCommandOptions(args)
	if err != nil {
		return err
	}

	restoreModelOverride := applyCommandModelOverride(opts.model)
	defer restoreModelOverride()

	cfg, mgr, store, err := initDependenciesFn()
	if store != nil {
		defer store.Close()
	}
	if err != nil {
		return fmt.Errorf("init dependencies: %w", err)
	}

	runtime, err := newReviewCommandRuntime(cfg, mgr)
	if err != nil {
		return fmt.Errorf("no model configured (set BUCKLEY_MODEL_REVIEW or configure models.review)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	if !quietMode {
		termOut.Dim("Using model: %s", runtime.modelID)
		if runtime.reasoningEffort != "" {
			termOut.Dim("Reasoning effort: %s", runtime.reasoningEffort)
		}
	}

	result, err := runReview(ctx, opts, runtime.framework)
	if err != nil {
		return err
	}

	if opts.verbose && result.contextAudit != nil {
		printReviewContextAudit(result.contextAudit)
	}

	if result.reviewText == "" {
		return fmt.Errorf("no review generated")
	}
	if !quietMode {
		printReviewAttemptCounts(result)
	}

	if err := writeReviewOutput(opts.outputFile, result.reviewText); err != nil {
		return err
	}

	if opts.showCost && result.trace != nil {
		printReviewCost(result.trace, runtime.ledger)
	}

	if opts.interactive && opts.outputFile == "" {
		parsed := result.parsed
		if parsed == nil {
			parsed = commands.ParseReview(result.reviewText)
		}
		if parsed != nil && len(parsed.Findings) > 0 {
			runReviewMenu(ctx, parsed, runtime.mgr, runtime.registry, runtime.modelID, runtime.reasoningEffort, runtime.ledger, opts.timeout)
		}
	}

	return nil
}

func newReviewCommandRuntime(cfg *config.Config, mgr *model.Manager) (*reviewCommandRuntime, error) {
	modelID := resolveReviewModel(cfg)
	if modelID == "" {
		return nil, fmt.Errorf("no review model configured")
	}
	reasoningEffort := model.ResolveReasoningEffort(cfg, mgr, nil, modelID, "review")
	arbEngine, err := rules.NewDefaultEngine()
	if err != nil {
		return nil, fmt.Errorf("initialize rules engine: %w", err)
	}

	ledger := transparency.NewCostLedger()
	registry := tool.NewRegistry()
	if cwd, err := os.Getwd(); err == nil {
		registry.ConfigureContainers(cfg, cwd)
	}

	rlmRunner := oneshot.NewRLMRunner(oneshot.RLMRunnerConfig{
		Models:          mgr,
		Registry:        registry,
		Ledger:          ledger,
		ModelID:         modelID,
		ReasoningEffort: reasoningEffort,
	})

	return &reviewCommandRuntime{
		mgr:             mgr,
		registry:        registry,
		ledger:          ledger,
		framework:       oneshot.NewFramework(nil, arbEngine).WithRLMRunner(rlmRunner),
		modelID:         modelID,
		reasoningEffort: reasoningEffort,
	}, nil
}

func resolveReviewModel(cfg *config.Config) string {
	modelID := strings.TrimSpace(modelOverrideFlag)
	if modelID != "" {
		modelID = normalizeModelIDWithReasoning(cfg, modelID)
	}
	if modelID == "" {
		modelID = normalizeModelIDWithReasoning(cfg, os.Getenv("BUCKLEY_MODEL_REVIEW"))
	}
	if modelID == "" && cfg != nil {
		modelID = cfg.Models.Review
	}
	if modelID == "" && cfg != nil {
		modelID = cfg.Models.Execution
	}
	return modelID
}

func runReview(ctx context.Context, opts reviewCommandOptions, framework *oneshot.Framework) (*reviewCommandResult, error) {
	if opts.projectMode {
		return runProjectReview(ctx, framework)
	}
	return runBranchReview(ctx, opts, framework)
}

func runProjectReview(ctx context.Context, framework *oneshot.Framework) (*reviewCommandResult, error) {
	spinner := terminal.NewSpinner("Analyzing project...")
	spinner.Start()
	policy := model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotTrackedWorktree}
	snapshot, err := model.CaptureReviewSnapshot(ctx, "", policy)
	if err != nil {
		spinner.StopWithError(err.Error())
		return nil, fmt.Errorf("capture project review snapshot: %w", err)
	}

	opts := commands.DefaultProjectContextOptions()
	projectCtx, audit, err := commands.AssembleProjectContext(opts)
	if err != nil {
		spinner.StopWithError(err.Error())
		return nil, fmt.Errorf("assemble context: %w", err)
	}
	if snapshot == nil || snapshot.Commit() != projectCtx.HeadCommit {
		err := fmt.Errorf("project review context does not match the captured immutable HEAD")
		spinner.StopWithError(err.Error())
		return nil, err
	}
	if err := verifyReviewSnapshotStable(ctx, snapshot, policy); err != nil {
		spinner.StopWithError(err.Error())
		return nil, err
	}

	userPrompt := commands.BuildProjectPrompt(projectCtx)
	fwResult, runErr := framework.RunRLM(ctx, commands.ReviewProjectDef{}, oneshot.RLMRunOpts{
		UserPrompt:     userPrompt,
		Audit:          audit,
		SnapshotPolicy: policy,
		ReviewSnapshot: snapshot,
	})
	if runErr != nil {
		spinner.StopWithError(runErr.Error())
		return nil, fmt.Errorf("review failed: %w", runErr)
	}

	spinner.StopWithSuccess("Project review complete")
	return reviewResultFromRLM(fwResult, audit), nil
}

func runBranchReview(ctx context.Context, opts reviewCommandOptions, framework *oneshot.Framework) (*reviewCommandResult, error) {
	reviewScope := normalizeReviewCommandScope(opts.scope)
	spinner := terminal.NewSpinner(fmt.Sprintf("Analyzing %s changes...", reviewScope))
	spinner.Start()
	policy := branchReviewSnapshotPolicy(reviewScope, opts.includeUnstaged, opts.untrackedPaths)
	snapshot, err := model.CaptureReviewSnapshot(ctx, "", policy)
	if err != nil {
		spinner.StopWithError(err.Error())
		return nil, fmt.Errorf("capture %s review snapshot: %w", reviewScope, err)
	}

	contextOpts := commands.DefaultBranchContextOptions()
	contextOpts.BaseBranch = opts.baseBranch
	contextOpts.IncludeUnstaged = opts.includeUnstaged
	contextOpts.UntrackedPaths = append([]string(nil), opts.untrackedPaths...)
	contextOpts.Scope = reviewScope
	if snapshot != nil && policy.Mode == model.ReviewSnapshotWorktree {
		contextOpts.CapturedUntracked = snapshot.UntrackedFiles()
	}

	branchCtx, audit, err := commands.AssembleBranchContext(contextOpts)
	if err != nil {
		spinner.StopWithError(err.Error())
		return nil, fmt.Errorf("assemble context: %w", err)
	}
	if snapshot == nil || snapshot.Commit() != branchCtx.HeadCommit {
		err := fmt.Errorf("branch review context does not match the captured immutable HEAD")
		spinner.StopWithError(err.Error())
		return nil, err
	}
	if err := verifyReviewSnapshotStable(ctx, snapshot, policy); err != nil {
		spinner.StopWithError(err.Error())
		return nil, err
	}
	if err := commands.RevalidateBranchContext(branchCtx); err != nil {
		spinner.StopWithError(err.Error())
		return nil, fmt.Errorf("revalidate branch review context: %w", err)
	}

	userPrompt := commands.BuildBranchPrompt(branchCtx)
	reviewDef := commands.ReviewBranchDef{
		ChangedFiles:      reviewChangedFilePaths(branchCtx.Files),
		ContextIncomplete: branchCtx.DiffTruncated || branchCtx.UnstagedTruncated || branchCtx.ContextIncomplete,
	}
	fwResult, runErr := framework.RunRLM(ctx, reviewDef, oneshot.RLMRunOpts{
		UserPrompt:     userPrompt,
		Audit:          audit,
		SnapshotPolicy: policy,
		ReviewSnapshot: snapshot,
	})
	if runErr != nil {
		spinner.StopWithError(runErr.Error())
		return nil, fmt.Errorf("review failed: %w", runErr)
	}

	spinner.StopWithSuccess(fmt.Sprintf("%s review complete", titleReviewScope(reviewScope)))
	return reviewResultFromRLM(fwResult, audit), nil
}

func branchReviewSnapshotPolicy(scope string, includeUnstaged bool, untrackedPaths []string) model.ReviewSnapshotPolicy {
	if scope == commands.ReviewScopeBranch {
		return model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotHead}
	}
	if includeUnstaged {
		if scope == commands.ReviewScopeWorktree && len(untrackedPaths) > 0 {
			return model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotWorktree, UntrackedPaths: append([]string(nil), untrackedPaths...)}
		}
		return model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotTrackedWorktree}
	}
	return model.ReviewSnapshotPolicy{Mode: model.ReviewSnapshotIndex}
}

func verifyReviewSnapshotStable(ctx context.Context, captured *model.ReviewSnapshot, policy model.ReviewSnapshotPolicy) error {
	current, err := model.CaptureReviewSnapshot(ctx, "", policy)
	if err != nil {
		return fmt.Errorf("revalidate review snapshot: %w", err)
	}
	if captured == nil || current == nil || captured.ID() != current.ID() {
		return fmt.Errorf("review Git state changed while context was assembled; retry against one stable snapshot")
	}
	return nil
}

func reviewChangedFilePaths(files []commands.FileChange) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		if path := strings.TrimSpace(file.Path); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func reviewResultFromRLM(fwResult *oneshot.RunResult, audit *transparency.ContextAudit) *reviewCommandResult {
	result := &reviewCommandResult{contextAudit: audit}
	if fwResult == nil {
		return result
	}
	if rlmResult, ok := fwResult.Value.(*commands.ReviewRLMResult); ok {
		result.reviewText = rlmResult.Review
		result.parsed = rlmResult.Parsed
	}
	result.trace = fwResult.Trace
	result.attempts = fwResult.Attempts
	result.primary = fwResult.PrimaryAttempts
	result.criticAttempts = fwResult.CriticAttempts
	return result
}

func printReviewAttemptCounts(result *reviewCommandResult) {
	if result == nil || result.attempts == 0 {
		return
	}
	termOut.Dim(
		"Review attempts: %d primary · %d approval critic · %d total",
		result.primary,
		result.criticAttempts,
		result.attempts,
	)
}

func writeReviewOutput(outputFile, reviewText string) error {
	if outputFile == "" {
		printReview(reviewText)
		return nil
	}
	if err := os.WriteFile(outputFile, []byte(reviewText), 0o644); err != nil {
		return fmt.Errorf("failed to write output file: %w", err)
	}
	termOut.Success("Review written to %s", outputFile)
	return nil
}

func normalizeReviewCommandScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", commands.ReviewScopeWorktree:
		return commands.ReviewScopeWorktree
	case commands.ReviewScopeBranch, "commits":
		return commands.ReviewScopeBranch
	case commands.ReviewScopeChanges, "change", "local", "working-tree":
		return commands.ReviewScopeChanges
	default:
		return commands.ReviewScopeWorktree
	}
}

func titleReviewScope(scope string) string {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return "Worktree"
	}
	return strings.ToUpper(scope[:1]) + scope[1:]
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

	summary := ledger.Summary()
	tokens := summary.SessionTokens
	var tokensLine string
	if tokens.Reasoning > 0 {
		tokensLine = fmt.Sprintf("Tokens: %d in · %d out · %d reasoning = %d total",
			tokens.Input, tokens.Output, tokens.Reasoning, tokens.Total())
	} else {
		tokensLine = fmt.Sprintf("Tokens: %d in · %d out = %d total",
			tokens.Input, tokens.Output, tokens.Total())
	}

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
