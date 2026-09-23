package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
)

// fakeCommitRunner is an injectable commitRunner for tests: it never
// touches a real model backend, matching the codebase's ToolInvoker-mock
// convention (pkg/oneshot/invoker_test.go's mockClient) one layer up, at
// the commitRunner seam commit.go already defines for this purpose.
type fakeCommitRunner struct {
	result *commitRunResult
	err    error
	calls  int
}

func (f *fakeCommitRunner) Run(ctx context.Context) (*commitRunResult, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func fakeMergeResult(action, scope string, body []string) *commitRunResult {
	return &commitRunResult{
		Commit: &commands.CommitResult{
			Action:  action,
			Scope:   scope,
			Subject: "ignored — the CLI overwrites this for merge commits",
			Body:    body,
		},
	}
}

// --- repo state helpers -----------------------------------------------

func chdirTemp(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func runGitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

// runGitAllowFail runs git and returns output plus whether it succeeded,
// for commands expected to fail (a merge that stops on conflicts).
func runGitAllowFail(t *testing.T, dir string, args ...string) (string, bool) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err == nil
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// conflictedMergeRepo builds a repo with a genuine merge conflict between
// "main" (current branch) and "feature" (merge source), returns the repo
// path. Caller resolves the conflict and stages it.
func conflictedMergeRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGitIn(t, repo, "init", "-q", "-b", "main")
	runGitIn(t, repo, "config", "user.email", "test@example.com")
	runGitIn(t, repo, "config", "user.name", "Test User")

	writeFile(t, repo, "f.txt", "line1\n")
	writeFile(t, repo, "g.txt", "untouched\n")
	runGitIn(t, repo, "add", "f.txt", "g.txt")
	runGitIn(t, repo, "commit", "-q", "-m", "base")

	runGitIn(t, repo, "checkout", "-q", "-b", "feature")
	writeFile(t, repo, "f.txt", "line1\nfeature change\n")
	runGitIn(t, repo, "commit", "-q", "-am", "feature: change f")
	writeFile(t, repo, "f.txt", "line1\nfeature change\nfeature change 2\n")
	runGitIn(t, repo, "commit", "-q", "-am", "feature: change f again")

	runGitIn(t, repo, "checkout", "-q", "main")
	writeFile(t, repo, "f.txt", "line1\nmain change\n")
	runGitIn(t, repo, "commit", "-q", "-am", "main: change f differently")

	if _, ok := runGitAllowFail(t, repo, "merge", "feature", "-m", "placeholder"); ok {
		t.Fatalf("expected merge to conflict")
	}
	return repo
}

// --- detectRepoOpState / unmerged paths --------------------------------

func TestDetectRepoOpState_NoOpInCleanRepo(t *testing.T) {
	repo := initTempGitRepo(t)
	chdirTemp(t, repo)

	state, err := detectRepoOpState()
	if err != nil {
		t.Fatalf("detectRepoOpState: %v", err)
	}
	if state.Kind != opNone {
		t.Fatalf("Kind = %v, want opNone", state.Kind)
	}
}

func TestDetectRepoOpState_MergeConflict(t *testing.T) {
	repo := conflictedMergeRepo(t)
	chdirTemp(t, repo)

	state, err := detectRepoOpState()
	if err != nil {
		t.Fatalf("detectRepoOpState: %v", err)
	}
	if state.Kind != opMerge {
		t.Fatalf("Kind = %v, want opMerge", state.Kind)
	}
	if state.MergeHead == "" {
		t.Fatal("MergeHead is empty")
	}

	wantHead := runGitIn(t, repo, "rev-parse", "feature")
	if state.MergeHead != wantHead {
		t.Fatalf("MergeHead = %s, want %s", state.MergeHead, wantHead)
	}
}

func TestDetectRepoOpState_CherryPick(t *testing.T) {
	repo := t.TempDir()
	runGitIn(t, repo, "init", "-q", "-b", "main")
	runGitIn(t, repo, "config", "user.email", "test@example.com")
	runGitIn(t, repo, "config", "user.name", "Test User")
	writeFile(t, repo, "f.txt", "base\n")
	runGitIn(t, repo, "add", "f.txt")
	runGitIn(t, repo, "commit", "-q", "-m", "base")
	runGitIn(t, repo, "checkout", "-q", "-b", "topic")
	writeFile(t, repo, "f.txt", "base\ntopic1\n")
	runGitIn(t, repo, "commit", "-q", "-am", "topic: change f")
	target := runGitIn(t, repo, "rev-parse", "topic")
	runGitIn(t, repo, "checkout", "-q", "main")
	writeFile(t, repo, "f.txt", "base\nmain change\n")
	runGitIn(t, repo, "commit", "-q", "-am", "main: change f")

	if _, ok := runGitAllowFail(t, repo, "cherry-pick", target); ok {
		t.Fatalf("expected cherry-pick to conflict")
	}
	chdirTemp(t, repo)

	state, err := detectRepoOpState()
	if err != nil {
		t.Fatalf("detectRepoOpState: %v", err)
	}
	if state.Kind != opCherryPick {
		t.Fatalf("Kind = %v, want opCherryPick", state.Kind)
	}
	if state.CherryPickHead != target {
		t.Fatalf("CherryPickHead = %s, want %s", state.CherryPickHead, target)
	}
}

func TestDetectRepoOpState_Revert(t *testing.T) {
	repo := t.TempDir()
	runGitIn(t, repo, "init", "-q", "-b", "main")
	runGitIn(t, repo, "config", "user.email", "test@example.com")
	runGitIn(t, repo, "config", "user.name", "Test User")
	writeFile(t, repo, "f.txt", "base\n")
	runGitIn(t, repo, "add", "f.txt")
	runGitIn(t, repo, "commit", "-q", "-m", "base")
	writeFile(t, repo, "f.txt", "base\nstep1\n")
	runGitIn(t, repo, "commit", "-q", "-am", "change f: step 1")
	bad := runGitIn(t, repo, "rev-parse", "HEAD")
	writeFile(t, repo, "f.txt", "base\nstep1\nstep2\n")
	runGitIn(t, repo, "commit", "-q", "-am", "change f: step 2")

	if _, ok := runGitAllowFail(t, repo, "revert", "--no-commit", bad); ok {
		t.Fatalf("expected revert to conflict")
	}
	chdirTemp(t, repo)

	state, err := detectRepoOpState()
	if err != nil {
		t.Fatalf("detectRepoOpState: %v", err)
	}
	if state.Kind != opRevert {
		t.Fatalf("Kind = %v, want opRevert", state.Kind)
	}
	if state.RevertHead != bad {
		t.Fatalf("RevertHead = %s, want %s", state.RevertHead, bad)
	}
}

func TestDetectRepoOpState_Rebase(t *testing.T) {
	repo := t.TempDir()
	runGitIn(t, repo, "init", "-q", "-b", "main")
	runGitIn(t, repo, "config", "user.email", "test@example.com")
	runGitIn(t, repo, "config", "user.name", "Test User")
	writeFile(t, repo, "f.txt", "base\n")
	runGitIn(t, repo, "add", "f.txt")
	runGitIn(t, repo, "commit", "-q", "-m", "base")
	runGitIn(t, repo, "checkout", "-q", "-b", "topic")
	writeFile(t, repo, "f.txt", "base\ntopic1\n")
	runGitIn(t, repo, "commit", "-q", "-am", "topic: change f")
	runGitIn(t, repo, "checkout", "-q", "main")
	writeFile(t, repo, "f.txt", "base\nmain change\n")
	runGitIn(t, repo, "commit", "-q", "-am", "main: change f")
	runGitIn(t, repo, "checkout", "-q", "topic")

	if _, ok := runGitAllowFail(t, repo, "rebase", "main"); ok {
		t.Fatalf("expected rebase to conflict")
	}
	chdirTemp(t, repo)

	state, err := detectRepoOpState()
	if err != nil {
		t.Fatalf("detectRepoOpState: %v", err)
	}
	if state.Kind != opRebase {
		t.Fatalf("Kind = %v, want opRebase", state.Kind)
	}
}

func TestDetectRepoOpState_MergeSquashMsg(t *testing.T) {
	repo := t.TempDir()
	runGitIn(t, repo, "init", "-q", "-b", "main")
	runGitIn(t, repo, "config", "user.email", "test@example.com")
	runGitIn(t, repo, "config", "user.name", "Test User")
	writeFile(t, repo, "f.txt", "base\n")
	runGitIn(t, repo, "add", "f.txt")
	runGitIn(t, repo, "commit", "-q", "-m", "base")
	runGitIn(t, repo, "checkout", "-q", "-b", "topic")
	writeFile(t, repo, "f.txt", "base\ntopic1\n")
	runGitIn(t, repo, "commit", "-q", "-am", "topic: t1")
	writeFile(t, repo, "f.txt", "base\ntopic1\ntopic2\n")
	runGitIn(t, repo, "commit", "-q", "-am", "topic: t2")
	runGitIn(t, repo, "checkout", "-q", "main")
	runGitIn(t, repo, "merge", "--squash", "topic")
	chdirTemp(t, repo)

	state, err := detectRepoOpState()
	if err != nil {
		t.Fatalf("detectRepoOpState: %v", err)
	}
	if state.Kind != opMergeSquash {
		t.Fatalf("Kind = %v, want opMergeSquash", state.Kind)
	}

	def := squashMsgCommitDefinition{squashMsg: readGitDirFile(state.GitDir, "SQUASH_MSG")}
	prompt := def.BuildPrompt(&oneshot.Context{Sources: map[string]string{}})
	if !strings.Contains(prompt, "topic: t1") || !strings.Contains(prompt, "topic: t2") {
		t.Fatalf("prompt missing squashed commit subjects: %s", prompt)
	}
}

func TestUnmergedPaths_ListsConflictedFiles(t *testing.T) {
	repo := conflictedMergeRepo(t)
	chdirTemp(t, repo)

	paths, err := unmergedPaths()
	if err != nil {
		t.Fatalf("unmergedPaths: %v", err)
	}
	if len(paths) != 1 || paths[0] != "f.txt" {
		t.Fatalf("paths = %#v, want [f.txt]", paths)
	}

	if err := refuseIfUnmerged(); err == nil {
		t.Fatal("refuseIfUnmerged() = nil, want error listing f.txt")
	} else if !strings.Contains(err.Error(), "f.txt") {
		t.Fatalf("refuseIfUnmerged() error = %v, want mention of f.txt", err)
	}
}

// --- conflict resolution analysis --------------------------------------

func TestMergeConflictFiles_ParsesConflictsSection(t *testing.T) {
	repo := conflictedMergeRepo(t)
	state, err := detectRepoOpStateIn(repo)
	if err != nil {
		t.Fatalf("detectRepoOpStateIn: %v", err)
	}
	files := mergeConflictFiles(state.GitDir)
	if len(files) != 1 || files[0] != "f.txt" {
		t.Fatalf("files = %#v, want [f.txt]", files)
	}
}

func TestAnalyzeConflictResolutions_KeptIncoming(t *testing.T) {
	repo := conflictedMergeRepo(t)
	// Resolve by taking the incoming (feature) side entirely.
	theirs := runGitIn(t, repo, "show", "MERGE_HEAD:f.txt")
	writeFile(t, repo, "f.txt", theirs+"\n")
	runGitIn(t, repo, "add", "f.txt")
	chdirTemp(t, repo)

	facts := analyzeConflictResolutions([]string{"f.txt"}, "HEAD", "MERGE_HEAD")
	if len(facts) != 1 {
		t.Fatalf("facts = %#v, want 1 entry", facts)
	}
	if !strings.Contains(facts[0], "kept the incoming version") {
		t.Fatalf("facts[0] = %q, want mention of keeping the incoming version", facts[0])
	}
}

func TestAnalyzeConflictResolutions_KeptOurs(t *testing.T) {
	repo := conflictedMergeRepo(t)
	ours := runGitIn(t, repo, "show", "HEAD:f.txt")
	writeFile(t, repo, "f.txt", ours+"\n")
	runGitIn(t, repo, "add", "f.txt")
	chdirTemp(t, repo)

	facts := analyzeConflictResolutions([]string{"f.txt"}, "HEAD", "MERGE_HEAD")
	if len(facts) != 1 || !strings.Contains(facts[0], "kept the current branch's version") {
		t.Fatalf("facts = %#v, want mention of keeping the current branch's version", facts)
	}
}

func TestAnalyzeConflictResolutions_Combined(t *testing.T) {
	repo := conflictedMergeRepo(t)
	writeFile(t, repo, "f.txt", "line1\nmain change\nfeature change\nfeature change 2\n")
	runGitIn(t, repo, "add", "f.txt")
	chdirTemp(t, repo)

	facts := analyzeConflictResolutions([]string{"f.txt"}, "HEAD", "MERGE_HEAD")
	if len(facts) != 1 || !strings.Contains(facts[0], "combined") {
		t.Fatalf("facts = %#v, want mention of combining both sides", facts)
	}
}

// detectRepoOpStateIn runs detectRepoOpState with cwd set to dir, without
// leaving cwd changed for the rest of the test (helper composes with
// chdirTemp's own t.Cleanup when the caller also calls it; here we swap and
// restore manually since some tests need the repo path outside cwd first).
func detectRepoOpStateIn(dir string) (repoOpState, error) {
	old, err := os.Getwd()
	if err != nil {
		return repoOpState{}, err
	}
	defer func() { _ = os.Chdir(old) }()
	if err := os.Chdir(dir); err != nil {
		return repoOpState{}, err
	}
	return detectRepoOpState()
}

// --- end-to-end merge completion (fake model) ---------------------------

func TestRunCompleteMerge_CreatesTwoParentCommitWithForcedSubject(t *testing.T) {
	repo := conflictedMergeRepo(t)
	theirs := runGitIn(t, repo, "show", "MERGE_HEAD:f.txt")
	writeFile(t, repo, "f.txt", theirs+"\n")
	runGitIn(t, repo, "add", "f.txt")
	chdirTemp(t, repo)

	state, err := detectRepoOpState()
	if err != nil {
		t.Fatalf("detectRepoOpState: %v", err)
	}

	runner := &fakeCommitRunner{result: fakeMergeResult("add", "core", []string{
		"Brought in feature's change to f.txt",
		"f.txt: kept the incoming version",
	})}

	opts := commitCommandOptions{yes: true, push: false, compactOutput: true, showCost: false}
	target, err := currentBranchName()
	if err != nil {
		t.Fatalf("currentBranchName: %v", err)
	}
	source := mergeMsgSourceName(state.MergeHead)

	if err := completeMergeWithRunner(opts, context.Background(), runner, nil, source, target); err != nil {
		t.Fatalf("completeMergeWithRunner: %v", err)
	}

	parents := strings.Fields(runGitIn(t, repo, "log", "-1", "--format=%P"))
	if len(parents) != 2 {
		t.Fatalf("parent count = %d, want 2 (log: %s)", len(parents), runGitIn(t, repo, "log", "-1", "--format=%P"))
	}

	subject := runGitIn(t, repo, "log", "-1", "--format=%s")
	if subject != "merge(core): Merge feature into main" {
		t.Fatalf("subject = %q, want forced merge grammar", subject)
	}

	body := runGitIn(t, repo, "log", "-1", "--format=%b")
	if !strings.Contains(body, "kept the incoming version") {
		t.Fatalf("body = %q, want conflict-resolution fact", body)
	}

	if runner.calls != 1 {
		t.Fatalf("runner called %d times, want 1", runner.calls)
	}

	if _, err := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD")); err == nil {
		t.Fatal("MERGE_HEAD still present after commit")
	}
}

