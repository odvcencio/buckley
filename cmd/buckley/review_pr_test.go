package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseReviewPRCommandOptions(t *testing.T) {
	opts, err := parseReviewPRCommandOptions([]string{
		"-verbose",
		"-cost=false",
		"-model", "test/reviewer",
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
	if opts.model != "test/reviewer" {
		t.Fatalf("model = %q, want test/reviewer", opts.model)
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
