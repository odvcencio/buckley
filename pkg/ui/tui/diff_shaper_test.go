package tui

import (
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/diffsignal"
)

// minifiedSegment returns a diff segment for a file with a very long single
// line so it is classified as minified content.
func minifiedSegment(path string, payloadLen int) string {
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

// sourceSegment returns a small hand-written source diff segment.
func sourceSegment(path, marker string) string {
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

// TestShapeDiff_LowSignalSummarized checks that minified noise is replaced by a
// summary line and the hand-written source change is preserved.
func TestShapeDiff_LowSignalSummarized(t *testing.T) {
	raw := minifiedSegment("client/js/bundle.js", 30_000) +
		sourceSegment("pkg/handler.go", "real change sentinel")

	out := shapeDiff(strings.TrimSpace(raw), diffsignal.CommitDiffBudget)

	if !strings.Contains(out, "real change sentinel") {
		t.Errorf("source change missing from shaped output")
	}
	if !strings.Contains(out, "client/js/bundle.js") {
		t.Errorf("minified file path must appear as summary line")
	}
	if strings.Contains(out, "var a=1;var a=1;") {
		t.Errorf("minified payload leaked into shaped output")
	}
}

// TestShapeDiff_BudgetRespected checks that the shaped output never exceeds
// the budget (including the truncation marker).
func TestShapeDiff_BudgetRespected(t *testing.T) {
	const budget = 5_000
	var raw strings.Builder
	for i := 0; i < 10; i++ {
		raw.WriteString(sourceSegment(fmt.Sprintf("pkg/file%d.go", i), strings.Repeat("x", 2_000)))
	}
	out := shapeDiff(strings.TrimSpace(raw.String()), budget)
	if len(out) > budget {
		t.Errorf("shaped output len %d exceeds budget %d", len(out), budget)
	}
}

// TestShapeDiff_TruncationMarkerPresent checks that the truncation marker is
// appended when content is cut and the total stays within budget.
// Uses a file large enough to trigger the per-file cap (MaxFileDiffBytes) so
// Truncated=true without the file being classified as low-signal.
func TestShapeDiff_TruncationMarkerPresent(t *testing.T) {
	// Build a source diff that exceeds MaxFileDiffBytes so the per-file cap
	// fires and sets Truncated=true.
	const budget = diffsignal.MaxFileDiffBytes + 10_000
	var rawBuf strings.Builder
	// Header
	fmt.Fprintf(&rawBuf, "diff --git a/pkg/a.go b/pkg/a.go\n")
	fmt.Fprintf(&rawBuf, "index 3333333..4444444 100644\n")
	fmt.Fprintf(&rawBuf, "--- a/pkg/a.go\n")
	fmt.Fprintf(&rawBuf, "+++ b/pkg/a.go\n")
	fmt.Fprintf(&rawBuf, "@@ -1,0 +1,999 @@\n")
	// Fill beyond MaxFileDiffBytes with short distinct lines (not minified)
	for rawBuf.Len() < diffsignal.MaxFileDiffBytes+1_000 {
		fmt.Fprintf(&rawBuf, "+func Helper%d() int { return %d }\n", rawBuf.Len(), rawBuf.Len())
	}

	out := shapeDiff(strings.TrimSpace(rawBuf.String()), budget)

	if !strings.Contains(out, "... (truncated)") {
		t.Errorf("truncation marker missing when per-file cap fires")
	}
	if len(out) > budget {
		t.Errorf("shaped output len %d exceeds budget %d after marker", len(out), budget)
	}
}

// TestShapeDiff_EmptyInput returns empty string for empty input.
func TestShapeDiff_EmptyInput(t *testing.T) {
	out := shapeDiff("", diffsignal.CommitDiffBudget)
	if out != "" {
		t.Errorf("got %q, want empty", out)
	}
}