func TestRunCompleteMerge_RefusesUnresolvedConflicts(t *testing.T) {
	repo := conflictedMergeRepo(t)
	chdirTemp(t, repo)

	state, err := detectRepoOpState()
	if err != nil {
		t.Fatalf("detectRepoOpState: %v", err)
	}
	opts := commitCommandOptions{yes: true}
	err = runCompleteMerge(opts, state)
	if err == nil {
		t.Fatal("runCompleteMerge() = nil, want refusal for unresolved conflicts")
	}
	if !strings.Contains(err.Error(), "f.txt") {
		t.Fatalf("error = %v, want mention of f.txt", err)
	}
}

// --- cherry-pick / revert continuation (deterministic, no model) --------

func TestRunCompleteCherryPick_PreservesOriginalMessageAndAddsReference(t *testing.T) {
	repo := t.TempDir()
	runGitIn(t, repo, "init", "-q", "-b", "main")
	runGitIn(t, repo, "config", "user.email", "test@example.com")
	runGitIn(t, repo, "config", "user.name", "Test User")
	writeFile(t, repo, "f.txt", "base\n")
	runGitIn(t, repo, "add", "f.txt")
	runGitIn(t, repo, "commit", "-q", "-m", "base")
	runGitIn(t, repo, "checkout", "-q", "-b", "topic")
	writeFile(t, repo, "f.txt", "base\ntopic1\n")
	runGitIn(t, repo, "commit", "-q", "-am", "topic: change f")
	target := runGitIn(t, repo, "rev-parse", "topic")
	runGitIn(t, repo, "checkout", "-q", "main")
	writeFile(t, repo, "f.txt", "base\nmain change\n")
	runGitIn(t, repo, "commit", "-q", "-am", "main: change f")
	if _, ok := runGitAllowFail(t, repo, "cherry-pick", target); ok {
		t.Fatalf("expected cherry-pick to conflict")
	}
	// Resolve: take incoming.
	theirs := runGitIn(t, repo, "show", "CHERRY_PICK_HEAD:f.txt")
	writeFile(t, repo, "f.txt", theirs+"\n")
	runGitIn(t, repo, "add", "f.txt")
	chdirTemp(t, repo)

	state, err := detectRepoOpState()
	if err != nil {
		t.Fatalf("detectRepoOpState: %v", err)
	}
	opts := commitCommandOptions{yes: true, push: false, compactOutput: true}
	if err := runCompleteCherryPick(opts, state); err != nil {
		t.Fatalf("runCompleteCherryPick: %v", err)
	}

	message := runGitIn(t, repo, "log", "-1", "--format=%B")
	if !strings.Contains(message, "topic: change f") {
		t.Fatalf("message = %q, want original subject preserved", message)
	}
	wantTrailer := "(cherry picked from commit " + target + ")"
	if !strings.Contains(message, wantTrailer) {
		t.Fatalf("message = %q, want trailer %q", message, wantTrailer)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "CHERRY_PICK_HEAD")); err == nil {
		t.Fatal("CHERRY_PICK_HEAD still present after commit")
	}
}

