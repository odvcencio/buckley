package goalrunner

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"m31labs.dev/buckley/pkg/durability"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/goalloop"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

func testGoalWorkflowInstanceID(runID string, generation int) string {
	if generation <= 0 {
		return "goal-" + runID
	}
	return fmt.Sprintf("goal-%s::resume::%d", runID, generation)
}

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
	intake, err := loop.Start(context.Background(), goalloop.Goal{Statement: "port files", WorkspaceRoot: dir})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	specs := make(map[string]goalloop.TaskSpec, len(intake.Tasks))
	for _, task := range intake.Tasks {
		specs[task.TaskID] = task.Spec
	}
	runner, err := New(loop, intake.RunID, dir, intake.Goal, specs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runner, intake
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
			WorkspaceRoot:      intake.Goal.WorkspaceRoot,
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

func TestRunner_NewRejectsWrongWorkspace(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []goalloop.TurnOutcome{{Rounds: 1}}}
	runner, intake := newTestRunner(t, engine)
	wrongRoot := t.TempDir()

	if _, err := New(runner.loop, intake.RunID, wrongRoot, intake.Goal, runner.specs); err == nil {
		t.Fatal("New accepted a worker rooted in a foreign workspace")
	}
}

func TestRunner_RejectsForeignRunBeforeQueueAccess(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []goalloop.TurnOutcome{{Rounds: 1}}}
	runner, _ := newTestRunner(t, engine)

	if _, err := runner.NextTask(context.Background(), durability.NextTaskRequest{RunID: "run_foreign"}); err == nil {
		t.Fatal("NextTask accepted a foreign run")
	}
}

func TestRunner_RunTurnRejectsForeignWorkspaceBeforeEngine(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []goalloop.TurnOutcome{{Rounds: 1}}}
	runner, intake := newTestRunner(t, engine)
	taskID := intake.Tasks[0].TaskID

	_, err := runner.RunTurn(context.Background(), durability.TurnRequest{
		RunID:         intake.RunID,
		TaskID:        taskID,
		WorkspaceRoot: t.TempDir(),
	})
	if err == nil {
		t.Fatal("RunTurn accepted a foreign workspace")
	}
	if engine.calls != 0 {
		t.Fatalf("engine calls = %d, want 0 after fail-closed binding", engine.calls)
	}
}

func TestRunner_LegacyTurnTrustsBoundWorkerRootButStaysRunBound(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []goalloop.TurnOutcome{{Rounds: 1, Summary: "legacy resumed"}}}
	canonical, intake := newTestRunner(t, engine)
	legacyGoal := intake.Goal
	legacyGoal.WorkspaceRoot = ""
	legacy, err := New(canonical.loop, intake.RunID, canonical.workspaceRoot, legacyGoal, canonical.specs)
	if err != nil {
		t.Fatalf("New legacy runner: %v", err)
	}
	if !legacy.legacyRoot {
		t.Fatal("legacy runner did not record its intentional worker-root trust boundary")
	}
	if legacy.WorkspaceRoot() != canonical.workspaceRoot {
		t.Fatalf("legacy workspace root = %q, want bound worker root %q", legacy.WorkspaceRoot(), canonical.workspaceRoot)
	}
	seed, err := legacy.ResumeSeed(context.Background(), intake.RunID, intake.Tasks[0].TaskID)
	if err != nil {
		t.Fatalf("ResumeSeed: %v", err)
	}
	request := durability.TurnRequest{RunID: intake.RunID, TaskID: intake.Tasks[0].TaskID, Drive: seed.Drive}
	if _, err := legacy.RunTurn(context.Background(), request); err == nil {
		t.Fatal("workspace-bound V2 turn accepted legacy input without a root")
	}
	if _, err := legacy.RunLegacyTurn(context.Background(), request); err != nil {
		t.Fatalf("RunLegacyTurn: %v", err)
	}
	request.RunID = "run_foreign"
	if _, err := legacy.RunLegacyTurn(context.Background(), request); err == nil {
		t.Fatal("legacy turn accepted a foreign run")
	}
	if engine.calls != 1 {
		t.Fatalf("engine calls = %d, want exactly one trusted legacy turn", engine.calls)
	}
}

