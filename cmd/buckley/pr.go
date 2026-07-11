package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/terminal"
	"m31labs.dev/buckley/pkg/transparency"
)

const (
	gitLocalTimeout = 30 * time.Second
	gitPushTimeout  = 60 * time.Second
	ghAPITimeout    = 120 * time.Second
)

type prCommandOptions struct {
	dryRun   bool
	yes      bool
	push     bool
	verbose  bool
	showCost bool
	base     string
	model    string
	backend  string
	timeout  time.Duration
}

type prCommandRuntime struct {
	backend   string
	modelID   string
	ledger    *transparency.CostLedger
	framework *oneshot.Framework
}

type prRunResult struct {
	PR           *commands.PRResult
	Trace        *transparency.Trace
	ContextAudit *transparency.ContextAudit
	Error        error
}

type prBaseRange struct {
	Branch    string
	Commit    string
	MergeBase string
	Range     string
}

func parsePRCommandOptions(args []string) (prCommandOptions, error) {
	fs := flag.NewFlagSet("pr", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print the generated PR without creating it")
	yes := fs.Bool("yes", false, "skip confirmation prompts and create the PR")
	pushFlag := fs.Bool("push", true, "push current branch before creating PR")
	baseFlag := fs.String("base", "", "base branch (default: auto-detect main/master)")
	verbose := fs.Bool("verbose", false, "show model reasoning and full trace")
	showCost := fs.Bool("cost", true, "show token/cost breakdown")
	modelFlag := fs.String("model", "", "model to use (default: BUCKLEY_MODEL_PR or models.utility.pr for API backend)")
	backendFlag := fs.String("backend", "", "backend to use: api, codex, or claude (default: BUCKLEY_PR_BACKEND, BUCKLEY_ONESHOT_BACKEND, or api)")
	timeout := fs.Duration("timeout", 2*time.Minute, "timeout for model request")

	if err := fs.Parse(args); err != nil {
		return prCommandOptions{}, err
	}
	backend, err := resolveOneshotBackend("pr", *backendFlag)
	if err != nil {
		return prCommandOptions{}, err
	}
	return prCommandOptions{
		dryRun:   *dryRun,
		yes:      *yes,
		push:     *pushFlag,
		verbose:  *verbose,
		showCost: *showCost,
		base:     *baseFlag,
		model:    *modelFlag,
		backend:  backend,
		timeout:  *timeout,
	}, nil
}

// runPRCommand generates a structured PR via tool-use.
func runPRCommand(args []string) error {
	opts, err := parsePRCommandOptions(args)
	if err != nil {
		return err
	}

	runtime, cleanup, err := newPRCommandRuntime(opts)
	defer cleanup()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	if !quietMode {
		termOut.Dim("Using %s", describeOneshotBackend(runtime.backend, runtime.modelID))
	}

	result, err := runPRGeneration(ctx, runtime.framework, opts.base)
	if err != nil {
		if opts.verbose && result != nil {
			if result.ContextAudit != nil {
				printContextAudit(result.ContextAudit)
			}
			if result.Trace != nil && result.Trace.Reasoning != "" {
				printReasoning(result.Trace.Reasoning)
			}
		}
		return err
	}

	pr, baseBranch, err := renderPRGenerationResult(opts, result, runtime.ledger)
	if err != nil {
		return err
	}
	if opts.dryRun {
		return nil
	}

	if err := confirmPRCreation(opts); err != nil {
		return err
	}

	if opts.push {
		if err := pushCurrentBranch(); err != nil {
			return err
		}
	}

	if err := createPR(pr, baseBranch); err != nil {
		return err
	}

	return nil
}

func newPRCommandRuntime(opts prCommandOptions) (*prCommandRuntime, func(), error) {
	cfg, mgr, store, err := initOneshotDependencies(opts.backend)
	cleanup := func() {
		if store != nil {
			store.Close()
		}
	}
	if err != nil {
		cleanup()
		return nil, func() {}, fmt.Errorf("init dependencies: %w", err)
	}

	modelID := resolvePRModelID(opts.model, cfg, opts.backend)
	if opts.backend == oneshotBackendAPI && modelID == "" {
		cleanup()
		return nil, func() {}, fmt.Errorf("no model configured (set BUCKLEY_MODEL_PR or configure models.utility.pr)")
	}

	pricing := transparency.ModelPricing{
		InputPerMillion:  3.0,
		OutputPerMillion: 15.0,
	}
	if opts.backend == oneshotBackendAPI && mgr != nil {
		if info, err := mgr.GetModelInfo(modelID); err == nil {
			pricing.InputPerMillion = info.Pricing.Prompt
			pricing.OutputPerMillion = info.Pricing.Completion
		}
	}

	ledger := transparency.NewCostLedger()
	invoker, err := newOneshotToolInvoker(opts.backend, modelID, cfg, mgr, pricing, ledger)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}

	return &prCommandRuntime{
		backend:   opts.backend,
		modelID:   modelID,
		ledger:    ledger,
		framework: oneshot.NewFramework(invoker, nil),
	}, cleanup, nil
}

