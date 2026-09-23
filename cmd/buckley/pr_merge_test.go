package main

import (
	"context"
	"strings"
	"testing"
)

// fakeGhRunner replays canned responses keyed by the joined argument list's
// prefix (first two args, e.g. "pr checks"), so a test only has to stub the
// gh subcommands it actually exercises.
type fakeGhRunner struct {
	responses map[string][]byte
	errs      map[string]error
	calls     [][]string
}

func (f *fakeGhRunner) run(args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string(nil), args...))
	key := strings.Join(args[:min(2, len(args))], " ")
	if err, ok := f.errs[key]; ok {
		return f.responses[key], err
	}
	if out, ok := f.responses[key]; ok {
		return out, nil
	}
	return nil, nil
}

func TestParsePRMergeOptions_ParsesFlagsAndPositional(t *testing.T) {
	opts, err := parsePRMergeOptions([]string{"--squash", "--admin", "--delete-branch", "--yes", "42"})
	if err != nil {
		t.Fatalf("parsePRMergeOptions: %v", err)
	}
	if opts.number != 42 {
		t.Fatalf("number = %d, want 42", opts.number)
	}
	if opts.method != "squash" {
		t.Fatalf("method = %q, want squash", opts.method)
	}
	if !opts.admin || !opts.deleteBranch || !opts.yes {
		t.Fatalf("unexpected bool options: %+v", opts)
	}
}

func TestParsePRMergeOptions_RefusesMultipleMethods(t *testing.T) {
	_, err := parsePRMergeOptions([]string{"--squash", "--merge", "42"})
	if err == nil {
		t.Fatal("parsePRMergeOptions() = nil, want error for conflicting methods")
	}
}

func TestParsePRMergeOptions_RequiresPRNumber(t *testing.T) {
	_, err := parsePRMergeOptions([]string{"--squash"})
	if err == nil {
		t.Fatal("parsePRMergeOptions() = nil, want error for missing PR number")
	}
}

func TestParsePRMergeOptions_DoesNotImplyAdmin(t *testing.T) {
	opts, err := parsePRMergeOptions([]string{"--squash", "42"})
	if err != nil {
		t.Fatalf("parsePRMergeOptions: %v", err)
	}
	if opts.admin {
		t.Fatal("admin = true, want false (never implied)")
	}
}

func TestFailingOrPendingChecks_FiltersOnlyBlockers(t *testing.T) {
	checks := []prMergeCheck{
		{Name: "build", State: "SUCCESS", Bucket: "pass"},
		{Name: "lint", State: "FAILURE", Bucket: "fail"},
		{Name: "slow-suite", State: "PENDING", Bucket: "pending"},
		{Name: "docs-only", State: "SKIPPED", Bucket: "skipping"},
	}
	bad := failingOrPendingChecks(checks)
	if len(bad) != 2 {
		t.Fatalf("bad = %#v, want 2 entries (lint, slow-suite)", bad)
	}
	names := []string{bad[0].Name, bad[1].Name}
	if names[0] != "lint" || names[1] != "slow-suite" {
		t.Fatalf("bad names = %#v, want [lint slow-suite]", names)
	}
}

func TestResolveMergeMethod_ExplicitRefusedWhenNotAllowed(t *testing.T) {
	settings := repoMergeSettings{SquashMergeAllowed: false, MergeCommitAllowed: true, RebaseMergeAllowed: true}
	_, err := resolveMergeMethod("squash", settings)
	if err == nil {
		t.Fatal("resolveMergeMethod() = nil, want refusal (squash not allowed)")
	}
}

