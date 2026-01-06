package builtin

import (
	"fmt"
	"os/exec"
	"strings"
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
