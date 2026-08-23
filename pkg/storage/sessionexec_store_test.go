package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/sessionexec"
)

func newSessionExecStore(t *testing.T, sessionIDs ...string) *Store {
	t.Helper()
	store, err := New(filepath.Join(t.TempDir(), "sessionexec.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, sessionID := range sessionIDs {
		if err := createTestSession(store, sessionID); err != nil {
			t.Fatalf("create session %q: %v", sessionID, err)
		}
	}
	return store
}

func acceptInput(t *testing.T, store *Store, sessionID, commandID, content string) sessionexec.Receipt {
	t.Helper()
	receipt, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: sessionID, CommandID: commandID, Type: "input", Content: content, AcceptedBy: "operator@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func claimLane(t *testing.T, store *Store, sessionID string, lane sessionexec.Lane, owner string) sessionexec.Command {
	t.Helper()
	command, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: sessionID, Lane: lane, Owner: owner, LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	return command
}

func TestSessionExecMigration_FreshUpgradeAndIdempotent(t *testing.T) {
	store := newSessionExecStore(t, "session-migration")
	for _, table := range []string{"session_commands", "session_command_transcript", "session_execution_state", "session_effect_permits"} {
		var found string
		if err := store.db.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&found); err != nil {
			t.Fatalf("fresh table %q: %v", table, err)
		}
	}
	if err := runMigrations(store.db); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}

	upgradePath := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := sql.Open("sqlite", upgradePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		t.Fatal(err)
	}
	for version := 1; version <= 20; version++ {
		if _, err := db.Exec(`INSERT INTO schema_migrations(version, name) VALUES (?, ?)`, version, fmt.Sprintf("legacy-%d", version)); err != nil {
			t.Fatal(err)
		}
	}
	if err := EnableSQLiteWAL(db); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatalf("upgrade migration: %v", err)
	}
	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != 24 {
		t.Fatalf("upgrade version = %d, err=%v", version, err)
	}
	if err := runMigrations(db); err != nil {
		t.Fatalf("upgrade idempotency: %v", err)
	}
}

func TestSessionExecAccept_DuplicateConflictPrincipalBindingAndNoTranscript(t *testing.T) {
	store := newSessionExecStore(t, "session-accept")
	request := sessionexec.AcceptRequest{
		SessionID: "session-accept", CommandID: "command-fixed", Type: "input",
		Content: "queued body", AcceptedBy: "operator@example.test",
	}
	first, err := store.Accept(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || first.State != sessionexec.StateAccepted || first.TaskID != sessionexec.ForegroundTaskID {
		t.Fatalf("first receipt = %#v", first)
	}
	duplicate, err := store.Accept(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate || duplicate.Sequence != first.Sequence || duplicate.AcceptedAt.IsZero() {
		t.Fatalf("duplicate receipt = %#v", duplicate)
	}
	request.AcceptedBy = "other@example.test"
	if _, err := store.Accept(context.Background(), request); !errors.Is(err, sessionexec.ErrIdempotencyConflict) {
		t.Fatalf("principal drift error = %v", err)
	}
	var messages int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, first.SessionID).Scan(&messages); err != nil || messages != 0 {
		t.Fatalf("accepted command leaked into transcript: count=%d err=%v", messages, err)
	}
	if delta := time.Since(first.AcceptedAt); delta < -time.Second || delta > 5*time.Second {
		t.Fatalf("accepted time was not DB-current: %v", first.AcceptedAt)
	}
}

func TestSessionExecAccept_ConcurrentSequenceAndIdempotency(t *testing.T) {
	store := newSessionExecStore(t, "session-concurrent")
	const workers = 32
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	sequences := make(chan int64, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			receipt, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
				SessionID: "session-concurrent", CommandID: fmt.Sprintf("command-%02d", i),
				Type: "input", Content: fmt.Sprintf("body-%02d", i), AcceptedBy: "operator",
			})
			if err == nil {
				sequences <- receipt.Sequence
			}
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)
	close(sequences)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[int64]bool, workers)
	for sequence := range sequences {
		seen[sequence] = true
	}
	if len(seen) != workers {
		t.Fatalf("unique sequences = %d, want %d", len(seen), workers)
	}
	for sequence := int64(1); sequence <= workers; sequence++ {
		if !seen[sequence] {
			t.Fatalf("missing sequence %d", sequence)
		}
	}

	var originals atomic.Int32
	errCh = make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receipt, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
				SessionID: "session-concurrent", CommandID: "command-shared",
				Type: "input", Content: "same body", AcceptedBy: "operator",
			})
			if err == nil && !receipt.Duplicate {
				originals.Add(1)
			}
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if originals.Load() != 1 {
		t.Fatalf("concurrent original accepts = %d", originals.Load())
	}
}

