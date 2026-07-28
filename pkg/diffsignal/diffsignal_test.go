package diffsignal

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// --- fixture builders -------------------------------------------------------

// minifiedFileDiff returns a realistic staged-diff segment for a minified
// bundle: a single added line of payloadLen bytes (the esbuild signature).
func minifiedFileDiff(path string, payloadLen int) string {
	payload := strings.Repeat("var a=1;", payloadLen/8+1)[:payloadLen]
	return fmt.Sprintf(`diff --git a/%[1]s b/%[1]s
index 1111111..2222222 100644
--- a/%[1]s
+++ b/%[1]s
@@ -1 +1 @@
-(()=>{var old=true})();
+%[2]s
`, path, payload)
}

// sourceFileDiff returns a small hand-written source change.
func sourceFileDiff(path, marker string) string {
	return fmt.Sprintf(`diff --git a/%[1]s b/%[1]s
index 3333333..4444444 100644
--- a/%[1]s
+++ b/%[1]s
@@ -10,6 +10,8 @@ func registerRoutes() {
 	mux.Handle("/health", health)
+	// %[2]s
+	mux.Handle("/retry", retryHandler)
 }
`, path, marker)
}

// binaryFileDiff returns the stanza git emits for binary files and for files
// suppressed via the gitattributes -diff flag.
func binaryFileDiff(path string) string {
	return fmt.Sprintf(`diff --git a/%[1]s b/%[1]s
index 5555555..6666666 100644
Binary files a/%[1]s and b/%[1]s differ
`, path)
}

// largeSourceDiff returns a legit source diff with many short, unique lines
// totalling at least minBytes.
func largeSourceDiff(path string, minBytes int) string {
	var b strings.Builder
	fmt.Fprintf(&b, `diff --git a/%[1]s b/%[1]s
index 7777777..8888888 100644
--- a/%[1]s
+++ b/%[1]s
@@ -1,0 +1,99999 @@
`, path)
	for i := 0; b.Len() < minBytes; i++ {
		fmt.Fprintf(&b, "+func GeneratedHelper%06d() int { return %d } // line %06d\n", i, i, i)
	}
	return b.String()
}

// --- required behavior tests ------------------------------------------------

// (a) A huge minified file alphabetically before a small real source change:
// the assembled context must contain the source change content and ONLY a
// summary line for the minified file.
func TestPrioritizeMinifiedNoiseBeforeSource(t *testing.T) {
	minified := minifiedFileDiff("client/js/bundle.js", 50_000)
	source := sourceFileDiff("pkg/server/handler.go", "retry budget guards the upstream")
	raw := minified + source

	res := Prioritize(strings.TrimSpace(raw), 80_000)

	if !strings.Contains(res.Context, "retry budget guards the upstream") {
		t.Fatalf("source change content missing from assembled context:\n%s", res.Context)
	}
	if strings.Contains(res.Context, "var a=1;var a=1;") {
		t.Errorf("minified payload leaked into assembled context")
	}
	if !strings.Contains(res.Context, "client/js/bundle.js") {
		t.Errorf("minified file path missing — model must still know the file changed:\n%s", res.Context)
	}
	if !strings.Contains(res.Context, string(ReasonMinified)) {
		t.Errorf("expected a %q summary annotation, got:\n%s", ReasonMinified, res.Context)
	}
	if res.LowSignal != 1 {
		t.Errorf("LowSignal = %d, want 1", res.LowSignal)
	}
}

// (b) Binary / gitattributes -diff suppressed stanzas become summary lines.
func TestPrioritizeBinarySuppressedStanza(t *testing.T) {
	binary := binaryFileDiff("client/js/bootstrap.js")
	source := sourceFileDiff("pkg/server/handler.go", "real change here")
	raw := binary + source

	res := Prioritize(strings.TrimSpace(raw), 80_000)

	if !strings.Contains(res.Context, "real change here") {
		t.Fatalf("source change content missing:\n%s", res.Context)
	}
	if !strings.Contains(res.Context, "client/js/bootstrap.js") {
		t.Errorf("binary file path missing from summaries:\n%s", res.Context)
	}
	if !strings.Contains(res.Context, string(ReasonBinary)) {
		t.Errorf("expected a %q summary annotation, got:\n%s", ReasonBinary, res.Context)
	}
	if strings.Contains(res.Context, "Binary files a/client/js/bootstrap.js") {
		t.Errorf("raw binary stanza should be replaced by a summary line")
	}
}

