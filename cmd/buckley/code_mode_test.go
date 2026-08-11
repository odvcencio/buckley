package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"m31labs.dev/buckley/pkg/execmode"
	"m31labs.dev/buckley/pkg/runledger"
)

func TestCodeModeRuntimeCreatesOneDurableRunPerSession(t *testing.T) {
	if execmode.DetectIsolation() != execmode.IsolationBwrap {
		t.Skip("bubblewrap not available")
	}
	dataDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BUCKLEY_DATA_DIR", dataDir)

	runtime, err := openCodeModeRuntime(workspace)
	if err != nil {
		t.Fatalf("openCodeModeRuntime: %v", err)
	}
	first, err := runtime.Tool("session-1")
	if err != nil {
		t.Fatalf("Tool: %v", err)
	}
	second, err := runtime.Tool("session-1")
	if err != nil {
		t.Fatalf("Tool again: %v", err)
	}
	if first != second {
		t.Fatal("same session should reuse one exec_program tool and run")
	}
	if first.Name() != "exec_program" {
		t.Fatalf("tool name = %q, want exec_program", first.Name())
	}

	runs, err := runtime.RunLedger().ListRuns(context.Background(), runledger.RunQuery{SessionID: "session-1"})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != "running" {
		t.Fatalf("runs = %+v, want one running code-mode run", runs)
	}
	runID := runs[0].RunID
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	stores, cleanup, err := openGoalStores()
	if err != nil {
		t.Fatalf("reopen stores: %v", err)
	}
	defer cleanup()
	closedRun, err := stores.ledger.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun after close: %v", err)
	}
	if closedRun.Status != "completed" || closedRun.EndedAt == nil {
		t.Fatalf("closed run = %+v, want completed terminal run", closedRun)
	}
}

func TestCodeModeRuntimeFailureMarksRunFailed(t *testing.T) {
	if execmode.DetectIsolation() != execmode.IsolationBwrap {
		t.Skip("bubblewrap not available")
	}
	dataDir := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BUCKLEY_DATA_DIR", dataDir)

	runtime, err := openCodeModeRuntime(workspace)
	if err != nil {
		t.Fatalf("openCodeModeRuntime: %v", err)
	}
	if _, err := runtime.Tool("failed-session"); err != nil {
		t.Fatalf("Tool: %v", err)
	}
	runs, err := runtime.RunLedger().ListRuns(context.Background(), runledger.RunQuery{SessionID: "failed-session"})
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns = %+v, %v", runs, err)
	}
	if err := runtime.Fail(errors.New("model request failed")); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	stores, cleanup, err := openGoalStores()
	if err != nil {
		t.Fatalf("reopen stores: %v", err)
	}
	defer cleanup()
	failedRun, err := stores.ledger.GetRun(context.Background(), runs[0].RunID)
	if err != nil {
		t.Fatalf("GetRun after failure: %v", err)
	}
	if failedRun.Status != "failed" {
		t.Fatalf("failed run status = %q, want failed", failedRun.Status)
	}
	if got, _ := failedRun.Outcome["error"].(string); got != "model request failed" {
		t.Fatalf("failed run outcome = %+v", failedRun.Outcome)
	}
}