func TestSessionExecClaim_TranscriptExactlyOnceAndStats(t *testing.T) {
	store := newSessionExecStore(t, "session-claim")
	acceptInput(t, store, "session-claim", "command-input", "visible only after claim")
	var before int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, "session-claim").Scan(&before); err != nil || before != 0 {
		t.Fatalf("preclaim messages=%d err=%v", before, err)
	}
	command := claimLane(t, store, "session-claim", sessionexec.LaneWork, "worker-a")
	if command.NextTranscriptOrdinal != 1 || command.Content != "visible only after claim" {
		t.Fatalf("claimed command = %#v", command)
	}
	if _, err := store.Release(context.Background(), command.Lease); err != nil {
		t.Fatal(err)
	}
	reclaimed := claimLane(t, store, "session-claim", sessionexec.LaneWork, "worker-b")
	if reclaimed.Lease.LeaseGeneration != command.Lease.LeaseGeneration+1 || reclaimed.NextTranscriptOrdinal != 1 {
		t.Fatalf("reclaimed command = %#v", reclaimed)
	}
	var messages, messageCount, totalTokens int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, "session-claim").Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT message_count, total_tokens FROM sessions WHERE session_id = ?`, "session-claim").Scan(&messageCount, &totalTokens); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || messageCount != 1 || totalTokens != 0 {
		t.Fatalf("message stats = rows:%d count:%d tokens:%d", messages, messageCount, totalTokens)
	}
}

func TestSessionExecClaim_WorkControlCoexistAndTargetSnapshot(t *testing.T) {
	store := newSessionExecStore(t, "session-lanes")
	acceptInput(t, store, "session-lanes", "command-work", "work")
	work := claimLane(t, store, "session-lanes", sessionexec.LaneWork, "work-owner")
	steerReceipt, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: "session-lanes", CommandID: "command-steer", Type: "steer",
		Content: "change direction", AcceptedBy: "operator",
	})
	if err != nil {
		t.Fatal(err)
	}
	if steerReceipt.Lane != sessionexec.LaneWork {
		t.Fatalf("steer lane = %q", steerReceipt.Lane)
	}
	if steerReceipt.TargetCommandID != work.CommandID || steerReceipt.Attempt != 0 {
		t.Fatalf("steer receipt target = %#v", steerReceipt)
	}
	duplicateSteer, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: "session-lanes", CommandID: "command-steer", Type: "steer",
		Content: "change direction", AcceptedBy: "operator",
	})
	if err != nil || !duplicateSteer.Duplicate || duplicateSteer.TargetCommandID != work.CommandID {
		t.Fatalf("duplicate steer receipt = %#v, %v", duplicateSteer, err)
	}
	storedSteer, err := store.Get(context.Background(), steerReceipt.SessionID, steerReceipt.CommandID)
	if err != nil || storedSteer.TargetCommandID != work.CommandID {
		t.Fatalf("stored steer receipt = %#v, %v", storedSteer, err)
	}
	listedSteers, err := store.List(context.Background(), sessionexec.Query{SessionID: steerReceipt.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	foundSteer := false
	for _, listed := range listedSteers {
		if listed.CommandID == steerReceipt.CommandID {
			foundSteer = listed.TargetCommandID == work.CommandID
		}
	}
	if !foundSteer {
		t.Fatalf("listed receipts omitted steer target: %#v", listedSteers)
	}
	if _, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: "session-lanes", CommandID: "command-interrupt", Type: "interrupt",
		AcceptedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}
	control := claimLane(t, store, "session-lanes", sessionexec.LaneControl, "control-owner")
	if control.TargetCommandID != work.CommandID || control.Type != "interrupt" {
		t.Fatalf("control target = %#v", control)
	}
	if _, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: "session-lanes", Lane: sessionexec.LaneWork, Owner: "other", LeaseDuration: time.Minute,
	}); !errors.Is(err, sessionexec.ErrNotFound) {
		t.Fatalf("second work claim error = %v", err)
	}
	if _, err := store.Complete(context.Background(), work.Lease, sessionexec.Completion{
		State: sessionexec.StateInterrupted, ErrorCode: "steered",
	}, nil); err != nil {
		t.Fatal(err)
	}
	steer := claimLane(t, store, "session-lanes", sessionexec.LaneWork, "steer-owner")
	if steer.Type != "steer" || steer.TargetCommandID != work.CommandID || steer.NextTranscriptOrdinal != 1 {
		t.Fatalf("steer claim = %#v", steer)
	}
}

func TestSessionExecCancellationRequested_StateTamperAndTwoStoreVisibility(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cancellation.db")
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
	if err := createTestSession(first, "session-cancellation"); err != nil {
		t.Fatal(err)
	}
	acceptInput(t, first, "session-cancellation", "command-work", "work")
	work := claimLane(t, first, "session-cancellation", sessionexec.LaneWork, "worker-a")
	requested, err := second.CancellationRequested(context.Background(), work.SessionID, work.CommandID)
	if err != nil || requested {
		t.Fatalf("pre-signal cancellation = %v, %v", requested, err)
	}
	interruptRequest := sessionexec.AcceptRequest{
		SessionID: work.SessionID, CommandID: "command-interrupt", Type: "interrupt", AcceptedBy: "operator",
	}
	interruptReceipt, err := second.Accept(context.Background(), interruptRequest)
	if err != nil || interruptReceipt.TargetCommandID != work.CommandID {
		t.Fatalf("interrupt receipt = %#v, %v", interruptReceipt, err)
	}
	requested, err = first.CancellationRequested(context.Background(), work.SessionID, work.CommandID)
	if err != nil || !requested {
		t.Fatalf("cross-store accepted cancellation = %v, %v", requested, err)
	}
	interrupt := claimLane(t, first, work.SessionID, sessionexec.LaneControl, "control-owner")
	if _, err := first.Complete(context.Background(), interrupt.Lease, sessionexec.Completion{State: sessionexec.StateSucceeded}, nil); err != nil {
		t.Fatal(err)
	}
	requested, err = second.CancellationRequested(context.Background(), work.SessionID, work.CommandID)
	if err != nil || !requested {
		t.Fatalf("terminal signal cancellation = %v, %v", requested, err)
	}
	if _, err := second.db.Exec(`UPDATE session_commands SET input_digest = ?
		WHERE session_id = ? AND command_id = ?`, strings.Repeat("a", 64), work.SessionID, interrupt.CommandID); err != nil {
		t.Fatal(err)
	}
	requested, err = first.CancellationRequested(context.Background(), work.SessionID, work.CommandID)
	if !errors.Is(err, sessionexec.ErrIdempotencyConflict) || requested {
		t.Fatalf("tampered cancellation = %v, %v", requested, err)
	}
}

func TestSessionExecAccept_EnforcesCancellationSignalBound(t *testing.T) {
	store := newSessionExecStore(t, "session-cancellation-bound")
	acceptInput(t, store, "session-cancellation-bound", "command-work", "work")
	work := claimLane(t, store, "session-cancellation-bound", sessionexec.LaneWork, "worker")
	for index := 0; index < sessionexec.MaxCancellationSignals; index++ {
		if _, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
			SessionID: work.SessionID, CommandID: fmt.Sprintf("interrupt-%03d", index),
			Type: "interrupt", AcceptedBy: "operator",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: work.SessionID, CommandID: "interrupt-over-limit",
		Type: "interrupt", AcceptedBy: "operator",
	}); !errors.Is(err, sessionexec.ErrCancellationLimit) {
		t.Fatalf("over-bound accept error = %v", err)
	}
	requested, err := store.CancellationRequested(context.Background(), work.SessionID, work.CommandID)
	if err != nil || !requested {
		t.Fatalf("bounded cancellation = %v, %v", requested, err)
	}
}

func TestSessionExecQuiesceSession_CancelsAndPermanentlyGates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quiesce.db")
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
	const sessionID = "session-quiesce"
	if err := createTestSession(first, sessionID); err != nil {
		t.Fatal(err)
	}
	implicit, err := first.GetExecutionState(context.Background(), sessionID)
	if err != nil || implicit.Mode != sessionexec.ExecutionModeHeadless || implicit.Generation != 0 {
		t.Fatalf("implicit execution state = %+v, %v", implicit, err)
	}
	acceptInput(t, first, sessionID, "work-active", "active")
	active := claimLane(t, first, sessionID, sessionexec.LaneWork, "worker-one")
	acceptInput(t, first, sessionID, "work-queued", "queued")
	if _, err := second.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: sessionID, CommandID: "control-queued", Type: "pause", AcceptedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}

	quiesced, err := second.QuiesceSession(context.Background(), sessionID, sessionexec.ExecutionModeAdopted, "session_adopted")
	if err != nil {
		t.Fatal(err)
	}
	if quiesced.Cancelled != 3 || quiesced.Duplicate || quiesced.State.Mode != sessionexec.ExecutionModeAdopted ||
		quiesced.State.Generation != 1 || quiesced.State.ReasonCode != "session_adopted" {
		t.Fatalf("quiesce result = %+v", quiesced)
	}
	for _, commandID := range []string{"work-active", "work-queued", "control-queued"} {
		receipt, err := first.Get(context.Background(), sessionID, commandID)
		if err != nil || receipt.State != sessionexec.StateCancelled || receipt.ErrorCode != "session_adopted" {
			t.Fatalf("receipt %s = %+v, %v", commandID, receipt, err)
		}
	}
	if _, err := first.Heartbeat(context.Background(), active.Lease, time.Minute); !errors.Is(err, sessionexec.ErrSessionQuiesced) {
		t.Fatalf("quiesced heartbeat error = %v", err)
	}
	if _, err := first.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: sessionID, CommandID: "after-quiesce", Type: "input", Content: "no", AcceptedBy: "operator",
	}); !errors.Is(err, sessionexec.ErrSessionQuiesced) {
		t.Fatalf("post-quiesce accept error = %v", err)
	}
	if _, err := first.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: sessionID, Lane: sessionexec.LaneWork, Owner: "worker-two", LeaseDuration: time.Minute,
	}); !errors.Is(err, sessionexec.ErrSessionQuiesced) {
		t.Fatalf("post-quiesce claim error = %v", err)
	}
	duplicate, err := first.QuiesceSession(context.Background(), sessionID, sessionexec.ExecutionModeAdopted, "ignored_reason")
	if err != nil || !duplicate.Duplicate || duplicate.Cancelled != 0 || duplicate.State.ReasonCode != "session_adopted" {
		t.Fatalf("duplicate quiesce = %+v, %v", duplicate, err)
	}
	if _, err := first.QuiesceSession(context.Background(), sessionID, sessionexec.ExecutionModeDetached, "session_detached"); !errors.Is(err, sessionexec.ErrSessionQuiesced) {
		t.Fatalf("quiesce mode rewrite error = %v", err)
	}
}

func TestSessionExecQuiesceSession_RacesAcceptAndClaimAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quiesce-race.db")
	stores := make([]*Store, 2)
	for i := range stores {
		store, err := New(path)
		if err != nil {
			t.Fatal(err)
		}
		stores[i] = store
		t.Cleanup(func() { _ = store.Close() })
	}
	const sessionID = "session-quiesce-race"
	if err := createTestSession(stores[0], sessionID); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 32; index++ {
		acceptInput(t, stores[index%2], sessionID, fmt.Sprintf("queued-%02d", index), "queued")
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index := 0; index < 32; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, err := stores[index%2].Accept(context.Background(), sessionexec.AcceptRequest{
				SessionID: sessionID, CommandID: fmt.Sprintf("racing-%02d", index),
				Type: "input", Content: "race", AcceptedBy: "operator",
			})
			if err != nil && !errors.Is(err, sessionexec.ErrSessionQuiesced) {
				t.Errorf("racing accept: %v", err)
			}
		}(index)
	}
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			_, err := stores[index].ClaimNext(context.Background(), sessionexec.ClaimRequest{
				SessionID: sessionID, Lane: sessionexec.LaneWork,
				Owner: fmt.Sprintf("race-worker-%d", index), LeaseDuration: time.Minute,
			})
			if err != nil && !errors.Is(err, sessionexec.ErrNotFound) &&
				!errors.Is(err, sessionexec.ErrSessionQuiesced) {
				t.Errorf("racing claim: %v", err)
			}
		}(index)
	}
	quiesceDone := make(chan error, 1)
	go func() {
		<-start
		_, err := stores[1].QuiesceSession(context.Background(), sessionID, sessionexec.ExecutionModeDetached, "session_detached")
		quiesceDone <- err
	}()
	close(start)
	wg.Wait()
	if err := <-quiesceDone; err != nil {
		t.Fatal(err)
	}
	summary, err := stores[0].Summary(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Accepted != 0 || summary.Running != 0 || summary.Cancelled != summary.Total {
		t.Fatalf("post-race summary = %+v", summary)
	}
}

func TestSessionExecEffectPermit_QuiesceDrainsAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "effect-drain.db")
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
	const sessionID = "session-effect-drain"
	if err := createTestSession(first, sessionID); err != nil {
		t.Fatal(err)
	}
	acceptInput(t, first, sessionID, "effect-command", "run effect")
	command := claimLane(t, first, sessionID, sessionexec.LaneWork, "effect-owner")
	request := sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "effect-step-model-001", Kind: sessionexec.EffectKindModel,
	}
	permit, err := first.BeginEffect(context.Background(), request)
	if err != nil || permit.Duplicate {
		t.Fatalf("BeginEffect = %+v, %v", permit, err)
	}
	renewed, err := first.Heartbeat(context.Background(), command.Lease, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var permitExpiry int64
	if err := second.db.QueryRow(`SELECT expires_at_ms FROM session_effect_permits
		WHERE session_id = ? AND command_id = ? AND effect_id = ?`,
		sessionID, command.CommandID, request.EffectID).Scan(&permitExpiry); err != nil {
		t.Fatal(err)
	}
	if permitExpiry != renewed.ExpiresAt.UnixMilli() {
		t.Fatalf("permit expiry = %d, heartbeat = %d", permitExpiry, renewed.ExpiresAt.UnixMilli())
	}
	request.Lease = renewed
	duplicate, err := second.BeginEffect(context.Background(), request)
	if !errors.Is(err, sessionexec.ErrEffectAmbiguous) || !duplicate.Duplicate || duplicate.State != sessionexec.EffectStateAmbiguous {
		t.Fatalf("duplicate BeginEffect = %+v, %v", duplicate, err)
	}

	type result struct {
		value sessionexec.QuiesceResult
		err   error
	}
	quiesced := make(chan result, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		value, err := second.QuiesceSession(ctx, sessionID, sessionexec.ExecutionModeDetached, "effect_test")
		quiesced <- result{value: value, err: err}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		state, stateErr := first.GetExecutionState(context.Background(), sessionID)
		if stateErr == nil && state.Mode == sessionexec.ExecutionModeDetached {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("quiesce gate did not close: %+v, %v", state, stateErr)
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case got := <-quiesced:
		t.Fatalf("quiesce returned before effect ended: %+v", got)
	case <-time.After(30 * time.Millisecond):
	}
	if _, err := first.Heartbeat(context.Background(), renewed, time.Second); !errors.Is(err, sessionexec.ErrSessionQuiesced) && !errors.Is(err, sessionexec.ErrEffectAmbiguous) {
		t.Fatalf("heartbeat after gate close = %v", err)
	}
	if _, err := first.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: renewed, EffectID: "effect-step-tool-002", Kind: sessionexec.EffectKindTool,
	}); !errors.Is(err, sessionexec.ErrSessionQuiesced) {
		t.Fatalf("BeginEffect after gate close = %v", err)
	}
	if err := first.EndEffect(context.Background(), permit); err != nil {
		t.Fatal(err)
	}
	got := <-quiesced
	if got.err != nil || got.value.State.Mode != sessionexec.ExecutionModeDetached {
		t.Fatalf("QuiesceSession = %+v, %v", got.value, got.err)
	}
	receipt, err := second.Get(context.Background(), sessionID, command.CommandID)
	if err != nil || receipt.State != sessionexec.StateBlocked || receipt.ErrorCode != "ambiguous_effect" {
		t.Fatalf("quiesced command = %+v, %v", receipt, err)
	}
}

func TestSessionExecEffectPermit_QuiesceTimeoutLeavesGateClosedForExpiryRetry(t *testing.T) {
	store := newSessionExecStore(t, "session-effect-timeout")
	acceptInput(t, store, "session-effect-timeout", "effect-timeout-command", "run effect")
	command, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: "session-effect-timeout", Lane: sessionexec.LaneWork,
		Owner: "effect-timeout-owner", LeaseDuration: 120 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "effect-timeout-model", Kind: sessionexec.EffectKindModel,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	first, err := store.QuiesceSession(ctx, command.SessionID, sessionexec.ExecutionModeDetached, "timeout_test")
	cancel()
	if !errors.Is(err, sessionexec.ErrQuiescenceIncomplete) || first.State.Mode != sessionexec.ExecutionModeDetached {
		t.Fatalf("timed quiesce = %+v, %v", first, err)
	}
	state, err := store.GetExecutionState(context.Background(), command.SessionID)
	if err != nil || state.Mode != sessionexec.ExecutionModeDetached {
		t.Fatalf("closed gate = %+v, %v", state, err)
	}
	time.Sleep(130 * time.Millisecond)
	retryCtx, retryCancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	retry, err := store.QuiesceSession(retryCtx, command.SessionID, sessionexec.ExecutionModeDetached, "timeout_test")
	retryCancel()
	if !errors.Is(err, sessionexec.ErrQuiescenceIncomplete) || !retry.Duplicate {
		t.Fatalf("quiesce expiry retry = %+v, %v", retry, err)
	}
	if err := store.EndEffect(context.Background(), permit); err != nil {
		t.Fatalf("EndEffect after expiry ambiguity: %v", err)
	}
	retry, err = store.QuiesceSession(context.Background(), command.SessionID, sessionexec.ExecutionModeDetached, "timeout_test")
	if err != nil || !retry.Duplicate {
		t.Fatalf("quiesce after exact EndEffect = %+v, %v", retry, err)
	}
}

func TestSessionExecEffectPermit_CapBoundsAndCorruptionFailClosed(t *testing.T) {
	store := newSessionExecStore(t, "session-effect-cap")
	acceptInput(t, store, "session-effect-cap", "effect-cap-command", "run effects")
	command := claimLane(t, store, "session-effect-cap", sessionexec.LaneWork, "effect-cap-owner")
	permits := make([]sessionexec.EffectPermit, 0, sessionexec.MaxActiveEffectPermits)
	for index := 0; index < sessionexec.MaxActiveEffectPermits; index++ {
		permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
			Lease: command.Lease, EffectID: fmt.Sprintf("effect-cap-%03d", index), Kind: sessionexec.EffectKindTool,
		})
		if err != nil {
			t.Fatalf("permit %d: %v", index, err)
		}
		permits = append(permits, permit)
	}
	if _, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "effect-cap-overflow", Kind: sessionexec.EffectKindTool,
	}); !errors.Is(err, sessionexec.ErrEffectPermitLimit) {
		t.Fatalf("permit overflow = %v", err)
	}
	if _, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: strings.Repeat("x", sessionexec.MaxEffectIDBytes+1), Kind: sessionexec.EffectKindTool,
	}); !errors.Is(err, sessionexec.ErrValidation) {
		t.Fatalf("oversized effect id = %v", err)
	}
	if _, err := store.db.Exec(`UPDATE session_effect_permits SET lease_owner = 'tampered-owner'
		WHERE session_id = ? AND command_id = ? AND effect_id = ?`,
		command.SessionID, command.CommandID, permits[0].EffectID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginEffect(context.Background(), permits[0].EffectRequest); !errors.Is(err, sessionexec.ErrEffectPermitConflict) {
		t.Fatalf("tampered duplicate = %v", err)
	}
	if _, err := store.Heartbeat(context.Background(), command.Lease, time.Minute); !errors.Is(err, sessionexec.ErrEffectAmbiguous) {
		t.Fatalf("heartbeat with corrupt permit = %v", err)
	}
}

func TestSessionExecAccept_CancellationSignalLimitConcurrentTwoStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "signal-race.db")
	stores := make([]*Store, 2)
	for i := range stores {
		store, err := New(path)
		if err != nil {
			t.Fatal(err)
		}
		stores[i] = store
		t.Cleanup(func() { _ = store.Close() })
	}
	const sessionID = "session-signal-race"
	if err := createTestSession(stores[0], sessionID); err != nil {
		t.Fatal(err)
	}
	acceptInput(t, stores[0], sessionID, "signal-target", "work")
	target := claimLane(t, stores[0], sessionID, sessionexec.LaneWork, "signal-worker")
	start := make(chan struct{})
	results := make(chan error, sessionexec.MaxCancellationSignals+1)
	for index := 0; index <= sessionexec.MaxCancellationSignals; index++ {
		go func(index int) {
			<-start
			_, err := stores[index%2].Accept(context.Background(), sessionexec.AcceptRequest{
				SessionID: sessionID, CommandID: fmt.Sprintf("signal-%03d", index),
				Type: "interrupt", AcceptedBy: "operator",
			})
			results <- err
		}(index)
	}
	close(start)
	succeeded, limited := 0, 0
	for index := 0; index <= sessionexec.MaxCancellationSignals; index++ {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, sessionexec.ErrCancellationLimit):
			limited++
		default:
			t.Fatalf("signal accept error = %v", err)
		}
	}
	if succeeded != sessionexec.MaxCancellationSignals || limited != 1 {
		t.Fatalf("signal results succeeded=%d limited=%d", succeeded, limited)
	}
	requested, err := stores[1].CancellationRequested(context.Background(), sessionID, target.CommandID)
	if err != nil || !requested {
		t.Fatalf("bounded cancellation = %v, %v", requested, err)
	}
}

func TestSessionExecLease_HeartbeatExpiredStaleAndTakeover(t *testing.T) {
	store := newSessionExecStore(t, "session-lease")
	acceptInput(t, store, "session-lease", "command-lease", "work")
	command := claimLane(t, store, "session-lease", sessionexec.LaneWork, "worker-a")
	updated, err := store.Heartbeat(context.Background(), command.Lease, 2*time.Minute)
	if err != nil || !updated.ExpiresAt.After(command.Lease.ExpiresAt) {
		t.Fatalf("heartbeat = %#v, %v", updated, err)
	}
	stale := updated
	stale.Owner = "worker-b"
	if _, err := store.Heartbeat(context.Background(), stale, time.Minute); !errors.Is(err, sessionexec.ErrLeaseStale) {
		t.Fatalf("stale heartbeat error = %v", err)
	}
	if _, err := store.db.Exec(`UPDATE session_commands SET lease_expires_at_ms = 0
		WHERE session_id = ? AND command_id = ?`, command.SessionID, command.CommandID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Heartbeat(context.Background(), updated, time.Minute); !errors.Is(err, sessionexec.ErrLeaseExpired) {
		t.Fatalf("expired heartbeat error = %v", err)
	}
	takeover := claimLane(t, store, "session-lease", sessionexec.LaneWork, "worker-b")
	if takeover.Generation != command.Generation || takeover.Lease.LeaseGeneration != updated.LeaseGeneration+1 {
		t.Fatalf("takeover fence = %#v", takeover.Lease)
	}
	if _, err := store.Release(context.Background(), updated); !errors.Is(err, sessionexec.ErrLeaseStale) {
		t.Fatalf("old release error = %v", err)
	}
}

func TestSessionExecComplete_TranscriptAtomicReplayAndConflict(t *testing.T) {
	store := newSessionExecStore(t, "session-complete")
	acceptInput(t, store, "session-complete", "command-complete", "question")
	command := claimLane(t, store, "session-complete", sessionexec.LaneWork, "worker-a")
	completion := sessionexec.Completion{
		State:   sessionexec.StateSucceeded,
		Error:   "token=super-secret",
		Outcome: sessionexec.Outcome{Code: "ok", EvidenceIDs: []string{"ev-2", "ev-1"}},
	}
	entries := []sessionexec.TranscriptEntry{{
		Ordinal: 1, Role: "assistant", Content: "answer", ContentType: "text", Tokens: 7,
	}}
	receipt, err := store.Complete(context.Background(), command.Lease, completion, entries)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != sessionexec.StateSucceeded || receipt.FinishedAt == nil || strings.Contains(receipt.Error, "super-secret") {
		t.Fatalf("completion receipt = %#v", receipt)
	}
	replay, err := store.Complete(context.Background(), command.Lease, completion, entries)
	if err != nil || !replay.Duplicate {
		t.Fatalf("completion replay = %#v, %v", replay, err)
	}
	contradiction := completion
	contradiction.State = sessionexec.StateFailed
	if _, err := store.Complete(context.Background(), command.Lease, contradiction, entries); !errors.Is(err, sessionexec.ErrTerminalConflict) {
		t.Fatalf("contradictory replay error = %v", err)
	}
	var messages, messageCount, totalTokens int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, "session-complete").Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT message_count, total_tokens FROM sessions WHERE session_id = ?`, "session-complete").Scan(&messageCount, &totalTokens); err != nil {
		t.Fatal(err)
	}
	if messages != 2 || messageCount != 2 || totalTokens != 7 {
		t.Fatalf("completion stats rows=%d count=%d tokens=%d", messages, messageCount, totalTokens)
	}
}

