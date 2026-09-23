package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/diffsignal"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestParsePRCommandOptions(t *testing.T) {
	opts, err := parsePRCommandOptions([]string{
		"-dry-run",
		"-yes",
		"-push=false",
		"-verbose",
		"-cost=false",
		"-base=develop",
		"-model=openai/test-pr",
		"-backend=codex",
		"-timeout=15s",
		"-diff-budget=123456",
		"-no-update",
		"-draft",
	})
	if err != nil {
		t.Fatalf("parsePRCommandOptions: %v", err)
	}

	if !opts.dryRun {
		t.Fatal("dryRun = false, want true")
	}
	if !opts.yes {
		t.Fatal("yes = false, want true")
	}
	if opts.push {
		t.Fatal("push = true, want false")
	}
	if !opts.verbose {
		t.Fatal("verbose = false, want true")
	}
	if opts.showCost {
		t.Fatal("showCost = true, want false")
	}
	if opts.base != "develop" {
		t.Fatalf("base = %q, want develop", opts.base)
	}
	if opts.model != "openai/test-pr" {
		t.Fatalf("model = %q, want openai/test-pr", opts.model)
	}
	if opts.backend != oneshot.CLIBackendCodex {
		t.Fatalf("backend = %q, want codex", opts.backend)
	}
	if opts.timeout != 15*time.Second {
		t.Fatalf("timeout = %s, want 15s", opts.timeout)
	}
	if opts.diffBudget != 123456 {
		t.Fatalf("diffBudget = %d, want 123456", opts.diffBudget)
	}
	if !opts.noUpdate {
		t.Fatal("noUpdate = false, want true")
	}
	if !opts.draft {
		t.Fatal("draft = false, want true")
	}
}

func TestParsePRCommandOptionsCollectsRepeatedContextFiles(t *testing.T) {
	opts, err := parsePRCommandOptions([]string{
		"-context-file=/tmp/a.md",
		"-context-file=/tmp/b.md",
	})
	if err != nil {
		t.Fatalf("parsePRCommandOptions: %v", err)
	}
	want := []string{"/tmp/a.md", "/tmp/b.md"}
	if len(opts.contextFiles) != len(want) {
		t.Fatalf("contextFiles = %v, want %v", opts.contextFiles, want)
	}
	for i, w := range want {
		if opts.contextFiles[i] != w {
			t.Fatalf("contextFiles[%d] = %q, want %q", i, opts.contextFiles[i], w)
		}
	}
}

func TestParsePRCommandOptionsHonorsEnvironment(t *testing.T) {
	t.Setenv(envPRBackend, "claude")

	opts, err := parsePRCommandOptions(nil)
	if err != nil {
		t.Fatalf("parsePRCommandOptions: %v", err)
	}

	if opts.backend != oneshot.CLIBackendClaude {
		t.Fatalf("backend = %q, want claude", opts.backend)
	}
	if opts.push != true {
		t.Fatal("push = false, want true")
	}
	if opts.showCost != true {
		t.Fatal("showCost = false, want true")
	}
	if opts.timeout != 2*time.Minute {
		t.Fatalf("timeout = %s, want 2m", opts.timeout)
	}
	if opts.diffBudget != diffsignal.PRDiffBudget {
		t.Fatalf("diffBudget = %d, want default %d", opts.diffBudget, diffsignal.PRDiffBudget)
	}
	if opts.noUpdate {
		t.Fatal("noUpdate default = true, want false (preserves existing update-in-place behavior)")
	}
	if opts.draft {
		t.Fatal("draft default = true, want false")
	}
}

