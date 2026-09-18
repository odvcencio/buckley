package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/gitwatcher"
	"m31labs.dev/buckley/pkg/oneshot/commands"
)

func TestRunBuckbotCommandRoutesGeneralReviews(t *testing.T) {
	originalReview := runBuckbotReviewFn
	originalPR := runBuckbotReviewPRFn
	t.Cleanup(func() {
		runBuckbotReviewFn = originalReview
		runBuckbotReviewPRFn = originalPR
	})

	type call struct {
		kind string
		args []string
	}
	var calls []call
	runBuckbotReviewFn = func(args []string) error {
		calls = append(calls, call{kind: "review", args: append([]string(nil), args...)})
		return nil
	}
	runBuckbotReviewPRFn = func(args []string) error {
		calls = append(calls, call{kind: "pr", args: append([]string(nil), args...)})
		return nil
	}

	tests := []struct {
		name string
		args []string
		want call
	}{
		{name: "default local review", want: call{kind: "review"}},
		{name: "named local review", args: []string{"review", "--scope", "branch"}, want: call{kind: "review", args: []string{"--scope", "branch"}}},
		{name: "direct review flags", args: []string{"--project", "--no-interactive"}, want: call{kind: "review", args: []string{"--project", "--no-interactive"}}},
		{name: "repository review", args: []string{"repo", "--no-interactive"}, want: call{kind: "review", args: []string{"--project", "--no-interactive"}}},
		{name: "project alias", args: []string{"project"}, want: call{kind: "review", args: []string{"--project"}}},
		{name: "pull request review", args: []string{"pr", "123", "--post"}, want: call{kind: "pr", args: []string{"123", "--post"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls = nil
			if err := runBuckbotCommand(tt.args); err != nil {
				t.Fatalf("runBuckbotCommand(%v) error = %v", tt.args, err)
			}
			if len(calls) != 1 {
				t.Fatalf("calls = %#v, want one", calls)
			}
			if calls[0].kind != tt.want.kind || !slices.Equal(calls[0].args, tt.want.args) {
				t.Fatalf("call = %#v, want %#v", calls[0], tt.want)
			}
		})
	}
}

func TestRunBuckbotCommandRequiresPRReference(t *testing.T) {
	err := runBuckbotCommand([]string{"pr"})
	if err == nil {
		t.Fatal("runBuckbotCommand() error = nil, want usage error")
	}
	if code := exitCodeForError(err); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(err.Error(), "buckley buckbot pr <pr-number-or-url>") {
		t.Fatalf("error = %q, want Buckbot PR usage", err)
	}
}

func TestFormatBuckbotInlineFinding(t *testing.T) {
	got := formatBuckbotInlineFinding(commands.Finding{
		ID:       "FINDING-001",
		Severity: commands.SeverityMajor,
		Title:    "Budget bypass",
		Evidence: "A zero value skips the guard.",
		Impact:   "A review can exceed its cap.",
		Fix:      "Reject non-positive budgets.",
	})
	for _, want := range []string{
		"<!-- buckbot:FINDING-001 -->",
		"MAJOR · Budget bypass",
		"A zero value skips the guard.",
		"**Impact:** A review can exceed its cap.",
		"**Recommended change:** Reject non-positive budgets.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("inline finding = %q, want %q", got, want)
		}
	}
}

func TestFormatBuckbotGitHubReview(t *testing.T) {
	approved := commands.ParseReview("## Grade: A\n\n## Coverage\n- every file\n\n## Invariant Audit\n- bounds\n\n## Falsification\n- disproved\n\n## Verdict\n- **Approved**: YES\n- **Blockers**: NONE")
	got := formatBuckbotGitHubReview(approved, approved.RawReview, "1234567890abcdef")
	for _, want := range []string{
		"[!TIP]",
		"Buckbot · Grade A · READY TO MERGE",
		"`1234567890ab`",
		"Line comments contain only demonstrated findings.",
		"<summary><strong>Full changed-file coverage ledger</strong></summary>",
		"<summary><strong>Cross-file invariant audit</strong></summary>",
		"<summary><strong>Strongest-failure falsification</strong></summary>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("GitHub review = %q, want %q", got, want)
		}
	}
}

