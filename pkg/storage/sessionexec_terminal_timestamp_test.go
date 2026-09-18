package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/sessionexec"
)

func TestSessionExecComplete_MonotonicTerminalTimestamp(t *testing.T) {
	const sessionID = "session-complete-terminal-clock"
	store := newSessionExecStore(t, sessionID)
	acceptInput(t, store, sessionID, "command-complete-terminal-clock", "private future complete")
	command := claimLane(t, store, sessionID, sessionexec.LaneWork, "worker-clock")
	_, futureStarted, _ := forceSessionExecCommandFutureTiming(t, store, command.SessionID, command.CommandID, true)
	assertSessionExecCommandStatusReadable(t, store, sessionID, command.CommandID, "pre-complete")

	receipt, err := store.Complete(context.Background(), command.Lease, sessionexec.Completion{
		State: sessionexec.StateSucceeded,
	}, nil)
	if err != nil {
		t.Fatalf("Complete error = %v", err)
	}
	if receipt.FinishedAt == nil || receipt.FinishedAt.UnixMilli() < futureStarted {
		t.Fatalf("Complete receipt finished_at = %v, want >= started_at_ms %d", receipt.FinishedAt, futureStarted)
	}
	status, err := store.GetCommandStatus(context.Background(), sessionID, command.CommandID)
	if err != nil {
		t.Fatalf("completed command status: %v", err)
	}
	if status.FinishedAt == nil || status.FinishedAt.UnixMilli() < futureStarted {
		t.Fatalf("completed command status = %+v, want finished_at >= %d", status, futureStarted)
	}
	replay, err := store.Complete(context.Background(), command.Lease, sessionexec.Completion{
		State: sessionexec.StateSucceeded,
	}, nil)
	if err != nil || !replay.Duplicate || replay.FinishedAt == nil ||
		!replay.FinishedAt.Equal(*receipt.FinishedAt) {
		t.Fatalf("Complete replay = %+v, %v; want duplicate with stable finished_at %v", replay, err, receipt.FinishedAt)
	}
}

func TestSessionExecComplete_ExpiredLeaseStillRejected(t *testing.T) {
	const sessionID = "session-complete-expired-terminal-clock"
	store := newSessionExecStore(t, sessionID)
	acceptInput(t, store, sessionID, "command-complete-expired-clock", "private expired complete")
	command := claimLane(t, store, sessionID, sessionexec.LaneWork, "worker-clock")
	if _, err := store.db.Exec(`UPDATE session_commands SET lease_expires_at_ms = 0
		WHERE session_id = ? AND command_id = ?`, command.SessionID, command.CommandID); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Complete(context.Background(), command.Lease, sessionexec.Completion{
		State: sessionexec.StateSucceeded,
	}, nil); !errors.Is(err, sessionexec.ErrLeaseExpired) {
		t.Fatalf("Complete expired lease error = %v, want %v", err, sessionexec.ErrLeaseExpired)
	}
}

func TestSessionExecQuiesceSession_MonotonicTerminalTimestamps(t *testing.T) {
	const sessionID = "session-quiesce-terminal-clock"
	store := newSessionExecStore(t, sessionID)
	acceptInput(t, store, sessionID, "command-running-terminal-clock", "private future running")
	running := claimLane(t, store, sessionID, sessionexec.LaneWork, "worker-clock")
	_, futureStarted, _ := forceSessionExecCommandFutureTiming(t, store, running.SessionID, running.CommandID, true)
	queued := acceptInput(t, store, sessionID, "command-queued-terminal-clock", "private future queued")
	futureAccepted, _, _ := forceSessionExecCommandFutureTiming(t, store, queued.SessionID, queued.CommandID, false)
	assertSessionExecCommandStatusReadable(t, store, sessionID, running.CommandID, "pre-quiesce running")
	assertSessionExecCommandStatusReadable(t, store, sessionID, queued.CommandID, "pre-quiesce queued")

	quiesced, err := store.QuiesceSession(context.Background(), sessionID, sessionexec.ExecutionModeDetached, "operator_detached")
	if err != nil {
		t.Fatalf("QuiesceSession error = %v", err)
	}
	if quiesced.Cancelled != 2 {
		t.Fatalf("QuiesceSession cancelled = %d, want 2", quiesced.Cancelled)
	}
	runningStatus, err := store.GetCommandStatus(context.Background(), sessionID, running.CommandID)
	if err != nil {
		t.Fatalf("quiesced running command status: %v", err)
	}
	if runningStatus.FinishedAt == nil || runningStatus.FinishedAt.UnixMilli() < futureStarted {
		t.Fatalf("quiesced running command status = %+v, want finished_at >= %d", runningStatus, futureStarted)
	}
	queuedStatus, err := store.GetCommandStatus(context.Background(), sessionID, queued.CommandID)
	if err != nil {
		t.Fatalf("quiesced queued command status: %v", err)
	}
	if queuedStatus.FinishedAt == nil || queuedStatus.FinishedAt.UnixMilli() < futureAccepted {
		t.Fatalf("quiesced queued command status = %+v, want finished_at >= %d", queuedStatus, futureAccepted)
	}
}