// TestLoadPRContextNotesConcatenatesLabeledFiles reproduces Important-5:
// `buckley pr` has no way to accept author-supplied steering notes (slice
// breakdown, verification evidence). loadPRContextNotes reads each
// --context-file path and concatenates them, labeled by path.
func TestLoadPRContextNotesConcatenatesLabeledFiles(t *testing.T) {
	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.md")
	pathB := filepath.Join(dir, "b.md")
	if err := os.WriteFile(pathA, []byte("Slice 1: extract the retry guard."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pathB, []byte("Verified with go test ./pkg/retry/..."), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := loadPRContextNotes([]string{pathA, pathB})
	if err != nil {
		t.Fatalf("loadPRContextNotes: %v", err)
	}
	for _, want := range []string{pathA, "Slice 1: extract the retry guard.", pathB, "Verified with go test ./pkg/retry/..."} {
		if !strings.Contains(got, want) {
			t.Fatalf("loadPRContextNotes output missing %q:\n%s", want, got)
		}
	}
}

// TestLoadPRContextNotesEmptyWhenNoFiles asserts the zero-flag case returns
// an empty string, so BuildPrompt's Author Notes section stays omitted.
func TestLoadPRContextNotesEmptyWhenNoFiles(t *testing.T) {
	got, err := loadPRContextNotes(nil)
	if err != nil {
		t.Fatalf("loadPRContextNotes: %v", err)
	}
	if got != "" {
		t.Fatalf("loadPRContextNotes(nil) = %q, want empty", got)
	}
}

// TestLoadPRContextNotesErrorsOnMissingFile guards against a typo'd
// --context-file path silently vanishing into an empty section: the author
// explicitly asked for this file's content to steer the PR.
func TestLoadPRContextNotesErrorsOnMissingFile(t *testing.T) {
	_, err := loadPRContextNotes([]string{"/no/such/file/here.md"})
	if err == nil {
		t.Fatal("loadPRContextNotes with a missing file: want error, got nil")
	}
}

// TestLoadPRContextNotesBoundsTotalSize guards against an oversized
// --context-file (or many of them) consuming an outsized share of the
// model's context; content is bounded, not silently unbounded.
func TestLoadPRContextNotesBoundsTotalSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.md")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", prContextNotesMaxBytes*2)), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := loadPRContextNotes([]string{path})
	if err != nil {
		t.Fatalf("loadPRContextNotes: %v", err)
	}
	if len(got) > prContextNotesMaxBytes+500 { // slack for path label + truncation marker
		t.Fatalf("loadPRContextNotes output %d bytes exceeds the %d-byte budget by more than the label/marker allow", len(got), prContextNotesMaxBytes)
	}
	if !strings.Contains(got, "truncated") {
		t.Fatalf("oversized context notes missing an explicit truncation marker:\n%.500s", got)
	}
}

// TestPRContextOptsUsesPRBudgetAndRanking guards Important-3: `buckley pr`
// must not silently inherit the 80KB commit default (RunOpts{} previously
// left ContextOpts zero-valued, and BuildContext's own fallback is the
// commit budget) or git's alphabetical file ordering.
func TestPRContextOptsUsesPRBudgetAndRanking(t *testing.T) {
	got := prContextOpts(0)
	if got.MaxDiffBytes != diffsignal.PRDiffBudget {
		t.Fatalf("prContextOpts(0).MaxDiffBytes = %d, want default %d", got.MaxDiffBytes, diffsignal.PRDiffBudget)
	}
	if !got.RankDiffForPR {
		t.Fatal("prContextOpts(0).RankDiffForPR = false, want true")
	}

	got = prContextOpts(500_000)
	if got.MaxDiffBytes != 500_000 {
		t.Fatalf("prContextOpts(500_000).MaxDiffBytes = %d, want 500000 (override honored)", got.MaxDiffBytes)
	}
	if !got.RankDiffForPR {
		t.Fatal("prContextOpts(500_000).RankDiffForPR = false, want true")
	}
}

func TestPRRunResultFromFramework(t *testing.T) {
	pr := &commands.PRResult{
		Title:   "tighten pr command",
		Summary: "Split command wiring into smaller helpers.",
		Changes: []string{
			"Parse flags separately",
		},
		Testing: []string{
			"go test ./cmd/buckley",
		},
	}
	trace := &transparency.Trace{Reasoning: "reasoning"}
	audit := transparency.NewContextAudit()

	result := prRunResultFromFramework(&oneshot.RunResult{
		Value:        pr,
		Trace:        trace,
		ContextAudit: audit,
	})

	if result.PR != pr {
		t.Fatal("PR result was not preserved")
	}
	if result.Trace != trace {
		t.Fatal("trace was not preserved")
	}
	if result.ContextAudit != audit {
		t.Fatal("context audit was not preserved")
	}
	if result.Error != nil {
		t.Fatalf("Error = %v, want nil", result.Error)
	}
}

