package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/sessionexec"
)

func TestSessionExecClaimNext_FloorsLeaseMetadataWithinRawExpiry(t *testing.T) {
	const sessionID = "session-claim-lease-clock"
	store := newSessionExecStore(t, sessionID)
	accepted := acceptInput(t, store, sessionID, "command-claim-lease-clock", "private future claim")
	dbBefore := sessionExecTestDBNowMillis(t, store)
	futureAccepted := dbBefore + int64(time.Hour/time.Millisecond)
	if _, err := store.db.Exec(`UPDATE session_commands SET accepted_at_ms = ?
		WHERE session_id = ? AND command_id = ?`, futureAccepted, sessionID, accepted.CommandID); err != nil {
		t.Fatal(err)
	}
	assertSessionExecCommandStatusReadable(t, store, sessionID, accepted.CommandID, "pre-claim")

	const leaseDuration = 2 * time.Hour
	command, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: sessionID, Lane: sessionexec.LaneWork, Owner: "worker-claim-clock", LeaseDuration: leaseDuration,
	})
	if err != nil {
		t.Fatalf("ClaimNext error = %v", err)
	}
	dbAfter := sessionExecTestDBNowMillis(t, store)
	timing := readSessionExecLeaseTiming(t, store, sessionID, accepted.CommandID)
	assertRawLeaseStartWithin(t, timing.leaseExpires, leaseDuration, dbBefore, dbAfter)
	if timing.started < futureAccepted || timing.heartbeat < timing.started || timing.leaseExpires <= timing.heartbeat {
		t.Fatalf("claim timing = %+v, want expiry > heartbeat >= start >= accepted %d", timing, futureAccepted)
	}
	if command.StartedAt == nil || command.StartedAt.UnixMilli() != timing.started {
		t.Fatalf("claimed command started_at = %v, want stored %d", command.StartedAt, timing.started)
	}
	assertSessionExecCommandStatusReadable(t, store, sessionID, accepted.CommandID, "post-claim")
}

func TestSessionExecClaimNext_ReclaimPreservesFirstStartAndUsesRawExpiry(t *testing.T) {
	const sessionID = "session-reclaim-lease-clock"
	store := newSessionExecStore(t, sessionID)
	acceptInput(t, store, sessionID, "command-reclaim-lease-clock", "private future reclaim")
	first := claimLane(t, store, sessionID, sessionexec.LaneWork, "worker-first-clock")
	_, futureStarted, futureExpiry := forceSessionExecCommandFutureTiming(t, store, first.SessionID, first.CommandID, true)
	if _, err := store.Release(context.Background(), first.Lease); err != nil {
		t.Fatal(err)
	}
	assertSessionExecCommandStatusReadable(t, store, sessionID, first.CommandID, "pre-reclaim")

	dbBefore := sessionExecTestDBNowMillis(t, store)
	second, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
		SessionID: sessionID, Lane: sessionexec.LaneWork, Owner: "worker-second-clock", LeaseDuration: 2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("reclaim error = %v", err)
	}
	dbAfter := sessionExecTestDBNowMillis(t, store)
	timing := readSessionExecLeaseTiming(t, store, sessionID, first.CommandID)
	assertRawLeaseStartWithin(t, timing.leaseExpires, 2*time.Hour, dbBefore, dbAfter)
	if second.Attempt != 2 || second.Lease.LeaseGeneration != 2 {
		t.Fatalf("reclaim command = %+v, want attempt and lease generation 2", second)
	}
	if second.StartedAt == nil || second.StartedAt.UnixMilli() != futureStarted || timing.started != futureStarted {
		t.Fatalf("reclaim started_at command=%v stored=%d, want preserved %d", second.StartedAt, timing.started, futureStarted)
	}
	if timing.heartbeat < futureStarted || timing.heartbeat < futureExpiry-int64(time.Minute/time.Millisecond) ||
		timing.leaseExpires <= timing.heartbeat {
		t.Fatalf("reclaim timing = %+v, want preserved first start and valid heartbeat floor", timing)
	}
	assertSessionExecCommandStatusReadable(t, store, sessionID, first.CommandID, "post-reclaim")
}

