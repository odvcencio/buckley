package oneshot

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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
	if !strings.Contains(diff, "func RealChange() int { return 7 }") {
		t.Error("source hunk after the oversized file was lost")
	}
}

func TestGatherGitDiff_WorkDirAndGeneratedAttrs(t *testing.T) {
	root := t.TempDir()
	gitIn(t, root, "init", "-q")
	for name, body := range map[string]string{".gitattributes": "custom.txt linguist-generated\n", "custom.txt": "generated payload\n", "source.go": "package p\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, root, "add", ".")
	t.Chdir(t.TempDir())
	context, stats, err := gatherGitDiffStats(map[string]string{"staged": "true"}, ContextOpts{WorkDir: root, MaxDiffBytes: 80000})
	if err != nil || len(stats.GeneratedPaths) != 1 || stats.GeneratedPaths[0] != "custom.txt" || strings.Contains(context, "+generated payload") || !strings.Contains(context, "+package p") {
		t.Fatalf("workdir/attributes lost: %s %+v %v", context, stats, err)
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

// gitOut runs a git command inside dir and returns its trimmed output.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// headRepo builds main plus a feature branch two commits ahead, then leaves
// the worktree on a third branch ("other", at main) with uncommitted edits, as
// when another agent holds the checkout.
func headRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	writeRepoFile(t, dir, "base.txt", "base\n")
	writeRepoFile(t, dir, "AGENTS.md", "main rules\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "base")

	gitIn(t, dir, "checkout", "-q", "-b", "feature")
	writeRepoFile(t, dir, "f1.txt", "feature file\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "feature: add f1\n\nVerified with go test ./pkg/f1/...")
	writeRepoFile(t, dir, "base.txt", "base\nfeature line\n")
	writeRepoFile(t, dir, "AGENTS.md", "feature rules\n")
	writeRepoFile(t, dir, ".gitattributes", "gen.js linguist-generated\n")
	writeRepoFile(t, dir, "gen.js", "zorblax(1)\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "feature: touch base")

	gitIn(t, dir, "checkout", "-q", "-b", "other", "main")
	writeRepoFile(t, dir, "base.txt", "other agent edit\n")
	writeRepoFile(t, dir, "AGENTS.md", "dirty rules\n")
	writeRepoFile(t, dir, "scratch.txt", "untracked scratch\n")
	return dir
}

func headSources(base, head string) []ContextSource {
	return []ContextSource{
		{Type: "git_diff", Params: map[string]string{"base": base, "head": head}},
		{Type: "git_log", Params: map[string]string{"base": base, "head": head, "include_body": "true"}},
		{Type: "git_files", Params: map[string]string{"base": base, "head": head}},
		{Type: "agents_md", Params: map[string]string{"head": head}},
	}
}

func TestBuildContext_HeadIgnoresCheckedOutBranchAndWorktree(t *testing.T) {
	dir := headRepo(t)
	statusBefore := gitOut(t, dir, "status", "--porcelain")
	t.Chdir(dir)

	ctx, err := BuildContext(headSources("main", "refs/heads/feature"), ContextOpts{MaxDiffBytes: 80_000})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}

	diff := ctx.Sources["git_diff:main"]
	for _, want := range []string{"+feature file", "+feature line"} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff is missing %q:\n%s", want, diff)
		}
	}
	for _, leaked := range []string{"other agent edit", "untracked scratch", "dirty rules"} {
		if strings.Contains(diff, leaked) {
			t.Errorf("diff leaked working tree content %q:\n%s", leaked, diff)
		}
	}
	log := ctx.Sources["git_log:main"]
	for _, want := range []string{"feature: add f1", "feature: touch base", "Verified with go test ./pkg/f1/..."} {
		if !strings.Contains(log, want) {
			t.Errorf("log is missing %q:\n%s", want, log)
		}
	}
	files := ctx.Sources["git_files:main"]
	for _, want := range []string{"A\tf1.txt", "M\tbase.txt", "A\tgen.js"} {
		if !strings.Contains(files, want) {
			t.Errorf("file list is missing %q:\n%s", want, files)
		}
	}
	if strings.Contains(files, "scratch.txt") {
		t.Errorf("file list leaked an untracked file:\n%s", files)
	}
	if got := ctx.Sources["agents_md"]; got != "feature rules\n" {
		t.Errorf("AGENTS.md = %q, want the committed text on feature, not the dirty working tree", got)
	}
	if !slices.Contains(ctx.Diff.GeneratedPaths, "gen.js") {
		t.Errorf("generated paths = %v: gitattributes must come from the head's tree", ctx.Diff.GeneratedPaths)
	}

	if got := gitOut(t, dir, "branch", "--show-current"); got != "other" {
		t.Errorf("checked-out branch = %q, want other: gathering must not switch branches", got)
	}
	if got := gitOut(t, dir, "status", "--porcelain"); got != statusBefore {
		t.Errorf("worktree status changed:\nbefore %q\nafter  %q", statusBefore, got)
	}
}

func TestBuildContext_NoHeadParamKeepsHEAD(t *testing.T) {
	dir := headRepo(t)
	gitIn(t, dir, "stash", "push", "-q", "--include-untracked")
	t.Chdir(dir)

	ctx, err := BuildContext([]ContextSource{
		{Type: "git_diff", Params: map[string]string{"base": "main"}},
		{Type: "git_log", Params: map[string]string{"base": "main"}},
		{Type: "git_files", Params: map[string]string{"base": "main"}},
	}, ContextOpts{MaxDiffBytes: 80_000})
	if err != nil {
		t.Fatalf("BuildContext: %v", err)
	}
	for _, label := range []string{"git_diff:main", "git_log:main", "git_files:main"} {
		if got := ctx.Sources[label]; got != "" {
			t.Errorf("%s = %q, want empty: HEAD is other, which has no commits past main", label, got)
		}
	}
}

func TestGatherGitDiff_FallbackNeverReadsWorkingTree(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	writeRepoFile(t, dir, "shared.txt", "same\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	gitIn(t, dir, "checkout", "-q", "-b", "feature")
	writeRepoFile(t, dir, "f1.txt", "feature only\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "feature")

	// unrel shares no history with feature but holds the same shared.txt.
	gitIn(t, dir, "checkout", "-q", "--orphan", "unrel")
	gitIn(t, dir, "rm", "-rf", "-q", ".")
	writeRepoFile(t, dir, "shared.txt", "same\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "unrelated")

	gitIn(t, dir, "checkout", "-q", "main")
	writeRepoFile(t, dir, "shared.txt", "another agent's uncommitted edit\n")
	t.Chdir(dir)

	params := map[string]string{"base": "unrel", "head": "refs/heads/feature"}
	diff, _, err := gatherGitDiffStats(params, ContextOpts{MaxDiffBytes: 80_000})
	if err != nil {
		t.Fatalf("gatherGitDiffStats: %v", err)
	}
	if !strings.Contains(diff, "f1.txt") {
		t.Errorf("fallback diff is missing the commit-to-commit change:\n%s", diff)
	}
	if strings.Contains(diff, "shared.txt") || strings.Contains(diff, "uncommitted edit") {
		t.Errorf("fallback diff read the working tree:\n%s", diff)
	}

	files, err := gatherGitFiles(params)
	if err != nil {
		t.Fatalf("gatherGitFiles: %v", err)
	}
	if files != "A\tf1.txt" {
		t.Errorf("fallback file list = %q, want only A\\tf1.txt", files)
	}
}

func TestGatherAgentsMD_ReadsHeadRef(t *testing.T) {
	symlinks := runtime.GOOS != "windows"
	cases := []struct {
		name    string
		setup   func(t *testing.T, dir string)
		cwd     string
		want    string
		wantErr bool
		head    string
	}{
		{
			name: "the committed file, not the dirty working tree",
			setup: func(t *testing.T, dir string) {
				writeRepoFile(t, dir, "AGENTS.md", "committed rules\n")
			},
			want: "committed rules\n",
		},
		{
			name:  "missing at the ref is empty, not an error",
			setup: func(t *testing.T, dir string) {},
			want:  "",
		},
		{
			name: "read from a subdirectory of the work tree",
			setup: func(t *testing.T, dir string) {
				writeRepoFile(t, dir, "AGENTS.md", "root rules\n")
				writeRepoFile(t, dir, "pkg/AGENTS.md", "package rules\n")
			},
			cwd:  "pkg",
			want: "root rules\n",
		},
		{
			name: "bounded to 10 KB",
			setup: func(t *testing.T, dir string) {
				writeRepoFile(t, dir, "AGENTS.md", strings.Repeat("x", agentsMDMaxBytes*3))
			},
			want: strings.Repeat("x", agentsMDMaxBytes),
		},
		{
			name: "a directory named AGENTS.md",
			setup: func(t *testing.T, dir string) {
				writeRepoFile(t, dir, "AGENTS.md/inner.txt", "not a file\n")
			},
			want: "",
		},
		{
			name:    "an option-like ref",
			setup:   func(t *testing.T, dir string) {},
			head:    "--output=pwned",
			wantErr: true,
		},
	}
	if symlinks {
		cases = append(cases, []struct {
			name    string
			setup   func(t *testing.T, dir string)
			cwd     string
			want    string
			wantErr bool
			head    string
		}{
			{
				name: "a symlink is followed one hop inside the repository",
				setup: func(t *testing.T, dir string) {
					writeRepoFile(t, dir, "CLAUDE.md", "linked rules\n")
					if err := os.Symlink("CLAUDE.md", filepath.Join(dir, "AGENTS.md")); err != nil {
						t.Fatal(err)
					}
				},
				want: "linked rules\n",
			},
			{
				name: "a symlink out of the repository is ignored",
				setup: func(t *testing.T, dir string) {
					if err := os.Symlink("../outside.md", filepath.Join(dir, "AGENTS.md")); err != nil {
						t.Fatal(err)
					}
				},
				want: "",
			},
			{
				name: "a chain of symlinks stops at the first hop",
				setup: func(t *testing.T, dir string) {
					if err := os.Symlink("MID.md", filepath.Join(dir, "AGENTS.md")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink("END.md", filepath.Join(dir, "MID.md")); err != nil {
						t.Fatal(err)
					}
					writeRepoFile(t, dir, "END.md", "end rules\n")
				},
				want: "",
			},
		}...)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			gitIn(t, dir, "init", "-q", "-b", "main")
			writeRepoFile(t, dir, "seed.txt", "seed\n")
			writeRepoFile(t, dir, "pkg/keep.txt", "keeps pkg on main\n")
			gitIn(t, dir, "add", ".")
			gitIn(t, dir, "commit", "-q", "-m", "seed")
			gitIn(t, dir, "checkout", "-q", "-b", "feature")
			tc.setup(t, dir)
			gitIn(t, dir, "add", "-A")
			gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "feature")
			gitIn(t, dir, "checkout", "-q", "main")
			writeRepoFile(t, dir, "AGENTS.md", "dirty working tree rules\n")
			t.Chdir(filepath.Join(dir, tc.cwd))

			head := tc.head
			if head == "" {
				head = "refs/heads/feature"
			}
			got, err := gatherAgentsMD(map[string]string{"head": head}, ContextOpts{})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("gatherAgentsMD() = %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("gatherAgentsMD: %v", err)
			}
			if got != tc.want {
				t.Fatalf("gatherAgentsMD() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGatherGit_RejectsOptionLikeRefs(t *testing.T) {
	dir := headRepo(t)
	t.Chdir(dir)

	for _, params := range []map[string]string{
		{"base": "--output=pwned"},
		{"base": "main", "head": "--output=pwned"},
	} {
		for name, gather := range map[string]func(map[string]string) error{
			"git_diff": func(p map[string]string) error { _, _, err := gatherGitDiffStats(p, ContextOpts{}); return err },
			"git_log":  func(p map[string]string) error { _, err := gatherGitLog(p); return err },
			"git_log body": func(p map[string]string) error {
				p = map[string]string{"base": p["base"], "head": p["head"], "include_body": "true"}
				_, err := gatherGitLog(p)
				return err
			},
			"git_files": func(p map[string]string) error { _, err := gatherGitFiles(p); return err },
		} {
			if err := gather(params); err == nil || !strings.Contains(err.Error(), "looks like an option") {
				t.Errorf("%s with %v: error = %v, want an option-like ref rejection", name, params, err)
			}
		}
	}
	entries, err := filepath.Glob(filepath.Join(dir, "pwned*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("an option reached git and wrote %v (%v)", entries, err)
	}
}
