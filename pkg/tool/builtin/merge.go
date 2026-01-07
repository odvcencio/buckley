package builtin

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ListMergeConflictsTool reports files with merge conflicts and summaries
type ListMergeConflictsTool struct{ workDirAware }

func (t *ListMergeConflictsTool) Name() string {
	return "list_merge_conflicts"
}

func (t *ListMergeConflictsTool) Description() string {
	return "List files with git merge conflicts and summarize conflicting sections. Shows conflict markers (<<<<<<, =======, >>>>>>) and the differing content from each branch. Use this after a merge/rebase/cherry-pick to see all conflicts at once before resolving them."
}

func (t *ListMergeConflictsTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type:       "object",
		Properties: map[string]PropertySchema{},
		Required:   []string{},
	}
}

func (t *ListMergeConflictsTool) Execute(params map[string]any) (*Result, error) {
	if !toolExists("git") {
		return &Result{
			Success: false,
			Error:   "git is required for this tool",
		}, nil
	}

	ctx, cancel := t.execContext()
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "diff", "--name-only", "--diff-filter=U")
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
			Error:   fmt.Sprintf("git diff failed: %v\n%s", err, strings.TrimSpace(string(output))),
		}, nil
	}

	fileList := strings.Fields(strings.TrimSpace(string(output)))
	if len(fileList) == 0 {
		return &Result{
			Success: true,
			Data: map[string]any{
				"files": []map[string]any{},
				"count": 0,
			},
		}, nil
	}

	files := make([]map[string]any, 0, len(fileList))
	for _, file := range fileList {
		info, err := summarizeConflicts(file, t.workDir)
		if err != nil {
			return &Result{
				Success: false,
				Error:   err.Error(),
			}, nil
		}
		files = append(files, info)
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"files": files,
			"count": len(files),
		},
	}, nil
}

func summarizeConflicts(path string, workDir string) (map[string]any, error) {
	openPath := path
	if strings.TrimSpace(workDir) != "" {
		abs, err := resolvePath(workDir, path)
		if err != nil {
			return nil, err
		}
		openPath = abs
	}
	file, err := os.Open(openPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %v", path, err)
	}
	defer file.Close()

	type section struct {
		StartLine int
		EndLine   int
		Ours      string
		Theirs    string
	}

	var sections []section
	scanner := bufio.NewScanner(file)
	lineNum := 0
	var current *section
	collectingTheirs := false
	var oursBuilder strings.Builder
	var theirsBuilder strings.Builder

	for scanner.Scan() {
		line := scanner.Text()
		lineNum++
		switch {
		case strings.HasPrefix(line, "<<<<<<<"):
			current = &section{StartLine: lineNum}
			collectingTheirs = false
			oursBuilder.Reset()
			theirsBuilder.Reset()
		case strings.HasPrefix(line, "=======") && current != nil:
			collectingTheirs = true
		case strings.HasPrefix(line, ">>>>>>>") && current != nil:
			current.EndLine = lineNum
			current.Ours = strings.TrimSuffix(oursBuilder.String(), "\n")
			current.Theirs = strings.TrimSuffix(theirsBuilder.String(), "\n")
			sections = append(sections, *current)
			current = nil
		default:
			if current != nil {
				if collectingTheirs {
					theirsBuilder.WriteString(line)
					theirsBuilder.WriteByte('\n')
				} else {
					oursBuilder.WriteString(line)
					oursBuilder.WriteByte('\n')
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read %s: %v", path, err)
	}

	conflicts := make([]map[string]any, 0, len(sections))
	for _, s := range sections {
		conflicts = append(conflicts, map[string]any{
			"start_line": s.StartLine,
			"end_line":   s.EndLine,
			"ours":       s.Ours,
			"theirs":     s.Theirs,
		})
	}

	return map[string]any{
		"path":      path,
		"conflicts": conflicts,
	}, nil
}

// MarkResolvedTool stages a file after conflicts are resolved
type MarkResolvedTool struct{ workDirAware }

func (t *MarkResolvedTool) Name() string {
	return "mark_conflict_resolved"
}

func (t *MarkResolvedTool) Description() string {
	return "Mark a merge conflict as resolved by staging the file (git add). Use this after you have manually edited the file to remove conflict markers and chosen the correct content. This tells git that the conflict in this file has been resolved."
}

func (t *MarkResolvedTool) Parameters() ParameterSchema {
	return ParameterSchema{
		Type: "object",
		Properties: map[string]PropertySchema{
			"path": {
				Type:        "string",
				Description: "Path to the file to stage",
			},
		},
		Required: []string{"path"},
	}
}

func (t *MarkResolvedTool) Execute(params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok || strings.TrimSpace(path) == "" {
		return &Result{
			Success: false,
			Error:   "path parameter must be a non-empty string",
		}, nil
	}

	if !toolExists("git") {
		return &Result{
			Success: false,
			Error:   "git is required for this tool",
		}, nil
	}

	if strings.TrimSpace(t.workDir) != "" {
		_, rel, err := resolveRelPath(t.workDir, path)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
		path = rel
	}

	ctx, cancel := t.execContext()
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "add", path)
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
			Error:   fmt.Sprintf("git add failed: %v\n%s", err, strings.TrimSpace(string(output))),
		}, nil
	}

	return &Result{
		Success: true,
		Data: map[string]any{
			"path": path,
		},
	}, nil
}