func TestRunCompleteCherryPick_RefusesUnresolvedConflicts(t *testing.T) {
	repo := t.TempDir()
	runGitIn(t, repo, "init", "-q", "-b", "main")
	runGitIn(t, repo, "config", "user.email", "test@example.com")
	runGitIn(t, repo, "config", "user.name", "Test User")
	writeFile(t, repo, "f.txt", "base\n")
	runGitIn(t, repo, "add", "f.txt")
	runGitIn(t, repo, "commit", "-q", "-m", "base")
	runGitIn(t, repo, "checkout", "-q", "-b", "topic")
	writeFile(t, repo, "f.txt", "base\ntopic1\n")
	runGitIn(t, repo, "commit", "-q", "-am", "topic: change f")
	target := runGitIn(t, repo, "rev-parse", "topic")
	runGitIn(t, repo, "checkout", "-q", "main")
	writeFile(t, repo, "f.txt", "base\nmain change\n")
	runGitIn(t, repo, "commit", "-q", "-am", "main: change f")
	if _, ok := runGitAllowFail(t, repo, "cherry-pick", target); ok {
		t.Fatalf("expected cherry-pick to conflict")
	}
	chdirTemp(t, repo)

	state, err := detectRepoOpState()
	if err != nil {
		t.Fatalf("detectRepoOpState: %v", err)
	}
	if err := runCompleteCherryPick(commitCommandOptions{yes: true}, state); err == nil {
		t.Fatal("runCompleteCherryPick() = nil, want refusal for unresolved conflicts")
	}
}

