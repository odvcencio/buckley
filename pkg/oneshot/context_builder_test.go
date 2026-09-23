package oneshot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/diffsignal"
)

// gitIn runs a git command inside dir, failing the test on error.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestBuildContextStagedDiffPrioritized reproduces the gosx hallucination
// bug: a huge minified bundle staged alphabetically before a small source
// change must not starve the source change out of the model context.
func TestBuildContextStagedDiffPrioritized(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")

	// Alphabetically-early minified bundle: one 50KB line.
	bundle := "(()=>{" + strings.Repeat("var a=1;", 6_250) + "})();"
	if err := os.WriteFile(filepath.Join(dir, "a_bundle.js"), []byte(bundle), 0o644); err != nil {
		t.Fatal(err)
	}
	// The real hand-written change, alphabetically last.
	source := "package zz\n\n// RealChange is the actual hand-written edit.\nfunc RealChange() int { return 42 }\n"
	if err := os.WriteFile(filepath.Join(dir, "zz_source.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")

	t.Chdir(dir)

	ctx, err := BuildContext([]ContextSource{
		{Type: "git_diff", Params: map[string]string{"staged": "true"}},
	}, ContextOpts{MaxDiffBytes: 80_000})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}

	diff := ctx.Sources["git_diff:staged"]
	if diff == "" {
		t.Fatalf("no git_diff:staged source gathered; sources: %v", ctx.Sources)
	}
	if !strings.Contains(diff, "RealChange is the actual hand-written edit") {
		t.Errorf("hand-written source change missing from model context:\n%.1500s", diff)
	}
	if strings.Contains(diff, "var a=1;var a=1;") {
		t.Errorf("minified payload leaked into model context")
	}
	if !strings.Contains(diff, "a_bundle.js") {
		t.Errorf("minified file must still be visible as a summary line:\n%.1500s", diff)
	}
	if len(diff) > 80_000 {
		t.Errorf("context length %d exceeds MaxDiffBytes budget", len(diff))
	}
}

