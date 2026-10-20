package textdiff

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// Unified formats a three-context-line unified diff, preserving
// empty files and missing final newlines.
func Unified(path, oldContent, newContent string) string {
	if oldContent == newContent {
		return ""
	}
	a, b := unifiedDiffLines(oldContent), unifiedDiffLines(newContent)
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	aEnd, bEnd := len(a), len(b)
	for aEnd > prefix && bEnd > prefix && a[aEnd-1] == b[bEnd-1] {
		aEnd--
		bEnd--
	}
	const context = 3
	// Keep identical edges out of the matcher so repeated context cannot
	// move an edit toward a window boundary.
	ops := difflib.NewMatcher(a[prefix:aEnd], b[prefix:bEnd]).GetOpCodes()
	var out strings.Builder
	if path != "" {
		fmt.Fprintf(&out, "--- %s\n+++ %s\n", path, path)
	}
	for groupStart := 0; groupStart < len(ops); {
		groupEnd := groupStart + 1
		for groupEnd < len(ops) && !(ops[groupEnd].Tag == 'e' && ops[groupEnd].I2-ops[groupEnd].I1 > 2*context) {
			groupEnd++
		}
		first, last := ops[groupStart], ops[groupEnd-1]
		aStart, bStart := max(0, prefix+first.I1-context), max(0, prefix+first.J1-context)
		aStop, bStop := min(len(a), prefix+last.I2+context), min(len(b), prefix+last.J2+context)
		fmt.Fprintf(&out, "@@ -%s +%s @@\n", unifiedRange(aStart, aStop), unifiedRange(bStart, bStop))
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
	// difflib writes each entry verbatim after its single diff prefix.
	// Keep the marker in the last entry so hunk counts still count file lines.
	lines[last] += "\n\\ No newline at end of file\n"
	return lines
}