func runPRGeneration(ctx context.Context, framework *oneshot.Framework, baseBranch string) (*prRunResult, error) {
	spinner := terminal.NewSpinner("Generating PR...")
	spinner.Start()

	base, err := resolvePRBaseRange(ctx, baseBranch)
	if err != nil {
		spinner.StopWithError(err.Error())
		return nil, fmt.Errorf("resolve PR base: %w", err)
	}

	fwResult, err := framework.Run(ctx, commands.PRDefinition{
		BaseBranch:  base.Branch,
		BaseCommit:  base.Commit,
		CommitRange: base.Range,
	}, oneshot.RunOpts{})
	result := prRunResultFromFramework(fwResult)
	if err != nil {
		result.Error = err
		spinner.StopWithError(err.Error())
		return result, fmt.Errorf("PR generation failed: %w", err)
	}
	if result.Error != nil {
		spinner.StopWithError(result.Error.Error())
	} else {
		spinner.StopWithSuccess("Generated PR")
	}
	return result, nil
}

// resolvePRBaseRange fetches the selected base branch, resolves it to an
// immutable commit, then computes the exact merge-base..HEAD range. PR context
// must never be assembled from a stale local branch or a moving remote ref.
func resolvePRBaseRange(ctx context.Context, baseFlag string) (prBaseRange, error) {
	_, baseBranch := detectPRBranches(baseFlag)
	remote := os.Getenv("BUCKLEY_REMOTE_NAME")
	if remote == "" {
		remote = "origin"
	}
	baseBranch = strings.TrimSpace(baseBranch)
	baseBranch = strings.TrimPrefix(baseBranch, "refs/heads/")
	baseBranch = strings.TrimPrefix(baseBranch, "refs/remotes/"+remote+"/")
	baseBranch = strings.TrimPrefix(baseBranch, remote+"/")
	if baseBranch == "" {
		return prBaseRange{}, fmt.Errorf("base branch is empty")
	}
	if err := exec.CommandContext(ctx, "git", "check-ref-format", "--branch", baseBranch).Run(); err != nil {
		return prBaseRange{}, fmt.Errorf("invalid base branch %q", baseBranch)
	}

	resolvedRef := baseBranch
	if err := exec.CommandContext(ctx, "git", "remote", "get-url", remote).Run(); err == nil {
		remoteRef := "refs/remotes/" + remote + "/" + baseBranch
		refspec := "+refs/heads/" + baseBranch + ":" + remoteRef
		cmd := exec.CommandContext(ctx, "git", "fetch", "--quiet", "--no-tags", remote, refspec)
		if output, err := cmd.CombinedOutput(); err != nil {
			detail := strings.TrimSpace(string(output))
			if detail != "" {
				return prBaseRange{}, fmt.Errorf("fetch %s/%s: %s", remote, baseBranch, detail)
			}
			return prBaseRange{}, fmt.Errorf("fetch %s/%s: %w", remote, baseBranch, err)
		}
		resolvedRef = remoteRef
	}

	baseCommit, err := prGitOutput(ctx, "rev-parse", "--verify", resolvedRef+"^{commit}")
	if err != nil {
		return prBaseRange{}, fmt.Errorf("resolve base %q: %w", resolvedRef, err)
	}
	mergeBase, err := prGitOutput(ctx, "merge-base", baseCommit, "HEAD")
	if err != nil {
		return prBaseRange{}, fmt.Errorf("merge-base %s and HEAD: %w", baseCommit, err)
	}

	return prBaseRange{
		Branch:    baseBranch,
		Commit:    baseCommit,
		MergeBase: mergeBase,
		Range:     mergeBase + "..HEAD",
	}, nil
}

