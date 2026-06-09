// Package diffsignal shapes raw unified diffs into model-friendly context:
// hand-written source changes first at full fidelity, low-signal bulk
// (binary, generated paths, minified content) reduced to one-line summaries.
//
// Motivation: small models generate commit messages and review notes from
// truncated `git diff` output. In repos that commit built artifacts (minified
// bundles, source maps), an alphabetically early artifact can consume the
// entire context budget before the model ever sees the hand-written change,
// producing confidently hallucinated output. Prioritize fixes WHAT fills the
// budget without changing its size.
package diffsignal

import (
	"fmt"
	"strings"
)

// Thresholds for low-signal classification and assembly.
const (
	// MaxSignalLineLen is the longest a single diff body line can be before
	// the whole file is classified as minified content.
	MaxSignalLineLen = 2000

	// MinifiedAvgBytesPerLine is the average bytes-per-line above which a
	// file body is classified as minified content.
	MinifiedAvgBytesPerLine = 512

	// MinifiedRatioMinBytes is the smallest body for which the average
	// bytes-per-line heuristic applies; tiny diffs are never ratio-classified.
	MinifiedRatioMinBytes = 4096

	// MaxFileDiffBytes caps the diff content included for a single
	// high-signal file so one large file cannot starve the files after it.
	MaxFileDiffBytes = 16_000

	// MaxParseBytes is the hard ceiling on raw diff input fed to the parser.
	MaxParseBytes = 8_000_000

	// summaryLineReserve is the per-summary-line byte estimate used when
	// reserving budget for the low-signal section during assembly.
	summaryLineReserve = 112
)

// summaryHeader introduces the low-signal section of the assembled context.
const summaryHeader = "=== Low-signal changes (diff content omitted) ==="

const diffBoundary = "diff --git "

// Reason explains why a file's diff content was suppressed.
type Reason string

const (
	ReasonNone          Reason = ""
	ReasonBinary        Reason = "binary"
	ReasonGeneratedPath Reason = "generated path"
	ReasonMinified      Reason = "minified"
	ReasonOverBudget    Reason = "over budget"
)

// generatedSuffixes are file suffixes for common built artifacts.
var generatedSuffixes = []string{".min.js", ".min.mjs", ".min.css", ".map", ".gz", ".br"}

// generatedDirs are directory names that hold built or vendored content.
var generatedDirs = map[string]bool{
	"dist":         true,
	"build":        true,
	"vendor":       true,
	"node_modules": true,
}

// FileDiff is one file's segment of a unified diff.
type FileDiff struct {
	Path       string
	OldPath    string // set for renames/copies
	Segment    string // raw segment text, exactly as emitted by git
	Insertions int
	Deletions  int
	Binary     bool
	Reason     Reason
}

// LowSignal reports whether the file's content was classified as noise.
func (fd FileDiff) LowSignal() bool { return fd.Reason != ReasonNone }

// Result is the assembled, budget-respecting diff context.
type Result struct {
	// Context is the assembled text: high-signal diffs first, then one-line
	// summaries for every suppressed file.
	Context string

	// Truncated is true when real content was cut (per-file cap, total
	// budget, or oversized raw input). Low-signal summarization alone does
	// not set it; the summary section is self-describing.
	Truncated bool

	// LowSignal counts files reduced to summary lines.
	LowSignal int
}

// Split parses a unified diff into per-file segments and classifies each.
// Concatenating the returned Segments reproduces the input from the first
// "diff --git" boundary onward.
func Split(raw string) []FileDiff {
	_, files := splitWithPreamble(raw)
	return files
}

