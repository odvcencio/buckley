package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

const (
	DefaultMarkResolvedTime        = 20 * time.Second
	DefaultMarkResolvedOutputBytes = 4 << 10
	MaxMarkResolvedFileBytes       = 32 << 20
)

// MarkResolvedTool validates and stages one unmerged file.
type MarkResolvedTool struct{ workDirAware }

func (t *MarkResolvedTool) Name() string { return "mark_conflict_resolved" }
func (t *MarkResolvedTool) Description() string {
	return "Stage one currently unmerged file after a bounded source scan verifies that conflict markers have been removed. path is literal, including spaces and option-like names. Uses Git's marker-size attributes, checks binary-marked files too, and accepts resolved deletions. Rejects directories, unrelated files and files exceeding the mutation budget (default 8 MiB, hard cap 32 MiB). Run tests separately to verify the resolved program's behavior."
}
func (t *MarkResolvedTool) Parameters() ParameterSchema {
	return ParameterSchema{Type: "object", Properties: map[string]PropertySchema{
		"path": {Type: "string", Description: "Literal path of the unmerged file to validate and stage, relative to workdir."},
	}, Required: []string{"path"}}
}
func (t *MarkResolvedTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *MarkResolvedTool) ExecuteWithContext(parent context.Context, params map[string]any) (*Result, error) {
	path, ok := params["path"].(string)
	if !ok || path == "" || len(path) > 8192 || strings.ContainsRune(path, 0) {
		return &Result{Error: "path must be a non-empty literal string of at most 8192 bytes without NUL"}, nil
	}
	dir, err := filepath.Abs(t.workDir)
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	abs, err := resolveLiteralPath(dir, path)
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	path, err = filepath.Rel(dir, abs)
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	path = filepath.ToSlash(path)
	fileLimit := min(t.mutationFileLimit(), int64(MaxMarkResolvedFileBytes))
	if info, err := os.Lstat(abs); err == nil {
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return &Result{Error: "path must name one file, not a directory or special device"}, nil
		}
		if info.Size() > fileLimit {
			return &Result{Error: mutationBudgetError(fileLimit).Error()}, nil
		}
	} else if !os.IsNotExist(err) {
		return &Result{Error: fmt.Sprintf("inspect conflict path: %v", err)}, nil
	}
	result := &Result{Success: true, Data: map[string]any{"path": path, "staged": true, "marker_check": "bounded source scan"}}
	budget := DefaultMarkResolvedOutputBytes
	if t.maxOutputBytes > 0 {
		budget = min(budget, t.maxOutputBytes)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	encoded, err := toon.New(true).MarshalString(result)
	if err != nil {
		return nil, err
	}
	if max(len(raw), len(encoded)) > budget {
		return &Result{Error: fmt.Sprintf("resolution result exceeds %d-byte output budget", budget)}, nil
	}
	ctx, cancel := t.execContextWithParent(parent)
	defer cancel()
	if t.maxExecTime <= 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, DefaultMarkResolvedTime)
		defer timeoutCancel()
	}
	run := func(outputBytes int, args ...string) (*limitedBuffer, error) {
		cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "--literal-pathspecs"}, args...)...)
		cmd.Dir = dir
		cmd.Env = mergeEnv(cmd.Env, t.env)
		configureCommandCancellation(cmd)
		output := newLimitedBuffer(outputBytes)
		cmd.Stdout, cmd.Stderr = output, output
		err := cmd.Run()
		if ctx.Err() != nil {
			return output, fmt.Errorf("conflict resolution canceled: %w", ctx.Err())
		}
		return output, err
	}
	// At most three index stages exist for one file; directory/pathspec matches
	// must not expand the mutation to other files.
	unmerged, err := run(32<<10, "ls-files", "--unmerged", "--format=%(path)", "-z", "--", path)
	if err != nil {
		text := unmerged.String()
		return &Result{Error: formatGitFailure(err, text[:min(len(text), budget/2)])}, nil
	}
	if unmerged.Truncated() {
		return &Result{Error: "unmerged path listing exceeds the single-file budget"}, nil
	}
	entries := strings.Split(unmerged.String(), "\x00")
	if len(entries) < 2 || entries[len(entries)-1] != "" {
		return &Result{Error: "path is not a currently unmerged file"}, nil
	}
	for _, entry := range entries[:len(entries)-1] {
		if entry != path {
			return &Result{Error: "path must select exactly one unmerged file"}, nil
		}
	}
	attributes, err := run(16<<10, "check-attr", "-z", "conflict-marker-size", "--", path)
	if err != nil {
		text := attributes.String()
		return &Result{Error: formatGitFailure(err, text[:min(len(text), budget/2)])}, nil
	}
	fields := strings.Split(attributes.String(), "\x00")
	if attributes.Truncated() || len(fields) != 4 || fields[0] != path || fields[1] != "conflict-marker-size" || fields[3] != "" {
		return &Result{Error: "invalid conflict-marker-size attributes from Git"}, nil
	}
	markerSize, err := strconv.ParseInt(fields[2], 10, 32)
	if err != nil || markerSize <= 0 {
		markerSize = 7
	}
	if err := checkResolutionFile(ctx, abs, fileLimit, markerSize); err != nil {
		return &Result{Error: "conflict marker validation failed; file was not staged: " + err.Error()}, nil
	}
	stage, err := run(budget/2, "add", "--", path)
	if err != nil {
		return &Result{Error: formatGitFailure(err, stage.String())}, nil
	}
	return result, nil
}

func checkResolutionFile(ctx context.Context, path string, limit, markerSize int64) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil // A missing unmerged file is a deliberate deletion.
	}
	if err != nil {
		return err
	}
	var source io.Reader
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		source = strings.NewReader(target)
	} else {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("path must remain a regular file or symlink")
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		source = f
	}
	input := &io.LimitedReader{R: source, N: limit + 1}
	buffer := make([]byte, 32<<10)
	line, column := int64(1), int64(0)
	var marker byte
	matching := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := input.Read(buffer)
		if input.N == 0 {
			return mutationBudgetError(limit)
		}
		for _, b := range buffer[:n] {
			if column == 0 {
				marker = b
				matching = b == '<' || b == '>' || b == '=' || b == '|'
			}
			if matching {
				if column < markerSize {
					matching = b == marker
				} else {
					space := b == ' ' || b == '\t' || b == '\n' || b == '\r' || b == '\v' || b == '\f'
					if space && ((marker != '<' && marker != '>') || b == ' ') {
						return fmt.Errorf("leftover conflict marker on line %d", line)
					}
					matching = false
				}
			}
			if b == '\n' {
				line++
				column = 0
			} else {
				column++
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