func TestSessionExecBeginEffectAmbiguity_MonotonicCommandTimestamp(t *testing.T) {
	const sessionID = "session-ambiguous-terminal-clock"
	store := newSessionExecStore(t, sessionID)
	acceptInput(t, store, sessionID, "command-ambiguous-terminal-clock", "private future effect")
	command := claimLane(t, store, sessionID, sessionexec.LaneWork, "worker-clock")
	permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "effect-terminal-clock", Kind: sessionexec.EffectKindTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, futureStarted, futureLeaseExpiry := forceSessionExecCommandFutureTiming(t, store, command.SessionID, command.CommandID, true)
	forceSessionExecEffectFutureTiming(t, store, permit, futureStarted, futureLeaseExpiry)
	assertSessionExecCommandStatusReadable(t, store, sessionID, command.CommandID, "pre-ambiguous-effect")

	duplicate, err := store.BeginEffect(context.Background(), permit.EffectRequest)
	if !errors.Is(err, sessionexec.ErrEffectAmbiguous) {
		t.Fatalf("duplicate BeginEffect error = %v, want %v", err, sessionexec.ErrEffectAmbiguous)
	}
	if !duplicate.Duplicate || duplicate.State != sessionexec.EffectStateAmbiguous {
		t.Fatalf("duplicate permit = %+v, want ambiguous duplicate", duplicate)
	}
	status, err := store.GetCommandStatus(context.Background(), sessionID, command.CommandID)
	if err != nil {
		t.Fatalf("ambiguous command status: %v", err)
	}
	if status.State != sessionexec.StateBlocked || status.FinishedAt == nil ||
		status.FinishedAt.UnixMilli() < futureStarted {
		t.Fatalf("ambiguous command status = %+v, want blocked finished_at >= %d", status, futureStarted)
	}
}

func forceSessionExecCommandFutureTiming(t *testing.T, store *Store, sessionID, commandID string, started bool) (int64, int64, int64) {
	t.Helper()
	dbNow := sessionExecTestDBNowMillis(t, store)
	futureAccepted := dbNow + int64(time.Hour/time.Millisecond)
	if !started {
		if _, err := store.db.Exec(`UPDATE session_commands SET accepted_at_ms = ?
			WHERE session_id = ? AND command_id = ?`, futureAccepted, sessionID, commandID); err != nil {
			t.Fatal(err)
		}
		return futureAccepted, 0, 0
	}
	futureStarted := futureAccepted + 6
	futureLeaseExpiry := futureStarted + int64(time.Minute/time.Millisecond)
	if _, err := store.db.Exec(`UPDATE session_commands SET
		accepted_at_ms = ?, started_at_ms = ?, heartbeat_at_ms = ?, lease_expires_at_ms = ?
		WHERE session_id = ? AND command_id = ?`,
		futureAccepted, futureStarted, futureStarted, futureLeaseExpiry,
		sessionID, commandID); err != nil {
		t.Fatal(err)
	}
	return futureAccepted, futureStarted, futureLeaseExpiry
}

func forceSessionExecEffectFutureTiming(t *testing.T, store *Store, permit sessionexec.EffectPermit, createdAt, expiresAt int64) {
	t.Helper()
	if createdAt >= expiresAt {
		t.Fatalf("created_at_ms %d must be before expires_at_ms %d", createdAt, expiresAt)
	}
	if _, err := store.db.Exec(`UPDATE session_effect_permits SET created_at_ms = ?, expires_at_ms = ?
		WHERE session_id = ? AND command_id = ? AND generation = ? AND effect_id = ?`,
		createdAt, expiresAt, permit.Lease.SessionID, permit.Lease.CommandID, permit.Lease.Generation, permit.EffectID); err != nil {
		t.Fatal(err)
	}
}

func assertSessionExecCommandStatusReadable(t *testing.T, store *Store, sessionID, commandID, phase string) {
	t.Helper()
	if _, err := store.GetCommandStatus(context.Background(), sessionID, commandID); err != nil {
		t.Fatalf("%s command status: %v", phase, err)
	}
}