// (c) Ordering: high-signal source diffs come before low-signal summaries,
// regardless of the order git emitted them.
func TestPrioritizeOrderingSourceBeforeSummaries(t *testing.T) {
	minified := minifiedFileDiff("aaa/bundle.js", 30_000) // alphabetically first, like gosx
	source := sourceFileDiff("zzz/handler.go", "ordering sentinel")
	raw := minified + source

	res := Prioritize(strings.TrimSpace(raw), 80_000)

	srcIdx := strings.Index(res.Context, "ordering sentinel")
	sumIdx := strings.Index(res.Context, "aaa/bundle.js")
	if srcIdx < 0 || sumIdx < 0 {
		t.Fatalf("missing source (%d) or summary (%d) in:\n%s", srcIdx, sumIdx, res.Context)
	}
	if srcIdx > sumIdx {
		t.Errorf("source content (idx %d) must come before low-signal summary (idx %d)", srcIdx, sumIdx)
	}
}

// (d) Per-file cap: a very large legitimate source file is truncated per-file
// instead of starving files that come after it.
func TestPrioritizePerFileCap(t *testing.T) {
	big := largeSourceDiff("pkg/big/generated_api.go", 3*MaxFileDiffBytes)
	small := sourceFileDiff("pkg/small/late.go", "late file survives")
	raw := big + small

	const tailLine = "GeneratedHelper002000" // exists in input, beyond the per-file cap
	if !strings.Contains(raw, tailLine) {
		t.Fatalf("fixture too small: %s not present in input", tailLine)
	}

	res := Prioritize(strings.TrimSpace(raw), 200_000)

	if !strings.Contains(res.Context, "GeneratedHelper000000") {
		t.Errorf("head of large source file should be included")
	}
	if strings.Contains(res.Context, tailLine) {
		t.Errorf("tail of large source file should be capped away")
	}
	if !strings.Contains(res.Context, "late file survives") {
		t.Errorf("file after the large one must not be starved:\n%.2000s", res.Context)
	}
	if !res.Truncated {
		t.Errorf("Truncated = false, want true (per-file cap applied)")
	}
}

// (e) The existing total-budget behavior still holds: output never exceeds
// maxBytes, and files that do not fit are demoted to summary lines so the
// model still knows they changed.
func TestPrioritizeTotalBudgetStillHolds(t *testing.T) {
	var raw strings.Builder
	for i := 0; i < 6; i++ {
		raw.WriteString(largeSourceDiff(fmt.Sprintf("pkg/mod%d/file%d.go", i, i), 2*MaxFileDiffBytes))
	}
	budget := 40_000

	res := Prioritize(strings.TrimSpace(raw.String()), budget)

	if len(res.Context) > budget {
		t.Fatalf("len(Context) = %d exceeds budget %d", len(res.Context), budget)
	}
	if !res.Truncated {
		t.Errorf("Truncated = false, want true")
	}
	// Every changed file must still be visible by path.
	for i := 0; i < 6; i++ {
		path := fmt.Sprintf("pkg/mod%d/file%d.go", i, i)
		if !strings.Contains(res.Context, path) {
			t.Errorf("file %s missing entirely from context — must appear at least as a summary", path)
		}
	}
}

// Generated/built-artifact path patterns are summarized even when their
// content looks ordinary.
func TestPrioritizeGeneratedPathPatterns(t *testing.T) {
	lowSignal := []string{
		"app.min.js",
		"styles/site.min.css",
		"client/js/bootstrap.js.map",
		"release/archive.gz",
		"release/page.br",
		"dist/app.js",
		"build/output.css",
		"vendor/lib/lib.go",
		"node_modules/dep/index.js",
		"web/dist/chunk.js",
	}
	for _, path := range lowSignal {
		t.Run(path, func(t *testing.T) {
			raw := sourceFileDiff(path, "innocuous content") + sourceFileDiff("src/app.go", "keep me")
			res := Prioritize(strings.TrimSpace(raw), 80_000)
			if !strings.Contains(res.Context, string(ReasonGeneratedPath)) {
				t.Errorf("%s should be classified %q:\n%s", path, ReasonGeneratedPath, res.Context)
			}
			if res.LowSignal != 1 {
				t.Errorf("LowSignal = %d, want 1", res.LowSignal)
			}
			if !strings.Contains(res.Context, "keep me") {
				t.Errorf("normal source content should be kept")
			}
		})
	}

	// Normal source paths must NOT be classified low-signal.
	for _, path := range []string{"src/app.go", "pkg/build.go", "builder/main.go", "distill/notes.md"} {
		t.Run("keep/"+path, func(t *testing.T) {
			raw := sourceFileDiff(path, "hand written")
			res := Prioritize(strings.TrimSpace(raw), 80_000)
			if res.LowSignal != 0 {
				t.Errorf("%s wrongly classified low-signal:\n%s", path, res.Context)
			}
			if !strings.Contains(res.Context, "hand written") {
				t.Errorf("content for %s should be included in full", path)
			}
		})
	}
}

