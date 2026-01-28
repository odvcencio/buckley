// pkg/ralph/sandbox_test.go
package ralph

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSandbox_CreateWorktree(t *testing.T) {
	// Skip if git not available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Create a temp git repo
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")

	// Create initial commit
	testFile := filepath.Join(repoDir, "README.md")
	os.WriteFile(testFile, []byte("# Test"), 0644)
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-m", "initial")

	// Create sandbox
	sandbox := NewSandboxManager(repoDir)
	worktreePath := filepath.Join(t.TempDir(), "ralph-test")

	err := sandbox.CreateWorktree(worktreePath, "ralph/test-session")
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	// Verify worktree exists
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Fatal("worktree directory not created")
	}

	// Verify it's a git worktree
	gitDir := filepath.Join(worktreePath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		t.Fatal("worktree .git not found")
	}

	// Cleanup
	sandbox.RemoveWorktree(worktreePath)
}

func TestSandbox_CreateFreshDirectory(t *testing.T) {
	sandbox := NewSandboxManager("")
	freshPath := filepath.Join(t.TempDir(), "fresh-project")

	// Test without git init
	err := sandbox.CreateFreshDirectory(freshPath, false)
	if err != nil {
		t.Fatalf("CreateFreshDirectory failed: %v", err)
	}

	if _, err := os.Stat(freshPath); os.IsNotExist(err) {
		t.Fatal("directory not created")
	}

	// Should not have .git
	gitDir := filepath.Join(freshPath, ".git")
	if _, err := os.Stat(gitDir); !os.IsNotExist(err) {
		t.Fatal("unexpected .git directory when initGit=false")
	}
}

func TestSandbox_CreateFreshDirectory_WithGitInit(t *testing.T) {
	// Skip if git not available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	sandbox := NewSandboxManager("")
	freshPath := filepath.Join(t.TempDir(), "fresh-project-git")

	err := sandbox.CreateFreshDirectory(freshPath, true)
	if err != nil {
		t.Fatalf("CreateFreshDirectory with git init failed: %v", err)
	}

	// Should have .git
	gitDir := filepath.Join(freshPath, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		t.Fatal(".git directory not created when initGit=true")
	}
}

func TestSandbox_GetModifiedFiles(t *testing.T) {
	// Skip if git not available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Create a temp git repo
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")

	// Create initial commit
	testFile := filepath.Join(repoDir, "README.md")
	os.WriteFile(testFile, []byte("# Test"), 0644)
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-m", "initial")

	// Create sandbox manager
	sandbox := NewSandboxManager(repoDir)

	// No modified files initially
	files, err := sandbox.GetModifiedFiles(repoDir)
	if err != nil {
		t.Fatalf("GetModifiedFiles failed: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 modified files, got %d", len(files))
	}

	// Modify a file
	os.WriteFile(testFile, []byte("# Test modified"), 0644)

	files, err = sandbox.GetModifiedFiles(repoDir)
	if err != nil {
		t.Fatalf("GetModifiedFiles failed after modification: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 modified file, got %d", len(files))
	}
	if files[0] != "README.md" {
		t.Fatalf("expected README.md, got %s", files[0])
	}
}

func TestSandbox_RemoveWorktree(t *testing.T) {
	// Skip if git not available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Create a temp git repo
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")
	runGit(t, repoDir, "config", "user.email", "test@test.com")
	runGit(t, repoDir, "config", "user.name", "Test")

	// Create initial commit
	testFile := filepath.Join(repoDir, "README.md")
	os.WriteFile(testFile, []byte("# Test"), 0644)
	runGit(t, repoDir, "add", ".")
	runGit(t, repoDir, "commit", "-m", "initial")

	// Create sandbox and worktree
	sandbox := NewSandboxManager(repoDir)
	worktreePath := filepath.Join(t.TempDir(), "ralph-test-remove")

	err := sandbox.CreateWorktree(worktreePath, "ralph/test-remove")
	if err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	// Verify worktree exists
	if _, err := os.Stat(worktreePath); os.IsNotExist(err) {
		t.Fatal("worktree directory not created")
	}

	// Remove worktree
	err = sandbox.RemoveWorktree(worktreePath)
	if err != nil {
		t.Fatalf("RemoveWorktree failed: %v", err)
	}

	// Verify worktree is gone
	if _, err := os.Stat(worktreePath); !os.IsNotExist(err) {
		t.Fatal("worktree directory not removed")
	}
}

func TestIsGitRepo(t *testing.T) {
	// Skip if git not available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Non-git directory
	nonGitDir := t.TempDir()
	if IsGitRepo(nonGitDir) {
		t.Fatal("expected non-git directory to return false")
	}

	// Git directory
	gitDir := t.TempDir()
	runGit(t, gitDir, "init")
	if !IsGitRepo(gitDir) {
		t.Fatal("expected git directory to return true")
	}
}

func TestGetRepoRoot(t *testing.T) {
	// Skip if git not available
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// Create git repo with subdirectory
	repoDir := t.TempDir()
	runGit(t, repoDir, "init")

	subDir := filepath.Join(repoDir, "subdir", "nested")
	os.MkdirAll(subDir, 0755)

	// Get repo root from subdirectory
	root, err := GetRepoRoot(subDir)
	if err != nil {
		t.Fatalf("GetRepoRoot failed: %v", err)
	}

	if root != repoDir {
		t.Fatalf("expected root %s, got %s", repoDir, root)
	}

	// Non-git directory should error
	nonGitDir := t.TempDir()
	_, err = GetRepoRoot(nonGitDir)
	if err == nil {
		t.Fatal("expected error for non-git directory")
	}
}

func TestSandbox_NilReceiver(t *testing.T) {
	var sandbox *SandboxManager

	// CreateWorktree should return error
	err := sandbox.CreateWorktree("/tmp/test", "branch")
	if err == nil {
		t.Fatal("expected error for nil receiver on CreateWorktree")
	}

	// CreateFreshDirectory should return error
	err = sandbox.CreateFreshDirectory("/tmp/test", false)
	if err == nil {
		t.Fatal("expected error for nil receiver on CreateFreshDirectory")
	}

	// RemoveWorktree should not panic and return nil
	err = sandbox.RemoveWorktree("/tmp/test")
	if err != nil {
		t.Fatalf("RemoveWorktree on nil should return nil, got: %v", err)
	}

	// GetModifiedFiles should return error
	_, err = sandbox.GetModifiedFiles("/tmp/test")
	if err == nil {
		t.Fatal("expected error for nil receiver on GetModifiedFiles")
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}
