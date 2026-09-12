package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/experiment"
	"m31labs.dev/buckley/pkg/storage"
)

func TestExperimentShow_ParsesDocumentedArgumentPositions(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "experiments.db")
	t.Setenv(envBuckleyDBPath, dbPath)
	store, err := storage.New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	expStore := experiment.NewStoreFromStorage(store)
	exp := &experiment.Experiment{ID: "show-id", Name: "args-test", Status: experiment.ExperimentFailed, Task: experiment.Task{Prompt: "read fixture"}, Variants: []experiment.Variant{{ID: "v", Name: "fixture", ModelID: "model"}}}
	if err := expStore.CreateExperiment(exp); err != nil {
		t.Fatal(err)
	}
	if err := expStore.SaveRun(&experiment.Run{ID: "r", ExperimentID: exp.ID, VariantID: "v", Status: experiment.RunFailed, Output: "retained evidence"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	oldNoColor := noColor
	noColor = true
	t.Cleanup(func() { noColor = oldNoColor })
	for _, args := range [][]string{
		{"args-test", "--format", "compact"},
		{"show-id", "--format=compact"},
		{"--format", "compact", "args-test"},
		{"--format=compact", "show-id"},
		{"--format", "compact", "--", "args-test"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var callErr error
			output := captureStdout(t, func() { callErr = runExperimentShow(args) })
			if callErr != nil {
				t.Fatal(callErr)
			}
			if !strings.HasPrefix(output, "args-test (failed)\n") || strings.Contains(output, "# Experiment:") || !strings.Contains(output, "model") || !strings.Contains(output, " r ") {
				t.Errorf("args=%v did not select compact output: %s", args, output)
			}
		})
	}
}

func TestExperimentShow_RejectsInvalidArgumentsBeforeStorage(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		reason string
	}{
		{[]string{"missing", "--format", "csv"}, "invalid experiment show format"},
		{[]string{"--format=csv", "missing"}, "invalid experiment show format"},
		{[]string{"missing", "--formatt", "compact"}, "flag provided but not defined"},
		{[]string{"missing", "--format"}, "flag needs an argument"},
		{[]string{"missing", "extra"}, "unexpected trailing argument"},
		{[]string{"--format", "compact", "missing", "extra"}, "unexpected trailing argument"},
		{[]string{"missing", "--", "--format=compact"}, "unexpected trailing argument"},
		{[]string{" "}, "experiment id or name is required"},
		{[]string{}, "usage:"},
		{[]string{"--format", "compact"}, "usage:"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "must-not-open.db")
			t.Setenv(envBuckleyDBPath, dbPath)
			err := runExperimentShow(tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("args=%v error=%v, want %q", tc.args, err, tc.reason)
			}
			if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
				t.Errorf("invalid arguments opened storage: stat error=%v", err)
			}
		})
	}
}