func TestSessionExecComplete_InjectedFailureRollsBackTranscriptAndTerminal(t *testing.T) {
	store := newSessionExecStore(t, "session-rollback")
	acceptInput(t, store, "session-rollback", "command-rollback", "question")
	command := claimLane(t, store, "session-rollback", sessionexec.LaneWork, "worker-a")
	if _, err := store.db.Exec(`CREATE TRIGGER fail_sessionexec_completion
		BEFORE UPDATE OF state ON session_commands
		WHEN NEW.state = 'succeeded'
		BEGIN SELECT RAISE(ABORT, 'injected completion failure'); END`); err != nil {
		t.Fatal(err)
	}
	entry := sessionexec.TranscriptEntry{Ordinal: 1, Role: "assistant", Content: "answer", ContentType: "text", Tokens: 5}
	if _, err := store.Complete(context.Background(), command.Lease, sessionexec.Completion{State: sessionexec.StateSucceeded}, []sessionexec.TranscriptEntry{entry}); err == nil {
		t.Fatal("completion unexpectedly succeeded")
	}
	receipt, err := store.Get(context.Background(), command.SessionID, command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State != sessionexec.StateRunning {
		t.Fatalf("state after rollback = %q", receipt.State)
	}
	var messages, mappings int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, command.SessionID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM session_command_transcript WHERE session_id = ?`, command.SessionID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if messages != 1 || mappings != 1 {
		t.Fatalf("rollback left transcript rows: messages=%d mappings=%d", messages, mappings)
	}
}

func TestSessionExecTranscript_DigestConflictFailsClosed(t *testing.T) {
	store := newSessionExecStore(t, "session-transcript")
	acceptInput(t, store, "session-transcript", "command-transcript", "body")
	command := claimLane(t, store, "session-transcript", sessionexec.LaneWork, "worker-a")
	if _, err := store.Release(context.Background(), command.Lease); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE session_command_transcript SET entry_digest = ?
		WHERE session_id = ? AND command_id = ?`, strings.Repeat("0", 64), command.SessionID, command.CommandID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: command.SessionID, Lane: sessionexec.LaneWork, Owner: "worker-b", LeaseDuration: time.Minute,
	}); !errors.Is(err, sessionexec.ErrTranscriptConflict) {
		t.Fatalf("transcript conflict error = %v", err)
	}
	receipt, err := store.Get(context.Background(), command.SessionID, command.CommandID)
	if err != nil || receipt.State != sessionexec.StateAccepted {
		t.Fatalf("claim rollback receipt = %#v, %v", receipt, err)
	}
	digest, err := sessionexec.TranscriptEntryDigest(sessionexec.TranscriptEntry{
		Ordinal: 0, Role: "user", Content: "body", ContentType: "text",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE session_command_transcript SET entry_digest = ?
		WHERE session_id = ? AND command_id = ?`, digest, command.SessionID, command.CommandID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE messages SET content = 'tampered'
		WHERE id = (SELECT message_id FROM session_command_transcript
			WHERE session_id = ? AND command_id = ?)`, command.SessionID, command.CommandID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: command.SessionID, Lane: sessionexec.LaneWork, Owner: "worker-c", LeaseDuration: time.Minute,
	}); !errors.Is(err, sessionexec.ErrTranscriptConflict) {
		t.Fatalf("mapped message conflict error = %v", err)
	}
}

