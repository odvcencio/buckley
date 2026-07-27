package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/gitwatcher"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/storage"
	"m31labs.dev/buckley/pkg/transparency"
)

func TestBuckbotService_DeduplicatesRevisionAndPostsWithinBudget(t *testing.T) {
	var reviewed, posted atomic.Int32
	service := newBuckbotService(config.BuckbotConfig{PerReviewBudgetUSD: 0.25, MonthlyBudgetUSD: 25}, func(context.Context, gitwatcher.PullRequestEvent) (string, float64, error) {
		reviewed.Add(1)
		return "LGTM", 0.10, nil
	}, func(context.Context, gitwatcher.PullRequestEvent, string) error {
		posted.Add(1)
		return nil
	})
	event := gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 1, HeadSHA: "abc"}
	service.handle(event)
	service.handle(event)
	if reviewed.Load() != 1 || posted.Load() != 1 {
		t.Fatalf("reviewed=%d posted=%d want one of each", reviewed.Load(), posted.Load())
	}
}

func TestRunBuckbotCommandRequiresOnDemandPosting(t *testing.T) {
	err := runBuckbotCommand([]string{"--bind", "0.0.0.0:8086"})
	if err == nil {
		t.Fatal("runBuckbotCommand() unexpectedly enabled automatic posting")
	}
	if code := exitCodeForError(err); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	for _, want := range []string{
		"automatic Buckbot webhook posting is retired",
		"buckley review-pr <PR> -post",
		"on-demand GitHub review",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("migration error = %q, want %q", err, want)
		}
	}
}

func TestBuckbotService_FailedReviewCanBeRetried(t *testing.T) {
	var reviewed, posted atomic.Int32
	service := newBuckbotService(config.BuckbotConfig{PerReviewBudgetUSD: 0.25, MonthlyBudgetUSD: 25}, func(context.Context, gitwatcher.PullRequestEvent) (string, float64, error) {
		if reviewed.Add(1) == 1 {
			return "", 0, errors.New("rate limited")
		}
		return "LGTM", 0.10, nil
	}, func(context.Context, gitwatcher.PullRequestEvent, string) error {
		posted.Add(1)
		return nil
	})
	event := gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 2, HeadSHA: "def"}
	service.handle(event)
	service.handle(event)
	if reviewed.Load() != 2 || posted.Load() != 1 {
		t.Fatalf("reviewed=%d posted=%d want retry then one post", reviewed.Load(), posted.Load())
	}
}

func TestBuckbotService_AutomaticallyRetriesRetryableReview(t *testing.T) {
	var reviewed, posted atomic.Int32
	var retryDelay time.Duration
	service := newBuckbotService(config.BuckbotConfig{PerReviewBudgetUSD: 0.25, MonthlyBudgetUSD: 25}, func(context.Context, gitwatcher.PullRequestEvent) (string, float64, error) {
		if reviewed.Add(1) == 1 {
			return "", 0, &model.APIError{StatusCode: 429, Retryable: true, RetryAfter: time.Minute}
		}
		return "LGTM", 0.10, nil
	}, func(context.Context, gitwatcher.PullRequestEvent, string) error {
		posted.Add(1)
		return nil
	})
	service.sleep = func(_ context.Context, delay time.Duration) error {
		retryDelay = delay
		return nil
	}

	service.handle(gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 20, HeadSHA: "retry"})
	if reviewed.Load() != 2 || posted.Load() != 1 {
		t.Fatalf("reviewed=%d posted=%d want automatic retry then one post", reviewed.Load(), posted.Load())
	}
	if retryDelay != time.Minute {
		t.Fatalf("retry delay=%v want provider Retry-After of 1m", retryDelay)
	}
}

func TestBuckbotService_BoundsReviewRetries(t *testing.T) {
	var reviewed atomic.Int32
	service := newBuckbotService(config.BuckbotConfig{PerReviewBudgetUSD: 0.25, MonthlyBudgetUSD: 25}, func(context.Context, gitwatcher.PullRequestEvent) (string, float64, error) {
		reviewed.Add(1)
		return "", 0, &model.APIError{StatusCode: 429, Retryable: true}
	}, func(context.Context, gitwatcher.PullRequestEvent, string) error {
		t.Fatal("must not post a failed review")
		return nil
	})
	service.sleep = func(context.Context, time.Duration) error { return nil }

	service.handle(gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 24, HeadSHA: "bounded"})
	if reviewed.Load() != buckbotMaxReviewAttempts {
		t.Fatalf("reviewed=%d want bounded attempts=%d", reviewed.Load(), buckbotMaxReviewAttempts)
	}
}