// TestBuildContextBudgetIncludesTruncationMarker checks that the truncation
// marker appended when output is cut does not push the result over MaxDiffBytes
// (off-by-18 regression: "\n... (truncated)" is 18 bytes).
func TestBuildContextBudgetIncludesTruncationMarker(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")

	// Create a file that is exactly at the budget edge.
	// We write enough content that Prioritize will both truncate and append the
	// marker, and the combined result could exceed MaxDiffBytes.
	const budget = 5_000
	// Use a source file large enough to trigger truncation after diffsignal.
	content := strings.Repeat("// line\n", budget)
	if err := os.WriteFile(filepath.Join(dir, "big.go"), []byte("package p\n"+content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	t.Chdir(dir)

	ctx, err := BuildContext([]ContextSource{
		{Type: "git_diff", Params: map[string]string{"staged": "true"}},
	}, ContextOpts{MaxDiffBytes: budget})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}

	diff := ctx.Sources["git_diff:staged"]
	if len(diff) > budget {
		t.Errorf("context length %d exceeds MaxDiffBytes %d (truncation marker not accounted for)", len(diff), budget)
	}
}

// TestBuildContextGitDiffBeyondMaxParseBytesStillSummarizesTailFile
// reproduces Important-2: gatherGitDiff hard-truncates the RAW git output at
// diffsignal.MaxParseBytes before diffsignal.Prioritize ever sees it, so
// Prioritize's own scanBoundariesBeyond (designed to stub-summarize files
// past that cutoff) never fires — a file whose diff starts after the 8MB
// mark vanishes from the model's context with no summary line at all,
// instead of the "[over budget]" stub Prioritize produces when it is given
// the full raw diff itself.
func TestBuildContextGitDiffBeyondMaxParseBytesStillSummarizesTailFile(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")

	// Alphabetically-first file whose diff alone exceeds MaxParseBytes.
	bigDir := filepath.Join(dir, "pkg", "big")
	if err := os.MkdirAll(bigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bigPath := filepath.Join(bigDir, "generated_file.go")
	if err := os.WriteFile(bigPath, []byte("package big\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "seed big file")

	var big strings.Builder
	big.WriteString("package big\n")
	for big.Len() < diffsignal.MaxParseBytes+64_000 {
		big.WriteString("// padding line to grow the diff past MaxParseBytes\n")
	}
	if err := os.WriteFile(bigPath, []byte(big.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	// Alphabetically-last file: a small, distinct hand-written change whose
	// diff segment starts well past the MaxParseBytes cutoff.
	smallDir := filepath.Join(dir, "pkg", "small")
	if err := os.MkdirAll(smallDir, 0o755); err != nil {
		t.Fatal(err)
	}
	smallPath := filepath.Join(smallDir, "real_change.go")
	source := "package small\n\n// RealChange is the actual hand-written edit past the cutoff.\nfunc RealChange() int { return 7 }\n"
	if err := os.WriteFile(smallPath, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	gitIn(t, dir, "add", ".")
	t.Chdir(dir)

	ctx, err := BuildContext([]ContextSource{
		{Type: "git_diff", Params: map[string]string{"staged": "true"}},
	}, ContextOpts{MaxDiffBytes: 10_000_000})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}

	diff := ctx.Sources["git_diff:staged"]
	if diff == "" {
		t.Fatalf("no git_diff:staged source gathered; sources: %v", ctx.Sources)
	}
	if !strings.Contains(diff, "pkg/small/real_change.go") {
		t.Errorf("file beyond MaxParseBytes vanished with no summary line at all:\n...tail:\n%.2000s",
			diff[max(0, len(diff)-2000):])
	}
}

// TestGatherGitDiffIgnoresConfiguredExternalDiffTool guards against a
// repository-local diff.external config hijacking the diff content the
// model receives: without --no-ext-diff, git runs the configured command
// in place of its own diff machinery, and whatever that command prints
// (never a parseable unified diff) becomes the "diff" diffsignal parses and
// the model reads.
func TestGatherGitDiffIgnoresConfiguredExternalDiffTool(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "config", "diff.external", "printf HIJACKED")
	t.Chdir(dir)

	ctx, err := BuildContext([]ContextSource{
		{Type: "git_diff", Params: map[string]string{"staged": "true"}},
	}, ContextOpts{MaxDiffBytes: 80_000})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}

	diff := ctx.Sources["git_diff:staged"]
	if strings.Contains(diff, "HIJACKED") {
		t.Fatalf("gathered diff was produced by the repo-configured external diff tool instead of git's own diff machinery:\n%s", diff)
	}
	if !strings.Contains(diff, "diff --git a/a.txt b/a.txt") {
		t.Errorf("gathered diff is not a parseable unified diff:\n%s", diff)
	}
}

// TestGatherGitDiffStripsColorEvenWhenConfigured guards against a
// repository-local color.ui=always config injecting ANSI escape sequences
// into the diff text diffsignal parses (which matches on literal
// "diff --git " boundaries and would silently misclassify or mangle
// colorized lines).
func TestGatherGitDiffStripsColorEvenWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "config", "color.ui", "always")
	t.Chdir(dir)

	ctx, err := BuildContext([]ContextSource{
		{Type: "git_diff", Params: map[string]string{"staged": "true"}},
		{Type: "git_files", Params: map[string]string{"staged": "true"}},
	}, ContextOpts{MaxDiffBytes: 80_000})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}

	for _, label := range []string{"git_diff:staged", "git_files:staged"} {
		if got := ctx.Sources[label]; strings.Contains(got, "\x1b[") {
			t.Errorf("%s contains ANSI escape sequences despite repo color.ui=always config:\n%q", label, got)
		}
	}
}

// TestBuildContextGitLogIncludesBodyWhenRequested reproduces Important-4:
// plain `git log --oneline` never reaches commit bodies, so verification
// evidence a contributor recorded in a commit body (test commands run,
// manual repro steps, and so on) never reaches the model synthesizing a PR.
// git_log with include_body=true must surface bounded body text per commit.
func TestBuildContextGitLogIncludesBodyWhenRequested(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	gitIn(t, dir, "checkout", "-q", "-b", "base")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "seed")
	gitIn(t, dir, "checkout", "-q", "-b", "feature")

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "fix: repro the hang\n\nVerified with: go test ./pkg/thing/... -run TestHang -race")

	t.Chdir(dir)

	withBody, err := BuildContext([]ContextSource{
		{Type: "git_log", Params: map[string]string{"base": "base", "include_body": "true"}},
	}, ContextOpts{})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	logWithBody := withBody.Sources["git_log:base"]
	if !strings.Contains(logWithBody, "Verified with: go test ./pkg/thing/... -run TestHang -race") {
		t.Errorf("git_log with include_body=true is missing the commit body (verification evidence):\n%s", logWithBody)
	}
	if !strings.Contains(logWithBody, "fix: repro the hang") {
		t.Errorf("git_log with include_body=true is missing the commit subject:\n%s", logWithBody)
	}

	withoutBody, err := BuildContext([]ContextSource{
		{Type: "git_log", Params: map[string]string{"base": "base"}},
	}, ContextOpts{})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	logWithoutBody := withoutBody.Sources["git_log:base"]
	if strings.Contains(logWithoutBody, "Verified with:") {
		t.Errorf("git_log without include_body must keep its existing --oneline behavior:\n%s", logWithoutBody)
	}
}

