package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/storage"
)

// squashRepo builds a repo with a "main" branch and a "topic" branch three
// commits ahead, current checkout on topic. Returns the repo path.
func squashRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGitIn(t, repo, "init", "-q", "-b", "main")
	runGitIn(t, repo, "config", "user.email", "test@example.com")
	runGitIn(t, repo, "config", "user.name", "Test User")
	writeFile(t, repo, "f.txt", "0\n")
	runGitIn(t, repo, "add", "f.txt")
	runGitIn(t, repo, "commit", "-q", "-m", "base")

	runGitIn(t, repo, "checkout", "-q", "-b", "topic")
	writeFile(t, repo, "f.txt", "0\n1\n")
	runGitIn(t, repo, "commit", "-q", "-am", "topic: add line 1")
	writeFile(t, repo, "f.txt", "0\n1\n2\n")
	runGitIn(t, repo, "commit", "-q", "-am", "topic: add line 2")
	writeFile(t, repo, "f.txt", "0\n1\n2\n3\n")
	runGitIn(t, repo, "commit", "-q", "-am", "topic: add line 3")

	return repo
}

func TestPrepareSquashReset_SoftResetsToMergeBaseAndReportsSubjects(t *testing.T) {
	repo := squashRepo(t)
	origHead := runGitIn(t, repo, "rev-parse", "HEAD")
	origTree := runGitIn(t, repo, "rev-parse", "HEAD^{tree}")
	chdirTemp(t, repo)

	outcome, err := prepareSquashReset(commitCommandOptions{squashBase: "main"})
	if err != nil {
		t.Fatalf("prepareSquashReset: %v", err)
	}
	if outcome.Branch != "topic" {
		t.Fatalf("Branch = %q, want topic", outcome.Branch)
	}
	if outcome.OrigHead != origHead {
		t.Fatalf("OrigHead = %s, want %s", outcome.OrigHead, origHead)
	}
	want := []string{"topic: add line 1", "topic: add line 2", "topic: add line 3"}
	if len(outcome.Subjects) != len(want) {
		t.Fatalf("Subjects = %#v, want %#v", outcome.Subjects, want)
	}
	for i := range want {
		if outcome.Subjects[i] != want[i] {
			t.Fatalf("Subjects[%d] = %q, want %q", i, outcome.Subjects[i], want[i])
		}
	}

	// HEAD moved to the merge-base; the index/worktree still hold the
	// pre-squash content, staged as one diff.
	newHead := runGitIn(t, repo, "rev-parse", "HEAD")
	mainHead := runGitIn(t, repo, "rev-parse", "main")
	if newHead != mainHead {
		t.Fatalf("HEAD after soft reset = %s, want merge-base (main = %s)", newHead, mainHead)
	}
	staged := runGitIn(t, repo, "diff", "--cached", "--name-only")
	if staged != "f.txt" {
		t.Fatalf("staged files = %q, want f.txt", staged)
	}
	worktreeContent := runGitIn(t, repo, "show", ":f.txt")
	origContent := runGitIn(t, repo, "cat-file", "-p", origTree+":f.txt")
	if worktreeContent != origContent {
		t.Fatalf("staged content = %q, want pre-squash content %q", worktreeContent, origContent)
	}

	// origHead is still reachable (reflog / dangling), so `git reset --hard
	// origHead` recovers pre-squash state.
	if _, ok := runGitAllowFail(t, repo, "cat-file", "-e", origHead); !ok {
		t.Fatalf("pre-squash HEAD %s is no longer reachable", origHead)
	}
	reflogPrior := runGitIn(t, repo, "rev-parse", "HEAD@{1}")
	if reflogPrior != origHead {
		t.Fatalf("HEAD@{1} = %s, want pre-squash head %s (reflog undo path)", reflogPrior, origHead)
	}
}

func TestPrepareSquashReset_RefusesDirtyWorkingTree(t *testing.T) {
	repo := squashRepo(t)
	writeFile(t, repo, "f.txt", "0\n1\n2\n3\nuncommitted\n")
	chdirTemp(t, repo)

	_, err := prepareSquashReset(commitCommandOptions{squashBase: "main"})
	if err == nil {
		t.Fatal("prepareSquashReset() = nil, want refusal for dirty working tree")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("error = %v, want mention of uncommitted changes", err)
	}

	// Nothing was reset.
	head := runGitIn(t, repo, "rev-parse", "HEAD")
	topicHead := runGitIn(t, repo, "rev-parse", "refs/heads/topic")
	if head != topicHead {
		t.Fatalf("HEAD moved despite refusal: %s != %s", head, topicHead)
	}
}

