package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/odvcencio/buckley/pkg/oneshot"
	"github.com/odvcencio/buckley/pkg/oneshot/commands"
	"github.com/odvcencio/buckley/pkg/terminal"
	"github.com/odvcencio/buckley/pkg/transparency"
)

// termOut is the terminal writer for styled output.
var termOut = terminal.New()

// commitRunResult adapts the new framework's RunResult for use by the CLI.
type commitRunResult struct {
	Commit       *commands.CommitResult
	Trace        *transparency.Trace
	ContextAudit *transparency.ContextAudit
	Error        error
}

type commitRunner interface {
	Run(ctx context.Context) (*commitRunResult, error)
}

// frameworkCommitRunner adapts oneshot.Framework to the commitRunner interface.
type frameworkCommitRunner struct {
	framework *oneshot.Framework
}

func (r *frameworkCommitRunner) Run(ctx context.Context) (*commitRunResult, error) {
	fwResult, err := r.framework.Run(ctx, commands.CommitDefinition{}, oneshot.RunOpts{})
	if err != nil {
		return &commitRunResult{Error: err}, nil
	}
	result := &commitRunResult{
		Trace:        fwResult.Trace,
		ContextAudit: fwResult.ContextAudit,
	}
	if fwResult.Value != nil {
		if cr, ok := fwResult.Value.(*commands.CommitResult); ok {
			result.Commit = cr
		} else {
			result.Error = fmt.Errorf("unexpected result type: %T", fwResult.Value)
		}
	}
	return result, nil
}

// runCommitCommand generates a structured commit via tool-use.
func runCommitCommand(args []string) error {
	fs := flag.NewFlagSet("commit", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print the generated commit message without committing")
	yes := fs.Bool("yes", false, "skip confirmation prompts and run git commit")
	pushFlag := fs.Bool("push", true, "push current branch after committing")
	verbose := fs.Bool("verbose", false, "stream model reasoning as it happens")
	minimalOutput := fs.Bool("minimal-output", false, "minimize output (prints commit message and critical errors only)")
	minAlias := fs.Bool("min", false, "alias for --minimal-output")
	graftMode := fs.Bool("graft", false, "use graft commit/push instead of git")
	trace := fs.Bool("trace", false, "show context audit and reasoning trace after completion")
	showCost := fs.Bool("cost", true, "show token/cost breakdown")
	modelFlag := fs.String("model", "", "model to use (default: BUCKLEY_MODEL_COMMIT or models.utility.commit for API backend)")
	backendFlag := fs.String("backend", "", "backend to use: api, codex, or claude (default: BUCKLEY_COMMIT_BACKEND, BUCKLEY_ONESHOT_BACKEND, or api)")
	timeout := fs.Duration("timeout", 2*time.Minute, "timeout for model request")

	if err := fs.Parse(args); err != nil {
		return err
	}
	backend, err := resolveOneshotBackend("commit", *backendFlag)
	if err != nil {
		return err
	}
	compactOutput := *minimalOutput || *minAlias || oneshotMinimalOutputEnabled()
	useGraft := *graftMode || os.Getenv("BUCKLEY_USE_GRAFT") == "1"
	filesToStage := fs.Args() // remaining positional args are files to stage

	// Stage files if provided
	if len(filesToStage) > 0 {
		if err := stageFiles(filesToStage, useGraft, compactOutput); err != nil {
			return fmt.Errorf("staging failed: %w", err)
		}
	}

	// Initialize dependencies
	cfg, mgr, store, err := initOneshotDependencies(backend)
	if store != nil {
		defer store.Close()
	}
	if err != nil {
		return fmt.Errorf("init dependencies: %w", err)
	}

	// Determine model
	modelID := resolveCommitModelID(*modelFlag, cfg, backend)
	if backend == oneshotBackendAPI && modelID == "" {
		return fmt.Errorf("no model configured (set BUCKLEY_MODEL_COMMIT or configure models.utility.commit)")
	}

	// Get model pricing for cost calculation
	pricing := transparency.ModelPricing{
		InputPerMillion:  3.0, // Default pricing, will be overridden if we can fetch
		OutputPerMillion: 15.0,
	}
	if backend == oneshotBackendAPI && mgr != nil {
		if info, err := mgr.GetModelInfo(modelID); err == nil {
			pricing.InputPerMillion = info.Pricing.Prompt
			pricing.OutputPerMillion = info.Pricing.Completion
		}
	}

	// Create cost ledger for tracking
	ledger := transparency.NewCostLedger()

	// Create invoker
	invoker, err := newOneshotToolInvoker(backend, modelID, cfg, mgr, pricing, ledger)
	if err != nil {
		return err
	}

	// Create unified framework runner
	framework := oneshot.NewFramework(invoker, nil)
	var runner commitRunner = &frameworkCommitRunner{framework: framework}

	// Run with timeout
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	// Show what we're doing
	if !quietMode {
		termOut.Dim("Using %s", describeOneshotBackend(backend, modelID))
	}

	// Execute the commit generation with spinner
	spinner := terminal.NewSpinner("Generating commit message...")
	spinner.Start()

	result, err := runner.Run(ctx)

	if err != nil {
		spinner.StopWithError(err.Error())
	} else if result.Error != nil {
		spinner.StopWithError(result.Error.Error())
	} else {
		spinner.StopWithSuccess("Generated commit message")
	}
	if err != nil {
		return fmt.Errorf("commit generation failed: %w", err)
	}

	// Show context audit (what was sent)
	if (*verbose || *trace) && result.ContextAudit != nil {
		printContextAudit(result.ContextAudit)
	}

	// Show reasoning (for thinking models)
	if (*verbose || *trace) && result.Trace != nil && result.Trace.Reasoning != "" {
		printReasoning(result.Trace.Reasoning)
	}

	// Check for errors
	if result.Error != nil {
		printError(result.Error, result.Trace)
		return result.Error
	}

	// Show the commit message
	if result.Commit == nil {
		return fmt.Errorf("no commit generated")
	}

	message := result.Commit.Format()
	printCommitMessage(message)

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
			return fmt.Errorf("refusing to commit without confirmation in non-interactive mode (use --dry-run or --yes)")
		}

		// Interactive prompt loop with regenerate/edit options
		for {
			action, newMessage := handleCommitPrompt(message, runner, ctx, *showCost, ledger)
			switch action {
			case "commit":
				message = newMessage
				goto doCommit
			case "abort":
				return fmt.Errorf("aborted")
			case "regenerate":
				message = newMessage
				printCommitMessage(message)
				continue
			}
		}
	}
