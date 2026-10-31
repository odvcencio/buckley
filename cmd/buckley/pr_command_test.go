package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/diffsignal"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/tools"
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
	notDraft := buildGhPRCreateArgs("fix: thing", "body", "main", "", false)
	if containsArg(notDraft, "--draft") {
		t.Errorf("args without --draft requested: %v", notDraft)
	}

	draft := buildGhPRCreateArgs("fix: thing", "body", "main", "", true)
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

func TestParsePRCommandOptions_HeadAndDir(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantHead string
		wantDir  string
	}{
		{"defaults", nil, "", ""},
		{"head", []string{"--head", "feature"}, "feature", ""},
		{"head for a fork", []string{"-head=octo:feature"}, "octo:feature", ""},
		{"short dir", []string{"-C", "/repo"}, "", "/repo"},
		{"long dir", []string{"--dir", "/repo"}, "", "/repo"},
		{"both", []string{"--head", "feature", "--dir=/repo"}, "feature", "/repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts, err := parsePRCommandOptions(tc.args)
			if err != nil {
				t.Fatalf("parsePRCommandOptions: %v", err)
			}
			if opts.head != tc.wantHead || opts.dir != tc.wantDir {
				t.Fatalf("head, dir = %q, %q, want %q, %q", opts.head, opts.dir, tc.wantHead, tc.wantDir)
			}
		})
	}
}

