package runledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
)

func TestNormalizeAttachmentHeartbeatContextError(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	deadline, deadlineCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer deadlineCancel()

	tests := []struct {
		name string
		ctx  context.Context
		err  error
		want error
	}{
		{name: "live context", ctx: context.Background(), err: fmt.Errorf("wrapped: %w", sql.ErrTxDone), want: sql.ErrTxDone},
		{name: "canceled", ctx: canceled, err: fmt.Errorf("wrapped: %w", sql.ErrTxDone), want: context.Canceled},
		{name: "deadline", ctx: deadline, err: fmt.Errorf("wrapped: %w", sql.ErrTxDone), want: context.DeadlineExceeded},
		{name: "stale outranks cancellation", ctx: canceled, err: errors.Join(ErrAttachmentStale, sql.ErrTxDone), want: ErrAttachmentStale},
		{name: "expired outranks cancellation", ctx: canceled, err: errors.Join(ErrAttachmentExpired, sql.ErrTxDone), want: ErrAttachmentExpired},
		{name: "terminal outranks cancellation", ctx: canceled, err: errors.Join(ErrAttachmentTerminal, sql.ErrTxDone), want: ErrAttachmentTerminal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, normalized := normalizeAttachmentHeartbeatContextError(test.ctx, test.err)
			if !errors.Is(got, test.want) {
				t.Fatalf("normalize error = %v, want %v", got, test.want)
			}
			wantNormalized := test.want == context.Canceled || test.want == context.DeadlineExceeded
			if normalized != wantNormalized {
				t.Fatalf("normalized = %t, want %t", normalized, wantNormalized)
			}
			if test.want != sql.ErrTxDone && test.want != context.Canceled && test.want != context.DeadlineExceeded && got != test.err {
				t.Fatalf("ownership error was replaced: got %v, original %v", got, test.err)
			}
		})
	}
}

func TestAttachments_ExpiredGenerationCannotMutateNewOwner(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-attachments", "run-attachments")
	first, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-attachments", RunID: "run-attachments", AttemptID: "attempt-1",
		LeaseDuration: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForAttachmentExpiry(t, store, "session-attachments", "run-attachments")
	second, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-attachments", RunID: "run-attachments", AttemptID: "attempt-2",
		LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.LeaseGeneration != 1 || second.LeaseGeneration != 2 {
		t.Fatalf("generations first=%d second=%d", first.LeaseGeneration, second.LeaseGeneration)
	}
	if _, err := store.Heartbeat(ctx, agentcoord.AttachmentHeartbeatRequest{
		SessionID: "session-attachments", RunID: "run-attachments", AttemptID: first.AttemptID,
		LeaseGeneration: first.LeaseGeneration,
	}); !errors.Is(err, ErrAttachmentStale) {
		t.Fatalf("stale heartbeat=%v, want ErrAttachmentStale", err)
	}
	if err := store.Detach(ctx, agentcoord.AttachmentDetachRequest{
		SessionID: "session-attachments", RunID: "run-attachments", AttemptID: first.AttemptID,
		LeaseGeneration: first.LeaseGeneration,
	}); !errors.Is(err, ErrAttachmentStale) {
		t.Fatalf("stale detach=%v, want ErrAttachmentStale", err)
	}
	current, err := store.Current(ctx, "session-attachments", "run-attachments")
	if err != nil || current.AttemptID != second.AttemptID || current.LeaseGeneration != second.LeaseGeneration {
		t.Fatalf("current=%+v, err=%v", current, err)
	}
	if _, err := store.Heartbeat(ctx, agentcoord.AttachmentHeartbeatRequest{
		SessionID: "session-attachments", RunID: "run-attachments", AttemptID: second.AttemptID,
		LeaseGeneration: second.LeaseGeneration, LeaseDuration: time.Second,
	}); err != nil {
		t.Fatalf("current heartbeat=%v", err)
	}
}

func TestAttachments_ConcurrentAttachElectsHighestCurrentGeneration(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-attach-concurrent", "run-attach-concurrent")
	const count = 16
	leases := make(chan agentcoord.AttachmentLease, count)
	errs := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
				SessionID: "session-attach-concurrent", RunID: "run-attach-concurrent",
				LeaseDuration: 5 * time.Second,
			})
			if err != nil {
				errs <- err
				return
			}
			leases <- lease
		}()
	}
	wg.Wait()
	close(leases)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent attach: %v", err)
	}
	var highest agentcoord.AttachmentLease
	for lease := range leases {
		if lease.LeaseGeneration > highest.LeaseGeneration {
			highest = lease
		}
	}
	if highest.LeaseGeneration != 1 {
		t.Fatalf("highest generation=%d, want one elected lease", highest.LeaseGeneration)
	}
	current, err := store.Current(ctx, "session-attach-concurrent", "run-attach-concurrent")
	if err != nil {
		t.Fatal(err)
	}
	if current.AttemptID != highest.AttemptID || current.LeaseGeneration != highest.LeaseGeneration {
		t.Fatalf("current=%+v, highest=%+v", current, highest)
	}
}

