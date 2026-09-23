package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
)

// ghCommandRunner runs one `gh` CLI invocation and returns its stdout.
// Tests inject a fake so `buckley pr merge` is testable without a real gh
// install or network access, mirroring the prCommandRunner seam already
// used by pkg/oneshot/commands/review_ci_admission.go.
type ghCommandRunner func(args ...string) ([]byte, error)

// execGhCommand is the production ghCommandRunner: it shells out to gh.
func execGhCommand(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ghAPITimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", args...).Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return out, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return out, fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

type prMergeCommandOptions struct {
	number       int
	method       string // "squash" | "merge" | "rebase" | "" (auto-detect)
	admin        bool
	deleteBranch bool
	dryRun       bool
	yes          bool
	model        string
	backend      string
	timeout      time.Duration
}

func parsePRMergeOptions(args []string) (prMergeCommandOptions, error) {
	fs := flag.NewFlagSet("pr merge", flag.ContinueOnError)
	squash := fs.Bool("squash", false, "squash the PR's commits into one commit and merge")
	mergeFlag := fs.Bool("merge", false, "merge with a two-parent merge commit")
	rebase := fs.Bool("rebase", false, "rebase the PR's commits onto the base branch")
	admin := fs.Bool("admin", false, "use administrator privileges to bypass merge requirements (never implied by other flags)")
	deleteBranch := fs.Bool("delete-branch", false, "delete the local and remote branch after merge")
	dryRun := fs.Bool("dry-run", false, "print the generated merge title and body without merging")
	yes := fs.Bool("yes", false, "skip confirmation and merge")
	modelFlag := fs.String("model", "", "model to use for the merge message (default: BUCKLEY_MODEL_PR or models.utility.pr)")
	backendFlag := fs.String("backend", "", "backend to use: api, codex, or claude")
	timeout := fs.Duration("timeout", 2*time.Minute, "timeout for model request")

	if err := fs.Parse(args); err != nil {
		return prMergeCommandOptions{}, err
	}

	chosen := 0
	method := ""
	for flagVal, name := range map[*bool]string{squash: "squash", mergeFlag: "merge", rebase: "rebase"} {
		if *flagVal {
			chosen++
			method = name
		}
	}
	if chosen > 1 {
		return prMergeCommandOptions{}, fmt.Errorf("pass at most one of --squash, --merge, --rebase")
	}

	if fs.NArg() < 1 {
		return prMergeCommandOptions{}, fmt.Errorf("usage: buckley pr merge <number> [--squash|--merge|--rebase] [--admin] [--delete-branch]")
	}
	number, err := strconv.Atoi(fs.Arg(0))
	if err != nil {
		return prMergeCommandOptions{}, fmt.Errorf("invalid PR number %q: %w", fs.Arg(0), err)
	}

	backend, err := resolveOneshotBackend("pr", *backendFlag)
	if err != nil {
		return prMergeCommandOptions{}, err
	}

	return prMergeCommandOptions{
		number:       number,
		method:       method,
		admin:        *admin,
		deleteBranch: *deleteBranch,
		dryRun:       *dryRun,
		yes:          *yes,
		model:        *modelFlag,
		backend:      backend,
		timeout:      *timeout,
	}, nil
}

// prMergeCheck is one entry from `gh pr checks --json name,state,bucket`.
type prMergeCheck struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Bucket string `json:"bucket"`
}

// fetchPRChecks fetches the PR's checks. `gh pr checks` exits nonzero when
// any check is failing or still pending even though it has already written
// a valid JSON payload (observed in pkg/oneshot/commands/review_pr_context.go);
// the JSON is trusted whenever it parses, and the exec error is only
// surfaced when there is nothing to parse.
func fetchPRChecks(run ghCommandRunner, number int) ([]prMergeCheck, error) {
	out, err := run("pr", "checks", strconv.Itoa(number), "--json", "name,state,bucket")
	var checks []prMergeCheck
	if jsonErr := json.Unmarshal(out, &checks); jsonErr != nil {
		if err != nil {
			return nil, fmt.Errorf("fetch PR checks: %w", err)
		}
		return nil, fmt.Errorf("decode PR checks: %w", jsonErr)
	}
	return checks, nil
}