doCommit:

	// Create the commit
	if err := createCommit(message, compactOutput, useGraft); err != nil {
		return err
	}

	// Push if requested
	if *pushFlag {
		if err := pushChanges(compactOutput, useGraft); err != nil {
			return err
		}
	}

	return nil
}

// handleCommitPrompt shows an interactive prompt with options to commit, regenerate, edit, or abort.
// Returns the action taken and the (possibly modified) message.
func handleCommitPrompt(message string, runner commitRunner, ctx context.Context, showCost bool, ledger *transparency.CostLedger) (string, string) {
	fmt.Print("\n[y] Commit  [r] Regenerate  [e] Edit  [n] Abort: ")
	var response string
	fmt.Scanln(&response)
	response = strings.ToLower(strings.TrimSpace(response))

	switch response {
	case "y", "yes":
		return "commit", message

	case "r", "regenerate":
		// Regenerate the commit message
		spinner := terminal.NewSpinner("Regenerating commit message...")
		spinner.Start()

		result, err := runner.Run(ctx)
		if err != nil {
			spinner.StopWithError(err.Error())
			termOut.Error("Regeneration failed: %v", err)
			return "regenerate", message // Keep old message
		}
		if result.Error != nil {
			spinner.StopWithError(result.Error.Error())
			termOut.Error("Regeneration failed: %v", result.Error)
			return "regenerate", message
		}
		spinner.StopWithSuccess("Regenerated commit message")

		if result.Commit == nil {
			termOut.Error("No commit generated")
			return "regenerate", message
		}

		// Show cost if enabled
		if showCost && result.Trace != nil {
			printCost(result.Trace, ledger)
		}

		return "regenerate", result.Commit.Format()

	case "e", "edit":
		// Open message in editor
		edited, err := editInEditor(message)
		if err != nil {
			termOut.Error("Edit failed: %v", err)
			return "regenerate", message // Stay in loop
		}
		if strings.TrimSpace(edited) == "" {
			termOut.Error("Empty message, keeping original")
			return "regenerate", message
		}
		return "commit", edited

	case "n", "no", "q", "quit":
		return "abort", message

	default:
		termOut.Dim("Unknown option '%s'", response)
		return "regenerate", message // Stay in loop
	}
}

