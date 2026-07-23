package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/terminal"
)

type reviewPRCommandOptions struct {
	verbose     bool
	showCost    bool
	model       string
	criticModel string
	timeout     time.Duration
	outputFile  string
	prRef       string
	budgetUSD   float64
	maxTurns    int
	maxDiff     int
	maxRetries  int
}

func parseReviewPRCommandOptions(args []string) (reviewPRCommandOptions, error) {
	fs := flag.NewFlagSet("review-pr", flag.ContinueOnError)
	verbose := fs.Bool("verbose", false, "show full context and reasoning")
	showCost := fs.Bool("cost", true, "show token/cost breakdown")
	modelFlag := fs.String("model", "", "model to use (default: BUCKLEY_MODEL_REVIEW or buckbot.model)")
	criticModel := fs.String("critic-model", "", "opt-in approval critic model for large or business-critical reviews")
	timeout := fs.Duration("timeout", 5*time.Minute, "timeout for model request")
	outputFile := fs.String("output", "", "write review to file instead of stdout")
	budgetUSD := fs.Float64("budget", 0, "maximum model spend in USD (0 = Buckbot default)")
	maxTurns := fs.Int("max-turns", 0, "hard model turn limit per review pass (0 = adaptive)")
	maxDiff := fs.Int("max-diff-bytes", 0, "maximum prioritized diff bytes (0 = Buckbot default)")
	maxRetries := fs.Int("max-validation-attempts", 0, "maximum schema-validation attempts (0 = Buckbot default)")

	if err := fs.Parse(interspersedReviewPRArgs(args)); err != nil {
		return reviewPRCommandOptions{}, err
	}

	if fs.NArg() != 1 || fs.Arg(0) == "" {
		return reviewPRCommandOptions{}, reviewPRUsageError()
	}
	return reviewPRCommandOptions{
		verbose:     *verbose,
		showCost:    *showCost,
		model:       *modelFlag,
		criticModel: *criticModel,
		timeout:     *timeout,
		outputFile:  *outputFile,
		prRef:       fs.Arg(0),
		budgetUSD:   *budgetUSD,
		maxTurns:    *maxTurns,
		maxDiff:     *maxDiff,
		maxRetries:  *maxRetries,
	}, nil
}

// interspersedReviewPRArgs lets callers use the natural
// `review-pr <ref> -model ...` form without silently treating every trailing
// flag as an ignored positional argument. The standard flag package stops at
// the first positional argument, so move the one PR reference behind the
// command's flags before parsing.
func interspersedReviewPRArgs(args []string) []string {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			positionals = append(positionals, arg)
			continue
		}

		flags = append(flags, arg)
		name, hasInlineValue := reviewPRFlagName(arg)
		if hasInlineValue || !reviewPRFlagTakesValue(name) || i+1 >= len(args) {
			continue
		}
		flags = append(flags, args[i+1])
		i++
	}
	return append(flags, positionals...)
}

func reviewPRFlagName(arg string) (string, bool) {
	name := strings.TrimLeft(arg, "-")
	if before, _, ok := strings.Cut(name, "="); ok {
		return before, true
	}
	return name, false
}

func reviewPRFlagTakesValue(name string) bool {
	switch name {
	case "model", "critic-model", "timeout", "output", "budget", "max-turns", "max-diff-bytes", "max-validation-attempts":
		return true
	default:
		return false
	}
}

func reviewPRUsageError() error {
	return fmt.Errorf("usage: buckley review-pr <pr-number-or-url> [flags]\n\nExamples:\n  buckley review-pr 123\n  buckley review-pr 123 -model codex/gpt-5.6-terra-high\n  buckley review-pr https://github.com/owner/repo/pull/123")
}

// runReviewPRCommand reviews a remote PR using gh CLI integration.
func runReviewPRCommand(args []string) error {
	opts, err := parseReviewPRCommandOptions(args)
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
	if strings.TrimSpace(opts.criticModel) != "" {
		cfg.Buckbot.CriticModel = strings.TrimSpace(opts.criticModel)
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
		termOut.Dim("Reviewing PR: %s", opts.prRef)
	}

	policy := runtime.policy.withOverrides(automatedReviewOptions{
		maxIterations: opts.maxTurns,
		maxRetries:    opts.maxRetries,
		maxDiffBytes:  opts.maxDiff,
		maxCostUSD:    opts.budgetUSD,
	})
	result, prInfo, reviewErr := runPRReviewWithOptions(ctx, opts.prRef, runtime.framework, policy)

	if opts.verbose && result != nil && result.contextAudit != nil {
		printReviewContextAudit(result.contextAudit)
	}
	if reviewErr != nil {
		if result == nil || !result.incomplete || strings.TrimSpace(result.reviewText) == "" {
			return reviewErr
		}
		if err := writePRReviewOutput(opts.outputFile, result.reviewText, prInfo); err != nil {
			return fmt.Errorf("%w; also failed to write salvaged review: %v", reviewErr, err)
		}
		if opts.showCost && result.trace != nil {
			printReviewCost(result.trace, runtime.ledger)
		}
		return fmt.Errorf("%w; incomplete review salvaged%s", reviewErr, reviewSalvageDestination(opts.outputFile))
	}

	if result.reviewText == "" {
		return fmt.Errorf("no review generated")
	}
	if !quietMode {
		printReviewAttemptCounts(result)
	}

	if err := writePRReviewOutput(opts.outputFile, result.reviewText, prInfo); err != nil {
		return err
	}

	if opts.showCost && result.trace != nil {
		printReviewCost(result.trace, runtime.ledger)
	}

	return nil
}

