package builtin

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/odvcencio/buckley/pkg/ralph"
)

// GitStatusTool shows git status
type GitStatusTool struct{ workDirAware }

func (t *GitStatusTool) Name() string {
	return "git_status"
}

func (t *GitStatusTool) Description() string {
	return "Show git working tree status including modified, staged, and untracked files. Use this to see what changes have been made before committing or to check repository state."
}

func (t *GitStatusTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type:       "object",
		Properties: map[string]PropertySchema{},
		Required:   []string{},
	}
}

func (t *GitStatusTool) Execute(params map[string]any) (*Result, error) {
	ctx, cancel := t.execContext()
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	if strings.TrimSpace(t.workDir) != "" {
		cmd.Dir = strings.TrimSpace(t.workDir)
	}
	cmd.Env = mergeEnv(cmd.Env, t.env)
	output, err := cmd.CombinedOutput()

	if ctx.Err() != nil {
		return &Result{
			Success: false,
			Error:   "git command timed out",
		}, nil
	}

	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("git command failed: %v", err),
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"status": string(output),
		},
	}, nil
}

// GitDiffTool shows git diff
type GitDiffTool struct{ workDirAware }

func (t *GitDiffTool) Name() string {
	return "git_diff"
}

func (t *GitDiffTool) Description() string {
	return "Show git diff of changes. Can show unstaged changes (default), staged changes (--cached), or differences between specific files/commits. Use this to review code changes before committing or to compare versions."
}

func (t *GitDiffTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"staged": {
				Type:        "boolean",
				Description: "Show staged changes (--cached)",
				Default:     false,
			},
			"file": {
				Type:        "string",
				Description: "Limit diff to specific file",
			},
		},
		Required: []string{},
	}
}

func (t *GitDiffTool) Execute(params map[string]any) (*Result, error) {
	args := []string{"diff"}

	if staged, ok := params["staged"].(bool); ok && staged {
		args = append(args, "--cached")
	}

	if file, ok := params["file"].(string); ok && file != "" {
		if strings.TrimSpace(t.workDir) != "" {
			_, rel, err := resolveRelPath(t.workDir, file)
			if err != nil {
				return &Result{Success: false, Error: err.Error()}, nil
			}
			file = rel
		}
		args = append(args, "--", file)
	}

	ctx, cancel := t.execContext()
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	if strings.TrimSpace(t.workDir) != "" {
		cmd.Dir = strings.TrimSpace(t.workDir)
	}
	cmd.Env = mergeEnv(cmd.Env, t.env)
	stdout := newLimitedBuffer(t.maxOutputBytes)
	stderr := newLimitedBuffer(t.maxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()

	if ctx.Err() != nil {
		return &Result{
			Success: false,
			Error:   "git command timed out",
		}, nil
	}

	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("git command failed: %v", err),
		}, nil
	}

	diff := stdout.String()
	data := map[string]any{
		"diff": diff,
	}
	if stdout.Truncated() {
		data["diff_truncated"] = true
	}
	result := &Result{
		Success: true,
		Data:    data,
	}
	if stdout.Truncated() {
		result.ShouldAbridge = true
		result.DisplayData = data
	}

	return result, nil
}

// GitLogTool shows git log
type GitLogTool struct{ workDirAware }

func (t *GitLogTool) Name() string {
	return "git_log"
}

func (t *GitLogTool) Description() string {
	return "Show git commit history with configurable count and format. Defaults to last 10 commits in oneline format. Use this to review recent changes, find when features were added, or trace bug origins."
}

func (t *GitLogTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"count": {
				Type:        "number",
				Description: "Number of commits to show",
				Default:     10,
			},
			"oneline": {
				Type:        "boolean",
				Description: "Show one line per commit",
				Default:     true,
			},
		},
		Required: []string{},
	}
}

func (t *GitLogTool) Execute(params map[string]any) (*Result, error) {
	count := 10
	if c, ok := params["count"].(float64); ok {
		count = int(c)
	}

	args := []string{"log", fmt.Sprintf("-n%d", count)}

	if oneline, ok := params["oneline"].(bool); ok && oneline {
		args = append(args, "--oneline")
	}

	ctx, cancel := t.execContext()
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	if strings.TrimSpace(t.workDir) != "" {
		cmd.Dir = strings.TrimSpace(t.workDir)
	}
	cmd.Env = mergeEnv(cmd.Env, t.env)
	output, err := cmd.CombinedOutput()

	if ctx.Err() != nil {
		return &Result{
			Success: false,
			Error:   "git command timed out",
		}, nil
	}

	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("git command failed: %v", err),
		}, nil
	}

	// Parse commits
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	commits := []string{}
	for _, line := range lines {
		if line != "" {
			commits = append(commits, line)
		}
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"commits": commits,
			"count":   len(commits),
		},
	}, nil
}

// GitBlameTool shows git blame
type GitBlameTool struct{ workDirAware }

func (t *GitBlameTool) Name() string {
	return "git_blame"
}

