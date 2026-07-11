package commands

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/diffsignal"
	"m31labs.dev/buckley/pkg/transparency"
)

type reviewCommandExitError struct {
	code int
}

func (e reviewCommandExitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e reviewCommandExitError) ExitCode() int { return e.code }

func TestNormalizePRCommandResult_PreservesPendingChecksJSON(t *testing.T) {
	want := []byte(`[{"name":"unit","state":"PENDING"}]`)
	got, err := normalizePRCommandResult(
		"gh",
		[]string{"pr", "checks", "208", "--json", "name,state", "--repo", "m31labs/gotreesitter"},
		want,
		reviewCommandExitError{code: 8},
	)
	if err != nil {
		t.Fatalf("normalizePRCommandResult: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("output = %q, want %q", got, want)
	}

	checks, err := getPRChecks(func(name string, args ...string) ([]byte, error) {
		return normalizePRCommandResult(name, args, want, reviewCommandExitError{code: 8})
	}, prReference{Number: 208, Repository: "m31labs/gotreesitter"})
	if err != nil {
		t.Fatalf("getPRChecks: %v", err)
	}
	if got := summarizePRChecks(checks); got != "pending (1/1)" {
		t.Fatalf("summarizePRChecks = %q, want pending (1/1)", got)
	}
}

func TestNormalizePRCommandResult_PreservesCanonicalNoChecksAsEmptyJSON(t *testing.T) {
	args := []string{"pr", "checks", "216", "--json", "name,state", "--repo", "m31labs/gotreesitter"}
	output := []byte("no checks reported on the 'release/v0.23.0' branch\n")
	got, err := normalizePRCommandResult("gh", args, output, reviewCommandExitError{code: 1})
	if err != nil {
		t.Fatalf("normalizePRCommandResult: %v", err)
	}
	if string(got) != "[]" {
		t.Fatalf("output = %q, want []", got)
	}

	checks, err := getPRChecks(func(name string, args ...string) ([]byte, error) {
		return normalizePRCommandResult(name, args, output, reviewCommandExitError{code: 1})
	}, prReference{Number: 216, Repository: "m31labs/gotreesitter"})
	if err != nil {
		t.Fatalf("getPRChecks: %v", err)
	}
	if len(checks) != 0 {
		t.Fatalf("checks = %#v, want stable empty state", checks)
	}
}

func TestNormalizePRCommandResult_RejectsOtherFailures(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		output []byte
		code   int
	}{
		{name: "gh", args: []string{"pr", "checks", "208", "--json", "name,state"}, output: []byte(`[{"state":"PENDING"}]`), code: 1},
		{name: "gh", args: []string{"pr", "checks", "208", "--json", "name,state"}, output: []byte("no checks reported on the 'topic' branch; authentication failed"), code: 1},
		{name: "gh", args: []string{"pr", "checks", "208", "--json", "name,state"}, output: []byte("no checks reported on the '' branch"), code: 1},
		{name: "gh", args: []string{"pr", "checks", "208", "--json", "name,state"}, output: []byte("not json"), code: 8},
		{name: "gh", args: []string{"pr", "view", "208", "--json", "state"}, output: []byte(`{"state":"OPEN"}`), code: 8},
		{name: "git", args: []string{"status", "--porcelain"}, output: []byte(`[]`), code: 8},
	}
	for _, test := range tests {
		if _, err := normalizePRCommandResult(test.name, test.args, test.output, reviewCommandExitError{code: test.code}); err == nil {
			t.Fatalf("normalizePRCommandResult(%s %v, code %d) unexpectedly succeeded", test.name, test.args, test.code)
		}
	}
}