// A clean, small diff passes through byte-identical: no reordering tax for
// the common case.
func TestPrioritizeSmallCleanDiffUnchanged(t *testing.T) {
	raw := strings.TrimSpace(
		sourceFileDiff("pkg/a/a.go", "first") + sourceFileDiff("pkg/b/b.go", "second"))

	res := Prioritize(raw, 80_000)

	if res.Context != raw {
		t.Errorf("clean diff should pass through unchanged.\ngot:\n%s\nwant:\n%s", res.Context, raw)
	}
	if res.Truncated || res.LowSignal != 0 {
		t.Errorf("Truncated=%v LowSignal=%d, want false/0", res.Truncated, res.LowSignal)
	}
}

func TestReviewDiffBudgetPreservesMediumPRWithoutTruncation(t *testing.T) {
	var raw strings.Builder
	for i := 0; i < 8; i++ {
		raw.WriteString(largeSourceDiff(fmt.Sprintf("pkg/review/file_%d.go", i), 50_000))
	}

	res := Prioritize(raw.String(), ReviewDiffBudget)
	if res.Truncated {
		t.Fatal("review budget truncated a medium-sized multi-file PR")
	}
	for i := 0; i < 8; i++ {
		path := fmt.Sprintf("pkg/review/file_%d.go", i)
		if !strings.Contains(res.Context, path) {
			t.Fatalf("review context omitted %q", path)
		}
	}
}

// Non-diff or unparseable input falls back to the legacy truncation behavior.
func TestPrioritizeFallbackNonDiffInput(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		res := Prioritize("", 80_000)
		if res.Context != "" || res.Truncated {
			t.Errorf("empty input should produce empty result, got %+v", res)
		}
	})

	t.Run("garbage within budget", func(t *testing.T) {
		res := Prioritize("not a diff at all", 80_000)
		if res.Context != "not a diff at all" {
			t.Errorf("non-diff input should pass through, got %q", res.Context)
		}
	})

	t.Run("garbage over budget", func(t *testing.T) {
		raw := strings.Repeat("x", 100) + "\n" + strings.Repeat("y", 100)
		res := Prioritize(raw, 150)
		if len(res.Context) > 150 {
			t.Errorf("len = %d exceeds budget 150", len(res.Context))
		}
		if !res.Truncated {
			t.Errorf("Truncated = false, want true")
		}
	})
}

// --- parser tests -----------------------------------------------------------

func TestSplitParsesFiles(t *testing.T) {
	raw := strings.TrimSpace(
		sourceFileDiff("pkg/a/a.go", "alpha") +
			binaryFileDiff("img/logo.png") +
			minifiedFileDiff("dist/app.js", 5_000))

	files := Split(raw)
	if len(files) != 3 {
		t.Fatalf("got %d files, want 3", len(files))
	}

	if files[0].Path != "pkg/a/a.go" {
		t.Errorf("files[0].Path = %q, want pkg/a/a.go", files[0].Path)
	}
	if files[0].Insertions != 2 || files[0].Deletions != 0 {
		t.Errorf("files[0] counts = +%d/-%d, want +2/-0", files[0].Insertions, files[0].Deletions)
	}
	if files[0].LowSignal() {
		t.Errorf("files[0] wrongly low-signal: %q", files[0].Reason)
	}

	if files[1].Path != "img/logo.png" {
		t.Errorf("files[1].Path = %q, want img/logo.png", files[1].Path)
	}
	if !files[1].Binary || files[1].Reason != ReasonBinary {
		t.Errorf("files[1] should be binary low-signal, got Binary=%v Reason=%q", files[1].Binary, files[1].Reason)
	}

	if files[2].Path != "dist/app.js" {
		t.Errorf("files[2].Path = %q, want dist/app.js", files[2].Path)
	}
	if !files[2].LowSignal() {
		t.Errorf("files[2] should be low-signal")
	}
	if files[2].Insertions != 1 || files[2].Deletions != 1 {
		t.Errorf("files[2] counts = +%d/-%d, want +1/-1", files[2].Insertions, files[2].Deletions)
	}
}

