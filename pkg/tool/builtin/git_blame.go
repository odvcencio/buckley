package builtin

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

const (
	DefaultGitBlameLines       = 40
	MaxGitBlameLines           = 100
	DefaultGitBlameOutputBytes = 16 << 10
	DefaultGitBlameScanBytes   = 32 << 20
	DefaultGitBlameTime        = 20 * time.Second
)

type GitBlameTool struct{ workDirAware }

type gitBlameRow struct {
	text            string
	line            int64
	truncated       bool
	authorTruncated bool
}

func (t *GitBlameTool) Name() string { return "git_blame" }
func (t *GitBlameTool) Description() string {
	return "Read bounded line-by-line authorship including uncommitted working-tree changes. blame contains full commit IDs, quoted authors, UTC dates, source line numbers and quoted source excerpts. All-zero IDs mean uncommitted. For more lines, pass next_start_line and blame_sha256 as expected_sha256; changed evidence rejects the page. The fingerprint covers the file scope and complete blame records, excluding the generated timestamps for uncommitted lines. Raw history ignores replacement refs. truncated_lines identifies clipped source excerpts; use read_file for source content. truncated_authors identifies clipped author names; git_log can recover commit metadata. Scan, parse and execution failures are errors."
}
func (t *GitBlameTool) Parameters() ParameterSchema {
	schema := ParameterSchema{Type: "object", Properties: map[string]PropertySchema{
		"file":            {Type: "string", Description: "Literal file to inspect, relative to repo_path or workdir."},
		"path":            {Type: "string", Description: "Compatibility alias for file."},
		"repo_path":       {Type: "string", Description: "Repository/directory to run in (default workdir)."},
		"start_line":      {Type: "integer", Description: "First source line (1-based, default 1). For a continuation, use next_start_line and expected_sha256.", Default: 1},
		"limit":           {Type: "integer", Description: "Maximum lines returned (default 40, range 1-100); byte budget may return fewer.", Default: DefaultGitBlameLines},
		"expected_sha256": {Type: "string", Description: "Previous blame_sha256 to reject changed evidence. Omit or use null/empty for a new query."},
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
func (t *GitBlameTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *GitBlameTool) ExecuteWithContext(parent context.Context, params map[string]any) (*Result, error) {
	for _, field := range []string{"file", "path", "repo_path", "expected_sha256"} {
		if value := params[field]; value != nil {
			s, ok := value.(string)
			if !ok || len(s) > 8192 {
				return &Result{Error: field + " must be a string of at most 8192 bytes"}, nil
			}
		}
	}
	file, _ := params["file"].(string)
	if strings.TrimSpace(file) == "" {
		file, _ = params["path"].(string)
	}
	if strings.TrimSpace(file) == "" {
		return &Result{Error: "file parameter required"}, nil
	}
	dir, err := gitCommandDir(t.workDir, map[string]any{"repo_path": params["repo_path"]})
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	_, file, err = resolveRelPath(dir, file)
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	start, limit := int64(1), int64(DefaultGitBlameLines)
	for _, p := range []struct {
		name string
		max  int64
		out  *int64
	}{{"start_line", maxDiffByteOffset, &start}, {"limit", MaxGitBlameLines, &limit}} {
		if value := params[p.name]; value != nil {
			*p.out, err = gitPageNumber(p.name, value, 1, p.max)
			if err != nil {
				return &Result{Error: err.Error()}, nil
			}
		}
	}
	expected, _ := params["expected_sha256"].(string)
	if expected != "" {
		if raw, e := hex.DecodeString(expected); e != nil || len(raw) != sha256.Size {
			return &Result{Error: "expected_sha256 must be a 64-character hexadecimal blame fingerprint"}, nil
		}
	}
	ctx, cancel := t.execContextWithParent(parent)
	defer cancel()
	if t.maxExecTime <= 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, DefaultGitBlameTime)
		defer timeoutCancel()
	}
	cmd := exec.CommandContext(ctx, "git", "--no-optional-locks", "--no-replace-objects", "blame", "--line-porcelain", "--encoding=UTF-8", "--no-textconv", "--", file)
	cmd.Dir = dir
	cmd.Env = mergeEnv(cmd.Env, t.env)
	configureCommandCancellation(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	stderr := newLimitedBuffer(1024)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return &Result{Error: formatGitFailure(err, stderr.String())}, nil
	}
	input := &io.LimitedReader{R: stdout, N: DefaultGitBlameScanBytes + 1}
	reader := bufio.NewReaderSize(input, 4096)
	digest := sha256.New()
	fmt.Fprintf(digest, "%q\x00%q\x00", dir, file)
	rows := make([]gitBlameRow, 0, limit)
	var total int64
	id, author := "", ""
	authorTruncated := false
	var timestamp int64
	authorSeen, timeSeen, zero := false, false, false
	for {
		line, clipped, e := gitBlameReadLine(reader, digest, zero)
		if e == io.EOF && id == "" {
			break
		}
		if e != nil {
			err = fmt.Errorf("incomplete blame record: %w", e)
			break
		}
		if id == "" {
			fields := strings.Fields(string(line))
			if clipped || (len(fields) != 3 && len(fields) != 4) || !validGitCommitID(fields[0]) {
				err = fmt.Errorf("malformed blame header")
				break
			}
			original, e1 := strconv.ParseInt(fields[1], 10, 64)
			final, e2 := strconv.ParseInt(fields[2], 10, 64)
			if e1 != nil || e2 != nil || original < 1 || final != total+1 {
				err = fmt.Errorf("invalid blame line numbers")
				break
			}
			id = fields[0]
			zero = strings.Trim(id, "0") == ""
			authorSeen, timeSeen, authorTruncated = false, false, false
			continue
		}
		if bytes.HasPrefix(line, []byte{'\t'}) {
			if !authorSeen || !timeSeen {
				err = fmt.Errorf("missing blame author/date")
				break
			}
			total++
			if total >= start && int64(len(rows)) < limit {
				content := line[1:]
				truncated := clipped || len(content) > 512
				content = content[:min(len(content), 512)]
				date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")
				text := fmt.Sprintf("%s (%s %s %d)\t%s", id, strconv.QuoteToGraphic(author), date, total, strconv.QuoteToGraphic(string(content)))
				rows = append(rows, gitBlameRow{text: text, line: total, truncated: truncated, authorTruncated: authorTruncated})
			}
			id, zero = "", false
		} else if value, ok := bytes.CutPrefix(line, []byte("author ")); ok {
			authorTruncated = clipped || len(value) > 256
			author, authorSeen = string(value[:min(len(value), 256)]), true
		} else if value, ok := bytes.CutPrefix(line, []byte("author-time ")); ok {
			timestamp, err = strconv.ParseInt(string(value), 10, 64)
			if err != nil || clipped {
				err = fmt.Errorf("invalid blame author timestamp")
				break
			}
			timeSeen = true
		}
	}
	exceeded := input.N == 0
	if err != nil || exceeded {
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return &Result{Error: "git blame canceled: " + ctx.Err().Error()}, nil
	}
	if exceeded {
		return &Result{Error: "git blame exceeds the 32 MiB scan budget"}, nil
	}
	if err != nil {
		return &Result{Error: fmt.Sprintf("git blame could not be parsed safely: %v", err)}, nil
	}
	if waitErr != nil {
		return &Result{Error: formatGitFailure(waitErr, stderr.String())}, nil
	}
	fingerprint := hex.EncodeToString(digest.Sum(nil))
	if expected != "" && !strings.EqualFold(expected, fingerprint) {
		return &Result{Error: "blame evidence changed; restart without expected_sha256"}, nil
	}
	if start > total && !(start == 1 && total == 0) {
		return &Result{Error: fmt.Sprintf("start_line exceeds file length (%d lines)", total)}, nil
	}
	budget := DefaultGitBlameOutputBytes
	if t.maxOutputBytes > 0 {
		budget = min(budget, t.maxOutputBytes)
	}
	codec := toon.New(true)
	for keep := len(rows); ; keep /= 2 {
		var text strings.Builder
		truncated := make([]int64, 0)
		truncatedAuthors := make([]int64, 0)
		for _, row := range rows[:keep] {
			text.WriteString(row.text)
			text.WriteByte('\n')
			if row.truncated {
				truncated = append(truncated, row.line)
			}
			if row.authorTruncated {
				truncatedAuthors = append(truncatedAuthors, row.line)
			}
		}
		end := int64(0)
		if keep > 0 {
			end = rows[keep-1].line
		}
		data := map[string]any{"file": file, "blame": text.String(), "count": keep, "total_lines": total, "start_line": start, "end_line": end, "has_more": end < total, "blame_sha256": fingerprint, "truncated_lines": truncated, "truncated_authors": truncatedAuthors}
		if end < total {
			data["next_start_line"] = end + 1
		}
		result := &Result{Success: true, Data: data}
		raw, e := json.Marshal(result)
		if e != nil {
			return nil, e
		}
		encoded, e := codec.MarshalString(result)
		if e != nil {
			return nil, e
		}
		if max(len(raw), len(encoded)) <= budget {
			if err := ctx.Err(); err != nil {
				return &Result{Error: "git blame canceled: " + err.Error()}, nil
			}
			return result, nil
		}
		if keep <= 1 {
			return &Result{Error: fmt.Sprintf("git blame result exceeds %d-byte output budget; reduce limit", budget)}, nil
		}
	}
}

// Hash complete records while retaining only a bounded prefix of each line.
// Git synthesizes wall-clock timestamps on every invocation for dirty lines.
func gitBlameReadLine(r *bufio.Reader, digest hash.Hash, zero bool) ([]byte, bool, error) {
	var prefix []byte
	first := true
	ignoreTime := false
	clipped := false
	for {
		part, err := r.ReadSlice('\n')
		if first && zero {
			for _, field := range []string{"author-time ", "committer-time "} {
				if bytes.HasPrefix(part, []byte(field)) {
					ignoreTime = true
					_, _ = digest.Write([]byte(field + "\n"))
					break
				}
			}
		}
		if !ignoreTime {
			_, _ = digest.Write(part)
		}
		if err == nil {
			part = bytes.TrimSuffix(part, []byte{'\n'})
		}
		if first && err != bufio.ErrBufferFull {
			return part, false, err
		}
		first = false
		if prefix == nil {
			prefix = make([]byte, 0, 1024)
		}
		take := min(len(part), 1024-len(prefix))
		prefix = append(prefix, part[:take]...)
		clipped = clipped || take < len(part)
		if err != bufio.ErrBufferFull {
			return prefix, clipped, err
		}
	}
}
