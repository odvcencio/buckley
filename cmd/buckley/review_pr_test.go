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
	if opts.prRef != "https://github.com/owner/repo/pull/123" {
		t.Fatalf("prRef = %q, want PR URL", opts.prRef)
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