func TestSessionExecHeartbeat_FloorsMetadataAndRenewsActiveEffectsToRawExpiry(t *testing.T) {
	const sessionID = "session-heartbeat-lease-clock"
	store := newSessionExecStore(t, sessionID)
	acceptInput(t, store, sessionID, "command-heartbeat-lease-clock", "private future heartbeat")
	command := claimLane(t, store, sessionID, sessionexec.LaneWork, "worker-heartbeat-clock")
	permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "effect-heartbeat-clock", Kind: sessionexec.EffectKindTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	dbBeforeSetup := sessionExecTestDBNowMillis(t, store)
	futureAccepted := dbBeforeSetup + int64(time.Hour/time.Millisecond)
	futureStarted := futureAccepted + 5
	futureHeartbeat := futureStarted + 5
	currentExpiry := dbBeforeSetup + int64(time.Minute/time.Millisecond)
	currentExpiry += int64(time.Hour / time.Millisecond)
	forceSessionExecRunningLeaseTiming(t, store, command, futureAccepted, futureStarted, futureHeartbeat, currentExpiry)
	forceSessionExecEffectFutureTiming(t, store, permit, futureStarted, currentExpiry)
	assertSessionExecCommandStatusReadable(t, store, sessionID, command.CommandID, "pre-heartbeat")
	assertSessionExecSnapshotReadable(t, store, sessionID, "pre-heartbeat")

	dbBefore := sessionExecTestDBNowMillis(t, store)
	renewed, err := store.Heartbeat(context.Background(), command.Lease, 2*time.Hour)
	if err != nil {
		t.Fatalf("Heartbeat error = %v", err)
	}
	dbAfter := sessionExecTestDBNowMillis(t, store)
	timing := readSessionExecLeaseTiming(t, store, sessionID, command.CommandID)
	assertRawLeaseStartWithin(t, timing.leaseExpires, 2*time.Hour, dbBefore, dbAfter)
	if timing.heartbeat < futureHeartbeat || timing.heartbeat < timing.started || timing.leaseExpires <= timing.heartbeat {
		t.Fatalf("heartbeat timing = %+v, want expiry > heartbeat >= prior heartbeat/start", timing)
	}
	if renewed.ExpiresAt.UnixMilli() != timing.leaseExpires {
		t.Fatalf("renewed lease expiry = %d, want stored %d", renewed.ExpiresAt.UnixMilli(), timing.leaseExpires)
	}
	if permitExpiry := readSessionExecEffectExpiry(t, store, permit); permitExpiry != timing.leaseExpires {
		t.Fatalf("effect expiry = %d, want renewed command expiry %d", permitExpiry, timing.leaseExpires)
	}
	assertSessionExecCommandStatusReadable(t, store, sessionID, command.CommandID, "post-heartbeat")
}

func TestSessionExecHeartbeat_RejectsRenewalBeforeActiveEffectCreationAtomically(t *testing.T) {
	const sessionID = "session-heartbeat-effect-clock-reject"
	store := newSessionExecStore(t, sessionID)
	acceptInput(t, store, sessionID, "command-heartbeat-effect-clock-reject", "private future effect renewal")
	command := claimLane(t, store, sessionID, sessionexec.LaneWork, "worker-effect-clock")
	permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "effect-renewal-clock-reject", Kind: sessionexec.EffectKindTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	dbNow := sessionExecTestDBNowMillis(t, store)
	accepted := dbNow - int64(time.Hour/time.Millisecond) - 10
	started := dbNow - int64(time.Hour/time.Millisecond)
	heartbeat := started
	futureCreated := dbNow + int64(time.Hour/time.Millisecond)
	currentExpiry := dbNow + int64(2*time.Hour/time.Millisecond)
	forceSessionExecRunningLeaseTiming(t, store, command, accepted, started, heartbeat, currentExpiry)
	forceSessionExecEffectFutureTiming(t, store, permit, futureCreated, currentExpiry)
	assertSessionExecCommandStatusReadable(t, store, sessionID, command.CommandID, "pre-heartbeat-effect-reject")
	assertSessionExecSnapshotReadable(t, store, sessionID, "pre-heartbeat-effect-reject")
	before := readSessionExecLeaseTiming(t, store, sessionID, command.CommandID)
	permitExpiryBefore := readSessionExecEffectExpiry(t, store, permit)
	permitCreatedBefore := readSessionExecEffectCreated(t, store, permit)

	_, err = store.Heartbeat(context.Background(), command.Lease, 30*time.Minute)
	if !errors.Is(err, sessionexec.ErrLeaseClockSkew) || !strings.Contains(err.Error(), "effect permit renewal expiry precedes creation") {
		t.Fatalf("Heartbeat error = %v, want %v", err, sessionexec.ErrLeaseClockSkew)
	}
	after := readSessionExecLeaseTiming(t, store, sessionID, command.CommandID)
	if after != before {
		t.Fatalf("heartbeat effect rejection timing changed: before=%+v after=%+v", before, after)
	}
	if permitExpiryAfter := readSessionExecEffectExpiry(t, store, permit); permitExpiryAfter != permitExpiryBefore {
		t.Fatalf("heartbeat effect rejection permit expiry = %d, want unchanged %d", permitExpiryAfter, permitExpiryBefore)
	}
	if permitCreatedAfter := readSessionExecEffectCreated(t, store, permit); permitCreatedAfter != permitCreatedBefore {
		t.Fatalf("heartbeat effect rejection permit created = %d, want unchanged %d", permitCreatedAfter, permitCreatedBefore)
	}
	assertSessionExecCommandStatusReadable(t, store, sessionID, command.CommandID, "post-heartbeat-effect-reject")
	assertSessionExecSnapshotReadable(t, store, sessionID, "post-heartbeat-effect-reject")
}