func TestPrepareSquashReset_RefusesProtectedBranchWithoutForce(t *testing.T) {
	repo := squashRepo(t)
	runGitIn(t, repo, "checkout", "-q", "main")
	runGitIn(t, repo, "merge", "-q", "--no-ff", "topic", "-m", "merge topic")
	chdirTemp(t, repo)

	_, err := prepareSquashReset(commitCommandOptions{squashBase: "HEAD~1"})
	if err == nil {
		t.Fatal("prepareSquashReset() = nil, want refusal on protected branch")
	}
	if !strings.Contains(err.Error(), "protected branch") {
		t.Fatalf("error = %v, want mention of protected branch", err)
	}
}

func TestPrepareSquashReset_AllowsProtectedBranchWithForce(t *testing.T) {
	repo := squashRepo(t)
	runGitIn(t, repo, "checkout", "-q", "main")
	writeFile(t, repo, "g.txt", "a\n")
	runGitIn(t, repo, "add", "g.txt")
	runGitIn(t, repo, "commit", "-q", "-m", "main: add g")
	base := runGitIn(t, repo, "rev-parse", "HEAD~1")
	chdirTemp(t, repo)

	_, err := prepareSquashReset(commitCommandOptions{squashBase: base, force: true})
	if err != nil {
		t.Fatalf("prepareSquashReset with --force: %v", err)
	}
}

func TestPrepareSquashReset_RefusesWhenNothingToSquash(t *testing.T) {
	repo := squashRepo(t)
	chdirTemp(t, repo)

	_, err := prepareSquashReset(commitCommandOptions{squashBase: "topic"})
	if err == nil {
		t.Fatal("prepareSquashReset() = nil, want refusal when HEAD is already at the base")
	}
}

// --- full squash-and-commit with a fake model, verifying the tree -------

func TestCompleteSquashWithRunner_ProducesOneCommitWithSameTree(t *testing.T) {
	repo := squashRepo(t)
	origTree := runGitIn(t, repo, "rev-parse", "HEAD^{tree}")
	chdirTemp(t, repo)

	outcome, err := prepareSquashReset(commitCommandOptions{squashBase: "main"})
	if err != nil {
		t.Fatalf("prepareSquashReset: %v", err)
	}

	runner := &fakeCommitRunner{result: fakeMergeResult("add", "core", []string{
		"Add lines 1 through 3 to f.txt",
	})}
	opts := commitCommandOptions{yes: true, push: false, compactOutput: true}

	if err := completeSquashWithRunner(opts, context.Background(), runner, nil, outcome.Branch); err != nil {
		t.Fatalf("completeSquashWithRunner: %v", err)
	}

	// Exactly one commit between main and HEAD now.
	count := runGitIn(t, repo, "rev-list", "--count", "main..HEAD")
	if count != "1" {
		t.Fatalf("commit count main..HEAD = %s, want 1", count)
	}
	newTree := runGitIn(t, repo, "rev-parse", "HEAD^{tree}")
	if newTree != origTree {
		t.Fatalf("tree after squash = %s, want unchanged tree %s", newTree, origTree)
	}
	if runner.calls != 1 {
		t.Fatalf("runner called %d times, want 1", runner.calls)
	}
}

func TestCompleteSquashWithRunner_SkipsPushWithoutForceWithLease(t *testing.T) {
	repo := squashRepo(t)
	remote := t.TempDir() + "/origin.git"
	runGitIn(t, repo, "init", "-q", "--bare", remote)
	runGitIn(t, repo, "remote", "add", "origin", remote)
	runGitIn(t, repo, "push", "-q", "-u", "origin", "topic")
	chdirTemp(t, repo)

	outcome, err := prepareSquashReset(commitCommandOptions{squashBase: "main"})
	if err != nil {
		t.Fatalf("prepareSquashReset: %v", err)
	}
	runner := &fakeCommitRunner{result: fakeMergeResult("add", "core", []string{"squashed"})}
	opts := commitCommandOptions{yes: true, push: true, forceWithLease: false, compactOutput: true}

	stderr := captureStderr(t, func() {
		if err := completeSquashWithRunner(opts, context.Background(), runner, nil, outcome.Branch); err != nil {
			t.Fatalf("completeSquashWithRunner: %v", err)
		}
	})
	if !strings.Contains(stderr, "--force-with-lease") {
		t.Fatalf("stderr = %q, want guidance to pass --force-with-lease", stderr)
	}

	// The remote still has the old (unsquashed) history: nothing pushed.
	remoteHead := runGitIn(t, repo, "ls-remote", "origin", "refs/heads/topic")
	localHead := runGitIn(t, repo, "rev-parse", "HEAD")
	if strings.Contains(remoteHead, localHead) {
		t.Fatalf("remote unexpectedly has the squashed head: %s", remoteHead)
	}
}