func TestBuckbotService_PersistsIncompletePaidReviewWithoutPosting(t *testing.T) {
	var posted, salvaged atomic.Int32
	service := newBuckbotService(config.BuckbotConfig{PerReviewBudgetUSD: 0.25, MonthlyBudgetUSD: 25}, func(context.Context, gitwatcher.PullRequestEvent) (string, float64, error) {
		return "> [!WARNING]\nIncomplete review\n\ncompleted evidence", 0.10, context.DeadlineExceeded
	}, func(context.Context, gitwatcher.PullRequestEvent, string) error {
		posted.Add(1)
		return nil
	})
	service.salvage = func(event gitwatcher.PullRequestEvent, review string, cause error) (string, error) {
		salvaged.Add(1)
		if event.HeadSHA != "partial" || !strings.Contains(review, "completed evidence") || !errors.Is(cause, context.DeadlineExceeded) {
			t.Fatalf("unexpected salvage: event=%+v review=%q cause=%v", event, review, cause)
		}
		return ".buckley/buckbot/salvage/review.md", nil
	}

	service.handle(gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 27, HeadSHA: "partial"})
	if posted.Load() != 0 || salvaged.Load() != 1 {
		t.Fatalf("posted=%d salvaged=%d want 0/1", posted.Load(), salvaged.Load())
	}
}

func TestSaveBuckbotSalvageWritesSecureArtifact(t *testing.T) {
	t.Chdir(t.TempDir())
	path, err := saveBuckbotSalvage(
		gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 28, HeadSHA: "1234567890abcdef"},
		"completed evidence",
		context.DeadlineExceeded,
	)
	if err != nil {
		t.Fatalf("saveBuckbotSalvage() error = %v", err)
	}
	if !strings.HasPrefix(path, filepath.Join(".buckley", "buckbot", "salvage")) || !strings.Contains(path, "owner-repo-pr-28-1234567890ab") {
		t.Fatalf("salvage path = %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read salvage: %v", err)
	}
	if !strings.Contains(string(data), "Incomplete review") || !strings.Contains(string(data), "completed evidence") || !strings.Contains(string(data), "deadline exceeded") {
		t.Fatalf("salvage content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("salvage permissions = %o, want 600", got)
	}
}

func TestBuckbotService_DeduplicatesConcurrentReview(t *testing.T) {
	var reviewed, posted atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	service := newBuckbotService(config.BuckbotConfig{PerReviewBudgetUSD: 0.25, MonthlyBudgetUSD: 25}, func(context.Context, gitwatcher.PullRequestEvent) (string, float64, error) {
		reviewed.Add(1)
		close(started)
		<-release
		return "LGTM", 0.10, nil
	}, func(context.Context, gitwatcher.PullRequestEvent, string) error {
		posted.Add(1)
		return nil
	})
	event := gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 3, HeadSHA: "ghi"}
	done := make(chan struct{})
	go func() {
		service.handle(event)
		close(done)
	}()
	<-started
	service.handle(event)
	close(release)
	<-done
	if reviewed.Load() != 1 || posted.Load() != 1 {
		t.Fatalf("reviewed=%d posted=%d want one concurrent review", reviewed.Load(), posted.Load())
	}
}

func TestBuckbotService_DiscardsStaleRevisionWhenReviewerIgnoresCancellation(t *testing.T) {
	oldStarted := make(chan struct{})
	releaseOld := make(chan struct{})
	posted := make(chan string, 2)
	service := newBuckbotService(config.BuckbotConfig{PerReviewBudgetUSD: 0.25, MonthlyBudgetUSD: 25}, func(_ context.Context, event gitwatcher.PullRequestEvent) (string, float64, error) {
		if event.HeadSHA == "old" {
			close(oldStarted)
			<-releaseOld
		}
		return "review " + event.HeadSHA, 0.10, nil
	}, func(_ context.Context, event gitwatcher.PullRequestEvent, _ string) error {
		posted <- event.HeadSHA
		return nil
	})

	oldDone := make(chan struct{})
	go func() {
		service.handle(gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 25, HeadSHA: "old"})
		close(oldDone)
	}()
	<-oldStarted
	service.handle(gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 25, HeadSHA: "new"})
	close(releaseOld)
	<-oldDone
	close(posted)

	var revisions []string
	for revision := range posted {
		revisions = append(revisions, revision)
	}
	if len(revisions) != 1 || revisions[0] != "new" {
		t.Fatalf("posted revisions=%v want only new", revisions)
	}
}

func TestBuckbotService_ReservesMonthlyBudgetAcrossPullRequests(t *testing.T) {
	var announced, reviewed, posted atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	service := newBuckbotService(config.BuckbotConfig{PerReviewBudgetUSD: 0.20, MonthlyBudgetUSD: 0.25}, func(context.Context, gitwatcher.PullRequestEvent) (string, float64, error) {
		reviewed.Add(1)
		close(started)
		<-release
		return "LGTM", 0.20, nil
	}, func(context.Context, gitwatcher.PullRequestEvent, string) error {
		posted.Add(1)
		return nil
	})
	service.announce = func(context.Context, gitwatcher.PullRequestEvent, string) error {
		announced.Add(1)
		return nil
	}

	done := make(chan struct{})
	go func() {
		service.handle(gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 21, HeadSHA: "one"})
		close(done)
	}()
	<-started
	service.handle(gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 22, HeadSHA: "two"})
	close(release)
	<-done

	if announced.Load() != 1 || reviewed.Load() != 1 || posted.Load() != 1 {
		t.Fatalf("announced=%d reviewed=%d posted=%d want one budget-reserved review", announced.Load(), reviewed.Load(), posted.Load())
	}
}