func TestSplitParsesRename(t *testing.T) {
	raw := `diff --git a/old/name.go b/new/name.go
similarity index 96%
rename from old/name.go
rename to new/name.go
index 9999999..aaaaaaa 100644
--- a/old/name.go
+++ b/new/name.go
@@ -1,3 +1,3 @@
-package old
+package new
 // body
`
	files := Split(strings.TrimSpace(raw))
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	if files[0].Path != "new/name.go" {
		t.Errorf("Path = %q, want new/name.go", files[0].Path)
	}
	if files[0].OldPath != "old/name.go" {
		t.Errorf("OldPath = %q, want old/name.go", files[0].OldPath)
	}
}

// Reassembling untouched segments must reproduce the input exactly.
func TestSplitSegmentsRoundTrip(t *testing.T) {
	raw := strings.TrimSpace(
		sourceFileDiff("pkg/a/a.go", "alpha") +
			binaryFileDiff("img/logo.png") +
			sourceFileDiff("pkg/c/c.go", "gamma"))

	files := Split(raw)
	var b strings.Builder
	for _, f := range files {
		b.WriteString(f.Segment)
	}
	if b.String() != raw {
		t.Errorf("segment round-trip mismatch.\ngot:\n%q\nwant:\n%q", b.String(), raw)
	}
}

// Important-1a: long-path summaries must never be silently cut by the final
// hard-cut when the summaryLineReserve underestimates actual line lengths.
//
// Regression probe: a tight budget lets a small source file "sneak in" via the
// underestimated reserve, causing assembled output to exceed budget, and the
// trailing summary lines to get chopped silently.
//
// After the fix: every path must appear in the output OR a single explicit
// "... and N more changed files (truncated)" line accounts for exactly the
// remainder.
func TestNeverDropSummaryLine_LongPaths(t *testing.T) {
	const nSummary = 40
	// Paths with 155-char lengths produce ~197-byte summary lines vs the old
	// 112-byte reserve — gap of ~85 bytes per file (3440 bytes for 40 files).
	base := "src/very/long/generated/path/monorepo/packages/feature/"
	suffix := ".go"
	padding := strings.Repeat("x", 155-len(base)-len(suffix)-2) // -2 for 2-digit index

	src1 := sourceFileDiff("pkg/real/handler.go", "real-sentinel")
	// budget chosen so that the underestimated reserve admits src2 but the
	// accurate reserve would have demoted it.  This causes the assembled output
	// to exceed budget and the hard-cut to chop trailing summary lines.
	budget := len(src1) + 5031

	var raw strings.Builder
	raw.WriteString(src1)
	raw.WriteString(sourceFileDiff("pkg/sneaky/file.go", "small-sneaky-content-1234567890abcdef"))
	paths := make([]string, nSummary)
	for i := 0; i < nSummary; i++ {
		path := fmt.Sprintf("%s%s%02d%s", base, padding, i, suffix)
		paths[i] = path
		raw.WriteString(minifiedFileDiff(path, 5_000))
	}

	res := Prioritize(strings.TrimSpace(raw.String()), budget)

	// Each path must appear, OR there must be a "... and N more changed files (truncated)"
	// line that accounts for exactly the missing remainder.
	missing := 0
	for _, p := range paths {
		if !strings.Contains(res.Context, p) {
			missing++
		}
	}
	if missing > 0 {
		needle := fmt.Sprintf("... and %d more changed files (truncated)", missing)
		if !strings.Contains(res.Context, needle) {
			t.Errorf("%d summary paths silently dropped (no explicit truncation line).\nwanted: %q\ncontext tail:\n%.2000s",
				missing, needle, res.Context[max(0, len(res.Context)-2000):])
		}
		if !res.Truncated {
			t.Errorf("Truncated = false, want true when summary lines overflow budget")
		}
	}
}

