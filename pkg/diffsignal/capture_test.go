package diffsignal

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func capturedDiff(t *testing.T, raw string, chunk int) *Diff {
	t.Helper()
	var capture Capture
	for len(raw) > 0 {
		n := min(chunk, len(raw))
		if _, err := capture.Write([]byte(raw[:n])); err != nil {
			t.Fatal(err)
		}
		raw = raw[n:]
	}
	diff, err := capture.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return diff
}

func TestCapture_MatchesSmallDiffContracts(t *testing.T) {
	for _, raw := range []string{
		"",
		"plain preamble\n",
		"diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n",
		"diff --git a/a b/b\nsimilarity index 100%\nrename from a\nrename to b\n",
		"diff --git \"a/old\\tname\" \"b/new\\nname\"\n--- \"a/old\\tname\"\n+++ \"b/new\\nname\"\n@@ -1 +1 @@\n-old\n+new",
		"diff --git a/photo.png b/photo.png\nBinary files a/photo.png and b/photo.png differ\n",
		"diff --git a/a b/a\nGIT binary patch\nliteral 4\nxxxx\n",
		"diff --git a/a b/a\n" + UnavailableMarker + "\n",
		"diff --git a/a.min.js b/a.min.js\n@@ -1 +1 @@\n-old\n+new\n",
		"diff --git a/a.js b/a.js\n@@ -1 +1 @@\n+" + strings.Repeat("x", MaxSignalLineLen) + "\n",
	} {
		for _, chunk := range []int{1, 7, 32768} {
			diff := capturedDiff(t, raw, chunk)
			want := Split(raw)
			if len(diff.Files) != len(want) {
				t.Fatalf("file count: %d != %d", len(diff.Files), len(want))
			}
			for i, file := range diff.Files {
				expected := want[i]
				if file.LowSignal() {
					expected.Segment = ""
				}
				if !reflect.DeepEqual(file, expected) {
					t.Fatalf("chunk %d: got %+v want %+v", chunk, file, expected)
				}
			}
			for _, opts := range []Options{{}, {ForPR: true}, {Generated: func(string) bool { return true }}} {
				got, expected := diff.Prioritize(80000, opts), PrioritizeWith(raw, 80000, opts)
				if got != expected {
					t.Fatalf("chunk %d: got %+v want %+v", chunk, got, expected)
				}
			}
		}
	}
}

func TestCapture_LateSourceAndFullBodyClassification(t *testing.T) {
	var capture Capture
	write := func(s string) {
		t.Helper()
		if _, err := io.WriteString(&capture, s); err != nil {
			t.Fatal(err)
		}
	}
	write("diff --git a/a.js b/a.js\n--- a/a.js\n+++ b/a.js\n@@ -1 +1 @@\n+")
	chunk := strings.Repeat("x", 32768)
	for i := 0; i < 1024; i++ {
		write(chunk)
	}
	write("\ndiff --git a/z.go b/z.go\n--- a/z.go\n+++ b/z.go\n@@ -1 +1 @@\n-old\n+meaningful\n")
	diff, err := capture.Finish()
	if err != nil {
		t.Fatal(err)
	}
	result := diff.Prioritize(80000, Options{})
	if len(diff.Files) != 2 || diff.Files[0].Reason != ReasonMinified || diff.Files[0].Insertions != 1 || diff.Files[1].Insertions != 1 || !strings.Contains(result.Context, "+meaningful") || result.Truncated {
		t.Fatalf("lost source/classification: %+v %+v", diff.Files, result)
	}
	t.Logf("streamed_body_bytes=%d retained_bytes=%d source_hunk=true generated_summary=true truncated=%t", 32<<20, capture.retained, result.Truncated)
}