// TestRenderGitLogWithBodyBoundsPerCommitAndTotal exercises
// renderGitLogWithBody directly against the delimiter-joined raw format
// gatherGitLogWithBody produces, without needing a live git repo.
func TestRenderGitLogWithBodyBoundsPerCommitAndTotal(t *testing.T) {
	rec := func(hash, subject, body string) string {
		return hash + gitLogFieldSep + subject + gitLogFieldSep + body + gitLogRecordSep
	}

	t.Run("keeps hash, subject, and short body", func(t *testing.T) {
		raw := rec("abc123", "fix: thing", "Verified with go test ./...")
		got := renderGitLogWithBody(raw)
		for _, want := range []string{"abc123", "fix: thing", "Verified with go test ./..."} {
			if !strings.Contains(got, want) {
				t.Errorf("missing %q in:\n%s", want, got)
			}
		}
	})

	t.Run("truncates a long body to the per-commit line cap", func(t *testing.T) {
		var lines []string
		for i := 0; i < gitLogBodyMaxLinesPerCommit+5; i++ {
			lines = append(lines, fmt.Sprintf("line %d", i))
		}
		raw := rec("abc123", "add: thing", strings.Join(lines, "\n"))
		got := renderGitLogWithBody(raw)
		if strings.Contains(got, "line "+fmt.Sprint(gitLogBodyMaxLinesPerCommit+4)) {
			t.Errorf("body was not truncated to %d lines:\n%s", gitLogBodyMaxLinesPerCommit, got)
		}
		if !strings.Contains(got, "...") {
			t.Errorf("truncated body missing a truncation marker:\n%s", got)
		}
	})

	t.Run("stops accepting commits once the total byte budget is spent", func(t *testing.T) {
		var raw strings.Builder
		bigBody := strings.Repeat("x", gitLogBodyMaxBytes/2)
		raw.WriteString(rec("aaa111", "add: first", bigBody))
		raw.WriteString(rec("bbb222", "add: second", bigBody))
		raw.WriteString(rec("ccc333", "add: third", "small body"))

		got := renderGitLogWithBody(raw.String())
		if len(got) > gitLogBodyMaxBytes+200 { // small slack for the "N more" trailer
			t.Errorf("rendered log %d bytes exceeds the %d-byte budget by more than the trailer allows", len(got), gitLogBodyMaxBytes)
		}
		if !strings.Contains(got, "more commits omitted") {
			t.Errorf("budget-exceeding log missing an explicit omission notice:\n%.500s", got)
		}
	})
}
