package diffsignal

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// siblingSuffixes are the compressed copies and source maps a build writes
// beside the file it produced.
var siblingSuffixes = []string{".br", ".gz", ".map"}

// generatedHeaderRE matches the standard marker (https://go.dev/s/generatedcode)
// after an optional comment opener. It is checked on the first lines of a new
// or fully shown file.
var generatedHeaderRE = regexp.MustCompile(`^(?://|#|/\*|<!--|--|;)?\s*Code generated .* DO NOT EDIT\.?`)

var commentLineRE = regexp.MustCompile(`^(?://|#|/\*|\*|<!--|--|;)`)

var hunkStartsAtLineOneRE = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+1(?:,\d+)? @@`)

// hasGeneratedHeader reports whether the diff segment shows a "Code generated
// ... DO NOT EDIT." line at the top of the new file. A diff that does not
// reach line 1 cannot show the header, so it is not recognized here.
func hasGeneratedHeader(seg string) bool {
	body := hunkBody(seg)
	if body == "" {
		return false
	}
	lines := strings.SplitN(body, "\n", 14)
	if !hunkStartsAtLineOneRE.MatchString(lines[0]) {
		return false
	}
	for _, line := range lines[1:] {
		if line == "" || (line[0] != '+' && line[0] != ' ') {
			continue
		}
		text := strings.TrimSpace(line[1:])
		if generatedHeaderRE.MatchString(text) {
			return true
		}
		// The marker must come before any code, as in the Go convention.
		if text != "" && !commentLineRE.MatchString(text) {
			return false
		}
	}
	return false
}

// markGeneratedSiblings classifies a file as build output when the diff also
// changes a compressed copy or source map of it that is itself generated.
func markGeneratedSiblings(files []FileDiff) {
	generated := make(map[string]bool, len(files))
	for _, f := range files {
		if f.Generated() {
			generated[f.Path] = true
		}
	}
	for i := range files {
		if files[i].Reason != ReasonNone {
			continue
		}
		for _, suffix := range siblingSuffixes {
			if generated[files[i].Path+suffix] {
				files[i].Reason = ReasonGeneratedSibling
				break
			}
		}
	}
}

// GeneratedGroup is one summary entry for generated files that share a
// directory and extension.
type GeneratedGroup struct {
	// Pattern is a glob such as "client/js/testdata/*.html", or the exact
	// path when the group holds one file.
	Pattern string
	// Files are the group's paths in diff order.
	Files []string
	// Bytes is the size of the group's diff text.
	Bytes int

	// single is the group's only file; a one-file group renders as that
	// file's own summary line, which carries its line counts and reason.
	single FileDiff
}

// Line renders the group as one summary line.
func (g GeneratedGroup) Line() string {
	if len(g.Files) == 1 {
		return summaryLine(g.single)
	}
	return fmt.Sprintf("%s | %d files | %s changed [generated: content omitted]", g.Pattern, len(g.Files), FormatBytes(g.Bytes))
}

// FormatBytes renders a byte count for a summary line.
func FormatBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// GroupGenerated collapses the generated files into one group per directory
// and extension, in order of first appearance. Compressed copies and source
// maps keep their full extension (".js.br") so they form their own group.
func GroupGenerated(files []FileDiff) []GeneratedGroup {
	var groups []*GeneratedGroup
	index := map[string]*GeneratedGroup{}
	for _, f := range files {
		if !f.Generated() {
			continue
		}
		key := groupPattern(f.Path)
		g := index[key]
		if g == nil {
			g = &GeneratedGroup{Pattern: key}
			index[key] = g
			groups = append(groups, g)
		}
		g.Files = append(g.Files, f.Path)
		g.single = f
		g.Bytes += len(f.Segment)
	}
	out := make([]GeneratedGroup, 0, len(groups))
	for _, g := range groups {
		if len(g.Files) == 1 {
			g.Pattern = g.Files[0]
		}
		out = append(out, *g)
	}
	return out
}

func groupPattern(p string) string {
	dir, base := path.Split(p)
	ext := path.Ext(base)
	if ext == "" {
		return p
	}
	for _, suffix := range siblingSuffixes {
		if ext == suffix {
			if inner := path.Ext(strings.TrimSuffix(base, suffix)); inner != "" {
				ext = inner + suffix
			}
			break
		}
	}
	return dir + "*" + ext
}

// GeneratedFiles lists the paths of the generated files in files.
func GeneratedFiles(files []FileDiff) []string {
	var out []string
	for _, f := range files {
		if f.Generated() {
			out = append(out, f.Path)
		}
	}
	return out
}

// ClassifyDiff splits raw and classifies every file, including files past the
// parse ceiling, which are classified from their path alone.
func ClassifyDiff(raw string) []FileDiff {
	_, files, _ := splitForShardPrioritization(raw)
	return files
}

// classifyStubs marks files past the parse ceiling as generated when their
// path says so. They have no diff text, so path rules are all that can apply;
// every other stub keeps its over-budget reason.
func classifyStubs(stubs []FileDiff, generated func(string) bool) {
	if len(stubs) == 0 {
		return
	}
	if generated == nil {
		paths := make([]string, len(stubs))
		for i := range stubs {
			paths[i] = stubs[i].Path
		}
		generated = resolveDefaultGenerated(paths)
	}
	for i := range stubs {
		switch {
		case generated != nil && generated(stubs[i].Path):
			stubs[i].Reason = ReasonGeneratedAttr
		case isGeneratedPath(stubs[i].Path):
			stubs[i].Reason = ReasonGeneratedPath
		}
	}
}
