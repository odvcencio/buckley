package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/commitmsg"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/transparency"
)

// protectedBranches are refused for --squash unless the caller passes
// --force. Squashing rewrites history: doing it to a shared integration
// branch by accident is exactly the mistake this guard exists to catch.
var protectedBranches = []string{"main", "master"}

func isProtectedBranch(branch string) bool {
	for _, p := range protectedBranches {
		if branch == p {
			return true
		}
	}
	return false
}

// squashRangeCommitDefinition wraps commands.CommitDefinition, adding the
// condensed subjects of the commits being squashed onto the prompt. Diff
// and file context still come from the staged index (inherited
// ContextSources); a soft reset to the squash base makes that diff
// identical to the base..HEAD range being squashed.
type squashRangeCommitDefinition struct {
	commands.CommitDefinition
	subjects []string
}

func (d squashRangeCommitDefinition) BuildPrompt(ctx *oneshot.Context) string {
	var b strings.Builder
	if len(d.subjects) > 0 {
		b.WriteString("## Commits being squashed (oldest first)\n\n")
		for _, s := range d.subjects {
			b.WriteString("- " + s + "\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(d.CommitDefinition.BuildPrompt(ctx))
	return b.String()
}

// workingTreeClean reports whether the tracked working tree and index have
// no uncommitted changes. Untracked files are ignored: they are untouched
// by `git reset --soft` and are not "unrelated changes" in the sense the
// --squash guard cares about.
func workingTreeClean() (bool, error) {
	out, err := gitOutput("status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return false, fmt.Errorf("check working tree: %w", err)
	}
	return out == "", nil
}

// squashResetOutcome records the state after prepareSquashReset has
// soft-reset the branch, for the caller to generate a message and commit.
type squashResetOutcome struct {
	Branch   string
	Subjects []string
	OrigHead string
}

// prepareSquashReset validates the --squash guards (clean tree, branch not
// protected unless --force, a real range to squash) and performs the soft
// reset to the merge-base with opts.squashBase. It never generates a
// message or commits: callers do that with the returned subjects, keeping
// the guard/reset mechanics testable without a model backend.
func prepareSquashReset(opts commitCommandOptions) (squashResetOutcome, error) {
	base := opts.squashBase

	clean, err := workingTreeClean()
	if err != nil {
		return squashResetOutcome{}, err
	}
	if !clean {
		return squashResetOutcome{}, fmt.Errorf("refusing --squash: the working tree has uncommitted changes unrelated to the squash; commit or stash them first")
	}

	branch, err := currentBranchName()
	if err != nil {
		return squashResetOutcome{}, fmt.Errorf("resolve current branch: %w", err)
	}
	if isProtectedBranch(branch) && !opts.force {
		return squashResetOutcome{}, fmt.Errorf("refusing --squash on protected branch %q; pass --force to override", branch)
	}

	if _, err := gitOutput("rev-parse", "--verify", base); err != nil {
		return squashResetOutcome{}, fmt.Errorf("resolve --squash base %q: %w", base, err)
	}
	mergeBase, err := gitOutput("merge-base", "HEAD", base)
	if err != nil {
		return squashResetOutcome{}, fmt.Errorf("resolve merge base with %q: %w", base, err)
	}
	origHead, err := gitOutput("rev-parse", "HEAD")
	if err != nil {
		return squashResetOutcome{}, fmt.Errorf("resolve HEAD: %w", err)
	}
	if mergeBase == origHead {
		return squashResetOutcome{}, fmt.Errorf("nothing to squash: HEAD is already at the merge base with %s", base)
	}

	subjects, err := commitSubjectsBetween(mergeBase, origHead)
	if err != nil {
		return squashResetOutcome{}, err
	}
	if len(subjects) == 0 {
		return squashResetOutcome{}, fmt.Errorf("nothing to squash: no commits between %s and HEAD", base)
	}

	// git reset --soft records origHead in the branch reflog (HEAD@{1})
	// before moving the ref; printing it here is the undo instructions,
	// not a separate bookkeeping step. --dry-run still performs the reset
	// (the generated preview needs the real range diff as context) but
	// runSquashCommand restores origHead before returning, so a preview
	// never leaves the branch changed.
	if err := gitRun("reset", "--soft", mergeBase); err != nil {
		return squashResetOutcome{}, fmt.Errorf("soft reset to merge-base %s: %w", mergeBase, err)
	}
	if opts.dryRun {
		fmt.Printf("buckley: --dry-run: previewing a squash of %d commit(s) on %s; the branch will be restored to %s after the preview\n", len(subjects), branch, origHead)
	} else {
		fmt.Printf("buckley: squashed %d commit(s) on %s; pre-squash HEAD was %s (recorded in the reflog)\n", len(subjects), branch, origHead)
		fmt.Printf("buckley: undo with: git reset --hard %s\n", origHead)
	}

	return squashResetOutcome{Branch: branch, Subjects: subjects, OrigHead: origHead}, nil
}

// runSquashCommand implements `buckley commit --squash <base>`: soft-reset
// the current branch to its merge-base with base, then create one commit
// with a generated message summarizing the whole range. --dry-run restores
// the branch to its pre-squash state before returning: a preview must
// never leave lasting changes (FINDING-001, buckbot review of PR #216).
func runSquashCommand(opts commitCommandOptions) error {
	outcome, err := prepareSquashReset(opts)
	if err != nil {
		return err
	}
	if opts.dryRun {
		defer func() {
			if restoreErr := gitRun("reset", "--soft", outcome.OrigHead); restoreErr != nil {
				fmt.Fprintf(os.Stderr, "buckley: failed to restore pre-squash HEAD %s after --dry-run: %v\n", outcome.OrigHead, restoreErr)
			}
		}()
	}
	subjects, branch := outcome.Subjects, outcome.Branch

	def := squashRangeCommitDefinition{subjects: subjects}
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

	return completeSquashWithRunner(opts, ctx, runtime.runner, runtime.ledger, branch)
}

// completeSquashWithRunner generates the squash commit message with runner,
// confirms, commits, and (with --force-with-lease) pushes. Split out from
// runSquashCommand so tests can inject a fake commitRunner instead of a
// live model backend, and can verify the soft-reset/guard mechanics
// (workingTreeClean, isProtectedBranch, the reflog-recoverable reset)
// independently of message generation.
func completeSquashWithRunner(opts commitCommandOptions, ctx context.Context, runner commitRunner, ledger *transparency.CostLedger, branch string) error {
	result, err := runCommitGeneration(ctx, runner)
	if err != nil {
		return err
	}
	if result.Error != nil {
		printError(result.Error, result.Trace)
		return result.Error
	}
	if result.Commit == nil {
		return fmt.Errorf("no squash commit message generated")
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

	if err := createCommit(message, opts.compactOutput, false, nil); err != nil {
		printStagedIndexOnError()
		return err
	}

	if !opts.push {
		return nil
	}
	if !opts.forceWithLease {
		fmt.Fprintln(os.Stderr, "buckley: squash rewrote history; not pushing (pass --force-with-lease to push the rewritten branch)")
		return nil
	}
	return pushSquashedBranch(opts.compactOutput, branch)
}

// pushSquashedBranch pushes the rewritten branch with --force-with-lease,
// the one safe way to overwrite a remote branch: it fails instead of
// clobbering a push that landed on the remote since this branch was
// squashed, rather than blindly forcing.
func pushSquashedBranch(compactOutput bool, branch string) error {
	remote := os.Getenv("BUCKLEY_REMOTE_NAME")
	if remote == "" {
		remote = "origin"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	args := []string{"push", "--force-with-lease", "-u", remote, branch}
	if compactOutput {
		args = append([]string{args[0], "--quiet"}, args[1:]...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	if compactOutput {
		var stderr bytes.Buffer
		cmd.Stdout = io.Discard
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			detail := strings.TrimSpace(stderr.String())
			if detail != "" {
				return fmt.Errorf("push --force-with-lease failed: %w: %s", err, detail)
			}
			return fmt.Errorf("push --force-with-lease failed: %w", err)
		}
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("push --force-with-lease failed: %w", err)
		}
	}

	hashCtx, hashCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer hashCancel()
	hash := currentHeadHash(hashCtx, false)
	if compactOutput {
		if hash != "" {
			fmt.Printf("Pushed: %s\n", hash)
		}
	} else if hash != "" {
		termOut.Success("Pushed: %s to %s/%s (force-with-lease)", hash, remote, branch)
	}
	return nil
}

// gitRun runs a git command with no captured output, for state-mutating
// commands (reset) where only success/failure and the error detail matter.
func gitRun(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	var stderr bytes.Buffer
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("%w: %s", err, detail)
		}
		return err
	}
	return nil
}
