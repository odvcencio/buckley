package builtin

import (
	"bufio"
	"bytes"
	"fmt"
	"math"
	"os"
	"strings"
)

const defaultReadFileSnapshotBytes = 8 << 20

func readFileFailure(kind, detail string) *Result {
	return &Result{Error: "[" + kind + "] " + detail, Data: map[string]any{"error_kind": kind}}
}

// Large files use a bounded buffer for the requested page, not a full-file
// snapshot. Reading the next line establishes whether another page exists.
func (t *ReadFileTool) readLargeFilePage(path string, params map[string]any, size, snapshotLimit int64) (*Result, error) {
	if _, anchor := params["anchor"]; anchor {
		return readFileFailure("file_too_large", fmt.Sprintf("%s has %d bytes; use start_line and end_line for a bounded page, or search_text to find a line", path, size)), nil
	}
	start, end, _, err := readFilePage(params, math.MaxInt)
	if err != nil {
		return readFileFailure("invalid_page", err.Error()), nil
	}
	numbered := false
	if value, ok := params["line_numbers"]; ok {
		var valid bool
		numbered, valid = value.(bool)
		if !valid {
			return readFileFailure("invalid_page", "line_numbers must be a boolean"), nil
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return readFileFailure("file_access", fmt.Sprintf("cannot open %s: %v", path, err)), nil
	}
	defer file.Close()
	limit := int(min(snapshotLimit, 1<<20))
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, min(4096, limit)), limit)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if end := bytes.IndexByte(data, '\n'); end >= 0 {
			return end + 1, data[:end], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	var lines []string
	line, pageBytes := 0, 0
	hasMore := false
	for scanner.Scan() {
		line++
		if bytes.IndexByte(scanner.Bytes(), 0) >= 0 {
			return readFileFailure("binary_file", fmt.Sprintf("%s contains NUL bytes; use a format-specific tool or a bounded hex dump through run_shell", path)), nil
		}
		if line < start {
			continue
		}
		if line > end || (len(lines) > 0 && pageBytes+len(scanner.Bytes())+1 > limit) {
			hasMore = true
			break
		}
		value := scanner.Text()
		lines = append(lines, value)
		pageBytes += len(value) + 1
	}
	if err := scanner.Err(); err != nil {
		return readFileFailure("line_too_large", fmt.Sprintf("cannot read line %d of %s within the %d-byte line limit: %v; use search_text or a bounded byte read through run_shell", line+1, path, limit, err)), nil
	}
	if start > line && start != 1 {
		return readFileFailure("invalid_page", fmt.Sprintf("start_line %d exceeds %s (%d lines); use start_line=1", start, path, line)), nil
	}
	page := map[string]any{"start_line": start, "end_line": start + len(lines) - 1, "has_more": hasMore}
	if hasMore {
		page["next_start_line"] = start + len(lines)
	} else {
		page["total_lines"] = line
	}
	data := map[string]any{"path": path, "size": size, "content": strings.Join(lines, "\n"), "content_is_page": true, "page": page}
	display := data
	if numbered {
		display = make(map[string]any, len(data))
		for key, value := range data {
			display[key] = value
		}
		for i := range lines {
			lines[i] = fmt.Sprintf("%d: %s", start+i, lines[i])
		}
		display["content"] = strings.Join(lines, "\n")
	}
	return &Result{Success: true, Data: data, DisplayData: display, ShouldAbridge: true}, nil
}
