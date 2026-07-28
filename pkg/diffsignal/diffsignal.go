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
	"path"
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

	// ReviewShardBudget is the high-signal byte target per shard when a diff
	// is too large for one review pass. PrioritizeShards derives the shard
	// count purely from content (ceil(HighSignalBytes/ReviewShardBudget));
	// there is no upper bound on shard count, because the requirement is
	// full coverage of a PR of any size, not a cap that silently drops
	// content. Concurrency (how many shards run at once), not shard count,
	// is where callers bound cost — that limit belongs in the orchestration
	// layer that runs each shard through a model, not here.
	ReviewShardBudget = ReviewDiffBudget
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

	// ReasonUnavailable marks a file whose diff content could not be
	// retrieved from the source at all. For example, GitHub's pull request
	// files API omits the `patch` field for binary content and for diffs
	// that exceed GitHub's own size threshold; a caller reconstructing a
	// diff from that API has no hunks to show for such a file. This differs
	// from ReasonOverBudget, which demotes content Buckley itself chose not
	// to show; ReasonUnavailable means the content was never available to
	// demote in the first place.
	ReasonUnavailable Reason = "diff unavailable"
)

// UnavailableMarker is the sentinel line a diff reconstructor can insert in
// place of a file's hunks when the upstream source withheld the content
// entirely. Split classifies any file whose segment contains this exact
// line as ReasonUnavailable, so the file still surfaces as an explicit,
// honestly-labeled entry instead of vanishing or being misread as an empty
// (no-op) change.
const UnavailableMarker = "Buckley: diff content unavailable from source (binary, or exceeds the source's own diff size threshold)"

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
	Path        string
	OldPath     string // set for renames/copies
	Segment     string // raw segment text, exactly as emitted by git
	Insertions  int
	Deletions   int
	Binary      bool
	Unavailable bool // segment contains UnavailableMarker
	Reason      Reason
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

// Shard is one review pass's worth of high-signal diff content: a coherent
// slice of files that fits ReviewShardBudget (or a caller-chosen budget) on
// its own.
type Shard struct {
	// Context is the concatenated segment text for this shard's files, in
	// the same emission order PrioritizeShards assigned them.
	Context string

	// Files lists, in emission order, the high-signal file paths carried by
	// this shard. Every high-signal file appears in exactly one Shard's
	// Files across a ShardResult: PrioritizeShards never splits a single
	// file's diff across two shards, and never drops one.
	Files []string
}

// ShardResult is the fan-out counterpart to Result. Prioritize compresses
// budget overflow into one-line summaries; PrioritizeShards instead keeps
// every high-signal file at full fidelity by splitting them across as many
// shards as the content requires. Shard count is derived only from content
// (ceil(HighSignalBytes/shardBudget)) and has no upper bound: a PR of any
// size gets full coverage, never a truncated subset. Low-signal files never
// consume a shard; they are reported once, in Summary and LowSignal, for
// whichever synthesis step needs the accounting.
type ShardResult struct {
	// Shards holds one entry per review pass, in file-emission order. Empty
	// when every changed file was low-signal (there is nothing to review).
	Shards []Shard

	// LowSignal is every file classified as generated, minified, binary, or
	// diff-unavailable. These are reported by Reason, never silently
	// dropped, and never counted toward HighSignalBytes/HighSignalFiles.
	LowSignal []FileDiff

	// Summary is the rendered low-signal section (one line per LowSignal
	// entry). Empty when LowSignal is empty.
	Summary string

	// HighSignalBytes is the total reviewable-content size PrioritizeShards
	// measured: the sum of each high-signal file's segment length, after
	// the same per-file MaxFileDiffBytes cap Prioritize applies. This is
	// the number the shard count is derived from; it deliberately excludes
	// every LowSignal file, so a PR whose bulk is generated bundles reports
	// a small number here even when its raw byte count or file count is
	// large.
	HighSignalBytes int

	// HighSignalFiles is len(LowSignal)'s complement: the count of files
	// that actually consumed shard space.
	HighSignalFiles int

	// Truncated is true only when a per-file cap (MaxFileDiffBytes) or the
	// MaxParseBytes raw-input ceiling cut real content. Shard count itself
	// never truncates content: PrioritizeShards adds shards instead of
	// demoting or dropping high-signal files.
	Truncated bool
}

// HighSignalStats classifies raw without allocating shard content, for
// callers that need the fan-out metric (to project shard count and cost, or
// to decide whether sharding is needed at all) before committing to full
// partitioning.
func HighSignalStats(raw string) (bytes, files int) {
	_, parsed, _ := splitForShardPrioritization(raw)
	for _, f := range parsed {
		if f.LowSignal() {
			continue
		}
		bytes += highSignalFileBytes(f)
		files++
	}
	return bytes, files
}

// EffectiveShardBudget returns the per-shard budget that targets exactly
// wantShards shards for a diff whose HighSignalBytes total is known. Callers
// that need to force a specific shard count (for example, a CLI testing
// flag) compute HighSignalStats once and pass the byte total here.
func EffectiveShardBudget(highSignalBytes, wantShards int) int {
	if wantShards <= 0 || highSignalBytes <= 0 {
		return 0
	}
	budget := (highSignalBytes + wantShards - 1) / wantShards
	if budget < 1 {
		budget = 1
	}
	return budget
}

// highSignalFileBytes is the size PrioritizeShards and HighSignalStats count
// toward HighSignalBytes for one file: its segment length after the same
// MaxFileDiffBytes cap Prioritize applies, so the metric matches what a
// shard will actually contain.
func highSignalFileBytes(f FileDiff) int {
	if len(f.Segment) > MaxFileDiffBytes {
		return MaxFileDiffBytes
	}
	return len(f.Segment)
}

