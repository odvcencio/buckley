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
		"-max-context-tokens", "9000",
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
	if opts.budgetUSD != 0.25 || opts.maxTurns != 3 || opts.maxDiff != 80_000 || opts.maxSupportingContext != 9_000 || opts.maxRetries != 2 {
		t.Fatalf("budget controls = $%.2f/%d/%d/%d/%d, want $0.25/3/80000/9000/2",
			opts.budgetUSD, opts.maxTurns, opts.maxDiff, opts.maxSupportingContext, opts.maxRetries)
	}
	if opts.prRef != "https://github.com/owner/repo/pull/123" {
		t.Fatalf("prRef = %q, want PR URL", opts.prRef)
	}
}

func TestParseReviewPRCommandOptionsRejectsConflictingBudgetModes(t *testing.T) {
	_, err := parseReviewPRCommandOptions([]string{"123", "--budget", "0.25", "--no-budget"})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("error = %v, want conflicting budget modes", err)
	}

	_, err = parseReviewPRCommandOptions([]string{"123", "--budget", "-0.25"})
	if err == nil || !strings.Contains(err.Error(), "must be zero or greater") {
		t.Fatalf("error = %v, want non-negative budget validation", err)
	}

	opts, err := parseReviewPRCommandOptions([]string{"123", "--no-budget"})
	if err != nil {
		t.Fatalf("parseReviewPRCommandOptions(--no-budget) error = %v", err)
	}
	if !opts.noBudget {
		t.Fatal("noBudget = false, want true")
	}
}

func TestDefaultAutomatedReviewOptionsAndOverrides(t *testing.T) {
	cfg := config.DefaultConfig()
	defaults := defaultAutomatedReviewOptions(cfg)
	if defaults.maxIterations != 0 || defaults.maxRetries != 2 || defaults.maxDiffBytes != 240_000 ||
		defaults.maxSupportingContextTokens != 12_000 ||
		defaults.maxCostUSD != 0 || defaults.criticReserveUSD != 0 || !defaults.approvalCritic ||
		defaults.reasoningEffort != "medium" || !defaults.adaptiveReasoning {
		t.Fatalf("defaults = %#v, want Buckbot defaults", defaults)
	}

	got := defaults.withOverrides(automatedReviewOptions{
		maxIterations: 5,
		maxCostUSD:    0.10,
	})
	// The critic ships enabled by default, so an overridden budget
	// recomputes its reserve instead of clearing it.
	if got.maxIterations != 5 || got.maxRetries != 2 || got.maxDiffBytes != 240_000 ||
		got.maxSupportingContextTokens != 12_000 ||
		got.maxCostUSD != 0.10 || got.criticReserveUSD != 0.012 || !got.approvalCritic {
		t.Fatalf("overrides = %#v, want selective CLI overrides", got)
	}

	cfg.Buckbot.CriticModel = "critic/model"
	withCritic := defaultAutomatedReviewOptions(cfg).withOverrides(automatedReviewOptions{maxCostUSD: 0.10})
	if withCritic.criticReserveUSD != 0.012 || !withCritic.approvalCritic {
		t.Fatalf("critic policy = %#v, want enabled with $0.012 reserve", withCritic)
	}

	cfg.Buckbot.PerReviewBudgetUSD = 0.60
	configured := defaultAutomatedReviewOptions(cfg)
	if configured.maxCostUSD != 0.60 || configured.criticReserveUSD != 0.072 {
		t.Fatalf("configured policy = %#v, want explicit $0.60 cap", configured)
	}
	uncapped := configured.withOverrides(automatedReviewOptions{clearCostBudget: true})
	if uncapped.maxCostUSD != 0 || uncapped.criticReserveUSD != 0 {
		t.Fatalf("uncapped policy = %#v, want no monetary cap", uncapped)
	}
}

func TestBoundPRApprovalCriticUsesOnePassOnlyForAuthoritativeCI(t *testing.T) {
	base := automatedReviewOptions{
		approvalCritic:      true,
		criticMaxIterations: 2,
		criticMaxToolCalls:  2,
	}
	passing := boundPRApprovalCritic(base, commands.ReviewPRDef{
		CIStatus:     "passing (3/3)",
		CIProvenance: "pull request head",
	})
	if passing.criticMaxIterations != 1 || passing.criticMaxToolCalls != 1 {
		t.Fatalf("passing CI critic = %#v, want one direct pass", passing)
	}

	pending := boundPRApprovalCritic(base, commands.ReviewPRDef{
		CIStatus:     "pending (2/3)",
		CIProvenance: "pull request head",
	})
	if pending.criticMaxIterations != 2 || pending.criticMaxToolCalls != 2 {
		t.Fatalf("pending CI critic = %#v, want independent verification pass", pending)
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
