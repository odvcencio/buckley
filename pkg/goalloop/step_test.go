package goalloop

import (
	"context"
	"fmt"
	"testing"

	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

// TestLoop_TurnStepDrivesTaskStepwise drives a task turn by turn the way
// a durable workflow does: seed once, then one TurnStep per activity
// call, carrying only the snapshot and counters between calls.
func TestLoop_TurnStepDrivesTaskStepwise(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []TurnOutcome{
		{Rounds: 3, ToolCalls: 5, StateChanged: true, Summary: "ported two files"},
		{Rounds: 2, ToolCalls: 2, Completed: true, CompletedEvidenceID: "ev_done", Summary: "all files ported"},
	}}
	loop, ledger := newTestLoop(t, Config{Engine: engine})
	ctx := context.Background()

	intake, err := loop.Start(ctx, Goal{Statement: "port files"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	taskID := intake.Tasks[0].TaskID

	seed, err := loop.SeedTask(ctx, taskID, intake.Tasks[0].Spec)
	if err != nil {
		t.Fatalf("SeedTask: %v", err)
	}
	snapshot := seed.Drive
	generation, turnIndex := seed.Generation, 0

	var kinds []StepKind
	for turn := 0; turn < 4; turn++ {
		step, err := loop.TurnStep(ctx, TurnStepRequest{
			RunID:              intake.RunID,
			TaskID:             taskID,
			Goal:               intake.Goal,
			Spec:               intake.Tasks[0].Spec,
			Generation:         generation,
			TurnIndex:          turnIndex,
			Drive:              snapshot,
			WorkflowInstanceID: "wf-test-1",
			ActivityName:       "run_turn",
		})
		if err != nil {
			t.Fatalf("TurnStep %d: %v", turn, err)
		}
		kinds = append(kinds, step.Kind)
		snapshot = step.Drive
		turnIndex++
		if step.Kind == StepCheckpoint {
			generation++
			turnIndex = 0
		}
		if step.Kind == StepCompleted {
			if step.Status != taskstate.StatusCompleted {
				t.Fatalf("completed step status = %s", step.Status)
			}
			break
		}
	}
	if len(kinds) != 2 || kinds[0] != StepContinue || kinds[1] != StepCompleted {
		t.Fatalf("kinds = %v, want [continue completed]", kinds)
	}

	// The engine saw the stable turn identities the workflow supplied.
	if len(engine.seen) != 2 {
		t.Fatalf("engine saw %d turns, want 2", len(engine.seen))
	}
	// Start writes each task's initial checkpoint, so the seed generation
	// is that checkpoint's version — the same seed RunTask uses.
	wantPrefix := fmt.Sprintf("%s/cp-%03d", taskID, seed.Generation)
	wantTurnIDs := []string{wantPrefix + "/turn-000", wantPrefix + "/turn-001"}
	for i, seen := range engine.seen {
		if seen.TurnID != wantTurnIDs[i] {
			t.Fatalf("turn %d ID = %s, want %s", i, seen.TurnID, wantTurnIDs[i])
		}
	}

	// Snapshot state carried between calls: the second turn absorbed the
	// first turn's summary before its own.
	if snapshot.Summary != "all files ported" {
		t.Fatalf("final snapshot summary = %q", snapshot.Summary)
	}

	// The durable scheduler identity landed on the ledger.
	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	durableTurns := 0
	for _, ev := range events {
		if ev.Type != runledger.EventDurableTurn {
			continue
		}
		durableTurns++
		if ev.Payload["workflow_instance_id"] != "wf-test-1" {
			t.Fatalf("durable.turn payload = %+v", ev.Payload)
		}
	}
	if durableTurns != 2 {
		t.Fatalf("durable.turn events = %d, want 2", durableTurns)
	}
}

// TestLoop_TurnStepSkipsDurableEventLocally keeps the local drive free
// of durable projection noise.
func TestLoop_TurnStepSkipsDurableEventLocally(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []TurnOutcome{
		{Rounds: 1, Completed: true, CompletedEvidenceID: "ev_done", Summary: "done"},
	}}
	loop, ledger := newTestLoop(t, Config{Engine: engine})
	ctx := context.Background()

	intake, err := loop.Start(ctx, Goal{Statement: "small fix"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	taskID := intake.Tasks[0].TaskID
	if _, err := loop.RunTask(ctx, intake.RunID, taskID, intake.Goal, intake.Tasks[0].Spec); err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	for _, ev := range events {
		if ev.Type == runledger.EventDurableTurn {
			t.Fatalf("local drive recorded durable.turn: %+v", ev.Payload)
		}
	}
}
