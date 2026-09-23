package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/transparency"
)

// repoOpKind identifies which VCS operation, if any, is in progress in the
// working tree. `buckley commit` uses this to complete the operation with a
// generated message instead of falling back to a normal commit (or, worse,
// telling the caller to run plain `git commit`).
type repoOpKind int

const (
	opNone repoOpKind = iota
	opMerge
	opCherryPick
	opRevert
	opRebase
	// opMergeSquash is `git merge --squash`: SQUASH_MSG is present but no
	// MERGE_HEAD exists, since --squash never records a merge parent.
	opMergeSquash
)

// repoOpState is the detected in-progress operation and the refs it names.
type repoOpState struct {
	Kind           repoOpKind
	GitDir         string
	MergeHead      string
	CherryPickHead string
	RevertHead     string
}

// errRebaseInProgress is returned when a rebase is in progress. buckley
// deliberately does not touch rebase state: an interactive rebase step can
// be mid-edit, mid-squash, or mid-conflict in ways that are unsafe to
// generalize a commit message for. The user completes it with plain git.
var errRebaseInProgress = errors.New("a rebase is in progress; resolve conflicts, `git add` the result, then run `git rebase --continue` (buckley does not drive rebases)")

// detectRepoOpState inspects the git directory for MERGE_HEAD,
// CHERRY_PICK_HEAD, REVERT_HEAD, an in-progress rebase, or a prepared
// `git merge --squash` message, in that precedence order.
func detectRepoOpState() (repoOpState, error) {
	out, err := gitOutput("rev-parse", "--git-dir")
	if err != nil {
		return repoOpState{}, fmt.Errorf("resolve git dir: %w", err)
	}
	gitDir := out
	if !filepath.IsAbs(gitDir) {
		cwd, err := os.Getwd()
		if err != nil {
			return repoOpState{}, fmt.Errorf("resolve working directory: %w", err)
		}
		gitDir = filepath.Join(cwd, gitDir)
	}

	exists := func(name string) bool {
		_, statErr := os.Stat(filepath.Join(gitDir, name))
		return statErr == nil
	}

	switch {
	case exists("MERGE_HEAD"):
		return repoOpState{Kind: opMerge, GitDir: gitDir, MergeHead: readGitDirFile(gitDir, "MERGE_HEAD")}, nil
	case exists("CHERRY_PICK_HEAD"):
		return repoOpState{Kind: opCherryPick, GitDir: gitDir, CherryPickHead: readGitDirFile(gitDir, "CHERRY_PICK_HEAD")}, nil
	case exists("REVERT_HEAD"):
		return repoOpState{Kind: opRevert, GitDir: gitDir, RevertHead: readGitDirFile(gitDir, "REVERT_HEAD")}, nil
	case exists("rebase-merge"), exists("rebase-apply"):
		return repoOpState{Kind: opRebase, GitDir: gitDir}, nil
	case exists("SQUASH_MSG"):
		return repoOpState{Kind: opMergeSquash, GitDir: gitDir}, nil
	default:
		return repoOpState{Kind: opNone, GitDir: gitDir}, nil
	}
}