// splitForShardPrioritization applies the same raw-size ceiling and
// stub-entry scanning Prioritize uses, then classifies the remainder into
// per-file segments. It is factored out so HighSignalStats and
// PrioritizeShards agree exactly on what counts as high-signal without
// duplicating the MaxParseBytes handling.
func splitForShardPrioritization(raw string) (preamble string, files []FileDiff, truncated bool) {
	var overBudgetFiles []FileDiff
	if len(raw) > MaxParseBytes {
		overBudgetFiles = scanBoundariesBeyond(raw, MaxParseBytes)
		raw = cutAtLineBoundary(raw, MaxParseBytes)
		truncated = true
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil, truncated
	}
	preamble, files = splitWithPreamble(raw)
	files = append(files, overBudgetFiles...)
	return preamble, files, truncated
}

// PrioritizeShards partitions a unified diff into one or more budget-bound
// shards. Unlike Prioritize, high-signal content that exceeds shardBudget is
// never demoted to a summary line and never dropped: it is split into
// additional shards instead, one per review pass, with no upper bound on
// shard count. Low-signal files (generated, minified, binary,
// diff-unavailable) are summarized once and never consume a shard.
//
// Files are grouped by directory first, and a group is split across shards
// only when the group alone exceeds shardBudget; a single file's diff is
// never split across two shards. Directory-first grouping is nearly free:
// git (and GitHub's files API) already emits changed files in path order,
// which already clusters a directory's files together, so this mostly
// preserves emission order and only reorders when a caller's input is not
// already path-sorted.
//
// shardBudget <= 0 uses ReviewShardBudget.
func PrioritizeShards(raw string, shardBudget int) ShardResult {
	if shardBudget <= 0 {
		shardBudget = ReviewShardBudget
	}

	_, files, truncated := splitForShardPrioritization(raw)

	var highSignal, lowSignal []FileDiff
	for _, f := range files {
		if f.LowSignal() {
			lowSignal = append(lowSignal, f)
		} else {
			highSignal = append(highSignal, f)
		}
	}

	highSignalBytes := 0
	for _, f := range highSignal {
		highSignalBytes += highSignalFileBytes(f)
	}

	shards, capTruncated := packShards(highSignal, shardBudget)
	if capTruncated {
		truncated = true
	}

	return ShardResult{
		Shards:          shards,
		LowSignal:       lowSignal,
		Summary:         renderLowSignalSummary(lowSignal),
		HighSignalBytes: highSignalBytes,
		HighSignalFiles: len(highSignal),
		Truncated:       truncated,
	}
}

// packShards groups high-signal files by directory (stably, preserving each
// directory's first-appearance order) and greedily bin-packs whole
// directory groups into shards. A group is split at file granularity only
// when the group's own total exceeds shardBudget; a single file is never
// split across shards, even if capping it at MaxFileDiffBytes was already
// necessary (that per-file cap is orthogonal to sharding and matches
// Prioritize's existing behavior).
func packShards(highSignal []FileDiff, shardBudget int) (shards []Shard, truncated bool) {
	if len(highSignal) == 0 {
		return nil, false
	}

	type group struct {
		dir   string
		files []FileDiff
		size  int
	}
	order := make([]string, 0)
	groups := make(map[string]*group)
	for _, f := range highSignal {
		seg := f.Segment
		if len(seg) > MaxFileDiffBytes {
			seg = cutAtLineBoundary(seg, MaxFileDiffBytes) +
				fmt.Sprintf("\n[... %s: diff truncated at %d bytes ...]\n", f.Path, MaxFileDiffBytes)
			f.Segment = seg
			truncated = true
		}
		dir := path.Dir(f.Path)
		g, ok := groups[dir]
		if !ok {
			g = &group{dir: dir}
			groups[dir] = g
			order = append(order, dir)
		}
		g.files = append(g.files, f)
		g.size += len(seg)
	}

	var currentFiles []string
	var currentContent strings.Builder
	flush := func() {
		if currentContent.Len() == 0 {
			return
		}
		shards = append(shards, Shard{Context: currentContent.String(), Files: currentFiles})
		currentContent.Reset()
		currentFiles = nil
	}
	appendFile := func(f FileDiff) {
		currentContent.WriteString(f.Segment)
		currentFiles = append(currentFiles, f.Path)
	}

	for _, dir := range order {
		g := groups[dir]
		if g.size <= shardBudget {
			// The whole group must land in one shard: start a fresh shard
			// if it does not fit in whatever the current shard has left.
			if currentContent.Len() > 0 && currentContent.Len()+g.size > shardBudget {
				flush()
			}
			for _, f := range g.files {
				appendFile(f)
			}
			continue
		}
		// The group alone exceeds shardBudget: it must split, but never a
		// single file's own diff.
		flush()
		for _, f := range g.files {
			if currentContent.Len() > 0 && currentContent.Len()+len(f.Segment) > shardBudget {
				flush()
			}
			appendFile(f)
		}
	}
	flush()

	return shards, truncated
}

// renderLowSignalSummary renders the shared low-signal section for
// PrioritizeShards callers: one line per suppressed file, in original
// emission order, with no arbitrary length cap. Unlike Prioritize's summary
// section (which shares a single output budget with high-signal content and
// so can itself overflow), PrioritizeShards keeps high-signal content out of
// this budget entirely by sharding, and the low-signal section alone is
// bounded by the number of changed files, not by shardBudget.
func renderLowSignalSummary(lowSignal []FileDiff) string {
	if len(lowSignal) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(summaryHeader)
	sb.WriteString("\n")
	for _, f := range lowSignal {
		sb.WriteString(summaryLine(f))
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
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
			case line == UnavailableMarker:
				fd.Unavailable = true
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
	case fd.Unavailable:
		fd.Reason = ReasonUnavailable
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
