package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPushChangesMinimalOutputIncludesHeadHash(t *testing.T) {
	repo := initTempGitRepo(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	runGitOutputForPushTest(t, repo, "init", "--bare", remote)
	runGitOutputForPushTest(t, repo, "remote", "add", "origin", remote)

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