func TestCapture_ClassificationUsesWholeBody(t *testing.T) {
	// The retained prefix looks minified by average line length, but the
	// complete diff's average is short. A late long line has the converse effect.
	for _, tc := range []struct {
		name   string
		suffix string
		reason Reason
	}{
		{"short_tail", strings.Repeat("+x\n", 50000), ReasonNone},
		{"long_tail", "+" + strings.Repeat("x", MaxSignalLineLen) + "\n", ReasonMinified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := "diff --git a/a.go b/a.go\n@@ -0,0 +1,1 @@\n" + strings.Repeat("+"+strings.Repeat("x", 600)+"\n", 150) + tc.suffix
			diff := capturedDiff(t, raw, 13)
			if diff.Files[0].Reason != tc.reason || diff.Files[0].Insertions != Split(raw)[0].Insertions {
				t.Fatalf("full classification/counts: %+v", diff.Files[0])
			}
		})
	}
}

func TestCapture_RawAndMetadataBudgets(t *testing.T) {
	t.Run("retained_prefixes", func(t *testing.T) {
		var capture Capture
		body := strings.Repeat("+short source line\n", 5000)
		for i := 0; i < 150; i++ {
			if _, err := fmt.Fprintf(&capture, "diff --git a/f%d.go b/f%d.go\n@@ -0,0 +1,1 @@\n%s", i, i, body); err != nil {
				t.Fatal(err)
			}
		}
		diff, err := capture.Finish()
		if err != nil {
			t.Fatal(err)
		}
		retained := 0
		for _, file := range diff.Files {
			retained += len(file.Segment)
			if len(file.Segment) > MaxFileDiffBytes {
				t.Fatal("per-file budget exceeded")
			}
		}
		out := diff.Prioritize(80000, Options{})
		if retained > MaxParseBytes || len(diff.Files) != 150 || !out.Truncated || !strings.Contains(out.Context, "f149.go") {
			t.Fatalf("budget/inventory: retained=%d files=%d out=%+v", retained, len(diff.Files), out)
		}
		t.Logf("retained_bytes=%d files=%d context_bytes=%d truncated=true", retained, len(diff.Files), len(out.Context))
	})
	for _, tc := range []struct{ name, raw string }{
		{"files", strings.Repeat("diff --git a/a b/a\n", MaxCaptureFiles+1)},
		{"line", "diff --git a/" + strings.Repeat("x", maxCaptureLine) + " b/b\n"},
		{"metadata", strings.Repeat("diff --git a/"+strings.Repeat("x", 1024)+" b/b\n", 2048)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c Capture
			_, writeErr := io.Copy(&c, strings.NewReader(tc.raw))
			diff, err := c.Finish()
			if err == nil || diff != nil || writeErr == nil || !strings.Contains(err.Error(), "narrow") {
				t.Fatalf("partial inventory accepted: %v %v", diff, err)
			}
		})
	}
}

func TestCapture_RenderDoesNotMutateInventory(t *testing.T) {
	diff := capturedDiff(t, "diff --git a/a b/a\n@@ -1 +1 @@\n+new\n", 7)
	diff.Prioritize(1, Options{})
	diff.Prioritize(1000, Options{Generated: func(string) bool { return true }})
	if diff.Files[0].Reason != ReasonNone || !strings.Contains(diff.Prioritize(1000, Options{}).Context, "+new") {
		t.Fatal("render mutated inventory")
	}
}

func TestPrioritize_SummaryAndPreambleStayWithinBudget(t *testing.T) {
	var raw strings.Builder
	for i := 0; i < 400; i++ {
		// Binary files stay one summary line each; generated files would collapse.
		fmt.Fprintf(&raw, "diff --git a/f%d.png b/f%d.png\nBinary files /dev/null and b/f%d.png differ\n", i, i, i)
	}
	for _, prefix := range []string{"", strings.Repeat("preamble\n", 100)} {
		for _, budget := range []int{1, 16, 50, 100, 4096} {
			for _, result := range []Result{Prioritize(prefix+raw.String(), budget), capturedDiff(t, prefix+raw.String(), 7).Prioritize(budget, Options{})} {
				if len(result.Context) > budget || !result.Truncated {
					t.Fatalf("budget=%d bytes=%d truncated=%t", budget, len(result.Context), result.Truncated)
				}
			}
		}
	}
}