func TestRunCompleteRevert_UsesGitDefaultRevertGrammar(t *testing.T) {
	repo := t.TempDir()
	runGitIn(t, repo, "init", "-q", "-b", "main")
	runGitIn(t, repo, "config", "user.email", "test@example.com")
	runGitIn(t, repo, "config", "user.name", "Test User")
	writeFile(t, repo, "f.txt", "base\n")
	runGitIn(t, repo, "add", "f.txt")
	runGitIn(t, repo, "commit", "-q", "-m", "base")
	writeFile(t, repo, "f.txt", "base\nstep1\n")
	runGitIn(t, repo, "commit", "-q", "-am", "change f: step 1")
	bad := runGitIn(t, repo, "rev-parse", "HEAD")
	writeFile(t, repo, "f.txt", "base\nstep1\nstep2\n")
	runGitIn(t, repo, "commit", "-q", "-am", "change f: step 2")
	if _, ok := runGitAllowFail(t, repo, "revert", "--no-commit", bad); ok {
		t.Fatalf("expected revert to conflict")
	}
	writeFile(t, repo, "f.txt", "base\nstep2\n")
	runGitIn(t, repo, "add", "f.txt")
	chdirTemp(t, repo)

	state, err := detectRepoOpState()
	if err != nil {
		t.Fatalf("detectRepoOpState: %v", err)
	}
	opts := commitCommandOptions{yes: true, push: false, compactOutput: true}
	if err := runCompleteRevert(opts, state); err != nil {
		t.Fatalf("runCompleteRevert: %v", err)
	}

	message := runGitIn(t, repo, "log", "-1", "--format=%B")
	wantSubject := `Revert "change f: step 1"`
	if !strings.HasPrefix(message, wantSubject) {
		t.Fatalf("message = %q, want prefix %q", message, wantSubject)
	}
	if !strings.Contains(message, "This reverts commit "+bad+".") {
		t.Fatalf("message = %q, want reference to reverted commit %s", message, bad)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "REVERT_HEAD")); err == nil {
		t.Fatal("REVERT_HEAD still present after commit")
	}
}

