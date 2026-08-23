package goalrunner

import (
	"context"
	"encoding/json"
	"fmt"
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

type contractCaptureEngine struct {
	request goalloop.GoalModelRequest
}

func (e *contractCaptureEngine) RunTurn(_ context.Context, task goalloop.TaskContext) (goalloop.TurnOutcome, error) {
	e.request = task.Goal.ModelRequest
	return goalloop.TurnOutcome{Rounds: 1, StateChanged: true, Summary: "captured"}, nil
}

type failOnceEventSink struct {
	eventType string
	calls     int
}

type reportFailLedger struct{ runledger.Store }

func (reportFailLedger) SumMetricByTask(context.Context, string, string) (map[string]float64, error) {
	return nil, fmt.Errorf("injected report metric failure")
}

func (s *failOnceEventSink) WriteEvent(_ context.Context, event runledger.Event) error {
	if event.Type != s.eventType {
		return nil
	}
	s.calls++
	if s.calls == 1 {
		return fmt.Errorf("injected %s secondary append failure", event.Type)
	}
	return nil
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
	return newTestRunnerWithGoal(t, engine, goalloop.Goal{Statement: "port files", WorkspaceRoot: t.TempDir()})
}

func newTestRunnerWithGoal(t *testing.T, engine goalloop.TurnEngine, goal goalloop.Goal) (*Runner, *goalloop.Intake) {
	t.Helper()
	dir := goal.WorkspaceRoot
	if strings.TrimSpace(dir) == "" {
		dir = t.TempDir()
		goal.WorkspaceRoot = dir
	}
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
	intake, err := loop.Start(context.Background(), goal)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	loadedGoal, specs, err := loop.LoadGoal(context.Background(), intake.RunID)
	if err != nil {
		t.Fatalf("LoadGoal after restart: %v", err)
	}
	runner, err := New(loop, intake.RunID, dir, loadedGoal, specs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return runner, intake
}

func TestRunner_RestartPreservesDurableGoalModelRequest(t *testing.T) {
	t.Parallel()
	engine := &contractCaptureEngine{}
	contract := goalloop.GoalModelRequest{
		PolicyVersion:            goalloop.GoalModelPolicyVersionV1,
		Policy:                   "strict_zdr",
		PolicyAction:             "allow",
		PolicyReasonCode:         "zdr_enforced",
		Model:                    "stealth/ox-alpha",
		ReasoningEffort:          "max",
		RetentionMode:            goalloop.GoalRetentionZDR,
		OpenRouterZDR:            true,
		OpenRouterDataCollection: "deny",
	}
	runner, intake := newTestRunnerWithGoal(t, engine, goalloop.Goal{
		Statement:     "probe",
		WorkspaceRoot: t.TempDir(),
		ModelRequest:  contract,
	})

	seed, err := runner.ResumeSeed(context.Background(), intake.RunID, intake.Tasks[0].TaskID)
	if err != nil {
		t.Fatalf("ResumeSeed: %v", err)
	}
	if _, err := runner.RunTurn(context.Background(), durability.TurnRequest{
		RunID:              intake.RunID,
		TaskID:             intake.Tasks[0].TaskID,
		WorkspaceRoot:      intake.Goal.WorkspaceRoot,
		WorkflowInstanceID: testGoalWorkflowInstanceID(intake.RunID, 0),
		Drive:              seed.Drive,
	}); err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if engine.request != contract {
		t.Fatalf("worker request = %+v, want persisted %+v", engine.request, contract)
	}
}

func TestRunner_NewNeverClassifiesRootlessV1GoalAsLegacy(t *testing.T) {
	workerRoot := t.TempDir()
	v1 := goalloop.Goal{
		Statement: "root-bound goal",
		ModelRequest: goalloop.GoalModelRequest{
			PolicyVersion: goalloop.GoalModelPolicyVersionV1,
			Policy:        "strict_zdr", PolicyAction: "allow", PolicyReasonCode: "zdr_enforced",
			Model: "stealth/ox-alpha", RetentionMode: goalloop.GoalRetentionZDR, OpenRouterZDR: true,
		},
	}
	if runner, err := New(nil, "run-rootless-v1", workerRoot, v1, nil); err == nil || runner != nil || !strings.Contains(err.Error(), "workspace root") {
		t.Fatalf("New rootless v1 = %#v, %v", runner, err)
	}
	legacy, err := New(nil, "run-rootless-legacy", workerRoot, goalloop.Goal{Statement: "legacy goal"}, nil)
	if err != nil {
		t.Fatalf("New rootless legacy: %v", err)
	}
	if !legacy.legacyRoot {
		t.Fatal("rootless v0 goal was not retained as legacy")
	}
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

func TestRunner_RetryWaitAuditAndWakeAreIdempotent(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour)
	engine := &scriptedEngine{outcomes: []goalloop.TurnOutcome{{
		Rounds: 1,
		Blocker: &taskstate.Blocker{
			Reason:     "provider rate limit",
			RetryAfter: &future,
		},
	}}}
	runner, intake := newTestRunner(t, engine)
	ctx := context.Background()
	taskID := intake.Tasks[0].TaskID
	seed, err := runner.ResumeSeed(ctx, intake.RunID, taskID)
	if err != nil {
		t.Fatalf("ResumeSeed: %v", err)
	}
	turn, err := runner.RunTurnV3(ctx, durability.TurnRequest{
		RunID:              intake.RunID,
		TaskID:             taskID,
		WorkspaceRoot:      intake.Goal.WorkspaceRoot,
		Drive:              seed.Drive,
		WorkflowInstanceID: "goal-retry-test",
	})
	if err != nil {
		t.Fatalf("RunTurn: %v", err)
	}
	if turn.Kind != string(goalloop.StepBlocked) || turn.RetryAfterUnixMS <= time.Now().UnixMilli() {
		t.Fatalf("turn = %+v, want future retryable block", turn)
	}
	wait := durability.RetryWait{
		RunID:                     intake.RunID,
		TaskID:                    taskID,
		WorkflowInstanceID:        "goal-retry-test",
		WaitID:                    "retry-1-0123456789abcdef01234567",
		Category:                  turn.BlockerCategory,
		ReasonCode:                turn.BlockerReasonCode,
		RetryAfterUnixMS:          turn.RetryAfterUnixMS,
		Ordinal:                   1,
		ExpectedCheckpointID:      turn.ExpectedCheckpointID,
		ExpectedCheckpointVersion: turn.ExpectedCheckpointVersion,
		BlockerDigest:             turn.BlockerDigest,
	}
	waitingSink := &failOnceEventSink{eventType: runledger.EventDurableRetryWaiting}
	runner.loop.Ledger().SetRalphSink(waitingSink)
	if err := runner.RecordRetryWaiting(ctx, wait); err == nil {
		t.Fatal("RecordRetryWaiting succeeded despite injected audit failure")
	}
	if err := runner.RecordRetryWaiting(ctx, wait); err != nil {
		t.Fatalf("RecordRetryWaiting redelivery: %v", err)
	}
	if waitingSink.calls != 2 {
		t.Fatalf("retry waiting deliveries = %d, want failed attempt plus reconciliation", waitingSink.calls)
	}
	waiting, err := runner.loop.Ledger().ListEvents(ctx, runledger.EventQuery{
		RunID: intake.RunID,
		Types: []string{runledger.EventDurableRetryWaiting},
	})
	if err != nil {
		t.Fatalf("List waiting events: %v", err)
	}
	if len(waiting) != 1 {
		t.Fatalf("retry waiting events = %d, want 1", len(waiting))
	}

	before, err := runner.loop.Ledger().LatestTaskCheckpoint(ctx, taskID)
	if err != nil {
		t.Fatalf("LatestTaskCheckpoint before wake: %v", err)
	}
	if before.Status != taskstate.StatusBlocked {
		t.Fatalf("checkpoint before wake = %q, want blocked", before.Status)
	}
	sink := &failOnceEventSink{eventType: runledger.EventControllerDecision}
	runner.loop.Ledger().SetRalphSink(sink)
	if err := runner.WakeRetry(ctx, wait); err == nil {
		t.Fatal("WakeRetry succeeded despite injected post-save audit failure")
	}
	after, err := runner.loop.Ledger().LatestTaskCheckpoint(ctx, taskID)
	if err != nil {
		t.Fatalf("LatestTaskCheckpoint after wake: %v", err)
	}
	if after.Status != taskstate.StatusInProgress || after.Version != before.Version+1 {
		t.Fatalf("checkpoint after wake = status %q version %d, want in_progress version %d", after.Status, after.Version, before.Version+1)
	}
	wakeResult, err := runner.WakeRetryV2(ctx, wait)
	if err != nil {
		t.Fatalf("WakeRetryV2 redelivery: %v", err)
	}
	if wakeResult.Disposition != durability.RetryWakeAlreadyApplied {
		t.Fatalf("wake redelivery disposition = %q, want %q", wakeResult.Disposition, durability.RetryWakeAlreadyApplied)
	}
	replayed, err := runner.loop.Ledger().LatestTaskCheckpoint(ctx, taskID)
	if err != nil {
		t.Fatalf("LatestTaskCheckpoint after redelivery: %v", err)
	}
	if replayed.Version != after.Version {
		t.Fatalf("redelivered wake created checkpoint version %d, want %d", replayed.Version, after.Version)
	}
	if sink.calls != 2 {
		t.Fatalf("wake audit deliveries = %d, want failed attempt plus reconciliation", sink.calls)
	}

	resolvedSink := &failOnceEventSink{eventType: runledger.EventDurableRetryResolved}
	runner.loop.Ledger().SetRalphSink(resolvedSink)
	if err := runner.ResolveRetry(ctx, wait); err == nil {
		t.Fatal("ResolveRetry succeeded despite injected audit failure")
	}
	if err := runner.ResolveRetry(ctx, wait); err != nil {
		t.Fatalf("ResolveRetry redelivery: %v", err)
	}
	if resolvedSink.calls != 2 {
		t.Fatalf("retry resolved deliveries = %d, want failed attempt plus reconciliation", resolvedSink.calls)
	}
	resolved, err := runner.loop.Ledger().ListEvents(ctx, runledger.EventQuery{
		RunID: intake.RunID,
		Types: []string{runledger.EventDurableRetryResolved},
	})
	if err != nil {
		t.Fatalf("List resolved events: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("retry resolved events = %d, want 1", len(resolved))
	}
}

func TestRunner_RetryWaitAuditNormalizesUntrustedCodes(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour)
	engine := &scriptedEngine{outcomes: []goalloop.TurnOutcome{{
		Rounds:  1,
		Blocker: &taskstate.Blocker{Reason: "provider rate limit", RetryAfter: &future},
	}}}
	runner, intake := newTestRunner(t, engine)
	ctx := context.Background()
	taskID := intake.Tasks[0].TaskID
	seed, err := runner.ResumeSeed(ctx, intake.RunID, taskID)
	if err != nil {
		t.Fatalf("ResumeSeed: %v", err)
	}
	turn, err := runner.RunTurnV3(ctx, durability.TurnRequest{
		RunID: intake.RunID, TaskID: taskID, WorkspaceRoot: intake.Goal.WorkspaceRoot,
		Generation: seed.Generation, Drive: seed.Drive, WorkflowInstanceID: "wf-normalize",
	})
	if err != nil {
		t.Fatalf("RunTurnV3: %v", err)
	}
	secret := "SECRET-retry-policy-π"
	wait := durability.RetryWait{
		RunID:                     intake.RunID,
		TaskID:                    taskID,
		WorkflowInstanceID:        "wf-normalize",
		WaitID:                    "retry-1-0123456789abcdef01234567",
		Category:                  secret + strings.Repeat("x", 2048),
		ReasonCode:                secret + strings.Repeat("界", 1024),
		RetryAfterUnixMS:          turn.RetryAfterUnixMS,
		Ordinal:                   1,
		ExpectedCheckpointID:      turn.ExpectedCheckpointID,
		ExpectedCheckpointVersion: turn.ExpectedCheckpointVersion,
		BlockerDigest:             turn.BlockerDigest,
	}
	if err := runner.RecordRetryWaiting(ctx, wait); err != nil {
		t.Fatalf("RecordRetryWaiting: %v", err)
	}
	events, err := runner.loop.Ledger().ListEvents(ctx, runledger.EventQuery{
		RunID: intake.RunID, Types: []string{runledger.EventDurableRetryWaiting},
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("retry waiting events = %d, want 1", len(events))
	}
	encoded, err := json.Marshal(events[0])
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "界") {
		t.Fatalf("retry event leaked untrusted text: %s", encoded)
	}
	if events[0].Payload["category"] != "execution" || events[0].Payload["reason_code"] != "blocked" {
		t.Fatalf("retry event codes = %+v, want normalized defaults", events[0].Payload)
	}
}

func TestRunner_NextBatchReportsBlockedTasksAsIncomplete(t *testing.T) {
	engine := &scriptedEngine{outcomes: []goalloop.TurnOutcome{{
		Rounds:  1,
		Blocker: &taskstate.Blocker{Reason: "manual dependency"},
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
	batch, err := runner.NextBatchV2(ctx, durability.NextBatchV2Request{RunID: intake.RunID})
	if err != nil {
		t.Fatalf("NextBatchV2: %v", err)
	}
	if !batch.Done || len(batch.Tasks) != 0 {
		t.Fatalf("terminal batch = %+v, want done with no tasks", batch)
	}
	if len(batch.IncompleteTaskIDs) != 1 || batch.IncompleteTaskIDs[0] != taskID {
		t.Fatalf("incomplete task IDs = %v, want [%s]", batch.IncompleteTaskIDs, taskID)
	}
}

func TestRunner_NextBatchV1DoesNotAcquireV2ReportFailure(t *testing.T) {
	dir := t.TempDir()
	ev, err := evidence.New(filepath.Join(dir, "shared.db"), evidence.WithBlobRoot(filepath.Join(dir, "blobs")))
	if err != nil {
		t.Fatalf("evidence.New: %v", err)
	}
	t.Cleanup(func() { _ = ev.Close() })
	store, err := runledger.NewWithDB(ev.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB: %v", err)
	}
	checkpoints, err := taskstate.NewManager(store, ev)
	if err != nil {
		t.Fatalf("taskstate.NewManager: %v", err)
	}
	ledger := reportFailLedger{Store: store}
	loop, err := goalloop.New(goalloop.Config{
		Ledger: ledger, Checkpoints: checkpoints,
		Engine: &scriptedEngine{outcomes: []goalloop.TurnOutcome{{Rounds: 1}}}, SessionID: "next-batch-versioning",
	})
	if err != nil {
		t.Fatalf("goalloop.New: %v", err)
	}
	ctx := context.Background()
	intake, err := loop.Start(ctx, goalloop.Goal{Statement: "next batch versioning", WorkspaceRoot: dir})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	specs := map[string]goalloop.TaskSpec{intake.Tasks[0].TaskID: intake.Tasks[0].Spec}
	runner, err := New(loop, intake.RunID, dir, intake.Goal, specs)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	deferred := []string{intake.Tasks[0].TaskID}
	legacy, err := runner.NextBatch(ctx, durability.NextBatchRequest{RunID: intake.RunID, Deferred: deferred})
	if err != nil || !legacy.Done {
		t.Fatalf("next_batch.v1 terminal pull = %+v err:%v, want historical Done", legacy, err)
	}
	if _, err := runner.NextBatchV2(ctx, durability.NextBatchV2Request{RunID: intake.RunID, Deferred: deferred}); err == nil || !strings.Contains(err.Error(), "injected report metric failure") {
		t.Fatalf("next_batch.v2 report error = %v", err)
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
