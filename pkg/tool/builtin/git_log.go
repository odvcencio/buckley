package builtin

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

const (
	DefaultGitLogCommits     = 10
	MaxGitLogCommits         = 100
	DefaultGitLogOutputBytes = 16 << 10
	DefaultGitLogScanBytes   = 32 << 20
	DefaultGitLogTime        = 20 * time.Second
	maxGitLogSkip            = 1000000
)

type GitLogTool struct{ workDirAware }
type gitLogTextCursor struct {
	Version int
	Commit  string
	Offset  int64
	Oneline bool
}
type gitLogEntry struct {
	id, text string
	bytes    int64
	next     string
}

func (t *GitLogTool) Name() string { return "git_log" }
func (t *GitLogTool) Description() string {
	return "Read bounded commit history; count is commits returned, not output lines. commits contains one text block per commit. Defaults to 10 full-ID oneline entries. For more history, pass the returned anchor and next_skip as skip; advancing HEAD will not shift those pages. Raw commit history ignores replacement refs. Long text has next_text_cursors keyed by commit ID; pass one unchanged as text_cursor to read its next text page. That cursor overrides count/anchor/skip/oneline. Text byte lengths are reported. Budget, parse, and execution failures are errors."
}
func (t *GitLogTool) Parameters() ParameterSchema {
	schema := ParameterSchema{Type: "object", Properties: map[string]PropertySchema{
		"count":       {Type: "integer", Description: "Maximum commits to return, default 10, range 1-100.", Default: DefaultGitLogCommits},
		"oneline":     {Type: "boolean", Description: "One line per commit (default true). False includes author/date and multiline message.", Default: true},
		"path":        {Type: "string", Description: "Repository or directory to inspect (default workdir)."},
		"repo_path":   {Type: "string", Description: "Alias for path; takes precedence."},
		"anchor":      {Type: "string", Description: "Returned starting commit ID for stable history paging. Empty/null starts at current HEAD."},
		"skip":        {Type: "integer", Description: "Commits to skip from anchor; use next_skip. Default 0, maximum 1000000.", Default: 0},
		"text_cursor": {Type: "string", Description: "Opaque next_text_cursors value. Empty/null starts a normal history query. Overrides other history/text selectors."},
	}}
	for name, p := range schema.Properties {
		p.RawSchema = map[string]any{"type": []string{p.Type, "null"}, "description": p.Description}
		if p.Default != nil {
			p.RawSchema["default"] = p.Default
		}
		schema.Properties[name] = p
	}
	return schema
}
func (t *GitLogTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *GitLogTool) ExecuteWithContext(parent context.Context, params map[string]any) (*Result, error) {
	for _, field := range []string{"path", "repo_path"} {
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
	count, skip, oneline, anchor, textOffset := DefaultGitLogCommits, int64(0), true, "", int64(0)
	cursor := ""
	if value := params["text_cursor"]; value != nil {
		var ok bool
		cursor, ok = value.(string)
		if !ok || len(cursor) > 512 {
			return &Result{Error: "invalid text_cursor"}, nil
		}
	}
	if cursor != "" {
		raw, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
		var c gitLogTextCursor
		if err != nil || json.Unmarshal(raw, &c) != nil || c.Version != 1 || !validGitLogID(c.Commit) || c.Offset < 1 || c.Offset > DefaultGitLogScanBytes {
			return &Result{Error: "invalid text_cursor; pass the returned cursor unchanged"}, nil
		}
		count, anchor, oneline, textOffset = 1, c.Commit, c.Oneline, c.Offset
	} else {
		if value := params["count"]; value != nil {
			count, err = positiveIntParameter("count", value)
			if err != nil || count > MaxGitLogCommits {
				return &Result{Error: "count must be an integer between 1 and 100"}, nil
			}
		}
		if value := params["skip"]; value != nil {
			skip, err = gitPageNumber("skip", value, 0, maxGitLogSkip)
			if err != nil {
				return &Result{Error: err.Error()}, nil
			}
		}
		if value := params["oneline"]; value != nil {
			var ok bool
			oneline, ok = value.(bool)
			if !ok {
				return &Result{Error: "oneline must be a boolean"}, nil
			}
		}
		if value := params["anchor"]; value != nil {
			var ok bool
			anchor, ok = value.(string)
			if !ok || (anchor != "" && !validGitLogID(anchor)) {
				return &Result{Error: "anchor must be a full 40/64-character commit ID"}, nil
			}
		}
	}
	ctx, cancel := t.execContextWithParent(parent)
	defer cancel()
	if t.maxExecTime <= 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, DefaultGitLogTime)
		defer timeoutCancel()
	}
	command := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "--no-replace-objects"}, args...)...)
		cmd.Dir = dir
		cmd.Env = mergeEnv(cmd.Env, t.env)
		configureCommandCancellation(cmd)
		return cmd
	}
	if anchor == "" {
		cmd := command("rev-parse", "--verify", "HEAD^{commit}")
		output, stderr := newLimitedBuffer(128), newLimitedBuffer(1024)
		cmd.Stdout = output
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			if ctx.Err() != nil {
				return &Result{Error: "git log canceled: " + ctx.Err().Error()}, nil
			}
			return &Result{Error: formatGitFailure(err, stderr.String())}, nil
		}
		anchor = strings.TrimSpace(output.String())
		if output.Truncated() || !validGitLogID(anchor) {
			return &Result{Error: "invalid starting commit from git"}, nil
		}
	}
	format := "%H%x00%H %s"
	if !oneline {
		format = "%H%x00commit %H%nAuthor: %an <%ae>%nDate: %aI%n%n%B"
	}
	args := []string{"log", "--log-size", "-z", "--no-color", "--no-decorate", "--no-notes", "--no-show-signature", "--encoding=UTF-8", "--topo-order", fmt.Sprintf("--max-count=%d", count+1), fmt.Sprintf("--skip=%d", skip), "--format=" + format, anchor, "--"}
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
	input := &io.LimitedReader{R: stdout, N: DefaultGitLogScanBytes + 1}
	reader := bufio.NewReaderSize(input, 4096)
	budget := DefaultGitLogOutputBytes
	if t.maxOutputBytes > 0 {
		budget = min(budget, t.maxOutputBytes)
	}
	textLimit := max(64, min(4096, budget/(2*count)))
	rows := make([]gitLogEntry, 0, count)
	more := false
	for {
		size, e := gitLogRecordSize(reader)
		if e == io.EOF {
			break
		}
		if e != nil {
			err = e
			break
		}
		if len(rows) == count {
			more = true
			break
		}
		if size > DefaultGitLogScanBytes {
			err = fmt.Errorf("git log record exceeds scan byte budget")
			break
		}
		if size < int64(len(anchor)+1) {
			err = fmt.Errorf("git log record is shorter than its commit ID")
			break
		}
		prefix := make([]byte, len(anchor)+1)
		if _, e := io.ReadFull(reader, prefix); e != nil {
			err = e
			break
		}
		id := string(prefix[:len(anchor)])
		if prefix[len(anchor)] != 0 || !validGitLogID(id) {
			err = fmt.Errorf("malformed commit ID in git log")
			break
		}
		textBytes := size - int64(len(prefix))
		text, e := gitLogText(reader, textBytes, textOffset, textLimit)
		if e != nil {
			err = e
			break
		}
		terminator, e := reader.ReadByte()
		if e != nil {
			err = e
			break
		}
		if terminator != 0 {
			err = fmt.Errorf("malformed git log record terminator")
			break
		}
		next := ""
		if textOffset+int64(len(text)) < textBytes {
			raw, _ := json.Marshal(gitLogTextCursor{Version: 1, Commit: id, Offset: textOffset + int64(len(text)), Oneline: oneline})
			next = base64.RawURLEncoding.EncodeToString(raw)
		}
		rows = append(rows, gitLogEntry{id: id, text: text, bytes: textBytes, next: next})
	}
	exceeded := input.N == 0
	if err != nil || more || exceeded {
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return &Result{Error: "git log canceled: " + ctx.Err().Error()}, nil
	}
	if exceeded {
		return &Result{Error: "git log exceeds the 32 MiB scan budget; reduce count or select another commit"}, nil
	}
	if err != nil {
		return &Result{Error: fmt.Sprintf("git log could not be parsed safely: %v", err)}, nil
	}
	if waitErr != nil && !more {
		return &Result{Error: formatGitFailure(waitErr, stderr.String())}, nil
	}
	codec := toon.New(true)
	for keep := len(rows); ; keep /= 2 {
		texts := make([]string, 0, keep)
		sizes := make(map[string]int64, keep)
		cursors := make(map[string]string)
		for _, r := range rows[:keep] {
			texts = append(texts, r.text)
			sizes[r.id] = r.bytes
			if r.next != "" {
				cursors[r.id] = r.next
			}
		}
		data := map[string]any{"commits": texts, "count": keep, "anchor": anchor, "skip": skip, "oneline": oneline, "text_bytes": sizes, "text_offset": textOffset, "next_text_cursors": cursors, "has_more": more || keep < len(rows)}
		if more || keep < len(rows) {
			if next := skip + int64(keep); next <= maxGitLogSkip {
				data["next_skip"] = next
			} else {
				data["history_limit_reached"] = true
			}
		}
		result := &Result{Success: true, Data: data}
		raw, e := json.Marshal(result)
		if e != nil {
			return nil, e
		}
		text, e := codec.MarshalString(result)
		if e != nil {
			return nil, e
		}
		if max(len(raw), len(text)) <= budget {
			if err := ctx.Err(); err != nil {
				return &Result{Error: "git log canceled: " + err.Error()}, nil
			}
			return result, nil
		}
		if keep <= 1 {
			return &Result{Error: fmt.Sprintf("git log result exceeds %d-byte output budget; reduce count", budget)}, nil
		}
	}
}
func validGitLogID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
func gitLogRecordSize(r *bufio.Reader) (int64, error) {
	line, err := r.ReadSlice('\n')
	if err == io.EOF && len(line) == 0 {
		return 0, io.EOF
	}
	if err != nil {
		return 0, fmt.Errorf("incomplete git log length header: %w", err)
	}
	if len(line) > 64 || !strings.HasPrefix(string(line), "log size ") {
		return 0, fmt.Errorf("malformed git log length header")
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(string(line[9:]), "\n"), 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid git log record length")
	}
	return n, nil
}
func gitLogText(r io.Reader, size, offset int64, limit int) (string, error) {
	if offset > size {
		return "", fmt.Errorf("text cursor exceeds formatted commit length")
	}
	if _, err := io.CopyN(io.Discard, r, offset); err != nil {
		return "", err
	}
	take := min(size-offset, int64(limit))
	data := make([]byte, int(take))
	if _, err := io.ReadFull(r, data); err != nil {
		return "", err
	}
	if _, err := io.CopyN(io.Discard, r, size-offset-take); err != nil {
		return "", err
	}
	for trim := 0; trim < utf8.UTFMax && trim <= len(data); trim++ {
		prefix := data[:len(data)-trim]
		if utf8.Valid(prefix) {
			if len(prefix) == 0 && take > 0 {
				return "", fmt.Errorf("formatted commit text is not UTF-8")
			}
			return string(prefix), nil
		}
	}
	return "", fmt.Errorf("formatted commit text is not UTF-8")
}
