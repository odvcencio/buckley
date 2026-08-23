package goalloop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/runledger"
	"m31labs.dev/buckley/pkg/taskstate"
)

type failDurableTurnSink struct{}

func (failDurableTurnSink) WriteEvent(_ context.Context, event runledger.Event) error {
	if event.Type == runledger.EventDurableTurn {
		return errors.New("injected durable.turn append failure")
	}
	return nil
}

type failOnceDurableTurnSink struct {
	calls atomic.Int32
}

type failCheckpointReadAfterCreate struct {
	runledger.Store
	armed    bool
	failNext bool
}

func (s *failCheckpointReadAfterCreate) CreateTaskCheckpoint(ctx context.Context, checkpoint runledger.TaskCheckpoint) (runledger.TaskCheckpoint, error) {
	saved, err := s.Store.CreateTaskCheckpoint(ctx, checkpoint)
	if err == nil && s.armed {
		s.failNext = true
	}
	return saved, err
}

func (s *failCheckpointReadAfterCreate) LatestTaskCheckpoint(ctx context.Context, taskID string) (runledger.TaskCheckpoint, error) {
	if s.failNext {
		s.failNext = false
		return runledger.TaskCheckpoint{}, errors.New("injected post-save checkpoint read failure")
	}
	return s.Store.LatestTaskCheckpoint(ctx, taskID)
}

func (s *failOnceDurableTurnSink) WriteEvent(_ context.Context, event runledger.Event) error {
	if event.Type == runledger.EventDurableTurn && s.calls.Add(1) == 1 {
		return errors.New("injected one-shot durable.turn append failure")
	}
	return nil
}

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

func TestLoop_RecordDurableTurnRetryIsIdempotent(t *testing.T) {
	loop, ledger := newTestLoop(t, Config{})
	ctx := context.Background()
	intake, err := loop.Start(ctx, Goal{Statement: "record a durable turn"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	req := TurnStepRequest{
		RunID:              intake.RunID,
		TaskID:             intake.Tasks[0].TaskID,
		Generation:         3,
		TurnIndex:          4,
		WorkflowInstanceID: "wf-retry",
		ActivityName:       "run_turn.v2",
	}
	resp := TurnStepResponse{Kind: StepContinue}
	loop.recordDurableTurn(ctx, req, resp)
	loop.recordDurableTurn(ctx, req, resp)

	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableTurn}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("durable turn events = %d, want 1", len(events))
	}
}