// --- rebase guidance -----------------------------------------------------

func TestRunCommitCommand_RebaseInProgressReturnsGuidance(t *testing.T) {
	repo := t.TempDir()
	runGitIn(t, repo, "init", "-q", "-b", "main")
	runGitIn(t, repo, "config", "user.email", "test@example.com")
	runGitIn(t, repo, "config", "user.name", "Test User")
	writeFile(t, repo, "f.txt", "base\n")
	runGitIn(t, repo, "add", "f.txt")
	runGitIn(t, repo, "commit", "-q", "-m", "base")
	runGitIn(t, repo, "checkout", "-q", "-b", "topic")
	writeFile(t, repo, "f.txt", "base\ntopic1\n")
	runGitIn(t, repo, "commit", "-q", "-am", "topic: change f")
	runGitIn(t, repo, "checkout", "-q", "main")
	writeFile(t, repo, "f.txt", "base\nmain change\n")
	runGitIn(t, repo, "commit", "-q", "-am", "main: change f")
	runGitIn(t, repo, "checkout", "-q", "topic")
	if _, ok := runGitAllowFail(t, repo, "rebase", "main"); ok {
		t.Fatalf("expected rebase to conflict")
	}
	chdirTemp(t, repo)

	err := runCommitCommand([]string{"--yes"})
	if err != errRebaseInProgress {
		t.Fatalf("runCommitCommand() error = %v, want errRebaseInProgress", err)
	}
	if !strings.Contains(err.Error(), "git rebase --continue") {
		t.Fatalf("error = %v, want guidance to run git rebase --continue", err)
	}
}