func (t *GitBlameTool) Description() string {
	return "Show line-by-line authorship and commit information for a file. Each line shows the commit hash, author, date, and content. Use this to track who wrote specific code, when changes were made, or to find the commit that introduced a bug."
}

func (t *GitBlameTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"file": {
				Type:        "string",
				Description: "File to show blame for",
			},
		},
		Required: []string{"file"},
	}
}

func (t *GitBlameTool) Execute(params map[string]any) (*Result, error) {
	file, ok := params["file"].(string)
	if !ok {
		return &Result{
			Success: false,
			Error:   "file parameter required",
		}, nil
	}

	if strings.TrimSpace(t.workDir) != "" {
		_, rel, err := resolveRelPath(t.workDir, file)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		file = rel
	}

	ctx, cancel := t.execContext()
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "blame", file)
	if strings.TrimSpace(t.workDir) != "" {
		cmd.Dir = strings.TrimSpace(t.workDir)
	}
	cmd.Env = mergeEnv(cmd.Env, t.env)
	output, err := cmd.CombinedOutput()

	if ctx.Err() != nil {
		return &Result{
			Success: false,
			Error:   "git command timed out",
		}, nil
	}

	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("git command failed: %v", err),
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"file":  file,
			"blame": string(output),
		},
	}, nil
}

// GitStageFilesTool stages specific files using go-git
type GitStageFilesTool struct{ workDirAware }

func (t *GitStageFilesTool) Name() string {
	return "git_stage_files"
}

func (t *GitStageFilesTool) Description() string {
	return "Stage specific files for commit. Use this to selectively add files to the staging area before committing."
}

func (t *GitStageFilesTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"files": {
				Type:        "array",
				Description: "List of file paths to stage",
				Items:       &PropertySchema{Type: "string"},
			},
		},
		Required: []string{"files"},
	}
}

func (t *GitStageFilesTool) Execute(params map[string]any) (*Result, error) {
	filesRaw, ok := params["files"].([]any)
	if !ok {
		return &Result{
			Success: false,
			Error:   "files parameter must be an array of strings",
		}, nil
	}

	var files []string
	for _, f := range filesRaw {
		if s, ok := f.(string); ok {
			files = append(files, s)
		}
	}

	if len(files) == 0 {
		return &Result{
			Success: false,
			Error:   "no files specified",
		}, nil
	}

	repoPath := t.workDir
	if repoPath == "" {
		repoPath = "."
	}

	if err := ralph.StageFiles(repoPath, files); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("staging files: %v", err),
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"staged": files,
			"count":  len(files),
		},
	}, nil
}

// GitStageAllTool stages all changes using go-git
type GitStageAllTool struct{ workDirAware }

func (t *GitStageAllTool) Name() string {
	return "git_stage_all"
}

func (t *GitStageAllTool) Description() string {
	return "Stage all changes (like git add -A). Use this to add all modified, deleted, and new files to staging."
}

func (t *GitStageAllTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type:       "object",
		Properties: map[string]PropertySchema{},
		Required:   []string{},
	}
}

func (t *GitStageAllTool) Execute(params map[string]any) (*Result, error) {
	repoPath := t.workDir
	if repoPath == "" {
		repoPath = "."
	}

	if err := ralph.StageAll(repoPath); err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("staging all: %v", err),
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"message": "all changes staged",
		},
	}, nil
}

// GitCommitTool creates a commit using go-git
type GitCommitTool struct{ workDirAware }

func (t *GitCommitTool) Name() string {
	return "git_commit"
}

func (t *GitCommitTool) Description() string {
	return "Create a git commit with the staged changes. Requires a commit message. Optionally specify author name and email."
}

func (t *GitCommitTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"message": {
				Type:        "string",
				Description: "Commit message",
			},
			"author_name": {
				Type:        "string",
				Description: "Author name (defaults to Ralph)",
			},
			"author_email": {
				Type:        "string",
				Description: "Author email (defaults to ralph@buckley.local)",
			},
		},
		Required: []string{"message"},
	}
}

func (t *GitCommitTool) Execute(params map[string]any) (*Result, error) {
	message, ok := params["message"].(string)
	if !ok || message == "" {
		return &Result{
			Success: false,
			Error:   "commit message required",
		}, nil
	}

	authorName, _ := params["author_name"].(string)
	authorEmail, _ := params["author_email"].(string)

	repoPath := t.workDir
	if repoPath == "" {
		repoPath = "."
	}

	hash, err := ralph.Commit(repoPath, message, authorName, authorEmail)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("creating commit: %v", err),
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"hash":    hash,
			"message": message,
		},
	}, nil
}

// GitBranchTool shows the current branch using go-git
type GitBranchTool struct{ workDirAware }

func (t *GitBranchTool) Name() string {
	return "git_branch"
}

func (t *GitBranchTool) Description() string {
	return "Show the current git branch name. Returns the branch name or short commit hash if in detached HEAD state."
}

