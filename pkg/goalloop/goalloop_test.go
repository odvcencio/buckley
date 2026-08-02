package goalloop

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

// scriptedEngine returns its outcomes in order, then keeps returning the
// last one.
type scriptedEngine struct {
	outcomes []TurnOutcome
	calls    int
	seen     []TaskContext
}

func (e *scriptedEngine) RunTurn(_ context.Context, task TaskContext) (TurnOutcome, error) {
	e.seen = append(e.seen, task)
	idx := e.calls
	if idx >= len(e.outcomes) {
		idx = len(e.outcomes) - 1
	}
	e.calls++
	return e.outcomes[idx], nil
}

type staticPlanner struct {
	specs []TaskSpec
}

func (p staticPlanner) Decompose(context.Context, Goal) ([]TaskSpec, error) {
	return p.specs, nil
}

func newTestLoop(t *testing.T, cfg Config) (*Loop, runledger.Store) {
	t.Helper()
	dir := t.TempDir()
	ev, err := evidence.New(filepath.Join(dir, "shared.db"), evidence.WithBlobRoot(filepath.Join(dir, "blobs")))
	if err != nil {
		t.Fatalf("evidence.New: %v", err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	rl, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	mgr, err := taskstate.NewManager(rl, ev)
	if err != nil {
		t.Fatalf("taskstate.NewManager: %v", err)
	}
	cfg.Ledger = rl
	cfg.Checkpoints = mgr
	if cfg.SessionID == "" {
		cfg.SessionID = "sess-goal"
	}
	loop, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return loop, rl
}

func TestLoop_StartRecordsTreeAndQueue(t *testing.T) {
	t.Parallel()
	loop, ledger := newTestLoop(t, Config{
		Planner: staticPlanner{specs: []TaskSpec{
			{Title: "port the store tests", Priority: 2},
			{Title: "update fixtures", Priority: 1},
		}},
	})
	ctx := context.Background()

	intake, err := loop.Start(ctx, Goal{
		Statement:          "Migrate storage tests to testcontainers",
		AcceptanceCriteria: []string{"full suite green"},
		BudgetUSD:          12,
		Posture:            "overnight",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if intake.RunID == "" || intake.GoalTaskID == "" || len(intake.Tasks) != 2 {
		t.Fatalf("intake = %+v, want run id, goal task, and 2 tasks", intake)
	}
	for _, task := range intake.Tasks {
		if task.CheckpointID == "" {
			t.Fatalf("task %s has no initial checkpoint", task.TaskID)
		}
	}

	// The tree is reconstructable from run events alone.
	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var goals, tasks int
	for _, ev := range events {
		if ev.Type != runledger.EventTaskCreated {
			continue
		}
		switch ev.Payload["kind"] {
		case "goal":
			goals++
			if ev.Payload["statement"] != "Migrate storage tests to testcontainers" {
				t.Fatalf("goal event payload = %+v", ev.Payload)
			}
		case "task":
			tasks++
		}
	}
	if goals != 1 || tasks != 2 {
		t.Fatalf("created events: goals=%d tasks=%d, want 1 and 2", goals, tasks)
	}

	// The queue orders by priority: "update fixtures" (1) first.
	queue, err := loop.BuildQueue(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	if len(queue) != 2 || queue[0].Title != "update fixtures" || queue[1].Title != "port the store tests" {
		t.Fatalf("queue = %+v, want fixtures first by priority", queue)
	}
}

func TestLoop_StartWithoutPlannerMakesOneTask(t *testing.T) {
	t.Parallel()
	loop, _ := newTestLoop(t, Config{})

	intake, err := loop.Start(context.Background(), Goal{Statement: "fix the flaky test"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(intake.Tasks) != 1 || intake.Tasks[0].Spec.Title != "fix the flaky test" {
		t.Fatalf("intake tasks = %+v, want one task carrying the goal", intake.Tasks)
	}
}

func TestLoop_RunTaskCompletesWithEvidence(t *testing.T) {
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

	result, err := loop.RunTask(ctx, intake.RunID, taskID, intake.Goal, intake.Tasks[0].Spec)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if result.Status != taskstate.StatusCompleted || result.Turns != 2 {
		t.Fatalf("result = %+v, want completed after 2 turns", result)
	}

	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var sawCompleted bool
	for _, ev := range events {
		if ev.Type == runledger.EventTaskCompleted && ev.TaskID == taskID {
			sawCompleted = true
			if ev.Payload["evidence_id"] != "ev_done" {
				t.Fatalf("completion event payload = %+v, want ev_done", ev.Payload)
			}
		}
	}
	if !sawCompleted {
		t.Fatal("no task.completed event recorded")
	}

	// A completed task leaves the queue.
	queue, err := loop.BuildQueue(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("queue after completion = %+v, want empty", queue)
	}
}

func TestLoop_RunTaskBlocksAndLeavesQueue(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []TurnOutcome{
		{Rounds: 1, Blocker: &taskstate.Blocker{Reason: "needs DATABASE_URL", Needs: "integration env"}},
	}}
	loop, ledger := newTestLoop(t, Config{Engine: engine})
	ctx := context.Background()

	intake, err := loop.Start(ctx, Goal{Statement: "run integration tests"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	taskID := intake.Tasks[0].TaskID

	result, err := loop.RunTask(ctx, intake.RunID, taskID, intake.Goal, intake.Tasks[0].Spec)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if result.Status != taskstate.StatusBlocked {
		t.Fatalf("result = %+v, want blocked", result)
	}

	events, _ := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID})
	var sawBlocked bool
	for _, ev := range events {
		if ev.Type == runledger.EventTaskBlocked && ev.TaskID == taskID {
			sawBlocked = true
		}
	}
	if !sawBlocked {
		t.Fatal("no task.blocked event recorded")
	}

	queue, err := loop.BuildQueue(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("queue with blocked task = %+v, want empty", queue)
	}
}

func TestLoop_BudgetExhaustionParks(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []TurnOutcome{
		{Rounds: 1, SpentUSD: 0.60, StateChanged: true},
	}}
	loop, ledger := newTestLoop(t, Config{Engine: engine})
	ctx := context.Background()

	intake, err := loop.Start(ctx, Goal{Statement: "expensive exploration", BudgetUSD: 1.00})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	taskID := intake.Tasks[0].TaskID

	result, err := loop.RunTask(ctx, intake.RunID, taskID, intake.Goal, intake.Tasks[0].Spec)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if result.Status != taskstate.StatusParked || result.Decision != agentloop.DecidePark {
		t.Fatalf("result = %+v, want parked on budget exhaustion", result)
	}
	if result.Turns != 2 {
		t.Fatalf("turns = %d, want 2 (budget survives one 0.60 turn, not two)", result.Turns)
	}

	// The park decision is on the ledger with its policy trace.
	events, _ := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID})
	var sawDecision bool
	for _, ev := range events {
		if ev.Type == runledger.EventControllerDecision && ev.Payload["kind"] == "goal_loop" {
			if ev.Payload["decision"] == string(agentloop.DecidePark) {
				sawDecision = true
			}
		}
	}
	if !sawDecision {
		t.Fatal("no goal_loop park decision recorded")
	}
}

func TestLoop_DrainRunsQueueToCompletion(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []TurnOutcome{
		{Rounds: 1, Completed: true, CompletedEvidenceID: "ev_1", Summary: "done"},
	}}
	loop, _ := newTestLoop(t, Config{
		Engine: engine,
		Planner: staticPlanner{specs: []TaskSpec{
			{Title: "task one", Priority: 1},
			{Title: "task two", Priority: 2},
		}},
	})
	ctx := context.Background()

	intake, err := loop.Start(ctx, Goal{Statement: "two easy tasks"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	specs := map[string]TaskSpec{}
	for _, task := range intake.Tasks {
		specs[task.TaskID] = task.Spec
	}

	results, err := loop.Drain(ctx, intake.RunID, intake.Goal, specs)
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want 2 completed drives", results)
	}
	for _, r := range results {
		if r.Status != taskstate.StatusCompleted {
			t.Fatalf("result %+v, want completed", r)
		}
	}
	queue, err := loop.BuildQueue(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	if len(queue) != 0 {
		t.Fatalf("queue after drain = %+v, want empty", queue)
	}
}

func TestLoop_ResumePassesCheckpointContext(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []TurnOutcome{
		{Rounds: 1, StateChanged: true, Summary: "halfway there", NextActions: []taskstate.NextAction{{Text: "finish the port"}}},
	}}
	// A one-request fuse in dynamic mode ends the first drive after one
	// turn with a checkpoint-and-yield.
	loop, _ := newTestLoop(t, Config{
		Engine:   engine,
		Progress: &agentloop.ProgressController{Mode: agentloop.ModeDynamic, Fuses: agentloop.Fuses{ModelRequests: 1}},
	})
	ctx := context.Background()

	intake, err := loop.Start(ctx, Goal{Statement: "long port"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	taskID := intake.Tasks[0].TaskID

	first, err := loop.RunTask(ctx, intake.RunID, taskID, intake.Goal, intake.Tasks[0].Spec)
	if err != nil {
		t.Fatalf("first RunTask: %v", err)
	}
	if first.Status != taskstate.StatusInProgress || first.Decision != agentloop.DecideStopSafety {
		t.Fatalf("first drive = %+v, want in_progress after stop_safety yield", first)
	}

	if _, err := loop.RunTask(ctx, intake.RunID, taskID, intake.Goal, intake.Tasks[0].Spec); err != nil {
		t.Fatalf("second RunTask: %v", err)
	}
	last := engine.seen[len(engine.seen)-1]
	if last.Resume == nil {
		t.Fatal("second drive got no resume context")
	}
	if last.Resume.State.Summary != "halfway there" {
		t.Fatalf("resume summary = %q, want the first drive's checkpoint", last.Resume.State.Summary)
	}
	if !strings.Contains(last.Resume.Prompt, "finish the port") {
		t.Fatalf("resume prompt missing next action:\n%s", last.Resume.Prompt)
	}
}
