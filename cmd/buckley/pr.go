package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/diffsignal"
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
	dryRun       bool
	yes          bool
	push         bool
	verbose      bool
	showCost     bool
	base         string
	model        string
	effort       string
	backend      string
	timeout      time.Duration
	diffBudget   int
	contextFiles []string
	noUpdate     bool
	draft        bool
	// head is the --head value; empty means the checked-out branch.
	head string
	// dir is the -C/--dir value; empty means the launch directory.
	dir string
	// verificationLabel is the requirement to set on the PR after it is
	// created or updated; empty leaves labels alone.
	verificationLabel commands.VerificationMode
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

func parsePRCommandOptions(args []string) (prCommandOptions, error) {
	fs := flag.NewFlagSet("pr", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "print the generated PR without creating it")
	yes := fs.Bool("yes", false, "skip confirmation prompts and create the PR")
	pushFlag := fs.Bool("push", true, "push the head branch before creating PR")
	baseFlag := fs.String("base", "", "base branch (default: BUCKLEY_PR_BASE, then the remote's default branch, then main, master or develop)")
	headFlag := fs.String("head", "", "branch the PR comes from (default: the checked-out branch); owner:branch names a fork, and owner must own the push remote's repository. The PR text is built from that branch's commits, so the worktree can be on another branch or dirty")
	var dir string
	fs.StringVar(&dir, "C", "", "run as if started in this directory, like git -C (uppercase; lowercase -c selects the config file). Relative --context-file, --config and --agent paths resolve against the directory you launched in")
	fs.StringVar(&dir, "dir", "", "same as -C")
	verbose := fs.Bool("verbose", false, "show model reasoning and full trace")
	showCost := fs.Bool("cost", true, "show token/cost breakdown")
	modelFlag := fs.String("model", "", "model to use (default: BUCKLEY_MODEL_PR or models.utility.pr for API backend)")
	effortFlag := fs.String("effort", "", "reasoning effort: low, medium, high, xhigh, or max (default: BUCKLEY_EFFORT_PR, BUCKLEY_ONESHOT_EFFORT, or config)")
	backendFlag := fs.String("backend", "", "backend to use: api, codex, or claude (default: BUCKLEY_PR_BACKEND, BUCKLEY_ONESHOT_BACKEND, or api)")
	timeout := fs.Duration("timeout", 2*time.Minute, "timeout for model request")
	diffBudget := fs.Int("diff-budget", diffsignal.PRDiffBudget, "total byte budget for gathered diff context (default: PR-scale budget, larger than the commit-message default)")
	var contextFileFlag stringSliceFlag
	fs.Var(&contextFileFlag, "context-file", "path to a file of author-supplied notes for the PR (repeatable); content is treated as data, not instructions")
	noUpdate := fs.Bool("no-update", false, "fail instead of silently overwriting an existing open PR's title/body")
	draft := fs.Bool("draft", false, "create the PR as a draft (passed through to gh pr create --draft)")
	requireVerification := fs.Bool("require-verification", false, "label the PR buckbot:verify-required so Buckbot needs a receipt or passing CI before it reviews")
	requireReceipt := fs.Bool("require-receipt", false, "label the PR buckbot:verify-receipt (receipt only)")
	noRequireVerification := fs.Bool("no-require-verification", false, "label the PR buckbot:verify-optional")

	if err := fs.Parse(args); err != nil {
		return prCommandOptions{}, err
	}
	if operands := fs.Args(); len(operands) > 0 {
		msg := fmt.Sprintf("unexpected argument %q: buckley pr takes flags only (name the branch with --head %s)", operands[0], operands[0])
		if dropped, _, found := unreadFlag(fs, args); found {
			msg += fmt.Sprintf("; the flag %q after it would not have been read either, so put every flag first", dropped)
		}
		return prCommandOptions{}, errors.New(msg)
	}
	effort, err := resolveOneshotEffort("pr", *effortFlag)
	if err != nil {
		return prCommandOptions{}, err
	}
	backend, err := resolveOneshotBackend("pr", *backendFlag)
	if err != nil {
		return prCommandOptions{}, err
	}
	verificationLabel, err := verificationFlagMode(*requireVerification, *requireReceipt, *noRequireVerification)
	if err != nil {
		return prCommandOptions{}, err
	}
	return prCommandOptions{
		verificationLabel: verificationLabel,
		dryRun:            *dryRun,
		yes:               *yes,
		push:              *pushFlag,
		verbose:           *verbose,
		showCost:          *showCost,
		base:              *baseFlag,
		model:             *modelFlag,
		effort:            effort,
		backend:           backend,
		timeout:           *timeout,
		diffBudget:        *diffBudget,
		contextFiles:      append([]string(nil), contextFileFlag...),
		noUpdate:          *noUpdate,
		draft:             *draft,
		head:              *headFlag,
		dir:               dir,
	}, nil
}