func runPRReview(ctx context.Context, prRef string, framework *oneshot.Framework) (*reviewCommandResult, *commands.PRInfo, error) {
	return runPRReviewWithIterationLimit(ctx, prRef, framework, 0)
}

func runPRReviewWithIterationLimit(ctx context.Context, prRef string, framework *oneshot.Framework, maxIterations int) (*reviewCommandResult, *commands.PRInfo, error) {
	return runPRReviewWithOptions(ctx, prRef, framework, automatedReviewOptions{maxIterations: maxIterations})
}

type automatedReviewOptions struct {
	maxIterations    int
	maxRetries       int
	maxDiffBytes     int
	maxCostUSD       float64
	criticReserveUSD float64
	approvalCritic   bool
}

func defaultAutomatedReviewOptions(cfg *config.Config) automatedReviewOptions {
	if cfg == nil {
		return automatedReviewOptions{}
	}
	opts := automatedReviewOptions{
		maxIterations: cfg.Buckbot.MaxReviewIterations,
		maxRetries:    cfg.Buckbot.MaxValidationAttempts,
		maxDiffBytes:  cfg.Buckbot.MaxDiffBytes,
		maxCostUSD:    cfg.Buckbot.PerReviewBudgetUSD,
	}
	if strings.TrimSpace(cfg.Buckbot.CriticModel) != "" {
		opts.criticReserveUSD = cfg.Buckbot.PerReviewBudgetUSD * 0.12
		opts.approvalCritic = true
	}
	return opts
}

func (defaults automatedReviewOptions) withOverrides(overrides automatedReviewOptions) automatedReviewOptions {
	if overrides.maxIterations > 0 {
		defaults.maxIterations = overrides.maxIterations
	}
	if overrides.maxRetries > 0 {
		defaults.maxRetries = overrides.maxRetries
	}
	if overrides.maxDiffBytes > 0 {
		defaults.maxDiffBytes = overrides.maxDiffBytes
	}
	if overrides.maxCostUSD > 0 {
		defaults.maxCostUSD = overrides.maxCostUSD
		if defaults.criticReserveUSD > 0 {
			defaults.criticReserveUSD = overrides.maxCostUSD * 0.12
		}
	}
	return defaults
}

func runPRReviewWithOptions(ctx context.Context, prRef string, framework *oneshot.Framework, opts automatedReviewOptions) (*reviewCommandResult, *commands.PRInfo, error) {
	spinner := terminal.NewSpinner("Fetching PR details...")
	spinner.Start()

	contextOpts := commands.DefaultPRContextOptions()
	if opts.maxDiffBytes > 0 {
		contextOpts.MaxDiffBytes = opts.maxDiffBytes
	}
	prCtx, audit, err := commands.AssemblePRContextWithOptions(prRef, contextOpts)
	if err != nil {
		spinner.StopWithError(err.Error())
		return nil, nil, fmt.Errorf("assemble PR context: %w", err)
	}
	spinner.SetMessage("Running model review...")

	userPrompt := commands.BuildPRPrompt(prCtx)
	reviewDef := commands.ReviewPRDef{
		ChangedFiles:                prCtx.Files,
		ContextIncomplete:           prCtx.HasIncompleteContext(),
		CIStatus:                    prCtx.PR.CIStatus,
		CIProvenance:                prCtx.CIProvenance,
		RequiresFeedbackDisposition: prCtx.HasReviewFeedback(),
		RequiredFeedbackIDs:         prCtx.RequiredFeedbackIDs(),
		MaxIterations:               opts.maxIterations,
		ApprovalCritic:              opts.approvalCritic,
	}
	fwResult, runErr := framework.RunRLM(ctx, reviewDef, oneshot.RLMRunOpts{
		UserPrompt:               userPrompt,
		Audit:                    audit,
		MaxRetries:               opts.maxRetries,
		MaxCostUSD:               opts.maxCostUSD,
		ApprovalCriticReserveUSD: opts.criticReserveUSD,
		SnapshotPolicy: model.ReviewSnapshotPolicy{
			Mode:           model.ReviewSnapshotHead,
			ExpectedCommit: prCtx.PR.HeadSHA,
		},
	})

	if runErr != nil {
		spinner.StopWithError(runErr.Error())
		partial := reviewResultFromRLM(fwResult, audit)
		if strings.TrimSpace(partial.reviewText) != "" {
			return partial, prCtx.PR, fmt.Errorf("review failed: %w", runErr)
		}
		return nil, prCtx.PR, fmt.Errorf("review failed: %w", runErr)
	}
	if err := commands.RevalidatePRContext(prCtx); err != nil {
		spinner.StopWithError(err.Error())
		return nil, prCtx.PR, fmt.Errorf("review target changed: %w", err)
	}

	spinner.StopWithSuccess("PR review complete")
	return reviewResultFromRLM(fwResult, audit), prCtx.PR, nil
}

func writePRReviewOutput(outputFile, reviewText string, prInfo *commands.PRInfo) error {
	if outputFile != "" {
		if err := os.WriteFile(outputFile, []byte(reviewText), 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		termOut.Success("Review written to %s", outputFile)
		return nil
	}
	printPRReview(reviewText, prInfo)
	return nil
}

func printPRReview(reviewText string, prInfo *commands.PRInfo) {
	termOut.Newline()
	if prInfo != nil {
		termOut.Header(fmt.Sprintf("PR #%d: %s", prInfo.Number, prInfo.Title))
		termOut.Dim("By @%s · %s · CI: %s", prInfo.Author, prInfo.State, prInfo.CIStatus)
	} else {
		termOut.Header("PR REVIEW")
	}
	termOut.Newline()
	fmt.Println(reviewText)
}