func TestSelectPRCIEvidence_DocumentationOnlyInheritsImmutableBaseChecks(t *testing.T) {
	pr := &PRInfo{
		Host:       "github.example",
		Repository: "m31labs/buckley",
		BaseSHA:    "base-sha",
		HeadSHA:    "head-sha",
	}
	tests := []struct {
		name           string
		checkResponse  string
		statusResponse string
		want           string
	}{
		{
			name: "passing across every page",
			checkResponse: `[{"total_count":2,"check_runs":[{"id":1,"name":"unit","status":"completed","conclusion":"success"}]},` +
				`{"total_count":2,"check_runs":[{"id":2,"name":"optional","status":"completed","conclusion":"neutral"}]}]`,
			statusResponse: `[{"state":"pending","total_count":0,"statuses":[]}]`,
			want:           "passing (2/2)",
		},
		{
			name:           "failing check run",
			checkResponse:  `[{"total_count":1,"check_runs":[{"id":1,"name":"unit","status":"completed","conclusion":"failure"}]}]`,
			statusResponse: `[{"state":"pending","total_count":0,"statuses":[]}]`,
			want:           "failing (1/1)",
		},
		{
			name:           "pending check run",
			checkResponse:  `[{"total_count":1,"check_runs":[{"id":1,"name":"unit","status":"in_progress","conclusion":""}]}]`,
			statusResponse: `[{"state":"pending","total_count":0,"statuses":[]}]`,
			want:           "pending (1/1)",
		},
		{
			name:           "legacy status-only failure",
			checkResponse:  `[{"total_count":0,"check_runs":[]}]`,
			statusResponse: `[{"state":"failure","total_count":1,"statuses":[{"id":3,"context":"legacy/ci","state":"failure"}]}]`,
			want:           "failing (1/1)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			run := func(name string, args ...string) ([]byte, error) {
				calls++
				if name != "gh" || !hasPRArgPrefix(args, "api", "--paginate", "--slurp") || !hasPRArgPair(args, "--hostname", "github.example") {
					return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
				}
				switch {
				case hasPRArg(args, "repos/m31labs/buckley/commits/base-sha/check-runs?filter=latest&per_page=100"):
					return []byte(tt.checkResponse), nil
				case hasPRArg(args, "repos/m31labs/buckley/commits/base-sha/status?per_page=100"):
					return []byte(tt.statusResponse), nil
				default:
					return nil, fmt.Errorf("unexpected endpoint: %s", strings.Join(args, " "))
				}
			}

			selection, err := selectPRCIEvidence(run, pr, []string{"CHANGELOG.md", "docs/release.md"}, true, nil)
			if err != nil {
				t.Fatalf("selectPRCIEvidence: %v", err)
			}
			if calls != 2 {
				t.Fatalf("base CI evidence calls = %d, want check-runs plus commit status", calls)
			}
			if selection.Source != prCISourceBase || selection.Revision != "base-sha" {
				t.Fatalf("selection provenance = %#v", selection)
			}
			if got := summarizePRChecks(selection.Checks); got != tt.want {
				t.Fatalf("summarizePRChecks = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetCommitStatuses_UsesLatestCombinedContextState(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		if name != "gh" || !hasPRArgPrefix(args, "api", "--paginate", "--slurp") ||
			!hasPRArg(args, "repos/m31labs/buckley/commits/base-sha/status?per_page=100") ||
			!hasPRArgPair(args, "--hostname", "github.com") {
			return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
		}
		// GitHub's combined-status endpoint exposes the latest status for each
		// context, so this successful rerun is authoritative over older failures.
		return []byte(`[{"state":"success","total_count":1,"statuses":[{"id":9,"context":"legacy/ci","state":"success"}]}]`), nil
	}
	checks, err := getCommitStatuses(run, "github.com", "m31labs/buckley", "base-sha")
	if err != nil {
		t.Fatalf("getCommitStatuses: %v", err)
	}
	if got := summarizePRChecks(checks); got != "passing (1/1)" {
		t.Fatalf("successful latest rerun summarized as %q", got)
	}
}

func TestSelectPRCIEvidence_HeadChecksAndMixedDiffNeverUseBase(t *testing.T) {
	pr := &PRInfo{
		Host:       "github.example",
		Repository: "m31labs/buckley",
		BaseSHA:    "base-sha",
		HeadSHA:    "head-sha",
	}
	run := func(name string, args ...string) ([]byte, error) {
		return nil, fmt.Errorf("base check-runs must not be fetched: %s %s", name, strings.Join(args, " "))
	}

	t.Run("head checks take precedence", func(t *testing.T) {
		head := []PRCheck{{Name: "docs", Status: "SUCCESS"}}
		selection, err := selectPRCIEvidence(run, pr, []string{"README.md"}, true, head)
		if err != nil {
			t.Fatalf("selectPRCIEvidence: %v", err)
		}
		if selection.Source != prCISourceHead || selection.Revision != "head-sha" || len(selection.Checks) != 1 {
			t.Fatalf("selection = %#v, want head checks", selection)
		}
	})

	t.Run("mixed diff rejects fallback", func(t *testing.T) {
		selection, err := selectPRCIEvidence(run, pr, []string{"README.md", "main.go"}, true, nil)
		if err != nil {
			t.Fatalf("selectPRCIEvidence: %v", err)
		}
		if selection.Source != prCISourceHead || selection.Revision != "head-sha" || len(selection.Checks) != 0 {
			t.Fatalf("selection = %#v, want empty head evidence", selection)
		}
	})

	t.Run("incomplete file pagination rejects fallback", func(t *testing.T) {
		selection, err := selectPRCIEvidence(run, pr, []string{"README.md"}, false, nil)
		if err != nil {
			t.Fatalf("selectPRCIEvidence: %v", err)
		}
		if selection.Source != prCISourceHead {
			t.Fatalf("selection = %#v, want empty head evidence", selection)
		}
	})
}

func TestGetCommitCheckRuns_RejectsPaginationCardinalityDrift(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     string
	}{
		{
			name:     "missing page",
			response: `[{"total_count":2,"check_runs":[{"id":1,"name":"unit","status":"completed","conclusion":"success"}]}]`,
			want:     "cardinality mismatch",
		},
		{
			name: "total changes between pages",
			response: `[{"total_count":2,"check_runs":[{"id":1,"name":"unit"}]},` +
				`{"total_count":3,"check_runs":[{"id":2,"name":"lint"}]}]`,
			want: "total_count changed across pages",
		},
		{
			name: "duplicate page",
			response: `[{"total_count":2,"check_runs":[{"id":1,"name":"unit"}]},` +
				`{"total_count":2,"check_runs":[{"id":1,"name":"unit"}]}]`,
			want: "duplicate check-run id 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := func(string, ...string) ([]byte, error) { return []byte(tt.response), nil }
			_, err := getCommitCheckRuns(run, "github.com", "m31labs/buckley", "base-sha")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("getCommitCheckRuns error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestBuildPRPrompt_SurfacesImmutableBaseCIProvenance(t *testing.T) {
	prompt := BuildPRPrompt(&PRContext{
		PR: &PRInfo{
			Number:       219,
			Title:        "Release notes",
			CIStatus:     "passing (2/2)",
			BaseSHA:      "base-sha",
			HeadSHA:      "head-sha",
			ChangedFiles: 1,
		},
		CIProvenance: prCISourceBase,
		CIRevision:   "base-sha",
	})
	if !strings.Contains(prompt, "**CI Evidence**: immutable base @ base-sha") {
		t.Fatalf("prompt omitted inherited CI provenance:\n%s", prompt)
	}
}

func TestAssemblePRContext_BuildPromptIncludesReviewEvidence(t *testing.T) {
	diff := oversizedReviewDiff()
	run := func(name string, args ...string) ([]byte, error) {
		switch {
		case name == "gh" && hasPRArgPrefix(args, "pr", "view", "208", "--json") && strings.Contains(args[len(args)-1], "headRefOid"):
			return []byte(`{
  "number": 208,
  "title": "Ratchet generated grammar memory",
  "author": {"login": "author"},
  "state": "OPEN",
  "url": "https://github.com/m31labs/buckley/pull/208",
  "body": "Keep the cleanup path deterministic.",
  "labels": [{"name": "review"}],
  "baseRefName": "main",
  "baseRefOid": "1111111111111111111111111111111111111111",
  "headRefName": "cleanup-ratchet",
  "headRefOid": "2222222222222222222222222222222222222222",
  "reviewDecision": "REVIEW_REQUIRED",
  "additions": 72,
  "deletions": 11,
  "changedFiles": 4
}`), nil
		case name == "gh" && hasPRArgPrefix(args, "pr", "checks", "208", "--json", "state"):
			return []byte(`[{"state":"SUCCESS"}]`), nil
		case name == "gh" && hasPRArgPrefix(args, "pr", "diff", "208"):
			return []byte(diff), nil
		case name == "gh" && hasPRArgPrefix(args, "pr", "checks", "208", "--json", "name,state"):
			return []byte(`[{"name":"unit","state":"SUCCESS"}]`), nil
		case name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") && hasPRArg(args, "repos/m31labs/buckley/issues/208/comments?per_page=100"):
			return []byte(`[[{"id":"IC_top_1","user":{"login":"maintainer"},"body":"Please keep the max ratchet at zero."}]]`), nil
		case name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") && hasPRArg(args, "repos/m31labs/buckley/pulls/208/reviews?per_page=100"):
			return []byte(`[[{"id":"PRR_review_1","user":{"login":"reviewer"},"body":"The cleanup path still needs explicit coverage.","state":"COMMENTED","submitted_at":"2026-07-10T12:00:00Z"}]]`), nil
		case name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "graphql":
			return []byte(`{
  "data": {
    "repository": {
      "pullRequest": {
        "reviewThreads": {
          "pageInfo": {"hasNextPage": false, "endCursor": ""},
          "nodes": [
            {
			  "id": "PRRT_thread_1",
              "isResolved": false,
              "isOutdated": false,
              "path": "pkg/oneshot/commands/review_pr_context.go",
              "line": 208,
              "startLine": 208,
              "originalLine": 208,
              "comments": {
                "pageInfo": {"hasNextPage": false},
				"nodes": [{"id":"PRRC_inline_1","author":{"login":"reviewer"},"body":"Register cleanup immediately so an early return cannot leak the worktree.","path":"pkg/oneshot/commands/review_pr_context.go","line":208,"startLine":208,"originalLine":208}]
              }
            },
            {
              "isResolved": true,
              "isOutdated": false,
              "path": "pkg/oneshot/commands/review_pr_context.go",
              "line": 99,
              "startLine": 99,
              "originalLine": 99,
              "comments": {
                "pageInfo": {"hasNextPage": false},
                "nodes": [{"author":{"login":"reviewer"},"body":"Already fixed; do not reopen this historical thread.","path":"pkg/oneshot/commands/review_pr_context.go","line":99,"startLine":99,"originalLine":99}]
              }
            }
          ]
        }
      }
    }
  }
}`), nil
		case name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") && hasPRArg(args, "repos/m31labs/buckley/pulls/208/files?per_page=100"):
			return []byte(`[[{"filename":"pkg/oneshot/commands/review_pr_context.go"},{"filename":"pkg/cleanup.go"}],[{"filename":"pkg/ratchet.go"},{"filename":"pkg/ratchet_test.go"}]]`), nil
		case name == "git" && matchesPRArgs(args, "--no-pager", "rev-parse", "--show-toplevel"):
			return []byte("/fixture/repo\n"), nil
		case name == "git" && matchesPRArgs(args, "--no-pager", "-C", "/fixture/repo", "rev-parse", "HEAD"):
			return []byte("2222222222222222222222222222222222222222\n"), nil
		case name == "git" && matchesPRArgs(args, "--no-pager", "-C", "/fixture/repo", "status", "--porcelain"):
			return nil, nil
		case name == "git" && matchesPRArgs(args, "--no-pager", "-C", "/fixture/repo", "ls-tree", "2222222222222222222222222222222222222222", "--", "AGENTS.md"):
			return []byte("100644 blob aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\tAGENTS.md\n"), nil
		case name == "git" && matchesPRArgs(args, "--no-pager", "-C", "/fixture/repo", "show", "2222222222222222222222222222222222222222:AGENTS.md"):
			return []byte("## Review rules\n- Never weaken ratchets.\n"), nil
		case name == "git" && hasPRArgPrefix(args, "--no-pager", "-C", "/fixture/repo", "ls-tree", "2222222222222222222222222222222222222222", "--") && strings.Contains(args[len(args)-1], "/AGENTS.md"):
			return nil, nil
		default:
			return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
		}
	}
	ctx, audit, err := assemblePRContext("208", prContextDependencies{run: run})
	if err != nil {
		t.Fatalf("assemblePRContext: %v", err)
	}
	prompt := BuildPRPrompt(ctx)

	for _, expected := range []string{
		"m31labs/buckley",
		"1111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222",
		"Local Verification Checkout",
		"REVIEW_REQUIRED",
		"Please keep the max ratchet at zero.",
		"The cleanup path still needs explicit coverage.",
		"Register cleanup immediately so an early return cannot leak the worktree.",
		"`top-level-comment:IC_top_1`",
		"`submitted-review:PRR_review_1`",
		"`inline-thread:PRRT_thread_1/comment:PRRC_inline_1`",
		"`pkg/oneshot/commands/review_pr_context.go:208`",
		"[unresolved]",
		"1 resolved thread filtered",
		"## Project Guidelines (applicable AGENTS.md chain)",
		"Never weaken ratchets.",
		"**PR diff**: truncated",
		"... (truncated)",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("prompt missing %q", expected)
		}
	}
	if strings.Contains(prompt, "Already fixed; do not reopen this historical thread.") {
		t.Error("resolved inline thread body should be filtered from the prompt")
	}
	if ctx.ResolvedThreadsFiltered != 1 {
		t.Fatalf("ResolvedThreadsFiltered = %d, want 1", ctx.ResolvedThreadsFiltered)
	}
	if !ctx.HasIncompleteContext() {
		t.Fatal("truncated diff should make authoritative context incomplete")
	}
	if !ctx.HasReviewFeedback() {
		t.Fatal("fixture should report review feedback")
	}
	wantFeedbackIDs := []string{
		"top-level-comment:IC_top_1",
		"submitted-review:PRR_review_1",
		"inline-thread:PRRT_thread_1/comment:PRRC_inline_1",
	}
	if got := ctx.RequiredFeedbackIDs(); fmt.Sprint(got) != fmt.Sprint(wantFeedbackIDs) {
		t.Fatalf("RequiredFeedbackIDs() = %v, want %v", got, wantFeedbackIDs)
	}
	if len(ctx.InlineComments) != 1 || !ctx.InlineComments[0].ResolutionKnown || ctx.InlineComments[0].Resolved {
		t.Fatalf("unexpected unresolved inline context: %#v", ctx.InlineComments)
	}
	if !audit.HasTruncation() {
		t.Fatal("context audit should expose diff truncation")
	}
	assertAuditSource(t, audit, "submitted reviews", false)
	assertAuditSource(t, audit, "inline review comments", false)
	assertAuditSource(t, audit, "AGENTS.md", false)
	assertAuditSource(t, audit, "PR diff", true)
}

func TestAssemblePRContext_FallbackAndFetchFailuresAreVisible(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		switch {
		case name == "gh" && hasPRArgPrefix(args, "pr", "view", "208", "--json") && strings.Contains(args[len(args)-1], "headRefOid"):
			return []byte(`{"number":208,"title":"Fallback coverage","author":{"login":"author"},"state":"OPEN","url":"https://github.com/m31labs/buckley/pull/208","baseRefName":"main","baseRefOid":"base-sha","headRefName":"topic","headRefOid":"head-sha","reviewDecision":"","additions":1,"deletions":0,"changedFiles":1}`), nil
		case name == "gh" && hasPRArgPrefix(args, "pr", "checks", "208", "--json", "state"):
			return []byte(`[]`), nil
		case name == "gh" && hasPRArgPrefix(args, "pr", "diff", "208"):
			return []byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"), nil
		case name == "gh" && hasPRArgPrefix(args, "pr", "checks", "208", "--json", "name,state"):
			return nil, errors.New("checks API unavailable")
		case name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") && hasPRArg(args, "repos/m31labs/buckley/issues/208/comments?per_page=100"):
			return nil, errors.New("comments API unavailable")
		case name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") && hasPRArg(args, "repos/m31labs/buckley/pulls/208/reviews?per_page=100"):
			return []byte(`[]`), nil
		case name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "graphql":
			return nil, errors.New("reviewThreads field denied")
		case name == "gh" && len(args) >= 3 && args[0] == "api" && args[1] == "--paginate" && hasPRArg(args, "repos/m31labs/buckley/pulls/208/comments?per_page=100"):
			return []byte(`[[{"user":{"login":"fallback-reviewer"},"body":"Fallback still carries the cleanup finding.","path":"pkg/cleanup.go","line":0,"original_line":77}]]`), nil
		case name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") && hasPRArg(args, "repos/m31labs/buckley/pulls/208/files?per_page=100"):
			return nil, errors.New("files API unavailable")
		case name == "git" && matchesPRArgs(args, "--no-pager", "rev-parse", "--show-toplevel"):
			return nil, errors.New("not in a worktree")
		default:
			return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
		}
	}

	ctx, audit, err := assemblePRContext("208", prContextDependencies{run: run})
	if err != nil {
		t.Fatalf("assemblePRContext: %v", err)
	}
	prompt := BuildPRPrompt(ctx)
	for _, expected := range []string{
		"**CI checks**: fetch failed — checks API unavailable",
		"**Top-level comments**: fetch failed — comments API unavailable",
		"**Inline review threads**: fallback",
		"GraphQL failed: reviewThreads field denied",
		"Fallback still carries the cleanup finding.",
		"`pkg/cleanup.go:77`",
		"[resolution unknown; fallback context]",
		"**Changed files**: fetch failed — files API unavailable",
		"**Root AGENTS.md**: fetch failed — repository root: not in a worktree",
	} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("prompt missing %q", expected)
		}
	}
	assertAuditSource(t, audit, "CI checks (fetch failed)", false)
	assertAuditSource(t, audit, "inline review comments (REST fallback)", false)
	assertAuditSource(t, audit, "Root AGENTS.md (fetch failed)", false)
}

