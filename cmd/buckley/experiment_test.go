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

func TestCalibratedExperimentProfiles_SkipsZeroObservationAttributionCaveats(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	profiles := storage.NewBehaviorProfileStore(store)
	taskSuccessKnown := true
	calibrations := []experiment.ModelCalibration{
		{
			ModelID:            "requested/model",
			AttributionCaveats: []string{"run skipped: selected model differed"},
		},
		{
			ModelID: "good/model", ProviderID: "openrouter", MeasuredAt: time.Now(),
			Observations: []modelprofile.Observation{{Succeeded: true, TaskSuccessObserved: &taskSuccessKnown}},
		},
	}
	got, err := calibratedExperimentProfiles(context.Background(), profiles, calibrations, "experiment-exp-1", "", true)
	if err != nil {
		t.Fatalf("calibratedExperimentProfiles: %v", err)
	}
	if len(got) != 1 || got[0].ModelID != "good/model" {
		t.Fatalf("profiles = %+v, want only attributable profile", got)
	}
	if stored, err := profiles.List(context.Background(), "requested/model"); err != nil || len(stored) != 0 {
		t.Fatalf("skipped model profiles = %+v, %v; want none", stored, err)
	}
}

func TestCalibratedExperimentProfilesRejectsProviderKeyCollision(t *testing.T) {
	store, err := storage.New(filepath.Join(t.TempDir(), "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	profiles := storage.NewBehaviorProfileStore(store)
	taskSuccessKnown := true
	calibrations := []experiment.ModelCalibration{
		{
			ModelID:    "same/model",
			ProviderID: "provider-a",
			MeasuredAt: time.Now(),
			Observations: []modelprofile.Observation{{
				Succeeded:           true,
				TaskSuccessObserved: &taskSuccessKnown,
			}},
		},
		{
			ModelID:    "same/model",
			ProviderID: "provider-b",
			MeasuredAt: time.Now(),
			Observations: []modelprofile.Observation{{
				Succeeded:           true,
				TaskSuccessObserved: &taskSuccessKnown,
			}},
		},
	}
	_, err = calibratedExperimentProfiles(context.Background(), profiles, calibrations, "experiment-exp-1", "", true)
	if err == nil || !strings.Contains(err.Error(), "multiple providers") {
		t.Fatalf("calibratedExperimentProfiles error = %v, want provider collision", err)
	}
	if stored, err := profiles.List(context.Background(), "same/model"); err != nil || len(stored) != 0 {
		t.Fatalf("stored profiles = %+v, %v; want none after provider collision", stored, err)
	}
}

func TestWriteExperimentProfiles_ShowsAttributionCaveats(t *testing.T) {
	profile := modelprofile.Profile{
		ModelID: "good/model", Version: "experiment-exp-1", SampleSize: 1,
		Samples: modelprofile.SampleCounts{TaskSuccess: 1},
		Metrics: modelprofile.Metrics{TaskSuccessRate: 1},
	}
	var out bytes.Buffer
	if err := writeExperimentProfiles(&out, []modelprofile.Profile{profile}, true, []string{"requested/model: run skipped: selected model differed"}); err != nil {
		t.Fatalf("writeExperimentProfiles: %v", err)
	}
	for _, want := range []string{"Attribution caveats:", "requested/model: run skipped: selected model differed"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("profile output missing %q:\n%s", want, out.String())
		}
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

func TestWriteExperimentProfiles_ShowsAttemptsSeparateFromAssessedOutcomes(t *testing.T) {
	profiles := []modelprofile.Profile{
		{
			ModelID: "unknown/model", Version: "experiment-exp-unknown", SampleSize: 2,
			Samples: modelprofile.SampleCounts{TaskSuccessUnknown: 2, Latency: 2, Cost: 2},
			Metrics: modelprofile.Metrics{AverageTaskLatencyMS: 150, AverageCostUSDPerTask: 0.03},
		},
		{
			ModelID: "mixed/model", Version: "experiment-exp-mixed", SampleSize: 3,
			Samples: modelprofile.SampleCounts{TaskSuccess: 2, TaskSuccessUnknown: 1, Latency: 3, Cost: 3},
			Metrics: modelprofile.Metrics{TaskSuccessRate: 0.5, AverageTaskLatencyMS: 200, AverageCostUSDPerTask: 0.04},
		},
	}
	var out bytes.Buffer
	if err := writeExperimentProfiles(&out, profiles, true); err != nil {
		t.Fatalf("writeExperimentProfiles: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"MODEL", "TASKS", "ASSESSED",
		"unknown/model",
		"150ms", "$0.0300",
		"mixed/model",
		"200ms", "$0.0400",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("profile output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "$0.0000") {
		t.Fatalf("profile output should not render unavailable cost/success as zero:\n%s", got)
	}
	rows := profileOutputRows(got)
	if got := rows["unknown/model"]; len(got) < 10 || got[2] != "2" || got[3] != "0" || got[4] != "-" {
		t.Fatalf("unknown/model row = %v, want tasks=2 assessed=0 success=-\n%s", got, out.String())
	}
	if got := rows["mixed/model"]; len(got) < 10 || got[2] != "3" || got[3] != "2" || got[4] != "50.0%" {
		t.Fatalf("mixed/model row = %v, want tasks=3 assessed=2 success=50.0%%\n%s", got, out.String())
	}
}

func profileOutputRows(output string) map[string][]string {
	rows := make(map[string][]string)
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			rows[fields[0]] = fields
		}
	}
	return rows
}

func TestRunExperimentPromote_PromotesStoredCandidate(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(envBuckleyDataDir, dataDir)
	store, err := storage.New(filepath.Join(dataDir, "buckley.db"))
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	profile := modelprofile.Profile{
		SchemaVersion: modelprofile.SchemaVersion,
		ModelID:       "cheap/model",
		Version:       "experiment-exp-1",
		Class:         modelprofile.ClassBalanced,
		SampleSize:    20,
		Confidence:    0.9,
		MeasuredAt:    time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC),
		Capabilities:  modelprofile.Capabilities{ToolCalls: true},
		Metrics: modelprofile.Metrics{
			ToolReliability:             0.9,
			ArgumentRepairReliability:   0.9,
			StructuredOutputReliability: 0.9,
			ParallelCallReliability:     0.9,
			EditFidelity:                0.9,
			VerificationPassRate:        0.9,
			ContinuationReliability:     0.9,
		},
	}
	if err := storage.NewBehaviorProfileStore(store).Put(context.Background(), profile); err != nil {
		t.Fatalf("Put profile: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close setup store: %v", err)
	}
	digest, err := profile.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runExperimentCommand([]string{"promote", "cheap/model", "experiment-exp-1"}); err != nil {
			t.Fatalf("runExperimentCommand promote: %v", err)
		}
	})
	for _, want := range []string{"Promoted model behavior profile", "cheap/model", "experiment-exp-1", digest, "balanced"} {
		if !strings.Contains(out, want) {
			t.Fatalf("promote output missing %q:\n%s", want, out)
		}
	}

	reopened, err := storage.New(filepath.Join(dataDir, "buckley.db"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer reopened.Close()
	promoted, ok, err := storage.NewBehaviorProfileStore(reopened).Promoted(context.Background(), "cheap/model")
	if err != nil || !ok || promoted.Version != "experiment-exp-1" {
		t.Fatalf("Promoted = %+v, %v, %v", promoted, ok, err)
	}
}

func TestRunExperimentPromote_MissingCandidateFails(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(envBuckleyDataDir, dataDir)
	err := runExperimentCommand([]string{"promote", "cheap/model", "missing"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("runExperimentCommand promote missing candidate err = %v, want not found", err)
	}
}
