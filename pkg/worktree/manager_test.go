package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewManagerDefaultsToRepoScopedWorktrees(t *testing.T) {
	repo := initGitRepo(t)

	mgr, err := NewManager(repo, "")
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}

	expectedRoot := filepath.Join(repo, ".buckley", "worktrees")
	if mgr.worktreeRoot != expectedRoot {
		t.Fatalf("unexpected worktree root: got %s want %s", mgr.worktreeRoot, expectedRoot)
	}
}

func TestCreateListRemoveWorktree(t *testing.T) {
	repo := initGitRepo(t)

	mgr, err := NewManager(repo, "")
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}

	wt, err := mgr.Create("feature/test")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if !strings.HasPrefix(wt.Path, filepath.Join(repo, ".buckley", "worktrees")) {
		t.Fatalf("worktree path not under repo-scoped root: %s", wt.Path)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("worktree path does not exist: %v", err)
	}

	worktrees, err := mgr.List()
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	found := false
	for _, w := range worktrees {
		if w.Branch == "feature/test" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected feature/test branch in worktree list")
	}

	if err := mgr.Remove("feature/test", false); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("expected worktree path to be removed, got err=%v", err)
	}
}

func TestNewManagerRejectsNonRepo(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewManager(dir, ""); err == nil {
		t.Fatalf("expected error for non-git directory")
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0o644); err != nil {
		t.Fatalf("failed to write README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "init")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}