func TestPRContextCompletenessRejectsCheckoutMismatch(t *testing.T) {
	ctx := &PRContext{ContextStatus: []PRContextStatus{
		{Source: "PR diff", Status: "complete"},
		{Source: "Root AGENTS.md", Status: "missing"},
		{Source: "Local verification checkout", Status: "mismatch"},
	}}
	if !ctx.HasIncompleteContext() {
		t.Fatal("checkout mismatch must prevent an authoritative clean verdict")
	}
}

func TestReadPRHeadFileRejectsTrackedSymlink(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		if name == "git" && matchesPRArgs(args, "--no-pager", "-C", "/repo", "ls-tree", "head-sha", "--", "AGENTS.md") {
			return []byte("120000 blob aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\tAGENTS.md\n"), nil
		}
		return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
	}
	if _, _, err := readPRHeadFile(run, "/repo", "head-sha", "AGENTS.md", 10_000); err == nil || !strings.Contains(err.Error(), "refusing to follow tracked symlink") {
		t.Fatalf("readPRHeadFile error = %v, want symlink rejection", err)
	}
}

func TestAppendNestedPRAgentsUsesImmutableApplicableChain(t *testing.T) {
	ctx := &PRContext{
		PR:    &PRInfo{HeadSHA: "head-sha"},
		Files: []string{"src/deep/code.go", "src/other.go"},
	}
	run := func(name string, args ...string) ([]byte, error) {
		if name != "git" || !hasPRArgPrefix(args, "--no-pager", "-C", "/repo") {
			return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
		}
		switch {
		case matchesPRArgs(args, "--no-pager", "-C", "/repo", "ls-tree", "head-sha", "--", "src/AGENTS.md"):
			return []byte("100644 blob aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\tsrc/AGENTS.md\n"), nil
		case matchesPRArgs(args, "--no-pager", "-C", "/repo", "show", "head-sha:src/AGENTS.md"):
			return []byte("src rules\n"), nil
		case matchesPRArgs(args, "--no-pager", "-C", "/repo", "ls-tree", "head-sha", "--", "src/deep/AGENTS.md"):
			return []byte("100644 blob bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\tsrc/deep/AGENTS.md\n"), nil
		case matchesPRArgs(args, "--no-pager", "-C", "/repo", "show", "head-sha:src/deep/AGENTS.md"):
			return []byte("deep rules\n"), nil
		default:
			return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
		}
	}
	audit := transparency.NewContextAudit()
	appendNestedPRAgentsContext(ctx, audit, run, "/repo", 10_000)
	if !strings.Contains(ctx.AgentsMD, "### src/AGENTS.md\n\nsrc rules") || !strings.Contains(ctx.AgentsMD, "### src/deep/AGENTS.md\n\ndeep rules") {
		t.Fatalf("applicable instruction chain = %q", ctx.AgentsMD)
	}
	if ctx.HasIncompleteContext() {
		t.Fatalf("complete nested guidance was marked incomplete: %#v", ctx.ContextStatus)
	}
}

