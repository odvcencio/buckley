package worktree

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Manager manages git worktrees for feature development
type Manager struct {
	repoPath     string
	worktreeRoot string
}

// Worktree represents a git worktree
type Worktree struct {
	Path   string
	Branch string
}

// WorktreeInfo contains detailed information about a worktree
type WorktreeInfo struct {
	Path   string
	Branch string
	Commit string
}

// NewManager creates a new worktree manager
func NewManager(repoPath string, worktreeRoot string) (*Manager, error) {
	// Validate that we're in a git repository
	if !isGitRepo(repoPath) {
		return nil, fmt.Errorf("not a git repository: %s", repoPath)
	}

	worktreeRoot = strings.TrimSpace(worktreeRoot)

	// Use default worktree root if not specified.
	if worktreeRoot == "" {
		worktreeRoot = filepath.Join(repoPath, ".buckley", "worktrees")
	} else {
		worktreeRoot = expandHomeDir(worktreeRoot)
		if !filepath.IsAbs(worktreeRoot) {
			worktreeRoot = filepath.Join(repoPath, worktreeRoot)
		}
	}

	return &Manager{
		repoPath:     repoPath,
		worktreeRoot: filepath.Clean(worktreeRoot),
	}, nil
}

func expandHomeDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// isGitRepo checks if the given path is a git repository
func isGitRepo(path string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = path
	err := cmd.Run()
	return err == nil
}

// getRepoName extracts the repository name from the path
func (wm *Manager) getRepoName() string {
	// Try to get remote origin URL first
	cmd := exec.Command("git", "config", "--get", "remote.origin.url")
	cmd.Dir = wm.repoPath
	output, err := cmd.Output()
	if err == nil {
		// Parse repo name from URL
		url := strings.TrimSpace(string(output))
		// Handle URLs like git@github.com:user/repo.git or https://github.com/user/repo.git
		parts := strings.Split(url, "/")
		if len(parts) > 0 {
			repoName := parts[len(parts)-1]
			repoName = strings.TrimSuffix(repoName, ".git")
			if repoName != "" {
				return repoName
			}
		}
	}

	// Fallback to directory name
	return filepath.Base(wm.repoPath)
}

// getWorktreePath returns the path for a given branch name
func (wm *Manager) getWorktreePath(branchName string) string {
	repoName := wm.getRepoName()
	return filepath.Join(wm.worktreeRoot, repoName, branchName, "source")
}

// Create creates a new worktree for the given branch name
func (wm *Manager) Create(branchName string) (*Worktree, error) {
	// Get the worktree path
	wtPath := wm.getWorktreePath(branchName)

	// Check if worktree path already exists
	if _, err := os.Stat(wtPath); err == nil {
		return nil, fmt.Errorf("worktree path already exists: %s", wtPath)
	}

	// Create parent directory
	if err := os.MkdirAll(filepath.Dir(wtPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create worktree directory: %w", err)
	}

	// Create the worktree
	cmd := exec.Command("git", "worktree", "add", "-b", branchName, wtPath, "HEAD")
	cmd.Dir = wm.repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to create worktree: %w\nOutput: %s", err, string(output))
	}

	return &Worktree{
		Path:   wtPath,
		Branch: branchName,
	}, nil
}

// CreateWithSpec creates a worktree and provisions containers based on a spec.
func (wm *Manager) CreateWithSpec(branchName string, spec *ContainerSpec) (*Worktree, error) {
	wt, err := wm.Create(branchName)
	if err != nil {
		return nil, err
	}
	if spec == nil {
		return wt, nil
	}

	if err := wm.setupContainersWithSpec(wt.Path, spec); err != nil {
		_ = wm.Remove(branchName, true)
		return nil, err
	}
	return wt, nil
}

// List returns all worktrees
func (wm *Manager) List() ([]WorktreeInfo, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = wm.repoPath
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to list worktrees: %w", err)
	}

	return parseWorktreeList(output), nil
}

// parseWorktreeList parses the output of `git worktree list --porcelain`
func parseWorktreeList(output []byte) []WorktreeInfo {
	worktrees := []WorktreeInfo{}
	scanner := bufio.NewScanner(strings.NewReader(string(output)))

	var currentWorktree WorktreeInfo
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			// Empty line indicates end of worktree entry
			if currentWorktree.Path != "" {
				worktrees = append(worktrees, currentWorktree)
				currentWorktree = WorktreeInfo{}
			}
			continue
		}

		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 2 {
			continue
		}

		key := parts[0]
		value := parts[1]

		switch key {
		case "worktree":
			currentWorktree.Path = value
		case "HEAD":
			currentWorktree.Commit = value
		case "branch":
			// Format: refs/heads/branch-name
			currentWorktree.Branch = strings.TrimPrefix(value, "refs/heads/")
		}
	}

	// Add the last worktree if exists
	if currentWorktree.Path != "" {
		worktrees = append(worktrees, currentWorktree)
	}

	return worktrees
}

// Remove removes a worktree and optionally deletes the branch
func (wm *Manager) Remove(branchName string, deleteBranch bool) error {
	wtPath := wm.getWorktreePath(branchName)

	// Check if worktree exists
	if _, err := os.Stat(wtPath); os.IsNotExist(err) {
		return fmt.Errorf("worktree does not exist: %s", wtPath)
	}

	// Remove the worktree
	cmd := exec.Command("git", "worktree", "remove", wtPath)
	cmd.Dir = wm.repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Try force remove if regular remove fails
		cmd = exec.Command("git", "worktree", "remove", "--force", wtPath)
		cmd.Dir = wm.repoPath
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to remove worktree: %w\nOutput: %s", err, string(output))
		}
	}
	_ = output // Output captured for error case above

	// Delete the branch if requested
	if deleteBranch {
		// Try to delete with -d (safe delete, only if merged)
		cmd = exec.Command("git", "branch", "-d", branchName)
		cmd.Dir = wm.repoPath
		err = cmd.Run()
		if err != nil {
			// If that fails, the branch is not merged
			// We don't force delete to prevent data loss
			return fmt.Errorf("worktree removed but branch not deleted (not merged): %s", branchName)
		}
	}

	return nil
}

// GetRepoPath returns the repository path
func (wm *Manager) GetRepoPath() string {
	return wm.repoPath
}

// GetWorktreeRoot returns the worktree root path
func (wm *Manager) GetWorktreeRoot() string {
	return wm.worktreeRoot
}