func TestSessionExecValidateHeartbeatEffects_AllowsProposedExpiryAtActiveEffectCreation(t *testing.T) {
	const sessionID = "session-heartbeat-effect-clock-equality"
	store := newSessionExecStore(t, sessionID)
	acceptInput(t, store, sessionID, "command-heartbeat-effect-clock-equality", "private equal effect renewal")
	command := claimLane(t, store, sessionID, sessionexec.LaneWork, "worker-effect-clock")
	permit, err := store.BeginEffect(context.Background(), sessionexec.EffectRequest{
		Lease: command.Lease, EffectID: "effect-renewal-clock-equality", Kind: sessionexec.EffectKindTool,
	})
	if err != nil {
		t.Fatal(err)
	}
	dbNow := sessionExecTestDBNowMillis(t, store)
	futureCreated := dbNow + int64(time.Hour/time.Millisecond)
	currentExpiry := dbNow + int64(2*time.Hour/time.Millisecond)
	forceSessionExecRunningLeaseTiming(t, store, command, dbNow-100, dbNow-50, dbNow-50, currentExpiry)
	forceSessionExecEffectFutureTiming(t, store, permit, futureCreated, currentExpiry)
	assertSessionExecSnapshotReadable(t, store, sessionID, "pre-heartbeat-effect-equality")

	err = store.withSessionExecWrite(context.Background(), func(db *sessionExecConn) error {
		active, err := sessionExecValidateHeartbeatEffects(db, command.Lease, currentExpiry, futureCreated)
		if err != nil {
			return err
		}
		if active != 1 {
			t.Fatalf("active heartbeat effects = %d, want 1", active)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("validate heartbeat effects equality error = %v", err)
	}
}

func TestSessionExecLeaseMetadataFloorRejectsAtomicallyWhenItReachesRawExpiry(t *testing.T) {
	t.Run("claim", func(t *testing.T) {
		const sessionID = "session-claim-floor-reject"
		store := newSessionExecStore(t, sessionID)
		accepted := acceptInput(t, store, sessionID, "command-claim-floor-reject", "private reject claim")
		dbNow := sessionExecTestDBNowMillis(t, store)
		futureAccepted := dbNow + int64(time.Hour/time.Millisecond)
		if _, err := store.db.Exec(`UPDATE session_commands SET accepted_at_ms = ?
			WHERE session_id = ? AND command_id = ?`, futureAccepted, sessionID, accepted.CommandID); err != nil {
			t.Fatal(err)
		}
		assertSessionExecCommandStatusReadable(t, store, sessionID, accepted.CommandID, "pre-claim-reject")

		_, err := store.ClaimNext(context.Background(), sessionexec.ClaimRequest{
			SessionID: sessionID, Lane: sessionexec.LaneWork, Owner: "worker-reject-clock", LeaseDuration: time.Second,
		})
		if !errors.Is(err, sessionexec.ErrLeaseClockSkew) || !strings.Contains(err.Error(), "lease metadata clock floor reaches expiry") {
			t.Fatalf("ClaimNext error = %v, want %v", err, sessionexec.ErrLeaseClockSkew)
		}
		timing := readSessionExecLeaseTiming(t, store, sessionID, accepted.CommandID)
		if timing.state != sessionexec.StateAccepted || timing.attempt != 0 || timing.leaseGeneration != 0 ||
			timing.startedValid || timing.heartbeatValid || timing.leaseExpiresValid {
			t.Fatalf("claim rejection timing = %+v, want unchanged accepted row", timing)
		}
		if messages, mappings := countSessionExecTranscriptRows(t, store, sessionID); messages != 0 || mappings != 0 {
			t.Fatalf("claim rejection transcript rows: messages=%d mappings=%d, want 0,0", messages, mappings)
		}
	})

	t.Run("heartbeat", func(t *testing.T) {
		const sessionID = "session-heartbeat-floor-reject"
		store := newSessionExecStore(t, sessionID)
		acceptInput(t, store, sessionID, "command-heartbeat-floor-reject", "private reject heartbeat")
		command := claimLane(t, store, sessionID, sessionexec.LaneWork, "worker-reject-clock")
		forceSessionExecCommandFutureTiming(t, store, command.SessionID, command.CommandID, true)
		assertSessionExecCommandStatusReadable(t, store, sessionID, command.CommandID, "pre-heartbeat-reject")
		assertSessionExecSnapshotReadable(t, store, sessionID, "pre-heartbeat-reject")
		before := readSessionExecLeaseTiming(t, store, sessionID, command.CommandID)

		_, err := store.Heartbeat(context.Background(), command.Lease, time.Second)
		if !errors.Is(err, sessionexec.ErrLeaseClockSkew) || !strings.Contains(err.Error(), "lease metadata clock floor reaches expiry") {
			t.Fatalf("Heartbeat error = %v, want %v", err, sessionexec.ErrLeaseClockSkew)
		}
		after := readSessionExecLeaseTiming(t, store, sessionID, command.CommandID)
		if after != before {
			t.Fatalf("heartbeat rejection timing changed: before=%+v after=%+v", before, after)
		}
	})
}

func TestSessionExecHeartbeat_StaleAndExpiredRefsAreNotBypassedByMetadataFloor(t *testing.T) {
	const sessionID = "session-heartbeat-floor-fences"
	store := newSessionExecStore(t, sessionID)
	acceptInput(t, store, sessionID, "command-heartbeat-floor-fences", "private stale heartbeat")
	command := claimLane(t, store, sessionID, sessionexec.LaneWork, "worker-fence-clock")
	_, futureStarted, futureExpiry := forceSessionExecCommandFutureTiming(t, store, command.SessionID, command.CommandID, true)

	stale := command.Lease
	stale.Owner = "worker-stale-clock"
	if _, err := store.Heartbeat(context.Background(), stale, 2*time.Hour); !errors.Is(err, sessionexec.ErrLeaseStale) {
		t.Fatalf("stale heartbeat error = %v, want %v", err, sessionexec.ErrLeaseStale)
	}
	timing := readSessionExecLeaseTiming(t, store, sessionID, command.CommandID)
	if timing.started != futureStarted || timing.leaseExpires != futureExpiry {
		t.Fatalf("stale heartbeat timing = %+v, want unchanged future timing", timing)
	}

	if _, err := store.db.Exec(`UPDATE session_commands SET lease_expires_at_ms = 0
		WHERE session_id = ? AND command_id = ?`, sessionID, command.CommandID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Heartbeat(context.Background(), command.Lease, 2*time.Hour); !errors.Is(err, sessionexec.ErrLeaseExpired) {
		t.Fatalf("expired heartbeat error = %v, want %v", err, sessionexec.ErrLeaseExpired)
	}
}

func TestSessionExecCommandLeaseMetadataMillis_BoundsRawExpiry(t *testing.T) {
	started, heartbeat, err := sessionExecCommandLeaseMetadataMillis(1000, 2000, sqlNullInt64(0, false), sqlNullInt64(0, false), 3000)
	if err != nil {
		t.Fatalf("metadata floor error = %v", err)
	}
	if started != 2000 || heartbeat != 2000 {
		t.Fatalf("metadata floor started=%d heartbeat=%d, want 2000,2000", started, heartbeat)
	}
	started, heartbeat, err = sessionExecCommandLeaseMetadataMillis(1000, 1500, sqlNullInt64(2100, true), sqlNullInt64(2500, true), 2600)
	if err != nil {
		t.Fatalf("metadata prior heartbeat floor error = %v", err)
	}
	if started != 2100 || heartbeat != 2500 {
		t.Fatalf("metadata prior heartbeat floor started=%d heartbeat=%d, want 2100,2500", started, heartbeat)
	}
	if _, _, err := sessionExecCommandLeaseMetadataMillis(1000, 1500, sqlNullInt64(2100, true), sqlNullInt64(2600, true), 2600); !errors.Is(err, sessionexec.ErrLeaseClockSkew) {
		t.Fatalf("metadata equality error = %v, want %v", err, sessionexec.ErrLeaseClockSkew)
	}
	if _, _, err := sessionExecCommandLeaseMetadataMillis(1000, 3000, sqlNullInt64(0, false), sqlNullInt64(0, false), 3000); !errors.Is(err, sessionexec.ErrLeaseClockSkew) {
		t.Fatalf("accepted equality error = %v, want %v", err, sessionexec.ErrLeaseClockSkew)
	}
}

type sessionExecLeaseTiming struct {
	state             sessionexec.State
	attempt           int
	leaseGeneration   int64
	started           int64
	heartbeat         int64
	leaseExpires      int64
	startedValid      bool
	heartbeatValid    bool
	leaseExpiresValid bool
}

func readSessionExecLeaseTiming(t *testing.T, store *Store, sessionID, commandID string) sessionExecLeaseTiming {
	t.Helper()
	var timing sessionExecLeaseTiming
	if err := store.db.QueryRow(`SELECT state, attempt, lease_generation,
		COALESCE(started_at_ms, 0), started_at_ms IS NOT NULL,
		COALESCE(heartbeat_at_ms, 0), heartbeat_at_ms IS NOT NULL,
		COALESCE(lease_expires_at_ms, 0), lease_expires_at_ms IS NOT NULL
		FROM session_commands WHERE session_id = ? AND command_id = ?`,
		sessionID, commandID).Scan(&timing.state, &timing.attempt, &timing.leaseGeneration,
		&timing.started, &timing.startedValid, &timing.heartbeat, &timing.heartbeatValid,
		&timing.leaseExpires, &timing.leaseExpiresValid); err != nil {
		t.Fatal(err)
	}
	return timing
}

func assertSessionExecSnapshotReadable(t *testing.T, store *Store, sessionID, phase string) {
	t.Helper()
	if _, err := store.GetExecutionSnapshot(context.Background(), sessionID, 10); err != nil {
		t.Fatalf("%s execution snapshot: %v", phase, err)
	}
}

func forceSessionExecRunningLeaseTiming(t *testing.T, store *Store, command sessionexec.Command, acceptedAt, startedAt, heartbeatAt, expiresAt int64) {
	t.Helper()
	if acceptedAt > startedAt || startedAt > heartbeatAt || heartbeatAt >= expiresAt {
		t.Fatalf("invalid forced lease timing: accepted=%d started=%d heartbeat=%d expires=%d", acceptedAt, startedAt, heartbeatAt, expiresAt)
	}
	if _, err := store.db.Exec(`UPDATE session_commands SET
		accepted_at_ms = ?, started_at_ms = ?, heartbeat_at_ms = ?, lease_expires_at_ms = ?
		WHERE session_id = ? AND command_id = ?`,
		acceptedAt, startedAt, heartbeatAt, expiresAt, command.SessionID, command.CommandID); err != nil {
		t.Fatal(err)
	}
}

func readSessionExecEffectExpiry(t *testing.T, store *Store, permit sessionexec.EffectPermit) int64 {
	t.Helper()
	var expiresAt int64
	if err := store.db.QueryRow(`SELECT expires_at_ms FROM session_effect_permits
		WHERE session_id = ? AND command_id = ? AND generation = ? AND effect_id = ?`,
		permit.Lease.SessionID, permit.Lease.CommandID, permit.Lease.Generation, permit.EffectID).Scan(&expiresAt); err != nil {
		t.Fatal(err)
	}
	return expiresAt
}

func readSessionExecEffectCreated(t *testing.T, store *Store, permit sessionexec.EffectPermit) int64 {
	t.Helper()
	var createdAt int64
	if err := store.db.QueryRow(`SELECT created_at_ms FROM session_effect_permits
		WHERE session_id = ? AND command_id = ? AND generation = ? AND effect_id = ?`,
		permit.Lease.SessionID, permit.Lease.CommandID, permit.Lease.Generation, permit.EffectID).Scan(&createdAt); err != nil {
		t.Fatal(err)
	}
	return createdAt
}

func countSessionExecTranscriptRows(t *testing.T, store *Store, sessionID string) (int, int) {
	t.Helper()
	var messages int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE session_id = ?`, sessionID).Scan(&messages); err != nil {
		t.Fatal(err)
	}
	var mappings int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM session_command_transcript WHERE session_id = ?`, sessionID).Scan(&mappings); err != nil {
		t.Fatal(err)
	}
	return messages, mappings
}

func assertRawLeaseStartWithin(t *testing.T, expiresAt int64, duration time.Duration, before, after int64) {
	t.Helper()
	rawStart := expiresAt - duration.Milliseconds()
	const toleranceMillis = int64(5 * time.Second / time.Millisecond)
	if rawStart < before-toleranceMillis || rawStart > after+toleranceMillis {
		t.Fatalf("raw lease start = %d from expiry %d, want near %d..%d", rawStart, expiresAt, before, after)
	}
}

func sqlNullInt64(value int64, valid bool) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: valid}
}