func TestPostBuckbotReviewLifecycleReactsAndPostsOneIntake(t *testing.T) {
	original := runBuckbotGitHubFn
	t.Cleanup(func() { runBuckbotGitHubFn = original })

	const head = "1234567890abcdef1234567890abcdef12345678"
	marker := commands.BuckbotReviewIntakeMarker(head)
	tests := []struct {
		name           string
		existingBody   string
		existingAuthor string
		wantPostIntake bool
	}{
		{name: "new revision", wantPostIntake: true},
		{name: "existing intake", existingBody: marker + "\nalready posted", existingAuthor: "buckbot", wantPostIntake: false},
		{name: "forged intake", existingBody: marker + "\nforged", existingAuthor: "attacker", wantPostIntake: true},
		{name: "quoted intake", existingBody: "quoted\n" + marker, existingAuthor: "buckbot", wantPostIntake: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			runBuckbotGitHubFn = func(_ context.Context, args []string, input []byte) ([]byte, error) {
				calls++
				if strings.Join(args, " ") != "api graphql --input -" {
					t.Fatalf("GitHub args = %v", args)
				}
				var request struct {
					Query     string         `json:"query"`
					Variables map[string]any `json:"variables"`
				}
				if err := json.Unmarshal(input, &request); err != nil {
					t.Fatalf("decode GraphQL request: %v", err)
				}
				if calls == 1 {
					if request.Variables["owner"] != "owner" || request.Variables["name"] != "repo" ||
						request.Variables["number"] != float64(42) {
						t.Fatalf("query variables = %#v", request.Variables)
					}
					return []byte(`{"data":{` +
						`"viewer":{"login":"buckbot"},` +
						`"repository":{"pullRequest":{"id":"PR_node","headRefOid":"` + head + `",` +
						`"comments":{"nodes":[{"author":{"login":` + mustJSONQuote(t, tt.existingAuthor) + `},"body":` + mustJSONQuote(t, tt.existingBody) + `}]}}}}}`), nil
				}
				if calls != 2 {
					t.Fatalf("GraphQL calls = %d, want 2", calls)
				}
				if request.Variables["subjectId"] != "PR_node" ||
					request.Variables["postIntake"] != tt.wantPostIntake {
					t.Fatalf("mutation variables = %#v", request.Variables)
				}
				body, _ := request.Variables["body"].(string)
				for _, want := range []string{
					marker,
					"Buckbot review started",
					"I captured commit `1234567890ab`.",
					"A deeper review will follow",
					"model `qwen/qwen3.7-plus`",
					"focused review",
					"high reasoning",
					"$0.15 limit",
				} {
					if !strings.Contains(body, want) {
						t.Fatalf("intake body missing %q:\n%s", want, body)
					}
				}
				if !strings.Contains(request.Query, "addReaction") || !strings.Contains(request.Query, "content: EYES") {
					t.Fatalf("mutation does not add EYES reaction:\n%s", request.Query)
				}
				if tt.wantPostIntake {
					return []byte(`{"data":{"eyes":{"reaction":{"content":"EYES"}},"intake":{"commentEdge":{"node":{"id":"intake"}}}}}`), nil
				}
				return []byte(`{"data":{"eyes":{"reaction":{"content":"EYES"}}}}`), nil
			}

			err := postBuckbotReviewLifecycle(context.Background(), gitwatcher.PullRequestEvent{
				Repository: "owner/repo",
				Number:     42,
				HeadSHA:    head,
			}, buckbotReviewIntake{
				Model:           "qwen/qwen3.7-plus",
				ReasoningEffort: "high",
				SizeClass:       "focused",
				BudgetUSD:       0.15,
			})
			if err != nil {
				t.Fatalf("postBuckbotReviewLifecycle() error = %v", err)
			}
			if calls != 2 {
				t.Fatalf("GraphQL calls = %d, want 2", calls)
			}
		})
	}
}

