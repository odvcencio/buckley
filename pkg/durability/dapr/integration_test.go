package dapr_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/durability"
	"m31labs.dev/buckley/pkg/durability/dapr"
	"m31labs.dev/buckley/pkg/durability/goalrunner"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

type completingEngine struct{ calls int }

func (e *completingEngine) RunTurn(_ context.Context, _ goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	e.calls++
	if e.calls == 1 {
		return goalloop.TurnOutcome{Rounds: 1, StateChanged: true, Summary: "first turn"}, nil
	}
	return goalloop.TurnOutcome{Rounds: 1, Completed: true, CompletedEvidenceID: "ev_it", Summary: "done"}, nil
}

// TestDaprBackend_GoalCompletes runs the Phase 1 happy path against a
// real sidecar. Set BUCKLEY_DAPR_TEST_ENDPOINT (for example
// localhost:50001 under `dapr run`) to enable it; CI without a sidecar
// skips.
func TestDaprBackend_GoalCompletes(t *testing.T) {
	endpoint := os.Getenv("BUCKLEY_DAPR_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("BUCKLEY_DAPR_TEST_ENDPOINT is not set; skipping sidecar integration test")
	}

	dir := t.TempDir()
	ev, err := evidence.New(filepath.Join(dir, "shared.db"), evidence.WithBlobRoot(filepath.Join(dir, "blobs")))
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
		Engine:      &completingEngine{},
		SessionID:   "dapr-it",
	})
	if err != nil {
		t.Fatalf("goalloop.New: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	intake, err := loop.Start(ctx, goalloop.Goal{Statement: "integration goal"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	specs := make(map[string]goalloop.TaskSpec, len(intake.Tasks))
	for _, task := range intake.Tasks {
		specs[task.TaskID] = task.Spec
	}

	backend, err := dapr.New(endpoint)
	if err != nil {
		t.Fatalf("dapr.New: %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	if err := backend.Health(ctx); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if err := backend.StartWorker(ctx, goalrunner.New(loop, intake.Goal, specs)); err != nil {
		t.Fatalf("StartWorker: %v", err)
	}
	instanceID, err := backend.StartGoal(ctx, durability.GoalStart{RunID: intake.RunID})
	if err != nil {
		t.Fatalf("StartGoal: %v", err)
	}
	status, err := backend.WaitForGoal(ctx, instanceID)
	if err != nil {
		t.Fatalf("WaitForGoal: %v", err)
	}
	if status.RuntimeStatus != "COMPLETED" || len(status.Result.Tasks) != 1 {
		t.Fatalf("status = %+v, want one completed task", status)
	}
	if status.Result.Tasks[0].Status != taskstate.StatusCompleted {
		t.Fatalf("task outcome = %+v", status.Result.Tasks[0])
	}
}