func TestAssemblePRContext_MarksConcurrentHeadPushIncomplete(t *testing.T) {
	metadataCalls := 0
	run := func(name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case name == "gh" && hasPRArgPrefix(args, "pr", "view", "208", "--json") && strings.Contains(joined, "baseRefOid"):
			metadataCalls++
			head := "initial-head"
			if metadataCalls > 1 {
				head = "pushed-head"
			}
			return []byte(fmt.Sprintf(`{"number":208,"title":"Concurrent push","author":{"login":"author"},"state":"OPEN","url":"https://github.com/m31labs/buckley/pull/208","baseRefName":"main","baseRefOid":"base-sha","headRefName":"topic","headRefOid":%q,"changedFiles":1}`, head)), nil
		case name == "gh" && hasPRArgPrefix(args, "pr", "checks", "208", "--json", "state"):
			return []byte(`[]`), nil
		case name == "gh" && hasPRArgPrefix(args, "pr", "diff", "208"):
			return []byte("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n"), nil
		case name == "gh" && hasPRArgPrefix(args, "pr", "checks", "208", "--json", "name,state"):
			return []byte(`[]`), nil
		case name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") && hasPRArg(args, "repos/m31labs/buckley/issues/208/comments?per_page=100"):
			return []byte(`[]`), nil
		case name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") && hasPRArg(args, "repos/m31labs/buckley/pulls/208/reviews?per_page=100"):
			return []byte(`[]`), nil
		case name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "graphql":
			return []byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}`), nil
		case name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") && hasPRArg(args, "repos/m31labs/buckley/pulls/208/files?per_page=100"):
			return []byte(`[[{"filename":"a.go"}]]`), nil
		case name == "git" && matchesPRArgs(args, "--no-pager", "rev-parse", "--show-toplevel"):
			return []byte("/fixture/repo\n"), nil
		case name == "git" && matchesPRArgs(args, "--no-pager", "-C", "/fixture/repo", "rev-parse", "HEAD"):
			return []byte("initial-head\n"), nil
		case name == "git" && matchesPRArgs(args, "--no-pager", "-C", "/fixture/repo", "status", "--porcelain"):
			return nil, nil
		case name == "git" && matchesPRArgs(args, "--no-pager", "-C", "/fixture/repo", "ls-tree", "initial-head", "--", "AGENTS.md"):
			return []byte("100644 blob aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\tAGENTS.md\n"), nil
		case name == "git" && matchesPRArgs(args, "--no-pager", "-C", "/fixture/repo", "show", "initial-head:AGENTS.md"):
			return []byte("Review all changed files.\n"), nil
		default:
			return nil, fmt.Errorf("unexpected command: %s %s", name, joined)
		}
	}
	ctx, audit, err := assemblePRContext("208", prContextDependencies{run: run})
	if err != nil {
		t.Fatalf("assemblePRContext: %v", err)
	}
	if metadataCalls != 2 {
		t.Fatalf("metadata calls = %d, want initial capture plus end-of-assembly revalidation", metadataCalls)
	}
	if !ctx.HasIncompleteContext() {
		t.Fatal("a concurrent head push must make assembled review context incomplete")
	}
	var revalidation *PRContextStatus
	for i := range ctx.ContextStatus {
		if ctx.ContextStatus[i].Source == "PR evidence revalidation" {
			revalidation = &ctx.ContextStatus[i]
			break
		}
	}
	if revalidation == nil || revalidation.Status != "changed" ||
		!strings.Contains(revalidation.Detail, `head revision "initial-head" -> "pushed-head"`) {
		t.Fatalf("PR metadata revalidation status = %#v", revalidation)
	}
	if !strings.Contains(BuildPRPrompt(ctx), "PR state moved while evidence was fetched") {
		t.Fatal("prompt must expose mixed-head evidence to the reviewer")
	}
	assertAuditSource(t, audit, "PR evidence changed during assembly", false)

	changed, err := revalidatePRContext(ctx, func(name string, args ...string) ([]byte, error) {
		if name != "gh" || !strings.Contains(strings.Join(args, " "), "headRefOid") {
			return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
		}
		return []byte(`{"number":208,"url":"https://github.com/m31labs/buckley/pull/208","baseRefName":"main","baseRefOid":"base-sha","headRefName":"topic","headRefOid":"pushed-head"}`), nil
	})
	if err != nil {
		t.Fatalf("revalidatePRContext: %v", err)
	}
	if !strings.Contains(changed, `head revision "initial-head" -> "pushed-head"`) {
		t.Fatalf("post-model revision change = %q", changed)
	}
}

func TestRevalidatePRContext_HeadStableCIFailureInvalidatesEvidence(t *testing.T) {
	ctx := stablePRRevalidationContext()
	run := stablePRRevalidationRunner(prRevalidationOutputs{
		checks: `[{"name":"unit","state":"FAILURE"}]`,
	})

	changed, err := revalidatePRContext(ctx, run)
	if err != nil {
		t.Fatalf("revalidatePRContext: %v", err)
	}
	for _, expected := range []string{`CI status "passing (1/1)" -> "failing (1/1)"`, "CI check outcomes changed"} {
		if !strings.Contains(changed, expected) {
			t.Errorf("changed evidence %q missing %q", changed, expected)
		}
	}
	if strings.Contains(changed, "head revision") {
		t.Fatalf("fixture head should remain stable, got %q", changed)
	}
}

func TestRevalidatePRContext_InheritedBaseCIYieldsToNewHeadChecks(t *testing.T) {
	ctx := stablePRRevalidationContext()
	ctx.CIProvenance = prCISourceBase
	ctx.CIRevision = "base-sha"

	changed, err := revalidatePRContext(ctx, stablePRRevalidationRunner(prRevalidationOutputs{}))
	if err != nil {
		t.Fatalf("revalidatePRContext: %v", err)
	}
	for _, expected := range []string{
		`CI provenance "immutable base" -> "pull request head"`,
		`CI revision "base-sha" -> "head-sha"`,
	} {
		if !strings.Contains(changed, expected) {
			t.Errorf("changed evidence %q missing %q", changed, expected)
		}
	}
}

func TestRevalidatePRContext_StableInheritedBaseCIRemainsSnapshotBound(t *testing.T) {
	ctx := stablePRRevalidationContext()
	ctx.Checks = []PRCheck{{Name: "unit", Status: "completed", Conclusion: "success"}}
	ctx.CIProvenance = prCISourceBase
	ctx.CIRevision = "base-sha"
	base := stablePRRevalidationRunner(prRevalidationOutputs{checks: `[]`})
	baseCheckCalls := 0
	run := func(name string, args ...string) ([]byte, error) {
		if name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") &&
			hasPRArg(args, "repos/m31labs/buckley/commits/base-sha/check-runs?filter=latest&per_page=100") {
			baseCheckCalls++
			if !hasPRArgPair(args, "--hostname", "github.com") {
				return nil, fmt.Errorf("base check-runs omitted explicit host: %s", strings.Join(args, " "))
			}
			return []byte(`[{"total_count":1,"check_runs":[{"id":1,"name":"unit","status":"completed","conclusion":"success"}]}]`), nil
		}
		if name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") &&
			hasPRArg(args, "repos/m31labs/buckley/commits/base-sha/status?per_page=100") {
			if !hasPRArgPair(args, "--hostname", "github.com") {
				return nil, fmt.Errorf("base commit status omitted explicit host: %s", strings.Join(args, " "))
			}
			return []byte(`[{"state":"pending","total_count":0,"statuses":[]}]`), nil
		}
		return base(name, args...)
	}

	changed, err := revalidatePRContext(ctx, run)
	if err != nil {
		t.Fatalf("revalidatePRContext: %v", err)
	}
	if changed != "" {
		t.Fatalf("stable inherited base CI changed evidence = %q", changed)
	}
	if baseCheckCalls != 1 {
		t.Fatalf("base check-runs revalidation calls = %d, want 1", baseCheckCalls)
	}
}

func TestRevalidatePRContext_InheritedBaseCIFailsClosedOnMetadataDrift(t *testing.T) {
	ctx := stablePRRevalidationContext()
	ctx.CIProvenance = prCISourceBase
	ctx.CIRevision = "base-sha"
	calls := 0
	run := func(name string, args ...string) ([]byte, error) {
		calls++
		if name != "gh" || !hasPRArgPrefix(args, "pr", "view", "208", "--json") {
			return nil, fmt.Errorf("unexpected command after metadata drift: %s %s", name, strings.Join(args, " "))
		}
		return []byte(`{"number":208,"url":"https://github.com/m31labs/buckley/pull/208","baseRefName":"main","baseRefOid":"moved-base","headRefName":"topic","headRefOid":"head-sha","reviewDecision":"REVIEW_REQUIRED"}`), nil
	}

	changed, err := revalidatePRContext(ctx, run)
	if err != nil {
		t.Fatalf("revalidatePRContext: %v", err)
	}
	if calls != 1 {
		t.Fatalf("commands after base metadata drift = %d, want metadata-only revalidation", calls)
	}
	if !strings.Contains(changed, `base revision "base-sha" -> "moved-base"`) {
		t.Fatalf("changed evidence = %q", changed)
	}
}

func TestRevalidatePRContext_PendingChecksExitEightRemainsReviewable(t *testing.T) {
	ctx := stablePRRevalidationContext()
	ctx.PR.CIStatus = "pending (1/1)"
	ctx.Checks = []PRCheck{{Name: "unit", Status: "PENDING"}}
	base := stablePRRevalidationRunner(prRevalidationOutputs{
		checks: `[{"name":"unit","state":"PENDING"}]`,
	})
	run := func(name string, args ...string) ([]byte, error) {
		output, err := base(name, args...)
		if err == nil && name == "gh" && hasPRArgPrefix(args, "pr", "checks", "208", "--json", "name,state") {
			return normalizePRCommandResult(name, args, output, reviewCommandExitError{code: 8})
		}
		return output, err
	}

	changed, err := revalidatePRContext(ctx, run)
	if err != nil {
		t.Fatalf("revalidatePRContext: %v", err)
	}
	if changed != "" {
		t.Fatalf("pending CI changed evidence = %q, want stable", changed)
	}
}

func TestRevalidatePRContext_StableNoChecksExitOneRemainsReviewable(t *testing.T) {
	ctx := stablePRRevalidationContext()
	ctx.PR.CIStatus = "no checks"
	ctx.Checks = nil
	base := stablePRRevalidationRunner(prRevalidationOutputs{checks: `[]`})
	run := func(name string, args ...string) ([]byte, error) {
		output, err := base(name, args...)
		if err == nil && name == "gh" && hasPRArgPrefix(args, "pr", "checks", "208", "--json", "name,state") {
			return normalizePRCommandResult(name, args, []byte("no checks reported on the 'release/v0.23.0' branch\n"), reviewCommandExitError{code: 1})
		}
		return output, err
	}

	changed, err := revalidatePRContext(ctx, run)
	if err != nil {
		t.Fatalf("revalidatePRContext: %v", err)
	}
	if changed != "" {
		t.Fatalf("no-check CI changed evidence = %q, want stable", changed)
	}
}

func TestRevalidatePRContext_FeedbackChangesInvalidateEvidence(t *testing.T) {
	tests := []struct {
		name     string
		outputs  prRevalidationOutputs
		expected []string
	}{
		{
			name: "new top-level comment",
			outputs: prRevalidationOutputs{
				comments: `[[{"id":"IC_top_1","user":{"login":"maintainer"},"body":"Keep the ratchet."},{"id":"IC_top_2","user":{"login":"reviewer"},"body":"New evidence arrived."}]]`,
			},
			expected: []string{"top-level comment IDs changed", "top-level comment content changed"},
		},
		{
			name: "edited submitted review",
			outputs: prRevalidationOutputs{
				reviews: `[[{"id":"PRR_review_1","user":{"login":"reviewer"},"body":"Edited after the model reviewed it.","state":"COMMENTED","submitted_at":"2026-07-10T12:00:00Z"}]]`,
			},
			expected: []string{"submitted review content or state changed"},
		},
		{
			name: "unresolved inline thread resolved",
			outputs: prRevalidationOutputs{
				inline: `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"PRRT_thread_1","isResolved":true,"isOutdated":false,"path":"pkg/ratchet.go","line":12,"startLine":12,"originalLine":12,"comments":{"pageInfo":{"hasNextPage":false},"nodes":[{"id":"PRRC_inline_1","author":{"login":"reviewer"},"body":"Keep the ratchet.","path":"pkg/ratchet.go","line":12,"startLine":12,"originalLine":12}]}}]}}}}}`,
			},
			expected: []string{"unresolved inline feedback IDs changed", "inline feedback content or resolution changed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed, err := revalidatePRContext(stablePRRevalidationContext(), stablePRRevalidationRunner(tt.outputs))
			if err != nil {
				t.Fatalf("revalidatePRContext: %v", err)
			}
			for _, expected := range tt.expected {
				if !strings.Contains(changed, expected) {
					t.Errorf("changed evidence %q missing %q", changed, expected)
				}
			}
		})
	}
}

func TestRevalidatePRContext_ReviewDecisionChangeAndFetchFailureFailClosed(t *testing.T) {
	t.Run("review decision changed", func(t *testing.T) {
		changed, err := revalidatePRContext(stablePRRevalidationContext(), stablePRRevalidationRunner(prRevalidationOutputs{
			reviewDecision: "CHANGES_REQUESTED",
		}))
		if err != nil {
			t.Fatalf("revalidatePRContext: %v", err)
		}
		if !strings.Contains(changed, `review decision "REVIEW_REQUIRED" -> "CHANGES_REQUESTED"`) {
			t.Fatalf("review decision change = %q", changed)
		}
	})

	t.Run("feedback fetch unavailable", func(t *testing.T) {
		base := stablePRRevalidationRunner(prRevalidationOutputs{})
		run := func(name string, args ...string) ([]byte, error) {
			if name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") && hasPRArg(args, "repos/m31labs/buckley/pulls/208/reviews?per_page=100") {
				return nil, errors.New("reviews API unavailable")
			}
			return base(name, args...)
		}
		changed, err := revalidatePRContext(stablePRRevalidationContext(), run)
		if err == nil || !strings.Contains(err.Error(), "submitted reviews: reviews API unavailable") {
			t.Fatalf("revalidation error = %v, changed = %q", err, changed)
		}
	})
}

func TestGetCIStatus_AllSkippedOrNeutralIsNotPassing(t *testing.T) {
	tests := []struct {
		name   string
		checks string
		want   string
	}{
		{name: "no checks", checks: `[]`, want: "no checks"},
		{name: "all non-authoritative", checks: `[{"name":"docs","state":"NEUTRAL"},{"name":"optional","state":"SKIPPED"}]`, want: "no checks"},
		{name: "success plus skipped", checks: `[{"name":"unit","state":"SUCCESS"},{"name":"optional","state":"SKIPPED"}]`, want: "passing (2/2)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := func(name string, args ...string) ([]byte, error) {
				if name != "gh" || !hasPRArgPrefix(args, "pr", "checks", "208", "--json", "name,state") {
					return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
				}
				return []byte(tt.checks), nil
			}
			if got := getCIStatus(run, prReference{Number: 208}); got != tt.want {
				t.Fatalf("getCIStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPaginatedPRFeedbackReadsEveryRESTPage(t *testing.T) {
	target := prReference{Number: 208, Host: "github.example", Repository: "m31labs/buckley"}
	run := func(name string, args ...string) ([]byte, error) {
		if name != "gh" || !hasPRArgPrefix(args, "api", "--paginate", "--slurp") || !hasPRArgPair(args, "--hostname", "github.example") {
			return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
		}
		switch {
		case hasPRArg(args, "repos/m31labs/buckley/issues/208/comments?per_page=100"):
			return []byte(`[[{"id":1,"user":{"login":"one"},"body":"first"}],[{"id":2,"user":{"login":"two"},"body":"second"}]]`), nil
		case hasPRArg(args, "repos/m31labs/buckley/pulls/208/reviews?per_page=100"):
			return []byte(`[[{"id":3,"user":{"login":"three"},"body":"third","state":"COMMENTED","submitted_at":"2026-07-10T12:00:00Z"}],[{"id":4,"user":{"login":"four"},"body":"fourth","state":"APPROVED","submitted_at":"2026-07-10T13:00:00Z"}]]`), nil
		default:
			return nil, fmt.Errorf("unexpected endpoint: %s", strings.Join(args, " "))
		}
	}
	comments, err := getPRComments(run, target)
	if err != nil || len(comments) != 2 || comments[0].ID != "1" || comments[1].ID != "2" {
		t.Fatalf("paginated comments = %#v, err=%v", comments, err)
	}
	reviews, err := getPRReviews(run, target)
	if err != nil || len(reviews) != 2 || reviews[0].ID != "3" || reviews[1].ID != "4" {
		t.Fatalf("paginated reviews = %#v, err=%v", reviews, err)
	}
}

type prRevalidationOutputs struct {
	checks         string
	comments       string
	reviews        string
	inline         string
	reviewDecision string
}

func stablePRRevalidationContext() *PRContext {
	return &PRContext{
		PR: &PRInfo{
			Number:         208,
			Host:           "github.com",
			Repository:     "m31labs/buckley",
			BaseBranch:     "main",
			BaseSHA:        "base-sha",
			HeadBranch:     "topic",
			HeadSHA:        "head-sha",
			ReviewDecision: "REVIEW_REQUIRED",
			CIStatus:       "passing (1/1)",
		},
		Checks:       []PRCheck{{Name: "unit", Status: "SUCCESS"}},
		CIProvenance: prCISourceHead,
		CIRevision:   "head-sha",
		Comments:     []PRComment{{ID: "IC_top_1", Author: "maintainer", Body: "Keep the ratchet."}},
		Reviews: []PRReview{{
			ID:          "PRR_review_1",
			Author:      "reviewer",
			Body:        "The cleanup path needs coverage.",
			State:       "COMMENTED",
			SubmittedAt: "2026-07-10T12:00:00Z",
		}},
		InlineComments: []PRComment{{
			ID:              "PRRC_inline_1",
			ThreadID:        "PRRT_thread_1",
			Author:          "reviewer",
			Body:            "Keep the ratchet.",
			Path:            "pkg/ratchet.go",
			Line:            12,
			StartLine:       12,
			OriginalLine:    12,
			ResolutionKnown: true,
		}},
		target: prReference{Number: 208, Host: "github.com", Repository: "m31labs/buckley"},
	}
}

func stablePRRevalidationRunner(overrides prRevalidationOutputs) prCommandRunner {
	checks := overrides.checks
	if checks == "" {
		checks = `[{"name":"unit","state":"SUCCESS"}]`
	}
	comments := overrides.comments
	if comments == "" {
		comments = `[[{"id":"IC_top_1","user":{"login":"maintainer"},"body":"Keep the ratchet."}]]`
	}
	reviews := overrides.reviews
	if reviews == "" {
		reviews = `[[{"id":"PRR_review_1","user":{"login":"reviewer"},"body":"The cleanup path needs coverage.","state":"COMMENTED","submitted_at":"2026-07-10T12:00:00Z"}]]`
	}
	inline := overrides.inline
	if inline == "" {
		inline = `{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"id":"PRRT_thread_1","isResolved":false,"isOutdated":false,"path":"pkg/ratchet.go","line":12,"startLine":12,"originalLine":12,"comments":{"pageInfo":{"hasNextPage":false},"nodes":[{"id":"PRRC_inline_1","author":{"login":"reviewer"},"body":"Keep the ratchet.","path":"pkg/ratchet.go","line":12,"startLine":12,"originalLine":12}]}}]}}}}}`
	}
	reviewDecision := overrides.reviewDecision
	if reviewDecision == "" {
		reviewDecision = "REVIEW_REQUIRED"
	}

	return func(name string, args ...string) ([]byte, error) {
		switch {
		case name == "gh" && hasPRArgPrefix(args, "pr", "view", "208", "--json") && strings.Contains(strings.Join(args, " "), "headRefOid"):
			return []byte(fmt.Sprintf(`{"number":208,"url":"https://github.com/m31labs/buckley/pull/208","baseRefName":"main","baseRefOid":"base-sha","headRefName":"topic","headRefOid":"head-sha","reviewDecision":%q}`, reviewDecision)), nil
		case name == "gh" && hasPRArgPrefix(args, "pr", "checks", "208", "--json", "name,state"):
			return []byte(checks), nil
		case name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") && hasPRArg(args, "repos/m31labs/buckley/issues/208/comments?per_page=100"):
			return []byte(comments), nil
		case name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") && hasPRArg(args, "repos/m31labs/buckley/pulls/208/reviews?per_page=100"):
			return []byte(reviews), nil
		case name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "graphql":
			return []byte(inline), nil
		default:
			return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
		}
	}
}

func TestEnterprisePRURLTargetsEveryGitHubOperation(t *testing.T) {
	const enterpriseHost = "github.corp.example:8443"
	target, err := parsePRRef("https://github.corp.example:8443/m31labs/buckley/pull/208/files?diff=split")
	if err != nil {
		t.Fatalf("parsePRRef: %v", err)
	}
	if target.Number != 208 || target.Host != enterpriseHost || target.Repository != "m31labs/buckley" {
		t.Fatalf("target = %#v, want PR 208 in %s/m31labs/buckley", target, enterpriseHost)
	}

	prCalls := 0
	apiCalls := 0
	run := func(name string, args ...string) ([]byte, error) {
		if name != "gh" || len(args) == 0 {
			return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
		}
		switch args[0] {
		case "pr":
			prCalls++
			if !hasPRArgPair(args, "--repo", enterpriseHost+"/m31labs/buckley") {
				t.Errorf("gh pr operation was not pinned to URL repository: %s", strings.Join(args, " "))
			}
			switch {
			case args[1] == "view" && strings.Contains(strings.Join(args, " "), "headRefOid"):
				return []byte(`{"number":208,"title":"Target integrity","author":{"login":"author"},"state":"OPEN","url":"https://wrong.example/wrong/repository/pull/208","baseRefName":"main","baseRefOid":"base","headRefName":"topic","headRefOid":"head","changedFiles":1}`), nil
			case args[1] == "checks" && hasPRArgPair(args, "--json", "state"):
				return []byte(`[]`), nil
			case args[1] == "diff":
				return []byte("diff --git a/a.go b/a.go\n"), nil
			case args[1] == "checks" && hasPRArgPair(args, "--json", "name,state"):
				return []byte(`[]`), nil
			}
		case "api":
			apiCalls++
			if !hasPRArgPair(args, "--hostname", enterpriseHost) {
				t.Errorf("gh api operation was not pinned to URL host: %s", strings.Join(args, " "))
			}
			if len(args) > 1 && args[1] == "graphql" {
				if !hasPRArgPair(args, "-F", "owner=m31labs") || !hasPRArgPair(args, "-F", "name=buckley") {
					t.Errorf("GraphQL operation was not pinned to URL repository: %s", strings.Join(args, " "))
				}
				return []byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}`), nil
			}
			if hasPRArg(args, "repos/m31labs/buckley/issues/208/comments?per_page=100") || hasPRArg(args, "repos/m31labs/buckley/pulls/208/reviews?per_page=100") {
				return []byte(`[]`), nil
			}
			if !hasPRArg(args, "repos/m31labs/buckley/pulls/208/files?per_page=100") {
				t.Errorf("REST operation was not pinned to URL repository: %s", strings.Join(args, " "))
			}
			return []byte(`[[{"filename":"a.go"}]]`), nil
		}
		return nil, fmt.Errorf("unexpected gh command: %s", strings.Join(args, " "))
	}

	pr, err := getPRInfo(run, target)
	if err != nil {
		t.Fatalf("getPRInfo: %v", err)
	}
	if pr.Host != enterpriseHost || pr.Repository != "m31labs/buckley" {
		t.Fatalf("PR target = %s/%s, want target preserved from input URL", pr.Host, pr.Repository)
	}
	if _, err := getPRDiff(run, target); err != nil {
		t.Fatalf("getPRDiff: %v", err)
	}
	if _, err := getPRChecks(run, target); err != nil {
		t.Fatalf("getPRChecks: %v", err)
	}
	if _, err := getPRComments(run, target); err != nil {
		t.Fatalf("getPRComments: %v", err)
	}
	if _, err := getPRReviews(run, target); err != nil {
		t.Fatalf("getPRReviews: %v", err)
	}
	if _, err := getPRInlineComments(run, pr); err != nil {
		t.Fatalf("getPRInlineComments: %v", err)
	}
	if _, err := getPRFiles(run, pr); err != nil {
		t.Fatalf("getPRFiles: %v", err)
	}
	if prCalls != 3 || apiCalls != 4 {
		t.Fatalf("observed %d gh pr calls and %d gh api calls, want 3 and 4", prCalls, apiCalls)
	}
}

