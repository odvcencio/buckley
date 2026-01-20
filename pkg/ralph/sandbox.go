// pkg/ralph/sandbox.go
package ralph

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// SandboxManager handles isolated workspace creation using go-git.
type SandboxManager struct {
	repoRoot string
	repo     *git.Repository
}

// NewSandboxManager creates a new sandbox manager.
func NewSandboxManager(repoRoot string) *SandboxManager {
	mgr := &SandboxManager{repoRoot: repoRoot}
	// Try to open the repository
	if repo, err := git.PlainOpen(repoRoot); err == nil {
		mgr.repo = repo
	}
	return mgr
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

	// Use go-git's worktree support
	if s.repo != nil {
		// Get the current HEAD reference
		head, err := s.repo.Head()
		if err != nil {
			return fmt.Errorf("get HEAD: %w", err)
		}

		// Create a new branch from HEAD
		branchRef := plumbing.NewBranchReferenceName(branchName)
		ref := plumbing.NewHashReference(branchRef, head.Hash())
		if err := s.repo.Storer.SetReference(ref); err != nil {
			return fmt.Errorf("create branch reference: %w", err)
		}

		// Add the worktree
		wt, err := s.repo.Worktree()
		if err != nil {
			return fmt.Errorf("get worktree: %w", err)
		}

		// go-git doesn't have full worktree add support, fall back to CLI for this
		_ = wt
		cmd := exec.Command("git", "worktree", "add", path, branchName)
		cmd.Dir = s.repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git worktree add: %w\n%s", err, out)
		}
		return nil
	}

	// Fallback to CLI if repo not opened
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
		_, err := git.PlainInit(path, false)
		if err != nil {
			return fmt.Errorf("git init: %w", err)
		}
	}

	return nil
}

// RemoveWorktree removes a git worktree.
func (s *SandboxManager) RemoveWorktree(path string) error {
	if s == nil {
		return nil
	}

	// Use CLI for worktree removal (go-git doesn't fully support this)
	cmd := exec.Command("git", "worktree", "remove", "--force", path)
	cmd.Dir = s.repoRoot
	cmd.CombinedOutput() // Ignore errors - worktree might not exist

	// Clean up the directory if it still exists
	return os.RemoveAll(path)
}

// GetModifiedFiles returns files modified in the sandbox using go-git.
func (s *SandboxManager) GetModifiedFiles(sandboxPath string) ([]string, error) {
	if s == nil {
		return nil, fmt.Errorf("sandbox manager is nil")
	}

	repo, err := git.PlainOpen(sandboxPath)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("get worktree: %w", err)
	}

	status, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("get status: %w", err)
	}

	var files []string
	for file, st := range status {
		// Include any file that has changes (staged or unstaged)
		if st.Staging != git.Unmodified || st.Worktree != git.Unmodified {
			files = append(files, file)
		}
	}
	return files, nil
}

// IsGitRepo checks if the given path is inside a git repository.
func IsGitRepo(path string) bool {
	_, err := git.PlainOpenWithOptions(path, &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	return err == nil
}

// GetRepoRoot returns the root of the git repository containing path.
func GetRepoRoot(path string) (string, error) {
	repo, err := git.PlainOpenWithOptions(path, &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return "", fmt.Errorf("not a git repository: %w", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("get worktree: %w", err)
	}

	return wt.Filesystem.Root(), nil
}

// GetCurrentBranch returns the current branch name.
func GetCurrentBranch(repoPath string) (string, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("open repo: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("get HEAD: %w", err)
	}

	if head.Name().IsBranch() {
		return head.Name().Short(), nil
	}

	// Detached HEAD - return the hash
	return head.Hash().String()[:8], nil
}

// StageFiles stages the specified files for commit.
func StageFiles(repoPath string, files []string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	for _, file := range files {
		if _, err := wt.Add(file); err != nil {
			// Try to add even if file might be deleted
			continue
		}
	}

	return nil
}

// StageAll stages all changes (like git add -A).
func StageAll(repoPath string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("get worktree: %w", err)
	}

	// Add all changes
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return fmt.Errorf("stage all: %w", err)
	}

	return nil
}

// Commit creates a commit with the staged changes.
func Commit(repoPath, message, authorName, authorEmail string) (string, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("open repo: %w", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("get worktree: %w", err)
	}

	// Default author if not provided
	if authorName == "" {
		authorName = "Ralph"
	}
	if authorEmail == "" {
		authorEmail = "ralph@buckley.local"
	}

	commit, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{
			Name:  authorName,
			Email: authorEmail,
			When:  time.Now(),
		},
	})
	if err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	return commit.String(), nil
}

