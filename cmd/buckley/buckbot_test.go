package main

import (
	"context"
	"sync/atomic"
	"testing"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/gitwatcher"
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