// readGitDirFile reads a file under the git directory and trims it. Returns
// "" if it cannot be read; callers treat a missing marker file as "no ref".
func readGitDirFile(gitDir, name string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// unmergedPaths returns the paths git still considers unmerged (conflict
// markers may or may not remain; what matters is the index stage).
func unmergedPaths() ([]string, error) {
	out, err := gitOutput("diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return nil, fmt.Errorf("list unmerged paths: %w", err)
	}
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// refuseIfUnmerged errors out, listing every remaining unmerged path, when
// conflicts have not all been resolved and staged.
func refuseIfUnmerged() error {
	paths, err := unmergedPaths()
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	return fmt.Errorf("unresolved conflicts remain in %d file(s):\n  %s\nResolve them, stage the result (git add), then run buckley commit again",
		len(paths), strings.Join(paths, "\n  "))
}

// mergeCommitDefinition wraps commands.CommitDefinition, layering the
// incoming commit subjects and conflict-resolution facts onto the prompt.
// Diff and file context still come from the staged index (inherited
// ContextSources), which after conflict resolution is the merged tree
// versus HEAD — useful context for the "why" the model writes about.
//
// The model only chooses the scope and body; the caller overwrites Action
// and Subject afterward to the fixed "Merge <source> into <target>"
// grammar, so generation cannot drift from the repo's merge-commit format.
type mergeCommitDefinition struct {
	commands.CommitDefinition
	source, target string
	subjects       []string
	resolutions    []string
}

func (d mergeCommitDefinition) BuildPrompt(ctx *oneshot.Context) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Merge\n\nMerging %s into %s.\n\n", d.source, d.target)
	if len(d.subjects) > 0 {
		b.WriteString("## Incoming commits\n\n")
		for _, s := range d.subjects {
			b.WriteString("- " + s + "\n")
		}
		b.WriteString("\n")
	}
	if len(d.resolutions) > 0 {
		b.WriteString("## Conflict resolutions\n\nState each of these in the body, in your own ASD-STE100 prose:\n\n")
		for _, r := range d.resolutions {
			b.WriteString("- " + r + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(d.CommitDefinition.BuildPrompt(ctx))
	return b.String()
}

// squashMsgCommitDefinition wraps commands.CommitDefinition, adding the
// content of a prepared `git merge --squash` SQUASH_MSG as extra context so
// the model can write one commit message that reflects every squashed
// commit, not just the combined diff.
type squashMsgCommitDefinition struct {
	commands.CommitDefinition
	squashMsg string
}

func (d squashMsgCommitDefinition) BuildPrompt(ctx *oneshot.Context) string {
	var b strings.Builder
	if msg := strings.TrimSpace(d.squashMsg); msg != "" {
		b.WriteString("## Squashed commits (from `git merge --squash`)\n\n```\n")
		b.WriteString(msg)
		b.WriteString("\n```\n\n")
	}
	b.WriteString(d.CommitDefinition.BuildPrompt(ctx))
	return b.String()
}

// mergeMsgSourceName returns the friendliest available name for the
// incoming ref: the branch/ref name git resolved (name-rev), falling back
// to a short SHA when the merge head is detached or unresolvable.
func mergeMsgSourceName(mergeHead string) string {
	if mergeHead == "" {
		return "the incoming branch"
	}
	if name, err := gitOutput("name-rev", "--name-only", mergeHead); err == nil {
		name = strings.TrimSpace(name)
		if name != "" && name != "undefined" {
			return name
		}
	}
	if len(mergeHead) > 12 {
		return mergeHead[:12]
	}
	return mergeHead
}

// commitSubjectsBetween returns commit subjects reachable from `to` but not
// `from`, oldest first, condensed to a sane cap so the prompt stays small.
func commitSubjectsBetween(from, to string) ([]string, error) {
	out, err := gitOutput("log", "--reverse", "--format=%s", from+".."+to)
	if err != nil {
		return nil, fmt.Errorf("list commits %s..%s: %w", from, to, err)
	}
	if out == "" {
		return nil, nil
	}
	subjects := strings.Split(out, "\n")
	const maxSubjects = 25
	if len(subjects) > maxSubjects {
		extra := len(subjects) - maxSubjects
		subjects = subjects[:maxSubjects]
		subjects = append(subjects, fmt.Sprintf("... and %d more commit(s)", extra))
	}
	return subjects, nil
}

// mergeConflictFiles parses the "# Conflicts:" section git appends to
// MERGE_MSG (or REVERT_HEAD's MERGE_MSG) when a merge/revert/cherry-pick
// stops on conflicts. Format (observed, git 2.x):
//
//	<subject>
//
//	# Conflicts:
//	#	path/one
//	#	path/two
func mergeConflictFiles(gitDir string) []string {
	msg := readGitDirFile(gitDir, "MERGE_MSG")
	if msg == "" {
		return nil
	}
	lines := strings.Split(msg, "\n")
	var files []string
	inSection := false
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if strings.HasPrefix(trimmed, "# Conflicts:") {
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		if !strings.HasPrefix(trimmed, "#") {
			break
		}
		path := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		if path == "" {
			continue
		}
		files = append(files, path)
	}
	return files
}

// analyzeConflictResolutions compares each conflicted file's resolved
// (staged) blob against the "ours" (target) and "theirs" (source) blobs to
// describe, in structured facts, how the conflict was resolved. It never
// needs the original conflict markers: the resolution is fully determined
// by comparing the three blobs after the fact.
func analyzeConflictResolutions(files []string, target, source string) []string {
	var facts []string
	for _, path := range files {
		facts = append(facts, describeConflictResolution(path, target, source))
	}
	return facts
}

func describeConflictResolution(path, target, source string) string {
	resolved, resolvedErr := gitOutput("show", ":"+path)
	ours, oursErr := gitOutput("show", target+":"+path)
	theirs, theirsErr := gitOutput("show", source+":"+path)

	switch {
	case oursErr != nil && theirsErr == nil:
		return fmt.Sprintf("%s: added by the incoming side (did not exist on %s); kept the incoming version", path, target)
	case theirsErr != nil && oursErr == nil:
		return fmt.Sprintf("%s: added by the current branch (did not exist on the incoming side); kept the current branch's version", path)
	case resolvedErr != nil:
		return fmt.Sprintf("%s: conflict resolved by deleting the file", path)
	case oursErr == nil && resolved == ours:
		return fmt.Sprintf("%s: kept the current branch's version", path)
	case theirsErr == nil && resolved == theirs:
		return fmt.Sprintf("%s: kept the incoming version", path)
	default:
		return fmt.Sprintf("%s: combined the current branch's and the incoming version by hand", path)
	}
}

// runCompleteMerge finishes an in-progress `git merge` with a generated
// two-parent commit. Called instead of a normal commit when MERGE_HEAD
// exists.
func runCompleteMerge(opts commitCommandOptions, state repoOpState) error {
	if err := refuseIfUnmerged(); err != nil {
		return err
	}

	target, err := currentBranchName()
	if err != nil {
		return fmt.Errorf("resolve current branch: %w", err)
	}
	source := mergeMsgSourceName(state.MergeHead)

	mergeBase, err := gitOutput("merge-base", "HEAD", state.MergeHead)
	if err != nil {
		return fmt.Errorf("resolve merge base: %w", err)
	}
	subjects, err := commitSubjectsBetween(mergeBase, state.MergeHead)
	if err != nil {
		return err
	}
	conflictFiles := mergeConflictFiles(state.GitDir)
	resolutions := analyzeConflictResolutions(conflictFiles, "HEAD", state.MergeHead)

	def := mergeCommitDefinition{source: source, target: target, subjects: subjects, resolutions: resolutions}

	runtime, cleanup, err := newCommitCommandRuntime(opts, def)
	defer cleanup()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	if !quietMode {
		termOut.Dim("Using %s", describeOneshotBackend(runtime.backend, runtime.modelID))
	}

	return completeMergeWithRunner(opts, ctx, runtime.runner, runtime.ledger, source, target)
}

// completeMergeWithRunner generates the merge commit message with runner,
// forces the fixed "Merge <source> into <target>" grammar, confirms, and
// commits. Split out from runCompleteMerge so tests can inject a fake
// commitRunner instead of a live model backend, matching the rest of the
// package's dependency-injection style (commitRunner is already an
// interface for this purpose; see frameworkCommitRunner in commit.go).
func completeMergeWithRunner(opts commitCommandOptions, ctx context.Context, runner commitRunner, ledger *transparency.CostLedger, source, target string) error {
	result, err := runCommitGeneration(ctx, runner)
	if err != nil {
		return err
	}
	if result.Error != nil {
		printError(result.Error, result.Trace)
		return result.Error
	}
	if result.Commit == nil {
		return fmt.Errorf("no merge commit message generated")
	}

	result.Commit.Action = "merge"
	result.Commit.Subject = fmt.Sprintf("Merge %s into %s", source, target)
	message := result.Commit.Format()

	printCommitMessage(message)
	if opts.showCost && result.Trace != nil {
		printCost(result.Trace, ledger)
	}

	if opts.dryRun {
		return nil
	}

	message, err = confirmCommitMessage(opts, message, runner, ctx, ledger, commitmsg.ChangeMetadata{})
	if err != nil {
		return err
	}

	// Safety recheck: the confirm loop (edit/regenerate) takes real time,
	// and conflicts could in principle still be unresolved in another
	// terminal. Refuse rather than commit a half-resolved merge.
	if err := refuseIfUnmerged(); err != nil {
		return err
	}

	if err := createCommit(message, opts.compactOutput, false, nil); err != nil {
		printStagedIndexOnError()
		return err
	}
	if opts.push {
		return pushChanges(opts.compactOutput, false)
	}
	return nil
}

// runCompleteCherryPick finishes an in-progress `git cherry-pick` after
// conflicts. No model call is needed: the original commit already has a
// message, and buckley's job is only to preserve it (plus the "(cherry
// picked from commit X)" reference) instead of falling back to plain git.
func runCompleteCherryPick(opts commitCommandOptions, state repoOpState) error {
	if err := refuseIfUnmerged(); err != nil {
		return err
	}
	fullSHA, err := gitOutput("rev-parse", state.CherryPickHead)
	if err != nil {
		return fmt.Errorf("resolve CHERRY_PICK_HEAD: %w", err)
	}
	original, err := gitOutput("log", "-1", "--format=%B", fullSHA)
	if err != nil {
		return fmt.Errorf("read original commit message: %w", err)
	}
	message := ensureReferenceTrailer(original, fmt.Sprintf("(cherry picked from commit %s)", fullSHA))
	return completeGeneratedOp(opts, message)
}

// runCompleteRevert finishes an in-progress `git revert` after conflicts,
// preserving git's default revert message grammar.
func runCompleteRevert(opts commitCommandOptions, state repoOpState) error {
	if err := refuseIfUnmerged(); err != nil {
		return err
	}
	fullSHA, err := gitOutput("rev-parse", state.RevertHead)
	if err != nil {
		return fmt.Errorf("resolve REVERT_HEAD: %w", err)
	}
	subject, err := gitOutput("log", "-1", "--format=%s", fullSHA)
	if err != nil {
		return fmt.Errorf("read original commit subject: %w", err)
	}
	message := fmt.Sprintf("Revert %q\n\nThis reverts commit %s.\n", subject, fullSHA)
	return completeGeneratedOp(opts, message)
}

// ensureReferenceTrailer appends trailer to message unless it is already
// present, matching how git records cherry-pick provenance with `-x`.
func ensureReferenceTrailer(message, trailer string) string {
	if strings.Contains(message, trailer) {
		return strings.TrimRight(message, "\n") + "\n"
	}
	return strings.TrimRight(message, "\n") + "\n\n" + trailer + "\n"
}

// completeGeneratedOp prints/confirms a deterministically-built message and
// commits it, sharing the confirm-and-commit tail with the merge path minus
// the regenerate option (there is nothing to regenerate: the message is
// fixed by the original commit being cherry-picked or reverted).
func completeGeneratedOp(opts commitCommandOptions, message string) error {
	printCommitMessage(message)
	if opts.dryRun {
		return nil
	}
	if !opts.yes {
		if !stdinIsTerminalFn() {
			return fmt.Errorf("refusing to commit without confirmation in non-interactive mode (use --dry-run or --yes)")
		}
		fmt.Print("\n[y] Commit  [n] Abort: ")
		var response string
		fmt.Scanln(&response)
		response = strings.ToLower(strings.TrimSpace(response))
		if response != "y" && response != "yes" {
			return fmt.Errorf("aborted")
		}
	}
	if err := refuseIfUnmerged(); err != nil {
		return err
	}
	if err := createCommit(message, opts.compactOutput, false, nil); err != nil {
		printStagedIndexOnError()
		return err
	}
	if opts.push {
		return pushChanges(opts.compactOutput, false)
	}
	return nil
}

// currentBranchName resolves the current checkout's branch name, erroring
// on a detached HEAD (merge continuation needs a name for the message).
func currentBranchName() (string, error) {
	name, err := gitOutput("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if name == "" || name == "HEAD" {
		return "", fmt.Errorf("current checkout is detached; buckley commit needs a branch name to complete the merge")
	}
	return name, nil
}