func TestLoop_LegacyTurnStepKeepsDurableAuditBestEffort(t *testing.T) {
	engine := &scriptedEngine{outcomes: []TurnOutcome{{
		Rounds: 1, Completed: true, CompletedEvidenceID: "ev_done", Summary: "done",
	}}}
	loop, ledger := newTestLoop(t, Config{Engine: engine})
	ctx := context.Background()
	intake, err := loop.Start(ctx, Goal{Statement: "durable audit failure"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	ledger.SetRalphSink(failDurableTurnSink{})
	seed, err := loop.SeedTask(ctx, intake.Tasks[0].TaskID, intake.Tasks[0].Spec)
	if err != nil {
		t.Fatalf("SeedTask: %v", err)
	}
	_, err = loop.TurnStep(ctx, TurnStepRequest{
		RunID:              intake.RunID,
		TaskID:             intake.Tasks[0].TaskID,
		Goal:               intake.Goal,
		Spec:               intake.Tasks[0].Spec,
		Generation:         seed.Generation,
		Drive:              seed.Drive,
		WorkflowInstanceID: "wf-audit-failure",
		ActivityName:       "run_turn.v2",
	})
	if err != nil {
		t.Fatalf("legacy TurnStep error = %v, want best-effort durable audit", err)
	}
	events, err := ledger.ListEvents(ctx, runledger.EventQuery{
		RunID: intake.RunID,
		Types: []string{runledger.EventDurableTurn},
	})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("durable.turn facts = %d, want canonical fact despite sink failure", len(events))
	}
}

func TestLoop_LegacyTurnStepBlockedDoesNotReadAfterSave(t *testing.T) {
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
	failing := &failCheckpointReadAfterCreate{Store: ledger}
	checkpoints, err := taskstate.NewManager(failing, ev)
	if err != nil {
		t.Fatalf("taskstate.NewManager: %v", err)
	}
	future := time.Now().UTC().Add(time.Hour)
	engine := &scriptedEngine{outcomes: []TurnOutcome{{
		Rounds: 1, Blocker: &taskstate.Blocker{Reason: "provider rate limit", RetryAfter: &future},
	}}}
	loop, err := New(Config{Ledger: ledger, Checkpoints: checkpoints, Engine: engine, SessionID: "legacy-read-failure"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	intake, err := loop.Start(ctx, Goal{Statement: "legacy blocked read"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	taskID := intake.Tasks[0].TaskID
	seed, err := loop.SeedTask(ctx, taskID, intake.Tasks[0].Spec)
	if err != nil {
		t.Fatalf("SeedTask: %v", err)
	}
	failing.armed = true
	resp, err := loop.TurnStep(ctx, TurnStepRequest{
		RunID: intake.RunID, TaskID: taskID, Goal: intake.Goal, Spec: intake.Tasks[0].Spec,
		Generation: seed.Generation, Drive: seed.Drive, WorkflowInstanceID: "legacy-v2", ActivityName: "run_turn.v2",
	})
	if err != nil {
		t.Fatalf("legacy TurnStep after blocked save: %v", err)
	}
	if resp.Kind != StepBlocked || resp.Status != taskstate.StatusBlocked {
		t.Fatalf("legacy blocked response = %+v", resp)
	}
	if resp.BlockerCategory != "" || resp.BlockerReasonCode != "" || resp.RetryAfterUnixMS != 0 || resp.RetryOrdinal != 0 || resp.ExpectedCheckpointID != "" || resp.ExpectedCheckpointVersion != 0 || resp.BlockerDigest != "" {
		t.Fatalf("legacy response leaked V3 retry metadata: %+v", resp)
	}
	if engine.calls != 1 {
		t.Fatalf("engine effects = %d, want 1", engine.calls)
	}
	checkpoint, err := ledger.LatestTaskCheckpoint(ctx, taskID)
	if err != nil {
		t.Fatalf("canonical blocked checkpoint: %v", err)
	}
	if checkpoint.Status != taskstate.StatusBlocked || checkpoint.Version != seed.Generation+1 {
		t.Fatalf("blocked checkpoint = %+v", checkpoint)
	}
}

func TestLoop_TurnStepV3_RedeliveryReplaysWholeTurnReceipt(t *testing.T) {
	engine := &scriptedEngine{outcomes: []TurnOutcome{{
		Rounds: 1, ToolCalls: 2, Completed: true, CompletedEvidenceID: "ev_done", Summary: "done",
	}}}
	loop, ledger := newTestLoop(t, Config{Engine: engine})
	ctx := context.Background()
	intake, err := loop.Start(ctx, Goal{Statement: "receipt-backed turn"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	taskID := intake.Tasks[0].TaskID
	seed, err := loop.SeedTask(ctx, taskID, intake.Tasks[0].Spec)
	if err != nil {
		t.Fatalf("SeedTask: %v", err)
	}
	req := TurnStepRequest{
		RunID:              intake.RunID,
		TaskID:             taskID,
		Goal:               intake.Goal,
		Spec:               intake.Tasks[0].Spec,
		Generation:         seed.Generation,
		Drive:              seed.Drive,
		WorkflowInstanceID: "wf-v3-redelivery",
		ActivityName:       "run_turn.v3",
	}
	sink := &failOnceDurableTurnSink{}
	ledger.SetRalphSink(sink)
	if _, err := loop.TurnStepV3(ctx, req); err == nil {
		t.Fatal("first V3 delivery succeeded despite injected receipt append failure")
	}
	firstCheckpoint, err := ledger.LatestTaskCheckpoint(ctx, taskID)
	if err != nil {
		t.Fatalf("LatestTaskCheckpoint after first delivery: %v", err)
	}

	second, err := loop.TurnStepV3(ctx, req)
	if err != nil {
		t.Fatalf("second V3 delivery: %v", err)
	}
	third, err := loop.TurnStepV3(ctx, req)
	if err != nil {
		t.Fatalf("third V3 delivery: %v", err)
	}
	if !reflect.DeepEqual(second, third) {
		t.Fatalf("replayed response changed: second=%+v third=%+v", second, third)
	}
	if len(engine.seen) != 1 {
		t.Fatalf("engine effects = %d, want 1", len(engine.seen))
	}
	if sink.calls.Load() != 2 {
		t.Fatalf("secondary durable.turn deliveries = %d, want failed attempt plus reconciliation", sink.calls.Load())
	}
	latest, err := ledger.LatestTaskCheckpoint(ctx, taskID)
	if err != nil {
		t.Fatalf("LatestTaskCheckpoint after replay: %v", err)
	}
	if latest.CheckpointID != firstCheckpoint.CheckpointID || latest.Version != seed.Generation+1 {
		t.Fatalf("checkpoint after replay = %+v, want one successor to generation %d", latest, seed.Generation)
	}
	events, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventDurableTurn}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("durable.turn receipts = %d, want 1", len(events))
	}
	if events[0].Payload["receipt_schema"] != runledger.DurableTurnReceiptSchemaV1 || events[0].Payload["output_digest"] == "" {
		t.Fatalf("durable.turn receipt integrity fields = %+v", events[0].Payload)
	}
	steps, err := ledger.(runledger.StepEnumerator).ListSteps(ctx, intake.RunID)
	if err != nil {
		t.Fatalf("ListSteps: %v", err)
	}
	if len(steps) != 1 || steps[0].Status != runledger.StepCompleted || steps[0].DispatchState != runledger.StepDispatchDispatched {
		t.Fatalf("whole-turn journal = %+v, want one completed dispatched step", steps)
	}

	changed := req
	changed.Drive.Summary = "changed input"
	if _, err := loop.TurnStepV3(ctx, changed); err == nil {
		t.Fatal("V3 redelivery accepted changed input for the same turn identity")
	}
}

func TestLoop_TurnStepV3_UnrelatedCheckpointDoesNotRecoverDispatchedTurn(t *testing.T) {
	engine := &scriptedEngine{outcomes: []TurnOutcome{{Rounds: 1, Summary: "must not run"}}}
	loop, ledger := newTestLoop(t, Config{Engine: engine})
	ctx := context.Background()
	intake, err := loop.Start(ctx, Goal{Statement: "ambiguous unrelated checkpoint"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	taskID := intake.Tasks[0].TaskID
	seed, err := loop.SeedTask(ctx, taskID, intake.Tasks[0].Spec)
	if err != nil {
		t.Fatalf("SeedTask: %v", err)
	}
	req := TurnStepRequest{
		RunID: intake.RunID, TaskID: taskID, Goal: intake.Goal, Spec: intake.Tasks[0].Spec,
		Generation: seed.Generation, Drive: seed.Drive, WorkflowInstanceID: "wf-ambiguous", ActivityName: "run_turn.v3",
	}
	inputDigest, err := turnStepInputDigest(req)
	if err != nil {
		t.Fatalf("turnStepInputDigest: %v", err)
	}
	journal := ledger.(runledger.FencedStepJournal)
	step, replay, err := journal.BeginStep(ctx, runledger.ExecutionStep{
		RunID: intake.RunID, TaskID: taskID, StepID: durableTurnStepID(req), Kind: "durable_turn",
		IdempotencyKey: durableTurnStepID(req), InputDigest: inputDigest, StartedAt: time.Now().UTC(),
	})
	if err != nil || replay {
		t.Fatalf("BeginStep = replay:%v err:%v", replay, err)
	}
	if err := journal.MarkStepDispatched(ctx, step, time.Now().UTC()); err != nil {
		t.Fatalf("MarkStepDispatched: %v", err)
	}
	resumed, err := loop.checkpoints.Resume(ctx, taskID)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	resumed.State.Summary = "unrelated controller checkpoint"
	resumed.State.UpdatedAt = time.Now().UTC()
	if _, err := loop.checkpoints.Save(ctx, taskstate.SaveInput{
		State: resumed.State, Reason: taskstate.TriggerDecisionRecorded, SessionID: loop.sessionID, RunID: intake.RunID,
	}); err != nil {
		t.Fatalf("save unrelated checkpoint: %v", err)
	}
	_, err = loop.TurnStepV3(ctx, req)
	if err == nil || !strings.Contains(err.Error(), "without a durable receipt") {
		t.Fatalf("TurnStepV3 ambiguity error = %v", err)
	}
	if engine.calls != 0 {
		t.Fatalf("engine effects = %d, want 0", engine.calls)
	}
}

func TestLoop_UnparkRetry_StaleWakeDoesNotClearNewerOrTerminalCheckpoint(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour)
	engine := &scriptedEngine{outcomes: []TurnOutcome{{
		Rounds:  1,
		Blocker: &taskstate.Blocker{Reason: "provider rate limit", RetryAfter: &future},
	}}}
	loop, ledger := newTestLoop(t, Config{Engine: engine})
	ctx := context.Background()
	intake, err := loop.Start(ctx, Goal{Statement: "stale wake"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	taskID := intake.Tasks[0].TaskID
	seed, err := loop.SeedTask(ctx, taskID, intake.Tasks[0].Spec)
	if err != nil {
		t.Fatalf("SeedTask: %v", err)
	}
	blocked, err := loop.TurnStepV3(ctx, TurnStepRequest{
		RunID: intake.RunID, TaskID: taskID, Goal: intake.Goal, Spec: intake.Tasks[0].Spec,
		Generation: seed.Generation, Drive: seed.Drive, WorkflowInstanceID: "wf-stale", ActivityName: "run_turn.v3",
	})
	if err != nil {
		t.Fatalf("TurnStep: %v", err)
	}
	wake := RetryWake{
		WaitID:                    "retry-1-stale",
		ExpectedCheckpointID:      blocked.ExpectedCheckpointID,
		ExpectedCheckpointVersion: blocked.ExpectedCheckpointVersion,
		BlockerDigest:             blocked.BlockerDigest,
		Category:                  blocked.BlockerCategory,
		ReasonCode:                blocked.BlockerReasonCode,
	}
	newer, err := loop.checkpoints.Resume(ctx, taskID)
	if err != nil {
		t.Fatalf("Resume blocked checkpoint: %v", err)
	}
	newer.State.Blocker = &taskstate.Blocker{Reason: "newer dependency blocker"}
	newer.State.UpdatedAt = time.Now().UTC()
	if _, err := loop.checkpoints.Save(ctx, taskstate.SaveInput{
		State: newer.State, Reason: taskstate.TriggerBlocker, SessionID: loop.sessionID, RunID: intake.RunID,
	}); err != nil {
		t.Fatalf("save newer blocker: %v", err)
	}
	if result, err := loop.UnparkRetry(ctx, intake.RunID, taskID, wake); err != nil || result.Disposition != RetryWakeStale {
		t.Fatalf("stale wake against newer blocker = result:%+v err:%v, want safe no-op", result, err)
	}
	latest, err := loop.checkpoints.Resume(ctx, taskID)
	if err != nil {
		t.Fatalf("Resume after stale wake: %v", err)
	}
	if latest.State.Status != taskstate.StatusBlocked || latest.State.Blocker == nil || latest.State.Blocker.Reason != "newer dependency blocker" {
		t.Fatalf("stale wake changed newer blocker: %+v", latest.State)
	}

	terminal := latest.State
	terminal.Status = taskstate.StatusCompleted
	terminal.Blocker = nil
	terminal.Completed = []taskstate.CompletedItem{{Text: "done", EvidenceID: "ev_done"}}
	terminal.UpdatedAt = time.Now().UTC()
	if _, err := loop.checkpoints.Save(ctx, taskstate.SaveInput{
		State: terminal, Reason: taskstate.TriggerDecisionRecorded, SessionID: loop.sessionID, RunID: intake.RunID,
	}); err != nil {
		t.Fatalf("save terminal checkpoint: %v", err)
	}
	if result, err := loop.UnparkRetry(ctx, intake.RunID, taskID, wake); err != nil || result.Disposition != RetryWakeStale || result.TaskStatus != taskstate.StatusCompleted {
		t.Fatalf("stale wake against terminal = result:%+v err:%v, want completed safe no-op", result, err)
	}
	final, err := ledger.LatestTaskCheckpoint(ctx, taskID)
	if err != nil {
		t.Fatalf("LatestTaskCheckpoint: %v", err)
	}
	if final.Status != taskstate.StatusCompleted {
		t.Fatalf("stale wake revived terminal checkpoint: %+v", final)
	}
}

func TestLoop_UnparkRetry_OwnershipFailsBeforeSuccessorOrStaleClassification(t *testing.T) {
	for _, state := range []string{"successor", "completed"} {
		for _, mismatch := range []string{"run", "session"} {
			t.Run(state+"/foreign-"+mismatch, func(t *testing.T) {
				future := time.Now().UTC().Add(time.Hour)
				engine := &scriptedEngine{outcomes: []TurnOutcome{{
					Rounds: 1, Blocker: &taskstate.Blocker{Reason: "provider rate limit", RetryAfter: &future},
				}}}
				loop, _, ledger := newSharedWakeLoops(t, engine)
				intake, taskID, wake := blockedRetryForConcurrentWake(t, loop)
				ctx := context.Background()
				if state == "successor" {
					result, err := loop.UnparkRetry(ctx, intake.RunID, taskID, wake)
					if err != nil || result.Disposition != RetryWakeApplied {
						t.Fatalf("create exact successor = %+v err:%v", result, err)
					}
				} else {
					resumed, err := loop.checkpoints.Resume(ctx, taskID)
					if err != nil {
						t.Fatalf("Resume blocked checkpoint: %v", err)
					}
					resumed.State.Status = taskstate.StatusCompleted
					resumed.State.Blocker = nil
					resumed.State.Completed = []taskstate.CompletedItem{{Text: "done", EvidenceID: "ev_done"}}
					resumed.State.UpdatedAt = time.Now().UTC()
					if _, err := loop.checkpoints.Save(ctx, taskstate.SaveInput{
						State: resumed.State, Reason: taskstate.TriggerDecisionRecorded,
						SessionID: loop.sessionID, RunID: intake.RunID,
					}); err != nil {
						t.Fatalf("save terminal checkpoint: %v", err)
					}
				}
				before, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventControllerDecision}})
				if err != nil {
					t.Fatalf("ListEvents before foreign wake: %v", err)
				}
				caller := loop
				runID := intake.RunID
				if mismatch == "run" {
					runID = "run_foreign"
				} else {
					caller, err = New(Config{
						Ledger: loop.ledger, Checkpoints: loop.checkpoints, Engine: engine, SessionID: "foreign-session",
					})
					if err != nil {
						t.Fatalf("New foreign-session loop: %v", err)
					}
				}
				if result, err := caller.UnparkRetry(ctx, runID, taskID, wake); err == nil || !strings.Contains(err.Error(), "ownership changed") || result.Disposition != "" {
					t.Fatalf("foreign wake = %+v err:%v, want fail closed", result, err)
				}
				after, err := ledger.ListEvents(ctx, runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventControllerDecision}})
				if err != nil {
					t.Fatalf("ListEvents after foreign wake: %v", err)
				}
				if len(after) != len(before) {
					t.Fatalf("foreign wake appended controller events: before=%d after=%d", len(before), len(after))
				}
			})
		}
	}

	loop, _ := newTestLoop(t, Config{})
	if _, err := loop.UnparkRetry(context.Background(), "run-a", "", RetryWake{}); err == nil || !strings.Contains(err.Error(), "task ID is required") {
		t.Fatalf("empty task ID error = %v", err)
	}
}

type retryWakeResult struct {
	disposition RetryWakeDisposition
	err         error
}

func runConcurrentRetryWakes(ctx context.Context, loops []*Loop, runID, taskID string, wake RetryWake) []retryWakeResult {
	start := make(chan struct{})
	results := make([]retryWakeResult, len(loops))
	var wg sync.WaitGroup
	for i, loop := range loops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			wakeResult, err := loop.UnparkRetry(ctx, runID, taskID, wake)
			results[i].disposition, results[i].err = wakeResult.Disposition, err
		}()
	}
	close(start)
	wg.Wait()
	return results
}

func newSharedWakeLoops(t *testing.T, engine TurnEngine) (*Loop, *Loop, *runledger.SQLiteStore) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "shared.db")
	blobRoot := filepath.Join(dir, "blobs")
	ev1, err := evidence.New(dbPath, evidence.WithBlobRoot(blobRoot))
	if err != nil {
		t.Fatalf("evidence.New first: %v", err)
	}
	t.Cleanup(func() { _ = ev1.Close() })
	ev2, err := evidence.New(dbPath, evidence.WithBlobRoot(blobRoot))
	if err != nil {
		t.Fatalf("evidence.New second: %v", err)
	}
	t.Cleanup(func() { _ = ev2.Close() })
	ledger1, err := runledger.NewWithDB(ev1.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB first: %v", err)
	}
	ledger2, err := runledger.NewWithDB(ev2.DB())
	if err != nil {
		t.Fatalf("runledger.NewWithDB second: %v", err)
	}
	manager1, err := taskstate.NewManager(ledger1, ev1)
	if err != nil {
		t.Fatalf("taskstate.NewManager first: %v", err)
	}
	manager2, err := taskstate.NewManager(ledger2, ev2)
	if err != nil {
		t.Fatalf("taskstate.NewManager second: %v", err)
	}
	loop1, err := New(Config{Ledger: ledger1, Checkpoints: manager1, Engine: engine, SessionID: "sess-cas-wake"})
	if err != nil {
		t.Fatalf("New first loop: %v", err)
	}
	loop2, err := New(Config{Ledger: ledger2, Checkpoints: manager2, Engine: engine, SessionID: "sess-cas-wake"})
	if err != nil {
		t.Fatalf("New second loop: %v", err)
	}
	return loop1, loop2, ledger1
}

