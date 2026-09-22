package builtin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strings"
	"unicode/utf8"
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
	return "Show unstaged or staged git diff in bounded byte pages. Use next_byte_offset with the returned diff_sha256 to inspect the rest of a large patch; file narrows the diff."
}

const (
	defaultDiffPageBytes = 4096
	maxDiffPageBytes     = 8192
	// JSON numbers remain exact through this offset on the model tool path.
	maxDiffByteOffset = 1<<53 - 1
)

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
			"byte_offset": {
				Type:        "number",
				Description: "Byte offset to resume reading the diff (default 0; use next_byte_offset from the previous page)",
			},
			"max_bytes": {
				Type:        "number",
				Description: "Maximum diff bytes in this page (default 4096, range 4-8192)",
			},
			"expected_sha256": {
				Type:        "string",
				Description: "Diff hash from the previous page; reject a changed diff instead of mixing revisions",
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
	offset := int64(0)
	if value, ok := params["byte_offset"]; ok {
		offset, err = diffPageNumber("byte_offset", value, 0, maxDiffByteOffset)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
	}
	pageBytes := int64(defaultDiffPageBytes)
	if value, ok := params["max_bytes"]; ok {
		pageBytes, err = diffPageNumber("max_bytes", value, 4, maxDiffPageBytes)
		if err != nil {
			return &Result{Success: false, Error: err.Error()}, nil
		}
	}
	if t.maxOutputBytes > 0 && pageBytes > int64(t.maxOutputBytes) {
		pageBytes = int64(t.maxOutputBytes)
	}
	expected, ok := params["expected_sha256"]
	if ok {
		value, isString := expected.(string)
		if !isString || len(value) != sha256.Size*2 {
			return &Result{Success: false, Error: "expected_sha256 must be a SHA-256 hex digest"}, nil
		}
		if _, err := hex.DecodeString(value); err != nil {
			return &Result{Success: false, Error: "expected_sha256 must be a SHA-256 hex digest"}, nil
		}
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
	stdout := &diffPageWriter{offset: offset, limit: int(pageBytes)}
	digest := sha256.New()
	stderr := newLimitedBuffer(t.maxOutputBytes)
	cmd.Stdout = io.MultiWriter(stdout, digest)
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

	if expected != nil && !strings.EqualFold(expected.(string), hex.EncodeToString(digest.Sum(nil))) {
		return &Result{Success: false, Error: "diff changed since the previous page; restart at byte_offset 0"}, nil
	}
	if offset > stdout.total {
		return &Result{Success: false, Error: fmt.Sprintf("byte_offset %d exceeds diff length %d", offset, stdout.total)}, nil
	}
	diff := stdout.buf.Bytes()
	if offset+int64(len(diff)) < stdout.total {
		// End at a complete line when possible, while keeping exact byte offsets.
		if lastNewline := bytes.LastIndexByte(diff, '\n'); lastNewline >= len(diff)/4 {
			diff = diff[:lastNewline+1]
		} else {
			for len(diff) > 0 && !utf8.Valid(diff) {
				diff = diff[:len(diff)-1]
			}
		}
	}
	if len(diff) == 0 && offset < stdout.total {
		return &Result{Success: false, Error: "diff page cannot advance at this byte offset; restart at byte_offset 0 with max_bytes at least 4"}, nil
	}
	next := offset + int64(len(diff))
	data := map[string]any{
		"diff":        string(diff),
		"byte_offset": offset,
		"total_bytes": stdout.total,
		"diff_sha256": hex.EncodeToString(digest.Sum(nil)),
	}
	if next < stdout.total {
		data["diff_truncated"] = true
		data["next_byte_offset"] = next
	}
	result := &Result{
		Success: true,
		Data:    data,
	}
	if next < stdout.total {
		result.ShouldAbridge = true
		result.DisplayData = data
	}

	return result, nil
}

type diffPageWriter struct {
	buf    bytes.Buffer
	offset int64
	limit  int
	total  int64
}

func (w *diffPageWriter) Write(p []byte) (int, error) {
	start := w.total
	w.total += int64(len(p))
	from := max(w.offset-start, 0)
	to := min(w.offset+int64(w.limit)-start, int64(len(p)))
	if from < to {
		_, _ = w.buf.Write(p[from:to])
	}
	return len(p), nil
}

func diffPageNumber(name string, value any, minimum, maximum int64) (int64, error) {
	invalid := func() (int64, error) {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, minimum, maximum)
	}
	var number int64
	switch v := value.(type) {
	case int:
		number = int64(v)
	case int64:
		number = v
	case json.Number:
		parsed, err := v.Int64()
		if err != nil {
			return invalid()
		}
		number = parsed
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || v < float64(minimum) || v > float64(maximum) {
			return invalid()
		}
		number = int64(v)
	default:
		return invalid()
	}
	if number < minimum || number > maximum {
		return invalid()
	}
	return number, nil
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
