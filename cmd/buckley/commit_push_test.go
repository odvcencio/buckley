package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPushChangesUniqueCodexBranchPushesAndVerifies(t *testing.T) {
	repo := initTempGitRepo(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	initializeBareRemoteDefault(t, repo, remote, "main")
	runGitOutputForPushTest(t, repo, "switch", "-c", "codex/test-checkpoint")

	branch := runGitOutputForPushTest(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	wantHash := runGitOutputForPushTest(t, repo, "rev-parse", "HEAD")

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	t.Setenv("BUCKLEY_REMOTE_NAME", "origin")

	var pushErr error
	out := captureStdout(t, func() {
		pushErr = pushChanges(true, false)
	})
	if pushErr != nil {
		t.Fatalf("pushChanges: %v", pushErr)
	}
	if !strings.Contains(out, "Pushed: "+wantHash) {
		t.Fatalf("push output %q does not include pushed hash %s", out, wantHash)
	}

	remoteRef := runGitOutputForPushTest(t, repo, "ls-remote", "origin", "refs/heads/"+branch)
	if !strings.Contains(remoteRef, wantHash) {
		t.Fatalf("remote ref %q does not include pushed hash %s", remoteRef, wantHash)
	}
}

func TestPushChangesMainFailsClosedBeforePush(t *testing.T) {
	repo := initTempGitRepo(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	runGitOutputForPushTest(t, repo, "init", "--bare", remote)
	runGitOutputForPushTest(t, repo, "remote", "add", "origin", remote)
	runGitOutputForPushTest(t, repo, "branch", "-M", "main")

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	t.Setenv("BUCKLEY_REMOTE_NAME", "origin")

	err = pushChanges(true, false)
	if err == nil || !strings.Contains(err.Error(), `refusing direct push to protected branch "main"`) {
		t.Fatalf("pushChanges error = %v, want protected-main failure", err)
	}
	if got := runGitOutputForPushTest(t, repo, "ls-remote", "origin", "refs/heads/main"); got != "" {
		t.Fatalf("protected main was pushed: %q", got)
	}
}

func TestProtectedPushBranchRejectsResolvedRemoteDefault(t *testing.T) {
	repo := initTempGitRepo(t)
	runGitOutputForPushTest(t, repo, "switch", "-c", "release")
	remote := filepath.Join(t.TempDir(), "origin.git")
	initializeBareRemoteDefault(t, repo, remote, "release")

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	if err := protectedPushBranch(context.Background(), "origin", "release"); err == nil || !strings.Contains(err.Error(), "remote default branch") {
		t.Fatalf("protectedPushBranch error = %v, want resolved-default rejection", err)
	}
	if err := protectedPushBranch(context.Background(), "origin", "codex/test-checkpoint"); err != nil {
		t.Fatalf("unique codex checkpoint branch rejected: %v", err)
	}
}

func TestParseRemoteDefaultBranchRejectsMalformedOutput(t *testing.T) {
	_, err := parseRemoteDefaultBranch([]byte("ref: refs/tags/v1\tHEAD\n1234\tHEAD\n"))
	if err == nil || !strings.Contains(err.Error(), "malformed HEAD symref") {
		t.Fatalf("parseRemoteDefaultBranch error = %v, want malformed-output failure", err)
	}
}

func TestPushBranchOutputIncludesHeadHash(t *testing.T) {
	repo := initTempGitRepo(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	runGitOutputForPushTest(t, repo, "init", "--bare", remote)
	runGitOutputForPushTest(t, repo, "remote", "add", "origin", remote)

	wantHash := runGitOutputForPushTest(t, repo, "rev-parse", "HEAD")

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	var pushErr error
	out := captureStdout(t, func() {
		pushErr = pushBranch("origin", "published")
	})
	if pushErr != nil {
		t.Fatalf("pushBranch: %v", pushErr)
	}
	if !strings.Contains(out, "Pushed: "+wantHash) {
		t.Fatalf("push output %q does not include pushed hash %s", out, wantHash)
	}

	remoteRef := runGitOutputForPushTest(t, repo, "ls-remote", "origin", "refs/heads/published")
	if !strings.Contains(remoteRef, wantHash) {
		t.Fatalf("remote ref %q does not include pushed hash %s", remoteRef, wantHash)
	}
}

func TestPushChangesDetachedHeadFailsClosed(t *testing.T) {
	repo := initTempGitRepo(t)
	runGitOutputForPushTest(t, repo, "checkout", "--detach")

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	err = pushChanges(true, false)
	if err == nil || !strings.Contains(err.Error(), "current checkout is detached") {
		t.Fatalf("pushChanges error = %v, want detached-checkout failure", err)
	}
}

func TestPushChangesMissingRemoteDefaultFailsClosed(t *testing.T) {
	repo := initTempGitRepo(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	runGitOutputForPushTest(t, repo, "init", "--bare", remote)
	runGitOutputForPushTest(t, repo, "remote", "add", "origin", remote)
	runGitOutputForPushTest(t, repo, "switch", "-c", "codex/missing-remote")

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	t.Setenv("BUCKLEY_REMOTE_NAME", "origin")

	err = pushChanges(true, false)
	if err == nil || !strings.Contains(err.Error(), "resolve remote default branch") {
		t.Fatalf("pushChanges error = %v, want missing-default failure", err)
	}
	if got := runGitOutputForPushTest(t, repo, "ls-remote", "origin", "refs/heads/codex/missing-remote"); got != "" {
		t.Fatalf("branch was pushed without a resolved remote default: %q", got)
	}
}

func TestVerifyPushedCommitRejectsSHAMismatch(t *testing.T) {
	repo := initTempGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "second.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatalf("write second commit: %v", err)
	}
	runGitOutputForPushTest(t, repo, "add", "second.txt")
	runGitOutputForPushTest(t, repo, "commit", "-m", "second")
	runGitOutputForPushTest(t, repo, "update-ref", "refs/remotes/origin/codex/checkpoint", "HEAD^")

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get wd: %v", err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatalf("chdir repo: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	err = verifyPushedCommit(context.Background(), "origin", "codex/checkpoint")
	if err == nil || !strings.Contains(err.Error(), "want") {
		t.Fatalf("verifyPushedCommit error = %v, want SHA mismatch", err)
	}
}

func initializeBareRemoteDefault(t *testing.T, repo, remote, branch string) {
	t.Helper()
	runGitOutputForPushTest(t, repo, "init", "--bare", remote)
	runGitOutputForPushTest(t, repo, "remote", "add", "origin", remote)
	runGitOutputForPushTest(t, repo, "--git-dir", remote, "symbolic-ref", "HEAD", "refs/heads/"+branch)
	runGitOutputForPushTest(t, repo, "push", "origin", "HEAD:refs/heads/"+branch)
}

func runGitOutputForPushTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, string(out))
	}
	return strings.TrimSpace(string(out))
}
