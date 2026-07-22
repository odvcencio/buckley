package main

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/gitwatcher"
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
