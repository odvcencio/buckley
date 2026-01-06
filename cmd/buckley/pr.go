package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/config"
	"github.com/odvcencio/buckley/pkg/oneshot"
	prgen "github.com/odvcencio/buckley/pkg/oneshot/pr"
	oneshotrlm "github.com/odvcencio/buckley/pkg/oneshot/rlm"
	"github.com/odvcencio/buckley/pkg/terminal"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// runPRCommand generates a structured PR via tool-use.
func runPRCommand(args []string) error {
	fs := flag.NewFlagSet("pr", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print the generated PR without creating it")
	yes := fs.Bool("yes", false, "skip confirmation prompts and create the PR")
	pushFlag := fs.Bool("push", true, "push current branch before creating PR")
	baseFlag := fs.String("base", "", "base branch (default: auto-detect main/master)")
	verbose := fs.Bool("verbose", false, "show model reasoning and full trace")
	showCost := fs.Bool("cost", true, "show token/cost breakdown")
	modelFlag := fs.String("model", "", "model to use (default: BUCKLEY_MODEL_PR or execution model)")
	timeout := fs.Duration("timeout", 2*time.Minute, "timeout for model request")

	if err := fs.Parse(args); err != nil {
		return err
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
		modelID = strings.TrimSpace(os.Getenv("BUCKLEY_MODEL_PR"))
	}
	if modelID == "" && cfg != nil {
		modelID = cfg.Models.Execution
	}
	if modelID == "" {
		return fmt.Errorf("no model configured (set BUCKLEY_MODEL_PR or configure models.execution)")
	}

	// Get model pricing for cost calculation
	pricing := transparency.ModelPricing{
		InputPerMillion:  3.0,
		OutputPerMillion: 15.0,
	}
	if mgr != nil {
		if info, err := mgr.GetModelInfo(modelID); err == nil {
			pricing.InputPerMillion = info.Pricing.Prompt
			pricing.OutputPerMillion = info.Pricing.Completion
		}
	}

	// Create cost ledger
	ledger := transparency.NewCostLedger()

	// Create invoker
	invoker := oneshot.NewInvoker(oneshot.InvokerConfig{
		Client:   mgr,
		Model:    modelID,
		Provider: "openrouter",
		Pricing:  pricing,
		Ledger:   ledger,
	})

	type prRunner interface {
		Run(ctx context.Context, opts prgen.ContextOptions) (*prgen.RunResult, error)
	}

	var runner prRunner
	if cfg != nil && cfg.OneshotMode() == config.ExecutionModeRLM {
		runner = oneshotrlm.NewPRRunner(oneshotrlm.PRRunnerConfig{
			Invoker: invoker,
			Ledger:  ledger,
		})
	} else {
		runner = prgen.NewRunner(prgen.RunnerConfig{
			Invoker: invoker,
			Ledger:  ledger,
		})
	}

	// Context options
	opts := prgen.DefaultContextOptions()
	if *baseFlag != "" {
		opts.BaseBranch = *baseFlag
	}

	// Run with timeout
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// Show what we're doing
	if !quietMode {
		termOut.Dim("Using model: %s", modelID)
	}

	// Execute the PR generation with spinner
	spinner := terminal.NewSpinner("Generating PR...")
	spinner.Start()

	result, err := runner.Run(ctx, opts)

	if err != nil {
		spinner.StopWithError(err.Error())
	} else if result.Error != nil {
		spinner.StopWithError(result.Error.Error())
	} else {
		spinner.StopWithSuccess("Generated PR")
	}

	if err != nil {
		return fmt.Errorf("PR generation failed: %w", err)
	}

	// Show context audit
	if *verbose && result.ContextAudit != nil {
		printContextAudit(result.ContextAudit)
	}

	// Show reasoning
	if *verbose && result.Trace != nil && result.Trace.Reasoning != "" {
		printReasoning(result.Trace.Reasoning)
	}

	// Check for errors
	if result.Error != nil {
		printError(result.Error, result.Trace)
		return result.Error
	}

	// Show the PR
	if result.PR == nil {
		return fmt.Errorf("no PR generated")
	}

	printPR(result.PR, result.Context)

	// Show cost
	if *showCost && result.Trace != nil {
		printCost(result.Trace, ledger)
	}

	// Dry run - just print and exit
	if *dryRun {
		return nil
	}

	// Auto-confirm or prompt
	if !*yes {
		if !stdinIsTerminalFn() {
			return fmt.Errorf("refusing to create PR without confirmation in non-interactive mode (use --dry-run or --yes)")
		}
		fmt.Print("\nCreate this PR? [y/N] ")
		var response string
		fmt.Scanln(&response)
		if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
			return fmt.Errorf("aborted")
		}
	}

	// Push if requested
	if *pushFlag {
		if err := pushCurrentBranch(); err != nil {
			return err
		}
	}

	// Create the PR
	if err := createPR(result.PR, result.Context); err != nil {
		return err
	}

	return nil
}

func printPR(pr *prgen.PRResult, ctx *prgen.Context) {
	termOut.Newline()
	termOut.Header(fmt.Sprintf("PULL REQUEST: %s → %s", ctx.Branch, ctx.BaseBranch))

	// Build content for the box
	var content strings.Builder
	content.WriteString(pr.Title)
	content.WriteString("\n\n")
	content.WriteString(pr.Summary)
	content.WriteString("\n\nChanges:")
	for _, change := range pr.Changes {
		content.WriteString("\n  - ")
		content.WriteString(change)
	}
	if pr.Breaking {
		content.WriteString("\n\n⚠️  BREAKING CHANGES")
	}

	termOut.Box("", content.String())

	// Also print the full body for piping
	fmt.Println("Title:", pr.Title)
	fmt.Println()
	fmt.Println(pr.FormatBody())
}

func pushCurrentBranch() error {
	branch, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return fmt.Errorf("failed to get current branch: %w", err)
	}
	branchName := strings.TrimSpace(string(branch))

	remote := os.Getenv("BUCKLEY_REMOTE_NAME")
	if remote == "" {
		remote = "origin"
	}

	spinner := terminal.NewSpinner(fmt.Sprintf("Pushing to %s/%s...", remote, branchName))
	spinner.Start()

	cmd := exec.Command("git", "push", "-u", remote, branchName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		spinner.StopWithError(fmt.Sprintf("push failed: %s", strings.TrimSpace(string(output))))
		return fmt.Errorf("git push failed: %w", err)
	}

	spinner.StopWithSuccess(fmt.Sprintf("Pushed to %s/%s", remote, branchName))
	return nil
}

func createPR(pr *prgen.PRResult, ctx *prgen.Context) error {
	// Check for gh CLI
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh CLI not found (install from https://cli.github.com)")
	}

	spinner := terminal.NewSpinner("Creating PR...")
	spinner.Start()

	// Create PR using gh
	body := pr.FormatBody()

	cmd := exec.Command("gh", "pr", "create",
		"--title", pr.Title,
		"--body", body,
		"--base", ctx.BaseBranch,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		spinner.StopWithError(fmt.Sprintf("failed: %s", strings.TrimSpace(string(output))))
		return fmt.Errorf("gh pr create failed: %w", err)
	}

	// Extract PR URL from output
	prURL := strings.TrimSpace(string(output))
	spinner.StopWithSuccess(fmt.Sprintf("PR created: %s", prURL))
	return nil
}
