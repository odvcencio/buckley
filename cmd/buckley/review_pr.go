package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/oneshot/review"
	"github.com/odvcencio/buckley/pkg/terminal"
	"github.com/odvcencio/buckley/pkg/tool"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// runReviewPRCommand reviews a remote PR using gh CLI integration.
func runReviewPRCommand(args []string) error {
	fs := flag.NewFlagSet("review-pr", flag.ContinueOnError)
	verbose := fs.Bool("verbose", false, "show full context and reasoning")
	showCost := fs.Bool("cost", true, "show token/cost breakdown")
	modelFlag := fs.String("model", "", "model to use (default: BUCKLEY_MODEL_REVIEW or execution model)")
	timeout := fs.Duration("timeout", 5*time.Minute, "timeout for model request")
	outputFile := fs.String("output", "", "write review to file instead of stdout")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Get PR identifier (number or URL)
	prArg := ""
	if fs.NArg() > 0 {
		prArg = fs.Arg(0)
	}
	if prArg == "" {
		return fmt.Errorf("usage: buckley review-pr <pr-number-or-url>\n\nExamples:\n  buckley review-pr 123\n  buckley review-pr https://github.com/owner/repo/pull/123")
	}

	// Initialize dependencies
	cfg, mgr, store, err := initDependenciesFn()
	if store != nil {
		defer store.Close()
	}
	if err != nil {
		return fmt.Errorf("init dependencies: %w", err)
	}

	// Determine model
	modelID := strings.TrimSpace(*modelFlag)
	if modelID == "" {
		modelID = strings.TrimSpace(os.Getenv("BUCKLEY_MODEL_REVIEW"))
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

	// Create cost ledger
	ledger := transparency.NewCostLedger()

	// Create tool registry for full RLM access
	registry := tool.NewRegistry()
	if cwd, err := os.Getwd(); err == nil {
		registry.ConfigureContainers(cfg, cwd)
	}
	registerMCPTools(cfg, registry)

	// Create runner with RLM for full tool access
	runner := review.NewRunner(review.RunnerConfig{
		Models:   mgr,
		Registry: registry,
		ModelID:  modelID,
		Ledger:   ledger,
	})

	// Run with timeout
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// Show what we're doing
	if !quietMode {
		termOut.Dim("Using model: %s", modelID)
		termOut.Dim("Reviewing PR: %s", prArg)
	}

	spinner := terminal.NewSpinner("Fetching PR details...")
	spinner.Start()

	result, runErr := runner.ReviewPR(ctx, prArg)

	if runErr != nil {
		spinner.StopWithError(runErr.Error())
		return fmt.Errorf("review failed: %w", runErr)
	} else if result.Error != nil {
		spinner.StopWithError(result.Error.Error())
	} else {
		spinner.StopWithSuccess("PR review complete")
	}

	// Show context audit if verbose
	if *verbose && result.ContextAudit != nil {
		printReviewContextAudit(result.ContextAudit)
	}

	// Check for errors
	if result.Error != nil {
		printReviewError(result.Error, result.Trace)
		return result.Error
	}

	// Output review
	if result.Review == "" {
		return fmt.Errorf("no review generated")
	}

	// Write to file or stdout
	if *outputFile != "" {
		if err := os.WriteFile(*outputFile, []byte(result.Review), 0o644); err != nil {
			return fmt.Errorf("failed to write output file: %w", err)
		}
		termOut.Success("Review written to %s", *outputFile)
	} else {
		printPRReview(result.Review, result.PRInfo)
	}

	// Show cost
	if *showCost && result.Trace != nil {
		printReviewCost(result.Trace, ledger)
	}

	return nil
}

func printPRReview(reviewText string, prInfo *review.PRInfo) {
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
