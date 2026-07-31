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
	return "Show git working tree status: modified, staged, and untracked files."
}

func (t *GitStatusTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Repository or directory path to inspect (default: current workdir)",
			},
			"repo_path": {
				Type:        "string",
				Description: "Alias for path",
			},
		},
		Required: []string{},
	}
}

func (t *GitStatusTool) Execute(params map[string]any) (*Result, error) {
	dir, err := gitCommandDir(t.workDir, params)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	ctx, cancel := t.execContext()
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = strings.TrimSpace(dir)
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
			Error:   formatGitFailure(err, string(output)),
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
	return "Show git diff: unstaged changes (default) or staged changes (--cached)."
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
			"path": {
				Type:        "string",
				Description: "Repository or directory path to diff in (default: current workdir)",
			},
			"repo_path": {
				Type:        "string",
				Description: "Alias for path",
			},
		},
		Required: []string{},
	}
}

func (t *GitDiffTool) Execute(params map[string]any) (*Result, error) {
	dir, err := gitCommandDir(t.workDir, params)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	args := []string{"diff"}

	if staged, ok := params["staged"].(bool); ok && staged {
		args = append(args, "--cached")
	}

	if file, ok := params["file"].(string); ok && file != "" {
		if strings.TrimSpace(dir) != "" {
			_, rel, err := resolveRelPath(dir, file)
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
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = strings.TrimSpace(dir)
	}
	cmd.Env = mergeEnv(cmd.Env, t.env)
	stdout := newLimitedBuffer(t.maxOutputBytes)
	stderr := newLimitedBuffer(t.maxOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err = cmd.Run()

	if ctx.Err() != nil {
		return &Result{
			Success: false,
			Error:   "git command timed out",
		}, nil
	}

	if err != nil {
		return &Result{
			Success: false,
			Error:   formatGitFailure(err, stderr.String()),
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
	return "Show git commit history (default: last 10 commits, oneline format)."
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
			"path": {
				Type:        "string",
				Description: "Repository or directory path to show log for (default: current workdir)",
			},
			"repo_path": {
				Type:        "string",
				Description: "Alias for path",
			},
		},
		Required: []string{},
	}
}

func (t *GitLogTool) Execute(params map[string]any) (*Result, error) {
	dir, err := gitCommandDir(t.workDir, params)
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	count := 10
	if c, ok := params["count"].(float64); ok {
		count = int(c)
	} else if c, ok := params["count"].(int); ok {
		count = c
	}

	args := []string{"log", fmt.Sprintf("-n%d", count)}

	oneline := true
	if configured, ok := params["oneline"].(bool); ok {
		oneline = configured
	}
	if oneline {
		args = append(args, "--oneline")
	}

	ctx, cancel := t.execContext()
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = strings.TrimSpace(dir)
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
			Error:   formatGitFailure(err, string(output)),
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
	return "Show line-by-line authorship for a file: commit hash, author, date, and content."
}

func (t *GitBlameTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"file": {
				Type:        "string",
				Description: "File to show blame for",
			},
			"path": {
				Type:        "string",
				Description: "Alias for file (compatibility)",
			},
			"repo_path": {
				Type:        "string",
				Description: "Repository or directory path to run git blame in (default: current workdir)",
			},
		},
		Required: []string{},
	}
}

func (t *GitBlameTool) Execute(params map[string]any) (*Result, error) {
	file, ok := params["file"].(string)
	if !ok || strings.TrimSpace(file) == "" {
		file, ok = params["path"].(string)
	}
	if !ok {
		return &Result{
			Success: false,
			Error:   "file parameter required",
		}, nil
	}

	dir, err := gitCommandDir(t.workDir, map[string]any{"repo_path": params["repo_path"]})
	if err != nil {
		return &Result{Success: false, Error: err.Error()}, nil
	}

	if strings.TrimSpace(dir) != "" {
		_, rel, err := resolveRelPath(dir, file)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		file = rel
	}

	ctx, cancel := t.execContext()
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "blame", file)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = strings.TrimSpace(dir)
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
			Error:   formatGitFailure(err, string(output)),
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

func gitCommandDir(workDir string, params map[string]any) (string, error) {
	dir := strings.TrimSpace(workDir)
	raw, _ := params["repo_path"].(string)
	if strings.TrimSpace(raw) == "" {
		raw, _ = params["path"].(string)
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return dir, nil
	}
	resolved, err := resolvePath(workDir, raw)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func formatGitFailure(err error, output string) string {
	message := fmt.Sprintf("git command failed: %v", err)
	output = strings.TrimSpace(output)
	if output == "" {
		return message
	}
	return message + ": " + output
}