// PushBranch pushes a branch to the remote.
func PushBranch(repoPath, branch, remote string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}

	if remote == "" {
		remote = "origin"
	}

	// Get the remote
	rem, err := repo.Remote(remote)
	if err != nil {
		return fmt.Errorf("get remote %s: %w", remote, err)
	}

	// Push the branch
	refSpec := config.RefSpec(fmt.Sprintf("refs/heads/%s:refs/heads/%s", branch, branch))
	err = rem.Push(&git.PushOptions{
		RemoteName: remote,
		RefSpecs:   []config.RefSpec{refSpec},
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("push: %w", err)
	}

	return nil
}

// GetCommitLog returns the last N commits from the repository.
func GetCommitLog(repoPath string, limit int) ([]CommitInfo, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}

	// Get HEAD
	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("get HEAD: %w", err)
	}

	// Get commit iterator
	iter, err := repo.Log(&git.LogOptions{
		From:  head.Hash(),
		Order: git.LogOrderCommitterTime,
	})
	if err != nil {
		return nil, fmt.Errorf("get log: %w", err)
	}
	defer iter.Close()

	var commits []CommitInfo
	count := 0
	err = iter.ForEach(func(c *object.Commit) error {
		if limit > 0 && count >= limit {
			return fmt.Errorf("limit reached") // Stop iteration
		}
		commits = append(commits, CommitInfo{
			Hash:    c.Hash.String(),
			Message: c.Message,
			Author:  c.Author.Name,
			Email:   c.Author.Email,
			Time:    c.Author.When,
		})
		count++
		return nil
	})
	// Ignore "limit reached" error
	if err != nil && err.Error() != "limit reached" {
		return nil, err
	}

	return commits, nil
}

// CommitInfo contains information about a git commit.
type CommitInfo struct {
	Hash    string
	Message string
	Author  string
	Email   string
	Time    time.Time
}

// GetDiff returns the diff between the working tree and HEAD.
func GetDiff(repoPath string) (string, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("open repo: %w", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("get worktree: %w", err)
	}

	// Get HEAD commit
	head, err := repo.Head()
	if err != nil {
		// No commits yet, can't diff
		return "", nil
	}

	headCommit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return "", fmt.Errorf("get HEAD commit: %w", err)
	}

	headTree, err := headCommit.Tree()
	if err != nil {
		return "", fmt.Errorf("get HEAD tree: %w", err)
	}

	// Get current status for changed files
	status, err := wt.Status()
	if err != nil {
		return "", fmt.Errorf("get status: %w", err)
	}

	var diffParts []string
	for file, st := range status {
		if st.Worktree == git.Unmodified && st.Staging == git.Unmodified {
			continue
		}

		// Get file from HEAD tree
		var oldContent string
		if f, err := headTree.File(file); err == nil {
			if content, err := f.Contents(); err == nil {
				oldContent = content
			}
		}

		// Get current file content
		var newContent string
		fullPath := filepath.Join(repoPath, file)
		if data, err := os.ReadFile(fullPath); err == nil {
			newContent = string(data)
		}

		if oldContent != newContent {
			diffParts = append(diffParts, fmt.Sprintf("--- a/%s\n+++ b/%s\n", file, file))
			// Simple diff indicator (full diff would require a diff library)
			if oldContent == "" {
				diffParts = append(diffParts, fmt.Sprintf("@@ new file @@\n"))
			} else if newContent == "" {
				diffParts = append(diffParts, fmt.Sprintf("@@ deleted file @@\n"))
			} else {
				diffParts = append(diffParts, fmt.Sprintf("@@ modified @@\n"))
			}
		}
	}

	return strings.Join(diffParts, "\n"), nil
}

// CreatePR creates a pull request using gh CLI.
// Note: go-git doesn't support GitHub API, so we use gh CLI for this.
func CreatePR(repoPath, title, body, baseBranch string) (string, error) {
	args := []string{"pr", "create", "--title", title, "--body", body}
	if baseBranch != "" {
		args = append(args, "--base", baseBranch)
	}
	cmd := exec.Command("gh", args...)
	cmd.Dir = repoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("creating PR: %w\n%s", err, out)
	}
	// gh pr create outputs the PR URL
	return strings.TrimSpace(string(out)), nil
}

// HasUncommittedChanges checks if there are any uncommitted changes.
func HasUncommittedChanges(repoPath string) (bool, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return false, fmt.Errorf("open repo: %w", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return false, fmt.Errorf("get worktree: %w", err)
	}

	status, err := wt.Status()
	if err != nil {
		return false, fmt.Errorf("get status: %w", err)
	}

	return !status.IsClean(), nil
}

// GetRemoteURL returns the URL of the specified remote.
func GetRemoteURL(repoPath, remoteName string) (string, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("open repo: %w", err)
	}

	if remoteName == "" {
		remoteName = "origin"
	}

	remote, err := repo.Remote(remoteName)
	if err != nil {
		return "", fmt.Errorf("get remote: %w", err)
	}

	urls := remote.Config().URLs
	if len(urls) == 0 {
		return "", fmt.Errorf("no URLs for remote %s", remoteName)
	}

	return urls[0], nil
}
