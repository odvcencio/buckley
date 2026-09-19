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

func TestRemoveDirtyWorktreePreservesExperimentOutput(t *testing.T) {
	repo := initGitRepo(t)

	mgr, err := NewManager(repo, "")
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}

	wt, err := mgr.Create("feature/preserve-work")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	readmePath := filepath.Join(wt.Path, "README.md")
	stagedPath := filepath.Join(wt.Path, "staged.txt")
	untrackedPath := filepath.Join(wt.Path, "notes.txt")
	if err := os.WriteFile(readmePath, []byte("# changed\n"), 0o644); err != nil {
		t.Fatalf("write tracked change: %v", err)
	}
	if err := os.WriteFile(stagedPath, []byte("staged experiment output\n"), 0o644); err != nil {
		t.Fatalf("write staged output: %v", err)
	}
	runGit(t, wt.Path, "add", "README.md", "staged.txt")
	if err := os.WriteFile(untrackedPath, []byte("untracked experiment output\n"), 0o644); err != nil {
		t.Fatalf("write untracked output: %v", err)
	}

	err = mgr.Remove("feature/preserve-work", false)
	if err == nil {
		t.Fatal("Remove succeeded on dirty worktree; want preservation error")
	}
	if got := err.Error(); !strings.Contains(got, "retained path") || !strings.Contains(got, wt.Path) {
		t.Fatalf("Remove error = %q, want retained path", got)
	}
	if _, err := os.Stat(wt.Path); err != nil {
		t.Fatalf("dirty worktree path was not preserved: %v", err)
	}
	assertFileContent(t, readmePath, "# changed\n")
	assertFileContent(t, stagedPath, "staged experiment output\n")
	assertFileContent(t, untrackedPath, "untracked experiment output\n")
	status := gitOutput(t, wt.Path, "status", "--short")
	for _, want := range []string{"M  README.md", "A  staged.txt", "?? notes.txt"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status missing %q:\n%s", want, status)
		}
	}
	worktrees := gitOutput(t, repo, "worktree", "list", "--porcelain")
	if !strings.Contains(worktrees, wt.Path) {
		t.Fatalf("worktree list no longer contains retained path %s:\n%s", wt.Path, worktrees)
	}
	branches := gitOutput(t, repo, "branch", "--list", "feature/preserve-work")
	if !strings.Contains(branches, "feature/preserve-work") {
		t.Fatalf("branch was deleted after failed cleanup:\n%s", branches)
	}
}

func TestRemoveCleanWorktreeDeletesMergedBranchWhenRequested(t *testing.T) {
	repo := initGitRepo(t)

	mgr, err := NewManager(repo, "")
	if err != nil {
		t.Fatalf("NewManager returned error: %v", err)
	}

	wt, err := mgr.Create("feature/delete-clean")
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if err := mgr.Remove("feature/delete-clean", true); err != nil {
		t.Fatalf("Remove returned error: %v", err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Fatalf("expected worktree path to be removed, got err=%v", err)
	}
	branches := gitOutput(t, repo, "branch", "--list", "feature/delete-clean")
	if strings.Contains(branches, "feature/delete-clean") {
		t.Fatalf("merged branch was not deleted:\n%s", branches)
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

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}
