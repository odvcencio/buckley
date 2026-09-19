package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/experiment"
	"m31labs.dev/buckley/pkg/model"
	"m31labs.dev/buckley/pkg/storage"
)

func TestRunExperimentShow_AllowsFormatBeforeOrAfterIdentifier(t *testing.T) {
	dbPath, expID := seedExperimentShowFixture(t)
	t.Setenv(envBuckleyDBPath, dbPath)
	t.Setenv(envBuckleyDataDir, "")
	oldNoColor := noColor
	noColor = true
	t.Cleanup(func() { noColor = oldNoColor })

	tests := []struct {
		name    string
		args    []string
		want    []string
		notWant []string
	}{
		{
			name: "source first compact",
			args: []string{expID, "--format", "compact"},
			want: []string{
				"#1 provider/model-a",
				"run-show-001",
				"input=",
			},
			notWant: []string{
				"# Experiment:",
				"| Rank | Run |",
				"Model │ Run │ Score",
			},
		},
		{
			name: "flags first terminal",
			args: []string{"--format", "terminal", expID},
			want: []string{
				"Experiment: show parser fixture",
				"Model │ Run │ Score",
				"provider/model-a",
				"run-show-001",
			},
			notWant: []string{
				"# Experiment:",
				"| Rank | Run |",
			},
		},
		{
			name: "flags first json",
			args: []string{"--format", "json", expID},
			want: []string{
				`"kind": "buckley.experiment.snapshot"`,
				`"version": "buckley-experiment-snapshot-v1"`,
				`"provenance": "retained supplied evaluation evidence; not re-executed/attested; export is not an atomic database snapshot; checksum covers snapshot JSON self-consistency only and is not execution attestation; exporter identity is not historical harness identity"`,
				`"Output": "ok"`,
				`"algorithm": "sha256"`,
			},
			notWant: []string{
				"Model │ Run │ Score",
				"# Experiment:",
			},
		},
		{
			name: "source first markdown",
			args: []string{expID, "--format", "markdown"},
			want: []string{
				"# Experiment: show parser fixture",
				"| Rank | Run | Variant | Requested model | Execution identity |",
				"unknown (no model response identity evidence)",
				"run-show-001",
			},
			notWant: []string{
				"Model │ Run │ Score",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var runErr error
			out := captureStdout(t, func() {
				runErr = runExperimentShow(tt.args)
			})
			if runErr != nil {
				t.Fatalf("runExperimentShow(%v): %v", tt.args, runErr)
			}
			for _, want := range tt.want {
				if !strings.Contains(out, want) {
					t.Fatalf("output missing %q:\n%s", want, out)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(out, notWant) {
					t.Fatalf("output unexpectedly contains %q:\n%s", notWant, out)
				}
			}
		})
	}
}

func TestRunExperimentShow_RejectsInvalidFormatAndTrailingArgsBeforeDB(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "invalid format",
			args: []string{"--format", "yaml", "exp-show"},
			want: "invalid experiment show format: yaml",
		},
		{
			name: "source first trailing arg",
			args: []string{"exp-show", "--format", "compact", "extra"},
			want: "unexpected trailing argument: extra",
		},
		{
			name: "flags first trailing arg",
			args: []string{"--format", "compact", "exp-show", "extra"},
			want: "unexpected trailing argument: extra",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "should-not-open", "buckley.db")
			t.Setenv(envBuckleyDBPath, dbPath)
			t.Setenv(envBuckleyDataDir, "")
			err := runExperimentShow(tt.args)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("runExperimentShow(%v) error = %v, want %q", tt.args, err, tt.want)
			}
			if storageExists(dbPath) {
				t.Fatalf("db path exists after early parse rejection: %s", dbPath)
			}
		})
	}
}