func TestAttachments_FencedEndDetachesBeforeStaleCompletionCanRace(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	if _, err := store.StartRun(ctx, AgentRun{RunID: "run-fenced", SessionID: "session-fenced", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	lease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-fenced", RunID: "run-fenced", AttemptID: "attempt-fenced", LeaseDuration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.EndRunFenced(ctx, "run-fenced", "completed", time.Now().UTC(), map[string]any{"ok": true}, "session-fenced", lease.AttemptID, lease.LeaseGeneration); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Heartbeat(ctx, agentcoord.AttachmentHeartbeatRequest{
		SessionID: "session-fenced", RunID: "run-fenced", AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration,
	}); !errors.Is(err, ErrAttachmentStale) {
		t.Fatalf("post-terminal heartbeat=%v, want ErrAttachmentStale", err)
	}
}

func TestAttachments_RejectOrphanCrossSessionAndTerminalRuns(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	if _, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-owner", RunID: "run-missing",
	}); !errors.Is(err, ErrAttachmentRunMissing) {
		t.Fatalf("orphan attach = %v, want ErrAttachmentRunMissing", err)
	}
	seedMailboxRun(t, store, "session-owner", "run-owned")
	if _, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-foreign", RunID: "run-owned",
	}); !errors.Is(err, ErrAttachmentSession) {
		t.Fatalf("cross-session attach = %v, want ErrAttachmentSession", err)
	}
	if err := store.EndRun(ctx, "run-owned", "completed", time.Now().UTC(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-owner", RunID: "run-owned",
	}); !errors.Is(err, ErrAttachmentTerminal) {
		t.Fatalf("terminal attach = %v, want ErrAttachmentTerminal", err)
	}
}

func TestFinalization_StaleGenerationCannotReleaseCurrentClaims(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-replacement", "run-replacement")
	first, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-replacement", RunID: "run-replacement",
		AttemptID: "attempt-first", LeaseDuration: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireClaims(ctx, "run-replacement", []string{"pkg/kernel"}); err != nil {
		t.Fatal(err)
	}
	waitForAttachmentExpiry(t, store, "session-replacement", "run-replacement")
	second, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-replacement", RunID: "run-replacement",
		AttemptID: "attempt-second", LeaseDuration: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	staleEvent := Event{
		ID: StableEventID("terminal", first.AttemptID), Type: EventSubagentCompleted,
		SessionID: "session-replacement", RunID: "run-replacement", Payload: map[string]any{"state": "completed"},
	}
	err = store.FinalizeRunAttempt(ctx, AttemptFinalization{
		SessionID: "session-replacement", RunID: "run-replacement", AttemptID: first.AttemptID,
		LeaseGeneration: first.LeaseGeneration, Status: "completed", Outcome: map[string]any{"ok": true}, Events: []Event{staleEvent},
	})
	if !errors.Is(err, ErrAttachmentStale) {
		t.Fatalf("stale finalization = %v, want ErrAttachmentStale", err)
	}
	claims, err := store.ListClaims(ctx, ClaimQuery{RunID: "run-replacement"})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims after stale finalization = %+v, %v", claims, err)
	}
	run, err := store.GetRun(ctx, "run-replacement")
	if err != nil || run.EndedAt != nil {
		t.Fatalf("run after stale finalization = %+v, %v", run, err)
	}
	current, err := store.Current(ctx, "session-replacement", "run-replacement")
	if err != nil || current.AttemptID != second.AttemptID {
		t.Fatalf("current after stale finalization = %+v, %v", current, err)
	}
}

