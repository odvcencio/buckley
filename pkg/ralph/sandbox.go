// pkg/ralph/sandbox.go
package ralph

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SandboxManager handles isolated workspace creation.
type SandboxManager struct {
	repoRoot string
}

// NewSandboxManager creates a new sandbox manager.
func NewSandboxManager(repoRoot string) *SandboxManager {
	return &SandboxManager{repoRoot: repoRoot}
}

// CreateWorktree creates a git worktree for isolated execution.
func (s *SandboxManager) CreateWorktree(path, branchName string) error {
	if s == nil {
		return fmt.Errorf("sandbox manager is nil")
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	// Create worktree with new branch
	cmd := exec.Command("git", "worktree", "add", "-b", branchName, path)
	cmd.Dir = s.repoRoot
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %w\n%s", err, out)
	}

	return nil
}

// CreateFreshDirectory creates a new directory for non-git projects.
func (s *SandboxManager) CreateFreshDirectory(path string, initGit bool) error {
	if s == nil {
		return fmt.Errorf("sandbox manager is nil")
	}

	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	if initGit {
		cmd := exec.Command("git", "init")
		cmd.Dir = path
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git init: %w\n%s", err, out)
		}
	}

	return nil
}

// RemoveWorktree removes a git worktree.
func (s *SandboxManager) RemoveWorktree(path string) error {
	if s == nil {
		return nil
	}

	// First, remove from git worktree list
	cmd := exec.Command("git", "worktree", "remove", "--force", path)
	cmd.Dir = s.repoRoot
	cmd.CombinedOutput() // Ignore errors - worktree might not exist

	// Clean up the directory if it still exists
	return os.RemoveAll(path)
}

// GetModifiedFiles returns files modified in the sandbox.
func (s *SandboxManager) GetModifiedFiles(sandboxPath string) ([]string, error) {
	if s == nil {
		return nil, fmt.Errorf("sandbox manager is nil")
	}

	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = sandboxPath
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		// git status --porcelain format: XY filename
		// XY is 2 chars (index status, worktree status), followed by space, then filename
		if len(line) >= 3 {
			files = append(files, line[3:])
		}
	}
	return files, nil
}

// IsGitRepo checks if the given path is inside a git repository.
func IsGitRepo(path string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path
	return cmd.Run() == nil
}

// GetRepoRoot returns the root of the git repository containing path.
func GetRepoRoot(path string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = path
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not a git repository")
	}
	return strings.TrimSpace(string(out)), nil
}