func TestPostBuckbotReviewLifecycleRejectsMovedHead(t *testing.T) {
	original := runBuckbotGitHubFn
	t.Cleanup(func() { runBuckbotGitHubFn = original })

	var calls int
	runBuckbotGitHubFn = func(context.Context, []string, []byte) ([]byte, error) {
		calls++
		return []byte(`{"data":{"viewer":{"login":"buckbot"},"repository":{"pullRequest":{"id":"PR_node","headRefOid":"new","comments":{"nodes":[]}}}}}`), nil
	}
	err := postBuckbotReviewLifecycle(context.Background(), gitwatcher.PullRequestEvent{
		Repository: "owner/repo",
		Number:     42,
		HeadSHA:    "old",
	}, buckbotReviewIntake{})
	if err == nil || !strings.Contains(err.Error(), "head changed from old to new") {
		t.Fatalf("error = %v, want moved-head rejection", err)
	}
	if calls != 1 {
		t.Fatalf("GraphQL calls = %d, want query only", calls)
	}
}

func TestPostBuckbotReviewLifecycleRequiresMutationEvidence(t *testing.T) {
	original := runBuckbotGitHubFn
	t.Cleanup(func() { runBuckbotGitHubFn = original })

	const head = "1234567890abcdef1234567890abcdef12345678"
	var calls int
	runBuckbotGitHubFn = func(context.Context, []string, []byte) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"data":{"viewer":{"login":"buckbot"},"repository":{"pullRequest":{` +
				`"id":"PR_node","headRefOid":"` + head + `","comments":{"pageInfo":{"hasNextPage":false},"nodes":[]}` +
				`}}}}`), nil
		}
		return []byte(`{"data":{"eyes":{"reaction":{"content":"EYES"}},"intake":{"commentEdge":{"node":{"id":""}}}}}`), nil
	}
	err := postBuckbotReviewLifecycle(context.Background(), gitwatcher.PullRequestEvent{
		Repository: "owner/repo",
		Number:     42,
		HeadSHA:    head,
	}, buckbotReviewIntake{})
	if err == nil || !strings.Contains(err.Error(), "omitted the intake comment ID") {
		t.Fatalf("error = %v, want missing intake evidence", err)
	}
}

func TestPostBuckbotReviewLifecycleFindsIntakeAfterOneHundredComments(t *testing.T) {
	original := runBuckbotGitHubFn
	t.Cleanup(func() { runBuckbotGitHubFn = original })

	const head = "1234567890abcdef1234567890abcdef12345678"
	marker := commands.BuckbotReviewIntakeMarker(head)
	var calls int
	runBuckbotGitHubFn = func(_ context.Context, _ []string, input []byte) ([]byte, error) {
		calls++
		var request struct {
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(input, &request); err != nil {
			t.Fatal(err)
		}
		switch calls {
		case 1:
			if request.Variables["after"] != nil {
				t.Fatalf("first cursor = %#v, want null", request.Variables["after"])
			}
			return []byte(`{"data":{"viewer":{"login":"buckbot"},"repository":{"pullRequest":{` +
				`"id":"PR_node","headRefOid":"` + head + `",` +
				`"comments":{"pageInfo":{"hasNextPage":true,"endCursor":"page-2"},"nodes":[{"author":{"login":"someone"},"body":"older"}]}` +
				`}}}}`), nil
		case 2:
			if request.Variables["after"] != "page-2" {
				t.Fatalf("second cursor = %#v, want page-2", request.Variables["after"])
			}
			return []byte(`{"data":{"viewer":{"login":"buckbot"},"repository":{"pullRequest":{` +
				`"id":"PR_node","headRefOid":"` + head + `",` +
				`"comments":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"author":{"login":"buckbot"},"body":` + mustJSONQuote(t, marker) + `}]}` +
				`}}}}`), nil
		case 3:
			if request.Variables["postIntake"] != false {
				t.Fatalf("postIntake = %#v, want false", request.Variables["postIntake"])
			}
			return []byte(`{"data":{"eyes":{"reaction":{"content":"EYES"}}}}`), nil
		default:
			t.Fatalf("calls = %d, want 3", calls)
			return nil, nil
		}
	}

	err := postBuckbotReviewLifecycle(context.Background(), gitwatcher.PullRequestEvent{
		Repository: "owner/repo",
		Number:     42,
		HeadSHA:    head,
	}, buckbotReviewIntake{})
	if err != nil {
		t.Fatalf("postBuckbotReviewLifecycle() error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want two pages and one mutation", calls)
	}
}

func TestPostBuckbotReviewPayloadFallsBackToGraphQLWhenRESTIsThrottled(t *testing.T) {
	original := runBuckbotGitHubFn
	t.Cleanup(func() { runBuckbotGitHubFn = original })

	const head = "1234567890abcdef1234567890abcdef12345678"
	var calls int
	runBuckbotGitHubFn = func(_ context.Context, args []string, input []byte) ([]byte, error) {
		calls++
		switch calls {
		case 1:
			if len(args) < 2 || args[0] != "api" || args[1] != "repos/owner/repo/pulls/42/reviews" {
				t.Fatalf("REST args = %v", args)
			}
			return []byte("gh: API rate limit exceeded (HTTP 403)"), errors.New("exit status 1")
		case 2:
			var request struct {
				Variables map[string]any `json:"variables"`
			}
			if err := json.Unmarshal(input, &request); err != nil {
				t.Fatal(err)
			}
			if request.Variables["owner"] != "owner" || request.Variables["name"] != "repo" {
				t.Fatalf("identity variables = %#v", request.Variables)
			}
			return []byte(`{"data":{"repository":{"pullRequest":{"id":"PR_node","headRefOid":"` + head + `"}}}}`), nil
		case 3:
			var request struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			if err := json.Unmarshal(input, &request); err != nil {
				t.Fatal(err)
			}
			if request.Variables["pullRequestId"] != "PR_node" ||
				request.Variables["commitOID"] != head ||
				request.Variables["body"] != "review body" {
				t.Fatalf("review variables = %#v", request.Variables)
			}
			threads, ok := request.Variables["threads"].([]any)
			if !ok || len(threads) != 1 {
				t.Fatalf("review threads = %#v", request.Variables["threads"])
			}
			thread, ok := threads[0].(map[string]any)
			if !ok || thread["path"] != "a.go" || thread["line"] != float64(12) ||
				thread["side"] != "RIGHT" || thread["body"] != "finding" {
				t.Fatalf("review thread = %#v", threads[0])
			}
			if !strings.Contains(request.Query, "addPullRequestReview") ||
				!strings.Contains(request.Query, "event: COMMENT") {
				t.Fatalf("review mutation = %s", request.Query)
			}
			return []byte(`{"data":{"addPullRequestReview":{"pullRequestReview":{"id":"review","url":"https://example/review"}}}}`), nil
		default:
			t.Fatalf("calls = %d, want 3", calls)
			return nil, nil
		}
	}

	err := postBuckbotReviewPayload(context.Background(), gitwatcher.PullRequestEvent{
		Repository: "owner/repo",
		Number:     42,
		HeadSHA:    head,
	}, "review body", []map[string]any{{
		"path": "a.go",
		"line": 12,
		"side": "RIGHT",
		"body": "finding",
	}})
	if err != nil {
		t.Fatalf("postBuckbotReviewPayload() error = %v", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want REST plus GraphQL query and mutation", calls)
	}
}

func TestPostBuckbotReviewPayloadDoesNotMaskNonThrottleRESTFailure(t *testing.T) {
	original := runBuckbotGitHubFn
	t.Cleanup(func() { runBuckbotGitHubFn = original })

	var calls int
	runBuckbotGitHubFn = func(context.Context, []string, []byte) ([]byte, error) {
		calls++
		return []byte("gh: Resource not accessible by integration (HTTP 403)"), errors.New("exit status 1")
	}
	err := postBuckbotReviewPayload(context.Background(), gitwatcher.PullRequestEvent{
		Repository: "owner/repo",
		Number:     42,
		HeadSHA:    "head",
	}, "review", nil)
	if err == nil || !strings.Contains(err.Error(), "Resource not accessible") {
		t.Fatalf("error = %v, want permission failure", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want no GraphQL fallback", calls)
	}
}

func TestPostBuckbotReviewPayloadGraphQLRequiresReviewID(t *testing.T) {
	original := runBuckbotGitHubFn
	t.Cleanup(func() { runBuckbotGitHubFn = original })

	const head = "1234567890abcdef1234567890abcdef12345678"
	var calls int
	runBuckbotGitHubFn = func(context.Context, []string, []byte) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`{"data":{"repository":{"pullRequest":{"id":"PR","headRefOid":"` + head + `"}}}}`), nil
		}
		return []byte(`{"data":{"addPullRequestReview":{"pullRequestReview":{"id":""}}}}`), nil
	}
	err := postBuckbotReviewPayloadGraphQL(context.Background(), gitwatcher.PullRequestEvent{
		Repository: "owner/repo",
		Number:     42,
		HeadSHA:    head,
	}, "review", nil)
	if err == nil || !strings.Contains(err.Error(), "omitted the review ID") {
		t.Fatalf("error = %v, want missing review evidence", err)
	}
}

func TestPostBuckbotReviewPayloadRESTClassifiesOnlyStructuredInlineValidation(t *testing.T) {
	original := runBuckbotGitHubFn
	t.Cleanup(func() { runBuckbotGitHubFn = original })

	event := gitwatcher.PullRequestEvent{
		Repository: "owner/repo",
		Number:     42,
		HeadSHA:    "1234567890abcdef",
	}
	inline := []map[string]any{{
		"path": "a.go",
		"line": 12,
		"side": "RIGHT",
		"body": "finding",
	}}

	tests := []struct {
		name      string
		output    string
		wantTyped bool
	}{
		{
			name: "pull request review comment validation",
			output: `gh: Validation Failed (HTTP 422)
{"message":"Validation Failed","errors":[{"resource":"PullRequestReviewComment","field":"line","code":"invalid"}]}`,
			wantTyped: true,
		},
		{
			name: "diagnostic before JSON and trailing diagnostic",
			output: `gh: Validation Failed (HTTP 422)
{"message":"Validation Failed","errors":[{"resource":"PullRequestReviewComment","field":"line","code":"invalid"}]}
gh: Validation Failed (HTTP 422)`,
			wantTyped: true,
		},
		{
			name: "JSON before diagnostic",
			output: `{"message":"Validation Failed","errors":[{"resource":"PullRequestReviewComment","field":"line","code":"invalid"}]}
