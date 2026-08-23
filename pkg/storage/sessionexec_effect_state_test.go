package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/sessionexec"
)

var (
	_ sessionexec.Journal        = (*Store)(nil)
	_ sessionexec.EffectResolver = (*Store)(nil)
)

func openAdversarialEffectStore(t *testing.T, sessionID string) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sessionexec-effects.db")
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := createTestSession(store, sessionID); err != nil {
		t.Fatal(err)
	}
	return store, path
}

func claimAdversarialEffectCommand(t *testing.T, store *Store, sessionID, commandID, owner string, lease time.Duration) sessionexec.Command {
	t.Helper()
	acceptInput(t, store, sessionID, commandID, "durable effect")
	command, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: sessionID, Lane: sessionexec.LaneWork, Owner: owner, LeaseDuration: lease,
	})
	if err != nil {
		t.Fatal(err)
	}
	return command
}

func beginAdversarialEffect(t *testing.T, store *Store, command sessionexec.Command, effectID string) sessionexec.EffectPermit {
	t.Helper()
	permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: effectID, Kind: sessionexec.EffectKindTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	return permit
}

func waitAdversarialEffectExpiry(t *testing.T, permit sessionexec.EffectPermit) {
	t.Helper()
	delay := time.Until(permit.ExpiresAt) + 30*time.Millisecond
	if delay > 0 {
		time.Sleep(delay)
	}
}