func TestRunner_FinalizeGoalMatchesReportAndIsIdempotent(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []goalloop.TurnOutcome{{
		Rounds: 1, Completed: true, CompletedEvidenceID: "ev_done", Summary: "done",
	}}}
	runner, intake := newTestRunner(t, engine)
	ctx := context.Background()
	taskID := intake.Tasks[0].TaskID

	seed, err := runner.ResumeSeed(ctx, intake.RunID, taskID)
	if err != nil {
		t.Fatalf("ResumeSeed: %v", err)
	}
	if _, err := runner.RunTurn(ctx, durability.TurnRequest{
		RunID:         intake.RunID,
		TaskID:        taskID,
		WorkspaceRoot: intake.Goal.WorkspaceRoot,
		Drive:         seed.Drive,
	}); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	finalization := durability.GoalFinalization{
		RunID:              intake.RunID,
		WorkspaceRoot:      intake.Goal.WorkspaceRoot,
		WorkflowInstanceID: testGoalWorkflowInstanceID(intake.RunID, 0),
	}
	if err := runner.FinalizeGoal(ctx, finalization); err != nil {
		t.Fatalf("FinalizeGoal: %v", err)
	}
	first, err := runner.loop.Ledger().GetRun(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if first.Status != "completed" || first.EndedAt == nil {
		t.Fatalf("run = %+v, want terminal completed lifecycle", first)
	}
	report, err := runner.loop.Report(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	if report.Status != first.Status {
		t.Fatalf("report status = %s, run status = %s", report.Status, first.Status)
	}
	if err := runner.FinalizeGoal(ctx, finalization); err != nil {
		t.Fatalf("second FinalizeGoal: %v", err)
	}
	second, err := runner.loop.Ledger().GetRun(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("second GetRun: %v", err)
	}
	if second.EndedAt == nil || !second.EndedAt.Equal(*first.EndedAt) {
		t.Fatalf("repeated finalization changed ended_at: first=%v second=%v", first.EndedAt, second.EndedAt)
	}
	events, err := runner.loop.Ledger().ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableGoalGeneration}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 || events[0].Payload["generation"] != float64(0) && events[0].Payload["generation"] != 0 {
		t.Fatalf("goal generation events = %+v, want one generation-zero fact", events)
	}
}

func TestRunner_FinalizeIncompleteGoalLeavesRunResumable(t *testing.T) {
	t.Parallel()
	engine := &scriptedEngine{outcomes: []goalloop.TurnOutcome{{Rounds: 1}}}
	runner, intake := newTestRunner(t, engine)
	ctx := context.Background()

	finalization := durability.GoalFinalization{
		RunID:              intake.RunID,
		WorkspaceRoot:      intake.Goal.WorkspaceRoot,
		WorkflowInstanceID: testGoalWorkflowInstanceID(intake.RunID, 1),
		Incomplete:         true,
	}
	if err := runner.FinalizeGoal(ctx, finalization); err != nil {
		t.Fatalf("FinalizeGoal incomplete: %v", err)
	}
	run, err := runner.loop.Ledger().GetRun(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.EndedAt != nil {
		t.Fatalf("incomplete run was sealed at %v", run.EndedAt)
	}
	queue, err := runner.loop.BuildQueue(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("BuildQueue: %v", err)
	}
	if len(queue) != 1 || queue[0].TaskID != intake.Tasks[0].TaskID {
		t.Fatalf("resumable queue = %+v, want original task", queue)
	}

	// Mutable report state may advance after the generation fact lands. A
	// redelivered finalization must remain idempotent because its payload uses
	// only immutable finalization input.
	seed, err := runner.ResumeSeed(ctx, intake.RunID, intake.Tasks[0].TaskID)
	if err != nil {
		t.Fatalf("ResumeSeed: %v", err)
	}
	engine.outcomes = []goalloop.TurnOutcome{{
		Rounds: 1, Completed: true, CompletedEvidenceID: "ev_done", Summary: "done after generation",
	}}
	engine.calls = 0
	if _, err := runner.RunTurn(ctx, durability.TurnRequest{
		RunID:         intake.RunID,
		TaskID:        intake.Tasks[0].TaskID,
		WorkspaceRoot: intake.Goal.WorkspaceRoot,
		Generation:    seed.Generation,
		Drive:         seed.Drive,
	}); err != nil {
		t.Fatalf("RunTurn report change: %v", err)
	}
	if err := runner.FinalizeGoal(ctx, finalization); err != nil {
		t.Fatalf("FinalizeGoal redelivery after report change: %v", err)
	}
	events, err := runner.loop.Ledger().ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableGoalGeneration}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 || events[0].Payload["workflow_instance_id"] != finalization.WorkflowInstanceID || events[0].Payload["incomplete"] != true {
		t.Fatalf("goal generation events = %+v, want one immutable incomplete fact", events)
	}
}

