package runledger

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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

func TestListSteps_EnumeratesRunInStableOrder(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	run, err := store.StartRun(ctx, AgentRun{SessionID: "step-enumeration"})
	if err != nil {
		t.Fatal(err)
	}
	for _, stepID := range []string{"step-b", "step-a"} {
		if _, _, err := store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: stepID, Kind: "tool", InputDigest: "digest-" + stepID}); err != nil {
			t.Fatal(err)
		}
	}
	steps, err := store.ListSteps(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 || steps[0].StepID != "step-a" || steps[1].StepID != "step-b" {
		t.Fatalf("steps=%+v, want stable step ID order", steps)
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

func TestExecutionStepJournal_LegacyTerminalMutatorsRejectRetriedAttempt(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-legacy-aba"})
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: "legacy-aba", Kind: "model", InputDigest: "input-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailStepAttempt(ctx, first, "retryable pre-dispatch failure", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	retry, replay, err := store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: first.StepID, Kind: first.Kind, InputDigest: "input-a"})
	if err != nil || replay || retry.Attempt != 2 {
		t.Fatalf("retry = %+v, replay=%v, err=%v", retry, replay, err)
	}
	if err := store.CompleteStep(ctx, run.RunID, first.StepID, "stale-evidence", "stale-digest", time.Now().UTC()); !errors.Is(err, ErrStepAttemptRequired) {
		t.Fatalf("legacy CompleteStep error = %v, want attempt required", err)
	}
	if err := store.FailStep(ctx, run.RunID, first.StepID, "stale failure", time.Now().UTC()); !errors.Is(err, ErrStepAttemptRequired) {
		t.Fatalf("legacy FailStep error = %v, want attempt required", err)
	}
	got, err := store.GetStep(ctx, run.RunID, first.StepID)
	if err != nil || got.Status != StepStarted || got.Attempt != 2 {
		t.Fatalf("step after stale legacy writers = %+v, err=%v", got, err)
	}
}

func TestExecutionStepJournal_RecoveryDistinguishesDispatchBoundary(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-recovery-phase"})
	if err != nil {
		t.Fatal(err)
	}

	claimed, _, err := store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: "claimed-step", Kind: "model"})
	if err != nil {
		t.Fatal(err)
	}
	if claimed.DispatchState != StepDispatchClaimed {
		t.Fatalf("claimed dispatch state = %q", claimed.DispatchState)
	}
	_, _, err = store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: claimed.StepID, Kind: claimed.Kind})
	var recovery *StepRecoveryError
	if !errors.As(err, &recovery) || recovery.Action != StepRecoveryResume {
		t.Fatalf("claimed recovery = %#v, err=%v", recovery, err)
	}
	if err := store.MarkStepDispatched(ctx, claimed, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkStepDispatched(ctx, claimed, time.Now().UTC()); err != nil {
		t.Fatalf("idempotent dispatch mark: %v", err)
	}
	_, _, err = store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: claimed.StepID, Kind: claimed.Kind})
	recovery = nil
	if !errors.As(err, &recovery) || recovery.Action != StepRecoveryRerun || recovery.DispatchState != StepDispatchDispatched {
		t.Fatalf("dispatched recovery = %#v, err=%v", recovery, err)
	}

	legacy, _, err := store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: "legacy-started", Kind: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE execution_steps SET dispatch_state = NULL WHERE run_id = ? AND step_id = ?`, run.RunID, legacy.StepID); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: legacy.StepID, Kind: legacy.Kind})
	recovery = nil
	if !errors.As(err, &recovery) || recovery.Action != StepRecoveryRerun || recovery.DispatchState != "" {
		t.Fatalf("legacy recovery = %#v, err=%v", recovery, err)
	}
}

func TestExecutionStepJournal_ReclaimFencesPriorOwnerBeforeDispatch(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-claim-fence"})
	if err != nil {
		t.Fatal(err)
	}
	owner1, _, err := store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: "fenced-model", Kind: "model", InputDigest: "input-a"})
	if err != nil {
		t.Fatal(err)
	}
	existing, _, beginErr := store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: owner1.StepID, Kind: owner1.Kind, InputDigest: owner1.InputDigest})
	var recovery *StepRecoveryError
	if !errors.As(beginErr, &recovery) || recovery.Action != StepRecoveryResume || existing.ClaimGeneration != owner1.ClaimGeneration {
		t.Fatalf("recovery step=%+v error=%v", existing, beginErr)
	}
	owner2, err := store.ReclaimStep(ctx, existing, time.Now().UTC())
	if err != nil || owner2.ClaimGeneration != owner1.ClaimGeneration+1 || owner2.Attempt != owner1.Attempt {
		t.Fatalf("owner2=%+v err=%v", owner2, err)
	}
	if err := store.MarkStepDispatched(ctx, owner1, time.Now().UTC()); !errors.Is(err, ErrStepTransitionConflict) {
		t.Fatalf("stale owner dispatch error = %v", err)
	}
	if err := store.CompleteStepAttempt(ctx, owner1, "stale", "stale", time.Now().UTC()); !errors.Is(err, ErrStepTransitionConflict) {
		t.Fatalf("stale owner completion error = %v", err)
	}
	if err := store.FailStepAttempt(ctx, owner1, "stale", time.Now().UTC()); !errors.Is(err, ErrStepTransitionConflict) {
		t.Fatalf("stale owner failure error = %v", err)
	}
	if err := store.CompleteStep(ctx, run.RunID, owner1.StepID, "legacy-stale", "legacy-stale", time.Now().UTC()); !errors.Is(err, ErrStepAttemptRequired) {
		t.Fatalf("legacy completion after claim transfer error = %v", err)
	}
	if err := store.MarkStepDispatched(ctx, owner2, time.Now().UTC()); err != nil {
		t.Fatalf("new owner dispatch: %v", err)
	}
	if err := store.FailStepAttempt(ctx, owner2, "post-dispatch failure", time.Now().UTC()); !errors.Is(err, ErrStepTransitionConflict) {
		t.Fatalf("post-dispatch FailStepAttempt error = %v", err)
	}
	if err := store.BlockStep(ctx, owner2, "reconcile provider outcome", "", "", time.Now().UTC()); err != nil {
		t.Fatalf("new owner block: %v", err)
	}
	got, err := store.GetStep(ctx, run.RunID, owner1.StepID)
	if err != nil || got.Status != StepBlocked || got.ClaimGeneration != owner2.ClaimGeneration {
		t.Fatalf("terminal step=%+v err=%v", got, err)
	}
}

func TestExecutionStepJournal_FailedRetryRequiresAndPreservesInputDigest(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-failed-digest"})
	if err != nil {
		t.Fatal(err)
	}
	step, _, err := store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: "failed-digest", Kind: "model", InputDigest: "digest-a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailStepAttempt(ctx, step, "predispatch failure", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: step.StepID, Kind: step.Kind}); err == nil || !strings.Contains(err.Error(), "input digest is required") {
		t.Fatalf("empty retry digest error = %v", err)
	}
	got, err := store.GetStep(ctx, run.RunID, step.StepID)
	if err != nil || got.InputDigest != "digest-a" || got.Status != StepFailed || got.Attempt != 1 {
		t.Fatalf("failed step after rejected retry = %+v, err=%v", got, err)
	}
}

func TestAddExecutionStepClaimGeneration_MigratesLegacyRows(t *testing.T) {
	db, err := sql.Open("sqlite", "file:claim-generation-migration?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE execution_steps (run_id TEXT NOT NULL, step_id TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO execution_steps (run_id, step_id) VALUES ('run', 'step')`); err != nil {
		t.Fatal(err)
	}
	if err := addExecutionStepClaimGeneration(db); err != nil {
		t.Fatal(err)
	}
	if err := addExecutionStepClaimGeneration(db); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	var generation int
	if err := db.QueryRow(`SELECT claim_generation FROM execution_steps WHERE run_id = 'run' AND step_id = 'step'`).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if generation != 1 {
		t.Fatalf("legacy claim generation = %d, want 1", generation)
	}
}

