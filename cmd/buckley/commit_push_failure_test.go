package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestPushChanges_DetachedHeadReturnsError(t *testing.T) {
	repo := initTempGitRepo(t)
	runGitOutputForPushTest(t, repo, "checkout", "--detach")

	t.Chdir(repo)

	var pushErr error
	out := captureStdout(t, func() {
		pushErr = pushChanges(true, false)
	})

	if pushErr == nil {
		t.Fatalf("pushChanges on detached HEAD returned nil error, want an error mentioning detached HEAD; stdout:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(pushErr.Error()), "detached") {
		t.Fatalf("pushChanges error = %q, want it to contain %q", pushErr.Error(), "detached")
	}
	if strings.Contains(out, "Pushed") {
		t.Fatalf("pushChanges reported success on detached HEAD; stdout:\n%s", out)
	}
}

func TestPushChanges_BranchResolutionFailureReturnsError(t *testing.T) {
	t.Chdir(t.TempDir())

	var pushErr error
	out := captureStdout(t, func() {
		pushErr = pushChanges(true, false)
	})

	if pushErr == nil {
		t.Fatalf("pushChanges outside a git repository returned nil error, want a *exec.ExitError; stdout:\n%s", out)
	}

	var exitErr *exec.ExitError
	if !errors.As(pushErr, &exitErr) {
		t.Fatalf("pushChanges error = %v (%T), want it to wrap *exec.ExitError", pushErr, pushErr)
	}
	if strings.Contains(out, "Pushed") {
		t.Fatalf("pushChanges reported success outside a git repository; stdout:\n%s", out)
	}
}