// editInEditor opens the message in $EDITOR for editing
func editInEditor(message string) (string, error) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi" // fallback
	}

	// Write message to temp file
	tmp, err := os.CreateTemp("", "buckley-edit-*.txt")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.WriteString(message); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write temp file: %w", err)
	}
	tmp.Close()

	// Open editor
	cmd := exec.Command(editor, tmpPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("editor failed: %w", err)
	}

	// Read back
	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return "", fmt.Errorf("read edited file: %w", err)
	}

	return string(data), nil
}

func printContextAudit(audit *transparency.ContextAudit) {
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

func printReasoning(reasoning string) {
	termOut.Newline()
	termOut.Header("MODEL REASONING")

	// Truncate for display
	lines := strings.Split(reasoning, "\n")
	maxLines := 10
	var displayLines []string
	for i, line := range lines {
		if i >= maxLines {
			displayLines = append(displayLines, fmt.Sprintf("... (%d more lines)", len(lines)-maxLines))
			break
		}
		if len(line) > 80 {
			line = line[:77] + "..."
		}
		displayLines = append(displayLines, line)
	}
	termOut.Dim("%s", strings.Join(displayLines, "\n"))
}

func printCommitMessage(message string) {
	if stdinIsTerminalFn() {
		// In a TTY, show styled commit message
		termOut.Newline()
		termOut.CommitMessage(message)
	} else {
		// For piping, print raw message only
		fmt.Println(message)
	}
}

func printCost(trace *transparency.Trace, ledger *transparency.CostLedger) {
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

func printError(err error, trace *transparency.Trace) {
	termOut.Newline()
	termOut.Error("%s", err.Error())

	if trace != nil {
		termOut.Dim("Tokens used: %d · Cost: $%.4f (still charged)",
			trace.Tokens.Total(), trace.Cost)
	}
}

// oneshotMinimalOutputEnabled checks if minimal output is enabled via environment.
func oneshotMinimalOutputEnabled() bool {
	v := os.Getenv("BUCKLEY_MINIMAL_OUTPUT")
	return v == "1" || v == "true"
}

// stageFiles stages the given files with the appropriate VCS.
// In graft mode: graft add (with entity extraction) first, then git add as mirror.
// In git mode: git add only.
func stageFiles(files []string, useGraft bool, compactOutput bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if useGraft {
		// Graft is primary — stage with full entity extraction
		args := append([]string{"add"}, files...)
		cmd := exec.CommandContext(ctx, "graft", args...)
		if compactOutput {
			cmd.Stdout = io.Discard
			cmd.Stderr = io.Discard
		} else {
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
		}
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("graft add: %w", err)
		}
	}
	// Always mirror to git (buckley needs git diff for context)
	gitArgs := append([]string{"add"}, files...)
	gitCmd := exec.CommandContext(ctx, "git", gitArgs...)
	gitCmd.Stdout = io.Discard
	gitCmd.Stderr = io.Discard
	if err := gitCmd.Run(); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	return nil
}

// stageForGraft mirrors the git staging index into graft by running graft add
// on all files that git reports as staged.
func stageForGraft(compactOutput bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get list of git-staged files
	out, err := exec.CommandContext(ctx, "git", "diff", "--cached", "--name-only").Output()
	if err != nil {
		return fmt.Errorf("list staged files: %w", err)
	}
	files := strings.Split(strings.TrimSpace(string(out)), "\n")
	var toAdd []string
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f != "" {
			toAdd = append(toAdd, f)
		}
	}
	if len(toAdd) == 0 {
		return nil
	}
	args := append([]string{"add"}, toAdd...)
	cmd := exec.CommandContext(ctx, "graft", args...)
	if compactOutput {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	return cmd.Run()
}

func createCommit(message string, compactOutput bool, useGraft bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Write message to temp file
	tmp, err := os.CreateTemp("", "buckley-commit-*.txt")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)

	if err := os.WriteFile(tmpPath, []byte(message), 0o600); err != nil {
		return fmt.Errorf("write commit message: %w", err)
	}

	vcs := "git"
	if useGraft {
		vcs = "graft"
	}

	// Stage files in graft if graft mode is enabled
	if useGraft {
		if err := stageForGraft(compactOutput); err != nil {
			return fmt.Errorf("graft staging failed: %w", err)
		}
	}

	// Run commit — graft uses -m, git uses -F for file-based messages
	var cmd *exec.Cmd
	if useGraft {
		cmd = exec.CommandContext(ctx, "graft", "commit", "-m", message)
	} else {
		cmd = exec.CommandContext(ctx, "git", "commit", "-F", tmpPath)
	}
	if compactOutput {
		var stderr bytes.Buffer
		cmd.Stdout = io.Discard
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			detail := strings.TrimSpace(stderr.String())
			if detail != "" {
				return fmt.Errorf("%s commit failed: %w: %s", vcs, err, detail)
			}
			return fmt.Errorf("%s commit failed: %w", vcs, err)
		}
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s commit failed: %w", vcs, err)
		}
	}

	// Mirror to git when in graft mode (git is the compatibility layer)
	if useGraft {
		gitCmd := exec.CommandContext(ctx, "git", "commit", "-F", tmpPath)
		if compactOutput {
			gitCmd.Stdout = io.Discard
			gitCmd.Stderr = io.Discard
		} else {
			gitCmd.Stdout = io.Discard
			gitCmd.Stderr = io.Discard
		}
		_ = gitCmd.Run() // best-effort mirror, don't fail if git commit fails
	}

	// Show commit hash
	if !compactOutput {
		if hash := currentHeadHash(ctx, useGraft); hash != "" {
			termOut.Success("Committed: %s", hash)
		}
	}

	return nil
}

