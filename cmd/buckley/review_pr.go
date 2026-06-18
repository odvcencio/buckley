package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/terminal"
)

type reviewPRCommandOptions struct {
	verbose    bool
	showCost   bool
	model      string
	timeout    time.Duration
	outputFile string
	prRef      string
}

func parseReviewPRCommandOptions(args []string) (reviewPRCommandOptions, error) {
	fs := flag.NewFlagSet("review-pr", flag.ContinueOnError)
	verbose := fs.Bool("verbose", false, "show full context and reasoning")
	showCost := fs.Bool("cost", true, "show token/cost breakdown")
	modelFlag := fs.String("model", "", "model to use (default: BUCKLEY_MODEL_REVIEW, models.review, or execution model)")
	timeout := fs.Duration("timeout", 5*time.Minute, "timeout for model request")
	outputFile := fs.String("output", "", "write review to file instead of stdout")

	if err := fs.Parse(args); err != nil {
		return reviewPRCommandOptions{}, err
	}

	prArg := ""
	if fs.NArg() > 0 {
		prArg = fs.Arg(0)
	}
	if prArg == "" {
		return reviewPRCommandOptions{}, reviewPRUsageError()
	}
	return reviewPRCommandOptions{
		verbose:    *verbose,
		showCost:   *showCost,
		model:      *modelFlag,
		timeout:    *timeout,
		outputFile: *outputFile,
		prRef:      prArg,
	}, nil
}

func reviewPRUsageError() error {
	return fmt.Errorf("usage: buckley review-pr <pr-number-or-url>\n\nExamples:\n  buckley review-pr 123\n  buckley review-pr https://github.com/owner/repo/pull/123")
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

	runtime, err := newReviewCommandRuntime(cfg, mgr)
	if err != nil {
		return fmt.Errorf("no model configured (set BUCKLEY_MODEL_REVIEW or configure models.review)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	if !quietMode {
		termOut.Dim("Using model: %s", runtime.modelID)
		termOut.Dim("Reviewing PR: %s", opts.prRef)
	}

	result, prInfo, err := runPRReview(ctx, opts.prRef, runtime.framework)
	if err != nil {
		return err
	}

	if opts.verbose && result.contextAudit != nil {
		printReviewContextAudit(result.contextAudit)
	}

	if result.reviewText == "" {
		return fmt.Errorf("no review generated")
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
	spinner := terminal.NewSpinner("Fetching PR details...")
	spinner.Start()

	prCtx, audit, err := commands.AssemblePRContext(prRef)
	if err != nil {
		spinner.StopWithError(err.Error())
		return nil, nil, fmt.Errorf("assemble PR context: %w", err)
	}

	userPrompt := commands.BuildPRPrompt(prCtx)
	fwResult, runErr := framework.RunRLM(ctx, commands.ReviewPRDef{}, oneshot.RLMRunOpts{
		UserPrompt: userPrompt,
		Audit:      audit,
	})

	if runErr != nil {
		spinner.StopWithError(runErr.Error())
		return nil, prCtx.PR, fmt.Errorf("review failed: %w", runErr)
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