func (t *GitBranchTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type:       "object",
		Properties: map[string]PropertySchema{},
		Required:   []string{},
	}
}

func (t *GitBranchTool) Execute(params map[string]any) (*Result, error) {
	repoPath := t.workDir
	if repoPath == "" {
		repoPath = "."
	}

	branch, err := ralph.GetCurrentBranch(repoPath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("getting branch: %v", err),
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"branch": branch,
		},
	}, nil
}

// GitCheckUncommittedTool checks for uncommitted changes using go-git
type GitCheckUncommittedTool struct{ workDirAware }

func (t *GitCheckUncommittedTool) Name() string {
	return "git_check_uncommitted"
}

func (t *GitCheckUncommittedTool) Description() string {
	return "Check if there are any uncommitted changes in the repository. Returns true if there are staged or unstaged changes."
}

func (t *GitCheckUncommittedTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type:       "object",
		Properties: map[string]PropertySchema{},
		Required:   []string{},
	}
}

func (t *GitCheckUncommittedTool) Execute(params map[string]any) (*Result, error) {
	repoPath := t.workDir
	if repoPath == "" {
		repoPath = "."
	}

	hasChanges, err := ralph.HasUncommittedChanges(repoPath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("checking uncommitted: %v", err),
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"has_uncommitted": hasChanges,
		},
	}, nil
}

// GitGetModifiedFilesTool returns list of modified files using go-git
type GitGetModifiedFilesTool struct {
	workDirAware
	sandboxMgr *ralph.SandboxManager
}

func (t *GitGetModifiedFilesTool) Name() string {
	return "git_get_modified"
}

func (t *GitGetModifiedFilesTool) Description() string {
	return "Get a list of all modified files (staged and unstaged). Useful for reviewing what has changed before committing."
}

func (t *GitGetModifiedFilesTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type:       "object",
		Properties: map[string]PropertySchema{},
		Required:   []string{},
	}
}

func (t *GitGetModifiedFilesTool) Execute(params map[string]any) (*Result, error) {
	repoPath := t.workDir
	if repoPath == "" {
		repoPath = "."
	}

	// Use sandbox manager if available, otherwise create one
	mgr := t.sandboxMgr
	if mgr == nil {
		mgr = ralph.NewSandboxManager(repoPath)
	}

	files, err := mgr.GetModifiedFiles(repoPath)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("getting modified files: %v", err),
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"files": files,
			"count": len(files),
		},
	}, nil
}

// GitCommitLogTool returns commit history using go-git
type GitCommitLogTool struct{ workDirAware }

func (t *GitCommitLogTool) Name() string {
	return "git_commit_log"
}

func (t *GitCommitLogTool) Description() string {
	return "Get detailed commit history using go-git. Returns structured commit information including hash, message, author, email, and timestamp."
}

func (t *GitCommitLogTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"limit": {
				Type:        "number",
				Description: "Maximum number of commits to return (default 10)",
				Default:     10,
			},
		},
		Required: []string{},
	}
}

func (t *GitCommitLogTool) Execute(params map[string]any) (*Result, error) {
	limit := 10
	if l, ok := params["limit"].(float64); ok {
		limit = int(l)
	}

	repoPath := t.workDir
	if repoPath == "" {
		repoPath = "."
	}

	commits, err := ralph.GetCommitLog(repoPath, limit)
	if err != nil {
		return &Result{
			Success: false,
			Error:   fmt.Sprintf("getting commit log: %v", err),
		}, nil
	}

	// Convert to serializable format
	commitData := make([]map[string]any, 0, len(commits))
	for _, c := range commits {
		commitData = append(commitData, map[string]any{
			"hash":    c.Hash,
			"message": c.Message,
			"author":  c.Author,
			"email":   c.Email,
			"time":    c.Time.Format("2006-01-02 15:04:05"),
		})
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"commits": commitData,
			"count":   len(commitData),
		},
	}, nil
}

// GitTool is a tool interface for git operations
type GitTool interface {
	Name() string
	Description() string
	Parameters() ParameterSchema
	Execute(params map[string]any) (*Result, error)
}

// GitToolRegistry is an interface for registering git tools
type GitToolRegistry interface {
	Register(GitTool)
}

// AllGitTools returns all available git tools
func AllGitTools() []GitTool {
	return []GitTool{
		// CLI-based tools
		&GitStatusTool{},
		&GitDiffTool{},
		&GitLogTool{},
		&GitBlameTool{},
		// go-git based tools
		&GitStageFilesTool{},
		&GitStageAllTool{},
		&GitCommitTool{},
		&GitBranchTool{},
		&GitCheckUncommittedTool{},
		&GitGetModifiedFilesTool{},
		&GitCommitLogTool{},
	}
}

// RegisterGitTools registers all git tools with a registry
func RegisterGitTools(registry GitToolRegistry) {
	for _, tool := range AllGitTools() {
		registry.Register(tool)
	}
}
