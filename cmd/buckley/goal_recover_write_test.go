package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/tool"
	"m31labs.dev/buckley/pkg/tool/builtin"
)

func TestGoalRecoverWrite_DispatchedWithoutReceipt(t *testing.T) {
	ctx := t.Context()
	store, err := evidence.New(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ledger, err := runledger.NewWithDB(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	run, err := ledger.StartRun(ctx, runledger.AgentRun{SessionID: "write-test"})
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name, finalContent, wantError string
		blocked                       bool
	}{
		{"applied", "desired", "", false},
		{"blocked after dispatch", "desired", "", true},
		{"original", "before", "pre-write content", false},
		{"user edited", "user edit", "different file content", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			workspace := t.TempDir()
			path := filepath.Join(workspace, "note.txt")
			if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
				t.Fatal(err)
			}
			step, _, err := ledger.BeginStep(ctx, runledger.ExecutionStep{RunID: run.RunID, StepID: scenario.name,
				Kind: "tool", InputDigest: "request-digest"})
			if err != nil {
				t.Fatal(err)
			}
			if err := ledger.MarkStepDispatched(ctx, step, time.Now()); err != nil {
				t.Fatal(err)
			}
			registry := tool.NewRegistry()
			registry.SetWorkDir(workspace)
			toolCtx := builtin.WithWriteFileIntent(ctx, func(ctx context.Context, intent builtin.WriteFileIntent) error {
				return recordGoalWriteIntent(ctx, ledger, store, step.RunID, step.StepID, intent)
			})
			result, err := registry.ExecuteWithContext(toolCtx, "write_file", map[string]any{"path": "note.txt", "content": "desired"})
			if err != nil || result == nil || !result.Success {
				t.Fatalf("write_file: result=%+v err=%v", result, err)
			}
			// Simulate losing the dispatcher before it can persist its result.
			if scenario.blocked {
				if err := ledger.BlockStep(ctx, step, "effect uncertain", "", "", time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			if scenario.finalContent != "desired" {
				if err := os.WriteFile(path, []byte(scenario.finalContent), 0600); err != nil {
					t.Fatal(err)
				}
			}
			timestamp := time.Unix(1700000000, 0)
			if err := os.Chtimes(path, timestamp, timestamp); err != nil {
				t.Fatal(err)
			}
			receipt, err := recoverGoalWrite(ctx, ledger, store, run.RunID, step.StepID)
			if scenario.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), scenario.wantError) {
					t.Fatalf("recover: %v, want %q", err, scenario.wantError)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				var outcome agentloop.ToolOutcome
				if err := json.Unmarshal(receipt.InlineBody, &outcome); err != nil || !outcome.Success || !outcome.StateChanged {
					t.Fatalf("invalid replay outcome: %+v err=%v", outcome, err)
				}
				repeat, err := recoverGoalWrite(ctx, ledger, store, run.RunID, step.StepID)
				if err != nil || repeat.ID != receipt.ID {
					t.Fatalf("repeat recovery: receipt=%s err=%v", repeat.ID, err)
				}
				updated, err := ledger.GetStep(ctx, run.RunID, step.StepID)
				if err != nil || updated.Status != runledger.StepCompleted || updated.OutputEvidenceID != receipt.ID {
					t.Fatalf("step receipt: %+v err=%v", updated, err)
				}
			}
			info, err := os.Stat(path)
			if err != nil || !info.ModTime().Equal(timestamp) {
				t.Fatalf("recovery changed file metadata: %v, %v", info, err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != scenario.finalContent {
				t.Fatalf("recovery changed file contents: %q, %v", data, err)
			}
		})
	}
}