func blockedRetryForConcurrentWake(t *testing.T, loop *Loop) (*Intake, string, RetryWake) {
	t.Helper()
	ctx := context.Background()
	intake, err := loop.Start(ctx, Goal{Statement: "concurrent retry wake"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	taskID := intake.Tasks[0].TaskID
	seed, err := loop.SeedTask(ctx, taskID, intake.Tasks[0].Spec)
	if err != nil {
		t.Fatalf("SeedTask: %v", err)
	}
	blocked, err := loop.TurnStepV3(ctx, TurnStepRequest{
		RunID: intake.RunID, TaskID: taskID, Goal: intake.Goal, Spec: intake.Tasks[0].Spec,
		Generation: seed.Generation, Drive: seed.Drive, WorkflowInstanceID: "wf-concurrent", ActivityName: "run_turn.v3",
	})
	if err != nil {
		t.Fatalf("TurnStep: %v", err)
	}
	return intake, taskID, RetryWake{
		WaitID:                    "retry-1-concurrent",
		ExpectedCheckpointID:      blocked.ExpectedCheckpointID,
		ExpectedCheckpointVersion: blocked.ExpectedCheckpointVersion,
		BlockerDigest:             blocked.BlockerDigest,
		Category:                  blocked.BlockerCategory,
		ReasonCode:                blocked.BlockerReasonCode,
	}
}

func TestLoop_UnparkRetry_ConcurrentWorkersCreateOneSuccessor(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour)
	engine := &scriptedEngine{outcomes: []TurnOutcome{{
		Rounds: 1, Blocker: &taskstate.Blocker{Reason: "provider rate limit", RetryAfter: &future},
	}}}
	loop1, loop2, ledger := newSharedWakeLoops(t, engine)
	intake, taskID, wake := blockedRetryForConcurrentWake(t, loop1)
	results := runConcurrentRetryWakes(context.Background(), []*Loop{loop1, loop2}, intake.RunID, taskID, wake)
	for i, result := range results {
		if result.err != nil || (result.disposition != RetryWakeApplied && result.disposition != RetryWakeAlreadyApplied) {
			t.Fatalf("worker %d wake = disposition:%v err:%v, want idempotent success", i, result.disposition, result.err)
		}
	}
	latest, err := ledger.LatestTaskCheckpoint(context.Background(), taskID)
	if err != nil {
		t.Fatalf("LatestTaskCheckpoint: %v", err)
	}
	if latest.Version != wake.ExpectedCheckpointVersion+1 || latest.ParentCheckpointID != wake.ExpectedCheckpointID || latest.Reason != string(retryWakeCheckpointReason(wake)) {
		t.Fatalf("latest checkpoint = %+v, want exactly one retry-wake successor", latest)
	}
	events, err := ledger.ListEvents(context.Background(), runledger.EventQuery{RunID: intake.RunID, Types: []string{runledger.EventControllerDecision}})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	wakeEvents := 0
	for _, event := range events {
		if event.Payload["wait_id"] == wake.WaitID {
			wakeEvents++
		}
	}
	if wakeEvents != 1 {
		t.Fatalf("stable retry-wake events = %d, want 1", wakeEvents)
	}
}

func TestLoop_UnparkRetry_ConcurrentDifferentWaitCannotClaimSuccessor(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour)
	engine := &scriptedEngine{outcomes: []TurnOutcome{{
		Rounds: 1, Blocker: &taskstate.Blocker{Reason: "provider rate limit", RetryAfter: &future},
	}}}
	loop1, loop2, ledger := newSharedWakeLoops(t, engine)
	intake, taskID, firstWake := blockedRetryForConcurrentWake(t, loop1)
	secondWake := firstWake
	secondWake.WaitID = "retry-2-different-wait"
	start := make(chan struct{})
	results := make([]retryWakeResult, 2)
	var wg sync.WaitGroup
	for i, item := range []struct {
		loop *Loop
		wake RetryWake
	}{{loop1, firstWake}, {loop2, secondWake}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			wakeResult, err := item.loop.UnparkRetry(context.Background(), intake.RunID, taskID, item.wake)
			results[i].disposition, results[i].err = wakeResult.Disposition, err
		}()
	}
	close(start)
	wg.Wait()
	winners := 0
	for i, result := range results {
		if result.err != nil {
			t.Fatalf("worker %d wake error: %v", i, result.err)
		}
		if result.disposition != RetryWakeStale {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("distinct wait winners = %d, want exactly 1", winners)
	}
	latest, err := ledger.LatestTaskCheckpoint(context.Background(), taskID)
	if err != nil {
		t.Fatalf("LatestTaskCheckpoint: %v", err)
	}
	if latest.Version != firstWake.ExpectedCheckpointVersion+1 || latest.ParentCheckpointID != firstWake.ExpectedCheckpointID {
		t.Fatalf("latest checkpoint = %+v, want one successor", latest)
	}
	winnerWaitID := firstWake.WaitID
	if results[1].disposition != RetryWakeStale {
		winnerWaitID = secondWake.WaitID
	}
	if latest.Reason != retryWakeCheckpointReasonPrefix+winnerWaitID {
		t.Fatalf("successor reason = %q, want wait-bound winner %q", latest.Reason, winnerWaitID)
	}
}