func TestCompleteSquashWithRunner_PushesWithForceWithLease(t *testing.T) {
	repo := squashRepo(t)
	remote := t.TempDir() + "/origin.git"
	runGitIn(t, repo, "init", "-q", "--bare", remote)
	runGitIn(t, repo, "remote", "add", "origin", remote)
	runGitIn(t, repo, "push", "-q", "-u", "origin", "topic")
	chdirTemp(t, repo)

	outcome, err := prepareSquashReset(commitCommandOptions{squashBase: "main"})
	if err != nil {
		t.Fatalf("prepareSquashReset: %v", err)
	}
	runner := &fakeCommitRunner{result: fakeMergeResult("add", "core", []string{"squashed"})}
	opts := commitCommandOptions{yes: true, push: true, forceWithLease: true, compactOutput: true}

	if err := completeSquashWithRunner(opts, context.Background(), runner, nil, outcome.Branch); err != nil {
		t.Fatalf("completeSquashWithRunner: %v", err)
	}

	localHead := runGitIn(t, repo, "rev-parse", "HEAD")
	remoteHead := runGitIn(t, repo, "ls-remote", "origin", "refs/heads/topic")
	if !strings.Contains(remoteHead, localHead) {
		t.Fatalf("remote ref = %q, want to contain pushed head %s", remoteHead, localHead)
	}
}

// TestRunSquashCommand_DryRunRestoresBranchEvenOnLateError guards against
// FINDING-001 from the buckbot review of PR #216: prepareSquashReset's
// `git reset --soft` ran unconditionally, and completeSquashWithRunner
// only checked --dry-run after that reset (to skip creating the commit),
// so a preview left HEAD moved and the range diff staged. runSquashCommand
// now restores origHead in a defer that fires on every return path,
// including an error after the reset. This test forces dependency init to
// fail (a real, deterministic, network-free error) so the model-generation
// step never happens, and confirms HEAD is still restored.
func TestRunSquashCommand_DryRunRestoresBranchEvenOnLateError(t *testing.T) {
	repo := squashRepo(t)
	origHead := runGitIn(t, repo, "rev-parse", "HEAD")
	chdirTemp(t, repo)

	previousInit := initDependenciesFn
	t.Cleanup(func() { initDependenciesFn = previousInit })
	sentinel := errors.New("dependency init reached (should not commit)")
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		return nil, nil, nil, sentinel
	}

	opts := commitCommandOptions{squashBase: "main", dryRun: true, yes: true, compactOutput: true, backend: oneshotBackendAPI}
	err := runSquashCommand(opts)
	if err == nil || !strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatalf("runSquashCommand() error = %v, want the sentinel dependency-init error", err)
	}

	head := runGitIn(t, repo, "rev-parse", "HEAD")
	if head != origHead {
		t.Fatalf("HEAD after --dry-run (with a late error) = %s, want restored to %s", head, origHead)
	}
	staged := runGitIn(t, repo, "diff", "--cached", "--name-only")
	if staged != "" {
		t.Fatalf("staged files after --dry-run restore = %q, want none", staged)
	}
}

func TestWorkingTreeClean_IgnoresUntrackedFiles(t *testing.T) {
	repo := squashRepo(t)
	writeFile(t, repo, "untracked.txt", "new file\n")
	chdirTemp(t, repo)

	clean, err := workingTreeClean()
	if err != nil {
		t.Fatalf("workingTreeClean: %v", err)
	}
	if !clean {
		t.Fatal("workingTreeClean() = false, want true (untracked files should not block --squash)")
	}
}

func TestIsProtectedBranch(t *testing.T) {
	cases := map[string]bool{
		"main":         true,
		"master":       true,
		"topic":        false,
		"release/main": false,
	}
	for branch, want := range cases {
		if got := isProtectedBranch(branch); got != want {
			t.Errorf("isProtectedBranch(%q) = %v, want %v", branch, got, want)
		}
	}
}

func TestSquashRangeCommitDefinition_PromptListsCommits(t *testing.T) {
	def := squashRangeCommitDefinition{subjects: []string{"topic: add line 1", "topic: add line 2"}}
	prompt := def.BuildPrompt(&oneshot.Context{Sources: map[string]string{}})
	if !strings.Contains(prompt, "topic: add line 1") || !strings.Contains(prompt, "topic: add line 2") {
		t.Fatalf("prompt = %q, want both subjects listed", prompt)
	}
}