// TestParsePRCommandOptions_RejectsPositionalArguments covers Go's flag
// package, which stops reading flags at the first argument that is not a flag.
// `buckley pr --yes feature -C /other` used to drop -C without a word and open
// a PR for the directory it was started in.
func TestParsePRCommandOptions_RejectsPositionalArguments(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"a branch name", []string{"feature"}, []string{`"feature"`, "--head"}},
		{"flags after it would be dropped", []string{"--yes", "feature", "-C", "/other"}, []string{`"feature"`, "--head", `"-C"`}},
		{"a word after the flags", []string{"--dry-run", "extra"}, []string{`"extra"`}},
		{"after the -- terminator", []string{"--", "feature"}, []string{`"feature"`}},
		{"the first of several", []string{"one", "two"}, []string{`"one"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePRCommandOptions(tc.args)
			if err == nil {
				t.Fatalf("parsePRCommandOptions(%q) = nil error, want a rejection", tc.args)
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %s", err, want)
				}
			}
		})
	}
}

func TestRunPRCommand_PositionalArgumentFailsBeforeAnythingRuns(t *testing.T) {
	isolateWorkDirGlobals(t)
	launch := t.TempDir()
	chdirTemp(t, launch)
	target := workDirRepo(t)
	stubPRDependencies(t, func() error {
		t.Fatal("dependencies were initialized: the command went on after an argument it could not read")
		return nil
	})

	err := runPRCommand([]string{"--yes", "--dry-run", "feature", "-C", target})
	if err == nil || !strings.Contains(err.Error(), `"feature"`) {
		t.Fatalf("runPRCommand() error = %v, want the stray argument named", err)
	}
	if cwd, _ := os.Getwd(); resolvedPath(t, cwd) != resolvedPath(t, launch) {
		t.Errorf("working directory = %s, want it unchanged at %s", cwd, launch)
	}
}

// TestRunPRCommand_SubcommandsStillDispatch keeps `pr merge` and `pr label`
// working: they take positional arguments and are chosen before pr's own flags
// are parsed.
func TestRunPRCommand_SubcommandsStillDispatch(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"label", []string{"label"}, "usage: buckley pr label"},
		{"merge", []string{"merge"}, "usage: buckley pr merge"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := runPRCommand(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("runPRCommand(%q) error = %v, want the %q usage", tc.args, err, tc.name)
			}
			if strings.Contains(err.Error(), "unexpected argument") {
				t.Fatalf("runPRCommand(%q) rejected the subcommand name as a stray argument: %v", tc.args, err)
			}
		})
	}
}

func TestBuildGhPRCreateArgs_Head(t *testing.T) {
	cases := []struct {
		name     string
		head     string
		wantHead string
	}{
		{"no --head leaves gh to infer the head", "", ""},
		{"branch", "feature", "feature"},
		{"fork", "octo:feature", "octo:feature"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := buildGhPRCreateArgs("fix: thing", "body", "main", tc.head, false)
			idx := -1
			for i, a := range args {
				if a == "--head" {
					idx = i
				}
			}
			if tc.wantHead == "" {
				if idx != -1 {
					t.Fatalf("args carry --head without one requested: %v", args)
				}
				return
			}
			if idx == -1 || idx+1 >= len(args) || args[idx+1] != tc.wantHead {
				t.Fatalf("args = %v, want --head %s", args, tc.wantHead)
			}
		})
	}
}

func prListJSON(entries ...string) []byte {
	return []byte("[" + strings.Join(entries, ",") + "]")
}

// prListEntry is one `gh pr list` row for PR number in o/r, whose head branch
// lives in a repository of headOwner. "o" is the base repository's own owner;
// any other owner is a fork.
func prListEntry(number int, head, base, headOwner string) string {
	return prListRow(number, fmt.Sprintf("https://github.com/o/r/pull/%d", number), head, base, headOwner)
}

func prListRow(number int, url, head, base, headOwner string) string {
	return fmt.Sprintf(`{"number":%d,"url":%q,"baseRefName":%q,"headRefName":%q,"headRepositoryOwner":{"id":"MDQ6VXNlcjE=","login":%q}}`,
		number, url, base, head, headOwner)
}

func TestOpenPRForHead_FiltersForksAndSurfacesGhErrors(t *testing.T) {
	cases := []struct {
		name     string
		head     string
		owner    string
		listed   []byte
		ghErr    error
		base     string
		wantURL  string
		wantBase string
		wantErr  string
		wantNoPR bool
	}{
		{
			name:     "no open PR",
			listed:   prListJSON(),
			wantNoPR: true,
		},
		{
			name:     "a fork's PR from a same-named branch is not ours",
			listed:   prListJSON(prListEntry(7, "feature", "main", "octo")),
			wantNoPR: true,
		},
		{
			name:     "a PR from another head is not ours",
			listed:   prListJSON(prListEntry(8, "feature-2", "main", "o")),
			wantNoPR: true,
		},
		{
			name:     "the base repository's PR is chosen over the fork's",
			listed:   prListJSON(prListEntry(7, "feature", "main", "octo"), prListEntry(9, "feature", "develop", "o")),
			wantURL:  "https://github.com/o/r/pull/9",
			wantBase: "develop",
		},
		{
			name:     "--head octo:patch-1 finds the fork's PR, not the base repository's own patch-1 PR",
			head:     "patch-1",
			owner:    "octo",
			listed:   prListJSON(prListEntry(3, "patch-1", "main", "o"), prListEntry(4, "patch-1", "main", "octo")),
			wantURL:  "https://github.com/o/r/pull/4",
			wantBase: "main",
		},
		{
			name:     "--head octo:patch-1 finds nothing when only the base repository has a patch-1 PR",
			head:     "patch-1",
			owner:    "octo",
			listed:   prListJSON(prListEntry(3, "patch-1", "main", "o")),
			wantNoPR: true,
		},
		{
			name:     "a fork checkout finds the caller's own PR in the parent's listing",
			owner:    "octo",
			listed:   prListJSON(prListEntry(9, "feature", "main", "parent-owner"), prListEntry(4, "feature", "main", "octo")),
			wantURL:  "https://github.com/o/r/pull/4",
			wantBase: "main",
		},
		{
			name:     "a fork checkout leaves the parent's same-named PR alone",
			owner:    "octo",
			listed:   prListJSON(prListEntry(9, "feature", "main", "parent-owner")),
			wantNoPR: true,
		},
		{
			name:     "owners compare without case",
			owner:    "Octo",
			listed:   prListJSON(prListEntry(4, "feature", "main", "octo")),
			wantURL:  "https://github.com/o/r/pull/4",
			wantBase: "main",
		},
		{
			name:     "a PR whose head repository was deleted is not ours",
			owner:    "octo",
			listed:   prListJSON(prListEntry(4, "feature", "main", "")),
			wantNoPR: true,
		},
		{
			name:    "a gh failure is an error, not no PR",
			ghErr:   errors.New("gh pr list: exit status 1: HTTP 502"),
			wantErr: "HTTP 502",
		},
		{
			name:    "an unreadable listing is an error",
			listed:  []byte("not json"),
			wantErr: "decode open PRs",
		},
		{
			name:    "an explicit base that differs is refused, not retargeted",
			listed:  prListJSON(prListEntry(9, "feature", "develop", "o")),
			base:    "main",
			wantErr: "refusing to retarget",
		},
		{
			name:     "an explicit base picks the PR with that base",
			listed:   prListJSON(prListEntry(9, "feature", "develop", "o"), prListEntry(10, "feature", "main", "o")),
			base:     "main",
			wantURL:  "https://github.com/o/r/pull/10",
			wantBase: "main",
		},
		{
			name:    "two open PRs without a base choice are ambiguous",
			listed:  prListJSON(prListEntry(9, "feature", "develop", "o"), prListEntry(10, "feature", "main", "o")),
			wantErr: "--base",
		},
		{
			name:    "a PR of ours without a URL is an error, because gh reads an empty URL as the current branch's PR",
			listed:  prListJSON(prListRow(5, "", "feature", "main", "o")),
			wantErr: "without a usable number and URL",
		},
		{
			name:    "a PR of ours with number 0 is an error",
			listed:  prListJSON(prListRow(0, "https://github.com/o/r/pull/5", "feature", "main", "o")),
			wantErr: "without a usable number and URL",
		},
		{
			name:    "a PR of ours whose URL is not a PR URL is an error",
			listed:  prListJSON(prListRow(5, "https://github.com/o/r/issues/5", "feature", "main", "o")),
			wantErr: "without a usable number and URL",
		},
		{
			name:     "a row for another PR without a URL does not matter",
			listed:   prListJSON(prListRow(6, "", "feature", "main", "octo"), prListEntry(10, "feature", "main", "o")),
			wantURL:  "https://github.com/o/r/pull/10",
			wantBase: "main",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			head := prHead{Name: "feature", Owner: "o"}
			if tc.head != "" {
				head.Name = tc.head
			}
			if tc.owner != "" {
				head.Owner = tc.owner
			}
			gh := &fakeGhRunner{
				responses: map[string][]byte{"pr list": tc.listed},
				errs:      map[string]error{},
			}
			if tc.ghErr != nil {
				gh.errs["pr list"] = tc.ghErr
			}

			got, err := openPRForHead(gh.run, head, tc.base)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("openPRForHead() error = %v, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("openPRForHead: %v", err)
			}
			if tc.wantNoPR {
				if got != nil {
					t.Fatalf("openPRForHead() = %+v, want none", got)
				}
				return
			}
			if got == nil || got.URL != tc.wantURL || got.Base != tc.wantBase {
				t.Fatalf("openPRForHead() = %+v, want %s into %s", got, tc.wantURL, tc.wantBase)
			}

			call := strings.Join(gh.calls[0], " ")
			for _, want := range []string{"pr list", "--head " + head.Name, "--state open", "headRepositoryOwner"} {
				if !strings.Contains(call, want) {
					t.Errorf("gh call %q missing %q", call, want)
				}
			}
		})
	}
}

func TestRunPRCommand_NonInteractiveRefusesBeforeModelCall(t *testing.T) {
	chdirTemp(t, t.TempDir())
	stubPRDependencies(t, func() error {
		t.Fatal("dependencies were initialized: the refusal must come before the model call")
		return nil
	})
	previousTTY := stdinIsTerminalFn
	t.Cleanup(func() { stdinIsTerminalFn = previousTTY })
	stdinIsTerminalFn = func() bool { return false }
	previousLookPath := lookPathFn
	t.Cleanup(func() { lookPathFn = previousLookPath })
	lookPathFn = func(string) (string, error) {
		t.Fatal("gh was looked up before the confirmation check")
		return "", nil
	}

	err := runPRCommand(nil)
	if !errors.Is(err, errPRNonInteractive) {
		t.Fatalf("runPRCommand() error = %v, want %v", err, errPRNonInteractive)
	}
}

func TestRunPRCommand_DetachedHeadFailsBeforeModelCall(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	runGitIn(t, r.work, "checkout", "-q", "--detach", "feature")
	stubPRDependencies(t, func() error {
		t.Fatal("dependencies were initialized for a detached HEAD")
		return nil
	})

	err := runPRCommand([]string{"--dry-run"})
	if err == nil || !strings.Contains(err.Error(), "HEAD is detached; pass --head <branch>") {
		t.Fatalf("runPRCommand() error = %v, want the detached-HEAD message", err)
	}
}

func TestRunPRCommand_DirEntersTargetBeforeModelInit(t *testing.T) {
	isolateWorkDirGlobals(t)
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	chdirTemp(t, t.TempDir())

	sentinel := errors.New("dependency init reached")
	var initDir string
	stubPRDependencies(t, func() error {
		initDir, _ = os.Getwd()
		return sentinel
	})

	err := runPRCommand([]string{"-C", r.work, "--head", "feature", "--dry-run"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("runPRCommand() error = %v, want the dependency-init sentinel", err)
	}
	if want, _ := filepath.EvalSymlinks(r.work); initDir != want {
		t.Fatalf("dependencies initialized in %s, want the -C target %s", initDir, want)
	}
}

// TestRunPRCommand_ContextFileResolvesFromLaunchDirectory records the choice
// for relative --context-file paths under -C: they resolve against the
// directory the command was launched in, because the caller typed them there.
func TestRunPRCommand_ContextFileResolvesFromLaunchDirectory(t *testing.T) {
	isolateWorkDirGlobals(t)
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	launch := t.TempDir()
	writeFile(t, launch, "notes.md", "written where the caller launched\n")
	writeFile(t, r.work, "inside-only.md", "exists only in the -C target\n")

	sentinel := errors.New("dependency init reached")
	stubPRDependencies(t, func() error { return sentinel })

	t.Run("a file next to the caller loads", func(t *testing.T) {
		chdirTemp(t, launch)
		err := runPRCommand([]string{"-C", r.work, "--head", "feature", "--dry-run", "--context-file", "notes.md"})
		if !errors.Is(err, sentinel) {
			t.Fatalf("runPRCommand() error = %v, want the notes to load and the run to reach dependency init", err)
		}
	})
	t.Run("a file only in the target is not found", func(t *testing.T) {
		chdirTemp(t, launch)
		err := runPRCommand([]string{"-C", r.work, "--head", "feature", "--dry-run", "--context-file", "inside-only.md"})
		if err == nil || !strings.Contains(err.Error(), filepath.Join(launch, "inside-only.md")) {
			t.Fatalf("runPRCommand() error = %v, want a read failure naming %s", err, filepath.Join(launch, "inside-only.md"))
		}
	})
}

// stubPRDependencies isolates runPRCommand from the real model and from
// environment-selected backends. initDeps runs in place of dependency setup.
func stubPRDependencies(t *testing.T, initDeps func() error) {
	t.Helper()
	t.Setenv(envPRBackend, "")
	t.Setenv(envOneshotBackend, "")
	previous := initDependenciesFn
	t.Cleanup(func() { initDependenciesFn = previous })
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		return nil, nil, nil, initDeps()
	}
}

// stubGh makes gh look installed and the terminal non-interactive, which is
// how an agent runs buckley.
func stubGh(t *testing.T) {
	t.Helper()
	previousLookPath := lookPathFn
	t.Cleanup(func() { lookPathFn = previousLookPath })
	lookPathFn = func(string) (string, error) { return "/usr/bin/gh", nil }
	previousTTY := stdinIsTerminalFn
	t.Cleanup(func() { stdinIsTerminalFn = previousTTY })
	stdinIsTerminalFn = func() bool { return false }
}

func newPreflightGh(listed []byte) *fakeGhRunner {
	return &fakeGhRunner{responses: map[string][]byte{"pr list": listed}, errs: map[string]error{}}
}

func TestPreflightPR_ResolvesHeadAndBaseFromCommits(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.commit(t, "feature", "g.txt")
	r.park(t)
	writeFile(t, r.work, "dirty.txt", "uncommitted\n")

	plan, err := preflightPR(prCommandOptions{head: "feature", dryRun: true, push: true}, nil)
	if err != nil {
		t.Fatalf("preflightPR: %v", err)
	}
	if plan.Head.Ref != "refs/heads/feature" || plan.Head.Name != "feature" {
		t.Errorf("Head = %+v, want refs/heads/feature", plan.Head)
	}
	if plan.Base.Short != "main" || plan.Base.Ref != "refs/remotes/origin/main" || plan.Base.Label != "origin/main" {
		t.Errorf("Base = %+v, want main read as refs/remotes/origin/main", plan.Base)
	}
	if plan.Commits != 2 {
		t.Errorf("Commits = %d, want 2", plan.Commits)
	}
	if got := plan.summary(); !strings.Contains(got, "feature → main (2 commits, tip "+shortSHA(plan.Head.SHA)+")") {
		t.Errorf("summary = %q", got)
	}
}

func TestPreflightPR_DryRunNeedsNeitherGhNorNetwork(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	r.park(t)
	r.breakRemote(t)
	stubGh(t)
	lookPathFn = func(string) (string, error) {
		t.Fatal("a dry run looked for gh")
		return "", nil
	}
	gh := &fakeGhRunner{}

	if _, err := preflightPR(prCommandOptions{head: "feature", dryRun: true, push: true}, gh.run); err != nil {
		t.Fatalf("preflightPR: %v", err)
	}
	if len(gh.calls) != 0 {
		t.Fatalf("a dry run called gh: %v", gh.calls)
	}
}

func TestPreflightPR_FailsBeforeTheModelCall(t *testing.T) {
	cases := []struct {
		name       string
		owner      string
		setup      func(t *testing.T, r prRepo)
		opts       prCommandOptions
		ghList     []byte
		ghErr      error
		noGh       bool
		wantErr    string
		wantNotErr string
	}{
		{
			name:    "gh is not installed",
			setup:   func(t *testing.T, r prRepo) { r.commit(t, "feature", "f.txt"); r.park(t) },
			opts:    prCommandOptions{head: "feature", yes: true, push: true},
			noGh:    true,
			wantErr: "gh CLI not found",
		},
		{
			name:    "the head is the base",
			setup:   func(t *testing.T, r prRepo) {},
			opts:    prCommandOptions{head: "main", yes: true, push: true},
			wantErr: "head and base are both main",
		},
		{
			name: "no commits ahead of the base",
			setup: func(t *testing.T, r prRepo) {
				runGitIn(t, r.work, "branch", "empty", "main")
			},
			opts:    prCommandOptions{head: "empty", yes: true, push: true},
			wantErr: "no PR to open",
		},
		{
			name:    "a branch that is not on the remote with --push=false",
			setup:   func(t *testing.T, r prRepo) { r.commit(t, "feature", "f.txt"); r.park(t) },
			opts:    prCommandOptions{head: "feature", yes: true},
			wantErr: "--push=false",
		},
		{
			name: "an open PR with --no-update",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "feature")
				r.park(t)
			},
			opts:    prCommandOptions{head: "feature", yes: true, push: true, noUpdate: true},
			ghList:  prListJSON(prListEntry(5, "feature", "main", "o")),
			wantErr: "https://github.com/o/r/pull/5",
		},
		{
			name:  "a fork checkout with --no-update finds the caller's own PR in the parent's listing",
			owner: "octo",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "feature")
			},
			opts: prCommandOptions{yes: true, push: true, noUpdate: true},
			ghList: prListJSON(
				prListEntry(9, "feature", "main", "parent-owner"),
				prListEntry(4, "feature", "main", "octo"),
			),
			wantErr:    "https://github.com/o/r/pull/4",
			wantNotErr: "pull/9",
		},
		{
			name:  "--head octo:patch-1 with --no-update finds the fork's PR, not the base repository's own",
			owner: "octo",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "patch-1", "f.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "patch-1")
				r.park(t)
			},
			opts: prCommandOptions{head: "octo:patch-1", yes: true, push: true, noUpdate: true},
			ghList: prListJSON(
				prListEntry(3, "patch-1", "main", "o"),
				prListEntry(4, "patch-1", "main", "octo"),
			),
			wantErr:    "https://github.com/o/r/pull/4",
			wantNotErr: "pull/3",
		},
		{
			name: "--head names an owner other than the push remote's",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				r.park(t)
			},
			opts:    prCommandOptions{head: "octo:feature", yes: true, push: true},
			wantErr: "names a branch of octo",
		},
		{
			name: "a listed PR of ours without a URL",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "feature")
				r.park(t)
			},
			opts:    prCommandOptions{head: "feature", yes: true, push: true},
			ghList:  prListJSON(prListRow(5, "", "feature", "main", "o")),
			wantErr: "without a usable number and URL",
		},
		{
			name: "an open PR with another base than the explicit --base",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "feature")
				r.park(t)
			},
			opts:    prCommandOptions{head: "feature", base: "main", yes: true, push: true},
			ghList:  prListJSON(prListEntry(5, "feature", "develop", "o")),
			wantErr: "refusing to retarget",
		},
		{
			name: "gh cannot list PRs",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				runGitIn(t, r.work, "push", "-q", "origin", "feature")
				r.park(t)
			},
			opts:    prCommandOptions{head: "feature", yes: true, push: true},
			ghErr:   errors.New("gh pr list: HTTP 502"),
			wantErr: "HTTP 502",
		},
		{
			name: "no confirmation channel",
			setup: func(t *testing.T, r prRepo) {
				r.commit(t, "feature", "f.txt")
				r.park(t)
			},
			opts:    prCommandOptions{head: "feature", push: true},
			wantErr: errPRNonInteractive.Error(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner := tc.owner
			if owner == "" {
				owner = "o"
			}
			r := newPRRepoOwnedBy(t, owner)
			tc.setup(t, r)
			stubGh(t)
			if tc.noGh {
				lookPathFn = func(string) (string, error) { return "", errors.New("not found") }
			}
			gh := newPreflightGh(tc.ghList)
			if tc.ghErr != nil {
				gh.errs["pr list"] = tc.ghErr
			}

			_, err := preflightPR(tc.opts, gh.run)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("preflightPR() error = %v, want it to contain %q", err, tc.wantErr)
			}
			if tc.wantNotErr != "" && strings.Contains(err.Error(), tc.wantNotErr) {
				t.Errorf("preflightPR() error = %v, must not mention %q: that is another repository's PR", err, tc.wantNotErr)
			}
		})
	}
}

func TestPreflightPR_WarnsWhenTheOpenPRHasAnotherDetectedBase(t *testing.T) {
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	runGitIn(t, r.work, "push", "-q", "origin", "feature")
	r.park(t)
	stubGh(t)
	gh := newPreflightGh(prListJSON(prListEntry(5, "feature", "develop", "o")))

	var err error
	stderr := captureStderr(t, func() {
		_, err = preflightPR(prCommandOptions{head: "feature", yes: true, push: true}, gh.run)
	})
	if err != nil {
		t.Fatalf("preflightPR: %v", err)
	}
	if !strings.Contains(stderr, "targets develop") || !strings.Contains(stderr, "--base develop") {
		t.Fatalf("stderr %q should say the PR targets develop and how to match it", stderr)
	}
}

func TestWarnPRHeadState_ByHeadState(t *testing.T) {
	cases := []struct {
		name string
		head prHead
		push bool
		want string
	}{
		{"behind", prHead{Name: "feature", Behind: 2}, true, "2 commit(s) behind origin/feature"},
		{"ahead with --push=false leaves the commits out of the described PR", prHead{Name: "feature", Ahead: 3}, false, "3 commit(s) that origin/feature lacks and --push=false; the PR text describes origin/feature and leaves them out"},
		{"ahead and pushing", prHead{Name: "feature", Ahead: 3}, true, ""},
		{"in sync", prHead{Name: "feature"}, false, ""},
		{"remote unreachable with --push=false", prHead{Name: "feature", RemoteErr: errors.New("fetch feature from origin: connection refused")}, false, "could not compare feature with origin/feature (fetch feature from origin: connection refused); the PR text describes the local branch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := captureStderr(t, func() {
				warnPRHeadState(prCommandOptions{push: tc.push}, &prPlan{Head: tc.head, Remote: "origin"})
			})
			if tc.want == "" && out != "" || !strings.Contains(out, tc.want) {
				t.Fatalf("warnings %q, want %q", out, tc.want)
			}
		})
	}
}

func testPRPlan(ghHead string) *prPlan {
	return &prPlan{
		Head:   prHead{Name: "feature", GhHead: ghHead, Owner: "o", Ref: "refs/heads/feature", Local: true},
		Base:   prBase{Short: "main", Ref: "refs/remotes/origin/main", Label: "origin/main"},
		Remote: "origin",
	}
}

var testPRResult = &commands.PRResult{Action: "add", Title: "thing", Summary: "Adds a thing.", Changes: []string{"Add it"}}

func TestCreatePR_ReturnsTheURLGhPrints(t *testing.T) {
	gh := newPreflightGh([]byte("[]"))
	gh.responses["pr create"] = []byte("Warning: 1 uncommitted change\nhttps://github.com/o/r/pull/12\n")

	var url string
	var err error
	captureStdout(t, func() {
		url, err = createPR(gh.run, testPRResult, testPRPlan("feature"), prCommandOptions{draft: true})
	})
	if err != nil {
		t.Fatalf("createPR: %v", err)
	}
	if url != "https://github.com/o/r/pull/12" {
		t.Fatalf("url = %q, want the last line gh printed", url)
	}
	create := gh.calls[len(gh.calls)-1]
	if got := strings.Join(create[:2], " "); got != "pr create" {
		t.Fatalf("last gh call = %v, want pr create", create)
	}
	for _, want := range []string{"--head", "feature", "--base", "main", "--draft"} {
		if !containsArg(create, want) {
			t.Errorf("pr create args %v missing %q", create, want)
		}
	}
}

func TestCreatePR_UpdatesTheOpenPRForTheHeadAndNoOther(t *testing.T) {
	gh := newPreflightGh(prListJSON(
		prListEntry(3, "other", "main", "o"),
		prListEntry(5, "feature", "main", "o"),
	))

	var url string
	var err error
	captureStdout(t, func() {
		url, err = createPR(gh.run, testPRResult, testPRPlan(""), prCommandOptions{})
	})
	if err != nil {
		t.Fatalf("createPR: %v", err)
	}
	if url != "https://github.com/o/r/pull/5" {
		t.Fatalf("url = %q, want the PR for the head", url)
	}
	var edits [][]string
	for _, call := range gh.calls {
		switch strings.Join(call[:2], " ") {
		case "pr create":
			t.Fatalf("created a PR although one is open for the head: %v", call)
		case "pr edit":
			edits = append(edits, call)
		}
	}
	if len(edits) != 1 || edits[0][2] != "https://github.com/o/r/pull/5" {
		t.Fatalf("pr edit calls = %v, want one edit of PR 5", edits)
	}
}

func TestCreatePR_NoUpdateRefusesAnOpenPR(t *testing.T) {
	gh := newPreflightGh(prListJSON(prListEntry(5, "feature", "main", "o")))

	var err error
	captureStdout(t, func() {
		_, err = createPR(gh.run, testPRResult, testPRPlan(""), prCommandOptions{noUpdate: true})
	})
	if err == nil || !strings.Contains(err.Error(), "https://github.com/o/r/pull/5") {
		t.Fatalf("createPR() error = %v, want a refusal naming the open PR", err)
	}
	for _, call := range gh.calls {
		if got := strings.Join(call[:2], " "); got == "pr create" || got == "pr edit" {
			t.Fatalf("gh was asked to change a PR: %v", call)
		}
	}
}

func TestCreatePR_GhListFailureIsNotNoPR(t *testing.T) {
	gh := newPreflightGh(nil)
	gh.errs["pr list"] = errors.New("gh pr list: HTTP 502")

	var err error
	captureStdout(t, func() {
		_, err = createPR(gh.run, testPRResult, testPRPlan(""), prCommandOptions{})
	})
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("createPR() error = %v, want the gh failure", err)
	}
	for _, call := range gh.calls {
		if got := strings.Join(call[:2], " "); got == "pr create" {
			t.Fatalf("created a PR after a failed lookup: %v", call)
		}
	}
}

func TestPreflightPR_IgnoresPRsOfOtherOwnersWithTheSameHeadName(t *testing.T) {
	r := newPRRepoOwnedBy(t, "octo")
	r.commit(t, "feature", "f.txt")
	runGitIn(t, r.work, "push", "-q", "origin", "feature")
	stubGh(t)
	gh := newPreflightGh(prListJSON(prListEntry(9, "feature", "main", "parent-owner")))

	plan, err := preflightPR(prCommandOptions{yes: true, push: true, noUpdate: true}, gh.run)
	if err != nil {
		t.Fatalf("preflightPR: %v (the parent's PR from its own feature branch is not the fork's PR, so --no-update has nothing to refuse)", err)
	}
	if plan.Head.Owner != "octo" {
		t.Errorf("Head.Owner = %q, want octo, the owner of the push remote", plan.Head.Owner)
	}
}

func TestCreatePR_UpdatesOnlyTheHeadOwnersPR(t *testing.T) {
	cases := []struct {
		name     string
		head     string
		ghHead   string
		owner    string
		listed   []byte
		wantEdit string
	}{
		{
			name:     "--head octo:patch-1 edits the fork's PR, not the base repository's own patch-1 PR",
			head:     "patch-1",
			ghHead:   "octo:patch-1",
			owner:    "octo",
			listed:   prListJSON(prListEntry(3, "patch-1", "main", "o"), prListEntry(4, "patch-1", "main", "octo")),
			wantEdit: "https://github.com/o/r/pull/4",
		},
		{
			name:     "a fork checkout edits the caller's PR, not the parent's PR from a same-named branch",
			owner:    "octo",
			listed:   prListJSON(prListEntry(9, "feature", "main", "parent-owner"), prListEntry(4, "feature", "main", "octo")),
			wantEdit: "https://github.com/o/r/pull/4",
		},
		{
			name:   "a fork checkout creates a PR when only the parent has a same-named one",
			owner:  "octo",
			listed: prListJSON(prListEntry(9, "feature", "main", "parent-owner")),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := testPRPlan(tc.ghHead)
			plan.Head.Owner = tc.owner
			if tc.head != "" {
				plan.Head.Name = tc.head
			}
			gh := newPreflightGh(tc.listed)
			gh.responses["pr create"] = []byte("https://github.com/o/r/pull/20\n")

			var url string
			var err error
			captureStdout(t, func() {
				url, err = createPR(gh.run, testPRResult, plan, prCommandOptions{})
			})
			if err != nil {
				t.Fatalf("createPR: %v", err)
			}

			var edits, creates [][]string
			for _, call := range gh.calls {
				switch strings.Join(call[:2], " ") {
				case "pr edit":
					edits = append(edits, call)
				case "pr create":
					creates = append(creates, call)
				}
			}
			if tc.wantEdit == "" {
				if len(edits) != 0 || len(creates) != 1 || url != "https://github.com/o/r/pull/20" {
					t.Fatalf("url %q, edits %v, creates %v: want a new PR and no edit of another owner's PR", url, edits, creates)
				}
				return
			}
			if len(creates) != 0 || len(edits) != 1 || edits[0][2] != tc.wantEdit || url != tc.wantEdit {
				t.Fatalf("url %q, edits %v, creates %v, want one edit of %s", url, edits, creates, tc.wantEdit)
			}
		})
	}
}

func TestCreatePR_RejectsOutputWithoutAPRURL(t *testing.T) {
	cases := []struct {
		name   string
		stdout []byte
	}{
		{"empty output", nil},
		{"only whitespace", []byte(" \n\n")},
		{"text without a URL", []byte("Creating pull request for feature into main in o/r\n")},
		{"a URL that is not a PR", []byte("https://github.com/o/r/issues/5\n")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh := newPreflightGh([]byte("[]"))
			gh.responses["pr create"] = tc.stdout

			var url string
			var err error
			captureStdout(t, func() {
				url, err = createPR(gh.run, testPRResult, testPRPlan(""), prCommandOptions{})
			})
			if err == nil || !strings.Contains(err.Error(), "not a PR URL") {
				t.Fatalf("createPR() = %q, %v, want an error saying the output holds no PR URL", url, err)
			}
			if url != "" {
				t.Errorf("createPR() returned %q with its error: a caller would act on it", url)
			}
		})
	}
}

func TestCreatePR_RefusesAListedPRWithoutAURL(t *testing.T) {
	gh := newPreflightGh(prListJSON(prListRow(5, "", "feature", "main", "o")))

	var url string
	var err error
	captureStdout(t, func() {
		url, err = createPR(gh.run, testPRResult, testPRPlan(""), prCommandOptions{})
	})
	if err == nil || !strings.Contains(err.Error(), "without a usable number and URL") {
		t.Fatalf("createPR() = %q, %v, want a refusal", url, err)
	}
	for _, call := range gh.calls {
		if got := strings.Join(call[:2], " "); got != "pr list" {
			t.Fatalf("gh was asked to run %v after a PR listing without a URL", call)
		}
	}
}

func TestUpdatePR_RefusesAnEmptySelector(t *testing.T) {
	for _, url := range []string{"", "  "} {
		t.Run(fmt.Sprintf("%q", url), func(t *testing.T) {
			gh := &fakeGhRunner{}
			var err error
			captureStdout(t, func() { err = updatePR(gh.run, url, "title", "body") })
			if !errors.Is(err, errEmptyPRSelector) {
				t.Fatalf("updatePR() error = %v, want %v", err, errEmptyPRSelector)
			}
			if len(gh.calls) != 0 {
				t.Fatalf("gh was called with an empty selector: %v", gh.calls)
			}
		})
	}
}

// fakeModelInvoker answers every tool call with one fixed PR and keeps the
// prompt it was given.
type fakeModelInvoker struct {
	raw    json.RawMessage
	prompt *string
}

func (f fakeModelInvoker) Invoke(_ context.Context, _, userPrompt string, _ tools.Definition, _ *transparency.ContextAudit) (*oneshot.Result, *transparency.Trace, error) {
	*f.prompt = userPrompt
	return &oneshot.Result{ToolCall: &tools.ToolCall{Arguments: f.raw}}, nil, nil
}

// stubPRModel replaces the model with fakeModelInvoker and returns where it
// keeps the prompt.
func stubPRModel(t *testing.T) *string {
	t.Helper()
	t.Setenv("BUCKLEY_PROMPT_PR", "")
	t.Setenv("BUCKLEY_PROMPT_PR_FILE", "")
	raw := json.RawMessage(`{"action":"add","title":"feature file","summary":"Adds the feature file.","changes":["Add f.txt"]}`)
	prompt := new(string)
	previous := newPRCommandRuntimeFn
	t.Cleanup(func() { newPRCommandRuntimeFn = previous })
	newPRCommandRuntimeFn = func(prCommandOptions) (*prCommandRuntime, func(), error) {
		return &prCommandRuntime{
			backend:   oneshotBackendAPI,
			modelID:   "test-model",
			ledger:    transparency.NewCostLedger(),
			framework: oneshot.NewFramework(fakeModelInvoker{raw: raw, prompt: prompt}, nil),
		}, func() {}, nil
	}
	return prompt
}

// fakeGh is a gh executable on PATH that records every call and answers from
// canned text. It runs under the real execGhCommand and ghLabelClient, so it
// sees every argument they send, including an empty one.
type fakeGh struct{ log string }

func installFakeGh(t *testing.T, listed, created string) *fakeGh {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{"list.json": listed, "create.out": created} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
for a in "$@"; do printf '%s\000' "$a" >> "$FAKE_GH_LOG"; done
printf '\036' >> "$FAKE_GH_LOG"
case "$1 $2" in
"pr list") cat "$FAKE_GH_DIR/list.json" ;;
"pr create") cat "$FAKE_GH_DIR/create.out" ;;
"pr view"|"pr edit")
	if [ -z "$3" ]; then echo "could not determine current branch" >&2; exit 1; fi
	if [ "$2" = view ]; then printf '{"labels":[]}'; fi
	;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	g := &fakeGh{log: filepath.Join(dir, "calls.log")}
	t.Setenv("FAKE_GH_DIR", dir)
	t.Setenv("FAKE_GH_LOG", g.log)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return g
}

// calls returns each gh invocation as its argument list.
func (g *fakeGh) calls(t *testing.T) [][]string {
	t.Helper()
	data, err := os.ReadFile(g.log)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var calls [][]string
	for _, record := range strings.Split(string(data), "\x1e") {
		if record != "" {
			calls = append(calls, strings.Split(strings.TrimSuffix(record, "\x00"), "\x00"))
		}
	}
	return calls
}

func TestPreflightPR_ReadsTheBaseThroughItsFullRef(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, r prRepo, tip string)
	}{
		{
			name:  "a local branch named origin/main at the head's tip",
			setup: func(t *testing.T, r prRepo, tip string) { runGitIn(t, r.work, "branch", "origin/main", tip) },
		},
		{
			name:  "a tag named origin/main at the head's tip",
			setup: func(t *testing.T, r prRepo, tip string) { runGitIn(t, r.work, "tag", "origin/main", tip) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPRRepo(t)
			tip := r.commit(t, "feature", "f.txt")
			r.park(t)
			tc.setup(t, r, tip)

			plan, err := preflightPR(prCommandOptions{head: "feature", dryRun: true, push: true}, nil)
			if err != nil {
				t.Fatalf("preflightPR: %v (a ref that merely shares the base's short name made the branch look empty)", err)
			}
			if plan.Commits != 1 {
				t.Errorf("Commits = %d, want 1", plan.Commits)
			}
		})
	}
}

func TestRunPRCommand_ThePromptCoversTheRangeOfTheRealBase(t *testing.T) {
	r := newPRRepo(t)
	first := r.commit(t, "feature", "f.txt")
	r.commit(t, "feature", "g.txt")
	r.park(t)
	runGitIn(t, r.work, "branch", "origin/main", first)
	stubPRDependencies(t, func() error { return nil })
	prompt := stubPRModel(t)

	var err error
	captureStdout(t, func() { err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature"}) })
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}

	for _, want := range []string{
		"(head: feature, base: origin/main)",
		"add f.txt",
		"add g.txt",
		"f.txt on feature",
		"g.txt on feature",
	} {
		if !strings.Contains(*prompt, want) {
			t.Errorf("prompt is missing %q: the range was not read against the remote branch:\n%s", want, *prompt)
		}
	}
}

// TestRunPRCommand_HeadDiffIgnoresTheCheckedOutWorktreesGitattributes is the
// ADR 0015 decision 6 case for attributes: PR evidence comes from commits, never
// from the working tree. Git reads diff attributes from the checked-out
// worktree, so a "-diff" line there, uncommitted or committed on the checked-out
// branch, turned the head's text file into "[binary: content omitted]".
func TestRunPRCommand_HeadDiffIgnoresTheCheckedOutWorktreesGitattributes(t *testing.T) {
	requireGit(t, 2, 40, "git --attr-source")
	for _, tc := range []struct {
		name      string
		committed bool
	}{
		{"an uncommitted .gitattributes", false},
		{"a .gitattributes committed on the checked-out branch", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPRRepo(t)
			r.commit(t, "feature", "f.txt")
			r.park(t)
			writeFile(t, r.work, ".gitattributes", "f.txt -diff\n")
			if tc.committed {
				runGitIn(t, r.work, "add", ".gitattributes")
				runGitIn(t, r.work, "commit", "-q", "-m", "mark f.txt -diff on the checked-out branch")
			}
			stubPRDependencies(t, func() error { return nil })
			prompt := stubPRModel(t)

			var err error
			captureStdout(t, func() { err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature"}) })
			if err != nil {
				t.Fatalf("runPRCommand: %v", err)
			}

			if !strings.Contains(*prompt, "+f.txt on feature") {
				t.Errorf("the prompt lacks the head's change to f.txt:\n%s", *prompt)
			}
			if strings.Contains(*prompt, "binary: content omitted") {
				t.Errorf("the prompt calls f.txt binary because of the checked-out worktree's .gitattributes:\n%s", *prompt)
			}
		})
	}
}

// TestRunPRCommand_DirIntoASubdirectoryKeepsGeneratedMarks covers git
// check-attr, which resolves the paths a diff names against the current
// directory. Started with -C <subdir>, it found no attributes and sent a
// generated file's whole diff to the model.
func TestRunPRCommand_DirIntoASubdirectoryKeepsGeneratedMarks(t *testing.T) {
	requireGit(t, 2, 40, "git --attr-source")
	isolateWorkDirGlobals(t)
	r := newPRRepo(t)
	r.commit(t, "feature", "f.txt")
	if err := os.MkdirAll(filepath.Join(r.work, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, r.work, ".gitattributes", "data/*.txt linguist-generated\n")
	writeFile(t, r.work, filepath.Join("data", "table.txt"), "zorblax generated row\n")
	runGitIn(t, r.work, "add", ".gitattributes", "data/table.txt")
	runGitIn(t, r.work, "commit", "-q", "-m", "add generated data")
	r.park(t)
	subdir := filepath.Join(r.work, "pkg")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	chdirTemp(t, t.TempDir())
	stubPRDependencies(t, func() error { return nil })
	prompt := stubPRModel(t)

	var err error
	captureStdout(t, func() {
		err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature", "-C", subdir})
	})
	if err != nil {
		t.Fatalf("runPRCommand: %v", err)
	}

	if !strings.Contains(*prompt, "+f.txt on feature") {
		t.Errorf("the prompt lacks the source change:\n%s", *prompt)
	}
	if strings.Contains(*prompt, "zorblax generated row") {
		t.Errorf("the generated file's content reached the model: -C into a subdirectory lost its linguist-generated mark:\n%s", *prompt)
	}
	if !strings.Contains(*prompt, "data/table.txt") {
		t.Errorf("the generated file is missing from the prompt, even as a summary line:\n%s", *prompt)
	}
}

func TestRunPRCommand_GitWithoutAttrSourceWarnsOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		version  string
		wantWarn bool
	}{
		{"git 2.39 cannot read .gitattributes from a commit", "2.39.0", true},
		{"git 2.40 can", "2.40.0", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPRRepo(t)
			r.commit(t, "feature", "f.txt")
			r.park(t)
			stubPRDependencies(t, func() error { return nil })
			stubPRModel(t)
			installFakeGit(t, tc.version)

			var err error
			stderr := captureStderr(t, func() {
				captureStdout(t, func() { err = runPRCommand([]string{"--dry-run", "--cost=false", "--head", "feature"}) })
			})
			if err != nil {
				t.Fatalf("runPRCommand: %v", err)
			}

			var warnings []string
			for _, line := range strings.Split(stderr, "\n") {
				if strings.Contains(line, ".gitattributes") {
					warnings = append(warnings, line)
				}
			}
			if !tc.wantWarn {
				if len(warnings) != 0 {
					t.Fatalf("warned about .gitattributes on git %s: %q", tc.version, warnings)
				}
				return
			}
			if len(warnings) != 1 || !strings.HasPrefix(warnings[0], "warning: ") || !strings.Contains(warnings[0], tc.version) || !strings.Contains(warnings[0], "2.40") {
				t.Fatalf("stderr = %q, want one warning line that names git %s and 2.40", stderr, tc.version)
			}
		})
	}
}

func TestRunPRCommand_LabelsTheURLOfThePRItCreatedOrUpdated(t *testing.T) {
	const pull = "https://github.com/o/r/pull/"
	cases := []struct {
		name        string
		args        []string
		owner       string
		listed      string
		created     string
		wantEdit    string
		wantCreate  bool
		wantLabeled string
		wantErr     string
	}{
		{
			name:        "a new PR is labeled by the URL gh printed",
			listed:      "[]",
			created:     pull + "12\n",
			wantCreate:  true,
			wantLabeled: pull + "12",
		},
		{
			name: "the open PR for the head is updated and labeled; the checked-out branch's PR is not touched",
			listed: string(prListJSON(
				prListEntry(3, "other", "main", "o"),
				prListEntry(5, "feature", "main", "o"),
			)),
			wantEdit:    pull + "5",
			wantLabeled: pull + "5",
		},
		{
			name:  "--head octo:feature labels the fork's PR, not the base repository's feature PR",
			args:  []string{"--head", "octo:feature"},
			owner: "octo",
			listed: string(prListJSON(
				prListEntry(3, "feature", "main", "o"),
				prListEntry(4, "feature", "main", "octo"),
			)),
			wantEdit:    pull + "4",
			wantLabeled: pull + "4",
		},
		{
			name:       "gh prints no PR URL: nothing is labeled",
			listed:     "[]",
			created:    "",
			wantCreate: true,
			wantErr:    "not a PR URL",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner := tc.owner
			if owner == "" {
				owner = "o"
			}
			r := newPRRepoOwnedBy(t, owner)
			r.commit(t, "feature", "f.txt")
			r.park(t)
			stubPRDependencies(t, func() error { return nil })
			stubPRModel(t)
			stubGh(t)
			g := installFakeGh(t, tc.listed, tc.created)

			args := []string{"--yes", "--cost=false", "--require-verification"}
			if len(tc.args) == 0 {
				args = append(args, "--head", "feature")
			}
			args = append(args, tc.args...)
			var err error
			captureStdout(t, func() { err = runPRCommand(args) })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("runPRCommand() error = %v, want it to contain %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("runPRCommand: %v", err)
			}

			var titleEdits, labelEdits, creates []string
			for _, call := range g.calls(t) {
				verb := strings.Join(call[:2], " ")
				if (verb == "pr view" || verb == "pr edit") && call[2] == "" {
					t.Fatalf("gh %v: an empty PR argument makes gh act on the current branch's PR", call)
				}
				switch {
				case verb == "pr create":
					creates = append(creates, verb)
				case verb == "pr edit" && containsArg(call, "--title"):
					titleEdits = append(titleEdits, call[2])
				case verb == "pr edit" && containsArg(call, "--add-label"):
					labelEdits = append(labelEdits, call[2])
				}
			}
			if tc.wantCreate != (len(creates) == 1) {
				t.Errorf("pr create calls = %d, want a create = %v", len(creates), tc.wantCreate)
			}
			if tc.wantEdit == "" && len(titleEdits) != 0 {
				t.Errorf("title and body edits of %v, want none", titleEdits)
			}
			if tc.wantEdit != "" && !reflect.DeepEqual(titleEdits, []string{tc.wantEdit}) {
				t.Errorf("title and body edits of %v, want only %s", titleEdits, tc.wantEdit)
			}
			if tc.wantLabeled == "" && len(labelEdits) != 0 {
				t.Errorf("labeled %v, want no PR labeled", labelEdits)
			}
			if tc.wantLabeled != "" && !reflect.DeepEqual(labelEdits, []string{tc.wantLabeled}) {
				t.Errorf("labeled %v, want only %s", labelEdits, tc.wantLabeled)
			}
		})
	}
}

func TestExecGhCommand_ErrorNamesTheCommandNotItsArguments(t *testing.T) {
	bin := t.TempDir()
	script := "#!/bin/sh\necho 'a pull request for branch already exists' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	_, err := execGhCommand("pr", "create", "--title", "t", "--body", "SECRET BODY TEXT")
	if err == nil {
		t.Fatal("execGhCommand() = nil, want the failing exit")
	}
	if strings.Contains(err.Error(), "SECRET BODY TEXT") {
		t.Errorf("error repeats the PR body: %v", err)
	}
	if !strings.Contains(err.Error(), "pr create") || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error %q should name the command and carry gh's message", err)
	}
}