// Important-1b: files beyond MaxParseBytes must still appear as summary lines
// with an "[over budget]" reason — they must never vanish silently.
func TestFileBeyondMaxParseBytes_StillGetsSummary(t *testing.T) {
	// Build a diff whose second file starts after MaxParseBytes.
	// First file: just over MaxParseBytes bytes.
	const bigPath = "pkg/big/generated_file.go"
	const smallPath = "pkg/small/real_change.go"

	bigSeg := largeSourceDiff(bigPath, MaxParseBytes+1_000)
	smallSeg := sourceFileDiff(smallPath, "late-file-sentinel")
	raw := bigSeg + smallSeg

	// The small file starts well beyond MaxParseBytes.
	res := Prioritize(strings.TrimSpace(raw), 10_000_000)

	// small file must be visible somehow.
	if !strings.Contains(res.Context, smallPath) {
		t.Errorf("file beyond MaxParseBytes (%q) missing from output — must appear as summary\ncontext:\n%.2000s",
			smallPath, res.Context)
	}
}

// Minor-1: git-quoted header/hunk paths (non-ASCII or control-char filenames)
// must not produce an empty Path ("") → anonymous summary line, and the a/ /
// b/ prefix must be stripped even when the header has no +++/--- lines.
func TestQuotedPathParsing(t *testing.T) {
	// Git quotes paths that contain non-ASCII or special characters with
	// C-style escaping.  Example: a file named "café.go" or "src/über/init.go".
	quotedDiff := `diff --git "a/src/caf\303\251.go" "b/src/caf\303\251.go"
index aaaaaaa..bbbbbbb 100644
--- "a/src/caf\303\251.go"
+++ "b/src/caf\303\251.go"
@@ -1,3 +1,4 @@
 package main
+// quoted path change
 func main() {}
`
	files := Split(strings.TrimSpace(quotedDiff))
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	// Path must be the clean unquoted form without the b/ prefix.
	if got := files[0].Path; got != "src/café.go" {
		t.Errorf("Path = %q, want %q — a//b/ prefix leaked or unescape failed", got, "src/café.go")
	}

	// Prioritize must produce a non-empty path in the summary line.
	res := Prioritize(strings.TrimSpace(quotedDiff), 80_000)
	if strings.Contains(res.Context, " |") {
		// If there's a summary line, it must not start with " | " (empty path).
		for _, line := range strings.Split(res.Context, "\n") {
			if strings.HasPrefix(line, " | ") {
				t.Errorf("summary line has empty path: %q", line)
			}
		}
	}
}

// TestQuotedPathHeaderOnly covers the header-only form (no +++/--- lines),
// which is produced for binary files, pure renames, and deletions.  In this
// case parseSegment never overwrites the path set by parseHeaderPaths, so the
// a/ prefix leak is visible directly.
func TestQuotedPathHeaderOnly(t *testing.T) {
	// Simulate a binary deletion: only the "diff --git" header, no +++ / ---.
	headerOnlyDiff := `diff --git "a/assets/caf\303\251.png" "b/assets/caf\303\251.png"
index aaaaaaa..bbbbbbb 100644
Binary files a/assets/caf\303\251.png and b/assets/caf\303\251.png differ
`
	files := Split(strings.TrimSpace(headerOnlyDiff))
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	const wantPath = "assets/café.png"
	if got := files[0].Path; got != wantPath {
		t.Errorf("header-only quoted path: got %q, want %q (a/ prefix not stripped or unescape failed)", got, wantPath)
	}
}

// TestScanBoundariesBeyondQuotedPath verifies that scanBoundariesBeyond (used
// for over-budget files) strips the a//b/ prefix from quoted paths so that
// summary lines in the prioritized output show the clean file name.
func TestScanBoundariesBeyondQuotedPath(t *testing.T) {
	// Build a diff where the first file fills the parse cap and a second
	// quoted-path file falls beyond it.  We force this by constructing a
	// raw string and calling Prioritize with a tiny budget.
	header := `diff --git "a/src/caf\303\251.go" "b/src/caf\303\251.go"
index aaaaaaa..bbbbbbb 100644
--- "a/src/caf\303\251.go"
+++ "b/src/caf\303\251.go"
@@ -1,2 +1,2 @@
 package main
-func Old() {}
+func New() {}
`
	// Prepend a large filler file so the quoted file is pushed beyond the cap.
	filler := "diff --git a/filler.go b/filler.go\nindex 0000000..1111111 100644\n--- a/filler.go\n+++ b/filler.go\n@@ -1 +1 @@\n-old\n+new\n"
	filler += strings.Repeat("// padding line\n", MaxParseBytes/16)
	raw := filler + header

	res := Prioritize(raw, 200_000)
	// The quoted file is beyond the parse cap, so scanBoundariesBeyond handles
	// it.  Its summary entry must NOT contain the raw b/ prefix.
	if strings.Contains(res.Context, `b/src/`) {
		t.Errorf("scanBoundariesBeyond left b/ prefix in summary line:\n%.500s", res.Context)
	}
	if strings.Contains(res.Context, `"src/`) {
		t.Errorf("scanBoundariesBeyond left surrounding quotes in summary line:\n%.500s", res.Context)
	}
}

