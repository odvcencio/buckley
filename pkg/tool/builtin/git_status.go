package builtin

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

const (
	DefaultGitStatusEntries     = 100
	MaxGitStatusEntries         = 500
	DefaultGitStatusOutputBytes = 16 << 10
	DefaultGitStatusScanBytes   = 32 << 20
	DefaultGitStatusTime        = 20 * time.Second
	maxGitStatusTokenBytes      = 16 << 10
)

// GitStatusTool reads paged status without optional index refresh writes.
type GitStatusTool struct{ workDirAware }

func (t *GitStatusTool) Name() string { return "git_status" }
func (t *GitStatusTool) Description() string {
	return "Read staged, unstaged, and expanded untracked file status in bounded pages without optional index writes. status uses repository-root relative paths; unusual names are quoted with escapes. count is total entries, returned is this page, and clean refers to the selected file scope. Continue with next_offset and status_sha256 as expected_sha256; changed status records or scope reject the page. The hash covers status records, not file contents. Use file to narrow a large scan."
}
func (t *GitStatusTool) Parameters() ParameterSchema {
	schema := ParameterSchema{Type: "object", Properties: map[string]PropertySchema{
		"path":            {Type: "string", Description: "Repository or directory to inspect (default workdir)."},
		"repo_path":       {Type: "string", Description: "Alias for path; takes precedence when both are set."},
		"file":            {Type: "string", Description: "Optional literal file/directory filter relative to path. No glob or pathspec magic."},
		"offset":          {Type: "integer", Description: "Zero-based status entry offset; use next_offset. Continuations require expected_sha256.", Default: 0},
		"limit":           {Type: "integer", Description: "Maximum entries per page (default 100, max 500); the byte budget may return fewer.", Default: DefaultGitStatusEntries},
		"expected_sha256": {Type: "string", Description: "For a continuation, status_sha256 from the preceding page of the same scope. Omit or use null/empty to start at offset 0."},
	}}
	for name, property := range schema.Properties {
		property.RawSchema = map[string]any{"type": []string{property.Type, "null"}, "description": property.Description}
		if property.Default != nil {
			property.RawSchema["default"] = property.Default
		}
		schema.Properties[name] = property
	}
	return schema
}
func (t *GitStatusTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *GitStatusTool) ExecuteWithContext(parent context.Context, params map[string]any) (*Result, error) {
	for _, field := range []string{"path", "repo_path", "file"} {
		if value := params[field]; value != nil {
			text, ok := value.(string)
			if !ok || len(text) > 8192 {
				return &Result{Error: field + " must be a string of at most 8192 bytes"}, nil
			}
		}
	}
	dir, err := gitCommandDir(t.workDir, params)
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	filter, _ := params["file"].(string)
	if filter != "" {
		_, filter, err = resolveRelPath(dir, filter)
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
	}
	offset := int64(0)
	if value := params["offset"]; value != nil {
		offset, err = gitPageNumber("offset", value, 0, maxDiffByteOffset)
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
	}
	limit := DefaultGitStatusEntries
	if value := params["limit"]; value != nil {
		limit, err = positiveIntParameter("limit", value)
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
	}
	if limit > MaxGitStatusEntries {
		return &Result{Error: "limit must be between 1 and 500"}, nil
	}
	expected := ""
	if value := params["expected_sha256"]; value != nil {
		var ok bool
		expected, ok = value.(string)
		if !ok || (expected != "" && len(expected) != 64) {
			return &Result{Error: "expected_sha256 must be a SHA-256 hex digest"}, nil
		}
		if _, err := hex.DecodeString(expected); err != nil {
			return &Result{Error: "expected_sha256 must be a SHA-256 hex digest"}, nil
		}
	}
	if offset > 0 && expected == "" {
		return &Result{Error: "continuations require expected_sha256 from the previous page"}, nil
	}
	ctx, cancel := t.execContextWithParent(parent)
	defer cancel()
	if t.maxExecTime <= 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, DefaultGitStatusTime)
		defer timeoutCancel()
	}
	command := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "--literal-pathspecs"}, args...)...)
		cmd.Dir = dir
		cmd.Env = mergeEnv(cmd.Env, t.env)
		configureCommandCancellation(cmd)
		return cmd
	}
	rootCommand := command("rev-parse", "--show-toplevel")
	rootOutput, rootError := newLimitedBuffer(8<<10), newLimitedBuffer(1024)
	rootCommand.Stdout = rootOutput
	rootCommand.Stderr = rootError
	if err := rootCommand.Run(); err != nil {
		if ctx.Err() != nil {
			return &Result{Error: "git status canceled: " + ctx.Err().Error()}, nil
		}
		return &Result{Error: formatGitFailure(err, rootError.String())}, nil
	}
	root := strings.TrimSuffix(rootOutput.String(), "\n")
	if rootOutput.Truncated() || !utf8.ValidString(root) || !filepath.IsAbs(root) {
		return &Result{Error: "repository root exceeds the metadata budget or is not a UTF-8 absolute path"}, nil
	}
	args := []string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-branch", "--no-show-stash"}
	if filter != "" {
		args = append(args, "--", filter)
	}
	cmd := command(args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	stderr := newLimitedBuffer(1024)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return &Result{Error: formatGitFailure(err, stderr.String())}, nil
	}
	digest := sha256.New()
	_, _ = io.WriteString(digest, dir+"\x00"+root+"\x00"+filter+"\x00")
	input := &io.LimitedReader{R: io.TeeReader(stdout, digest), N: DefaultGitStatusScanBytes + 1}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxGitStatusTokenBytes)
	scanner.Split(splitGitStatusToken)
	budget := DefaultGitStatusOutputBytes
	if t.maxOutputBytes > 0 {
		budget = min(budget, t.maxOutputBytes)
	}
	rows := make([]string, 0, limit)
	pageBytes, count := 0, 0
	full := false
	parseError := ""
	for scanner.Scan() {
		token := scanner.Bytes()
		if len(token) < 4 || token[2] != ' ' || !strings.ContainsRune(" MADRCUT?!", rune(token[0])) || !strings.ContainsRune(" MADRCUT?!", rune(token[1])) {
			parseError = "malformed git status record"
			break
		}
		wanted := int64(count) >= offset && len(rows) < limit && !full
		record := ""
		if wanted {
			record = string(token)
		}
		original := ""
		if token[0] == 'R' || token[0] == 'C' || token[1] == 'R' || token[1] == 'C' {
			if !scanner.Scan() {
				parseError = "missing original path in git status rename/copy"
				break
			}
			if len(scanner.Bytes()) == 0 {
				parseError = "empty original path in git status"
				break
			}
			if wanted {
				original = string(scanner.Bytes())
			}
		}
		if wanted {
			code, path := record[:2], record[3:]
			line := code + " " + quoteStatusPath(path) + "\n"
			if original != "" {
				line = code + " " + quoteStatusPath(original) + " -> " + quoteStatusPath(path) + "\n"
			}
			if pageBytes+len(line) > budget {
				full = true
			} else {
				rows = append(rows, line)
				pageBytes += len(line)
			}
		}
		count++
	}
	scanErr := scanner.Err()
	exceeded := input.N == 0
	if parseError != "" || scanErr != nil || exceeded {
		_ = cmd.Cancel()
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return &Result{Error: "git status canceled: " + ctx.Err().Error()}, nil
	}
	if exceeded {
		return &Result{Error: "git status exceeds the 32 MiB scan budget; narrow the file filter"}, nil
	}
	if scanErr != nil {
		return &Result{Error: fmt.Sprintf("git status could not be parsed safely: %v", scanErr)}, nil
	}
	if parseError != "" {
		return &Result{Error: parseError}, nil
	}
	if err != nil {
		return &Result{Error: formatGitFailure(err, stderr.String())}, nil
	}
	hash := hex.EncodeToString(digest.Sum(nil))
	if expected != "" && !strings.EqualFold(hash, expected) {
		return &Result{Error: "status records or scope changed since the previous page; restart at offset 0"}, nil
	}
	if offset > int64(count) {
		return &Result{Error: "offset exceeds the status entry count"}, nil
	}
	if len(rows) == 0 && offset < int64(count) {
		return &Result{Error: fmt.Sprintf("status entry exceeds %d-byte output budget; narrow the file filter", budget)}, nil
	}
	data := map[string]any{"repository_root": root, "file": strings.Clone(filter), "status_sha256": hash, "offset": offset, "count": count, "clean": count == 0}
	codec := toon.New(true)
	for keep := len(rows); ; keep /= 2 {
		next := offset + int64(keep)
		data["status"] = strings.Join(rows[:keep], "")
		data["returned"] = keep
		data["truncated"] = next < int64(count)
		delete(data, "next_offset")
		if next < int64(count) {
			data["next_offset"] = next
		}
		result := &Result{Success: true, Data: data}
		raw, err := json.Marshal(result)
		if err != nil {
			return nil, err
		}
		text, err := codec.MarshalString(result)
		if err != nil {
			return nil, err
		}
		if max(len(raw), len(text)) <= budget {
			if err := ctx.Err(); err != nil {
				return &Result{Error: "git status canceled: " + err.Error()}, nil
			}
			return result, nil
		}
		if keep <= 1 {
			return &Result{Error: fmt.Sprintf("git status result exceeds %d-byte output budget; narrow the file filter", budget)}, nil
		}
	}
}
func splitGitStatusToken(data []byte, atEOF bool) (int, []byte, error) {
	if at := bytes.IndexByte(data, 0); at >= 0 {
		return at + 1, data[:at], nil
	}
	if atEOF && len(data) > 0 {
		return 0, nil, fmt.Errorf("unterminated git status record")
	}
	return 0, nil, nil
}
func quoteStatusPath(path string) string {
	if !utf8.ValidString(path) || strings.IndexFunc(path, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == '"' || r == '\\' }) >= 0 {
		return strconv.Quote(path)
	}
	return path
}