gh: Validation Failed (HTTP 422)`,
			wantTyped: true,
		},
		{
			name: "top-level comments field validation remains ambiguous",
			output: `gh: Validation Failed (HTTP 422)
{"message":"Validation Failed","errors":[{"resource":"PullRequestReview","field":"comments","code":"invalid"}]}`,
			wantTyped: false,
		},
		{
			name: "spam 422 remains ambiguous",
			output: `gh: Validation Failed, or the endpoint has been spammed. (HTTP 422)
{"message":"Validation Failed","errors":[{"resource":"PullRequestReview","field":"body","code":"spam"}]}`,
			wantTyped: false,
		},
		{
			name: "inline then spam remains ambiguous",
			output: `gh: Validation Failed (HTTP 422)
{"message":"Validation Failed","errors":[{"resource":"PullRequestReviewComment","field":"line","code":"invalid"},{"resource":"PullRequestReview","field":"body","code":"spam"}]}`,
			wantTyped: false,
		},
		{
			name: "spam then inline remains ambiguous",
			output: `gh: Validation Failed (HTTP 422)
{"message":"Validation Failed","errors":[{"resource":"PullRequestReview","field":"body","code":"spam"},{"resource":"PullRequestReviewComment","field":"line","code":"invalid"}]}`,
			wantTyped: false,
		},
		{
			name: "inline plus top level body remains ambiguous",
			output: `gh: Validation Failed (HTTP 422)
{"message":"Validation Failed","errors":[{"resource":"PullRequestReviewComment","field":"line","code":"invalid"},{"resource":"PullRequestReview","field":"body","code":"invalid"}]}`,
			wantTyped: false,
		},
		{
			name: "inline comment body remains ambiguous",
			output: `gh: Validation Failed (HTTP 422)
{"message":"Validation Failed","errors":[{"resource":"PullRequestReviewComment","field":"body","code":"invalid"}]}`,
			wantTyped: false,
		},
		{
			name: "multiple inline location failures",
			output: `gh: Validation Failed (HTTP 422)
{"message":"Validation Failed","errors":[{"resource":"PullRequestReviewComment","field":"line","code":"invalid"},{"resource":"PullRequestReviewComment","field":"path","code":"invalid"}]}`,
			wantTyped: true,
		},
		{
			name:      "permission failure is not validation",
			output:    `gh: Resource not accessible by integration (HTTP 403)`,
			wantTyped: false,
		},
		{
			name:      "unstructured 422 is not enough",
			output:    `gh: Validation Failed for line comments (HTTP 422)`,
			wantTyped: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runBuckbotGitHubFn = func(context.Context, []string, []byte) ([]byte, error) {
				return []byte(tt.output), errors.New("exit status 1")
			}

			err := postBuckbotReviewPayloadREST(context.Background(), event, "review", inline)
			if err == nil {
				t.Fatal("postBuckbotReviewPayloadREST() error = nil, want error")
			}
			if got := isBuckbotInlineValidationError(err); got != tt.wantTyped {
				t.Fatalf("isBuckbotInlineValidationError() = %v, want %v for %v", got, tt.wantTyped, err)
			}
		})
	}
}

func TestBuckbotReviewPayloadHelpersRejectIncompleteIdentity(t *testing.T) {
	original := runBuckbotGitHubFn
	t.Cleanup(func() { runBuckbotGitHubFn = original })
	runBuckbotGitHubFn = func(context.Context, []string, []byte) ([]byte, error) {
		t.Fatal("incomplete identity must fail before GitHub access")
		return nil, nil
	}

	event := gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 42}
	for name, post := range map[string]func() error{
		"wrapper": func() error {
			return postBuckbotReviewPayload(context.Background(), event, "review", nil)
		},
		"REST": func() error {
			return postBuckbotReviewPayloadREST(context.Background(), event, "review", nil)
		},
		"GraphQL": func() error {
			return postBuckbotReviewPayloadGraphQL(context.Background(), event, "review", nil)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := post(); err == nil || !strings.Contains(err.Error(), "incomplete pull request identity") {
				t.Fatalf("error = %v, want identity rejection", err)
			}
		})
	}
}

func TestPostBuckbotReviewPreservesValidInlineCommentsAfterBatchRejection(t *testing.T) {
	original := postBuckbotReviewPayloadFn
	t.Cleanup(func() { postBuckbotReviewPayloadFn = original })

	type postCall struct {
		body     string
		comments []map[string]any
	}
	var calls []postCall
	postBuckbotReviewPayloadFn = func(_ context.Context, _ gitwatcher.PullRequestEvent, body string, comments []map[string]any) error {
		calls = append(calls, postCall{body: body, comments: comments})
		if len(calls) == 1 {
			return &buckbotInlineValidationError{err: errors.New("one line is outside the diff")}
		}
		if len(calls) == 4 {
			return &buckbotInlineValidationError{err: errors.New("second line remains invalid")}
		}
		return nil
	}

	review := `## Grade: C

