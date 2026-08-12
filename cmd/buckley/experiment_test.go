package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/experiment"
	"m31labs.dev/buckley/pkg/modelprofile"
	"m31labs.dev/buckley/pkg/storage"
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

func TestSplitExperimentProfileArgs_AllowsFlagsBeforeOrAfterIdentifier(t *testing.T) {
	for _, args := range [][]string{
		{"exp-1", "--class", "frontier", "--dry-run"},
		{"--class", "frontier", "--dry-run", "exp-1"},
	} {
		identifier, flags, err := splitExperimentProfileArgs(args)
		if err != nil {
			t.Fatalf("splitExperimentProfileArgs(%v): %v", args, err)
		}
		if identifier != "exp-1" || strings.Join(flags, " ") != "--class frontier --dry-run" {
			t.Fatalf("splitExperimentProfileArgs(%v) = %q, %v", args, identifier, flags)
		}
	}
}

func TestParseExperimentProfileClass(t *testing.T) {
	if class, err := parseExperimentProfileClass("auto"); err != nil || class != "" {
		t.Fatalf("auto class = %q, %v", class, err)
	}
	if class, err := parseExperimentProfileClass("FRONTIER"); err != nil || class != modelprofile.ClassFrontier {
		t.Fatalf("frontier class = %q, %v", class, err)
	}
	if _, err := parseExperimentProfileClass("premium"); err == nil {
		t.Fatal("expected invalid class error")
	}
}

func TestCalibratedExperimentProfiles_PersistenceIsIdempotent(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	profiles := storage.NewBehaviorProfileStore(store)
	calibrations := []experiment.ModelCalibration{{
		ModelID: "cheap/model", ProviderID: "openrouter", MeasuredAt: time.Now(),
		Observations: []modelprofile.Observation{{Succeeded: true, LatencyMS: 50}},
	}}
	for range 2 {
		got, err := calibratedExperimentProfiles(context.Background(), profiles, calibrations, "experiment-exp-1", "", true)
		if err != nil {
			t.Fatalf("calibratedExperimentProfiles: %v", err)
		}
		if len(got) != 1 || got[0].SampleSize != 1 {
			t.Fatalf("profiles = %+v", got)
		}
	}
	stored, err := profiles.List(context.Background(), "cheap/model")
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored profiles = %+v, %v", stored, err)
	}
}

func TestWriteExperimentProfiles_ShowsUsableEfficiencySummary(t *testing.T) {
	profile := modelprofile.Profile{
		ModelID: "cheap/model", Version: "experiment-exp-1", SampleSize: 2,
		Samples: modelprofile.SampleCounts{TaskSuccess: 2, Latency: 2, Tokens: 2, Cost: 2},
		Metrics: modelprofile.Metrics{TaskSuccessRate: 0.5, AverageTaskLatencyMS: 200, AverageTokensPerTask: 150, AverageCostUSDPerTask: 0.02, CostUSDPerSuccessfulTask: 0.04},
	}
	var out bytes.Buffer
	if err := writeExperimentProfiles(&out, []modelprofile.Profile{profile}, true); err != nil {
		t.Fatalf("writeExperimentProfiles: %v", err)
	}
	for _, want := range []string{"cheap/model", "50.0%", "200ms", "$0.0200", "$0.0400", "preview"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("profile output missing %q:\n%s", want, out.String())
		}
	}
}
