package main

import (
	"bytes"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/experiment"
)

func TestParseExperimentDiffOptions(t *testing.T) {
	opts, err := parseExperimentDiffOptions([]string{"exp-1", "--output", "--max-output", "42"})
	if err != nil {
		t.Fatalf("parseExperimentDiffOptions: %v", err)
	}
	if opts.identifier != "exp-1" {
		t.Fatalf("identifier = %q, want exp-1", opts.identifier)
	}
	if !opts.showOutput {
		t.Fatal("showOutput = false, want true")
	}
	if opts.maxOutputLen != 42 {
		t.Fatalf("maxOutputLen = %d, want 42", opts.maxOutputLen)
	}
}

func TestParseExperimentDiffOptionsRequiresIdentifier(t *testing.T) {
	_, err := parseExperimentDiffOptions(nil)
	if err == nil {
		t.Fatal("expected usage error")
	}
	if !strings.Contains(err.Error(), "experiment diff <id|name>") {
		t.Fatalf("error = %q, want usage", err)
	}
}

func TestExperimentVariantNamePrefersModelID(t *testing.T) {
	if got := experimentVariantName(experiment.Variant{Name: "friendly", ModelID: "provider/model"}); got != "provider/model" {
		t.Fatalf("variant name = %q, want provider/model", got)
	}
	if got := experimentVariantName(experiment.Variant{Name: "friendly"}); got != "friendly" {
		t.Fatalf("variant name = %q, want friendly", got)
	}
}

func TestWriteExperimentDiff(t *testing.T) {
	exp := &experiment.Experiment{
		ID:   "exp-1",
		Name: "model shootout",
		Variants: []experiment.Variant{
			{ID: "v1", Name: "fast", ModelID: "qwen/flash"},
			{ID: "v2", Name: "careful"},
		},
	}
	errText := "tests failed"
	runs := []experiment.Run{
		{
			VariantID: "v1",
			Status:    experiment.RunCompleted,
			Output:    "short output",
			Files:     []string{"main.go"},
			Metrics: experiment.RunMetrics{
				PromptTokens:     10,
				CompletionTokens: 5,
				TotalCost:        0.0123,
				DurationMs:       1200,
			},
		},
		{
			VariantID: "v2",
			Status:    experiment.RunFailed,
			Output:    "abcdefghijklmnopqrstuvwxyz",
			Error:     &errText,
		},
	}

	var out bytes.Buffer
	if err := writeExperimentDiff(&out, exp, runs, experimentDiffOptions{maxOutputLen: 8}); err != nil {
		t.Fatalf("writeExperimentDiff: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"# Experiment Diff: model shootout",
		"qwen/flash",
		"careful",
		"$0.0123",
		"main.go",
		"**Error:** tests failed",
		"abcdefgh...",
		"_(truncated, use --output to see full)_",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestWriteExperimentRunOutputFullOutput(t *testing.T) {
	var out bytes.Buffer
	writeExperimentRunOutput(&out, " abcdef ", experimentDiffOptions{showOutput: true, maxOutputLen: 3})

	got := out.String()
	if !strings.Contains(got, "abcdef") {
		t.Fatalf("full output missing: %q", got)
	}
	if strings.Contains(got, "abc...") {
		t.Fatalf("full output was truncated: %q", got)
	}
}
