package diffsignal

import (
	"fmt"
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
