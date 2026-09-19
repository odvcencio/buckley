package storage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/sessionexec"
)

func TestSessionExecEffectPermit_TransitionTimestampsRespectStoredCreation(t *testing.T) {
	t.Run("end", func(t *testing.T) {
		store, _ := openAdversarialEffectStore(t, "effect-monotonic-end")
		command := claimAdversarialEffectCommand(t, store, "effect-monotonic-end", "effect-monotonic-end-command", "monotonic-owner", 10*time.Minute)
		permit := beginAdversarialEffect(t, store, command, "effect-monotonic-end-step")
		futureCreatedAt := permit.ExpiresAt.Add(-time.Minute).UnixMilli()
		forceEffectCreatedAt(t, store, permit, futureCreatedAt)

		if err := store.EndEffect(context.Background(), permit); err != nil {
			t.Fatalf("EndEffect with backward database clock: %v", err)
		}
		if endedAt := readEffectEndedAt(t, store, permit); !endedAt.Valid || endedAt.Int64 < futureCreatedAt {
			t.Fatalf("ended_at_ms = %v, want >= created_at_ms %d", endedAt, futureCreatedAt)
		}
		assertEffectPermitsScan(t, store, command.SessionID)
	})

	t.Run("duplicate ambiguity", func(t *testing.T) {
		store, _ := openAdversarialEffectStore(t, "effect-monotonic-ambiguous")
		command := claimAdversarialEffectCommand(t, store, "effect-monotonic-ambiguous", "effect-monotonic-ambiguous-command", "monotonic-owner", 10*time.Minute)
		permit := beginAdversarialEffect(t, store, command, "effect-monotonic-ambiguous-step")
		futureCreatedAt := permit.ExpiresAt.Add(-time.Minute).UnixMilli()
		forceEffectCreatedAt(t, store, permit, futureCreatedAt)

		duplicate, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
			Lease: command.Lease, EffectID: permit.EffectID, Kind: permit.Kind,
		})
		if !errors.Is(err, sessionexec.ErrEffectAmbiguous) || errors.Is(err, sessionexec.ErrEffectPermitConflict) {
			t.Fatalf("duplicate BeginEffect error = %v, want ambiguity without permit conflict", err)
		}
		if duplicate.AmbiguousAt == nil || duplicate.AmbiguousAt.UnixMilli() < futureCreatedAt {
			t.Fatalf("duplicate ambiguous_at = %v, want >= created_at_ms %d", duplicate.AmbiguousAt, futureCreatedAt)
		}
		if ambiguousAt := readEffectAmbiguousAt(t, store, permit); !ambiguousAt.Valid || ambiguousAt.Int64 < futureCreatedAt {
			t.Fatalf("stored ambiguous_at_ms = %v, want >= created_at_ms %d", ambiguousAt, futureCreatedAt)
		}
		assertEffectPermitsScan(t, store, command.SessionID)
	})
}

func forceEffectCreatedAt(t *testing.T, store *Store, permit sessionexec.EffectPermit, createdAt int64) {
	t.Helper()
	if createdAt >= permit.ExpiresAt.UnixMilli() {
		t.Fatalf("forced created_at_ms %d must stay before expiry %d", createdAt, permit.ExpiresAt.UnixMilli())
	}
	if _, err := store.db.Exec(`UPDATE session_effect_permits SET created_at_ms = ?
		WHERE session_id = ? AND command_id = ? AND generation = ? AND effect_id = ?`,
		createdAt, permit.Lease.SessionID, permit.Lease.CommandID, permit.Lease.Generation, permit.EffectID); err != nil {
		t.Fatal(err)
	}
}

func readEffectEndedAt(t *testing.T, store *Store, permit sessionexec.EffectPermit) sql.NullInt64 {
	t.Helper()
	var endedAt sql.NullInt64
	if err := store.db.QueryRow(`SELECT ended_at_ms FROM session_effect_permits
		WHERE session_id = ? AND command_id = ? AND generation = ? AND effect_id = ?`,
		permit.Lease.SessionID, permit.Lease.CommandID, permit.Lease.Generation, permit.EffectID).Scan(&endedAt); err != nil {
		t.Fatal(err)
	}
	return endedAt
}

func readEffectAmbiguousAt(t *testing.T, store *Store, permit sessionexec.EffectPermit) sql.NullInt64 {
	t.Helper()
	var ambiguousAt sql.NullInt64
	if err := store.db.QueryRow(`SELECT ambiguous_at_ms FROM session_effect_permits
		WHERE session_id = ? AND command_id = ? AND generation = ? AND effect_id = ?`,
		permit.Lease.SessionID, permit.Lease.CommandID, permit.Lease.Generation, permit.EffectID).Scan(&ambiguousAt); err != nil {
		t.Fatal(err)
	}
	return ambiguousAt
}

func assertEffectPermitsScan(t *testing.T, store *Store, sessionID string) {
	t.Helper()
	if err := store.withSessionExecWrite(context.Background(), func(db *sessionExecConn) error {
		_, err := sessionExecListEffectPermits(db, sessionID)
		return err
	}); err != nil {
		t.Fatalf("scan effect permits after transition: %v", err)
	}
}