func prGitOutput(ctx context.Context, args ...string) (string, error) {
	output, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return "", fmt.Errorf("%s", detail)
		}
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func prRunResultFromFramework(fwResult *oneshot.RunResult) *prRunResult {
	result := &prRunResult{}
	if fwResult == nil {
		return result
	}
	result.Trace = fwResult.Trace
	result.ContextAudit = fwResult.ContextAudit
	if fwResult.Value == nil {
		return result
	}
	pr, ok := fwResult.Value.(*commands.PRResult)
	if !ok {
		result.Error = fmt.Errorf("unexpected result type: %T", fwResult.Value)
		return result
	}
	result.PR = pr
	return result
}

func renderPRGenerationResult(opts prCommandOptions, result *prRunResult, ledger *transparency.CostLedger) (*commands.PRResult, string, error) {
	if opts.verbose && result.ContextAudit != nil {
		printContextAudit(result.ContextAudit)
	}
	if opts.verbose && result.Trace != nil && result.Trace.Reasoning != "" {
		printReasoning(result.Trace.Reasoning)
	}
	if result.Error != nil {
		printError(result.Error, result.Trace)
		return nil, "", result.Error
	}
	if result.PR == nil {
		return nil, "", fmt.Errorf("no PR generated")
	}

	branch, baseBranch := detectPRBranches(opts.base)
	printPR(result.PR, branch, baseBranch)
	if opts.showCost && result.Trace != nil {
		printCost(result.Trace, ledger)
	}
	return result.PR, baseBranch, nil
}

func confirmPRCreation(opts prCommandOptions) error {
	if opts.yes {
		return nil
	}
	if !stdinIsTerminalFn() {
		return fmt.Errorf("refusing to create PR without confirmation in non-interactive mode (use --dry-run or --yes)")
	}
	fmt.Print("\nCreate this PR? [y/N] ")
	var response string
	fmt.Scanln(&response)
	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		return fmt.Errorf("aborted")
	}
	return nil
}

// detectPRBranches returns the current branch and base branch for PR display.
func detectPRBranches(baseFlag string) (branch, baseBranch string) {
	ctx, cancel := context.WithTimeout(context.Background(), gitLocalTimeout)
	defer cancel()

	if out, err := exec.CommandContext(ctx, "git", "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		branch = strings.TrimSpace(string(out))
	}
	if baseFlag != "" {
		baseBranch = baseFlag
	} else {
		// Auto-detect main/master
		for _, candidate := range []string{"main", "master", "develop"} {
			if err := exec.CommandContext(ctx, "git", "rev-parse", "--verify", candidate).Run(); err == nil {
				baseBranch = candidate
				break
			}
		}
		if baseBranch == "" {
			baseBranch = "main"
		}
	}
	return
}

func printPR(pr *commands.PRResult, branch, baseBranch string) {
	termOut.Newline()
	termOut.Header(fmt.Sprintf("PULL REQUEST: %s → %s", branch, baseBranch))

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
	branchCtx, branchCancel := context.WithTimeout(context.Background(), gitLocalTimeout)
	defer branchCancel()

	branch, err := exec.CommandContext(branchCtx, "git", "rev-parse", "--abbrev-ref", "HEAD").Output()
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

	pushCtx, pushCancel := context.WithTimeout(context.Background(), gitPushTimeout)
	defer pushCancel()

	cmd := exec.CommandContext(pushCtx, "git", "push", "-u", remote, branchName)
	output, err := cmd.CombinedOutput()
	if err != nil {
		spinner.StopWithError(fmt.Sprintf("push failed: %s", strings.TrimSpace(string(output))))
		return fmt.Errorf("git push failed: %w", err)
	}

	hashCtx, hashCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer hashCancel()
	if hash := currentHeadHash(hashCtx, false); hash != "" {
		spinner.StopWithSuccess(fmt.Sprintf("Pushed: %s to %s/%s", hash, remote, branchName))
	} else {
		spinner.StopWithSuccess(fmt.Sprintf("Pushed to %s/%s", remote, branchName))
	}
	return nil
}

func createPR(pr *commands.PRResult, baseBranch string) error {
	// Check for gh CLI
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh CLI not found (install from https://cli.github.com)")
	}

	spinner := terminal.NewSpinner("Creating PR...")
	spinner.Start()

	ctx, cancel := context.WithTimeout(context.Background(), ghAPITimeout)
	defer cancel()

	// Create PR using gh
	body := pr.FormatBody()

	cmd := exec.CommandContext(ctx, "gh", "pr", "create",
		"--title", pr.Title,
		"--body", body,
		"--base", baseBranch,
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
