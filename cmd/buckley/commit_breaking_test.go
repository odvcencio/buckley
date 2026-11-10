package main

import (
	"context"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot/commands"
)

// Review of #469, finding 2: production code keeps a breaking claim, also under
// internal/. Only tests, fixtures and documentation drop it by default.
func TestOnlyTestsFixturesOrDocs(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  bool
	}{
		{"tests and golden files", []string{"internal/ccore/golden_test.go", "internal/ccore/testdata/golden/json-object.json"}, true},
		{"tests only", []string{"pkg/foo/foo_test.go", "parser_test.go"}, true},
		{"docs", []string{"docs/README.md", "README.md", "docs/guide.txt"}, true},
		{"fixtures", []string{"pkg/x/fixtures/a.yaml", "pkg/x/__snapshots__/a.snap", "a.golden"}, true},
		{"production code under internal", []string{"internal/ccore/lexer.go"}, false},
		{"a CLI flag removed under internal", []string{"internal/cli/flags.go", "internal/cli/flags_test.go"}, false},
		{"an exported package", []string{"parser.go"}, false},
		{"a command", []string{"cmd/tool/main.go"}, false},
		{"a file under internal that is not Go", []string{"internal/schema.json"}, false},
		{"tests with production code", []string{"parser_test.go", "parser.go"}, false},
		{"no files", nil, false},
	}
	for _, tc := range cases {
		if got := onlyTestsFixturesOrDocs(tc.files); got != tc.want {
			t.Errorf("%s: onlyTestsFixturesOrDocs(%v) = %v, want %v", tc.name, tc.files, got, tc.want)
		}
	}
}

func TestApplyBreakingPolicy(t *testing.T) {
	internalOnly := map[string]string{
		"internal/ccore/lexer_test.go":          "package ccore\n",
		"internal/ccore/testdata/golden/a.json": "{}\n",
		"docs/notes.md":                         "x\n",
	}
	withPublic := map[string]string{"internal/ccore/lexer.go": "package ccore\n", "parser.go": "package p\n"}
	internalCode := map[string]string{"internal/cli/flags.go": "package cli\n"}

	cases := []struct {
		name     string
		files    map[string]string
		opts     commitCommandOptions
		wantKept bool
		wantNote bool
	}{
		{"auto drops the trailer when only tests, fixtures and docs changed", internalOnly, commitCommandOptions{breaking: breakingAuto}, false, true},
		{"auto keeps it when production code changed", withPublic, commitCommandOptions{breaking: breakingAuto}, true, false},
		{"keep trusts the model", internalOnly, commitCommandOptions{breaking: breakingKeep}, true, false},
		{"options without a policy leave the result alone", internalOnly, commitCommandOptions{}, true, false},
		{"auto keeps it for production code under internal", internalCode, commitCommandOptions{breaking: breakingAuto}, true, false},
		{"never leaves it out", withPublic, commitCommandOptions{breaking: breakingNever}, false, false},
		{"auto looks only at the files of a scoped commit", mergeMaps(withPublic, internalOnly), commitCommandOptions{breaking: breakingAuto, paths: []string{"docs"}}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			stageRepo(t, tc.files)
			cr := &commands.CommitResult{Action: "refactor", Subject: "port the lex automaton", Breaking: true, BreakingReason: "Callers must supply lex tables."}
			stderr := captureStderr(t, func() { applyBreakingPolicy(cr, tc.opts) })
			if cr.Breaking != tc.wantKept {
				t.Errorf("Breaking = %v, want %v", cr.Breaking, tc.wantKept)
			}
			if noted := strings.Contains(stderr, "left out the BREAKING CHANGE trailer"); noted != tc.wantNote {
				t.Errorf("stderr = %q, want a note = %v", stderr, tc.wantNote)
			}
			if got := strings.Contains(cr.Format(), "BREAKING CHANGE:"); got != tc.wantKept {
				t.Errorf("message has the trailer = %v, want %v:\n%s", got, tc.wantKept, cr.Format())
			}
		})
	}

	// A commit that does not claim to be breaking is left alone, quietly.
	t.Setenv("HOME", t.TempDir())
	stageRepo(t, internalOnly)
	plain := &commands.CommitResult{Action: "refactor", Subject: "port the lex automaton"}
	if stderr := captureStderr(t, func() { applyBreakingPolicy(plain, commitCommandOptions{breaking: breakingAuto}) }); stderr != "" || plain.Breaking {
		t.Errorf("a non-breaking commit was touched: Breaking = %v, stderr = %q", plain.Breaking, stderr)
	}
	applyBreakingPolicy(nil, commitCommandOptions{breaking: breakingAuto})
}

// Every path that generates a message goes through runCommitGeneration, so
// the rule covers normal, merge and squash commits and the regenerate prompt.
func TestRunCommitGeneration_AppliesTheBreakingPolicy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stageRepo(t, map[string]string{"internal/ccore/lexer_test.go": "package ccore\n"})
	runner := &fakeCommitRunner{result: &commitRunResult{Commit: &commands.CommitResult{
		Action: "refactor", Scope: "ccore", Subject: "port the lex automaton", Body: []string{"Move the DFA walk"},
		Breaking: true, BreakingReason: "Callers must supply lex tables.",
	}}}

	var result *commitRunResult
	var err error
	captureStderr(t, func() {
		result, err = runCommitGeneration(context.Background(), runner, commitCommandOptions{compactOutput: true, breaking: breakingAuto})
	})
	if err != nil {
		t.Fatalf("runCommitGeneration: %v", err)
	}
	if message := result.Commit.Format(); strings.Contains(message, "BREAKING CHANGE") {
		t.Errorf("the message keeps the trailer:\n%s", message)
	}
}

func TestParseCommitCommandOptions_Breaking(t *testing.T) {
	t.Setenv(envCommitBackend, "")
	t.Setenv(envOneshotBackend, "")
	opts, err := parseCommitCommandOptions(nil)
	if err != nil || opts.breaking != breakingAuto {
		t.Fatalf("default: opts.breaking = %q, err = %v", opts.breaking, err)
	}
	if opts, err = parseCommitCommandOptions([]string{"--breaking=keep"}); err != nil || opts.breaking != breakingKeep {
		t.Fatalf("--breaking=keep: opts.breaking = %q, err = %v", opts.breaking, err)
	}
	if _, err = parseCommitCommandOptions([]string{"--breaking=sometimes"}); err == nil || !strings.Contains(err.Error(), "auto, keep, or never") {
		t.Fatalf("--breaking=sometimes: err = %v", err)
	}
}

// A bullet that stays over the word limit after the model was asked to
// shorten it is cut by code, and stderr says which bullet and by how much.
func TestRunCommitGeneration_ReportsWhatCodeChanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stageRepo(t, map[string]string{"a.go": "package a\n"})
	runner := &fakeCommitRunner{result: &commitRunResult{
		Commit:  &commands.CommitResult{Action: "fix", Subject: "tidy the report", Body: []string{"Keep it short"}},
		Repairs: []string{"cut bullet 1 from 25 to 16 words; check that it still says what changed"},
	}}
	var err error
	stderr := captureStderr(t, func() {
		_, err = runCommitGeneration(context.Background(), runner, commitCommandOptions{compactOutput: true, breaking: breakingAuto})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "changed the generated message: cut bullet 1 from 25 to 16 words") {
		t.Errorf("stderr does not report the cut:\n%s", stderr)
	}
}

func mergeMaps(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