## Findings
### FINDING-001: [MAJOR] First defect
- **File**: first.go:12
- **Evidence**: The first condition fails.
- **Impact**: Users receive the wrong result.
- **Fix**: Correct the first condition.

### FINDING-002: [MINOR] Second defect
- **File**: second.go:24
- **Evidence**: The second condition fails.
- **Impact**: Logs contain the wrong value.
- **Fix**: Correct the second condition.

## Verdict
- **Approved**: NO
- **Blockers**: FINDING-001`

	err := postBuckbotReview(context.Background(), gitwatcher.PullRequestEvent{
		Repository: "owner/repo",
		Number:     42,
		HeadSHA:    "1234567890abcdef",
	}, review)
	var deliveryErr *buckbotReviewDeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("postBuckbotReview() error = %v, want delivery error", err)
	}
	if !deliveryErr.summaryPosted || deliveryErr.inlineTotal != 2 ||
		deliveryErr.inlineAttempted != 2 || deliveryErr.inlineFailed != 1 ||
		deliveryErr.ambiguous {
		t.Fatalf("delivery error = %#v, want one definite inline failure after summary", deliveryErr)
	}
	if len(deliveryErr.locations) != 1 || deliveryErr.locations[0] != "second.go:24" {
		t.Fatalf("locations = %#v, want second.go:24", deliveryErr.locations)
	}
	if !strings.Contains(err.Error(), "summary posted") || !strings.Contains(err.Error(), "1 of 2 inline comments failed") {
		t.Fatalf("delivery error text = %q, want partial summary/inline details", err)
	}
	if len(calls) != 4 {
		t.Fatalf("post calls = %d, want batch, summary, and two individual comments", len(calls))
	}
	if len(calls[0].comments) != 2 || len(calls[1].comments) != 0 ||
		len(calls[2].comments) != 1 || len(calls[3].comments) != 1 {
		t.Fatalf("post comment counts = %d/%d/%d/%d, want 2/0/1/1",
			len(calls[0].comments), len(calls[1].comments), len(calls[2].comments), len(calls[3].comments))
	}
	if !strings.Contains(calls[1].body, "Buckbot · Grade C · CHANGES REQUESTED") {
		t.Fatalf("summary fallback = %q, want formatted review", calls[1].body)
	}
}

func TestPostBuckbotReviewDoesNotFallbackWithoutAffirmativeInlineValidation(t *testing.T) {
	original := postBuckbotReviewPayloadFn
	t.Cleanup(func() { postBuckbotReviewPayloadFn = original })

	tests := []struct {
		name string
		err  error
	}{
		{name: "permission", err: errors.New("post GitHub review: Resource not accessible by integration (HTTP 403)")},
		{name: "spam 422", err: errors.New("post GitHub review: Validation Failed, or the endpoint has been spammed. (HTTP 422)")},
		{name: "post state ambiguous", err: errors.New("post GitHub review: connection reset; post-state check: read posted Buckbot reviews: timeout")},
		{name: "stale head", err: errors.New("check existing GitHub review: read posted Buckbot reviews: head changed from old to new")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			postBuckbotReviewPayloadFn = func(context.Context, gitwatcher.PullRequestEvent, string, []map[string]any) error {
				calls++
				return tt.err
			}

			err := postBuckbotReview(context.Background(), gitwatcher.PullRequestEvent{
				Repository: "owner/repo",
				Number:     42,
				HeadSHA:    "1234567890abcdef",
			}, buckbotReviewWithTwoFindings())
			if err == nil || !errors.Is(err, tt.err) {
				t.Fatalf("error = %v, want original %v", err, tt.err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want no fallback fanout", calls)
			}
		})
	}
}

func TestPostBuckbotReviewStopsFallbackOnCancellation(t *testing.T) {
	original := postBuckbotReviewPayloadFn
	t.Cleanup(func() { postBuckbotReviewPayloadFn = original })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	postBuckbotReviewPayloadFn = func(context.Context, gitwatcher.PullRequestEvent, string, []map[string]any) error {
		t.Fatal("canceled context must fail before posting")
		return nil
	}
	err := postBuckbotReview(ctx, gitwatcher.PullRequestEvent{
		Repository: "owner/repo",
		Number:     42,
		HeadSHA:    "1234567890abcdef",
	}, buckbotReviewWithTwoFindings())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestPostBuckbotReviewStopsInlineFanoutWhenCanceledAfterSummaryFallback(t *testing.T) {
	original := postBuckbotReviewPayloadFn
	t.Cleanup(func() { postBuckbotReviewPayloadFn = original })

	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	postBuckbotReviewPayloadFn = func(_ context.Context, _ gitwatcher.PullRequestEvent, body string, comments []map[string]any) error {
		calls++
		switch calls {
		case 1:
			return &buckbotInlineValidationError{err: errors.New("batch inline validation failed")}
		case 2:
			if strings.TrimSpace(body) == "" || len(comments) != 0 {
				t.Fatalf("summary fallback call = body %q comments %d, want summary only", body, len(comments))
			}
			cancel()
			return nil
		default:
			t.Fatalf("calls = %d, want cancellation to stop before inline fanout", calls)
			return nil
		}
	}

	err := postBuckbotReview(ctx, gitwatcher.PullRequestEvent{
		Repository: "owner/repo",
		Number:     42,
		HeadSHA:    "1234567890abcdef",
	}, buckbotReviewWithTwoFindings())
	var deliveryErr *buckbotReviewDeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("error = %v, want delivery error", err)
	}
	if !errors.Is(err, context.Canceled) || !deliveryErr.summaryPosted ||
		!deliveryErr.ambiguous || deliveryErr.inlineAttempted != 0 ||
		deliveryErr.inlineFailed != 0 || deliveryErr.inlineTotal != 2 {
		t.Fatalf("delivery error = %#v, want summary-only cancellation", deliveryErr)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want batch and summary only", calls)
	}
}

func TestPostBuckbotReviewStopsInlineFanoutAfterAmbiguousIndividualFailure(t *testing.T) {
	original := postBuckbotReviewPayloadFn
	t.Cleanup(func() { postBuckbotReviewPayloadFn = original })

	ambiguousErr := errors.New("post GitHub review: connection reset; post-state check unavailable")
	var calls int
	postBuckbotReviewPayloadFn = func(_ context.Context, _ gitwatcher.PullRequestEvent, body string, comments []map[string]any) error {
		calls++
		switch calls {
		case 1:
			return &buckbotInlineValidationError{err: errors.New("batch inline validation failed")}
		case 2:
			if strings.TrimSpace(body) == "" || len(comments) != 0 {
				t.Fatalf("summary fallback call = body %q comments %d, want summary only", body, len(comments))
			}
			return nil
		case 3:
			if len(comments) != 1 {
				t.Fatalf("first inline fallback comments = %d, want 1", len(comments))
			}
			return ambiguousErr
		default:
			t.Fatalf("calls = %d, want fanout to stop after ambiguous inline failure", calls)
			return nil
		}
	}

	err := postBuckbotReview(context.Background(), gitwatcher.PullRequestEvent{
		Repository: "owner/repo",
		Number:     42,
		HeadSHA:    "1234567890abcdef",
	}, buckbotReviewWithTwoFindings())
	var deliveryErr *buckbotReviewDeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("error = %v, want delivery error", err)
	}
	if !errors.Is(err, ambiguousErr) || !deliveryErr.summaryPosted ||
		!deliveryErr.ambiguous || deliveryErr.inlineAttempted != 1 ||
		deliveryErr.inlineFailed != 1 || deliveryErr.inlineTotal != 2 {
		t.Fatalf("delivery error = %#v, want ambiguous partial after one inline", deliveryErr)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want batch, summary, first inline only", calls)
	}
}

func buckbotReviewWithTwoFindings() string {
	return `## Grade: C

## Findings
### FINDING-001: [MAJOR] First defect
- **File**: first.go:12
- **Evidence**: The first condition fails.
- **Impact**: Users receive the wrong result.
- **Fix**: Correct the first condition.

### FINDING-002: [MINOR] Second defect
- **File**: second.go:24
- **Evidence**: The second condition fails.
- **Impact**: Logs contain the wrong value.
- **Fix**: Correct the second condition.

## Verdict
- **Approved**: NO
- **Blockers**: FINDING-001`
}

func mustJSONQuote(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