func TestParsePRRef_NumericKeepsCurrentRepository(t *testing.T) {
	target, err := parsePRRef("208")
	if err != nil {
		t.Fatalf("parsePRRef: %v", err)
	}
	if target != (prReference{Number: 208}) {
		t.Fatalf("numeric target = %#v, want current-repository PR 208", target)
	}
}

func TestAssemblePRContext_ChangedFileCardinalityMismatchIsIncomplete(t *testing.T) {
	run := func(name string, args ...string) ([]byte, error) {
		if name == "gh" && hasPRArgPrefix(args, "pr") && len(args) > 1 && args[1] != "view" &&
			!hasPRArgPair(args, "--repo", "github.com/m31labs/buckley") {
			t.Errorf("post-metadata PR operation was not pinned to resolved repository: %s", strings.Join(args, " "))
		}
		switch {
		case name == "gh" && hasPRArgPrefix(args, "pr", "view", "208", "--json") && strings.Contains(args[len(args)-1], "headRefOid"):
			return []byte(`{"number":208,"title":"Cardinality","author":{"login":"author"},"state":"OPEN","url":"https://github.com/m31labs/buckley/pull/208","baseRefName":"main","baseRefOid":"base","headRefName":"topic","headRefOid":"head","changedFiles":3}`), nil
		case name == "gh" && hasPRArgPrefix(args, "pr", "checks", "208", "--json", "state"):
			return []byte(`[]`), nil
		case name == "gh" && hasPRArgPrefix(args, "pr", "diff", "208"):
			return []byte("diff --git a/a.go b/a.go\n"), nil
		case name == "gh" && hasPRArgPrefix(args, "pr", "checks", "208", "--json", "name,state"):
			return []byte(`[]`), nil
		case name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") && hasPRArg(args, "repos/m31labs/buckley/issues/208/comments?per_page=100"):
			return []byte(`[]`), nil
		case name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") && hasPRArg(args, "repos/m31labs/buckley/pulls/208/reviews?per_page=100"):
			return []byte(`[]`), nil
		case name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "graphql":
			return []byte(`{"data":{"repository":{"pullRequest":{"reviewThreads":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[]}}}}}`), nil
		case name == "gh" && hasPRArgPrefix(args, "api", "--paginate", "--slurp") && hasPRArg(args, "repos/m31labs/buckley/pulls/208/files?per_page=100"):
			return []byte(`[[{"filename":"a.go"}],[{"filename":"b.go"}]]`), nil
		case name == "git" && matchesPRArgs(args, "--no-pager", "rev-parse", "--show-toplevel"):
			return []byte("/fixture/repo\n"), nil
		case name == "git" && matchesPRArgs(args, "--no-pager", "-C", "/fixture/repo", "rev-parse", "HEAD"):
			return []byte("head\n"), nil
		case name == "git" && matchesPRArgs(args, "--no-pager", "-C", "/fixture/repo", "status", "--porcelain"):
			return nil, nil
		case name == "git" && matchesPRArgs(args, "--no-pager", "-C", "/fixture/repo", "ls-tree", "head", "--", "AGENTS.md"):
			return []byte("100644 blob aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\tAGENTS.md\n"), nil
		case name == "git" && matchesPRArgs(args, "--no-pager", "-C", "/fixture/repo", "show", "head:AGENTS.md"):
			return []byte("Review all changed files.\n"), nil
		default:
			return nil, fmt.Errorf("unexpected command: %s %s", name, strings.Join(args, " "))
		}
	}
	ctx, audit, err := assemblePRContext("208", prContextDependencies{run: run})
	if err != nil {
		t.Fatalf("assemblePRContext: %v", err)
	}
	if len(ctx.Files) != 2 || ctx.Files[0] != "a.go" || ctx.Files[1] != "b.go" {
		t.Fatalf("paginated changed files = %#v, want both API pages", ctx.Files)
	}
	if !ctx.HasIncompleteContext() {
		t.Fatal("changed-file cardinality mismatch must make context incomplete")
	}
	var changedFilesStatus *PRContextStatus
	for i := range ctx.ContextStatus {
		if ctx.ContextStatus[i].Source == "Changed files" {
			changedFilesStatus = &ctx.ContextStatus[i]
			break
		}
	}
	if changedFilesStatus == nil || changedFilesStatus.Status != "incomplete" || !strings.Contains(changedFilesStatus.Detail, "metadata reports 3 files; paginated API returned 2") {
		t.Fatalf("changed-files status = %#v", changedFilesStatus)
	}
	assertAuditSource(t, audit, "changed files (cardinality mismatch)", false)
}