func TestExecutionStepJournal_BlockedStepIsTerminal(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-steps"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	first, _, err := store.BeginStep(ctx, ExecutionStep{
		RunID:       run.RunID,
		TaskID:      "task-1",
		StepID:      "run/task/turn/model-blocked",
		Kind:        "model",
		InputDigest: "input-a",
	})
	if err != nil {
		t.Fatalf("BeginStep first: %v", err)
	}
	if err := store.BlockStep(ctx, first, "provider outcome is ambiguous", "ev_partial", "output-a", time.Now().UTC()); err != nil {
		t.Fatalf("BlockStep: %v", err)
	}

	blocked, replay, err := store.BeginStep(ctx, ExecutionStep{
		RunID:       run.RunID,
		TaskID:      "task-1",
		StepID:      first.StepID,
		Kind:        "model",
		InputDigest: "input-a",
	})
	if err != nil {
		t.Fatalf("BeginStep blocked: %v", err)
	}
	if !replay || blocked.Status != StepBlocked || blocked.Attempt != 1 || blocked.Error != "provider outcome is ambiguous" || blocked.OutputEvidenceID != "ev_partial" || blocked.OutputDigest != "output-a" {
		t.Fatalf("blocked step = %+v, replay=%v", blocked, replay)
	}
}

func TestExecutionStepJournal_TerminalTransitionsAreAttemptGuarded(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-guarded-steps"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	blocked, _, err := store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: "blocked-step", Kind: "model", InputDigest: "input-a"})
	if err != nil {
		t.Fatalf("BeginStep blocked: %v", err)
	}
	if err := store.BlockStep(ctx, blocked, "ambiguous provider result", "ev-partial", "digest-partial", time.Now().UTC()); err != nil {
		t.Fatalf("BlockStep: %v", err)
	}
	if err := store.BlockStep(ctx, blocked, "ambiguous provider result", "ev-partial", "digest-partial", time.Now().UTC()); err != nil {
		t.Fatalf("idempotent BlockStep: %v", err)
	}
	if err := store.FailStepAttempt(ctx, blocked, "late failure", time.Now().UTC()); !errors.Is(err, ErrStepTransitionConflict) {
		t.Fatalf("blocked -> failed error = %v, want transition conflict", err)
	}
	if err := store.CompleteStepAttempt(ctx, blocked, "ev-late", "digest-late", time.Now().UTC()); !errors.Is(err, ErrStepTransitionConflict) {
		t.Fatalf("blocked -> completed error = %v, want transition conflict", err)
	}
	got, err := store.GetStep(ctx, run.RunID, blocked.StepID)
	if err != nil || got.Status != StepBlocked || got.Error != "ambiguous provider result" {
		t.Fatalf("blocked step after late writers = %+v, err=%v", got, err)
	}

	completed, _, err := store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: "completed-step", Kind: "model", InputDigest: "input-b"})
	if err != nil {
		t.Fatalf("BeginStep completed: %v", err)
	}
	if err := store.CompleteStepAttempt(ctx, completed, "ev-complete", "digest-complete", time.Now().UTC()); err != nil {
		t.Fatalf("CompleteStepAttempt: %v", err)
	}
	if err := store.CompleteStepAttempt(ctx, completed, "ev-complete", "digest-complete", time.Now().UTC()); err != nil {
		t.Fatalf("idempotent CompleteStepAttempt: %v", err)
	}
	if err := store.BlockStep(ctx, completed, "late block", "ev-complete", "digest-complete", time.Now().UTC()); !errors.Is(err, ErrStepTransitionConflict) {
		t.Fatalf("completed -> blocked error = %v, want transition conflict", err)
	}
	if err := store.FailStepAttempt(ctx, completed, "late failure", time.Now().UTC()); !errors.Is(err, ErrStepTransitionConflict) {
		t.Fatalf("completed -> failed error = %v, want transition conflict", err)
	}

	stale, _, err := store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: "retried-step", Kind: "tool", InputDigest: "input-c"})
	if err != nil {
		t.Fatalf("BeginStep stale: %v", err)
	}
	if err := store.FailStepAttempt(ctx, stale, "retryable", time.Now().UTC()); err != nil {
		t.Fatalf("FailStepAttempt: %v", err)
	}
	if err := store.FailStepAttempt(ctx, stale, "retryable", time.Now().UTC()); err != nil {
		t.Fatalf("idempotent FailStepAttempt: %v", err)
	}
	retry, replay, err := store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: stale.StepID, Kind: "tool", InputDigest: "input-c"})
	if err != nil || replay || retry.Attempt != 2 {
		t.Fatalf("retry = %+v, replay=%v, err=%v", retry, replay, err)
	}
	if err := store.CompleteStepAttempt(ctx, stale, "ev-stale", "digest-stale", time.Now().UTC()); !errors.Is(err, ErrStepTransitionConflict) {
		t.Fatalf("stale completion error = %v, want transition conflict", err)
	}
	if err := store.CompleteStepAttempt(ctx, retry, "ev-retry", "digest-retry", time.Now().UTC()); err != nil {
		t.Fatalf("retry completion: %v", err)
	}
}

func TestExecutionStepJournal_ConcurrentBeginHasSingleOwner(t *testing.T) {
	store := newTestStore(t)
	ctx := t.Context()
	run, err := store.StartRun(ctx, AgentRun{SessionID: "sess-concurrent-step"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	const callers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	var providerCalls atomic.Int32
	errCh := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, replay, err := store.BeginStep(ctx, ExecutionStep{RunID: run.RunID, StepID: "concurrent-model", Kind: "model", InputDigest: "input-a"})
			if err == nil && !replay {
				providerCalls.Add(1)
				return
			}
			if !errors.Is(err, ErrStepInProgress) {
				errCh <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent BeginStep: %v", err)
	}
	if got := providerCalls.Load(); got != 1 {
		t.Fatalf("provider owners = %d, want 1", got)
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
