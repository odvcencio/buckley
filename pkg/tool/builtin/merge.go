package builtin

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/encoding/toon"
)

const (
	DefaultConflictFiles       = 10
	MaxConflictFiles           = 50
	DefaultConflictOutputBytes = 16 << 10
	DefaultConflictScanBytes   = 32 << 20
	DefaultConflictTime        = 20 * time.Second
)

type ListMergeConflictsTool struct{ workDirAware }

type conflictCursor struct {
	Version int
	Kind    string
	File    string
	Offset  int64
	Index   string
	Source  string
}

func encodeConflictCursor(cursor conflictCursor) string {
	raw, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func (t *ListMergeConflictsTool) Name() string { return "list_merge_conflicts" }
func (t *ListMergeConflictsTool) Description() string {
	return "List unmerged files under workdir in bounded pages, with one conflict section per file and bounded side excerpts. Diff3 base text is separate from ours/theirs. Use next_cursor for more files or a file's next_section_cursor for its remaining sections; pass cursors unchanged. Cursors override file; section cursors also override limit. Index changes reject continuations; section cursors also validate source bytes, file kind and marker-size attributes. Files remain unmerged even when conflicts is empty (binary, deleted, or edited without staging). Truncated sides have byte counts; use read_file with start_line/end_line for source. Non-UTF8 byte prefixes are Go-quoted and identified in excerpt_encodings. Malformed markers and scan failures are errors."
}
func (t *ListMergeConflictsTool) Parameters() ParameterSchema {
	schema := ParameterSchema{Type: "object", Properties: map[string]PropertySchema{
		"file":   {Type: "string", Description: "Optional literal file filter relative to workdir. Empty/null lists all unmerged files under workdir."},
		"limit":  {Type: "integer", Description: "Maximum files returned, default 10, range 1-50; output budget may return fewer.", Default: DefaultConflictFiles},
		"cursor": {Type: "string", Description: "Opaque next_cursor or next_section_cursor. Empty/null starts a new query."},
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
func (t *ListMergeConflictsTool) Execute(params map[string]any) (*Result, error) {
	return t.ExecuteWithContext(context.Background(), params)
}
func (t *ListMergeConflictsTool) ExecuteWithContext(parent context.Context, params map[string]any) (*Result, error) {
	dir, err := filepath.Abs(t.workDir)
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	cursor := conflictCursor{Version: 1, Kind: "files"}
	if value := params["cursor"]; value != nil {
		s, ok := value.(string)
		if !ok || len(s) > 16<<10 {
			return &Result{Error: "invalid conflict cursor"}, nil
		}
		if s != "" {
			raw, e := base64.RawURLEncoding.Strict().DecodeString(s)
			if e != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Version != 1 || (cursor.Kind != "files" && cursor.Kind != "sections") || cursor.Offset < 1 || cursor.Offset > DefaultConflictScanBytes || !validConflictDigest(cursor.Index) || (cursor.Kind == "sections" && (cursor.File == "" || !validConflictDigest(cursor.Source))) {
				return &Result{Error: "invalid conflict cursor; pass a returned cursor unchanged"}, nil
			}
		}
	}
	if cursor.Index == "" {
		if value := params["file"]; value != nil {
			var ok bool
			cursor.File, ok = value.(string)
			if !ok {
				return &Result{Error: "file must be a literal string"}, nil
			}
		}
	}
	if len(cursor.File) > 8192 || strings.ContainsRune(cursor.File, 0) {
		return &Result{Error: "file must be at most 8192 bytes without NUL"}, nil
	}
	if cursor.File != "" {
		abs, e := resolveLiteralPath(dir, cursor.File)
		if e != nil {
			return &Result{Error: e.Error()}, nil
		}
		rel, e := filepath.Rel(dir, abs)
		if e != nil {
			return &Result{Error: e.Error()}, nil
		}
		cursor.File = filepath.ToSlash(rel)
	}
	limit := DefaultConflictFiles
	fileOffset, sectionOffset := cursor.Offset, int64(0)
	if cursor.Kind == "sections" {
		limit, fileOffset, sectionOffset = 1, 0, cursor.Offset
	} else if value := params["limit"]; value != nil {
		limit, err = positiveIntParameter("limit", value)
		if err != nil || limit > MaxConflictFiles {
			return &Result{Error: "limit must be an integer between 1 and 50"}, nil
		}
	}
	ctx, cancel := t.execContextWithParent(parent)
	defer cancel()
	if t.maxExecTime <= 0 {
		var timeoutCancel context.CancelFunc
		ctx, timeoutCancel = context.WithTimeout(ctx, DefaultConflictTime)
		defer timeoutCancel()
	}
	command := func(args ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "--literal-pathspecs"}, args...)...)
		cmd.Dir, cmd.Env = dir, mergeEnv(nil, t.env)
		configureCommandCancellation(cmd)
		return cmd
	}
	cmd := command("ls-files", "--unmerged", "-z", "--", ".")
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
	fmt.Fprintf(digest, "%q\x00", dir)
	input := &io.LimitedReader{R: stdout, N: DefaultConflictScanBytes + 1}
	reader := bufio.NewReaderSize(io.TeeReader(input, digest), 16<<10)
	paths := make([]string, 0, limit)
	last := ""
	var total int64
	for {
		record, e := reader.ReadSlice(0)
		if e == io.EOF && len(record) == 0 {
			break
		}
		if e != nil {
			err = fmt.Errorf("incomplete or oversized unmerged index record: %w", e)
			break
		}
		header, path, found := strings.Cut(string(record[:len(record)-1]), "\t")
		fields := strings.Fields(header)
		if !found || path == "" || len(path) > 8192 || !utf8.ValidString(path) || len(fields) != 3 || !validGitCommitID(fields[1]) {
			err = fmt.Errorf("malformed unmerged index record or unsupported filename encoding")
			break
		}
		mode, e1 := strconv.ParseUint(fields[0], 8, 32)
		stage, e2 := strconv.Atoi(fields[2])
		if e1 != nil || mode == 0 || e2 != nil || stage < 1 || stage > 3 {
			err = fmt.Errorf("invalid unmerged index mode/stage")
			break
		}
		if path != last {
			last = path
			if cursor.File == "" || cursor.File == path {
				if total >= fileOffset && len(paths) < limit {
					paths = append(paths, path)
				}
				total++
			}
		}
	}
	exceeded := input.N == 0
	if err != nil || exceeded {
		_ = cmd.Cancel()
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return &Result{Error: "conflict listing canceled: " + ctx.Err().Error()}, nil
	}
	if exceeded {
		return &Result{Error: "unmerged index exceeds the 32 MiB scan budget"}, nil
	}
	if err != nil {
		return &Result{Error: err.Error()}, nil
	}
	if waitErr != nil {
		return &Result{Error: formatGitFailure(waitErr, stderr.String())}, nil
	}
	indexHash := hex.EncodeToString(digest.Sum(nil))
	if cursor.Index != "" && cursor.Index != indexHash {
		return &Result{Error: "unmerged index changed; restart conflict listing without a cursor"}, nil
	}
	if (cursor.Kind == "sections" && total != 1) || (fileOffset > 0 && fileOffset >= total) {
		return &Result{Error: "conflict cursor is outside the selected unmerged files"}, nil
	}
	rows := make([]*conflictFileSummary, 0, len(paths))
	remaining := int64(DefaultConflictScanBytes)
	for _, path := range paths {
		abs, err := resolveLiteralPath(dir, path)
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
		attrs := command("check-attr", "-z", "conflict-marker-size", "--", path)
		output, diagnostics := newLimitedBuffer(16<<10), newLimitedBuffer(1024)
		attrs.Stdout, attrs.Stderr = output, diagnostics
		if err := attrs.Run(); err != nil {
			if ctx.Err() != nil {
				return &Result{Error: "conflict listing canceled: " + ctx.Err().Error()}, nil
			}
			return &Result{Error: formatGitFailure(err, diagnostics.String())}, nil
		}
		if output.Truncated() {
			return &Result{Error: "conflict-marker-size attributes exceed output budget"}, nil
		}
		markerSize, err := parseConflictMarkerSize(path, output.String())
		if err != nil {
			return &Result{Error: err.Error()}, nil
		}
		row, err := scanConflictFile(ctx, abs, path, markerSize, sectionOffset, min(t.mutationFileLimit(), remaining))
		if err != nil {
			return &Result{Error: fmt.Sprintf("inspect conflict %q: %v", path, err)}, nil
		}
		if cursor.Kind == "sections" && row.sourceHash != cursor.Source {
			return &Result{Error: "conflict source changed; restart listing without a cursor"}, nil
		}
		if sectionOffset > 0 && sectionOffset >= row.count {
			return &Result{Error: "section cursor exceeds conflict section count"}, nil
		}
		remaining -= row.bytes
		rows = append(rows, row)
	}
	budget := DefaultConflictOutputBytes
	if t.maxOutputBytes > 0 {
		budget = min(budget, t.maxOutputBytes)
	}
	codec := toon.New(true)
	keep, excerpt := len(rows), 256
	for {
		files := make([]map[string]any, 0, keep)
		for _, row := range rows[:keep] {
			sections := make([]map[string]any, 0, 1)
			if row.section != nil {
				sections = append(sections, row.section.data(excerpt))
			}
			state := "unmerged_without_text_markers"
			if row.count > 0 {
				state = "text_conflicts"
			}
			file := map[string]any{"path": row.path, "kind": row.kind, "unmerged": true, "state": state, "conflicts": sections, "conflict_count": row.count, "section_offset": row.offset, "source_sha256": row.sourceHash}
			if row.count > row.offset+1 {
				file["next_section_cursor"] = encodeConflictCursor(conflictCursor{Version: 1, Kind: "sections", File: row.path, Offset: row.offset + 1, Index: indexHash, Source: row.sourceHash})
			}
			files = append(files, file)
		}
		data := map[string]any{"files": files, "count": keep, "total_files": total, "file_filter": cursor.File, "unmerged_sha256": indexHash}
		if fileOffset+int64(keep) < total {
			data["next_cursor"] = encodeConflictCursor(conflictCursor{Version: 1, Kind: "files", File: cursor.File, Offset: fileOffset + int64(keep), Index: indexHash})
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
				return &Result{Error: "conflict listing canceled: " + err.Error()}, nil
			}
			return result, nil
		}
		if keep > 1 {
			keep /= 2
		} else if excerpt > 16 {
			excerpt /= 2
		} else {
			return &Result{Error: fmt.Sprintf("conflict result exceeds %d-byte output budget", budget)}, nil
		}
	}
}

func validConflictDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