// prContextNotesMaxBytes bounds the total size of author-supplied
// --context-file content that reaches the prompt: enough for a slice
// breakdown and verification notes, not an unbounded attachment.
const prContextNotesMaxBytes = 16_000

// loadPRContextNotes reads and concatenates the given --context-file paths,
// each labeled by its path, bounded to prContextNotesMaxBytes total. A
// missing or unreadable file is an error (an explicitly-requested file
// vanishing silently would defeat the point of asking for it); the size
// bound truncates rather than errors, since a legitimate note that merely
// ran long is still useful context.
func loadPRContextNotes(paths []string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}

	var b strings.Builder
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read --context-file %s: %w", path, err)
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "### %s\n\n%s", path, content)
	}

	out := b.String()
	if len(out) > prContextNotesMaxBytes {
		out = out[:prContextNotesMaxBytes] + "\n... (truncated)"
	}
	return out, nil
}

// prContextOpts derives the git-diff gathering options for a PR run: a
// PR-scale byte budget (diffsignal.PRDiffBudget by default, overridable via
// --diff-budget) and PR-specific ranking (source first, tests next,
// docs/config after, dotdirs/scratch/handoff notes last) rather than the
// 80KB commit-message default and git's alphabetical file order that
// RunOpts{} previously left BuildContext to fall back to.
func prContextOpts(diffBudget int) oneshot.ContextOpts {
	if diffBudget <= 0 {
		diffBudget = diffsignal.PRDiffBudget
	}
	return oneshot.ContextOpts{
		MaxDiffBytes:  diffBudget,
		RankDiffForPR: true,
	}
}

// runPRCommand generates a structured PR via tool-use.
func runPRCommand(args []string) error {
	if len(args) > 0 && args[0] == "merge" {
		return runPRMergeCommand(args[1:])
	}
	if len(args) > 0 && args[0] == "label" {
		return runPRLabelCommand(args[1:])
	}

	opts, err := parsePRCommandOptions(args)
	if err != nil {
		return err
	}
	if opts.dir != "" {
		if opts.contextFiles, err = enterWorkDir(opts.dir, opts.contextFiles); err != nil {
			return err
		}
	}

	plan, err := preflightPR(opts, execGhCommand)
	if err != nil {
		return err
	}
	warnPRHeadState(opts, plan)
	warnPRGitVersion()

	contextNotes, err := loadPRContextNotes(opts.contextFiles)
	if err != nil {
		return err
	}

	runtime, cleanup, err := newPRCommandRuntimeFn(opts)
	defer cleanup()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	if !quietMode {
		termOut.Dim("Using %s", describeOneshotBackend(runtime.backend, runtime.modelID))
	}

	result, err := runPRGeneration(ctx, runtime.framework, plan, opts.diffBudget, contextNotes)
	if err != nil {
		return err
	}

	pr, err := renderPRGenerationResult(opts, plan, result, runtime.ledger)
	if err != nil {
		return err
	}
	if opts.dryRun {
		return nil
	}

	if err := confirmPRCreation(opts); err != nil {
		return err
	}

	if opts.push && plan.Head.PushNeeded {
		if err := pushPRHead(plan.Head, plan.Remote); err != nil {
			return err
		}
	}
	warnIfPRHeadMoved(plan.Head)

	prURL, err := createPR(execGhCommand, pr, plan, opts)
	if err != nil {
		return err
	}
	if opts.verificationLabel != "" {
		ctx, cancel := context.WithTimeout(context.Background(), ghAPITimeout)
		defer cancel()
		if _, _, err := setVerificationLabel(ctx, ghLabelClient{}, prURL, opts.verificationLabel); err != nil {
			return err
		}
		termOut.Dim("Verification policy label set: %s", opts.verificationLabel)
	}

	return nil
}

