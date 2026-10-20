package textdiff

import (
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// Unified formats a three-context-line unified diff, preserving
// empty files and missing final newlines.
func Unified(path, oldContent, newContent string) string {
	if oldContent == newContent {
		return ""
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        unifiedDiffLines(oldContent),
		B:        unifiedDiffLines(newContent),
		FromFile: path,
		ToFile:   path,
		Context:  3,
	})
	if err != nil {
		return fmt.Sprintf("--- %s\n+++ %s\n", path, path)
	}
	return diff
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
