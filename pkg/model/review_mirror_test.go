package model

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReviewMirrorIdentity_StableAndCredentialFree(t *testing.T) {
	root := t.TempDir()
	gitEnv, err := reviewGitEnvironment(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	git("init", "--quiet")
	git("config", "remote.origin.url", "https://user:secret@github.com/owner/repo.git")
	if got := reviewMirrorIdentity(context.Background(), root, gitEnv); got != "github.com/owner/repo" {
		t.Fatalf("identity=%q", got)
	}
	git("config", "remote.origin.url", "git@github.com:owner/repo.git")
	if got := reviewMirrorIdentity(context.Background(), root, gitEnv); got != "github.com/owner/repo" {
		t.Fatalf("scp identity=%q", got)
	}
	mirror := filepath.Join(t.TempDir(), "mirror.git")
	if out, err := exec.Command("git", "init", "--bare", "--quiet", mirror).CombinedOutput(); err != nil {
		t.Fatalf("bare: %v %s", err, out)
	}
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "commit", "--allow-empty", "-qm", "fixture")
	git("push", "--quiet", mirror, "HEAD:refs/review/test")
	checkout := filepath.Join(t.TempDir(), "checkout")
	if out, err := exec.Command("git", "--git-dir", mirror, "worktree", "add", "--detach", checkout, "refs/review/test").CombinedOutput(); err != nil {
		t.Fatalf("checkout: %v %s", err, out)
	}
	if out, err := exec.Command("git", "--git-dir", mirror, "worktree", "remove", "--force", checkout).CombinedOutput(); err != nil {
		t.Fatalf("cleanup: %v %s", err, out)
	}
}
