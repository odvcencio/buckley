package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/prompts"
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
	// noConflicts is true only when git's MERGE_MSG was read and lists no
	// conflicts. When the file is missing the prompt says nothing about conflicts.
	noConflicts bool
}

func (d mergeCommitDefinition) BuildPrompt(ctx *oneshot.Context) string {
	var b strings.Builder
	b.WriteString(prompts.MergeNoteProseBlock() + "\n\n")
	fmt.Fprintf(&b, "## Merge\n\nMerging %s into %s.\n\n", d.source, d.target)
	fmt.Fprintf(&b, "Buckley writes the header itself: \"merge(<scope>): Merge %s into %s\". Supply the scope and the body. The action and the subject you send are not used.\n\n", d.source, d.target)
	if len(d.subjects) > 0 {
		b.WriteString("## Incoming commits\n\n")
		for _, s := range d.subjects {
			b.WriteString("- " + s + "\n")
		}
		b.WriteString("\n")
	}
	if len(d.resolutions) > 0 {
		b.WriteString("## Conflict resolutions\n\nState each of these in the body, in plain language:\n\n")
		for _, r := range d.resolutions {
			b.WriteString("- " + r + "\n")
		}
		b.WriteString("\n")
	} else if d.noConflicts {
		// Without this line a model that is told "Merging A into B" invented
		// a conflict and its resolution (live check, 10 November 2026). It is
		// said only when git's record was read: missing information is not
		// a clean merge.
		b.WriteString("## Conflicts\n\nThere were no conflicts in this merge. Do not mention conflicts or how they were resolved.\n\n")
	}
	b.WriteString(d.CommitDefinition.BuildPrompt(ctx))
	return b.String()
}

// unusedMergeSubject stands in for the model's subject when a merge result is
// checked. It passes every subject rule, and it is too short for the
// removed-name check to look at.
const unusedMergeSubject = "x"

// forChecks returns the tool arguments as the merge commit uses them: the
// scope, body and issues the model sent, under the fixed action.
//
// completeMergeWithRunner replaces the action and the subject after
// generation, so the checks must not judge what the model wrote there. Told
// "Merging A into B", a model answers with the action "merge" and git's own
// subject "Merge A into B". As a generated subject that breaks three rules at
// once: a capital first letter, a repeated action verb, and with a long branch
// name a header over 72 characters. The run then failed after three attempts
// on text that was never going to be committed, and passed on a later try
// when the model happened to word the subject differently.
func (d mergeCommitDefinition) forChecks(raw json.RawMessage) json.RawMessage {
	return d.withFixedHeader(raw, unusedMergeSubject)
}

// forLeakChecks returns the arguments with the header that is committed: the
// action "merge" and the subject "Merge <source> into <target>". Branch names
// can hold an email address or a private term, and Buckley inserts them into
// the header after generation, so the leak checks must read them.
func (d mergeCommitDefinition) forLeakChecks(raw json.RawMessage) json.RawMessage {
	return d.withFixedHeader(raw, fmt.Sprintf("Merge %s into %s", d.source, d.target))
}

func (d mergeCommitDefinition) withFixedHeader(raw json.RawMessage, subject string) json.RawMessage {
	var fields map[string]json.RawMessage
	// A tool call whose arguments are "null" decodes without error to a nil
	// map; assigning to it would panic. Leave it for validation to reject.
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return raw
	}
	fields["action"], _ = json.Marshal("merge")
	fields["subject"], _ = json.Marshal(subject)
	out, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return out
}

func (d mergeCommitDefinition) Validate(raw json.RawMessage) error {
	return d.CommitDefinition.Validate(d.forChecks(raw))
}

func (d mergeCommitDefinition) Repair(raw json.RawMessage) (json.RawMessage, []string) {
	return d.CommitDefinition.Repair(d.forChecks(raw))
}

func (d mergeCommitDefinition) ValidateWithContext(ctx *oneshot.Context, raw json.RawMessage) error {
	return d.CommitDefinition.ValidateWithContext(ctx, d.forLeakChecks(raw))
}

func (d mergeCommitDefinition) PolicyFacts(ctx *oneshot.Context, raw json.RawMessage) (*oneshot.PolicyRequest, error) {
	return d.CommitDefinition.PolicyFacts(ctx, d.forLeakChecks(raw))
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

// mergeConflictFilesKnown parses the "# Conflicts:" section git appends to
// MERGE_MSG (or REVERT_HEAD's MERGE_MSG) when a merge/revert/cherry-pick
// stops on conflicts. Format (observed, git 2.x):
//
//	<subject>
//
//	# Conflicts:
//	#	path/one
//	#	path/two
//
// conflictsHeaderRe finds git's "Conflicts:" line under any comment character
// (core.commentChar can be "#", ";", "@" and others).
var conflictsHeaderRe = regexp.MustCompile(`^(\S)\s?Conflicts:\s*$`)

// mergeConflictFilesKnown is mergeConflictFiles plus whether the answer is
// known. known is false when MERGE_MSG is missing or empty, so that a missing
// record is never read as a merge without conflicts.
func mergeConflictFilesKnown(gitDir string) (files []string, known bool) {
	msg := readGitDirFile(gitDir, "MERGE_MSG")
	if strings.TrimSpace(msg) == "" {
		return nil, false
	}
	comment := ""
	for _, line := range strings.Split(msg, "\n") {
		trimmed := strings.TrimRight(line, "\r")
		if comment == "" {
			if m := conflictsHeaderRe.FindStringSubmatch(trimmed); m != nil {
				comment = m[1]
			}
			continue
		}
		if !strings.HasPrefix(trimmed, comment) {
			break
		}
		path := strings.TrimSpace(strings.TrimPrefix(trimmed, comment))
		if path == "" {
			continue
		}
		files = append(files, path)
	}
	return files, true
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
	conflictFiles, conflictsKnown := mergeConflictFilesKnown(state.GitDir)
	resolutions := analyzeConflictResolutions(conflictFiles, "HEAD", state.MergeHead)

	def := mergeCommitDefinition{source: source, target: target, subjects: subjects, resolutions: resolutions,
		noConflicts: conflictsKnown && len(conflictFiles) == 0}

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
	runner = mergeHeaderRunner{commitRunner: runner, source: source, target: target}
	result, err := runCommitGeneration(ctx, runner, opts)
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

// mergeHeaderRunner gives every message of the wrapped runner the fixed merge
// header. The header was set once, after the first generation, so a message
// regenerated at the confirmation prompt ([r]) kept the model's own action and
// subject and the merge was committed without the merge grammar.
type mergeHeaderRunner struct {
	commitRunner
	source, target string
}

func (r mergeHeaderRunner) Run(ctx context.Context) (*commitRunResult, error) {
	result, err := r.commitRunner.Run(ctx)
	if result != nil && result.Commit != nil {
		result.Commit.Action = "merge"
		result.Commit.Subject = fmt.Sprintf("Merge %s into %s", r.source, r.target)
	}
	return result, err
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