func oversizedReviewDiff() string {
	var diff strings.Builder
	diff.WriteString("diff --git a/pkg/cleanup.go b/pkg/cleanup.go\n")
	diff.WriteString("--- a/pkg/cleanup.go\n")
	diff.WriteString("+++ b/pkg/cleanup.go\n")
	diff.WriteString("@@ -1,0 +1,99999 @@\n")
	for i := 0; diff.Len() <= diffsignal.MaxFileDiffBytes; i++ {
		fmt.Fprintf(&diff, "+var cleanupLine%d = true\n", i)
	}
	return diff.String()
}

func matchesPRArgs(actual []string, expected ...string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for i := range expected {
		if actual[i] != expected[i] {
			return false
		}
	}
	return true
}

func hasPRArgPrefix(actual []string, expected ...string) bool {
	if len(actual) < len(expected) {
		return false
	}
	return matchesPRArgs(actual[:len(expected)], expected...)
}

func hasPRArgPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func hasPRArg(args []string, expected string) bool {
	for _, arg := range args {
		if arg == expected {
			return true
		}
	}
	return false
}

func assertAuditSource(t *testing.T, audit *transparency.ContextAudit, name string, truncated bool) {
	t.Helper()
	for _, source := range audit.Sources() {
		if source.Name == name {
			if source.Truncated != truncated {
				t.Fatalf("audit source %q truncated = %t, want %t", name, source.Truncated, truncated)
			}
			return
		}
	}
	t.Fatalf("audit source %q not found", name)
}