// Minified classification via the bytes-per-line ratio (no single huge line,
// but relentlessly dense content).
func TestClassifyMinifiedByRatio(t *testing.T) {
	var b strings.Builder
	b.WriteString(`diff --git a/web/pack.js b/web/pack.js
index bbbbbbb..ccccccc 100644
--- a/web/pack.js
+++ b/web/pack.js
@@ -1,12 +1,12 @@
`)
	line := "+" + strings.Repeat("f(x);", (MinifiedAvgBytesPerLine+200)/5)
	for i := 0; i < 12; i++ {
		b.WriteString(line + "\n")
	}

	files := Split(strings.TrimSpace(b.String()))
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	if files[0].Reason != ReasonMinified {
		t.Errorf("Reason = %q, want %q (ratio heuristic)", files[0].Reason, ReasonMinified)
	}
}

// --- PrioritizeShards -------------------------------------------------------

// manySourceDiffs builds n small, distinct source-file diffs spread across a
// handful of directories so directory-first grouping has something to group.
func manySourceDiffs(n int) (raw string, paths []string) {
	dirs := []string{"pkg/alpha", "pkg/beta", "pkg/gamma", "pkg/delta"}
	var b strings.Builder
	for i := 0; i < n; i++ {
		p := fmt.Sprintf("%s/file%04d.go", dirs[i%len(dirs)], i)
		paths = append(paths, p)
		b.WriteString(sourceFileDiff(p, fmt.Sprintf("marker-%d", i)))
	}
	return b.String(), paths
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// shardFileSet returns the union of every shard's Files, and reports how
// many files appeared more than once (which must always be zero: a file may
// never be split across, or duplicated into, two shards).
func shardFileSet(result ShardResult) (files []string, duplicates int) {
	seen := make(map[string]int)
	for _, shard := range result.Shards {
		for _, f := range shard.Files {
			seen[f]++
			files = append(files, f)
		}
	}
	for _, count := range seen {
		if count > 1 {
			duplicates++
		}
	}
	return files, duplicates
}

func TestPrioritizeShards_TableDriven(t *testing.T) {
	tests := []struct {
		name           string
		raw            string
		expectPaths    []string
		shardBudget    int
		wantShardCount int // -1 means "don't check exact count"
		wantMinShards  int
		wantLowSignal  int
	}{
		{
			// Mutation: revert packShards to always start a fresh shard per
			// file (drop the "fits in current shard" check) and this still
			// passes (1 file trivially fits alone); the case that actually
			// catches that mutation is "over budget" below. This case instead
			// catches a mutation that makes PrioritizeShards demote to a
			// summary line (Prioritize's old behavior) instead of sharding:
			// if that regression lands, wantShardCount would need to become 0.
			name:           "under budget: one shard, every high-signal file present",
			raw:            mustJoin(sourceFileDiff("pkg/a/one.go", "m1"), sourceFileDiff("pkg/a/two.go", "m2")),
			expectPaths:    []string{"pkg/a/one.go", "pkg/a/two.go"},
			shardBudget:    ReviewShardBudget,
			wantShardCount: 1,
		},
		{
			name: "over budget: multiple shards, every high-signal file in exactly one",
			raw: mustJoin(
				largeSourceDiff("pkg/big/one.go", 4000),
				largeSourceDiff("pkg/big2/two.go", 4000),
				largeSourceDiff("pkg/big3/three.go", 4000),
			),
			expectPaths:    []string{"pkg/big/one.go", "pkg/big2/two.go", "pkg/big3/three.go"},
			shardBudget:    4500, // each file alone is ~4000-4500B; budget forces 3 separate shards
			wantShardCount: -1,
			wantMinShards:  2,
		},
		{
			name:           "all low-signal input: zero shards",
			raw:            mustJoin(minifiedFileDiff("dist/bundle.js", 20_000), binaryFileDiff("assets/logo.png")),
			expectPaths:    nil,
			shardBudget:    ReviewShardBudget,
			wantShardCount: 0,
			wantLowSignal:  2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := PrioritizeShards(tc.raw, tc.shardBudget)

			if tc.wantShardCount >= 0 && len(result.Shards) != tc.wantShardCount {
				t.Errorf("len(Shards) = %d, want %d", len(result.Shards), tc.wantShardCount)
			}
			if tc.wantMinShards > 0 && len(result.Shards) < tc.wantMinShards {
				t.Errorf("len(Shards) = %d, want >= %d", len(result.Shards), tc.wantMinShards)
			}
			if len(result.LowSignal) != tc.wantLowSignal {
				t.Errorf("len(LowSignal) = %d, want %d", len(result.LowSignal), tc.wantLowSignal)
			}

			got, duplicates := shardFileSet(result)
			if duplicates != 0 {
				t.Errorf("%d files appeared in more than one shard", duplicates)
			}
			if !equalStringSets(got, tc.expectPaths) {
				t.Errorf("shard file union = %v, want %v", sortedCopy(got), sortedCopy(tc.expectPaths))
			}
		})
	}
}