func TestSessionExecReleaseRecover_PreserveGenerationAndOrdinal(t *testing.T) {
	store := newSessionExecStore(t, "session-recover")
	acceptInput(t, store, "session-recover", "command-recover", "body")
	first := claimLane(t, store, "session-recover", sessionexec.LaneWork, "worker-a")
	if _, err := store.Release(context.Background(), first.Lease); err != nil {
		t.Fatal(err)
	}
	second := claimLane(t, store, "session-recover", sessionexec.LaneWork, "worker-b")
	if _, err := store.db.Exec(`UPDATE session_commands SET lease_expires_at_ms = 0
		WHERE session_id = ? AND command_id = ?`, second.SessionID, second.CommandID); err != nil {
		t.Fatal(err)
	}
	count, err := store.RecoverExpired(context.Background(), second.SessionID)
	if err != nil || count != 1 {
		t.Fatalf("RecoverExpired = %d, %v", count, err)
	}
	third := claimLane(t, store, "session-recover", sessionexec.LaneWork, "worker-c")
	if third.Generation != 0 || third.NextTranscriptOrdinal != 1 || third.Attempt != 3 {
		t.Fatalf("recovered command = %#v", third)
	}
}

func TestSessionExecCancelPending_DoesNotMutateRunning(t *testing.T) {
	store := newSessionExecStore(t, "session-cancel")
	acceptInput(t, store, "session-cancel", "command-running", "running")
	running := claimLane(t, store, "session-cancel", sessionexec.LaneWork, "worker-a")
	acceptInput(t, store, "session-cancel", "command-pending-1", "pending-1")
	acceptInput(t, store, "session-cancel", "command-pending-2", "pending-2")
	count, err := store.CancelPending(context.Background(), "session-cancel", "operator_cancel")
	if err != nil || count != 2 {
		t.Fatalf("CancelPending = %d, %v", count, err)
	}
	runningReceipt, err := store.Get(context.Background(), running.SessionID, running.CommandID)
	if err != nil || runningReceipt.State != sessionexec.StateRunning {
		t.Fatalf("running receipt = %#v, %v", runningReceipt, err)
	}
	summary, err := store.Summary(context.Background(), "session-cancel")
	if err != nil || summary.Cancelled != 2 || summary.Running != 1 || summary.Accepted != 0 {
		t.Fatalf("summary = %#v, %v", summary, err)
	}
}