func currentHeadHash(ctx context.Context, useGraft bool) string {
	if useGraft {
		if hash, err := exec.CommandContext(ctx, "graft", "log", "--format=%H", "-1").Output(); err == nil {
			if trimmed := strings.TrimSpace(string(hash)); trimmed != "" {
				return trimmed
			}
		}
	}
	hash, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(hash))
}

func pushChanges(compactOutput bool, useGraft bool) error {
	vcs := "git"
	if useGraft {
		vcs = "graft"
	}

	// Get current branch (local git op: 30s)
	branchCtx, branchCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer branchCancel()

	var branch []byte
	var err error
	if useGraft {
		branch, err = exec.CommandContext(branchCtx, "graft", "branch", "--show-current").Output()
	} else {
		branch, err = exec.CommandContext(branchCtx, "git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	}
	if err != nil {
		return nil // Skip push if we can't get branch
	}
	branchName := strings.TrimSpace(string(branch))
	if branchName == "" || branchName == "HEAD" {
		return nil // Detached HEAD, skip push
	}

	// Check if remote exists
	remote := os.Getenv("BUCKLEY_REMOTE_NAME")
	if remote == "" {
		remote = "origin"
	}

	// Push (network op: 60s)
	pushCtx, pushCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer pushCancel()

	var cmd *exec.Cmd
	if useGraft {
		cmd = exec.CommandContext(pushCtx, "graft", "push", remote, branchName)
	} else if compactOutput {
		cmd = exec.CommandContext(pushCtx, "git", "push", "--quiet", "-u", remote, branchName)
	} else {
		cmd = exec.CommandContext(pushCtx, "git", "push", "-u", remote, branchName)
	}
	if compactOutput {
		var stderr bytes.Buffer
		cmd.Stdout = io.Discard
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			detail := strings.TrimSpace(stderr.String())
			if detail != "" {
				return fmt.Errorf("%s push failed: %w: %s", vcs, err, detail)
			}
			return fmt.Errorf("%s push failed: %w", vcs, err)
		}
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s push failed: %w", vcs, err)
		}
	}

	// Mirror push to git when in graft mode
	if useGraft {
		gitPush := exec.CommandContext(pushCtx, "git", "push", "--quiet", "-u", remote, branchName)
		gitPush.Stdout = io.Discard
		gitPush.Stderr = io.Discard
		_ = gitPush.Run() // best-effort mirror
	}

	hashCtx, hashCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer hashCancel()
	hash := currentHeadHash(hashCtx, useGraft)
	if compactOutput {
		if hash != "" {
			fmt.Printf("Pushed: %s\n", hash)
		}
	} else if hash != "" {
		termOut.Success("Pushed: %s to %s/%s", hash, remote, branchName)
	} else {
		termOut.Success("Pushed to %s/%s", remote, branchName)
	}
	return nil
}
