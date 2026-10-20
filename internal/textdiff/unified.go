package textdiff

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

const contextLines = 3

// Unified formats a three-context-line unified diff, preserving
// empty files and missing final newlines.
func Unified(path, oldContent, newContent string) string {
	if oldContent == newContent {
		return ""
	}
	oldWindow, newWindow, offset := changedLineWindow(oldContent, newContent)
	a, b := unifiedDiffLines(oldWindow), unifiedDiffLines(newWindow)
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	aEnd, bEnd := len(a), len(b)
	for aEnd > prefix && bEnd > prefix && a[aEnd-1] == b[bEnd-1] {
		aEnd--
		bEnd--
	}
	// Keep identical edges out of the matcher so repeated context cannot
	// move an edit toward a window boundary.
	ops := difflib.NewMatcher(a[prefix:aEnd], b[prefix:bEnd]).GetOpCodes()
	var out strings.Builder
	if path != "" {
		fmt.Fprintf(&out, "--- %s\n+++ %s\n", path, path)
	}
	for groupStart := 0; groupStart < len(ops); {
		groupEnd := groupStart + 1
		for groupEnd < len(ops) && !(ops[groupEnd].Tag == 'e' && ops[groupEnd].I2-ops[groupEnd].I1 > 2*contextLines) {
			groupEnd++
		}
		first, last := ops[groupStart], ops[groupEnd-1]
		aStart, bStart := max(0, prefix+first.I1-contextLines), max(0, prefix+first.J1-contextLines)
		aStop, bStop := min(len(a), prefix+last.I2+contextLines), min(len(b), prefix+last.J2+contextLines)
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", unifiedRange(offset+aStart, offset+aStop), unifiedRange(offset+bStart, offset+bStop))
		for _, line := range a[aStart : prefix+first.I1] {
			out.WriteByte(' ')
			out.WriteString(line)
		}
		for _, op := range ops[groupStart:groupEnd] {
			if op.Tag == 'e' {
				for _, line := range a[prefix+op.I1 : prefix+op.I2] {
					out.WriteByte(' ')
					out.WriteString(line)
				}
				continue
			}
			if op.Tag == 'd' || op.Tag == 'r' {
				for _, line := range a[prefix+op.I1 : prefix+op.I2] {
					out.WriteByte('-')
					out.WriteString(line)
				}
			}
			if op.Tag == 'i' || op.Tag == 'r' {
				for _, line := range b[prefix+op.J1 : prefix+op.J2] {
					out.WriteByte('+')
					out.WriteString(line)
				}
			}
		}
		for _, line := range a[prefix+last.I2 : aStop] {
			out.WriteByte(' ')
			out.WriteString(line)
		}
		groupStart = groupEnd + 1
	}
	return out.String()
}

func changedLineWindow(old, next string) (string, string, int) {
	prefix, offset := 0, 0
	for prefix < len(old) {
		end := strings.IndexByte(old[prefix:], '\n')
		if end < 0 || !strings.HasPrefix(next[prefix:], old[prefix:prefix+end+1]) {
			break
		}
		prefix += end + 1
		offset++
	}
	oldEnd, newEnd := len(old), len(next)
	for oldEnd > prefix && newEnd > prefix {
		searchEnd := oldEnd
		if old[searchEnd-1] == '\n' {
			searchEnd--
		}
		start := prefix + strings.LastIndexByte(old[prefix:searchEnd], '\n') + 1
		line := old[start:oldEnd]
		if !strings.HasSuffix(next[prefix:newEnd], line) {
			break
		}
		newStart := newEnd - len(line)
		if newStart > prefix && next[newStart-1] != '\n' {
			break
		}
		oldEnd, newEnd = start, newStart
	}
	start := prefix
	for i := 0; i < contextLines && start > 0; i++ {
		start = strings.LastIndexByte(old[:start-1], '\n') + 1
		offset--
	}
	for i := 0; i < contextLines && oldEnd < len(old); i++ {
		end := strings.IndexByte(old[oldEnd:], '\n')
		if end < 0 {
			newEnd += len(old) - oldEnd
			oldEnd = len(old)
			break
		}
		oldEnd += end + 1
		newEnd += end + 1
	}
	return old[start:oldEnd], next[start:newEnd], offset
}

func unifiedRange(start, end int) string {
	count := end - start
	if count == 0 {
		return strconv.Itoa(start) + ",0"
	}
	if count == 1 {
		return strconv.Itoa(start + 1)
	}
	return strconv.Itoa(start+1) + "," + strconv.Itoa(count)
}

func unifiedDiffLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.SplitAfter(content, "\n")
	last := len(lines) - 1
	if lines[last] == "" {
		return lines[:last]
	}
	// Each entry is written verbatim after its single diff prefix.
	// Keep the marker in the last entry so hunk counts still count file lines.
	lines[last] += "\n\\ No newline at end of file\n"
	return lines
}