// --- source name resolution ----------------------------------------------

func TestMergeMsgSourceName_ResolvesBranchName(t *testing.T) {
	repo := conflictedMergeRepo(t)
	head := runGitIn(t, repo, "rev-parse", "feature")
	chdirTemp(t, repo)

	got := mergeMsgSourceName(head)
	if got != "feature" {
		t.Fatalf("mergeMsgSourceName() = %q, want %q", got, "feature")
	}
}

func TestCommitSubjectsBetween_OldestFirst(t *testing.T) {
	repo := conflictedMergeRepo(t)
	chdirTemp(t, repo)

	mergeBase := runGitIn(t, repo, "merge-base", "HEAD", "MERGE_HEAD")
	subjects, err := commitSubjectsBetween(mergeBase, "MERGE_HEAD")
	if err != nil {
		t.Fatalf("commitSubjectsBetween: %v", err)
	}
	want := []string{"feature: change f", "feature: change f again"}
	if len(subjects) != len(want) {
		t.Fatalf("subjects = %#v, want %#v", subjects, want)
	}
	for i := range want {
		if subjects[i] != want[i] {
			t.Fatalf("subjects[%d] = %q, want %q", i, subjects[i], want[i])
		}
	}
}

func TestCommitSubjectsBetween_CondensesLongRanges(t *testing.T) {
	repo := t.TempDir()
	runGitIn(t, repo, "init", "-q", "-b", "main")
	runGitIn(t, repo, "config", "user.email", "test@example.com")
	runGitIn(t, repo, "config", "user.name", "Test User")
	writeFile(t, repo, "f.txt", "0\n")
	runGitIn(t, repo, "add", "f.txt")
	runGitIn(t, repo, "commit", "-q", "-m", "base")
	base := runGitIn(t, repo, "rev-parse", "HEAD")
	for i := 1; i <= 30; i++ {
		writeFile(t, repo, "f.txt", "0\n"+strconv.Itoa(i)+"\n")
		runGitIn(t, repo, "commit", "-q", "-am", "commit "+strconv.Itoa(i))
	}
	chdirTemp(t, repo)

	subjects, err := commitSubjectsBetween(base, "HEAD")
	if err != nil {
		t.Fatalf("commitSubjectsBetween: %v", err)
	}
	if len(subjects) != 26 { // 25 + "and N more"
		t.Fatalf("len(subjects) = %d, want 26", len(subjects))
	}
	last := subjects[len(subjects)-1]
	if !strings.Contains(last, "and 5 more") {
		t.Fatalf("last subject = %q, want mention of 5 more commits", last)
	}
}
