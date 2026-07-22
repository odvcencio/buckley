package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/gitwatcher"
	"m31labs.dev/buckley/pkg/model"
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
	var reviewed, posted atomic.Int32
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

	done := make(chan struct{})
	go func() {
		service.handle(gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 21, HeadSHA: "one"})
		close(done)
	}()
	<-started
	service.handle(gitwatcher.PullRequestEvent{Repository: "owner/repo", Number: 22, HeadSHA: "two"})
	close(release)
	<-done

	if reviewed.Load() != 1 || posted.Load() != 1 {
		t.Fatalf("reviewed=%d posted=%d want one budget-reserved review", reviewed.Load(), posted.Load())
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