func TestSessionExecCancelPending_BoundsEachCall(t *testing.T) {
	store := newSessionExecStore(t, "session-cancel-bound")
	for i := 0; i < sessionexec.MaxCancelBatch+1; i++ {
		acceptInput(t, store, "session-cancel-bound", fmt.Sprintf("command-%03d", i), "pending")
	}
	count, err := store.CancelPending(context.Background(), "session-cancel-bound", "operator_cancel")
	if err != nil || count != sessionexec.MaxCancelBatch {
		t.Fatalf("first CancelPending = %d, %v", count, err)
	}
	summary, err := store.Summary(context.Background(), "session-cancel-bound")
	if err != nil || summary.Accepted != 1 || summary.Cancelled != sessionexec.MaxCancelBatch {
		t.Fatalf("bounded cancellation summary = %#v, %v", summary, err)
	}
}

func TestSessionExecListSummary_BoundedDeterministicAndSessionScoped(t *testing.T) {
	store := newSessionExecStore(t, "session-list", "session-foreign")
	for i := 0; i < 60; i++ {
		acceptInput(t, store, "session-list", fmt.Sprintf("command-%03d", i), fmt.Sprintf("secret-body-%03d", i))
	}
	acceptInput(t, store, "session-foreign", "command-foreign", "foreign-secret")
	receipts, err := store.List(context.Background(), sessionexec.Query{SessionID: "session-list"})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != sessionexec.DefaultListLimit {
		t.Fatalf("default list length = %d", len(receipts))
	}
	for i, receipt := range receipts {
		if receipt.SessionID != "session-list" || (i > 0 && receipt.Sequence <= receipts[i-1].Sequence) {
			t.Fatalf("nondeterministic or foreign receipt at %d: %#v", i, receipt)
		}
		serialized := fmt.Sprintf("%#v", receipt)
		if strings.Contains(serialized, "secret-body") || strings.Contains(serialized, "foreign-secret") {
			t.Fatalf("receipt exposed content: %s", serialized)
		}
	}
	summary, err := store.Summary(context.Background(), "session-list")
	if err != nil || summary.Total != 60 || summary.LastSequence != 60 || summary.WorkPending != 60 {
		t.Fatalf("summary = %#v, %v", summary, err)
	}
	if _, err := store.List(context.Background(), sessionexec.Query{SessionID: "session-list", Limit: sessionexec.MaxListLimit + 1}); !errors.Is(err, sessionexec.ErrValidation) {
		t.Fatalf("oversized list error = %v", err)
	}
}

func TestSessionExecTimestamps_NumericOrderIsFixed(t *testing.T) {
	store := newSessionExecStore(t, "session-time")
	one := acceptInput(t, store, "session-time", "command-one", "one")
	two := acceptInput(t, store, "session-time", "command-two", "two")
	if _, err := store.db.Exec(`UPDATE session_commands SET accepted_at_ms = CASE command_id
		WHEN 'command-one' THEN 1001 WHEN 'command-two' THEN 1012 END
		WHERE session_id = ?`, "session-time"); err != nil {
		t.Fatal(err)
	}
	oneReceipt, err := store.Get(context.Background(), one.SessionID, one.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	twoReceipt, err := store.Get(context.Background(), two.SessionID, two.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if oneReceipt.AcceptedAt.UnixMilli() != 1001 || twoReceipt.AcceptedAt.UnixMilli() != 1012 {
		t.Fatalf("fixed numeric timestamps = %d, %d", oneReceipt.AcceptedAt.UnixMilli(), twoReceipt.AcceptedAt.UnixMilli())
	}
}

func TestSessionExecClaim_ThirtyTwoWayExclusive(t *testing.T) {
	store := newSessionExecStore(t, "session-claim-race")
	for i := 0; i < 32; i++ {
		acceptInput(t, store, "session-claim-race", fmt.Sprintf("command-%02d", i), "body")
	}
	var wg sync.WaitGroup
	var successes atomic.Int32
	errCh := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
				SessionID: "session-claim-race", Lane: sessionexec.LaneWork,
				Owner: fmt.Sprintf("worker-%02d", i), LeaseDuration: time.Minute,
			})
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, sessionexec.ErrNotFound) {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if successes.Load() != 1 {
		t.Fatalf("claim successes = %d", successes.Load())
	}
}