func TestBuckbotService_RetriesPostWithoutRerunningReview(t *testing.T) {
	var reviewed, posted atomic.Int32
	service := newBuckbotService(config.BuckbotConfig{PerReviewBudgetUSD: 0.25, MonthlyBudgetUSD: 25}, func(context.Context, gitwatcher.PullRequestEvent) (string, float64, error) {
		reviewed.Add(1)
		return "LGTM", 0.10, nil
	}, func(context.Context, gitwatcher.PullRequestEvent, string) error {
		if posted.Add(1) == 1 {
			return errors.New("temporary GitHub failure")
		}
		return nil
	})
	service.sleep = func(context.Context, time.Duration) error { return nil }

	service.handle(gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 23, HeadSHA: "post-retry"})
	if reviewed.Load() != 1 || posted.Load() != 2 {
		t.Fatalf("reviewed=%d posted=%d want one review and two post attempts", reviewed.Load(), posted.Load())
	}
	service.mu.Lock()
	spent := service.spentUSD
	service.mu.Unlock()
	if spent != 0.10 {
		t.Fatalf("spent=%v want one review cost of 0.10", spent)
	}
}

func TestBuckbotService_BoundsPostRetriesWithoutRerunningReview(t *testing.T) {
	var reviewed, posted atomic.Int32
	service := newBuckbotService(config.BuckbotConfig{PerReviewBudgetUSD: 0.25, MonthlyBudgetUSD: 25}, func(context.Context, gitwatcher.PullRequestEvent) (string, float64, error) {
		reviewed.Add(1)
		return "LGTM", 0.10, nil
	}, func(context.Context, gitwatcher.PullRequestEvent, string) error {
		posted.Add(1)
		return errors.New("GitHub unavailable")
	})
	service.sleep = func(context.Context, time.Duration) error { return nil }

	service.handle(gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 26, HeadSHA: "post-bounded"})
	if reviewed.Load() != 1 || posted.Load() != buckbotMaxPostAttempts {
		t.Fatalf("reviewed=%d posted=%d want one review and %d bounded posts", reviewed.Load(), posted.Load(), buckbotMaxPostAttempts)
	}
}

func TestSaveBuckbotSpend_PersistsMonthlyCost(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "buckbot.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	event := gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 7, HeadSHA: "abc"}
	entries := []transparency.CostEntry{{Model: "moonshotai/kimi-k3", Tokens: transparency.TokenUsage{Input: 100, Output: 10}, Cost: 0.12}}
	if err := saveBuckbotSpend(store, event, "moonshotai/kimi-k3", entries); err != nil {
		t.Fatalf("save spend: %v", err)
	}
	spend, err := store.GetMonthlyCostForPrincipal("buckbot")
	if err != nil {
		t.Fatalf("monthly spend: %v", err)
	}
	if spend != 0.12 {
		t.Fatalf("spend=%v want 0.12", spend)
	}
}

func TestBuckbotService_DoesNotPostOverBudget(t *testing.T) {
	var posted atomic.Int32
	service := newBuckbotService(config.BuckbotConfig{PerReviewBudgetUSD: 0.25, MonthlyBudgetUSD: 25}, func(context.Context, gitwatcher.PullRequestEvent) (string, float64, error) {
		return "review", 0.26, nil
	}, func(context.Context, gitwatcher.PullRequestEvent, string) error {
		posted.Add(1)
		return nil
	})
	service.handle(gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 1, HeadSHA: "abc"})
	if posted.Load() != 0 {
		t.Fatal("over-budget review must not be posted")
	}
}

func TestAppendBuckbotCostFooter(t *testing.T) {
	got := appendBuckbotCostFooter("## Review\nLooks good.", "moonshotai/kimi-k2.7-code", transparency.CostSummary{
		SessionCost:     0.08321,
		SessionTokens:   transparency.TokenUsage{Input: 12_400, Output: 2_000},
		InvocationCount: 2,
	}, 0.25)
	for _, want := range []string{
		"Looks good.",
		"`moonshotai/kimi-k2.7-code`",
		"$0.0832 / $0.25",
		"12.4k input + 2k output tokens",
		"2 model calls",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("footer = %q, want %q", got, want)
		}
	}
	if got := appendBuckbotCostFooter("", "model", transparency.CostSummary{}, 0.25); got != "" {
		t.Fatalf("empty review footer = %q, want empty", got)
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
			return errors.New("one line is outside the diff")
		}
		if len(calls) == 4 {
			return errors.New("second line remains invalid")
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
	if err != nil {
		t.Fatalf("postBuckbotReview() error = %v", err)
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

func mustJSONQuote(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFormatBuckbotCount(t *testing.T) {
	tests := map[int]string{
		999:       "999",
		1_000:     "1k",
		12_400:    "12.4k",
		999_999:   "1.0M",
		1_000_000: "1.0M",
		1_500_000: "1.5M",
	}
	for input, want := range tests {
		if got := formatBuckbotCount(input); got != want {
			t.Errorf("formatBuckbotCount(%d) = %q, want %q", input, got, want)
		}
	}
}