func TestRunExperimentCompareSnapshotLoadsOfflineBeforeDependencies(t *testing.T) {
	dbPath, expID := seedExperimentShowFixture(t)
	t.Setenv(envBuckleyDBPath, dbPath)
	t.Setenv(envBuckleyDataDir, "")

	var showErr error
	snapshotJSON := captureStdout(t, func() {
		showErr = runExperimentShow([]string{"--format", "json", expID})
	})
	if showErr != nil {
		t.Fatalf("runExperimentShow json: %v", showErr)
	}
	var exported experiment.ExperimentSnapshot
	if err := json.Unmarshal([]byte(snapshotJSON), &exported); err != nil {
		t.Fatalf("snapshot json unmarshal: %v", err)
	}
	if len(exported.Runs) != 1 || exported.Runs[0].Output != "ok" {
		t.Fatalf("exported runs = %+v", exported.Runs)
	}

	snapshotPath := filepath.Join(t.TempDir(), "snapshot.json")
	if err := os.WriteFile(snapshotPath, []byte(snapshotJSON), 0600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}

	origInit := initDependenciesFn
	t.Cleanup(func() { initDependenciesFn = origInit })
	initDependenciesFn = func() (*config.Config, *model.Manager, *storage.Store, error) {
		t.Fatal("experiment compare --snapshot initialized live dependencies")
		return nil, nil, nil, nil
	}
	t.Setenv(envBuckleyDBPath, filepath.Join(t.TempDir(), "must-not-open", "buckley.db"))

	var compareErr error
	compareJSON := captureStdout(t, func() {
		compareErr = runExperimentCommand([]string{"compare", "--snapshot", snapshotPath})
	})
	if compareErr != nil {
		t.Fatalf("runExperimentCommand compare: %v", compareErr)
	}
	for _, want := range []string{`"ExperimentID": "exp-show-parser"`, `"RunID": "run-show-001"`, `"Verified": true`, `"CostEvidence": {`, `"status": "legacy_known"`} {
		if !strings.Contains(compareJSON, want) {
			t.Fatalf("compare output missing %q:\n%s", want, compareJSON)
		}
	}
}

func seedExperimentShowFixture(t *testing.T) (string, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "buckley.db")
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatalf("storage.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	expStore := experiment.NewStoreFromStorage(store)
	now := time.Date(2026, 9, 4, 18, 0, 0, 0, time.UTC)
	done := now.Add(time.Second)
	exp := &experiment.Experiment{
		ID:          "exp-show-parser",
		Name:        "show parser fixture",
		Task:        experiment.Task{Prompt: "show the experiment"},
		Status:      experiment.ExperimentCompleted,
		CreatedAt:   now,
		CompletedAt: &done,
		Variants: []experiment.Variant{{
			ID:         "variant-show-001",
			Name:       "variant-a",
			ModelID:    "provider/model-a",
			ProviderID: "provider",
		}},
		Criteria: []experiment.SuccessCriterion{{
			Name:   "verified",
			Type:   experiment.CriterionContains,
			Target: "ok",
			Weight: 1,
		}},
	}
	if err := expStore.CreateExperiment(exp); err != nil {
		t.Fatalf("CreateExperiment: %v", err)
	}
	run := &experiment.Run{
		ID:           "run-show-001",
		ExperimentID: exp.ID,
		VariantID:    exp.Variants[0].ID,
		Branch:       "experiment/show-parser",
		Status:       experiment.RunCompleted,
		Output:       "ok",
		Metrics:      experiment.RunMetrics{DurationMs: 1000, PromptTokens: 10, CompletionTokens: 5, TotalCost: 0.01},
		StartedAt:    now,
		CompletedAt:  &done,
	}
	if err := expStore.SaveRun(run); err != nil {
		t.Fatalf("SaveRun: %v", err)
	}
	if err := expStore.ReplaceEvaluations(run.ID, []experiment.CriterionEvaluation{{
		CriterionID: exp.Criteria[0].ID,
		Passed:      true,
		Score:       1,
		Details:     "ok",
		EvaluatedAt: done,
	}}); err != nil {
		t.Fatalf("ReplaceEvaluations: %v", err)
	}
	return dbPath, exp.ID
}

func storageExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