// failingOrPendingChecks returns every check that is not passing, skipping,
// or cancelled (which are not merge blockers).
func failingOrPendingChecks(checks []prMergeCheck) []prMergeCheck {
	var bad []prMergeCheck
	for _, c := range checks {
		switch strings.ToLower(c.Bucket) {
		case "pass", "skipping", "cancel":
			continue
		default:
			bad = append(bad, c)
		}
	}
	return bad
}

func formatChecks(checks []prMergeCheck) []string {
	lines := make([]string, 0, len(checks))
	for _, c := range checks {
		lines = append(lines, fmt.Sprintf("%s: %s", c.Name, c.State))
	}
	return lines
}

// repoMergeSettings mirrors the merge-method fields from `gh repo view`.
type repoMergeSettings struct {
	SquashMergeAllowed bool `json:"squashMergeAllowed"`
	MergeCommitAllowed bool `json:"mergeCommitAllowed"`
	RebaseMergeAllowed bool `json:"rebaseMergeAllowed"`
}

func fetchRepoMergeSettings(run ghCommandRunner) (repoMergeSettings, error) {
	out, err := run("repo", "view", "--json", "squashMergeAllowed,mergeCommitAllowed,rebaseMergeAllowed")
	if err != nil {
		return repoMergeSettings{}, fmt.Errorf("fetch repo merge settings: %w", err)
	}
	var settings repoMergeSettings
	if err := json.Unmarshal(out, &settings); err != nil {
		return repoMergeSettings{}, fmt.Errorf("decode repo merge settings: %w", err)
	}
	return settings, nil
}

// resolveMergeMethod honors an explicit --squash/--merge/--rebase choice
// (refusing if the repo does not allow it), or auto-detects the repo's
// preferred method: squash, then merge, then rebase.
func resolveMergeMethod(explicit string, settings repoMergeSettings) (string, error) {
	if explicit != "" {
		allowed := map[string]bool{
			"squash": settings.SquashMergeAllowed,
			"merge":  settings.MergeCommitAllowed,
			"rebase": settings.RebaseMergeAllowed,
		}
		if !allowed[explicit] {
			return "", fmt.Errorf("%s merges are not allowed on this repository", explicit)
		}
		return explicit, nil
	}
	switch {
	case settings.SquashMergeAllowed:
		return "squash", nil
	case settings.MergeCommitAllowed:
		return "merge", nil
	case settings.RebaseMergeAllowed:
		return "rebase", nil
	}
	return "", fmt.Errorf("no merge method is allowed on this repository")
}

// prMergeInfo is the PR data needed to generate a merge/squash message.
type prMergeInfo struct {
	Title   string
	Body    string
	Base    string
	Head    string
	Commits []string
}