// TestPrioritizeShards_MetricExcludesLowSignal proves the fan-out trigger is
// post-classification content, not raw file count or byte count: 500
// generated/minified files must measure ~0 high-signal bytes and trigger
// zero shards, even though the raw input is large.
//
// Mutation: change HighSignalBytes accounting (or the classification it
// reads) to count LowSignal files too, and this test fails because
// HighSignalBytes/HighSignalFiles would be far from zero and Shards would be
// non-empty.
func TestPrioritizeShards_MetricExcludesLowSignal(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 500; i++ {
		b.WriteString(minifiedFileDiff(fmt.Sprintf("dist/chunk%04d.min.js", i), 2000))
	}

	result := PrioritizeShards(b.String(), ReviewShardBudget)

	if result.HighSignalBytes != 0 {
		t.Errorf("HighSignalBytes = %d, want 0 for an all-generated/minified diff", result.HighSignalBytes)
	}
	if result.HighSignalFiles != 0 {
		t.Errorf("HighSignalFiles = %d, want 0", result.HighSignalFiles)
	}
	if len(result.Shards) != 0 {
		t.Errorf("len(Shards) = %d, want 0: low-signal files must never consume a review pass", len(result.Shards))
	}
	if len(result.LowSignal) != 500 {
		t.Errorf("len(LowSignal) = %d, want 500", len(result.LowSignal))
	}

	statsBytes, statsFiles := HighSignalStats(b.String())
	if statsBytes != result.HighSignalBytes || statsFiles != result.HighSignalFiles {
		t.Errorf("HighSignalStats = (%d, %d), want to match PrioritizeShards (%d, %d)",
			statsBytes, statsFiles, result.HighSignalBytes, result.HighSignalFiles)
	}
}

// TestPrioritizeShards_JustUnderVsJustOverBudget proves the shard count is a
// direct function of HighSignalBytes vs. the budget: one byte under the
// per-shard budget must still produce one shard, and crossing it must
// produce a second.
//
// Mutation: change the packing comparison from `> shardBudget` to
// `>= shardBudget` (or drop the "fits in current shard" check entirely and
// always start a new shard per group) and the "just under" case starts
// reporting 2 shards for content that fits in 1, failing this test.
func TestPrioritizeShards_JustUnderVsJustOverBudget(t *testing.T) {
	const budget = 5000

	// One directory (one group) so the whole file set is a single atomic
	// unit for packing purposes.
	under := largeSourceDiff("pkg/one/file.go", budget-200)
	stats, _ := HighSignalStats(under)
	if stats >= budget {
		t.Fatalf("fixture HighSignalBytes = %d, want < %d for the 'just under' case", stats, budget)
	}
	res := PrioritizeShards(under, budget)
	if len(res.Shards) != 1 {
		t.Errorf("just-under-budget: len(Shards) = %d, want 1 (got %d high-signal bytes vs budget %d)",
			len(res.Shards), res.HighSignalBytes, budget)
	}

	// Two separate directories/files whose combined size crosses the
	// budget: must split into 2 shards.
	over := mustJoin(largeSourceDiff("pkg/one/file.go", budget-200), largeSourceDiff("pkg/two/file.go", budget-200))
	res = PrioritizeShards(over, budget)
	if len(res.Shards) < 2 {
		t.Errorf("just-over-budget: len(Shards) = %d, want >= 2 (got %d high-signal bytes vs budget %d)",
			len(res.Shards), res.HighSignalBytes, budget)
	}
}