// Prioritize reorders a unified diff so high-signal source changes fill the
// budget first and low-signal files appear only as summary lines. maxBytes
// is the total output budget; values <= 0 mean no total budget (per-file
// caps and low-signal summarization still apply).
func Prioritize(raw string, maxBytes int) Result {
	truncated := false
	if len(raw) > MaxParseBytes {
		raw = cutAtLineBoundary(raw, MaxParseBytes)
		truncated = true
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Result{}
	}

	preamble, files := splitWithPreamble(raw)
	if len(files) == 0 {
		// Not a recognizable per-file diff: preserve legacy behavior.
		if maxBytes > 0 && len(raw) > maxBytes {
			return Result{Context: cutAtLineBoundary(raw, maxBytes), Truncated: true}
		}
		return Result{Context: raw, Truncated: truncated}
	}

	// Partition while preserving git's emission order within each class.
	var normal []int
	summarized := 0
	for i, f := range files {
		if f.LowSignal() {
			summarized++
		} else {
			normal = append(normal, i)
		}
	}

	// Assemble high-signal content first, demoting whole files to summary
	// lines once the budget (minus a reserve for the summary section) runs
	// out. Per-file caps bound the demotion granularity.
	var content strings.Builder
	content.WriteString(preamble)
	used := len(preamble)

	for i, idx := range normal {
		f := files[idx]
		seg := f.Segment
		if len(seg) > MaxFileDiffBytes {
			seg = cutAtLineBoundary(seg, MaxFileDiffBytes) +
				fmt.Sprintf("\n[... %s: diff truncated at %d bytes ...]\n", f.Path, MaxFileDiffBytes)
			truncated = true
		}
		if maxBytes > 0 {
			remainingNormal := len(normal) - i - 1
			if len(seg) > maxBytes-used-summaryReserve(summarized+remainingNormal) {
				// Demote this and let later (possibly smaller) files try;
				// they fall through to summaries too if they don't fit.
				files[idx].Reason = ReasonOverBudget
				summarized++
				truncated = true
				continue
			}
		}
		content.WriteString(seg)
		used += len(seg)
	}

	// Append the summary section in original file order.
	out := content.String()
	if summarized > 0 {
		var sb strings.Builder
		sb.WriteString(out)
		if sb.Len() > 0 && !strings.HasSuffix(out, "\n") {
			sb.WriteString("\n")
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(summaryHeader)
		sb.WriteString("\n")
		for _, f := range files {
			if f.LowSignal() {
				sb.WriteString(summaryLine(f))
				sb.WriteString("\n")
			}
		}
		out = sb.String()
	}

	out = strings.TrimRight(out, "\n")
	if maxBytes > 0 && len(out) > maxBytes {
		out = cutAtLineBoundary(out, maxBytes)
		truncated = true
	}

	return Result{Context: out, Truncated: truncated, LowSignal: summarized}
}

// summaryReserve estimates the bytes needed for a summary section holding n
// entries, so content packing leaves room for it.
func summaryReserve(n int) int {
	if n == 0 {
		return 0
	}
	return len(summaryHeader) + 2 + n*summaryLineReserve
}

// summaryLine renders the one-line representation of a suppressed file:
//
//	client/js/bootstrap.js | 312 ++ / 298 -- [minified: content omitted]
func summaryLine(f FileDiff) string {
	counts := fmt.Sprintf("%d ++ / %d --", f.Insertions, f.Deletions)
	if f.Binary && f.Insertions == 0 && f.Deletions == 0 {
		counts = "bin"
	}
	path := f.Path
	if f.OldPath != "" && f.OldPath != f.Path {
		path = f.OldPath + " -> " + f.Path
	}
	return fmt.Sprintf("%s | %s [%s: content omitted]", path, counts, f.Reason)
}

// splitWithPreamble splits raw into any text before the first file boundary
// plus the per-file segments, classified.
func splitWithPreamble(raw string) (string, []FileDiff) {
	if raw == "" {
		return "", nil
	}
	var starts []int
	if strings.HasPrefix(raw, diffBoundary) {
		starts = append(starts, 0)
	}
	for i := 0; ; {
		j := strings.Index(raw[i:], "\n"+diffBoundary)
		if j < 0 {
			break
		}
		starts = append(starts, i+j+1)
		i += j + 1
	}
	if len(starts) == 0 {
		return raw, nil
	}

	files := make([]FileDiff, 0, len(starts))
	for k, s := range starts {
		end := len(raw)
		if k+1 < len(starts) {
			end = starts[k+1]
		}
		fd := parseSegment(raw[s:end])
		classify(&fd)
		files = append(files, fd)
	}
	return raw[:starts[0]], files
}

// parseSegment extracts metadata from a single per-file diff segment.
func parseSegment(seg string) FileDiff {
	fd := FileDiff{Segment: seg}

	lines := strings.Split(seg, "\n")
	fd.OldPath, fd.Path = parseHeaderPaths(lines[0])

	inHunks := false
	for _, line := range lines[1:] {
		if !inHunks {
			switch {
			case strings.HasPrefix(line, "@@"):
				inHunks = true
			case strings.HasPrefix(line, "+++ b/"):
				fd.Path = unquotePath(strings.TrimPrefix(line, "+++ b/"))
			case strings.HasPrefix(line, "--- a/"):
				fd.OldPath = unquotePath(strings.TrimPrefix(line, "--- a/"))
			case strings.HasPrefix(line, "rename to "):
				fd.Path = unquotePath(strings.TrimPrefix(line, "rename to "))
			case strings.HasPrefix(line, "rename from "):
				fd.OldPath = unquotePath(strings.TrimPrefix(line, "rename from "))
			case strings.HasPrefix(line, "Binary files ") && strings.HasSuffix(line, " differ"):
				fd.Binary = true
			case line == "GIT binary patch":
				fd.Binary = true
			}
			continue
		}
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			fd.Insertions++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			fd.Deletions++
		}
	}

	if fd.OldPath == fd.Path {
		fd.OldPath = ""
	}
	return fd
}

