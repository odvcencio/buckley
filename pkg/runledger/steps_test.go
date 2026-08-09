package runledger

import (
	"context"
	"testing"
	"time"
)

func TestExecutionStepJournal_ReplaysCompletedOutput(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-steps"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	first, replay, err := store.BeginStep(ctx, ExecutionStep{
		RunID:       run.RunID,
		TaskID:      "task-1",
		StepID:      "run/task/turn/model-1",
		Kind:        "model",
		InputDigest: "input-a",
	})
	if err != nil {
		t.Fatalf("BeginStep first: %v", err)
	}
	if replay || first.Attempt != 1 || first.Status != StepStarted {
		t.Fatalf("first = %+v, replay=%v; want attempt 1 started and no replay", first, replay)
	}

	if err := store.CompleteStep(ctx, run.RunID, first.StepID, "ev_response", "output-a", time.Now().UTC()); err != nil {
		t.Fatalf("CompleteStep: %v", err)
	}

	second, replay, err := store.BeginStep(ctx, ExecutionStep{
		RunID:       run.RunID,
		TaskID:      "task-1",
		StepID:      first.StepID,
		Kind:        "model",
		InputDigest: "input-a",
	})
	if err != nil {
		t.Fatalf("BeginStep replay: %v", err)
	}
	if !replay || second.Status != StepCompleted || second.OutputEvidenceID != "ev_response" || second.Attempt != 1 {
		t.Fatalf("replayed step = %+v, replay=%v; want completed attempt 1 with output", second, replay)
	}
}

func TestExecutionStepJournal_RetryPreservesIdentityAndAdvancesAttempt(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-steps"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	first, _, err := store.BeginStep(ctx, ExecutionStep{
		RunID:       run.RunID,
		TaskID:      "task-1",
		StepID:      "run/task/turn/tool-1",
		Kind:        "tool",
		InputDigest: "input-a",
	})
	if err != nil {
		t.Fatalf("BeginStep first: %v", err)
	}
	if err := store.FailStep(ctx, run.RunID, first.StepID, "temporary failure", time.Now().UTC()); err != nil {
		t.Fatalf("FailStep: %v", err)
	}

	retry, replay, err := store.BeginStep(ctx, ExecutionStep{
		RunID:       run.RunID,
		TaskID:      "task-1",
		StepID:      first.StepID,
		Kind:        "tool",
		InputDigest: "input-a",
	})
	if err != nil {
		t.Fatalf("BeginStep retry: %v", err)
	}
	if replay || retry.Attempt != 2 || retry.Status != StepStarted || retry.StepID != first.StepID {
		t.Fatalf("retry = %+v, replay=%v; want same identity, attempt 2 started", retry, replay)
	}
}

func TestExecutionStepJournal_RejectsInputDrift(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-steps"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	_, _, err = store.BeginStep(ctx, ExecutionStep{
		RunID:       run.RunID,
		StepID:      "run/task/turn/model-1",
		Kind:        "model",
		InputDigest: "input-a",
	})
	if err != nil {
		t.Fatalf("BeginStep first: %v", err)
	}
	if _, _, err := store.BeginStep(ctx, ExecutionStep{
		RunID:       run.RunID,
		StepID:      "run/task/turn/model-1",
		Kind:        "model",
		InputDigest: "input-b",
	}); err == nil {
		t.Fatal("BeginStep accepted input drift for the same logical step")
	}
}
