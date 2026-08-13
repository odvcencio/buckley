package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/durability"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

func newGoalTestLedger(t *testing.T) *runledger.SQLiteStore {
	t.Helper()
	ledger, err := runledger.New(filepath.Join(t.TempDir(), "ledger.db"))
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	return ledger
}

func TestEnsureDurableGoalRunOpen_RejectsTerminalBeforeRuntimeMutation(t *testing.T) {
	ledger := newGoalTestLedger(t)
	run, err := ledger.StartRun(context.Background(), runledger.AgentRun{RunID: "run-terminal", SessionID: "goal-test"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := ledger.EndRun(context.Background(), run.RunID, "completed", time.Now().UTC(), nil); err != nil {
		t.Fatalf("EndRun: %v", err)
	}
	err = ensureDurableGoalRunOpen(context.Background(), ledger, run.RunID)
	if err == nil || !strings.Contains(err.Error(), "already finalized as completed") || !strings.Contains(err.Error(), "goal report") {
		t.Fatalf("ensureDurableGoalRunOpen error = %v", err)
	}
}

func TestDurableGoalResumeFence_UsesLatestIncompleteGeneration(t *testing.T) {
	ledger := newGoalTestLedger(t)
	run, err := ledger.StartRun(context.Background(), runledger.AgentRun{RunID: "run-fence", SessionID: "goal-test"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	appendGeneration := func(instanceID string, incomplete, failure bool) {
		t.Helper()
		generation, err := durableWorkflowGeneration(run.RunID, instanceID)
		if err != nil {
			t.Fatalf("durableWorkflowGeneration: %v", err)
		}
		if _, err := ledger.Append(context.Background(), runledger.Event{
			RunID: run.RunID,
			Type:  runledger.EventDurableGoalGeneration,
			Payload: map[string]any{
				"run_id":               run.RunID,
				"workflow_instance_id": instanceID,
				"generation":           generation,
				"incomplete":           incomplete,
				"failure":              failure,
			},
		}); err != nil {
			t.Fatalf("Append generation: %v", err)
		}
	}
	appendGeneration("goal-run-fence", true, false)
	appendGeneration("goal-run-fence::resume::1", true, false)
	appendGeneration("goal-run-fence::resume::2", false, false)

	fence, err := durableGoalResumeFence(context.Background(), ledger, run.RunID)
	if err != nil {
		t.Fatalf("durableGoalResumeFence: %v", err)
	}
	if fence != "goal-run-fence::resume::1" {
		t.Fatalf("fence = %q, want latest incomplete generation", fence)
	}
}

func TestDurableGoalResumeFence_RejectsNonCanonicalGenerationEvent(t *testing.T) {
	ledger := newGoalTestLedger(t)
	run, err := ledger.StartRun(context.Background(), runledger.AgentRun{RunID: "run-bad-fence", SessionID: "goal-test"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := ledger.Append(context.Background(), runledger.Event{
		RunID: run.RunID,
		Type:  runledger.EventDurableGoalGeneration,
		Payload: map[string]any{
			"run_id":               run.RunID,
			"workflow_instance_id": "goal-run-bad-fence::resume::01",
			"generation":           1,
			"incomplete":           true,
			"failure":              false,
		},
	}); err != nil {
		t.Fatalf("Append generation: %v", err)
	}
	if _, err := durableGoalResumeFence(context.Background(), ledger, run.RunID); err == nil {
		t.Fatal("durableGoalResumeFence accepted a non-canonical generation event")
	}
}

func TestDurableGoalResumeFence_RejectsMalformedGenerationFacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing run ID", mutate: func(payload map[string]any) { delete(payload, "run_id") }},
		{name: "foreign run ID", mutate: func(payload map[string]any) { payload["run_id"] = "run-other" }},
		{name: "missing instance ID", mutate: func(payload map[string]any) { delete(payload, "workflow_instance_id") }},
		{name: "non-string instance ID", mutate: func(payload map[string]any) { payload["workflow_instance_id"] = 7 }},
		{name: "foreign instance ID", mutate: func(payload map[string]any) { payload["workflow_instance_id"] = "goal-run-other" }},
		{name: "missing generation", mutate: func(payload map[string]any) { delete(payload, "generation") }},
		{name: "fractional generation", mutate: func(payload map[string]any) { payload["generation"] = 0.5 }},
		{name: "mismatched generation", mutate: func(payload map[string]any) { payload["generation"] = 1 }},
		{name: "missing incomplete", mutate: func(payload map[string]any) { delete(payload, "incomplete") }},
		{name: "non-boolean incomplete", mutate: func(payload map[string]any) { payload["incomplete"] = "true" }},
		{name: "missing failure", mutate: func(payload map[string]any) { delete(payload, "failure") }},
		{name: "non-boolean failure", mutate: func(payload map[string]any) { payload["failure"] = "false" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ledger := newGoalTestLedger(t)
			run, err := ledger.StartRun(context.Background(), runledger.AgentRun{RunID: "run-malformed-fact", SessionID: "goal-test"})
			if err != nil {
				t.Fatalf("StartRun: %v", err)
			}
			payload := map[string]any{
				"run_id":               run.RunID,
				"workflow_instance_id": "goal-run-malformed-fact",
				"generation":           0,
				"incomplete":           true,
				"failure":              false,
			}
			tc.mutate(payload)
			if _, err := ledger.Append(context.Background(), runledger.Event{
				RunID:   run.RunID,
				Type:    runledger.EventDurableGoalGeneration,
				Payload: payload,
			}); err != nil {
				t.Fatalf("Append generation: %v", err)
			}
			if _, err := durableGoalResumeFence(context.Background(), ledger, run.RunID); err == nil {
				t.Fatal("durableGoalResumeFence accepted a malformed scheduler fact")
			}
		})
	}
}

func TestDurableGoalResumeFence_RejectsConflictingGenerationFacts(t *testing.T) {
	ledger := newGoalTestLedger(t)
	run, err := ledger.StartRun(context.Background(), runledger.AgentRun{RunID: "run-conflicting-fact", SessionID: "goal-test"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for _, incomplete := range []bool{true, false} {
		if _, err := ledger.Append(context.Background(), runledger.Event{
			RunID: run.RunID,
			Type:  runledger.EventDurableGoalGeneration,
			Payload: map[string]any{
				"run_id":               run.RunID,
				"workflow_instance_id": "goal-run-conflicting-fact",
				"generation":           0,
				"incomplete":           incomplete,
				"failure":              false,
			},
		}); err != nil {
			t.Fatalf("Append generation: %v", err)
		}
	}
	if _, err := durableGoalResumeFence(context.Background(), ledger, run.RunID); err == nil || !strings.Contains(err.Error(), "conflicting ledger facts") {
		t.Fatalf("durableGoalResumeFence conflict error = %v", err)
	}
}

func TestDurableGoalIncompleteMessage_IsExplicitlyResumable(t *testing.T) {
	message := durableGoalIncompleteMessage("goal-run-1::resume::2", "run-1", 3)
	if !strings.Contains(message, "bounded generation") || !strings.Contains(message, "3 deferred task(s)") || !strings.Contains(message, "buckley goal run run-1") || !strings.Contains(message, "next durable generation") {
		t.Fatalf("incomplete message = %q", message)
	}
}

type durableGoalTestEngine struct{}

func (durableGoalTestEngine) RunTurn(context.Context, goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	return goalloop.TurnOutcome{}, nil
}

func TestGoalFinalizationForStatus_PreservesTerminalObservation(t *testing.T) {
	status := durability.GoalStatus{
		InstanceID:    "goal-run-1",
		RuntimeStatus: "COMPLETED",
		Result: durability.GoalResult{
			Status:        durability.GoalResultIncomplete,
			DeferredTasks: []string{"task-1"},
		},
	}

	got := goalFinalizationForStatus("run-1", "/work/repo", status.InstanceID, status)
	if got.RunID != "run-1" || got.WorkspaceRoot != "/work/repo" || got.WorkflowInstanceID != status.InstanceID {
		t.Fatalf("finalization identity = %+v", got)
	}
	if !got.Incomplete || got.Failure != "" {
		t.Fatalf("finalization lifecycle = %+v, want resumable incomplete observation", got)
	}
}

func TestGoalFinalizationForStatus_FailureRemainsTerminal(t *testing.T) {
	status := durability.GoalStatus{
		InstanceID:    "goal-run-2",
		RuntimeStatus: "FAILED",
		Failure:       "child workflow failed after fan-in",
	}

	got := goalFinalizationForStatus("run-2", "/work/repo", status.InstanceID, status)
	if got.Incomplete || got.Failure != status.Failure {
		t.Fatalf("finalization lifecycle = %+v, want terminal failure", got)
	}
}

func TestGoalFinalizationForStatus_SynthesizesTerminalRuntimeFailure(t *testing.T) {
	status := durability.GoalStatus{InstanceID: "goal-run-3", RuntimeStatus: "CANCELED"}

	got := goalFinalizationForStatus("run-3", "/work/repo", status.InstanceID, status)
	if got.Failure != "durable workflow ended with status canceled" {
		t.Fatalf("finalization failure = %q", got.Failure)
	}
}

func TestNewDurableGoalRunners_WorkerResolvesConcurrentRun(t *testing.T) {
	ctx := context.Background()
	workDir := t.TempDir()
	ev, err := evidence.New(filepath.Join(workDir, "durable-goals.db"), evidence.WithBlobRoot(filepath.Join(workDir, "blobs")))
	if err != nil {
		t.Fatalf("evidence.New: %v", err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	ledger, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	checkpoints, err := taskstate.NewManager(ledger, ev)
	if err != nil {
		t.Fatalf("taskstate.NewManager: %v", err)
	}
	loop, err := goalloop.New(goalloop.Config{
		Ledger:      ledger,
		Checkpoints: checkpoints,
		Engine:      durableGoalTestEngine{},
		SessionID:   "durable-runner-test",
	})
	if err != nil {
		t.Fatalf("goalloop.New: %v", err)
	}
	first, err := loop.Start(ctx, goalloop.Goal{Statement: "first goal", WorkspaceRoot: workDir})
	if err != nil {
		t.Fatalf("start first goal: %v", err)
	}
	second, err := loop.Start(ctx, goalloop.Goal{Statement: "second goal", WorkspaceRoot: workDir})
	if err != nil {
		t.Fatalf("start second goal: %v", err)
	}
	firstSpecs := map[string]goalloop.TaskSpec{}
	for _, task := range first.Tasks {
		firstSpecs[task.TaskID] = task.Spec
	}

	local, worker, err := newDurableGoalRunners(loop, first.RunID, workDir, first.Goal, firstSpecs)
	if err != nil {
		t.Fatalf("newDurableGoalRunners: %v", err)
	}
	if _, err := local.NextBatch(ctx, durability.NextBatchRequest{RunID: second.RunID}); err == nil {
		t.Fatal("one-run finalization runner accepted a concurrent foreign run")
	}
	batch, err := worker.NextBatch(ctx, durability.NextBatchRequest{RunID: second.RunID})
	if err != nil {
		t.Fatalf("resolver worker could not serve concurrent run: %v", err)
	}
	if batch.Done || len(batch.Tasks) != 1 || batch.Tasks[0].TaskID != second.Tasks[0].TaskID {
		t.Fatalf("resolved concurrent batch = %+v", batch)
	}
}