// parseHeaderPaths extracts the a/ and b/ paths from a "diff --git" line.
func parseHeaderPaths(header string) (oldPath, newPath string) {
	rest := strings.TrimPrefix(header, diffBoundary)
	if i := strings.Index(rest, " b/"); i >= 0 {
		oldPath = unquotePath(strings.TrimPrefix(rest[:i], "a/"))
		newPath = unquotePath(rest[i+3:])
	}
	return oldPath, newPath
}

// unquotePath strips trailing terminators and git's quoting from a path.
func unquotePath(p string) string {
	p = strings.TrimRight(p, "\t ")
	if len(p) >= 2 && strings.HasPrefix(p, `"`) && strings.HasSuffix(p, `"`) {
		p = p[1 : len(p)-1]
	}
	return p
}

// classify sets fd.Reason for low-signal files. Precedence: binary content
// (or gitattributes -diff suppression) > generated/built path > minified
// content heuristics.
func classify(fd *FileDiff) {
	switch {
	case fd.Binary:
		fd.Reason = ReasonBinary
	case isGeneratedPath(fd.Path) || (fd.OldPath != "" && isGeneratedPath(fd.OldPath)):
		fd.Reason = ReasonGeneratedPath
	case isMinifiedBody(hunkBody(fd.Segment)):
		fd.Reason = ReasonMinified
	}
}

// isGeneratedPath reports whether the path matches common built-artifact
// patterns: known suffixes or a known directory anywhere above the basename.
func isGeneratedPath(path string) bool {
	lower := strings.ToLower(path)
	for _, suffix := range generatedSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	segments := strings.Split(lower, "/")
	for _, dir := range segments[:len(segments)-1] {
		if generatedDirs[dir] {
			return true
		}
	}
	return false
}

// hunkBody returns the segment content from the first hunk header onward.
func hunkBody(seg string) string {
	if strings.HasPrefix(seg, "@@") {
		return seg
	}
	if i := strings.Index(seg, "\n@@"); i >= 0 {
		return seg[i+1:]
	}
	return ""
}

// isMinifiedBody applies the minified-content heuristics: any extremely long
// line, or a very high average bytes-per-line over a non-trivial body.
func isMinifiedBody(body string) bool {
	if body == "" {
		return false
	}
	longest, lineCount := 0, 0
	for rest := body; rest != ""; {
		var line string
		if i := strings.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i], rest[i+1:]
		} else {
			line, rest = rest, ""
		}
		lineCount++
		if len(line) > longest {
			longest = len(line)
		}
	}
	if longest > MaxSignalLineLen {
		return true
	}
	return len(body) >= MinifiedRatioMinBytes && len(body)/lineCount > MinifiedAvgBytesPerLine
}

// cutAtLineBoundary truncates s to at most n bytes, preferring the last
// complete line.
func cutAtLineBoundary(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := s[:n]
	if i := strings.LastIndexByte(cut, '\n'); i > 0 {
		return cut[:i]
	}
	return cut
}
