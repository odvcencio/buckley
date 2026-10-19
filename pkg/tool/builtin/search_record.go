package builtin

import (
	"bytes"
	"strconv"
	"unicode/utf8"
)

// DefaultSearchPageBytes bounds the JSON-encoded records in a search page.
const DefaultSearchPageBytes = 16 << 10

// DefaultSearchRecordBytes bounds the text excerpt returned for one record.
const DefaultSearchRecordBytes = 2 << 10

// Records borrow the scanner buffer until the next Scan; only returned pages
// convert their path and excerpt to owned strings.
type searchRecord struct {
	path, text   []byte
	line, column int
	isContext    bool
}

func parseSearchLine(line []byte) (searchRecord, bool) {
	var record searchRecord
	separator := bytes.IndexAny(line, "\x00\x1e\x1f")
	if separator >= 0 {
		record.path = bytes.TrimSpace(line[:separator])
		rest := line[separator+1:]
		switch line[separator] {
		case 0:
			index := bytes.IndexAny(rest, ":-")
			if index <= 0 {
				return record, false
			}
			record.line = searchLineNumber(rest[:index])
			record.isContext = rest[index] == '-'
			record.text = rest[index+1:]
		case '\x1f', '\x1e':
			number, remaining, ok := bytes.Cut(rest, line[separator:separator+1])
			if !ok {
				return record, false
			}
			record.line = searchLineNumber(number)
			record.isContext = line[separator] == '\x1f'
			if record.isContext {
				record.text = remaining
			} else {
				column, text, ok := bytes.Cut(remaining, line[separator:separator+1])
				if !ok {
					return record, false
				}
				record.column, record.text = searchLineNumber(column), text
			}
		}
		return record, true
	}
	// Compatibility with colon-delimited search output.
	path, rest, ok := bytes.Cut(line, []byte(":"))
	if !ok {
		return record, false
	}
	record.path = bytes.TrimSpace(path)
	number, remaining, hasColumn := bytes.Cut(rest, []byte(":"))
	record.line = searchLineNumber(number)
	if hasColumn {
		column, text, _ := bytes.Cut(remaining, []byte(":"))
		record.column, record.text = searchLineNumber(column), text
	}
	return record, true
}

func searchLineNumber(value []byte) int {
	number, err := strconv.Atoi(string(bytes.TrimSpace(value)))
	if err != nil {
		return 0
	}
	return number
}

func (r searchRecord) result(textLimit int) map[string]any {
	start := 0
	if len(r.text) > textLimit && r.column > 0 {
		start = max(0, min(r.column-1-textLimit/4, len(r.text)-textLimit))
	}
	for start > 0 && start < len(r.text) && !utf8.RuneStart(r.text[start]) {
		start++
	}
	end := min(len(r.text), start+textLimit)
	for end > start && end < len(r.text) && !utf8.RuneStart(r.text[end]) {
		end--
	}
	text := string(r.text[start:end])
	match, contextText, kind := text, "", "match"
	if r.isContext {
		match, contextText, kind = "", text, "context"
	}
	result := map[string]any{
		"path": string(r.path), "line": r.line, "column": r.column,
		"match": match, "context": contextText, "kind": kind,
	}
	if start > 0 || end < len(r.text) {
		result["text_truncated"] = true
		result["text_start_column"] = start + 1
		result["text_bytes"] = len(r.text)
	}
	return result
}