func TestSessionExecValidation_RawBigAndInvalidFieldsFailClosed(t *testing.T) {
	store := newSessionExecStore(t, "session-invalid")
	tests := []sessionexec.AcceptRequest{
		{SessionID: "session-invalid", CommandID: "bad id", Type: "input", Content: "x", AcceptedBy: "operator"},
		{SessionID: "session-invalid", CommandID: "command-big", Type: "input", Content: strings.Repeat("x", sessionexec.MaxContentBytes+1), AcceptedBy: "operator"},
		{SessionID: "session-invalid", CommandID: "command-utf", Type: "input", Content: string([]byte{0xff}), AcceptedBy: "operator"},
		{SessionID: "session-invalid", CommandID: "command-principal", Type: "input", Content: "x", AcceptedBy: ""},
		{SessionID: "session-invalid", CommandID: "command-type", Type: "unknown", Content: "x", AcceptedBy: "operator"},
	}
	for i, request := range tests {
		if _, err := store.Accept(context.Background(), request); !errors.Is(err, sessionexec.ErrValidation) {
			t.Fatalf("case %d error = %v", i, err)
		}
	}
	var commands int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM session_commands WHERE session_id = ?`, "session-invalid").Scan(&commands); err != nil || commands != 0 {
		t.Fatalf("invalid writes persisted: count=%d err=%v", commands, err)
	}

	acceptInput(t, store, "session-invalid", "command-completion", "work")
	command := claimLane(t, store, "session-invalid", sessionexec.LaneWork, "worker")
	if _, err := store.Complete(context.Background(), command.Lease, sessionexec.Completion{
		State: sessionexec.StateFailed, Error: strings.Repeat("x", sessionexec.MaxErrorTextBytes+1),
	}, nil); !errors.Is(err, sessionexec.ErrValidation) {
		t.Fatalf("oversized completion error = %v", err)
	}
	refs := make([]string, sessionexec.MaxOutcomeReferences+1)
	for i := range refs {
		refs[i] = fmt.Sprintf("ev-%03d", i)
	}
	if _, err := store.Complete(context.Background(), command.Lease, sessionexec.Completion{
		State: sessionexec.StateSucceeded, Outcome: sessionexec.Outcome{EvidenceIDs: refs},
	}, nil); !errors.Is(err, sessionexec.ErrValidation) {
		t.Fatalf("oversized completion outcome error = %v", err)
	}
	receipt, err := store.Get(context.Background(), command.SessionID, command.CommandID)
	if err != nil || receipt.State != sessionexec.StateRunning {
		t.Fatalf("invalid completion mutated command: %#v, %v", receipt, err)
	}
}

func TestSessionExecList_StateFilterOrder(t *testing.T) {
	store := newSessionExecStore(t, "session-filter")
	for _, commandID := range []string{"command-c", "command-a", "command-b"} {
		acceptInput(t, store, "session-filter", commandID, commandID)
	}
	count, err := store.CancelPending(context.Background(), "session-filter", "cancelled")
	if err != nil || count != 3 {
		t.Fatalf("cancel = %d, %v", count, err)
	}
	receipts, err := store.List(context.Background(), sessionexec.Query{
		SessionID: "session-filter", States: []sessionexec.State{sessionexec.StateCancelled}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	sequences := make([]int, len(receipts))
	for i, receipt := range receipts {
		sequences[i] = int(receipt.Sequence)
	}
	if !sort.IntsAreSorted(sequences) || len(receipts) != 3 {
		t.Fatalf("filtered order = %v", sequences)
	}
}

func TestSessionExecMapping_ReplaceCurrentTranscriptRetainsOrdinalWithoutDuplicate(t *testing.T) {
	store := newSessionExecStore(t, "session-replace-current")
	acceptInput(t, store, "session-replace-current", "command-replace", "durable user input")
	claimed := claimLane(t, store, "session-replace-current", sessionexec.LaneWork, "worker-a")
	if _, err := store.Release(context.Background(), claimed.Lease); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceMessages(claimed.SessionID, []Message{{
		SessionID: claimed.SessionID, Role: "user", Content: claimed.Content,
		ContentType: "text", Timestamp: time.Now().UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
	var linked sql.NullInt64
	var payload string
	if err := store.db.QueryRow(`SELECT message_id, entry_json FROM session_command_transcript
		WHERE session_id = ? AND command_id = ? AND ordinal = 0`, claimed.SessionID, claimed.CommandID).
		Scan(&linked, &payload); err != nil {
		t.Fatal(err)
	}
	if linked.Valid || payload == "" {
		t.Fatalf("retained mapping after replacement = message:%#v payload:%q", linked, payload)
	}
	reclaimed := claimLane(t, store, claimed.SessionID, sessionexec.LaneWork, "worker-b")
	if reclaimed.NextTranscriptOrdinal != 1 {
		t.Fatalf("next transcript ordinal = %d", reclaimed.NextTranscriptOrdinal)
	}
	var userMessages, mappings int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages
		WHERE session_id = ? AND role = 'user' AND content = ?`, claimed.SessionID, claimed.Content).Scan(&userMessages); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM session_command_transcript
		WHERE session_id = ? AND command_id = ?`, claimed.SessionID, claimed.CommandID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if userMessages != 1 || mappings != 1 {
		t.Fatalf("replacement duplicated semantics: users=%d mappings=%d", userMessages, mappings)
	}
}

func TestSessionExecMapping_ReplacementMissingProjectionDoesNotReinsertBody(t *testing.T) {
	store := newSessionExecStore(t, "session-replace-missing")
	acceptInput(t, store, "session-replace-missing", "command-replace", "do not resurrect")
	claimed := claimLane(t, store, "session-replace-missing", sessionexec.LaneWork, "worker-a")
	if _, err := store.Release(context.Background(), claimed.Lease); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceMessages(claimed.SessionID, nil); err != nil {
		t.Fatal(err)
	}
	reclaimed := claimLane(t, store, claimed.SessionID, sessionexec.LaneWork, "worker-b")
	if reclaimed.NextTranscriptOrdinal != 1 {
		t.Fatalf("next transcript ordinal = %d", reclaimed.NextTranscriptOrdinal)
	}
	var messages, mappings int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, claimed.SessionID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM session_command_transcript
		WHERE session_id = ? AND command_id = ? AND message_id IS NULL`, claimed.SessionID, claimed.CommandID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if messages != 0 || mappings != 1 {
		t.Fatalf("missing projection was resurrected: messages=%d retained=%d", messages, mappings)
	}
}

func completedSessionExec(t *testing.T, sessionID string) (*Store, sessionexec.Command, sessionexec.Completion, []sessionexec.TranscriptEntry) {
	t.Helper()
	store := newSessionExecStore(t, sessionID)
	acceptInput(t, store, sessionID, "command-complete", "question")
	command := claimLane(t, store, sessionID, sessionexec.LaneWork, "worker-a")
	completion := sessionexec.Completion{State: sessionexec.StateSucceeded, Outcome: sessionexec.Outcome{Code: "ok"}}
	entries := []sessionexec.TranscriptEntry{{
		Ordinal: 1, Role: "assistant", Content: "answer", ContentType: "text", Tokens: 7,
	}}
	if _, err := store.Complete(context.Background(), command.Lease, completion, entries); err != nil {
		t.Fatal(err)
	}
	return store, command, completion, entries
}

type sessionExecMutationSnapshot struct {
	state        sessionexec.State
	mappings     int
	messages     int
	messageCount int64
	totalTokens  int64
}

func readSessionExecMutationSnapshot(t *testing.T, store *Store, command sessionexec.Command) sessionExecMutationSnapshot {
	t.Helper()
	var snapshot sessionExecMutationSnapshot
	if err := store.db.QueryRow(`SELECT state FROM session_commands
		WHERE session_id = ? AND command_id = ?`, command.SessionID, command.CommandID).Scan(&snapshot.state); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM session_command_transcript
		WHERE session_id = ? AND command_id = ?`, command.SessionID, command.CommandID).Scan(&snapshot.mappings); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, command.SessionID).Scan(&snapshot.messages); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT message_count, total_tokens FROM sessions
		WHERE session_id = ?`, command.SessionID).Scan(&snapshot.messageCount, &snapshot.totalTokens); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func tamperSessionExecCommandField(t *testing.T, store *Store, command sessionexec.Command, column string, value any) {
	t.Helper()
	conn, err := store.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = ON`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = OFF`)
	}()
	statement := fmt.Sprintf(`UPDATE session_commands SET %s = ? WHERE session_id = ? AND command_id = ?`, column)
	if _, err := conn.ExecContext(context.Background(), statement, value, command.SessionID, command.CommandID); err != nil {
		t.Fatal(err)
	}
}