// TestPrioritizeShards_NeverSplitsOversizedFileAcrossShards proves a single
// file bigger than the per-shard budget lands entirely in one shard rather
// than being cut across two.
//
// Mutation: change packShards' per-file loop to flush and slice `f.Segment`
// itself into pieces (splitting one file's diff across shards) and this
// test fails because more than one shard would contain a prefix of the
// oversized file's path, or the file's content would be fragmented.
func TestPrioritizeShards_NeverSplitsOversizedFileAcrossShards(t *testing.T) {
	const budget = 4000
	huge := largeSourceDiff("pkg/huge/file.go", budget*3)
	small := sourceFileDiff("pkg/small/file.go", "tiny")

	res := PrioritizeShards(mustJoin(huge, small), budget)

	shardsWithHuge := 0
	for _, shard := range res.Shards {
		for _, f := range shard.Files {
			if f == "pkg/huge/file.go" {
				shardsWithHuge++
			}
		}
	}
	if shardsWithHuge != 1 {
		t.Errorf("pkg/huge/file.go appeared in %d shards, want exactly 1 (never split a file across shards)", shardsWithHuge)
	}
}

// TestPrioritizeShards_FullCoverageInvariant is the load-bearing honesty
// check: at 1, 10, and 500+ shard scale, the union of every shard's Files
// must equal the high-signal file set exactly, with no remainder bucket and
// no duplicate.
//
// Mutation: reintroduce a clamp (for example, "if len(shards) >= 12, stop
// packing and drop the rest") and this test fails at the 500-shard scale
// because the file-set union would be missing entries.
func TestPrioritizeShards_FullCoverageInvariant(t *testing.T) {
	for _, n := range []int{1, 10, 2500} {
		t.Run(fmt.Sprintf("%d files", n), func(t *testing.T) {
			raw, paths := manySourceDiffs(n)
			// Force many shards deterministically: each file's own segment
			// is a few hundred bytes, so a tiny budget guarantees far more
			// than one shard once n is large.
			res := PrioritizeShards(raw, 300)

			got, duplicates := shardFileSet(res)
			if duplicates != 0 {
				t.Errorf("%d files appeared in more than one shard", duplicates)
			}
			if !equalStringSets(got, paths) {
				missing, extra := diffStringSets(paths, got)
				t.Errorf("shard file union does not equal high-signal file set: missing=%v extra=%v", missing, extra)
			}
			if res.HighSignalFiles != n {
				t.Errorf("HighSignalFiles = %d, want %d", res.HighSignalFiles, n)
			}
			if n > 100 && len(res.Shards) < 10 {
				t.Errorf("len(Shards) = %d, want a large PR to actually fan out (>= 10) rather than being capped", len(res.Shards))
			}
		})
	}
}

func TestEffectiveShardBudget(t *testing.T) {
	tests := []struct {
		name            string
		highSignalBytes int
		wantShards      int
		want            int
	}{
		{"zero shards requested", 1000, 0, 0},
		{"zero bytes", 0, 4, 0},
		{"even split", 1000, 4, 250},
		{"rounds up", 1001, 4, 251},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectiveShardBudget(tc.highSignalBytes, tc.wantShards); got != tc.want {
				t.Errorf("EffectiveShardBudget(%d, %d) = %d, want %d", tc.highSignalBytes, tc.wantShards, got, tc.want)
			}
		})
	}
}

func mustJoin(parts ...string) string {
	return strings.Join(parts, "")
}

func equalStringSets(a, b []string) bool {
	missing, extra := diffStringSets(a, b)
	return len(missing) == 0 && len(extra) == 0
}

// diffStringSets treats a as "expected" and b as "got": missing is present
// in a but not b, extra is present in b but not a. Both inputs may contain
// duplicates; the comparison is set-based (membership only), matching how
// callers use it to check file-set coverage.
func diffStringSets(expected, got []string) (missing, extra []string) {
	expectedSet := make(map[string]bool, len(expected))
	for _, v := range expected {
		expectedSet[v] = true
	}
	gotSet := make(map[string]bool, len(got))
	for _, v := range got {
		gotSet[v] = true
	}
	for v := range expectedSet {
		if !gotSet[v] {
			missing = append(missing, v)
		}
	}
	for v := range gotSet {
		if !expectedSet[v] {
			extra = append(extra, v)
		}
	}
	return missing, extra
}