func TestResolveMergeMethod_AutoPrefersSquashThenMergeThenRebase(t *testing.T) {
	cases := []struct {
		settings repoMergeSettings
		want     string
	}{
		{repoMergeSettings{SquashMergeAllowed: true, MergeCommitAllowed: true, RebaseMergeAllowed: true}, "squash"},
		{repoMergeSettings{MergeCommitAllowed: true, RebaseMergeAllowed: true}, "merge"},
		{repoMergeSettings{RebaseMergeAllowed: true}, "rebase"},
	}
	for _, c := range cases {
		got, err := resolveMergeMethod("", c.settings)
		if err != nil {
			t.Fatalf("resolveMergeMethod(%+v): %v", c.settings, err)
		}
		if got != c.want {
			t.Fatalf("resolveMergeMethod(%+v) = %q, want %q", c.settings, got, c.want)
		}
	}
}

func TestResolveMergeMethod_RefusesWhenNoneAllowed(t *testing.T) {
	_, err := resolveMergeMethod("", repoMergeSettings{})
	if err == nil {
		t.Fatal("resolveMergeMethod() = nil, want refusal when no method is allowed")
	}
}

func TestBuildGhPRMergeArgs_SquashIncludesSubjectAndBody(t *testing.T) {
	args := buildGhPRMergeArgs(7, "squash", "add: subject", "- body line", false, true)
	want := []string{"pr", "merge", "7", "--squash", "--subject", "add: subject", "--body", "- body line", "--delete-branch"}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestBuildGhPRMergeArgs_RebaseOmitsSubjectAndBody(t *testing.T) {
	args := buildGhPRMergeArgs(7, "rebase", "add: subject", "- body line", true, false)
	for _, a := range args {
		if a == "--subject" || a == "--body" {
			t.Fatalf("args = %#v, rebase must not pass --subject/--body", args)
		}
	}
	if !contains(args, "--admin") {
		t.Fatalf("args = %#v, want --admin", args)
	}
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestBuildGhPRMergeArgs_NeverAddsAdminUnlessRequested(t *testing.T) {
	args := buildGhPRMergeArgs(7, "merge", "", "", false, false)
	if contains(args, "--admin") {
		t.Fatalf("args = %#v, --admin must not appear unless requested", args)
	}
}

func TestFetchPRChecks_TrustsJSONEvenWhenGhExitsNonzero(t *testing.T) {
	fake := &fakeGhRunner{
		responses: map[string][]byte{
			"pr checks": []byte(`[{"name":"build","state":"FAILURE","bucket":"fail"}]`),
		},
		errs: map[string]error{
			"pr checks": errExitNonzero,
		},
	}
	checks, err := fetchPRChecks(fake.run, 9)
	if err != nil {
		t.Fatalf("fetchPRChecks: %v", err)
	}
	if len(checks) != 1 || checks[0].Name != "build" {
		t.Fatalf("checks = %#v, want [build]", checks)
	}
}

func TestFetchPRMergeInfo_ParsesCommitsAndBranches(t *testing.T) {
	fake := &fakeGhRunner{
		responses: map[string][]byte{
			"pr view": []byte(`{"title":"add: widget","body":"does a thing","baseRefName":"main","headRefName":"feature","commits":[{"messageHeadline":"add: step one"},{"messageHeadline":"add: step two"}]}`),
		},
	}
	info, err := fetchPRMergeInfo(fake.run, 9)
	if err != nil {
		t.Fatalf("fetchPRMergeInfo: %v", err)
	}
	if info.Title != "add: widget" || info.Base != "main" || info.Head != "feature" {
		t.Fatalf("info = %+v, unexpected", info)
	}
	if len(info.Commits) != 2 || info.Commits[0] != "add: step one" {
		t.Fatalf("commits = %#v, want [add: step one, add: step two]", info.Commits)
	}
}

func TestPRMergeWithRunner_RefusesOnFailingChecks(t *testing.T) {
	fake := &fakeGhRunner{
		responses: map[string][]byte{
			"pr checks": []byte(`[{"name":"ci","state":"FAILURE","bucket":"fail"}]`),
		},
	}
	err := prMergeWithRunner(prMergeCommandOptions{number: 5, yes: true}, fake.run)
	if err == nil {
		t.Fatal("prMergeWithRunner() = nil, want refusal on failing checks")
	}
	if !strings.Contains(err.Error(), "ci") {
		t.Fatalf("error = %v, want mention of the failing check", err)
	}
	// Must never have reached gh pr merge.
	for _, call := range fake.calls {
		if len(call) >= 2 && call[0] == "pr" && call[1] == "merge" {
			t.Fatalf("gh pr merge was invoked despite failing checks: %v", call)
		}
	}
}

// The squash/merge path through prMergeWithRunner also calls
// generatePRMergeMessage, which does full model-backend dependency init
// (same as buckley commit/pr); that path is exercised end-to-end in the
// live demo, not here. This suite covers it in the pieces that matter for
// unit testing: gh argument construction (TestBuildGhPRMergeArgs_*),
// message rendering from a fake commitRunner
// (TestRenderPRMergeMessageWithRunner_UsesFakeModel), and the full
// checks/settings/merge flow for --rebase, which never calls the model.
func TestPRMergeWithRunner_RebaseSkipsMessageGenerationAndSubjectBody(t *testing.T) {
	fake := &fakeGhRunner{
		responses: map[string][]byte{
			"pr checks": []byte(`[]`),
			"repo view": []byte(`{"squashMergeAllowed":false,"mergeCommitAllowed":false,"rebaseMergeAllowed":true}`),
			"pr view":   []byte(`{"title":"add: widget","body":"","baseRefName":"main","headRefName":"feature","commits":[{"messageHeadline":"add: step one"}]}`),
			"pr merge":  []byte(""),
		},
	}
	err := prMergeWithRunner(prMergeCommandOptions{number: 5, yes: true}, fake.run)
	if err != nil {
		t.Fatalf("prMergeWithRunner: %v", err)
	}

	var mergeCall []string
	for _, call := range fake.calls {
		if len(call) >= 2 && call[0] == "pr" && call[1] == "merge" {
			mergeCall = call
		}
	}
	if mergeCall == nil {
		t.Fatal("gh pr merge was never invoked")
	}
	for _, a := range mergeCall {
		if a == "--subject" || a == "--body" {
			t.Fatalf("mergeCall = %v, rebase must not include --subject/--body", mergeCall)
		}
	}
	if !contains(mergeCall, "--rebase") {
		t.Fatalf("mergeCall = %v, want --rebase", mergeCall)
	}

	// pr diff must never have been fetched for a rebase merge (no message
	// to generate).
	for _, call := range fake.calls {
		if len(call) >= 2 && call[0] == "pr" && call[1] == "diff" {
			t.Fatalf("pr diff was fetched despite --rebase: %v", call)
		}
	}
}

func TestRenderPRMergeMessageWithRunner_UsesFakeModel(t *testing.T) {
	runner := &fakeCommitRunner{result: fakeMergeResult("add", "api", []string{
		"Add the widget endpoint",
		"Cover it with an integration test",
	})}
	subject, body, err := renderPRMergeMessageWithRunner(context.Background(), runner)
	if err != nil {
		t.Fatalf("renderPRMergeMessageWithRunner: %v", err)
	}
	if subject != "add(api): ignored — the CLI overwrites this for merge commits" {
		t.Fatalf("subject = %q", subject)
	}
	if !strings.Contains(body, "Add the widget endpoint") {
		t.Fatalf("body = %q, want generated bullet", body)
	}
	if runner.calls != 1 {
		t.Fatalf("runner called %d times, want 1", runner.calls)
	}
}

// errExitNonzero simulates the `gh pr checks` nonzero-exit-with-valid-JSON
// behavior documented in pkg/oneshot/commands/review_pr_context.go.
var errExitNonzero = &strconvError{msg: "exit status 8"}

type strconvError struct{ msg string }

func (e *strconvError) Error() string { return e.msg }