func TestSessionExecComplete_RejectsTamperedAcceptanceBeforeEitherBranch(t *testing.T) {
	tamperCases := []struct {
		name   string
		column string
		value  any
	}{
		{"derived run identity", "run_id", sessionexec.RunIDForSession("different-session")},
		{"task identity", "task_id", "foreground-other"},
		{"derived turn identity", "turn_id", sessionexec.TurnID("different-command", 0)},
		{"generation", "generation", 1},
		{"sequence", "sequence", int64(2)},
		{"lane", "lane", string(sessionexec.LaneControl)},
		{"type", "command_type", "queue"},
		{"content", "content", "different-content"},
		{"principal", "accepted_by", "different@example.test"},
		{"target", "target_command_id", "different-command"},
		{"valid-shaped input digest", "input_digest", strings.Repeat("a", 64)},
	}
	phases := []struct {
		name     string
		terminal bool
	}{
		{"post-claim", false},
		{"post-terminal", true},
	}
	for _, phase := range phases {
		for _, tamper := range tamperCases {
			t.Run(phase.name+"/"+tamper.name, func(t *testing.T) {
				store := newSessionExecStore(t, "session-complete-tamper")
				acceptInput(t, store, "session-complete-tamper", "command-complete", "question")
				command := claimLane(t, store, "session-complete-tamper", sessionexec.LaneWork, "worker-a")
				completion := sessionexec.Completion{State: sessionexec.StateSucceeded, Outcome: sessionexec.Outcome{Code: "ok"}}
				entries := []sessionexec.TranscriptEntry{{
					Ordinal: 1, Role: "assistant", Content: "answer", ContentType: "text", Tokens: 7,
				}}
				if phase.terminal {
					if _, err := store.Complete(context.Background(), command.Lease, completion, entries); err != nil {
						t.Fatal(err)
					}
				}
				before := readSessionExecMutationSnapshot(t, store, command)
				tamperSessionExecCommandField(t, store, command, tamper.column, tamper.value)

				receipt, err := store.Complete(context.Background(), command.Lease, completion, entries)
				if !errors.Is(err, sessionexec.ErrIdempotencyConflict) {
					t.Fatalf("tampered completion error = %v", err)
				}
				if receipt.CommandID != "" || receipt.Duplicate {
					t.Fatalf("tampered completion returned receipt %#v", receipt)
				}
				after := readSessionExecMutationSnapshot(t, store, command)
				if after != before {
					t.Fatalf("tampered completion mutated journal: before=%#v after=%#v", before, after)
				}
			})
		}
	}
}