func TestRunner_FinalizePendingObserverReconciliationLeavesRunOpen(t *testing.T) {
	t.Parallel()
	runner, intake := newTestRunner(t, &scriptedEngine{outcomes: []goalloop.TurnOutcome{{Rounds: 1}}})
	ctx := context.Background()

	// The CLI performs one observer-side reconciliation after WaitForGoal.
	// It may not carry the V4 incomplete marker, so the durable report remains
	// the final guard against sealing a yielded run as pending.
	if err := runner.FinalizeGoal(ctx, durability.GoalFinalization{
		RunID:              intake.RunID,
		WorkspaceRoot:      intake.Goal.WorkspaceRoot,
		WorkflowInstanceID: testGoalWorkflowInstanceID(intake.RunID, 0),
	}); err != nil {
		t.Fatalf("FinalizeGoal observer reconciliation: %v", err)
	}
	run, err := runner.loop.Ledger().GetRun(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.EndedAt != nil {
		t.Fatalf("pending run was sealed at %v", run.EndedAt)
	}
}

func TestRunner_FinalizeFailureOverridesPendingAndEndsRun(t *testing.T) {
	t.Parallel()
	runner, intake := newTestRunner(t, &scriptedEngine{outcomes: []goalloop.TurnOutcome{{Rounds: 1}}})
	ctx := context.Background()

	if err := runner.FinalizeGoal(ctx, durability.GoalFinalization{
		RunID:              intake.RunID,
		WorkspaceRoot:      intake.Goal.WorkspaceRoot,
		WorkflowInstanceID: testGoalWorkflowInstanceID(intake.RunID, 0),
		Incomplete:         true,
		Failure:            "child workflow failed after fan-in",
	}); err != nil {
		t.Fatalf("FinalizeGoal failure: %v", err)
	}
	run, err := runner.loop.Ledger().GetRun(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run.Status != "failed" || run.EndedAt == nil {
		t.Fatalf("failed run = %+v, want terminal failed lifecycle", run)
	}
}

func TestRunner_FinalizeGoalRejectsForeignWorkflowIdentity(t *testing.T) {
	t.Parallel()
	runner, intake := newTestRunner(t, &scriptedEngine{outcomes: []goalloop.TurnOutcome{{Rounds: 1}}})
	err := runner.FinalizeGoal(context.Background(), durability.GoalFinalization{
		RunID:              intake.RunID,
		WorkspaceRoot:      intake.Goal.WorkspaceRoot,
		WorkflowInstanceID: "goal-another-run",
		Incomplete:         true,
	})
	if err == nil {
		t.Fatal("FinalizeGoal accepted a foreign workflow identity")
	}
	events, listErr := runner.loop.Ledger().ListEvents(context.Background(), runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableGoalGeneration}})
	if listErr != nil {
		t.Fatalf("ListEvents: %v", listErr)
	}
	if len(events) != 0 {
		t.Fatalf("foreign identity recorded events: %+v", events)
	}
}

func TestWorkflowGeneration_RequiresCanonicalIdentity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		instanceID string
		want       int
		wantErr    bool
	}{
		{instanceID: "goal-run-1", want: 0},
		{instanceID: "goal-run-1::resume::1", want: 1},
		{instanceID: "goal-run-1::resume::27", want: 27},
		{instanceID: "goal-other", wantErr: true},
		{instanceID: "goal-run-1::resume::0", wantErr: true},
		{instanceID: "goal-run-1::resume::01", wantErr: true},
		{instanceID: "goal-run-1::resume::-1", wantErr: true},
		{instanceID: "goal-run-1::resume::1::extra", wantErr: true},
	}
	for _, tc := range cases {
		got, err := workflowGeneration("run-1", tc.instanceID)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("workflowGeneration(%q) = %d, want error", tc.instanceID, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("workflowGeneration(%q) = (%d, %v), want (%d, nil)", tc.instanceID, got, err, tc.want)
		}
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