func TestPRRunResultFromFrameworkRejectsUnexpectedValue(t *testing.T) {
	result := prRunResultFromFramework(&oneshot.RunResult{Value: "not a pr"})

	if result.PR != nil {
		t.Fatal("PR result should be nil for unexpected value")
	}
	if result.Error == nil {
		t.Fatal("expected unexpected result type error")
	}
	if !strings.Contains(result.Error.Error(), "unexpected result type") {
		t.Fatalf("error = %q, want unexpected result type", result.Error)
	}
}

// TestResolvePRUpdatePolicyRefusesOverwriteWithNoUpdate reproduces
// Important-6: `buckley pr` silently overwrote an existing open PR's
// title/body via `gh pr edit`. With --no-update set, an existing open PR
// must fail the run instead of being silently rewritten.
func TestResolvePRUpdatePolicyRefusesOverwriteWithNoUpdate(t *testing.T) {
	cases := []struct {
		name          string
		existingURL   string
		existingFound bool
		noUpdate      bool
		wantUpdate    bool
		wantErr       bool
	}{
		{"no existing PR: always create", "", false, false, false, false},
		{"no existing PR: --no-update is a no-op", "", false, true, false, false},
		{"existing PR, default: update in place", "https://github.com/o/r/pull/7", true, false, true, false},
		{"existing PR, --no-update: refuse", "https://github.com/o/r/pull/7", true, true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			update, err := resolvePRUpdatePolicy(tc.existingURL, tc.existingFound, tc.noUpdate)
			if update != tc.wantUpdate {
				t.Errorf("update = %v, want %v", update, tc.wantUpdate)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantErr && err != nil && !strings.Contains(err.Error(), tc.existingURL) {
				t.Errorf("error %q does not mention the existing PR URL %q", err, tc.existingURL)
			}
		})
	}
}

// TestBuildGhPRCreateArgsPassesThroughDraft reproduces Important-7:
// `buckley pr` had no way to create a draft PR; --draft must pass through
// to `gh pr create --draft`.
func TestBuildGhPRCreateArgsPassesThroughDraft(t *testing.T) {
	notDraft := buildGhPRCreateArgs("fix: thing", "body", "main", false)
	if containsArg(notDraft, "--draft") {
		t.Errorf("args without --draft requested: %v", notDraft)
	}

	draft := buildGhPRCreateArgs("fix: thing", "body", "main", true)
	if !containsArg(draft, "--draft") {
		t.Errorf("args with --draft requested missing --draft: %v", draft)
	}
	for _, want := range []string{"fix: thing", "body", "main"} {
		if !containsArg(draft, want) {
			t.Errorf("args missing %q: %v", want, draft)
		}
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestParseOpenPRView(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantURL string
		wantOK  bool
	}{
		{"open PR", `{"state":"OPEN","url":"https://github.com/o/r/pull/7"}`, "https://github.com/o/r/pull/7", true},
		{"merged PR", `{"state":"MERGED","url":"https://github.com/o/r/pull/7"}`, "", false},
		{"closed PR", `{"state":"CLOSED","url":"https://github.com/o/r/pull/7"}`, "", false},
		{"missing url", `{"state":"OPEN"}`, "", false},
		{"garbage", `no such pull request`, "", false},
	}
	for _, tc := range cases {
		url, ok := parseOpenPRView([]byte(tc.payload))
		if url != tc.wantURL || ok != tc.wantOK {
			t.Fatalf("%s: parseOpenPRView = (%q, %v), want (%q, %v)", tc.name, url, ok, tc.wantURL, tc.wantOK)
		}
	}
}