func fetchPRMergeInfo(run ghCommandRunner, number int) (prMergeInfo, error) {
	out, err := run("pr", "view", strconv.Itoa(number), "--json", "title,body,baseRefName,headRefName,commits")
	if err != nil {
		return prMergeInfo{}, fmt.Errorf("fetch PR info: %w", err)
	}
	var payload struct {
		Title       string `json:"title"`
		Body        string `json:"body"`
		BaseRefName string `json:"baseRefName"`
		HeadRefName string `json:"headRefName"`
		Commits     []struct {
			MessageHeadline string `json:"messageHeadline"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return prMergeInfo{}, fmt.Errorf("decode PR info: %w", err)
	}
	info := prMergeInfo{Title: payload.Title, Body: payload.Body, Base: payload.BaseRefName, Head: payload.HeadRefName}
	for _, c := range payload.Commits {
		if strings.TrimSpace(c.MessageHeadline) != "" {
			info.Commits = append(info.Commits, c.MessageHeadline)
		}
	}
	return info, nil
}

const prMergeDiffMaxBytes = 60_000

func fetchPRDiff(run ghCommandRunner, number int) (string, error) {
	out, err := run("pr", "diff", strconv.Itoa(number))
	if err != nil {
		return "", fmt.Errorf("fetch PR diff: %w", err)
	}
	diff := string(out)
	if len(diff) > prMergeDiffMaxBytes {
		diff = diff[:prMergeDiffMaxBytes] + "\n... (truncated)"
	}
	return diff, nil
}

// prMergeCommitDefinition wraps commands.CommitDefinition to generate a
// merge/squash commit message from a PR's title, description, commits, and
// diff instead of a staged local diff. ContextSources is overridden to
// nothing: buckley pr merge may run against a PR that is not the current
// checkout, so every fact comes from the gh-fetched data in BuildPrompt.
type prMergeCommitDefinition struct {
	commands.CommitDefinition
	prNumber               int
	prTitle, prBody        string
	baseBranch, headBranch string
	subjects               []string
	diff                   string
}

func (prMergeCommitDefinition) ContextSources() []oneshot.ContextSource { return nil }

func (d prMergeCommitDefinition) BuildPrompt(*oneshot.Context) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Pull Request #%d: %s into %s\n\n", d.prNumber, d.headBranch, d.baseBranch)
	if title := strings.TrimSpace(d.prTitle); title != "" {
		b.WriteString("PR title: " + title + "\n\n")
	}
	if body := strings.TrimSpace(d.prBody); body != "" {
		b.WriteString("## PR description\n\n" + body + "\n\n")
	}
	if len(d.subjects) > 0 {
		b.WriteString("## Commits in this PR\n\n")
		for _, s := range d.subjects {
			b.WriteString("- " + s + "\n")
		}
		b.WriteString("\n")
	}
	if diff := strings.TrimSpace(d.diff); diff != "" {
		b.WriteString("## Diff\n\n```diff\n" + diff + "\n```\n\n")
	}
	b.WriteString("Call generate_commit with the merge commit's action, optional scope, subject, and body summarizing this PR.")
	return b.String()
}

// renderPRMergeMessageWithRunner generates the merge/squash title and body
// with runner. Split from generatePRMergeMessage so tests can inject a
// fake commitRunner instead of a live model backend.
func renderPRMergeMessageWithRunner(ctx context.Context, runner commitRunner) (subject, body string, err error) {
	result, err := runCommitGeneration(ctx, runner)
	if err != nil {
		return "", "", err
	}
	if result.Error != nil {
		return "", "", result.Error
	}
	if result.Commit == nil {
		return "", "", fmt.Errorf("no merge message generated")
	}

	var bodyLines []string
	for _, bullet := range result.Commit.Body {
		if b := commitmsg.NormalizeBullet(bullet); b != "" {
			bodyLines = append(bodyLines, "- "+commitmsg.NeutralizeCloseDirectives(b))
		}
	}
	return result.Commit.Header(), strings.Join(bodyLines, "\n"), nil
}

// generatePRMergeMessage builds the model runtime for a PR's merge message
// and generates it. Not used in tests directly (it does full dependency
// init); tests exercise renderPRMergeMessageWithRunner instead.
func generatePRMergeMessage(opts prMergeCommandOptions, info prMergeInfo, diff string) (subject, body string, err error) {
	def := prMergeCommitDefinition{
		prNumber: opts.number, prTitle: info.Title, prBody: info.Body,
		baseBranch: info.Base, headBranch: info.Head, subjects: info.Commits, diff: diff,
	}
	commitOpts := commitCommandOptions{backend: opts.backend, model: opts.model, timeout: opts.timeout, showCost: true}
	runtime, cleanup, err := newCommitCommandRuntime(commitOpts, def)
	defer cleanup()
	if err != nil {
		return "", "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	if !quietMode {
		termOut.Dim("Using %s", describeOneshotBackend(runtime.backend, runtime.modelID))
	}
	return renderPRMergeMessageWithRunner(ctx, runtime.runner)
}

// buildGhPRMergeArgs assembles `gh pr merge` arguments. --subject/--body
// are only accepted by gh for squash and merge methods: a rebase preserves
// each original commit message, so gh rejects them there and they are
// omitted.
func buildGhPRMergeArgs(number int, method, subject, body string, admin, deleteBranch bool) []string {
	args := []string{"pr", "merge", strconv.Itoa(number)}
	switch method {
	case "squash":
		args = append(args, "--squash")
	case "merge":
		args = append(args, "--merge")
	case "rebase":
		args = append(args, "--rebase")
	}
	if method != "rebase" {
		if subject != "" {
			args = append(args, "--subject", subject)
		}
		if body != "" {
			args = append(args, "--body", body)
		}
	}
	if admin {
		args = append(args, "--admin")
	}
	if deleteBranch {
		args = append(args, "--delete-branch")
	}
	return args
}

func confirmPRMerge(number int, method string) error {
	if !stdinIsTerminalFn() {
		return fmt.Errorf("refusing to merge without confirmation in non-interactive mode (use --dry-run or --yes)")
	}
	fmt.Printf("\nMerge PR #%d with %s? [y/N] ", number, method)
	var response string
	fmt.Scanln(&response)
	response = strings.ToLower(strings.TrimSpace(response))
	if response != "y" && response != "yes" {
		return fmt.Errorf("aborted")
	}
	return nil
}

// runPRMergeCommand implements `buckley pr merge <n> [--squash|--merge|--rebase] [--admin] [--delete-branch]`.
func runPRMergeCommand(args []string) error {
	opts, err := parsePRMergeOptions(args)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh CLI not found (install from https://cli.github.com)")
	}
	return prMergeWithRunner(opts, execGhCommand)
}

// prMergeWithRunner is the injectable core of runPRMergeCommand: given a
// ghCommandRunner, it checks required checks, resolves the merge method,
// generates the title/body (skipped for rebase), confirms, and merges.
func prMergeWithRunner(opts prMergeCommandOptions, run ghCommandRunner) error {
	checks, err := fetchPRChecks(run, opts.number)
	if err != nil {
		return err
	}
	if bad := failingOrPendingChecks(checks); len(bad) > 0 {
		return fmt.Errorf("refusing to merge PR #%d: %d required check(s) are not passing:\n  %s",
			opts.number, len(bad), strings.Join(formatChecks(bad), "\n  "))
	}

	settings, err := fetchRepoMergeSettings(run)
	if err != nil {
		return err
	}
	method, err := resolveMergeMethod(opts.method, settings)
	if err != nil {
		return err
	}

	info, err := fetchPRMergeInfo(run, opts.number)
	if err != nil {
		return err
	}

	var subject, body string
	if method != "rebase" {
		diff, err := fetchPRDiff(run, opts.number)
		if err != nil {
			return err
		}
		subject, body, err = generatePRMergeMessage(opts, info, diff)
		if err != nil {
			return err
		}
	}

	if !quietMode {
		termOut.Newline()
		termOut.Header(fmt.Sprintf("MERGE PR #%d (%s)", opts.number, method))
		if subject != "" {
			fmt.Println(subject)
			fmt.Println()
			fmt.Println(body)
		}
	}

	if opts.dryRun {
		return nil
	}
	if !opts.yes {
		if err := confirmPRMerge(opts.number, method); err != nil {
			return err
		}
	}

	mergeArgs := buildGhPRMergeArgs(opts.number, method, subject, body, opts.admin, opts.deleteBranch)
	if _, err := run(mergeArgs...); err != nil {
		return fmt.Errorf("gh pr merge failed: %w", err)
	}
	fmt.Printf("Merged PR #%d (%s)\n", opts.number, method)
	return nil
}
