package main

import (
	"context"
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
		"FINDING-001 · MAJOR: Budget bypass",
		"A zero value skips the guard.",
		"**Impact:** A review can exceed its cap.",
		"**Suggested fix:** Reject non-positive budgets.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("inline finding = %q, want %q", got, want)
		}
	}
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