func adversarialEffectState(t *testing.T, store *Store, permit sessionexec.EffectPermit) string {
	t.Helper()
	var state string
	err := store.db.QueryRow(`SELECT state FROM session_effect_permits
		WHERE session_id = ? AND command_id = ? AND generation = ? AND effect_id = ?`,
		permit.Lease.SessionID, permit.Lease.CommandID, permit.Lease.Generation, permit.EffectID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func adversarialEffectRowCount(t *testing.T, store *Store, sessionID string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM session_effect_permits WHERE session_id = ?`, sessionID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertEffectBlocked(t *testing.T, err error) {
	t.Helper()
	if err == nil || (!errors.Is(err, sessionexec.ErrEffectPermitConflict) &&
		!errors.Is(err, sessionexec.ErrEffectAmbiguous) &&
		!errors.Is(err, sessionexec.ErrQuiescenceIncomplete)) {
		t.Fatalf("effect operation error = %v, want effect blocker", err)
	}
}

func TestSessionExecEffectAdversarial_ExpiryTombstoneSurvivesReopenAndRecoveryRefusesRequeue(t *testing.T) {
	store, path := openAdversarialEffectStore(t, "effect-tombstone")
	command := claimAdversarialEffectCommand(t, store, "effect-tombstone", "effect-tombstone-command", "tombstone-owner", 50*time.Millisecond)
	permit := beginAdversarialEffect(t, store, command, "effect-tombstone-step")
	waitAdversarialEffectExpiry(t, permit)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	recovered, recoverErr := reopened.RecoverExpired(context.Background(), command.SessionID)
	if recoverErr != nil && !errors.Is(recoverErr, sessionexec.ErrEffectAmbiguous) {
		t.Fatalf("RecoverExpired error = %v", recoverErr)
	}
	if recovered != 0 {
		t.Fatalf("RecoverExpired count = %d, want no requeue", recovered)
	}
	if state := adversarialEffectState(t, reopened, permit); state != string(sessionexec.EffectStateAmbiguous) {
		t.Fatalf("expired permit state = %q, want ambiguous tombstone", state)
	}
	receipt, err := reopened.Get(context.Background(), command.SessionID, command.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.State == sessionexec.StateAccepted {
		t.Fatal("expired command was requeued despite an ambiguous effect")
	}
	if _, err := reopened.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: command.SessionID, Lane: sessionexec.LaneWork,
		Owner: "second-owner", LeaseDuration: time.Minute,
	}); err == nil || (!errors.Is(err, sessionexec.ErrEffectAmbiguous) && !errors.Is(err, sessionexec.ErrNotFound)) {
		t.Fatalf("ClaimNext after ambiguous expiry = %v, want no reclaim", err)
	}

	third, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = third.Close() })
	if state := adversarialEffectState(t, third, permit); state != string(sessionexec.EffectStateAmbiguous) {
		t.Fatalf("reopened tombstone state = %q, want ambiguous", state)
	}
}

func TestSessionExecEffectAdversarial_QuiesceRemainsIncompleteUntilExactEnd(t *testing.T) {
	store, _ := openAdversarialEffectStore(t, "effect-quiesce-expiry")
	command := claimAdversarialEffectCommand(t, store, "effect-quiesce-expiry", "effect-quiesce-command", "quiesce-owner", 50*time.Millisecond)
	permit := beginAdversarialEffect(t, store, command, "effect-quiesce-step")

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	quiesced, quiesceErr := store.QuiesceSession(ctx, command.SessionID, sessionexec.ExecutionModeDetached, "effect_expired")
	cancel()
	if quiesced.State.Mode != sessionexec.ExecutionModeDetached {
		t.Fatalf("quiesced mode = %q, want detached", quiesced.State.Mode)
	}
	if quiesceErr == nil || (!errors.Is(quiesceErr, sessionexec.ErrQuiescenceIncomplete) && !errors.Is(quiesceErr, sessionexec.ErrEffectAmbiguous)) {
		t.Fatalf("expired quiesce error = %v, want incomplete/ambiguous", quiesceErr)
	}
	if state := adversarialEffectState(t, store, permit); state != string(sessionexec.EffectStateAmbiguous) {
		t.Fatalf("expired quiesce permit state = %q, want ambiguous", state)
	}
	tampered := permit
	tampered.Lease.Owner = "wrong-owner"
	if err := store.EndEffect(context.Background(), tampered); !errors.Is(err, sessionexec.ErrEffectPermitConflict) {
		t.Fatalf("inexact EndEffect = %v, want conflict", err)
	}
	if err := store.EndEffect(context.Background(), permit); err != nil {
		t.Fatalf("exact EndEffect after expiry = %v", err)
	}
	if state := adversarialEffectState(t, store, permit); state != string(sessionexec.EffectStateEnded) {
		t.Fatalf("ended permit state = %q, want retained ended proof", state)
	}
	if _, err := store.QuiesceSession(context.Background(), command.SessionID, sessionexec.ExecutionModeDetached, "effect_expired"); err != nil {
		t.Fatalf("quiesce after exact EndEffect = %v", err)
	}
}

func TestSessionExecEffectAdversarial_ReleaseAndCompleteRejectActiveAndAmbiguous(t *testing.T) {
	for _, state := range []sessionexec.EffectState{sessionexec.EffectStateActive, sessionexec.EffectStateAmbiguous} {
		t.Run(string(state), func(t *testing.T) {
			store, _ := openAdversarialEffectStore(t, "effect-block-"+string(state))
			command := claimAdversarialEffectCommand(t, store, "effect-block-"+string(state), "effect-block-command", "block-owner", time.Minute)
			permit := beginAdversarialEffect(t, store, command, "effect-block-step")
			if state == sessionexec.EffectStateAmbiguous {
				if _, err := store.db.Exec(`UPDATE session_effect_permits
					SET state = ?, ambiguous_at_ms = CAST(strftime('%s','now') AS INTEGER) * 1000
					WHERE session_id = ? AND command_id = ? AND generation = ? AND effect_id = ?`,
					state, command.SessionID, command.CommandID, command.Generation, permit.EffectID); err != nil {
					t.Fatal(err)
				}
			}
			_, releaseErr := store.Release(context.Background(), command.Lease)
			assertEffectBlocked(t, releaseErr)
			receipt, err := store.Get(context.Background(), command.SessionID, command.CommandID)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.State != sessionexec.StateRunning {
				t.Fatalf("state after rejected Release = %q, want running", receipt.State)
			}
			_, completeErr := store.Complete(context.Background(), command.Lease, sessionexec.Completion{State: sessionexec.StateSucceeded}, nil)
			assertEffectBlocked(t, completeErr)
			receipt, err = store.Get(context.Background(), command.SessionID, command.CommandID)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.State != sessionexec.StateRunning {
				t.Fatalf("state after rejected Complete = %q, want running", receipt.State)
			}
			if adversarialEffectState(t, store, permit) != string(state) {
				t.Fatalf("permit state changed after blocked operations: %q", adversarialEffectState(t, store, permit))
			}
		})
	}
}

func TestSessionExecEffectAdversarial_ResolveRequiresQuiescedExpiredBlockedOrCancelledAndAudits(t *testing.T) {
	t.Run("headless rejects", func(t *testing.T) {
		store, _ := openAdversarialEffectStore(t, "effect-resolve-headless")
		command := claimAdversarialEffectCommand(t, store, "effect-resolve-headless", "effect-resolve-command", "resolve-owner", 50*time.Millisecond)
		permit := beginAdversarialEffect(t, store, command, "effect-resolve-step")
		_, err := store.ResolveAmbiguousEffect(context.Background(), sessionexec.EffectResolutionRequest{
			SessionID: command.SessionID, CommandID: command.CommandID, Generation: command.Generation,
			EffectID: permit.EffectID, Actor: "operator@example.test", Reason: "manual review",
		})
		if err == nil {
			t.Fatal("headless resolve was accepted")
		}
		if state := adversarialEffectState(t, store, permit); state != string(sessionexec.EffectStateActive) {
			t.Fatalf("live headless permit state = %q, want active", state)
		}
	})

	for _, commandState := range []string{"cancelled", "blocked"} {
		t.Run(commandState, func(t *testing.T) {
			store, path := openAdversarialEffectStore(t, "effect-resolve-"+commandState)
			command := claimAdversarialEffectCommand(t, store, "effect-resolve-"+commandState, "effect-resolve-command", "resolve-owner", 50*time.Millisecond)
			permit := beginAdversarialEffect(t, store, command, "effect-resolve-step")
			waitAdversarialEffectExpiry(t, permit)
			if commandState == "blocked" {
				if _, err := store.db.Exec(`UPDATE session_commands SET
					state = ?, completed_at_ms = CAST(strftime('%s','now') AS INTEGER) * 1000,
					lease_owner = NULL, lease_expires_at_ms = NULL, heartbeat_at_ms = NULL
					WHERE session_id = ? AND command_id = ?`,
					sessionexec.StateBlocked, command.SessionID, command.CommandID); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			_, quiesceErr := store.QuiesceSession(ctx, command.SessionID, sessionexec.ExecutionModeDetached, "resolve_effect")
			cancel()
			if quiesceErr != nil && !errors.Is(quiesceErr, sessionexec.ErrQuiescenceIncomplete) && !errors.Is(quiesceErr, sessionexec.ErrEffectAmbiguous) {
				t.Fatalf("quiesce before resolve = %v", quiesceErr)
			}
			request := sessionexec.EffectResolutionRequest{
				SessionID: command.SessionID, CommandID: command.CommandID, Generation: command.Generation,
				EffectID: permit.EffectID, Actor: "operator@example.test", Reason: "provider outcome reconciled",
			}
			resolved, err := store.ResolveAmbiguousEffect(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.State != sessionexec.EffectStateResolved || resolved.ResolvedBy != request.Actor || resolved.ResolutionReason != request.Reason || resolved.ResolvedAt == nil || resolved.AmbiguousAt == nil {
				t.Fatalf("resolved permit = %+v", resolved)
			}
			var state, actor, reason string
			var ambiguousAt, resolvedAt sql.NullInt64
			if err := store.db.QueryRow(`SELECT state, resolved_by, resolution_reason, ambiguous_at_ms, resolved_at_ms
				FROM session_effect_permits WHERE session_id = ? AND command_id = ? AND effect_id = ?`,
				command.SessionID, command.CommandID, permit.EffectID).Scan(&state, &actor, &reason, &ambiguousAt, &resolvedAt); err != nil {
				t.Fatal(err)
			}
			if state != string(sessionexec.EffectStateResolved) || actor != request.Actor || reason != request.Reason || !ambiguousAt.Valid || !resolvedAt.Valid {
				t.Fatalf("resolution audit row = state:%q actor:%q reason:%q ambiguous:%v resolved:%v", state, actor, reason, ambiguousAt, resolvedAt)
			}
			if _, err := store.QuiesceSession(context.Background(), command.SessionID, sessionexec.ExecutionModeDetached, "resolve_effect"); err != nil {
				t.Fatalf("quiesce retry after resolution = %v", err)
			}
			reopened, err := New(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			replayed, err := reopened.ResolveAmbiguousEffect(context.Background(), request)
			if err != nil || !replayed.Duplicate || replayed.State != sessionexec.EffectStateResolved ||
				replayed.ResolvedBy != request.Actor || replayed.ResolutionReason != request.Reason {
				t.Fatalf("exact resolution replay = %+v, %v", replayed, err)
			}
			if err := store.EndEffect(context.Background(), permit); !errors.Is(err, sessionexec.ErrEffectPermitConflict) {
				t.Fatalf("late EndEffect after resolution = %v, want conflict", err)
			}
			var afterState, afterActor, afterReason string
			if err := store.db.QueryRow(`SELECT state, resolved_by, resolution_reason
				FROM session_effect_permits WHERE session_id = ? AND command_id = ? AND effect_id = ?`,
				command.SessionID, command.CommandID, permit.EffectID).
				Scan(&afterState, &afterActor, &afterReason); err != nil {
				t.Fatal(err)
			}
			if afterState != string(sessionexec.EffectStateResolved) || afterActor != request.Actor || afterReason != request.Reason {
				t.Fatalf("late EndEffect changed resolution audit: state=%q actor=%q reason=%q", afterState, afterActor, afterReason)
			}
			actorDrift := request
			actorDrift.Actor = "different-operator"
			if _, err := reopened.ResolveAmbiguousEffect(context.Background(), actorDrift); !errors.Is(err, sessionexec.ErrEffectPermitConflict) {
				t.Fatalf("resolution actor drift = %v, want conflict", err)
			}
			reasonDrift := request
			reasonDrift.Reason = "different outcome"
			if _, err := reopened.ResolveAmbiguousEffect(context.Background(), reasonDrift); !errors.Is(err, sessionexec.ErrEffectPermitConflict) {
				t.Fatal("resolution reason drift was accepted")
			}
		})
	}

	t.Run("quiesced live rejects", func(t *testing.T) {
		store, _ := openAdversarialEffectStore(t, "effect-resolve-live")
		command := claimAdversarialEffectCommand(t, store, "effect-resolve-live", "effect-resolve-command", "resolve-owner", time.Minute)
		permit := beginAdversarialEffect(t, store, command, "effect-resolve-step")
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, quiesceErr := store.QuiesceSession(ctx, command.SessionID, sessionexec.ExecutionModeDetached, "live_effect")
		cancel()
		if quiesceErr == nil || !errors.Is(quiesceErr, sessionexec.ErrQuiescenceIncomplete) {
			t.Fatalf("live quiesce error = %v, want incomplete", quiesceErr)
		}
		if _, err := store.ResolveAmbiguousEffect(context.Background(), sessionexec.EffectResolutionRequest{
			SessionID: command.SessionID, CommandID: command.CommandID, Generation: command.Generation,
			EffectID: permit.EffectID, Actor: "operator@example.test", Reason: "must fail while live",
		}); err == nil {
			t.Fatal("live quiesced effect was resolved")
		}
		if err := store.EndEffect(context.Background(), permit); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSessionExecEffectAdversarial_EndedCleanupOnlyAfterSuccessfulComplete(t *testing.T) {
	store, _ := openAdversarialEffectStore(t, "effect-ended-cleanup")
	command := claimAdversarialEffectCommand(t, store, "effect-ended-cleanup", "effect-ended-command", "ended-owner", time.Minute)
	permit := beginAdversarialEffect(t, store, command, "effect-ended-step")
	if err := store.EndEffect(context.Background(), permit); err != nil {
		t.Fatal(err)
	}
	if state := adversarialEffectState(t, store, permit); state != string(sessionexec.EffectStateEnded) {
		t.Fatalf("ended permit state = %q, want ended", state)
	}
	if _, err := store.Complete(context.Background(), sessionexec.LeaseRef{
		SessionID: command.SessionID, CommandID: command.CommandID, Generation: command.Generation,
		Owner: "wrong-owner", LeaseGeneration: command.Lease.LeaseGeneration, ExpiresAt: command.Lease.ExpiresAt,
	}, sessionexec.Completion{State: sessionexec.StateSucceeded}, nil); !errors.Is(err, sessionexec.ErrLeaseStale) {
		t.Fatalf("stale Complete error = %v, want stale lease", err)
	}
	if adversarialEffectState(t, store, permit) != string(sessionexec.EffectStateEnded) {
		t.Fatal("failed Complete removed ended permit")
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_effect_complete
		BEFORE UPDATE OF state ON session_commands
		WHEN NEW.state = 'succeeded'
		BEGIN SELECT RAISE(FAIL, 'forced completion rollback'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(context.Background(), command.Lease, sessionexec.Completion{State: sessionexec.StateSucceeded}, nil); err == nil || !strings.Contains(err.Error(), "forced completion rollback") {
		t.Fatalf("triggered Complete error = %v", err)
	}
	if adversarialEffectState(t, store, permit) != string(sessionexec.EffectStateEnded) {
		t.Fatal("rolled-back Complete removed ended permit")
	}
	if _, err := store.db.Exec(`DROP TRIGGER fail_effect_complete`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(context.Background(), command.Lease, sessionexec.Completion{State: sessionexec.StateSucceeded}, nil); err != nil {
		t.Fatal(err)
	}
	if count := adversarialEffectRowCount(t, store, command.SessionID); count != 0 {
		t.Fatalf("ended permit rows after successful Complete = %d, want zero", count)
	}
	if err := store.EndEffect(context.Background(), permit); err != nil {
		t.Fatalf("idempotent EndEffect after successful cleanup: %v", err)
	}
}

func TestSessionExecEffectAdversarial_ShorterExpiryTamperNeverDrains(t *testing.T) {
	store, _ := openAdversarialEffectStore(t, "effect-short-expiry")
	command := claimAdversarialEffectCommand(t, store, "effect-short-expiry", "effect-short-command", "short-owner", time.Minute)
	permit := beginAdversarialEffect(t, store, command, "effect-short-step")
	if _, err := store.db.Exec(`UPDATE session_effect_permits SET expires_at_ms = created_at_ms
		WHERE session_id = ? AND command_id = ? AND generation = ? AND effect_id = ?`,
		command.SessionID, command.CommandID, command.Generation, permit.EffectID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	_, err := store.QuiesceSession(ctx, command.SessionID, sessionexec.ExecutionModeDetached, "tampered_expiry")
	cancel()
	if err == nil || (!errors.Is(err, sessionexec.ErrQuiescenceIncomplete) &&
		!errors.Is(err, sessionexec.ErrEffectAmbiguous) &&
		!errors.Is(err, sessionexec.ErrEffectPermitConflict)) {
		t.Fatalf("tampered expiry quiesce error = %v, want incomplete/ambiguous", err)
	}
	if state := adversarialEffectState(t, store, permit); state != string(sessionexec.EffectStateAmbiguous) {
		t.Fatalf("tampered expiry permit state = %q, want ambiguous tombstone", state)
	}
	if err := store.EndEffect(context.Background(), permit); err != nil {
		t.Fatalf("exact EndEffect after tampered expiry = %v", err)
	}
	if _, err := store.QuiesceSession(context.Background(), command.SessionID, sessionexec.ExecutionModeDetached, "tampered_expiry"); err != nil {
		t.Fatal(err)
	}
}

func TestSessionExecEffectAdversarial_ActiveAndTotalCaps(t *testing.T) {
	t.Run("active", func(t *testing.T) {
		store, _ := openAdversarialEffectStore(t, "effect-active-cap")
		command := claimAdversarialEffectCommand(t, store, "effect-active-cap", "effect-active-command", "cap-owner", 10*time.Minute)
		permits := make([]sessionexec.EffectPermit, 0, sessionexec.MaxActiveEffectPermits)
		for index := 0; index < sessionexec.MaxActiveEffectPermits; index++ {
			permits = append(permits, beginAdversarialEffect(t, store, command, fmt.Sprintf("effect-active-%03d", index)))
		}
		if _, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
			Lease: command.Lease, EffectID: "effect-active-overflow", Kind: sessionexec.EffectKindTool,
		}); !errors.Is(err, sessionexec.ErrEffectPermitLimit) {
			t.Fatalf("active cap error = %v, want limit", err)
		}
		for _, permit := range permits {
			if err := store.EndEffect(context.Background(), permit); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("total", func(t *testing.T) {
		store, _ := openAdversarialEffectStore(t, "effect-total-cap")
		command := claimAdversarialEffectCommand(t, store, "effect-total-cap", "effect-total-command", "cap-owner", 10*time.Minute)
		for index := 0; index < sessionexec.MaxEffectPermitsPerSession; index++ {
			permit := beginAdversarialEffect(t, store, command, fmt.Sprintf("effect-total-%03d", index))
			if err := store.EndEffect(context.Background(), permit); err != nil {
				t.Fatalf("end total permit %d: %v", index, err)
			}
		}
		if _, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
			Lease: command.Lease, EffectID: "effect-total-overflow", Kind: sessionexec.EffectKindTool,
		}); !errors.Is(err, sessionexec.ErrEffectPermitLimit) {
			t.Fatalf("total cap error = %v, want limit", err)
		}
		if count := adversarialEffectRowCount(t, store, command.SessionID); count != sessionexec.MaxEffectPermitsPerSession {
			t.Fatalf("retained effect rows = %d, want %d", count, sessionexec.MaxEffectPermitsPerSession)
		}
	})
}

func TestSessionExecEffectAdversarial_SessionAmbiguityFencesWorkersAcrossStores(t *testing.T) {
	const sessionID = "effect-session-barrier"
	first, path := openAdversarialEffectStore(t, sessionID)
	second, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })

	commandA := claimAdversarialEffectCommand(t, first, sessionID, "effect-barrier-a", "owner-a", 80*time.Millisecond)
	permitA := beginAdversarialEffect(t, first, commandA, "effect-barrier-a-step")

	if _, err := first.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: sessionID, CommandID: "effect-barrier-b", Type: "pause", AcceptedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}
	commandB, err := first.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: sessionID, Lane: sessionexec.LaneControl, Owner: "owner-b", LeaseDuration: 10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	steer, err := first.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: sessionID, CommandID: "effect-barrier-steer", Type: "steer",
		Content: "follow-up", AcceptedBy: "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if steer.TargetCommandID != commandA.CommandID {
		t.Fatalf("steer target = %q, want %q", steer.TargetCommandID, commandA.CommandID)
	}
	if _, err := first.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: sessionID, CommandID: "effect-barrier-control", Type: "resume", AcceptedBy: "alice",
	}); err != nil {
		t.Fatal(err)
	}

	var beforeState string
	var beforeExpiry, beforeHeartbeat int64
	if err := first.db.QueryRow(`SELECT state, lease_expires_at_ms, heartbeat_at_ms
		FROM session_commands WHERE session_id = ? AND command_id = ?`, sessionID, commandB.CommandID).
		Scan(&beforeState, &beforeExpiry, &beforeHeartbeat); err != nil {
		t.Fatal(err)
	}
	if beforeState != string(sessionexec.StateRunning) {
		t.Fatalf("command B state = %q, want running", beforeState)
	}
	var beforeLastActive string
	if err := first.db.QueryRow(`SELECT last_active FROM sessions WHERE session_id = ?`, sessionID).Scan(&beforeLastActive); err != nil {
		t.Fatal(err)
	}

	waitAdversarialEffectExpiry(t, permitA)
	type operationResult struct {
		name string
		err  error
	}
	operations := []struct {
		name string
		run  func() error
	}{
		{"claim work", func() error {
			_, err := second.ClaimNext(context.Background(), sessionexec.ClaimRequest{
				SessionID: sessionID, Lane: sessionexec.LaneWork, Owner: "claim-work", LeaseDuration: time.Minute,
			})
			return err
		}},
		{"claim control", func() error {
			_, err := first.ClaimNext(context.Background(), sessionexec.ClaimRequest{
				SessionID: sessionID, Lane: sessionexec.LaneControl, Owner: "claim-control", LeaseDuration: time.Minute,
			})
			return err
		}},
		{"recover", func() error {
			_, err := second.RecoverExpired(context.Background(), sessionID)
			return err
		}},
		{"heartbeat", func() error {
			_, err := first.Heartbeat(context.Background(), commandB.Lease, 10*time.Minute)
			return err
		}},
		{"release", func() error {
			_, err := second.Release(context.Background(), commandB.Lease)
			return err
		}},
		{"complete", func() error {
			_, err := first.Complete(context.Background(), commandB.Lease,
				sessionexec.Completion{State: sessionexec.StateSucceeded}, nil)
			return err
		}},
		{"begin other effect", func() error {
			_, err := second.BeginEffect(context.Background(), sessionexec.EffectRequest{
				Lease: commandB.Lease, EffectID: "effect-barrier-b-step", Kind: sessionexec.EffectKindTool,
			})
			return err
		}},
	}
	start := make(chan struct{})
	results := make(chan operationResult, len(operations))
	for _, operation := range operations {
		operation := operation
		go func() {
			<-start
			results <- operationResult{name: operation.name, err: operation.run()}
		}()
	}
	close(start)
	for range operations {
		result := <-results
		if !errors.Is(result.err, sessionexec.ErrEffectAmbiguous) {
			t.Fatalf("%s error = %v, want session ambiguity", result.name, result.err)
		}
	}

	if state := adversarialEffectState(t, first, permitA); state != string(sessionexec.EffectStateAmbiguous) {
		t.Fatalf("command A permit state = %q, want ambiguous", state)
	}
	receiptA, err := second.Get(context.Background(), sessionID, commandA.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	if receiptA.State != sessionexec.StateBlocked || receiptA.ErrorCode != "ambiguous_effect" {
		t.Fatalf("command A receipt = %+v", receiptA)
	}
	var afterState string
	var afterExpiry, afterHeartbeat int64
	if err := second.db.QueryRow(`SELECT state, lease_expires_at_ms, heartbeat_at_ms
		FROM session_commands WHERE session_id = ? AND command_id = ?`, sessionID, commandB.CommandID).
		Scan(&afterState, &afterExpiry, &afterHeartbeat); err != nil {
		t.Fatal(err)
	}
	if afterState != beforeState || afterExpiry != beforeExpiry || afterHeartbeat != beforeHeartbeat {
		t.Fatalf("command B mutated under barrier: before=(%s,%d,%d) after=(%s,%d,%d)",
			beforeState, beforeExpiry, beforeHeartbeat, afterState, afterExpiry, afterHeartbeat)
	}
	for _, commandID := range []string{commandB.CommandID, steer.CommandID, "effect-barrier-control"} {
		receipt, err := second.Get(context.Background(), sessionID, commandID)
		if err != nil {
			t.Fatal(err)
		}
		want := sessionexec.StateAccepted
		if commandID == commandB.CommandID {
			want = sessionexec.StateRunning
		}
		if receipt.State != want {
			t.Fatalf("command %s state = %q, want %q", commandID, receipt.State, want)
		}
	}
	var speculativeMappings int
	if err := second.db.QueryRow(`SELECT COUNT(*) FROM session_command_transcript
		WHERE session_id = ? AND command_id IN (?, ?, ?)`, sessionID,
		commandB.CommandID, steer.CommandID, "effect-barrier-control").Scan(&speculativeMappings); err != nil {
		t.Fatal(err)
	}
	if speculativeMappings != 0 {
		t.Fatalf("barrier-created transcript mappings = %d, want zero", speculativeMappings)
	}
	var afterLastActive string
	if err := second.db.QueryRow(`SELECT last_active FROM sessions WHERE session_id = ?`, sessionID).Scan(&afterLastActive); err != nil {
		t.Fatal(err)
	}
	if afterLastActive != beforeLastActive {
		t.Fatalf("session activity changed under barrier: before=%q after=%q", beforeLastActive, afterLastActive)
	}
	if count := adversarialEffectRowCount(t, second, sessionID); count != 1 {
		t.Fatalf("effect permit count under barrier = %d, want one", count)
	}
	if requested, err := second.CancellationRequested(context.Background(), sessionID, commandA.CommandID); err != nil || !requested {
		t.Fatalf("cancellation intent under barrier = %v, %v", requested, err)
	}
	if accepted, err := second.Accept(context.Background(), sessionexec.AcceptRequest{
		SessionID: sessionID, CommandID: "effect-barrier-later", Type: "input",
		Content: "accepted while fenced", AcceptedBy: "alice",
	}); err != nil || accepted.State != sessionexec.StateAccepted {
		t.Fatalf("Accept while ambiguous = %+v, %v", accepted, err)
	}

	if err := second.EndEffect(context.Background(), permitA); err != nil {
		t.Fatalf("EndEffect clears session barrier: %v", err)
	}
	if _, err := first.Heartbeat(context.Background(), commandB.Lease, 10*time.Minute); err != nil {
		t.Fatalf("heartbeat after barrier clears: %v", err)
	}
	claimed, err := second.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: sessionID, Lane: sessionexec.LaneWork, Owner: "post-barrier", LeaseDuration: time.Minute,
	})
	if err != nil {
		t.Fatalf("claim after barrier clears: %v", err)
	}
	if claimed.CommandID != steer.CommandID {
		t.Fatalf("claimed command after barrier = %q, want %q", claimed.CommandID, steer.CommandID)
	}
}
