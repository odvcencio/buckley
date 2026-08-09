package goalrunner

import (
	"context"
	"path/filepath"
	"testing"

	"m31labs.dev/buckley/pkg/durability"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

// scriptedEngine mirrors the goalloop test double: outcomes in order,
// last one repeats.
type scriptedEngine struct {
	outcomes []goalloop.TurnOutcome
	calls    int
}

func (e *scriptedEngine) RunTurn(_ context.Context, _ goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	idx := e.calls
	if idx >= len(e.outcomes) {
		idx = len(e.outcomes) - 1
	}
	e.calls++
	return e.outcomes[idx], nil
}

func newTestRunner(t *testing.T, engine goalloop.TurnEngine) (*Runner, *goalloop.Intake) {
	t.Helper()
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
		Engine:      engine,
		SessionID:   "runner-test",
	})
	if err != nil {
		t.Fatalf("goalloop.New: %v", err)
	}
	intake, err := loop.Start(context.Background(), goalloop.Goal{Statement: "port files"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	specs := make(map[string]goalloop.TaskSpec, len(intake.Tasks))
	for _, task := range intake.Tasks {
		specs[task.TaskID] = task.Spec
	}
	return New(loop, intake.Goal, specs), intake
}

// TestRunner_DrivesTaskThroughWireForm exercises the full activity
// surface the way the Dapr workflows do: NextTask, ResumeSeed, then
// RunTurn with the opaque snapshot until the task completes.
func TestRunner_DrivesTaskThroughWireForm(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []goalloop.TurnOutcome{
		{Rounds: 2, ToolCalls: 3, StateChanged: true, Summary: "halfway", SpentUSD: 0.25},
		{Rounds: 1, Completed: true, CompletedEvidenceID: "ev_done", Summary: "done", SpentUSD: 0.10},
	}}
	runner, intake := newTestRunner(t, engine)
	ctx := context.Background()

	next, err := runner.NextTask(ctx, durability.NextTaskRequest{RunID: intake.RunID})
	if err != nil {
		t.Fatalf("NextTask: %v", err)
	}
	if next.Done || next.TaskID == "" {
		t.Fatalf("next = %+v, want a runnable task", next)
	}

	seed, err := runner.ResumeSeed(ctx, intake.RunID, next.TaskID)
	if err != nil {
		t.Fatalf("ResumeSeed: %v", err)
	}
	if len(seed.Drive) == 0 {
		t.Fatal("seed drive snapshot is empty")
	}

	drive := seed.Drive
	generation, turnIndex := seed.Generation, 0
	var last durability.TurnResponse
	for turn := 0; turn < 4; turn++ {
		last, err = runner.RunTurn(ctx, durability.TurnRequest{
			RunID:              intake.RunID,
			TaskID:             next.TaskID,
			Generation:         generation,
			TurnIndex:          turnIndex,
			Drive:              drive,
			WorkflowInstanceID: "wf-runner-test",
		})
		if err != nil {
			t.Fatalf("RunTurn %d: %v", turn, err)
		}
		drive = last.Drive
		turnIndex++
		if last.Kind == string(goalloop.StepCheckpoint) {
			generation++
			turnIndex = 0
		}
		if last.Kind == string(goalloop.StepCompleted) {
			break
		}
	}
	if last.Kind != string(goalloop.StepCompleted) || last.Status != taskstate.StatusCompleted {
		t.Fatalf("last = %+v, want completed", last)
	}
	if last.TurnSpentUSD != 0.10 {
		t.Fatalf("turn spend = %v, want 0.10", last.TurnSpentUSD)
	}

	// A completed task leaves the queue: the goal is done.
	next, err = runner.NextTask(ctx, durability.NextTaskRequest{RunID: intake.RunID})
	if err != nil {
		t.Fatalf("NextTask after completion: %v", err)
	}
	if !next.Done {
		t.Fatalf("next after completion = %+v, want done", next)
	}
}

func TestPartitionIndependent(t *testing.T) {
	t.Parallel()
	claim := func(id string, paths ...string) durability.TaskClaim {
		return durability.TaskClaim{TaskID: id, Claims: paths}
	}
	ids := func(batch []durability.TaskClaim) []string {
		var out []string
		for _, item := range batch {
			out = append(out, item.TaskID)
		}
		return out
	}
	cases := []struct {
		name        string
		candidates  []durability.TaskClaim
		maxParallel int
		want        []string
	}{
		{"disjoint claims fan out", []durability.TaskClaim{claim("a", "pkg/a"), claim("b", "pkg/b"), claim("c", "pkg/c")}, 3, []string{"a", "b", "c"}},
		{"bounded by max parallel", []durability.TaskClaim{claim("a", "pkg/a"), claim("b", "pkg/b"), claim("c", "pkg/c")}, 2, []string{"a", "b"}},
		{"nested claim conflicts", []durability.TaskClaim{claim("a", "pkg/a"), claim("b", "pkg/a/sub"), claim("c", "pkg/c")}, 3, []string{"a"}},
		{"no-claims task runs alone", []durability.TaskClaim{claim("a"), claim("b", "pkg/b")}, 3, []string{"a"}},
		{"no-claims task blocks batch growth", []durability.TaskClaim{claim("a", "pkg/a"), claim("b"), claim("c", "pkg/c")}, 3, []string{"a"}},
		{"zero max parallel means one", []durability.TaskClaim{claim("a", "pkg/a"), claim("b", "pkg/b")}, 0, []string{"a"}},
		{"empty queue", nil, 3, nil},
	}
	for _, tc := range cases {
		got := ids(partitionIndependent(tc.candidates, tc.maxParallel))
		if len(got) != len(tc.want) {
			t.Fatalf("%s: batch = %v, want %v", tc.name, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: batch = %v, want %v", tc.name, got, tc.want)
			}
		}
	}
}

func TestPathsNest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want bool
	}{
		{"pkg/a", "pkg/a", true},
		{"pkg/a", "pkg/a/sub", true},
		{"pkg/a/sub", "pkg/a", true},
		{"pkg/a", "pkg/ab", false},
		{"./pkg/a/", "pkg/a", true},
		{"cmd", "pkg", false},
	}
	for _, tc := range cases {
		if got := pathsNest(tc.a, tc.b); got != tc.want {
			t.Fatalf("pathsNest(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestRunner_NextTaskHonorsDeferred mirrors the Drain yield rule.
func TestRunner_NextTaskHonorsDeferred(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []goalloop.TurnOutcome{{Rounds: 1}}}
	runner, intake := newTestRunner(t, engine)
	ctx := context.Background()

	next, err := runner.NextTask(ctx, durability.NextTaskRequest{RunID: intake.RunID})
	if err != nil {
		t.Fatalf("NextTask: %v", err)
	}
	deferred, err := runner.NextTask(ctx, durability.NextTaskRequest{
		RunID:    intake.RunID,
		Deferred: []string{next.TaskID},
	})
	if err != nil {
		t.Fatalf("NextTask deferred: %v", err)
	}
	if !deferred.Done {
		t.Fatalf("deferred pull = %+v, want done for a one-task goal", deferred)
	}
}
