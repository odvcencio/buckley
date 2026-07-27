package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/gitwatcher"
	"m31labs.dev/buckley/pkg/oneshot/commands"
)

func TestParseReviewPRCommandOptions(t *testing.T) {
	opts, err := parseReviewPRCommandOptions([]string{
		"-verbose",
		"-cost=false",
		"-post=false",
		"-model", "test/reviewer",
		"-critic-model", "test/critic",
		"-timeout", "30s",
		"-output", "review.md",
		"-budget", "0.25",
		"-max-turns", "3",
		"-max-diff-bytes", "80000",
		"-max-validation-attempts", "2",
		"https://github.com/owner/repo/pull/123",
	})
	if err != nil {
		t.Fatalf("parseReviewPRCommandOptions() error = %v", err)
	}

	if !opts.verbose {
		t.Fatal("verbose = false, want true")
	}
	if opts.showCost {
		t.Fatal("showCost = true, want false")
	}
	if opts.post {
		t.Fatal("post = true, want false")
	}
	if opts.model != "test/reviewer" {
		t.Fatalf("model = %q, want test/reviewer", opts.model)
	}
	if opts.criticModel != "test/critic" {
		t.Fatalf("criticModel = %q, want test/critic", opts.criticModel)
	}
	if opts.timeout != 30*time.Second {
		t.Fatalf("timeout = %s, want 30s", opts.timeout)
	}
	if opts.outputFile != "review.md" {
		t.Fatalf("outputFile = %q, want review.md", opts.outputFile)
	}
	if opts.budgetUSD != 0.25 || opts.maxTurns != 3 || opts.maxDiff != 80_000 || opts.maxRetries != 2 {
		t.Fatalf("budget controls = $%.2f/%d/%d/%d, want $0.25/3/80000/2",
			opts.budgetUSD, opts.maxTurns, opts.maxDiff, opts.maxRetries)
	}
	if opts.prRef != "https://github.com/owner/repo/pull/123" {
		t.Fatalf("prRef = %q, want PR URL", opts.prRef)
	}
}

func TestDefaultAutomatedReviewOptionsAndOverrides(t *testing.T) {
	cfg := config.DefaultConfig()
	defaults := defaultAutomatedReviewOptions(cfg)
	if defaults.maxIterations != 0 || defaults.maxRetries != 2 || defaults.maxDiffBytes != 240_000 ||
		defaults.maxCostUSD != 0.15 || defaults.criticReserveUSD != 0 || defaults.approvalCritic ||
		defaults.reasoningEffort != "medium" || !defaults.adaptiveReasoning {
		t.Fatalf("defaults = %#v, want Buckbot defaults", defaults)
	}

	got := defaults.withOverrides(automatedReviewOptions{
		maxIterations: 5,
		maxCostUSD:    0.10,
	})
	if got.maxIterations != 5 || got.maxRetries != 2 || got.maxDiffBytes != 240_000 ||
		got.maxCostUSD != 0.10 || got.criticReserveUSD != 0 || got.approvalCritic {
		t.Fatalf("overrides = %#v, want selective CLI overrides", got)
	}

	cfg.Buckbot.CriticModel = "critic/model"
	withCritic := defaultAutomatedReviewOptions(cfg).withOverrides(automatedReviewOptions{maxCostUSD: 0.10})
	if withCritic.criticReserveUSD != 0.012 || !withCritic.approvalCritic {
		t.Fatalf("critic policy = %#v, want enabled with $0.012 reserve", withCritic)
	}
}

func TestMarkIncompleteReviewPreservesCompletedRevalidationWork(t *testing.T) {
	review := markIncompleteReview(
		"## Summary\n\nCompleted review evidence.",
		"review target could not be revalidated: GitHub rate limited the request",
	)
	for _, want := range []string{
		"Incomplete review",
		"must not be used as an approval",
		"GitHub rate limited the request",
		"Completed review evidence",
	} {
		if !strings.Contains(review, want) {
			t.Fatalf("salvaged review missing %q:\n%s", want, review)
		}
	}
}

func TestParseReviewPRCommandOptionsAcceptsFlagsAfterReference(t *testing.T) {
	opts, err := parseReviewPRCommandOptions([]string{
		"208",
		"-model", "codex/gpt-5.6-terra-high",
		"-timeout=40m",
		"-output", "/tmp/pr208.md",
		"-cost=false",
		"-verbose",
	})
	if err != nil {
		t.Fatalf("parseReviewPRCommandOptions() error = %v", err)
	}
	if opts.prRef != "208" {
		t.Fatalf("prRef = %q, want 208", opts.prRef)
	}
	if opts.model != "codex/gpt-5.6-terra-high" {
		t.Fatalf("model = %q, want Terra High override", opts.model)
	}
	if opts.timeout != 40*time.Minute {
		t.Fatalf("timeout = %s, want 40m", opts.timeout)
	}
	if opts.outputFile != "/tmp/pr208.md" {
		t.Fatalf("outputFile = %q, want /tmp/pr208.md", opts.outputFile)
	}
	if opts.showCost {
		t.Fatal("showCost = true, want false")
	}
	if !opts.verbose {
		t.Fatal("verbose = false, want true")
	}
	if opts.post {
		t.Fatal("post = true, want default false")
	}
}

