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
	MaxFileDiffBytes = 64_000

	// MaxParseBytes is the hard ceiling on raw diff input fed to the parser.
	MaxParseBytes = 8_000_000

	// CommitDiffBudget is the output budget for commit-message generation paths.
	// Shared by the CLI oneshot path and the TUI /commit command.
	CommitDiffBudget = 80_000

	// ReviewDiffBudget is the output budget for code-review paths.
	// Reviews must retain complete medium-sized PRs so the approval gate does
	// not depend on Git history that is intentionally absent from an isolated
	// snapshot workspace.
	ReviewDiffBudget = 1_000_000
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

	// Important-1b: before cutting at MaxParseBytes, scan the FULL input for
	// file boundaries beyond the cap so those files still get summary lines
	// rather than vanishing entirely.
	var overBudgetFiles []FileDiff
	if len(raw) > MaxParseBytes {
		overBudgetFiles = scanBoundariesBeyond(raw, MaxParseBytes)
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

	// Append stub entries for files that were beyond MaxParseBytes so they
	// appear as summary lines.
	files = append(files, overBudgetFiles...)

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

	// Compute exact summary line sizes upfront so budget accounting is precise.
	// summaryLineSize[i] is the byte length of summaryLine(files[i])+"\n".
	summaryLineSize := make([]int, len(files))
	for i, f := range files {
		if f.LowSignal() {
			summaryLineSize[i] = len(summaryLine(f)) + 1 // +1 for '\n'
		}
	}

	// exactSummaryReserve computes the byte cost of the summary section for
	// the given set of file indices.  It uses actual rendered line lengths,
	// not an estimate.
	exactSummaryBytes := func() int {
		n := 0
		for i, f := range files {
			if f.LowSignal() {
				n += summaryLineSize[i]
			}
		}
		if n == 0 {
			return 0
		}
		return len(summaryHeader) + 2 + n // header + "\n\n" prefix
	}

	// Assemble high-signal content first, demoting whole files to summary
	// lines once the budget (minus exact space for the summary section) runs
	// out. Per-file caps bound the demotion granularity.
	var content strings.Builder
	content.WriteString(preamble)
	used := len(preamble)

	for _, idx := range normal {
		f := files[idx]
		seg := f.Segment
		if len(seg) > MaxFileDiffBytes {
			seg = cutAtLineBoundary(seg, MaxFileDiffBytes) +
				fmt.Sprintf("\n[... %s: diff truncated at %d bytes ...]\n", f.Path, MaxFileDiffBytes)
			truncated = true
		}
		if maxBytes > 0 {
			reserve := exactSummaryBytes()
			if len(seg) > maxBytes-used-reserve {
				// Demote this and let later (possibly smaller) files try;
				// they fall through to summaries too if they don't fit.
				files[idx].Reason = ReasonOverBudget
				summaryLineSize[idx] = len(summaryLine(files[idx])) + 1
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

		// Write summary lines, but respect the budget. If the summary section
		// itself overflows (pathological long-path case), stop and append an
		// explicit "... and N more" line rather than silently cutting.
		remaining := maxBytes
		if remaining > 0 {
			remaining -= sb.Len()
		}
		skipped := 0
		for _, f := range files {
			if !f.LowSignal() {
				continue
			}
			line := summaryLine(f) + "\n"
			if maxBytes > 0 && remaining > 0 && len(line) > remaining {
				// Budget exhausted mid-summary: count remaining low-signal
				// files and emit a single "N more" line instead.
				skipped++
				continue
			}
			if skipped > 0 {
				// A prior line already didn't fit; skip the rest too.
				skipped++
				continue
			}
			sb.WriteString(line)
			if maxBytes > 0 {
				remaining -= len(line)
			}
		}
		if skipped > 0 {
			more := fmt.Sprintf("... and %d more changed files (truncated)\n", skipped)
			sb.WriteString(more)
			truncated = true
		}
		out = sb.String()
	}

	out = strings.TrimRight(out, "\n")
	return Result{Context: out, Truncated: truncated, LowSignal: summarized}
}

// scanBoundariesBeyond finds all "diff --git" file headers that start at or
// after byteOffset in s.  It returns stub FileDiff entries (no segment content,
// just path and [over budget] reason) so those files still appear as summary
// lines even though their diff content was discarded.
func scanBoundariesBeyond(s string, byteOffset int) []FileDiff {
	var result []FileDiff
	tail := s[byteOffset:]
	// Find every "\ndiff --git " (or start-of-string) in the tail.
	start := 0
	for {
		var lineStart int
		if start == 0 && strings.HasPrefix(tail[start:], diffBoundary) {
			lineStart = start
		} else {
			j := strings.Index(tail[start:], "\n"+diffBoundary)
			if j < 0 {
				break
			}
			lineStart = start + j + 1 // skip the '\n'
		}

		// Extract the header line.
		end := strings.IndexByte(tail[lineStart:], '\n')
		var headerLine string
		if end < 0 {
			headerLine = tail[lineStart:]
		} else {
			headerLine = tail[lineStart : lineStart+end]
		}
		oldPath, newPath := parseHeaderPaths(headerLine)
		if newPath == "" {
			newPath = oldPath
		}
		fd := FileDiff{
			Path:    newPath,
			OldPath: oldPath,
			Reason:  "over budget",
		}
		if fd.OldPath == fd.Path {
			fd.OldPath = ""
		}
		result = append(result, fd)

		if end < 0 {
			break
		}
		start = lineStart + end + 1
	}
	return result
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
			case strings.HasPrefix(line, `+++ "b/`):
				// Quoted form: +++ "b/path"
				fd.Path = unquotePath(strings.TrimPrefix(strings.TrimRight(line, "\t "), `+++ `))
				fd.Path = strings.TrimPrefix(fd.Path, "b/")
			case strings.HasPrefix(line, "+++ b/"):
				fd.Path = unquotePath(strings.TrimPrefix(line, "+++ b/"))
			case strings.HasPrefix(line, `--- "a/`):
				// Quoted form: --- "a/path"
				fd.OldPath = unquotePath(strings.TrimPrefix(strings.TrimRight(line, "\t "), `--- `))
				fd.OldPath = strings.TrimPrefix(fd.OldPath, "a/")
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
// It handles both the unquoted form ("diff --git a/foo b/foo") and git's
// C-quoted form ("diff --git \"a/foo\" \"b/foo\"") used for paths containing
// non-ASCII characters or special bytes.
func parseHeaderPaths(header string) (oldPath, newPath string) {
	rest := strings.TrimPrefix(header, diffBoundary)
	rest = strings.TrimSpace(rest)

	// Quoted form: "a/..." "b/..."
	// consumeQuoted returns the token WITH surrounding quotes, so we must
	// unquote/unescape first (which strips the outer quotes) and only then
	// strip the a/ / b/ prefix.  Doing TrimPrefix before unquoting is a
	// no-op because the string starts with '"', not 'a'.
	if strings.HasPrefix(rest, `"`) {
		aQuoted, afterA := consumeQuoted(rest)
		afterA = strings.TrimLeft(afterA, " ")
		bQuoted, _ := consumeQuoted(afterA)
		oldPath = strings.TrimPrefix(unquotePath(aQuoted), "a/")
		newPath = strings.TrimPrefix(unquotePath(bQuoted), "b/")
		return oldPath, newPath
	}

	// Unquoted form: a/... b/...
	if i := strings.Index(rest, " b/"); i >= 0 {
		oldPath = unquotePath(strings.TrimPrefix(rest[:i], "a/"))
		newPath = unquotePath(rest[i+3:])
	}
	return oldPath, newPath
}

// consumeQuoted returns the content inside the leading double-quoted token
// (including the quotes) and the remainder of s after the closing quote.
// If s does not start with '"', it returns ("", s).
func consumeQuoted(s string) (quoted, rest string) {
	if !strings.HasPrefix(s, `"`) {
		return "", s
	}
	i := 1
	for i < len(s) {
		if s[i] == '\\' {
			i += 2 // skip escape sequence
			continue
		}
		if s[i] == '"' {
			return s[:i+1], s[i+1:]
		}
		i++
	}
	return s, "" // unterminated quote
}

// unquotePath strips trailing terminators and git's C-style quoting from a
// path token.  For quoted paths it removes the surrounding double-quotes and
// decodes octal escape sequences (\nnn) that git uses for non-ASCII bytes,
// producing a displayable (UTF-8) representation.  Full fidelity is not
// required; the goal is a non-empty, recognizable path string.
func unquotePath(p string) string {
	p = strings.TrimRight(p, "\t ")
	if len(p) >= 2 && strings.HasPrefix(p, `"`) && strings.HasSuffix(p, `"`) {
		inner := p[1 : len(p)-1]
		p = unescapeGitPath(inner)
	}
	return p
}

// unescapeGitPath decodes the C-style escape sequences that git inserts into
// quoted path strings.  Only octal sequences (\nnn) and common single-char
// escapes are handled; full C-string unescaping is not required.
func unescapeGitPath(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			i++
			continue
		}
		next := s[i+1]
		switch {
		case next >= '0' && next <= '7' && i+3 < len(s) && s[i+2] >= '0' && s[i+2] <= '7' && s[i+3] >= '0' && s[i+3] <= '7':
			// Octal escape \nnn
			val := (int(next-'0') << 6) | (int(s[i+2]-'0') << 3) | int(s[i+3]-'0')
			b.WriteByte(byte(val))
			i += 4
		case next == 'n':
			b.WriteByte('\n')
			i += 2
		case next == 't':
			b.WriteByte('\t')
			i += 2
		case next == '\\':
			b.WriteByte('\\')
			i += 2
		case next == '"':
			b.WriteByte('"')
			i += 2
		default:
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
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