// newPRCommandRuntimeFn builds the model runtime. Tests replace it.
var newPRCommandRuntimeFn = newPRCommandRuntime

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

	modelID, cfg := resolveOneshotModelWithEffort(cfg, opts.effort, func(cfg *config.Config) string {
		return resolvePRModelID(opts.model, cfg, opts.backend)
	})
	if opts.backend == oneshotBackendAPI && modelID == "" {
		cleanup()
		return nil, func() {}, fmt.Errorf("no model configured (set BUCKLEY_MODEL_PR or configure models.utility.pr)")
	}

	ledger := transparency.NewCostLedger()
	invoker, err := newOneshotToolInvoker(opts.backend, "pr", modelID, cfg, mgr, ledger)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}

	return &prCommandRuntime{
		backend:   opts.backend,
		modelID:   modelID,
		ledger:    ledger,
		framework: withUtilityValidationFallbacks(oneshot.NewFramework(invoker, nil), opts.backend, "pr", modelID, cfg, mgr, ledger),
	}, cleanup, nil
}

func runPRGeneration(ctx context.Context, framework *oneshot.Framework, plan *prPlan, diffBudget int, contextNotes string) (*prRunResult, error) {
	spinner := terminal.NewSpinner("Generating PR...")
	spinner.Start()

	def := commands.PRDefinition{
		BaseBranch:   plan.Base.Ref,
		BaseName:     plan.Base.Label,
		HeadRef:      plan.Head.Ref,
		HeadName:     plan.Head.Name,
		ContextNotes: contextNotes,
	}
	fwResult, err := framework.Run(ctx, def, oneshot.RunOpts{
		ContextOpts: prContextOpts(diffBudget),
	})
	result := prRunResultFromFramework(fwResult)
	if err != nil {
		result.Error = err
		spinner.StopWithError(err.Error())
		return nil, fmt.Errorf("PR generation failed: %w", err)
	}
	if result.Error != nil {
		spinner.StopWithError(result.Error.Error())
	} else {
		spinner.StopWithSuccess("Generated PR")
	}
	return result, nil
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

func renderPRGenerationResult(opts prCommandOptions, plan *prPlan, result *prRunResult, ledger *transparency.CostLedger) (*commands.PRResult, error) {
	if opts.verbose && result.ContextAudit != nil {
		printContextAudit(result.ContextAudit)
	}
	if opts.verbose && result.Trace != nil && result.Trace.Reasoning != "" {
		printReasoning(result.Trace.Reasoning)
	}
	if result.Error != nil {
		printError(result.Error, result.Trace)
		return nil, result.Error
	}
	if result.PR == nil {
		return nil, fmt.Errorf("no PR generated")
	}

	printPR(result.PR, plan)
	if opts.showCost && result.Trace != nil {
		printCost(result.Trace, ledger)
	}
	return result.PR, nil
}

var errPRNonInteractive = errors.New("refusing to create PR without confirmation in non-interactive mode (use --dry-run or --yes)")

func confirmPRCreation(opts prCommandOptions) error {
	if opts.yes {
		return nil
	}
	if !stdinIsTerminalFn() {
		return errPRNonInteractive
	}
	fmt.Print("\nCreate this PR? [y/N] ")
	var response string
	fmt.Scanln(&response)
	if strings.ToLower(response) != "y" && strings.ToLower(response) != "yes" {
		return fmt.Errorf("aborted")
	}
	return nil
}

// prPlan is everything the preflight resolves before the model call.
type prPlan struct {
	Head    prHead
	Base    prBase
	Remote  string
	Commits int
}

// explicitBase returns the base branch only when the caller chose it, which is
// the only case where an open PR with another base is a conflict.
func (p *prPlan) explicitBase() string {
	if p.Base.Explicit {
		return p.Base.Short
	}
	return ""
}

// summary renders "head -> base (N commits, tip abc1234)".
func (p *prPlan) summary() string {
	noun := "commits"
	if p.Commits == 1 {
		noun = "commit"
	}
	from := ""
	if !p.Head.Local {
		from = ", read from " + strings.TrimPrefix(p.Head.Ref, "refs/remotes/")
	}
	return fmt.Sprintf("%s → %s (%d %s, tip %s%s)", p.Head.Name, p.Base.Short, p.Commits, noun, shortSHA(p.Head.SHA), from)
}

// lookPathFn finds an executable on PATH. Tests replace it.
var lookPathFn = exec.LookPath

var errGhNotFound = errors.New("gh CLI not found (install from https://cli.github.com)")

// preflightPR runs every check that can fail without a model: the
// confirmation channel, gh, the head and base branches, the commit count and
// conflicts with an open PR. A failure here costs no tokens. A dry run skips
// the checks that need gh, the network or the push remote's URL, so it previews
// in a repository whose remote is missing or is not a GitHub URL.
func preflightPR(opts prCommandOptions, run ghCommandRunner) (*prPlan, error) {
	if !opts.yes && !opts.dryRun && !stdinIsTerminalFn() {
		return nil, errPRNonInteractive
	}
	if !opts.dryRun {
		if _, err := lookPathFn("gh"); err != nil {
			return nil, errGhNotFound
		}
	}

	remote, err := prRemoteName()
	if err != nil {
		return nil, err
	}
	head, err := resolvePRHead(prHeadOptions{Flag: opts.head, Remote: remote, Push: opts.push, CheckRemote: !opts.dryRun})
	if err != nil {
		return nil, err
	}
	base, err := resolvePRBase(opts.base, remote)
	if err != nil {
		return nil, err
	}
	if head.Name == base.Short {
		return nil, fmt.Errorf("head and base are both %s; pass --head <branch> or --base <branch>", head.Name)
	}
	commits, err := countPRCommits(base.Ref, head.Ref)
	if err != nil {
		return nil, err
	}
	if commits == 0 {
		return nil, fmt.Errorf("%s has no commits that %s lacks, so there is no PR to open", head.Name, base.Label)
	}
	plan := &prPlan{Head: head, Base: base, Remote: remote, Commits: commits}

	if opts.dryRun {
		return plan, nil
	}
	if plan.Head.Owner, err = prHeadOwner(head, remote); err != nil {
		return nil, err
	}
	existing, err := openPRForHead(run, plan.Head, plan.explicitBase())
	if err != nil {
		return nil, err
	}
	if existing == nil {
		return plan, nil
	}
	if _, err := resolvePRUpdatePolicy(existing.URL, true, opts.noUpdate); err != nil {
		return nil, err
	}
	if !base.Explicit && existing.Base != base.Short {
		prWarnf("the open PR %s targets %s, but the PR text is written against %s; pass --base %s to match", existing.URL, existing.Base, base.Short, existing.Base)
	}
	return plan, nil
}

// warnPRHeadState reports head states the preflight accepts but the caller
// should know about.
func warnPRHeadState(opts prCommandOptions, plan *prPlan) {
	head, remoteBranch := plan.Head, plan.Remote+"/"+plan.Head.Name
	if head.RemoteErr != nil {
		prWarnf("could not compare %s with %s (%v); the PR text describes the local branch and may not match what the PR will contain", head.Name, remoteBranch, head.RemoteErr)
	}
	if head.Behind > 0 {
		prWarnf("%s is %d commit(s) behind %s; the PR text describes the remote branch and nothing is pushed", head.Name, head.Behind, remoteBranch)
	}
	if !opts.push && head.Ahead > 0 {
		prWarnf("%s has %d commit(s) that %s lacks and --push=false; the PR text describes %s and leaves them out", head.Name, head.Ahead, remoteBranch, remoteBranch)
	}
}

// warnPRGitVersion says so when the installed git cannot read .gitattributes
// from the head commit. The diff then follows the working tree's attributes,
// which may belong to another branch or hold uncommitted edits.
func warnPRGitVersion() {
	if ok, installed := oneshot.HeadAttributesSupported(); !ok {
		prWarnf("the installed git (%s) cannot read .gitattributes from the head commit; it needs 2.40 or newer. The diff follows the working tree's .gitattributes, so the PR text may not match the commits", installed)
	}
}

func printPR(pr *commands.PRResult, plan *prPlan) {
	termOut.Newline()
	termOut.Header("PULL REQUEST: " + plan.summary())

	// Build content for the box
	var content strings.Builder
	content.WriteString(pr.Header())
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
	fmt.Println("Title:", pr.Header())
	fmt.Println()
	fmt.Println(pr.FormatBody())
}

// createPR creates the PR for the plan, or updates the open PR for the same
// head, and returns its URL. The lookup keys on the head branch, so the PR
// that belongs to whatever branch the worktree has checked out is never touched.
func createPR(run ghCommandRunner, pr *commands.PRResult, plan *prPlan, opts prCommandOptions) (string, error) {
	body := pr.FormatBody()
	existing, err := openPRForHead(run, plan.Head, plan.explicitBase())
	if err != nil {
		return "", err
	}
	existingURL := ""
	if existing != nil {
		existingURL = existing.URL
	}
	update, err := resolvePRUpdatePolicy(existingURL, existing != nil, opts.noUpdate)
	if err != nil {
		return "", err
	}
	if update {
		if err := updatePR(run, existingURL, pr.Header(), body); err != nil {
			return "", err
		}
		return existingURL, nil
	}

	spinner := terminal.NewSpinner("Creating PR...")
	spinner.Start()

	output, err := run(buildGhPRCreateArgs(pr.Header(), body, plan.Base.Short, plan.Head.GhHead, opts.draft)...)
	if err != nil {
		spinner.StopWithError(fmt.Sprintf("failed: %s", err))
		return "", fmt.Errorf("gh pr create failed: %w", err)
	}

	prURL := lastLine(output)
	if !prURLPattern.MatchString(prURL) {
		spinner.StopWithError("gh printed no PR URL")
		return "", fmt.Errorf("gh pr create exited 0 but its last output line is not a PR URL (%q); check whether the PR was created", prURL)
	}
	spinner.StopWithSuccess(fmt.Sprintf("PR created: %s", prURL))
	return prURL, nil
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// resolvePRUpdatePolicy decides whether createPR should update an existing
// open PR in place, create a new one, or refuse. Kept pure (no gh
// invocation) so the --no-update guard is unit-testable directly.
//
// Default (noUpdate=false) preserves the pre-existing behavior: re-running
// `buckley pr` after new commits refreshes the open PR instead of failing
// with "already exists". --no-update opts into the stricter, less
// surprising behavior of refusing to touch a PR this run did not create.
func resolvePRUpdatePolicy(existingURL string, existingFound bool, noUpdate bool) (update bool, err error) {
	if !existingFound {
		return false, nil
	}
	if noUpdate {
		return false, fmt.Errorf("an open PR already exists at %s; refusing to overwrite its title/body (--no-update)", existingURL)
	}
	return true, nil
}

// buildGhPRCreateArgs assembles the `gh pr create` argument list. head is
// passed as --head only when the caller gave one; --draft is passed through
// when requested.
func buildGhPRCreateArgs(title, body, baseBranch, head string, draft bool) []string {
	args := []string{"pr", "create",
		"--title", title,
		"--body", body,
		"--base", baseBranch,
	}
	if head != "" {
		args = append(args, "--head", head)
	}
	if draft {
		args = append(args, "--draft")
	}
	return args
}

// openPullRequest is an open PR as `gh pr list` reports it.
type openPullRequest struct {
	Number int
	URL    string
	Base   string
}

// prURLPattern matches the URL of a pull request, the last line gh pr create
// prints.
var prURLPattern = regexp.MustCompile(`^https?://\S+/pull/[0-9]+$`)

// errEmptyPRSelector stops an empty PR argument before it reaches gh, which
// reads "" as "the PR for the current branch": a PR the caller did not mean.
var errEmptyPRSelector = errors.New("no PR given: gh would read an empty PR argument as the PR for the current branch")

// openPRForHead finds the open PR whose head is the branch head.Name of the
// repository head.Owner owns. Closed and merged PRs stay untouched: a fresh
// push after a merge gets a fresh PR. It returns nil when there is none and an
// error when gh fails, so a transient failure never reads as "no PR".
//
// gh filters by branch name only, so the listing holds every open PR with that
// head name: one from the base repository and one from each fork. Only the
// head repository's owner tells them apart, so the match needs it. In a fork
// checkout gh lists the parent repository, and the caller's own PR is the one
// whose head owner is the fork. A base other than "" is the base the caller
// chose: an open PR for the head with a different base is refused rather than
// edited, because editing it would change a PR the caller did not name.
func openPRForHead(run ghCommandRunner, head prHead, base string) (*openPullRequest, error) {
	out, err := run("pr", "list", "--head", head.Name, "--state", "open",
		"--json", "number,url,baseRefName,headRefName,headRepositoryOwner")
	if err != nil {
		return nil, fmt.Errorf("look up open PRs for %s: %w", head.Name, err)
	}
	var listed []struct {
		Number              int    `json:"number"`
		URL                 string `json:"url"`
		BaseRefName         string `json:"baseRefName"`
		HeadRefName         string `json:"headRefName"`
		HeadRepositoryOwner struct {
			Login string `json:"login"`
		} `json:"headRepositoryOwner"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return nil, fmt.Errorf("decode open PRs for %s: %w", head.Name, err)
	}
	var matches []openPullRequest
	for _, pr := range listed {
		// No login means the head repository was deleted; nothing can be
		// pushed to it, so the PR cannot be ours.
		login := pr.HeadRepositoryOwner.Login
		if pr.HeadRefName != head.Name || login == "" || !strings.EqualFold(login, head.Owner) {
			continue
		}
		if pr.Number <= 0 || !prURLPattern.MatchString(pr.URL) {
			return nil, fmt.Errorf("gh listed an open PR for %s without a usable number and URL (#%d %q)", head.Name, pr.Number, pr.URL)
		}
		matches = append(matches, openPullRequest{Number: pr.Number, URL: pr.URL, Base: pr.BaseRefName})
	}
	if len(matches) == 0 {
		return nil, nil
	}
	if base != "" {
		for i := range matches {
			if matches[i].Base == base {
				return &matches[i], nil
			}
		}
		return nil, fmt.Errorf("an open PR for %s targets %s, not %s; refusing to retarget it (%s)", head.Name, matches[0].Base, base, openPRURLs(matches))
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("%s has %d open PRs (%s); pass --base to pick one", head.Name, len(matches), openPRURLs(matches))
	}
	return &matches[0], nil
}

func openPRURLs(prs []openPullRequest) string {
	parts := make([]string, len(prs))
	for i, pr := range prs {
		parts[i] = fmt.Sprintf("#%d %s", pr.Number, pr.URL)
	}
	return strings.Join(parts, ", ")
}

// updatePR regenerates the existing open PR's title and body in place,
// so re-running `buckley pr` after new commits refreshes the PR instead
// of failing with "already exists".
func updatePR(run ghCommandRunner, url, title, body string) error {
	if strings.TrimSpace(url) == "" {
		return errEmptyPRSelector
	}
	spinner := terminal.NewSpinner("Updating PR...")
	spinner.Start()

	if _, err := run("pr", "edit", url, "--title", title, "--body", body); err != nil {
		spinner.StopWithError(fmt.Sprintf("failed: %s", err))
		return fmt.Errorf("gh pr edit failed: %w", err)
	}
	spinner.StopWithSuccess(fmt.Sprintf("PR updated: %s", url))
	return nil
}