func TestParseReviewPRCommandOptionsRejectsIgnoredTrailingArguments(t *testing.T) {
	for _, args := range [][]string{
		{"208", "unexpected"},
		{"208", "-unknown"},
	} {
		if _, err := parseReviewPRCommandOptions(args); err == nil {
			t.Fatalf("parseReviewPRCommandOptions(%v) unexpectedly succeeded", args)
		}
	}
}

func TestParseReviewPRCommandOptionsRequiresReference(t *testing.T) {
	_, err := parseReviewPRCommandOptions(nil)
	if err == nil {
		t.Fatal("expected usage error")
	}
	if !strings.Contains(err.Error(), "buckley review-pr <pr-number-or-url>") {
		t.Fatalf("error = %q, want usage", err)
	}
}

func TestWritePRReviewOutputWritesFile(t *testing.T) {
	outputFile := filepath.Join(t.TempDir(), "review.md")

	if err := writePRReviewOutput(outputFile, "review body", nil); err != nil {
		t.Fatalf("writePRReviewOutput() error = %v", err)
	}

	got, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if string(got) != "review body" {
		t.Fatalf("output = %q, want review body", got)
	}
}

func TestPostCompletedPRReviewRetriesWithoutAnotherModelRun(t *testing.T) {
	originalPost := postCompletedBuckbotReviewFn
	originalWait := waitCompletedBuckbotPostRetryFn
	t.Cleanup(func() {
		postCompletedBuckbotReviewFn = originalPost
		waitCompletedBuckbotPostRetryFn = originalWait
	})

	var posts, waits int
	postCompletedBuckbotReviewFn = func(ctx context.Context, event gitwatcher.PullRequestEvent, review string) error {
		posts++
		if event.HeadSHA != "head-sha" || review != "validated review" {
			t.Fatalf("post input = %#v / %q", event, review)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > completedBuckbotPostAttemptLimit {
			t.Fatalf("post attempt deadline = %v, want at most %s", deadline, completedBuckbotPostAttemptLimit)
		}
		if posts == 1 {
			return errors.New("GitHub secondary rate limit (HTTP 403)")
		}
		return nil
	}
	waitCompletedBuckbotPostRetryFn = func(_ context.Context, delay time.Duration) error {
		waits++
		if delay != time.Second {
			t.Fatalf("retry delay = %s, want 1s", delay)
		}
		return nil
	}

	err := postCompletedPRReview(context.Background(), &commands.PRInfo{
		Host:       "github.com",
		Repository: "owner/repo",
		Number:     42,
		HeadSHA:    "head-sha",
	}, "validated review")
	if err != nil {
		t.Fatalf("postCompletedPRReview() error = %v", err)
	}
	if posts != 2 || waits != 1 {
		t.Fatalf("posts/waits = %d/%d, want 2/1", posts, waits)
	}
}

func TestPostedReviewBudgetsStayBelowFiveMinutes(t *testing.T) {
	const observedQwenReview = 4*time.Minute + 9*time.Second
	if defaultReviewTimeout < observedQwenReview {
		t.Fatalf("review budget = %s, want at least observed qwen run %s", defaultReviewTimeout, observedQwenReview)
	}
	if total := defaultReviewTimeout + completedBuckbotPostBudget; total >= 5*time.Minute {
		t.Fatalf("review plus post budget = %s, want less than five minutes", total)
	}
	retryDelay := completedBuckbotPostRetryDelay + 2*completedBuckbotPostRetryDelay
	attemptBudget := maxCompletedBuckbotPostAttempts*completedBuckbotPostAttemptLimit + retryDelay
	if attemptBudget > completedBuckbotPostBudget {
		t.Fatalf("post attempt schedule = %s, exceeds post budget %s", attemptBudget, completedBuckbotPostBudget)
	}
	if defaultReviewTimeout != 4*time.Minute+25*time.Second ||
		completedBuckbotPostBudget != 25*time.Second ||
		completedBuckbotPostAttemptLimit != 7*time.Second {
		t.Fatalf("production budgets changed: review=%s post=%s attempt=%s",
			defaultReviewTimeout, completedBuckbotPostBudget, completedBuckbotPostAttemptLimit)
	}
}
