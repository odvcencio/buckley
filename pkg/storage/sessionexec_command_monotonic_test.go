package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/sessionexec"
)

func TestSessionExecCancelPending_MonotonicCompletionTimestamp(t *testing.T) {
	const sessionID = "session-cancel-monotonic"
	store := newSessionExecStore(t, sessionID)

	acceptInput(t, store, sessionID, "command-clock-regressed", "private future command")
	running := claimLane(t, store, sessionID, sessionexec.LaneWork, "worker-clock")
	if _, err := store.Release(context.Background(), running.Lease); err != nil {
		t.Fatal(err)
	}
	futureAcceptedOnly := acceptInput(t, store, sessionID, "command-future-accepted", "private future accepted")
	acceptInput(t, store, sessionID, "command-plain-pending", "private pending command")

	dbNow := sessionExecTestDBNowMillis(t, store)
	futureAccepted := dbNow + int64(time.Hour/time.Millisecond)
	futureStarted := futureAccepted + 6
	if _, err := store.db.Exec(`UPDATE session_commands
		SET accepted_at_ms = ?, started_at_ms = ?
		WHERE session_id = ? AND command_id = ?`,
		futureAccepted, futureStarted, sessionID, running.CommandID); err != nil {
		t.Fatal(err)
	}
	futureAcceptedOnlyMillis := futureAccepted + 11
	if _, err := store.db.Exec(`UPDATE session_commands
		SET accepted_at_ms = ?
		WHERE session_id = ? AND command_id = ?`,
		futureAcceptedOnlyMillis, sessionID, futureAcceptedOnly.CommandID); err != nil {
		t.Fatal(err)
	}

	count, err := store.CancelPending(context.Background(), sessionID, "operator_cancelled")
	if err != nil {
		t.Fatalf("CancelPending error = %v", err)
	}
	if count != 3 {
		t.Fatalf("CancelPending count = %d, want 3", count)
	}

	status, err := store.GetCommandStatus(context.Background(), sessionID, running.CommandID)
	if err != nil {
		t.Fatalf("future cancelled command status: %v", err)
	}
	if status.State != sessionexec.StateCancelled || status.FinishedAt == nil ||
		status.AcceptedAt.UnixMilli() != futureAccepted ||
		status.StartedAt == nil || status.StartedAt.UnixMilli() != futureStarted ||
		status.FinishedAt.UnixMilli() < futureStarted {
		t.Fatalf("future cancelled command status = %+v", status)
	}
	acceptedOnlyStatus, err := store.GetCommandStatus(context.Background(), sessionID, futureAcceptedOnly.CommandID)
	if err != nil {
		t.Fatalf("future accepted-only command status: %v", err)
	}
	if acceptedOnlyStatus.State != sessionexec.StateCancelled || acceptedOnlyStatus.StartedAt != nil ||
		acceptedOnlyStatus.FinishedAt == nil || acceptedOnlyStatus.FinishedAt.UnixMilli() < futureAcceptedOnlyMillis {
		t.Fatalf("future accepted-only cancelled command status = %+v", acceptedOnlyStatus)
	}

	snapshot, err := store.GetExecutionSnapshot(context.Background(), sessionID, 10)
	if err != nil {
		t.Fatalf("execution snapshot after cancellation: %v", err)
	}
	if snapshot.Summary.Accepted != 0 || snapshot.Summary.Cancelled != 3 {
		t.Fatalf("snapshot summary = %+v, want accepted=0 cancelled=3", snapshot.Summary)
	}
	summary, err := store.Summary(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Accepted != 0 || summary.Cancelled != 3 {
		t.Fatalf("summary = %+v, want accepted=0 cancelled=3", summary)
	}
	count, err = store.CancelPending(context.Background(), sessionID, "operator_cancelled")
	if err != nil {
		t.Fatalf("second CancelPending error = %v", err)
	}
	if count != 0 {
		t.Fatalf("second CancelPending count = %d, want 0", count)
	}
}

func TestSessionExecCancelPending_CorruptTerminalTimestampStillRejected(t *testing.T) {
	const sessionID = "session-cancel-corrupt-terminal"
	store := newSessionExecStore(t, sessionID)
	receipt := acceptInput(t, store, sessionID, "command-corrupt-cancel", "private")

	count, err := store.CancelPending(context.Background(), sessionID, "operator_cancelled")
	if err != nil {
		t.Fatalf("CancelPending error = %v", err)
	}
	if count != 1 {
		t.Fatalf("CancelPending count = %d, want 1", count)
	}
	if _, err := store.db.Exec(`UPDATE session_commands
		SET completed_at_ms = accepted_at_ms - 1
		WHERE session_id = ? AND command_id = ?`, sessionID, receipt.CommandID); err != nil {
		t.Fatal(err)
	}

	if _, err := store.GetCommandStatus(context.Background(), sessionID, receipt.CommandID); !errors.Is(err, sessionexec.ErrIdempotencyConflict) {
		t.Fatalf("corrupt command status error = %v, want %v", err, sessionexec.ErrIdempotencyConflict)
	}
	if _, err := store.GetExecutionSnapshot(context.Background(), sessionID, 10); !errors.Is(err, sessionexec.ErrIdempotencyConflict) {
		t.Fatalf("corrupt snapshot error = %v, want %v", err, sessionexec.ErrIdempotencyConflict)
	}
}