func waitForAttachmentExpiry(t *testing.T, store *SQLiteStore, sessionID, runID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := store.Current(context.Background(), sessionID, runID)
		if errors.Is(err, ErrAttachmentExpired) {
			return
		}
		if err != nil {
			t.Fatalf("wait for attachment expiry: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for attachment expiry")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestFinalization_EventConflictRollsBackLifecycleClaimsAndDetach(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-atomic", "run-atomic")
	lease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-atomic", RunID: "run-atomic", AttemptID: "attempt-atomic", LeaseDuration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireClaims(ctx, "run-atomic", []string{"pkg/atomic"}); err != nil {
		t.Fatal(err)
	}
	eventID := StableEventID("terminal-conflict", "run-atomic")
	if _, err := store.Append(ctx, Event{
		ID: eventID, Type: EventSubagentCompleted, RunID: "run-atomic", Payload: map[string]any{"state": "wrong"},
	}); err != nil {
		t.Fatal(err)
	}
	err = store.FinalizeRunAttempt(ctx, AttemptFinalization{
		SessionID: "session-atomic", RunID: "run-atomic", AttemptID: lease.AttemptID,
		LeaseGeneration: lease.LeaseGeneration, Status: "completed", Outcome: map[string]any{"ok": true},
		Events: []Event{{ID: eventID, Type: EventSubagentCompleted, RunID: "run-atomic", Payload: map[string]any{"state": "completed"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts with an existing immutable event") {
		t.Fatalf("finalization conflict = %v", err)
	}
	run, err := store.GetRun(ctx, "run-atomic")
	if err != nil || run.EndedAt != nil {
		t.Fatalf("run after rollback = %+v, %v", run, err)
	}
	claims, err := store.ListClaims(ctx, ClaimQuery{RunID: "run-atomic"})
	if err != nil || len(claims) != 1 {
		t.Fatalf("claims after rollback = %+v, %v", claims, err)
	}
	current, err := store.Current(ctx, "session-atomic", "run-atomic")
	if err != nil || current.AttemptID != lease.AttemptID {
		t.Fatalf("attachment after rollback = %+v, %v", current, err)
	}
}

func TestFinalization_DetachBeforeFinalizeCannotEndRunReleaseClaimsOrAppendEvents(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-detach-before-finalize", "run-detach-before-finalize")
	lease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-detach-before-finalize", RunID: "run-detach-before-finalize",
		AttemptID: "attempt-detach-before-finalize", LeaseDuration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireClaims(ctx, "run-detach-before-finalize", []string{"pkg/detach-before-finalize"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Detach(ctx, agentcoord.AttachmentDetachRequest{
		SessionID: lease.SessionID, RunID: lease.RunID, AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration,
		Reason: "process exited before durable finalization",
	}); err != nil {
		t.Fatal(err)
	}
	event := Event{
		ID: StableEventID("detach-before-finalize", lease.RunID), RunID: lease.RunID, SessionID: lease.SessionID,
		Type: EventSubagentCompleted, Payload: map[string]any{"state": "completed"},
	}
	err = store.FinalizeRunAttempt(ctx, AttemptFinalization{
		SessionID: lease.SessionID, RunID: lease.RunID, AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration,
		Status: "completed", Outcome: map[string]any{"ok": true}, Events: []Event{event},
	})
	if !errors.Is(err, ErrAttachmentStale) {
		t.Fatalf("detached first finalization=%v, want ErrAttachmentStale", err)
	}
	run, err := store.GetRun(ctx, lease.RunID)
	if err != nil || run.EndedAt != nil {
		t.Fatalf("run after rejected detached finalization=%+v err=%v", run, err)
	}
	claims, err := store.ListClaims(ctx, ClaimQuery{RunID: lease.RunID})
	if err != nil || len(claims) != 1 || claims[0].ReleasedAt != nil {
		t.Fatalf("claims after rejected detached finalization=%+v err=%v", claims, err)
	}
	events, err := store.ListEvents(ctx, EventQuery{RunID: lease.RunID})
	if err != nil || len(events) != 0 {
		t.Fatalf("events after rejected detached finalization=%+v err=%v", events, err)
	}
}

func TestFinalization_DetachedReplayRequiresIdenticalTerminalRunAndExpectedEvents(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-finalize-replay", "run-finalize-replay")
	lease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-finalize-replay", RunID: "run-finalize-replay", AttemptID: "attempt-finalize-replay", LeaseDuration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	finalization := AttemptFinalization{
		SessionID: lease.SessionID, RunID: lease.RunID, AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration,
		Status: "completed", Outcome: map[string]any{"ok": true},
		Events: []Event{{
			ID: StableEventID("finalize-replay", lease.RunID), RunID: lease.RunID, SessionID: lease.SessionID,
			Type: EventSubagentCompleted, Payload: map[string]any{"state": "completed"},
		}},
	}
	if err := store.FinalizeRunAttempt(ctx, finalization); err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeRunAttempt(ctx, finalization); err != nil {
		t.Fatalf("identical detached replay=%v", err)
	}
	missingEvent := finalization
	missingEvent.Events = []Event{{
		ID: StableEventID("finalize-replay-missing", lease.RunID), RunID: lease.RunID, SessionID: lease.SessionID,
		Type: EventSubagentCompleted, Payload: map[string]any{"state": "completed"},
	}}
	if err := store.FinalizeRunAttempt(ctx, missingEvent); err == nil || !strings.Contains(err.Error(), "is missing") {
		t.Fatalf("detached replay with absent expected event=%v", err)
	}
}

func TestFinalization_ExpiredLatestAttemptCannotEndNonterminalRun(t *testing.T) {
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-expired-finalize", "run-expired-finalize")
	lease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-expired-finalize", RunID: "run-expired-finalize", AttemptID: "attempt-expired-finalize", LeaseDuration: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireClaims(ctx, lease.RunID, []string{"pkg/expired-finalize"}); err != nil {
		t.Fatal(err)
	}
	waitForAttachmentExpiry(t, store, lease.SessionID, lease.RunID)
	err = store.FinalizeRunAttempt(ctx, AttemptFinalization{
		SessionID: lease.SessionID, RunID: lease.RunID, AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration,
		Status: "failed", Outcome: map[string]any{"error": "late"},
		Events: []Event{{ID: StableEventID("expired-finalize", lease.RunID), RunID: lease.RunID, SessionID: lease.SessionID, Type: EventSubagentFailed}},
	})
	if !errors.Is(err, ErrAttachmentExpired) {
		t.Fatalf("expired finalization=%v, want ErrAttachmentExpired", err)
	}
	run, err := store.GetRun(ctx, lease.RunID)
	if err != nil || run.EndedAt != nil {
		t.Fatalf("run after expired finalization=%+v err=%v", run, err)
	}
	claims, err := store.ListClaims(ctx, ClaimQuery{RunID: lease.RunID})
	if err != nil || len(claims) != 1 || claims[0].ReleasedAt != nil {
		t.Fatalf("claims after expired finalization=%+v err=%v", claims, err)
	}
}

func TestFinalization_RejectsEveryNonterminalStatusWithoutMutation(t *testing.T) {
	for _, status := range []string{"", "queued", "running", "resumable", "unknown", "COMPLETED"} {
		t.Run(firstNonEmptyMailbox(status, "empty"), func(t *testing.T) {
			store := newMailboxTestStore(t)
			ctx := context.Background()
			runID := "run-nonterminal-" + firstNonEmptyMailbox(status, "empty")
			sessionID := "session-nonterminal-" + firstNonEmptyMailbox(status, "empty")
			seedMailboxRun(t, store, sessionID, runID)
			lease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
				SessionID: sessionID, RunID: runID, AttemptID: "attempt-" + runID, LeaseDuration: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.AcquireClaims(ctx, runID, []string{"pkg/" + runID}); err != nil {
				t.Fatal(err)
			}
			err = store.FinalizeRunAttempt(ctx, AttemptFinalization{
				SessionID: sessionID, RunID: runID, AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration,
				Status: status, Outcome: map[string]any{"status": status},
				Events: []Event{{ID: StableEventID("nonterminal", runID), RunID: runID, SessionID: sessionID, Type: EventSubagentUpdated}},
			})
			if err == nil {
				t.Fatalf("FinalizeRunAttempt accepted nonterminal status %q", status)
			}
			run, err := store.GetRun(ctx, runID)
			if err != nil || run.EndedAt != nil {
				t.Fatalf("run after rejected status = %+v, %v", run, err)
			}
			claims, err := store.ListClaims(ctx, ClaimQuery{RunID: runID})
			if err != nil || len(claims) != 1 {
				t.Fatalf("claims after rejected status = %+v, %v", claims, err)
			}
			if _, err := store.Current(ctx, sessionID, runID); err != nil {
				t.Fatalf("attachment after rejected status = %v", err)
			}
		})
	}
}

func TestFinalization_BlockedMatchesCanonicalTerminalState(t *testing.T) {
	if !agentcoord.RunBlocked.Terminal() {
		t.Fatal("domain no longer classifies blocked as terminal")
	}
	store := newMailboxTestStore(t)
	ctx := context.Background()
	seedMailboxRun(t, store, "session-blocked", "run-blocked")
	lease, err := store.Attach(ctx, agentcoord.AttachmentRequest{
		SessionID: "session-blocked", RunID: "run-blocked", AttemptID: "attempt-blocked", LeaseDuration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeRunAttempt(ctx, AttemptFinalization{
		SessionID: "session-blocked", RunID: "run-blocked", AttemptID: lease.AttemptID, LeaseGeneration: lease.LeaseGeneration,
		Status: string(agentcoord.RunBlocked), Outcome: map[string]any{"reason": "awaiting external authority"},
		Events: []Event{{ID: StableEventID("blocked", "run-blocked"), RunID: "run-blocked", SessionID: "session-blocked", Type: EventSubagentFailed}},
	}); err != nil {
		t.Fatal(err)
	}
	run, err := store.GetRun(ctx, "run-blocked")
	if err != nil || run.EndedAt == nil || run.Status != string(agentcoord.RunBlocked) {
		t.Fatalf("blocked terminal run = %+v, %v", run, err)
	}
}