func TestSessionExecComplete_ReplayAuthenticatesEveryRetainedMapping(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(*testing.T, *Store, sessionexec.Command)
	}{
		{
			name: "linked message",
			tamper: func(t *testing.T, store *Store, command sessionexec.Command) {
				_, err := store.db.Exec(`UPDATE messages SET content = 'altered'
					WHERE id = (SELECT message_id FROM session_command_transcript
						WHERE session_id = ? AND command_id = ? AND ordinal = 1)`, command.SessionID, command.CommandID)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "mapping digest",
			tamper: func(t *testing.T, store *Store, command sessionexec.Command) {
				if _, err := store.db.Exec(`UPDATE session_command_transcript SET entry_digest = ?
					WHERE session_id = ? AND command_id = ? AND ordinal = 1`, strings.Repeat("0", 64), command.SessionID, command.CommandID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "mapping payload",
			tamper: func(t *testing.T, store *Store, command sessionexec.Command) {
				if _, err := store.db.Exec(`UPDATE session_command_transcript SET entry_json = '{}'
					WHERE session_id = ? AND command_id = ? AND ordinal = 1`, command.SessionID, command.CommandID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "self-consistent wrong payload",
			tamper: func(t *testing.T, store *Store, command sessionexec.Command) {
				_, payload, digest, err := sessionexec.TranscriptEntryPayload(sessionexec.TranscriptEntry{
					Ordinal: 1, Role: "assistant", Content: "different", ContentType: "text", Tokens: 7,
				})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.Exec(`UPDATE session_command_transcript SET entry_json = ?, entry_digest = ?
					WHERE session_id = ? AND command_id = ? AND ordinal = 1`, payload, digest, command.SessionID, command.CommandID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing mapping",
			tamper: func(t *testing.T, store *Store, command sessionexec.Command) {
				if _, err := store.db.Exec(`DELETE FROM session_command_transcript
					WHERE session_id = ? AND command_id = ? AND ordinal = 1`, command.SessionID, command.CommandID); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, command, completion, entries := completedSessionExec(t, "session-replay-"+strings.ReplaceAll(test.name, " ", "-"))
			test.tamper(t, store, command)
			if _, err := store.Complete(context.Background(), command.Lease, completion, entries); !errors.Is(err, sessionexec.ErrTranscriptConflict) {
				t.Fatalf("replay error = %v", err)
			}
		})
	}
}

func TestSessionExecComplete_ReplayAcceptsRetainedMappingAfterReplaceMessages(t *testing.T) {
	store, command, completion, entries := completedSessionExec(t, "session-terminal-replace")
	if err := store.ReplaceMessages(command.SessionID, []Message{
		{SessionID: command.SessionID, Role: "user", Content: "question", ContentType: "text", Timestamp: time.Now().UTC()},
		{SessionID: command.SessionID, Role: "assistant", Content: "answer", ContentType: "text", Tokens: 7, Timestamp: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	var retained, linked int
	if err := store.db.QueryRow(`SELECT COUNT(*), COUNT(message_id) FROM session_command_transcript
		WHERE session_id = ? AND command_id = ?`, command.SessionID, command.CommandID).Scan(&retained, &linked); err != nil {
		t.Fatal(err)
	}
	if retained != 2 || linked != 0 {
		t.Fatalf("post-replacement mappings retained=%d linked=%d", retained, linked)
	}
	replay, err := store.Complete(context.Background(), command.Lease, completion, entries)
	if err != nil || !replay.Duplicate {
		t.Fatalf("post-replacement replay = %#v, %v", replay, err)
	}
}

func TestSessionExecClaim_RejectsTamperedAcceptanceBeforeMutation(t *testing.T) {
	tests := []struct {
		name              string
		column            string
		value             any
		lane              sessionexec.Lane
		ignoreConstraints bool
	}{
		{"content", "content", "tampered", sessionexec.LaneWork, false},
		{"content control", "content", "bad\x01content", sessionexec.LaneWork, false},
		{"type", "command_type", "queue", sessionexec.LaneWork, false},
		{"principal", "accepted_by", "other", sessionexec.LaneWork, false},
		{"target", "target_command_id", "command-target", sessionexec.LaneWork, false},
		{"lane", "lane", string(sessionexec.LaneControl), sessionexec.LaneControl, false},
		{"run", "run_id", "run_tampered", sessionexec.LaneWork, false},
		{"turn", "turn_id", "turn_tampered", sessionexec.LaneWork, false},
		{"task", "task_id", "tampered", sessionexec.LaneWork, true},
		{"generation", "generation", 1, sessionexec.LaneWork, false},
		{"sequence", "sequence", 2, sessionexec.LaneWork, false},
		{"digest", "input_digest", strings.Repeat("0", 64), sessionexec.LaneWork, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newSessionExecStore(t, "session-tamper")
			acceptInput(t, store, "session-tamper", "command-tamper", "original")
			statement := fmt.Sprintf(`UPDATE session_commands SET %s = ? WHERE session_id = ? AND command_id = ?`, test.column)
			if test.ignoreConstraints {
				conn, err := store.db.Conn(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if _, err := conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := conn.ExecContext(context.Background(), statement, test.value, "session-tamper", "command-tamper"); err != nil {
					t.Fatal(err)
				}
				if _, err := conn.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = OFF`); err != nil {
					t.Fatal(err)
				}
			} else if _, err := store.db.Exec(statement, test.value, "session-tamper", "command-tamper"); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
				SessionID: "session-tamper", Lane: test.lane, Owner: "worker", LeaseDuration: time.Minute,
			}); err == nil {
				t.Fatal("tampered claim unexpectedly succeeded")
			}
			var state sessionexec.State
			var attempt, messages int
			if err := store.db.QueryRow(`SELECT state, attempt FROM session_commands
				WHERE session_id = ? AND command_id = ?`, "session-tamper", "command-tamper").Scan(&state, &attempt); err != nil {
				t.Fatal(err)
			}
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, "session-tamper").Scan(&messages); err != nil {
				t.Fatal(err)
			}
			if state != sessionexec.StateAccepted || attempt != 0 || messages != 0 {
				t.Fatalf("tamper mutated state=%q attempt=%d messages=%d", state, attempt, messages)
			}
		})
	}
}

func TestSessionExecComplete_CheckedSessionTokenAccounting(t *testing.T) {
	tests := []struct {
		name      string
		current   int64
		tokens    int64
		wantErr   bool
		wantTotal int64
	}{
		{"exact aggregate maximum", sessionexec.MaxSessionTotalTokens - sessionexec.MaxTranscriptEntryTokens, sessionexec.MaxTranscriptEntryTokens, false, sessionexec.MaxSessionTotalTokens},
		{"aggregate maximum plus one", sessionexec.MaxSessionTotalTokens - sessionexec.MaxTranscriptEntryTokens + 1, sessionexec.MaxTranscriptEntryTokens, true, sessionexec.MaxSessionTotalTokens - sessionexec.MaxTranscriptEntryTokens + 1},
		{"stored arithmetic overflow", int64(1<<63 - 1), 1, true, int64(1<<63 - 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newSessionExecStore(t, "session-token")
			acceptInput(t, store, "session-token", "command-token", "question")
			command := claimLane(t, store, "session-token", sessionexec.LaneWork, "worker")
			if _, err := store.db.Exec(`UPDATE sessions SET total_tokens = ? WHERE session_id = ?`, test.current, command.SessionID); err != nil {
				t.Fatal(err)
			}
			_, err := store.Complete(context.Background(), command.Lease, sessionexec.Completion{State: sessionexec.StateSucceeded}, []sessionexec.TranscriptEntry{{
				Ordinal: 1, Role: "assistant", Content: "answer", ContentType: "text", Tokens: test.tokens,
			}})
			if test.wantErr != (err != nil) {
				t.Fatalf("Complete error = %v", err)
			}
			var total int64
			if err := store.db.QueryRow(`SELECT total_tokens FROM sessions WHERE session_id = ?`, command.SessionID).Scan(&total); err != nil {
				t.Fatal(err)
			}
			if total != test.wantTotal {
				t.Fatalf("total tokens = %d, want %d", total, test.wantTotal)
			}
			if test.wantErr {
				var state sessionexec.State
				var mappings, messages int
				if err := store.db.QueryRow(`SELECT state FROM session_commands WHERE session_id = ? AND command_id = ?`, command.SessionID, command.CommandID).Scan(&state); err != nil {
					t.Fatal(err)
				}
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM session_command_transcript WHERE session_id = ? AND command_id = ?`, command.SessionID, command.CommandID).Scan(&mappings); err != nil {
					t.Fatal(err)
				}
				if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, command.SessionID).Scan(&messages); err != nil {
					t.Fatal(err)
				}
				if state != sessionexec.StateRunning || mappings != 1 || messages != 1 {
					t.Fatalf("failed completion mutated state=%q mappings=%d messages=%d", state, mappings, messages)
				}
			}
		})
	}
}

func TestSessionExecComplete_RoleCoherenceFailsBeforeTransaction(t *testing.T) {
	store := newSessionExecStore(t, "session-role")
	acceptInput(t, store, "session-role", "command-role", "question")
	command := claimLane(t, store, "session-role", sessionexec.LaneWork, "worker")
	if _, err := store.Complete(context.Background(), command.Lease, sessionexec.Completion{State: sessionexec.StateSucceeded}, []sessionexec.TranscriptEntry{{
		Ordinal: 1, Role: "tool", Content: "result",
	}}); !errors.Is(err, sessionexec.ErrValidation) {
		t.Fatalf("invalid role completion error = %v", err)
	}
	var state sessionexec.State
	var mappings int
	if err := store.db.QueryRow(`SELECT state FROM session_commands WHERE session_id = ? AND command_id = ?`, command.SessionID, command.CommandID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM session_command_transcript WHERE session_id = ? AND command_id = ?`, command.SessionID, command.CommandID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	if state != sessionexec.StateRunning || mappings != 1 {
		t.Fatalf("role preflight mutated state=%q mappings=%d", state, mappings)
	}
}

func TestSessionExec_CrossStoreAcceptAndClaimRace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cross-store.db")
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
	if err := createTestSession(first, "session-cross-store"); err != nil {
		t.Fatal(err)
	}
	stores := []*Store{first, second}
	start := make(chan struct{})
	receipts := make(chan sessionexec.Receipt, 2)
	errs := make(chan error, 2)
	for _, store := range stores {
		go func(store *Store) {
			<-start
			receipt, err := store.Accept(context.Background(), sessionexec.AcceptRequest{
				SessionID: "session-cross-store", CommandID: "command-cross",
				Type: "input", Content: "same", AcceptedBy: "operator",
			})
			receipts <- receipt
			errs <- err
		}(store)
	}
	close(start)
	duplicates := 0
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if (<-receipts).Duplicate {
			duplicates++
		}
	}
	if duplicates != 1 {
		t.Fatalf("cross-store duplicate receipts = %d", duplicates)
	}

	start = make(chan struct{})
	claims := make(chan error, 2)
	for i, store := range stores {
		go func(index int, store *Store) {
			<-start
			_, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
				SessionID: "session-cross-store", Lane: sessionexec.LaneWork,
				Owner: fmt.Sprintf("worker-%d", index), LeaseDuration: time.Minute,
			})
			claims <- err
		}(i, store)
	}
	close(start)
	successes := 0
	for i := 0; i < 2; i++ {
		err := <-claims
		if err == nil {
			successes++
		} else if !errors.Is(err, sessionexec.ErrNotFound) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("cross-store claim successes = %d", successes)
	}
}

func TestSessionExecSchema_BaseAndMigrationsParity(t *testing.T) {
	open := func(t *testing.T) *sql.DB {
		t.Helper()
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = db.Close() })
		return db
	}
	base := open(t)
	if _, err := base.Exec(schemaSQL); err != nil {
		t.Fatal(err)
	}
	migrated := open(t)
	if _, err := migrated.Exec(sessionExecSchemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := migrated.Exec(sessionExecutionStateSchemaSQL); err != nil {
		t.Fatal(err)
	}
	if _, err := migrated.Exec(sessionEffectPermitSchemaSQL); err != nil {
		t.Fatal(err)
	}
	objects := func(t *testing.T, db *sql.DB) map[string]string {
		t.Helper()
		rows, err := db.Query(`SELECT name, sql FROM sqlite_master
			WHERE sql IS NOT NULL AND (name = 'session_commands' OR name = 'session_command_transcript'
				OR name = 'session_execution_state' OR name = 'session_effect_permits'
				OR name LIKE 'idx_session_command%' OR name LIKE 'idx_session_effect%') ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		result := make(map[string]string)
		for rows.Next() {
			var name, statement string
			if err := rows.Scan(&name, &statement); err != nil {
				t.Fatal(err)
			}
			result[name] = strings.Join(strings.Fields(statement), " ")
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	baseObjects := objects(t, base)
	migratedObjects := objects(t, migrated)
	if !reflect.DeepEqual(baseObjects, migratedObjects) {
		t.Fatalf("base/migrated schema mismatch:\nbase=%#v\nmigrated=%#v", baseObjects, migratedObjects)
	}
}
