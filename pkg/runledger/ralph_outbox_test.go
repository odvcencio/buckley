package runledger

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
)

type recordingRalphSink struct {
	fail  bool
	calls atomic.Int32
	event atomic.Value
}

type blockingRalphSink struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (s *blockingRalphSink) WriteEvent(ctx context.Context, _ Event) error {
	if s.calls.Add(1) == 1 {
		close(s.entered)
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *recordingRalphSink) WriteEvent(_ context.Context, event Event) error {
	s.calls.Add(1)
	s.event.Store(event)
	if s.fail {
		return errors.New("injected ralph failure")
	}
	return nil
}

func TestRalphOutbox_DuplicateRetrySurvivesStoreRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ralph-retry.db")
	ctx := context.Background()
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.StartRun(ctx, AgentRun{RunID: "run-ralph-retry", SessionID: "session-ralph-retry"})
	if err != nil {
		t.Fatal(err)
	}
	failed := &recordingRalphSink{fail: true}
	store.SetRalphSink(failed)
	event := Event{
		ID: StableEventID("ralph-retry", run.RunID), RunID: run.RunID,
		Type: EventDurableTurn, Payload: map[string]any{"turn": float64(1)},
	}
	first, err := store.Append(ctx, event)
	if !errors.Is(err, ErrRalphDualWriteFailed) {
		t.Fatalf("first append error=%v, want ErrRalphDualWriteFailed", err)
	}
	if failed.calls.Load() != 1 {
		t.Fatalf("failed sink calls=%d, want 1", failed.calls.Load())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recovered := &recordingRalphSink{}
	reopened.SetRalphSink(recovered)
	if recovered.calls.Load() != 1 {
		t.Fatalf("startup recovery calls=%d, want 1 without duplicate Append", recovered.calls.Load())
	}
	second, err := reopened.Append(ctx, event)
	if err != nil {
		t.Fatalf("duplicate retry: %v", err)
	}
	if second.Sequence != first.Sequence || recovered.calls.Load() != 1 {
		t.Fatalf("retry sequence=%d calls=%d, want sequence=%d calls=1", second.Sequence, recovered.calls.Load(), first.Sequence)
	}
	canonical := recovered.event.Load().(Event)
	if canonical.ID != first.ID || canonical.SessionID != run.SessionID || canonical.Timestamp.IsZero() {
		t.Fatalf("replayed non-canonical event: %+v", canonical)
	}
	if _, err := reopened.Append(ctx, event); err != nil {
		t.Fatalf("delivered duplicate: %v", err)
	}
	if recovered.calls.Load() != 1 {
		t.Fatalf("delivered duplicate sink calls=%d, want 1", recovered.calls.Load())
	}
	var state string
	var attempts int
	if err := reopened.db.QueryRow(`
		SELECT state, attempt_count FROM run_event_ralph_outbox WHERE event_id = ?
	`, event.ID).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "delivered" || attempts != 2 {
		t.Fatalf("outbox state=%s attempts=%d, want delivered/2", state, attempts)
	}
}

func TestRalphOutbox_CanonicalDuplicateCreatesPreviouslyUntrackedDelivery(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	run, err := store.StartRun(ctx, AgentRun{RunID: "run-ralph-untracked", SessionID: "session-ralph-untracked"})
	if err != nil {
		t.Fatal(err)
	}
	event := Event{ID: StableEventID("ralph-untracked", run.RunID), RunID: run.RunID, Type: EventDurableTurn}
	first, err := store.Append(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM run_event_ralph_outbox WHERE event_id = ?`, event.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("unconfigured outbox rows=%d, err=%v", rows, err)
	}
	sink := &recordingRalphSink{}
	store.SetRalphSink(sink)
	if sink.calls.Load() != 0 {
		t.Fatalf("sink received untracked event before canonical retry: %d", sink.calls.Load())
	}
	duplicate, err := store.Append(ctx, event)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Sequence != first.Sequence || sink.calls.Load() != 1 {
		t.Fatalf("duplicate sequence=%d calls=%d, want %d/1", duplicate.Sequence, sink.calls.Load(), first.Sequence)
	}
	var state string
	if err := store.db.QueryRow(`SELECT state FROM run_event_ralph_outbox WHERE event_id = ?`, event.ID).Scan(&state); err != nil || state != "delivered" {
		t.Fatalf("reconciled outbox state=%q, err=%v", state, err)
	}
}

func TestRalphOutbox_SinkInstallationSharesAppendBoundary(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	run, err := store.StartRun(ctx, AgentRun{RunID: "run-ralph-install", SessionID: "session-ralph-install"})
	if err != nil {
		t.Fatal(err)
	}
	<-store.appendGate
	sink := &recordingRalphSink{}
	installed := make(chan struct{})
	go func() {
		store.SetRalphSink(sink)
		close(installed)
	}()
	select {
	case <-installed:
		t.Fatal("Ralph sink installation crossed an active append boundary")
	case <-time.After(25 * time.Millisecond):
	}
	store.appendGate <- struct{}{}
	select {
	case <-installed:
	case <-time.After(time.Second):
		t.Fatal("Ralph sink installation did not resume")
	}
	event := Event{ID: StableEventID("ralph-install", run.RunID), RunID: run.RunID, Type: EventDurableTurn}
	if _, err := store.Append(ctx, event); err != nil {
		t.Fatal(err)
	}
	if sink.calls.Load() != 1 {
		t.Fatalf("installed sink calls=%d, want 1", sink.calls.Load())
	}
}

func TestRalphOutbox_FinalizationAtomicallyTracksAndDeliversEveryEvent(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	finalization := seedRalphFinalization(t, store, "success")
	sink := &recordingRalphSink{}
	store.SetRalphSink(sink)
	if err := store.FinalizeRunAttempt(ctx, finalization); err != nil {
		t.Fatal(err)
	}
	if sink.calls.Load() != int32(len(finalization.Events)) {
		t.Fatalf("sink calls=%d, want %d", sink.calls.Load(), len(finalization.Events))
	}
	var delivered int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM run_event_ralph_outbox WHERE state = 'delivered'`).Scan(&delivered); err != nil || delivered != len(finalization.Events) {
		t.Fatalf("delivered outbox rows=%d, err=%v", delivered, err)
	}
}

func TestRalphOutbox_FinalizationFailureReplaysFromOutboxWithoutFinalizerRetry(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	finalization := seedRalphFinalization(t, store, "replay")
	failed := &recordingRalphSink{fail: true}
	store.SetRalphSink(failed)
	if err := store.FinalizeRunAttempt(ctx, finalization); !errors.Is(err, ErrRalphDualWriteFailed) {
		t.Fatalf("FinalizeRunAttempt = %v, want ErrRalphDualWriteFailed", err)
	}
	run, err := store.GetRun(ctx, finalization.RunID)
	if err != nil || run.EndedAt == nil || run.Status != finalization.Status {
		t.Fatalf("committed terminal run = %+v, %v", run, err)
	}
	recovered := &recordingRalphSink{}
	store.SetRalphSink(recovered)
	if recovered.calls.Load() != int32(len(finalization.Events)) {
		t.Fatalf("recovery calls=%d, want %d", recovered.calls.Load(), len(finalization.Events))
	}
	var failedRows int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM run_event_ralph_outbox WHERE state <> 'delivered'`).Scan(&failedRows); err != nil || failedRows != 0 {
		t.Fatalf("undelivered outbox rows=%d, err=%v", failedRows, err)
	}
}

func TestRalphOutbox_FinalizationOutboxFailureRollsBackLifecycle(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	finalization := seedRalphFinalization(t, store, "rollback")
	store.SetRalphSink(&recordingRalphSink{})
	if _, err := store.db.Exec(`
		CREATE TRIGGER reject_finalizer_outbox
		BEFORE INSERT ON run_event_ralph_outbox
		BEGIN
			SELECT RAISE(ABORT, 'injected finalizer outbox failure');
		END
	`); err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeRunAttempt(ctx, finalization); err == nil {
		t.Fatal("FinalizeRunAttempt unexpectedly survived outbox failure")
	}
	run, err := store.GetRun(ctx, finalization.RunID)
	if err != nil || run.EndedAt != nil {
		t.Fatalf("rolled-back run = %+v, %v", run, err)
	}
	claims, err := store.ListClaims(ctx, ClaimQuery{RunID: finalization.RunID})
	if err != nil || len(claims) != 1 {
		t.Fatalf("rolled-back claims = %+v, %v", claims, err)
	}
	if _, err := store.Current(ctx, finalization.SessionID, finalization.RunID); err != nil {
		t.Fatalf("rolled-back attachment = %v", err)
	}
	events, err := store.ListEvents(ctx, EventQuery{RunID: finalization.RunID})
	if err != nil || len(events) != 0 {
		t.Fatalf("rolled-back events = %+v, %v", events, err)
	}
}

func seedRalphFinalization(t *testing.T, store *SQLiteStore, suffix string) AttemptFinalization {
	t.Helper()
	ctx := context.Background()
	sessionID := "session-finalizer-ralph-" + suffix
	runID := "run-finalizer-ralph-" + suffix
	seedMailboxRun(t, store, sessionID, runID)
	lease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: sessionID, RunID: runID, AttemptID: "attempt-finalizer-ralph-" + suffix, LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireClaims(ctx, runID, []string{"pkg/finalizer-ralph-" + suffix}); err != nil {
		t.Fatal(err)
	}
	return AttemptFinalization{
		SessionID: sessionID, RunID: runID, AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration,
		Status: "completed", Outcome: map[string]any{"summary": "done"}, ReleaseReason: "terminal completed",
		Events: []Event{
			{ID: StableEventID("ralph-finalizer-terminal", runID), RunID: runID, SessionID: sessionID, Type: EventSubagentCompleted, Payload: map[string]any{"state": "completed"}},
			{ID: StableEventID("ralph-finalizer-release", runID), RunID: runID, SessionID: sessionID, Type: EventSubagentReleased, Payload: map[string]any{"reason": "terminal completed"}},
		},
	}
}

func TestRalphOutbox_ConcurrentDuplicateWaitsForOneDelivery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ralph-concurrent.db")
	ctx := context.Background()
	first, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	run, err := first.StartRun(ctx, AgentRun{RunID: "run-ralph-concurrent", SessionID: "session-ralph-concurrent"})
	if err != nil {
		t.Fatal(err)
	}
	sink := &blockingRalphSink{entered: make(chan struct{}), release: make(chan struct{})}
	first.SetRalphSink(sink)
	second.SetRalphSink(sink)
	event := Event{ID: StableEventID("ralph-concurrent", run.RunID), RunID: run.RunID, Type: EventDurableTurn}
	firstResult := make(chan error, 1)
	go func() {
		_, appendErr := first.Append(ctx, event)
		firstResult <- appendErr
	}()
	<-sink.entered
	secondResult := make(chan error, 1)
	go func() {
		_, appendErr := second.Append(ctx, event)
		secondResult <- appendErr
	}()
	select {
	case err := <-secondResult:
		t.Fatalf("duplicate returned before in-flight delivery settled: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(sink.release)
	if err := <-firstResult; err != nil {
		t.Fatal(err)
	}
	if err := <-secondResult; err != nil {
		t.Fatal(err)
	}
	if sink.calls.Load() != 1 {
		t.Fatalf("sink calls=%d, want 1", sink.calls.Load())
	}
}

func TestRalphOutbox_SinkInstallationDrainsMoreThanOneBoundedBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ralph-multibatch.db")
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	run, err := store.StartRun(ctx, AgentRun{RunID: "run-ralph-multibatch", SessionID: "session-ralph-multibatch"})
	if err != nil {
		t.Fatal(err)
	}
	failed := &recordingRalphSink{fail: true}
	store.SetRalphSink(failed)
	const backlog = 137
	for index := 0; index < backlog; index++ {
		_, err := store.Append(ctx, Event{
			ID: StableEventID("ralph-multibatch", run.RunID, fmt.Sprint(index)), RunID: run.RunID,
			Type: EventDurableTurn, Payload: map[string]any{"index": index},
		})
		if !errors.Is(err, ErrRalphDualWriteFailed) {
			t.Fatalf("append %d error=%v, want ErrRalphDualWriteFailed", index, err)
		}
	}
	if failed.calls.Load() != backlog {
		t.Fatalf("failed calls=%d, want %d", failed.calls.Load(), backlog)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recovered := &recordingRalphSink{}
	reopened.SetRalphSink(recovered)
	if recovered.calls.Load() != backlog {
		t.Fatalf("multi-batch recovery calls=%d, want %d", recovered.calls.Load(), backlog)
	}
	var remaining int
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM run_event_ralph_outbox WHERE state <> 'delivered'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("undelivered multi-batch rows=%d", remaining)
	}
}

func TestRalphOutbox_BoundedRecoveryHonorsCancellation(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	run, err := store.StartRun(ctx, AgentRun{RunID: "run-ralph-cancel", SessionID: "session-ralph-cancel"})
	if err != nil {
		t.Fatal(err)
	}
	failed := &recordingRalphSink{fail: true}
	store.SetRalphSink(failed)
	if _, err := store.Append(ctx, Event{ID: StableEventID("ralph-cancel", run.RunID), RunID: run.RunID, Type: EventDurableTurn}); !errors.Is(err, ErrRalphDualWriteFailed) {
		t.Fatalf("seed failed delivery=%v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	recovered := &recordingRalphSink{}
	if err := store.drainRalphOutbox(cancelled, recovered, 64); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled recovery=%v, want context.Canceled", err)
	}
	if recovered.calls.Load() != 0 {
		t.Fatalf("cancelled recovery called sink %d times", recovered.calls.Load())
	}
}