func TestLoop_UnparkRetry_ConcurrentWorkersCannotReviveNewerOrTerminal(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour)
	engine := &scriptedEngine{outcomes: []TurnOutcome{{
		Rounds: 1, Blocker: &taskstate.Blocker{Reason: "provider rate limit", RetryAfter: &future},
	}}}
	loop1, loop2, ledger := newSharedWakeLoops(t, engine)
	intake, taskID, wake := blockedRetryForConcurrentWake(t, loop1)
	ctx := context.Background()
	newer, err := loop1.checkpoints.Resume(ctx, taskID)
	if err != nil {
		t.Fatalf("Resume blocked checkpoint: %v", err)
	}
	newer.State.Blocker = &taskstate.Blocker{Reason: "newer dependency blocker"}
	newer.State.UpdatedAt = time.Now().UTC()
	if _, err := loop1.checkpoints.Save(ctx, taskstate.SaveInput{
		State: newer.State, Reason: taskstate.TriggerBlocker, SessionID: loop1.sessionID, RunID: intake.RunID,
	}); err != nil {
		t.Fatalf("save newer blocker: %v", err)
	}
	for i, result := range runConcurrentRetryWakes(ctx, []*Loop{loop1, loop2}, intake.RunID, taskID, wake) {
		if result.err != nil || result.disposition != RetryWakeStale {
			t.Fatalf("worker %d stale wake against newer blocker = disposition:%v err:%v", i, result.disposition, result.err)
		}
	}

	latest, err := loop1.checkpoints.Resume(ctx, taskID)
	if err != nil {
		t.Fatalf("Resume newer checkpoint: %v", err)
	}
	terminal := latest.State
	terminal.Status = taskstate.StatusCompleted
	terminal.Blocker = nil
	terminal.Completed = []taskstate.CompletedItem{{Text: "done", EvidenceID: "ev_done"}}
	terminal.UpdatedAt = time.Now().UTC()
	if _, err := loop1.checkpoints.Save(ctx, taskstate.SaveInput{
		State: terminal, Reason: taskstate.TriggerDecisionRecorded, SessionID: loop1.sessionID, RunID: intake.RunID,
	}); err != nil {
		t.Fatalf("save terminal checkpoint: %v", err)
	}
	for i, result := range runConcurrentRetryWakes(ctx, []*Loop{loop1, loop2}, intake.RunID, taskID, wake) {
		if result.err != nil || result.disposition != RetryWakeStale {
			t.Fatalf("worker %d stale wake against terminal = disposition:%v err:%v", i, result.disposition, result.err)
		}
	}
	final, err := ledger.LatestTaskCheckpoint(ctx, taskID)
	if err != nil {
		t.Fatalf("LatestTaskCheckpoint: %v", err)
	}
	if final.Status != taskstate.StatusCompleted {
		t.Fatalf("concurrent stale wakes revived terminal checkpoint: %+v", final)
	}
}
