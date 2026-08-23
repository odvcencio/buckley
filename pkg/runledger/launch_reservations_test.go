package runledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"m31labs.dev/buckley/pkg/launchcontract"
)

type launchReservationFixture struct {
	store   *SQLiteStore
	runID   string
	session string
	taskID  string
	turnID  string
	account launchBudgetAccount
	window  launchTurnWindow
}

func TestLaunchReservationsV22_MigrationImmutabilityAndRunCascade(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "launch-reservations.db")
	store, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	fixture := prepareLaunchReservationFixture(t, store, "session-migration", "run-migration", "gsxmail")
	request := fixture.request(t, "step-migration", 1, 4)
	admission, err := store.reserveLaunchModelStep(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}

	var version, tables, triggers int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM runledger_schema_migrations WHERE version = 22`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("v22 migration count=%d err=%v", version, err)
	}
	if err := store.db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_schema
		WHERE type='table' AND name IN ('launch_budget_accounts','launch_turn_windows','launch_model_reservations')
	`).Scan(&tables); err != nil || tables != 3 {
		t.Fatalf("v22 table count=%d err=%v", tables, err)
	}
	if err := store.db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_schema
		WHERE type='trigger' AND name IN (
			'trg_launch_budget_accounts_delete_immutable',
			'trg_launch_turn_windows_delete_immutable',
			'trg_launch_model_reservations_delete_immutable',
			'trg_launch_model_reservations_same_state_immutable'
		)
	`).Scan(&triggers); err != nil || triggers != 4 {
		t.Fatalf("v22 trigger count=%d err=%v", triggers, err)
	}

	if _, err := store.db.Exec(`DELETE FROM launch_model_reservations WHERE run_id=? AND step_id=?`, fixture.runID, request.StepID); err == nil {
		t.Fatal("direct reservation deletion succeeded")
	}
	if _, err := store.db.Exec(`DELETE FROM launch_turn_windows WHERE run_id=? AND turn_id=?`, fixture.runID, fixture.turnID); err == nil {
		t.Fatal("direct turn deletion succeeded")
	}
	if _, err := store.db.Exec(`DELETE FROM launch_budget_accounts WHERE run_id=?`, fixture.runID); err == nil {
		t.Fatal("direct account deletion succeeded")
	}
	if _, err := store.db.Exec(`UPDATE launch_model_reservations SET lease_expires_at=lease_expires_at WHERE run_id=? AND step_id=?`, fixture.runID, request.StepID); err == nil {
		t.Fatal("same-state reservation mutation succeeded")
	}
	if _, err := store.db.Exec(`DELETE FROM agent_runs WHERE run_id=? AND session_id=?`, fixture.runID, fixture.session); err != nil {
		t.Fatalf("intentional run cascade: %v", err)
	}
	for _, table := range []string{"launch_budget_accounts", "launch_turn_windows", "launch_model_reservations"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE run_id=?`, fixture.runID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s cascade count=%d err=%v", table, count, err)
		}
	}
	if admission.Claim.RunID == "" {
		t.Fatal("reservation admission was unexpectedly empty")
	}

	if _, err := store.db.Exec(`DELETE FROM runledger_schema_migrations WHERE version=22`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM runledger_schema_migrations WHERE version=22`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("replayed v22 count=%d err=%v", version, err)
	}
}

func TestLaunchBudgetActivation_FreshOnceStableReplayAndStaleFirstActivation(t *testing.T) {
	t.Run("fresh activation establishes database-time run boundary", func(t *testing.T) {
		store, err := New(filepath.Join(t.TempDir(), "launch.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		ensureLaunchTestRun(t, store, "session-fresh", "run-fresh")
		observed := time.Now().UTC().Round(0).Add(-100 * time.Millisecond)
		envelope := launchTestEnvelope(t, "session-fresh", "run-fresh", "gsxmail", launchTestPrice(t, observed, 2*time.Second), "")
		if _, _, err := ensureLaunchTestEnvelope(context.Background(), store, envelope); err != nil {
			t.Fatal(err)
		}
		before := time.Now().UTC().Add(-time.Second)
		account, replay, err := store.activateLaunchBudget(context.Background(), envelope.RunID)
		if err != nil || replay {
			t.Fatalf("activation replay=%v err=%v", replay, err)
		}
		after := time.Now().UTC().Add(time.Second)
		if account.StartedAt.Before(before) || account.StartedAt.After(after) {
			t.Fatalf("database start %v outside [%v,%v]", account.StartedAt, before, after)
		}
		runTimeout, err := launchDurationFromMS(account.AbsoluteRunTimeoutMS)
		if err != nil || !account.RunDeadlineAt.Equal(account.StartedAt.Add(runTimeout)) {
			t.Fatalf("run deadline=%v start=%v timeout=%v err=%v", account.RunDeadlineAt, account.StartedAt, runTimeout, err)
		}
		first := account
		time.Sleep(2100 * time.Millisecond)
		account, replay, err = store.activateLaunchBudget(context.Background(), envelope.RunID)
		if err != nil || !replay || !reflect.DeepEqual(account, first) {
			t.Fatalf("expired-evidence exact replay=%v account=%+v err=%v", replay, account, err)
		}
		if _, err := store.db.Exec(`DROP TRIGGER trg_launch_budget_accounts_immutable`); err != nil {
			t.Fatal(err)
		}
		invalidStart := envelope.PriceEvidence.ExpiresAt.Add(time.Second)
		if _, err := store.db.Exec(`
			UPDATE launch_budget_accounts SET started_at=?, run_deadline_at=? WHERE run_id=?
		`, sqliteTimestamp(invalidStart), sqliteTimestamp(invalidStart.Add(runTimeout)), envelope.RunID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.activateLaunchBudget(context.Background(), envelope.RunID); !errors.Is(err, errLaunchReservationIntegrity) {
			t.Fatalf("post-expiry activation projection error=%v", err)
		}
	})

	t.Run("stale first activation writes no account", func(t *testing.T) {
		store, err := New(filepath.Join(t.TempDir(), "launch.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		const sessionID, runID = "session-stale", "run-stale"
		ensureLaunchTestRun(t, store, sessionID, runID)
		observed := time.Now().UTC().Round(0).Add(-2 * time.Minute)
		envelope := launchTestEnvelope(t, sessionID, runID, "gsxmail", launchTestPrice(t, observed, time.Minute), "")
		record, err := launchRecordAt(envelope, observed)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := record.CanonicalBytes()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`
			INSERT INTO launch_envelopes (
				run_id, session_id, schema_version, profile_id, profile_version,
				profile_digest, envelope_digest, envelope_json, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, runID, sessionID, envelope.Schema, envelope.Profile.ID, envelope.Profile.Schema,
			envelope.ProfileDigest, envelope.EnvelopeDigest, string(encoded), sqliteTimestamp(record.CreatedAt())); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.activateLaunchBudget(context.Background(), runID); !errors.Is(err, errLaunchReservationInvalid) {
			t.Fatalf("stale activation error=%v", err)
		}
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM launch_budget_accounts WHERE run_id=?`, runID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("stale activation rows=%d err=%v", count, err)
		}
	})
}

func TestLaunchTurnWindow_StableDatabaseTimeAndExactDeadline(t *testing.T) {
	fixture := newLaunchReservationFixture(t, "turn-window", "gsxmail")
	turnTimeout, err := launchDurationFromMS(fixture.account.TurnTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	wantDeadline := minLaunchTime(fixture.window.StartedAt.Add(turnTimeout), fixture.account.RunDeadlineAt)
	if !fixture.window.DeadlineAt.Equal(wantDeadline) {
		t.Fatalf("turn deadline=%v want=%v", fixture.window.DeadlineAt, wantDeadline)
	}
	replayed, replay, err := fixture.store.beginLaunchTurn(context.Background(), fixture.runID, fixture.taskID, fixture.turnID)
	if err != nil || !replay || !reflect.DeepEqual(replayed, fixture.window) {
		t.Fatalf("turn replay=%v window=%+v err=%v", replay, replayed, err)
	}
	if _, _, err := fixture.store.beginLaunchTurn(context.Background(), fixture.runID, fixture.taskID+"-drift", fixture.turnID); !errors.Is(err, errLaunchReservationConflict) {
		t.Fatalf("turn identity drift error=%v", err)
	}
	fixture.store.launchReservationNow = func(context.Context, launchEnvelopeQueryer) (time.Time, error) {
		return fixture.account.RunDeadlineAt, nil
	}
	if _, _, err := fixture.store.beginLaunchTurn(context.Background(), fixture.runID, fixture.taskID, fixture.turnID+"-new"); !errors.Is(err, errLaunchDeadline) {
		t.Fatalf("deadline equality error=%v", err)
	}
}

func TestLaunchReservation_RunEndFencesFreshAuthorityAndAllowsDurableCleanup(t *testing.T) {
	t.Run("first activation", func(t *testing.T) {
		store, err := New(filepath.Join(t.TempDir(), "launch.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		const sessionID, runID = "session-ended-activation", "run-ended-activation"
		ensureLaunchTestRun(t, store, sessionID, runID)
		envelope := launchTestEnvelope(t, sessionID, runID, "gsxmail",
			launchTestPrice(t, time.Now().UTC().Round(0).Add(-time.Second), 5*time.Minute), "")
		if _, _, err := ensureLaunchTestEnvelope(context.Background(), store, envelope); err != nil {
			t.Fatal(err)
		}
		endLaunchReservationRun(t, store, runID, sessionID)
		if account, replay, err := store.activateLaunchBudget(context.Background(), runID); !errors.Is(err, errLaunchRunEnded) || replay || account != (launchBudgetAccount{}) {
			t.Fatalf("ended first activation account=%+v replay=%v err=%v", account, replay, err)
		}
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM launch_budget_accounts WHERE run_id=?`, runID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("ended first activation rows=%d err=%v", count, err)
		}
	})

	t.Run("replay release and new authority", func(t *testing.T) {
		fixture := newLaunchReservationFixture(t, "ended-authority", "gsxmail")
		ctx := context.Background()
		completedRequest := fixture.request(t, "step-ended-completed", 0, 1)
		completed, err := fixture.store.reserveLaunchModelStep(ctx, completedRequest)
		if err != nil {
			t.Fatal(err)
		}
		completedPermit, err := fixture.store.dispatchLaunchModelStep(ctx, completed.Claim, "request-ended-completed", completedRequest.WireRequestDigest)
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.settleLaunchModelStep(ctx, completedPermit.Claim, launchReservationSettlement{
			ResponseEvidenceID: "response-ended-completed", OutputDigest: launchTestDigest(t, "ended-completed"),
		}); err != nil {
			t.Fatal(err)
		}
		heldRequest := fixture.request(t, "step-ended-held", 0, 1)
		held, err := fixture.store.reserveLaunchModelStep(ctx, heldRequest)
		if err != nil {
			t.Fatal(err)
		}

		endLaunchReservationRun(t, fixture.store, fixture.runID, fixture.session)
		if account, replay, err := fixture.store.activateLaunchBudget(ctx, fixture.runID); err != nil || !replay || !reflect.DeepEqual(account, fixture.account) {
			t.Fatalf("ended account replay=%v account=%+v err=%v", replay, account, err)
		}
		if window, replay, err := fixture.store.beginLaunchTurn(ctx, fixture.runID, fixture.taskID, fixture.turnID); err != nil || !replay || !reflect.DeepEqual(window, fixture.window) {
			t.Fatalf("ended turn replay=%v window=%+v err=%v", replay, window, err)
		}
		if _, _, err := fixture.store.beginLaunchTurn(ctx, fixture.runID, fixture.taskID, fixture.turnID+"-new"); !errors.Is(err, errLaunchRunEnded) {
			t.Fatalf("ended new turn error=%v", err)
		}
		if admission, err := fixture.store.reserveLaunchModelStep(ctx, fixture.request(t, "step-ended-new", 0, 1)); !errors.Is(err, errLaunchRunEnded) || admission != (launchReservationAdmission{}) {
			t.Fatalf("ended new reservation=%+v err=%v", admission, err)
		}
		if permit, err := fixture.store.dispatchLaunchModelStep(ctx, held.Claim, "request-ended-held", heldRequest.WireRequestDigest); !errors.Is(err, errLaunchRunEnded) || permit != (launchDispatchPermit{}) {
			t.Fatalf("ended dispatch permit=%+v err=%v", permit, err)
		}
		if err := fixture.store.releaseLaunchModelStep(ctx, held.Claim, launchReasonReleased); err != nil {
			t.Fatalf("ended release: %v", err)
		}
		if err := fixture.store.releaseLaunchModelStep(ctx, held.Claim, launchReasonReleased); err != nil {
			t.Fatalf("ended release replay: %v", err)
		}
		if admission, err := fixture.store.reserveLaunchModelStep(ctx, heldRequest); !errors.Is(err, errLaunchRunEnded) || admission != (launchReservationAdmission{}) {
			t.Fatalf("ended released retry=%+v err=%v", admission, err)
		}
		if replay, err := fixture.store.reserveLaunchModelStep(ctx, completedRequest); err != nil || replay.Disposition != launchAdmissionReplayCompleted {
			t.Fatalf("ended completed replay=%+v err=%v", replay, err)
		}
	})

	for _, operation := range []string{"settle", "block"} {
		operation := operation
		t.Run(operation+" cleanup", func(t *testing.T) {
			fixture := newLaunchReservationFixture(t, "ended-cleanup-"+operation, "gsxmail")
			request := fixture.request(t, "step-ended-cleanup-"+operation, 0, 1)
			admission, err := fixture.store.reserveLaunchModelStep(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			permit, err := fixture.store.dispatchLaunchModelStep(context.Background(), admission.Claim, "request-ended-cleanup-"+operation, request.WireRequestDigest)
			if err != nil {
				t.Fatal(err)
			}
			endLaunchReservationRun(t, fixture.store, fixture.runID, fixture.session)
			switch operation {
			case "settle":
				settlement := launchReservationSettlement{ResponseEvidenceID: "response-ended-settle", OutputDigest: launchTestDigest(t, "ended-settle")}
				if err := fixture.store.settleLaunchModelStep(context.Background(), permit.Claim, settlement); err != nil {
					t.Fatal(err)
				}
				if err := fixture.store.settleLaunchModelStep(context.Background(), permit.Claim, settlement); err != nil {
					t.Fatalf("ended settle replay: %v", err)
				}
			case "block":
				digest := launchTestDigest(t, "ended-block")
				if err := fixture.store.blockLaunchModelStep(context.Background(), permit.Claim, launchReasonAmbiguous, "response-ended-block", digest); err != nil {
					t.Fatal(err)
				}
				if err := fixture.store.blockLaunchModelStep(context.Background(), permit.Claim, launchReasonAmbiguous, "response-ended-block", digest); err != nil {
					t.Fatalf("ended block replay: %v", err)
				}
			}
		})
	}
}

func TestLaunchModelReservation_LifecycleReplayRefundAndAmbiguity(t *testing.T) {
	fixture := newLaunchReservationFixture(t, "lifecycle", "gsxmail")
	ctx := context.Background()

	request := fixture.request(t, "step-complete", 4, 8)
	first, err := fixture.store.reserveLaunchModelStep(ctx, request)
	if err != nil || first.Disposition != launchAdmissionReserved {
		t.Fatalf("first reservation=%+v err=%v", first, err)
	}
	duplicate, err := fixture.store.reserveLaunchModelStep(ctx, request)
	if err != nil || duplicate.Disposition != launchAdmissionAlreadyReserved || !reflect.DeepEqual(duplicate.Claim, first.Claim) {
		t.Fatalf("duplicate reservation=%+v err=%v", duplicate, err)
	}
	permit, err := fixture.store.dispatchLaunchModelStep(ctx, first.Claim, "request-step-complete", request.WireRequestDigest)
	if err != nil || permit.Claim.LeaseExpiresAt.Equal(first.Claim.LeaseExpiresAt) || !permit.RequestDeadlineAt.Equal(permit.Claim.LeaseExpiresAt) {
		t.Fatalf("dispatch permit=%+v err=%v", permit, err)
	}
	if secondPermit, err := fixture.store.dispatchLaunchModelStep(ctx, duplicate.Claim, "request-step-complete", request.WireRequestDigest); !errors.Is(err, errLaunchDispatchAlreadyDurable) || !reflect.DeepEqual(secondPermit, launchDispatchPermit{}) {
		t.Fatalf("duplicate dispatch permit=%+v err=%v", secondPermit, err)
	}
	settlement := launchReservationSettlement{
		InputTokens: 1, OutputTokens: 2, TotalTokens: 0,
		ResponseEvidenceID: "response-step-complete", OutputDigest: launchTestDigest(t, "output-step-complete"),
	}
	if err := fixture.store.settleLaunchModelStep(ctx, permit.Claim, settlement); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.settleLaunchModelStep(ctx, permit.Claim, settlement); err != nil {
		t.Fatalf("settlement replay: %v", err)
	}
	replay, err := fixture.store.reserveLaunchModelStep(ctx, request)
	if err != nil || replay.Disposition != launchAdmissionReplayCompleted || replay.Step.Status != StepCompleted {
		t.Fatalf("completed replay=%+v err=%v", replay, err)
	}

	releaseRequest := fixture.request(t, "step-release", 100, 200)
	released, err := fixture.store.reserveLaunchModelStep(ctx, releaseRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.releaseLaunchModelStep(ctx, released.Claim, launchReasonReleased); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.releaseLaunchModelStep(ctx, released.Claim, launchReasonReleased); err != nil {
		t.Fatalf("release replay: %v", err)
	}
	retry, err := fixture.store.reserveLaunchModelStep(ctx, releaseRequest)
	if err != nil || retry.Claim.Attempt != released.Claim.Attempt+1 || retry.Claim.ClaimGeneration != released.Claim.ClaimGeneration+1 {
		t.Fatalf("retry reservation=%+v err=%v", retry, err)
	}

	blockedRequest := fixture.request(t, "step-blocked", 2, 3)
	blocked, err := fixture.store.reserveLaunchModelStep(ctx, blockedRequest)
	if err != nil {
		t.Fatal(err)
	}
	blockedPermit, err := fixture.store.dispatchLaunchModelStep(ctx, blocked.Claim, "request-step-blocked", blockedRequest.WireRequestDigest)
	if err != nil {
		t.Fatal(err)
	}
	blockedDigest := launchTestDigest(t, "ambiguous-output")
	if err := fixture.store.blockLaunchModelStep(ctx, blockedPermit.Claim, launchReasonAmbiguous, "response-step-blocked", blockedDigest); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.blockLaunchModelStep(ctx, blockedPermit.Claim, launchReasonAmbiguous, "response-step-blocked", blockedDigest); err != nil {
		t.Fatalf("block replay: %v", err)
	}
	blockedReplay, err := fixture.store.reserveLaunchModelStep(ctx, blockedRequest)
	if err != nil || blockedReplay.Disposition != launchAdmissionReplayBlocked || blockedReplay.Step.Status != StepBlocked {
		t.Fatalf("blocked replay=%+v err=%v", blockedReplay, err)
	}
}

func TestLaunchModelReservation_DispatchExpiryReturnsDurableReplayBeforeDeadlineAdmission(t *testing.T) {
	fixture := newLaunchReservationFixture(t, "dispatch-expiry-replay", "gsxmail")
	request := fixture.request(t, "step-expired-dispatch", 1, 2)
	admission, err := fixture.store.reserveLaunchModelStep(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := fixture.store.dispatchLaunchModelStep(context.Background(), admission.Claim, "request-expired-dispatch", request.WireRequestDigest)
	if err != nil {
		t.Fatal(err)
	}
	fixture.store.launchReservationNow = func(context.Context, launchEnvelopeQueryer) (time.Time, error) {
		return permit.RequestDeadlineAt, nil
	}
	replay, err := fixture.store.reserveLaunchModelStep(context.Background(), request)
	if err != nil || replay.Disposition != launchAdmissionReplayBlocked || replay.Step.Status != StepBlocked || replay.Step.Error != launchReasonRequestExpired {
		t.Fatalf("expired dispatch replay=%+v err=%v", replay, err)
	}
}

func TestLaunchModelReservation_TerminalOperationReplayDoesNotSampleDatabaseTime(t *testing.T) {
	for _, operation := range []string{"release", "settle", "block"} {
		operation := operation
		t.Run(operation, func(t *testing.T) {
			fixture := newLaunchReservationFixture(t, "terminal-replay-clock-"+operation, "gsxmail")
			request := fixture.request(t, "step-terminal-replay-clock-"+operation, 0, 1)
			admission, err := fixture.store.reserveLaunchModelStep(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			var replay func() error
			switch operation {
			case "release":
				if err := fixture.store.releaseLaunchModelStep(context.Background(), admission.Claim, launchReasonReleased); err != nil {
					t.Fatal(err)
				}
				replay = func() error {
					return fixture.store.releaseLaunchModelStep(context.Background(), admission.Claim, launchReasonReleased)
				}
			case "settle":
				permit, err := fixture.store.dispatchLaunchModelStep(context.Background(), admission.Claim, "request-replay-settle", request.WireRequestDigest)
				if err != nil {
					t.Fatal(err)
				}
				settlement := launchReservationSettlement{ResponseEvidenceID: "response-replay-settle", OutputDigest: launchTestDigest(t, "terminal-replay-settle")}
				if err := fixture.store.settleLaunchModelStep(context.Background(), permit.Claim, settlement); err != nil {
					t.Fatal(err)
				}
				replay = func() error {
					return fixture.store.settleLaunchModelStep(context.Background(), permit.Claim, settlement)
				}
			case "block":
				permit, err := fixture.store.dispatchLaunchModelStep(context.Background(), admission.Claim, "request-replay-block", request.WireRequestDigest)
				if err != nil {
					t.Fatal(err)
				}
				digest := launchTestDigest(t, "terminal-replay-block")
				if err := fixture.store.blockLaunchModelStep(context.Background(), permit.Claim, launchReasonAmbiguous, "response-replay-block", digest); err != nil {
					t.Fatal(err)
				}
				replay = func() error {
					return fixture.store.blockLaunchModelStep(context.Background(), permit.Claim, launchReasonAmbiguous, "response-replay-block", digest)
				}
			}
			fixture.store.launchReservationNow = func(context.Context, launchEnvelopeQueryer) (time.Time, error) {
				return time.Time{}, errors.New("database clock must not be sampled")
			}
			if err := replay(); err != nil {
				t.Fatalf("terminal replay sampled database time: %v", err)
			}
		})
	}
}

func TestLaunchModelReservation_BudgetBoundariesAndBreachLockout(t *testing.T) {
	t.Run("per request and request count boundaries", func(t *testing.T) {
		fixture := newLaunchReservationFixture(t, "requests", "gsxmail")
		ctx := context.Background()
		maximum := fixture.request(t, "step-max-output", 0, fixture.account.MaxOutputPerRequest)
		maximumAdmission, err := fixture.store.reserveLaunchModelStep(ctx, maximum)
		if err != nil {
			t.Fatalf("exact max output: %v", err)
		}
		if err := fixture.store.releaseLaunchModelStep(ctx, maximumAdmission.Claim, launchReasonReleased); err != nil {
			t.Fatal(err)
		}
		tooLarge := fixture.request(t, "step-too-large", 0, fixture.account.MaxOutputPerRequest+1)
		if _, err := fixture.store.reserveLaunchModelStep(ctx, tooLarge); !errors.Is(err, errLaunchBudgetExhausted) {
			t.Fatalf("max output + 1 error=%v", err)
		}
		for index := int64(0); index < fixture.account.ModelRequests; index++ {
			stepID := "step-count-" + time.Unix(index, 0).UTC().Format("150405")
			request := fixture.request(t, stepID, 0, 1)
			admission, err := fixture.store.reserveLaunchModelStep(ctx, request)
			if err != nil {
				t.Fatalf("request %d reserve: %v", index+1, err)
			}
			permit, err := fixture.store.dispatchLaunchModelStep(ctx, admission.Claim, "request-"+stepID, request.WireRequestDigest)
			if err != nil {
				t.Fatalf("request %d dispatch: %v", index+1, err)
			}
			if err := fixture.store.settleLaunchModelStep(ctx, permit.Claim, launchReservationSettlement{
				ResponseEvidenceID: "response-" + stepID, OutputDigest: launchTestDigest(t, "output:"+stepID),
			}); err != nil {
				t.Fatalf("request %d settle: %v", index+1, err)
			}
		}
		if _, err := fixture.store.reserveLaunchModelStep(ctx, fixture.request(t, "step-count-over", 0, 1)); !errors.Is(err, errLaunchBudgetExhausted) {
			t.Fatalf("request count + 1 error=%v", err)
		}
	})

	t.Run("token equality and plus one", func(t *testing.T) {
		fixture := newLaunchReservationFixture(t, "tokens", "gsxmail")
		request := fixture.request(t, "step-token-limit", fixture.account.InputTokens, 1)
		admission, err := fixture.store.reserveLaunchModelStep(context.Background(), request)
		if err != nil {
			t.Fatalf("exact input limit: %v", err)
		}
		permit, err := fixture.store.dispatchLaunchModelStep(context.Background(), admission.Claim, "request-token-limit", request.WireRequestDigest)
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.settleLaunchModelStep(context.Background(), permit.Claim, launchReservationSettlement{
			InputTokens: fixture.account.InputTokens, OutputTokens: 1,
			ResponseEvidenceID: "response-token-limit", OutputDigest: launchTestDigest(t, "token-limit-output"),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.reserveLaunchModelStep(context.Background(), fixture.request(t, "step-token-over", 1, 1)); !errors.Is(err, errLaunchBudgetExhausted) {
			t.Fatalf("input limit + 1 error=%v", err)
		}
	})

	t.Run("authoritative overrun locks future admission", func(t *testing.T) {
		fixture := newLaunchReservationFixture(t, "breach", "gsxmail")
		request := fixture.request(t, "step-breach", 1, 1)
		admission, err := fixture.store.reserveLaunchModelStep(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		permit, err := fixture.store.dispatchLaunchModelStep(context.Background(), admission.Claim, "request-step-breach", request.WireRequestDigest)
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.settleLaunchModelStep(context.Background(), permit.Claim, launchReservationSettlement{
			InputTokens: 2, OutputTokens: 1, ResponseEvidenceID: "response-step-breach", OutputDigest: launchTestDigest(t, "breach-output"),
		}); !errors.Is(err, errLaunchBudgetBreached) {
			t.Fatalf("overrun error=%v", err)
		}
		var state string
		if err := fixture.store.db.QueryRow(`SELECT state FROM launch_model_reservations WHERE run_id=? AND step_id=?`, fixture.runID, request.StepID).Scan(&state); err != nil || state != launchReservationBreached {
			t.Fatalf("breach state=%q err=%v", state, err)
		}
		if _, err := fixture.store.reserveLaunchModelStep(context.Background(), fixture.request(t, "step-after-breach", 0, 1)); !errors.Is(err, errLaunchBudgetBreached) {
			t.Fatalf("post-breach admission error=%v", err)
		}
	})

	t.Run("nonreleased history cannot exceed canonical request limit", func(t *testing.T) {
		fixture := newLaunchReservationFixture(t, "usage-request-bound", "gsxmail")
		if _, err := fixture.store.reserveLaunchModelStep(context.Background(), fixture.request(t, "step-usage-bound", 0, 1)); err != nil {
			t.Fatal(err)
		}
		err := fixture.store.withLaunchReservationWrite(context.Background(), func(ctx context.Context, conn *launchForeignKeyConn) error {
			account, err := readAndValidateLaunchBudgetAccount(ctx, conn, fixture.runID)
			if err != nil {
				return err
			}
			account.ModelRequests = 0
			_, err = readLaunchBudgetUsage(ctx, conn, account)
			return err
		})
		if !errors.Is(err, errLaunchReservationIntegrity) {
			t.Fatalf("nonreleased request overflow error=%v", err)
		}
	})
}

func TestLaunchModelReservation_CapacityAcrossRunsAndStores(t *testing.T) {
	t.Run("per run", func(t *testing.T) {
		fixture := newLaunchReservationFixture(t, "per-run-cap", "gsxmail")
		for index := 0; index < int(fixture.account.PerRunParallelism); index++ {
			if _, err := fixture.store.reserveLaunchModelStep(context.Background(), fixture.request(t, "step-cap-"+string(rune('a'+index)), 0, 1)); err != nil {
				t.Fatalf("reservation %d: %v", index, err)
			}
		}
		if _, err := fixture.store.reserveLaunchModelStep(context.Background(), fixture.request(t, "step-cap-over", 0, 1)); !errors.Is(err, errLaunchCapacity) {
			t.Fatalf("per-run overflow error=%v", err)
		}
	})

	t.Run("global concurrent across database handles", func(t *testing.T) {
		dbPath := filepath.Join(t.TempDir(), "launch.db")
		seed, err := New(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		fixtures := make([]launchReservationFixture, 3)
		for index := range fixtures {
			fixtures[index] = prepareLaunchReservationFixture(t, seed, "session-global", "run-global-"+string(rune('a'+index)), "gsxmail")
		}
		if err := seed.Close(); err != nil {
			t.Fatal(err)
		}
		stores := make([]*SQLiteStore, len(fixtures))
		for index := range stores {
			stores[index], err = New(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer stores[index].Close()
			fixtures[index].store = stores[index]
		}
		start := make(chan struct{})
		errorsByRun := make(chan error, len(fixtures))
		var wg sync.WaitGroup
		for index := range fixtures {
			fixture := fixtures[index]
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := fixture.store.reserveLaunchModelStep(context.Background(), fixture.request(t, "step-global", 0, 1))
				errorsByRun <- err
			}()
		}
		close(start)
		wg.Wait()
		close(errorsByRun)
		succeeded, capacity := 0, 0
		for err := range errorsByRun {
			switch {
			case err == nil:
				succeeded++
			case errors.Is(err, errLaunchCapacity):
				capacity++
			default:
				t.Fatalf("concurrent admission error=%v", err)
			}
		}
		if succeeded != launchcontract.GlobalCapacity || capacity != len(fixtures)-launchcontract.GlobalCapacity {
			t.Fatalf("succeeded=%d capacity=%d", succeeded, capacity)
		}
	})
}

func TestLaunchModelReservation_PairedWritesRollbackAtomically(t *testing.T) {
	t.Run("reserve", func(t *testing.T) {
		fixture := newLaunchReservationFixture(t, "atomic-reserve", "gsxmail")
		if _, err := fixture.store.db.Exec(`
			CREATE TRIGGER fail_launch_reservation_insert
			BEFORE INSERT ON launch_model_reservations
			BEGIN SELECT RAISE(ABORT, 'injected reservation insert failure'); END
		`); err != nil {
			t.Fatal(err)
		}
		request := fixture.request(t, "step-atomic-reserve", 0, 1)
		if admission, err := fixture.store.reserveLaunchModelStep(context.Background(), request); err == nil || admission != (launchReservationAdmission{}) {
			t.Fatalf("reserve admission=%+v err=%v", admission, err)
		}
		assertLaunchStepAndReservationCounts(t, fixture.store, fixture.runID, request.StepID, 0, 0)
	})

	t.Run("dispatch", func(t *testing.T) {
		fixture := newLaunchReservationFixture(t, "atomic-dispatch", "gsxmail")
		request := fixture.request(t, "step-atomic-dispatch", 0, 1)
		admission, err := fixture.store.reserveLaunchModelStep(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		installLaunchStepFailureTrigger(t, fixture.store, "dispatched")
		if permit, err := fixture.store.dispatchLaunchModelStep(context.Background(), admission.Claim, "request-atomic-dispatch", request.WireRequestDigest); err == nil || permit != (launchDispatchPermit{}) {
			t.Fatalf("dispatch permit=%+v err=%v", permit, err)
		}
		reservation := adversarialReservation(t, fixture.store, fixture.runID, request.StepID, admission.Claim.Attempt)
		step, err := fixture.store.GetStep(context.Background(), fixture.runID, request.StepID)
		if err != nil || reservation.State != launchReservationReserved || step.Status != StepStarted || step.DispatchState != StepDispatchClaimed {
			t.Fatalf("rolled back dispatch reservation=%+v step=%+v err=%v", reservation, step, err)
		}
	})

	t.Run("released retry", func(t *testing.T) {
		fixture := newLaunchReservationFixture(t, "atomic-retry", "gsxmail")
		request := fixture.request(t, "step-atomic-retry", 0, 1)
		admission, err := fixture.store.reserveLaunchModelStep(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if err := fixture.store.releaseLaunchModelStep(context.Background(), admission.Claim, launchReasonReleased); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.db.Exec(`
			CREATE TRIGGER fail_launch_step_retry
			BEFORE UPDATE OF attempt ON execution_steps
			BEGIN SELECT RAISE(ABORT, 'injected retry failure'); END
		`); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.store.reserveLaunchModelStep(context.Background(), request); err == nil {
			t.Fatal("retry succeeded through injected failure")
		}
		assertLaunchStepAndReservationCounts(t, fixture.store, fixture.runID, request.StepID, 1, 1)
		step, err := fixture.store.GetStep(context.Background(), fixture.runID, request.StepID)
		if err != nil || step.Status != StepFailed || step.Attempt != admission.Claim.Attempt || step.ClaimGeneration != admission.Claim.ClaimGeneration {
			t.Fatalf("rolled back retry step=%+v err=%v", step, err)
		}
	})

	for _, terminal := range []struct {
		name      string
		stepState string
		dispatch  bool
		invoke    func(*launchReservationFixture, launchReservationClaim) error
	}{
		{name: "release", stepState: StepFailed, invoke: func(fixture *launchReservationFixture, claim launchReservationClaim) error {
			return fixture.store.releaseLaunchModelStep(context.Background(), claim, launchReasonReleased)
		}},
		{name: "settle", stepState: StepCompleted, dispatch: true, invoke: func(fixture *launchReservationFixture, claim launchReservationClaim) error {
			return fixture.store.settleLaunchModelStep(context.Background(), claim, launchReservationSettlement{
				ResponseEvidenceID: "response-atomic-settle", OutputDigest: strings.Repeat("d", 64),
			})
		}},
		{name: "block", stepState: StepBlocked, dispatch: true, invoke: func(fixture *launchReservationFixture, claim launchReservationClaim) error {
			return fixture.store.blockLaunchModelStep(context.Background(), claim, launchReasonAmbiguous, "", "")
		}},
	} {
		terminal := terminal
		t.Run(terminal.name, func(t *testing.T) {
			fixture := newLaunchReservationFixture(t, "atomic-"+terminal.name, "gsxmail")
			request := fixture.request(t, "step-atomic-"+terminal.name, 0, 1)
			admission, err := fixture.store.reserveLaunchModelStep(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			claim := admission.Claim
			wantReservationState, wantDispatch := launchReservationReserved, StepDispatchClaimed
			if terminal.dispatch {
				permit, err := fixture.store.dispatchLaunchModelStep(context.Background(), claim, "request-atomic-"+terminal.name, request.WireRequestDigest)
				if err != nil {
					t.Fatal(err)
				}
				claim = permit.Claim
				wantReservationState, wantDispatch = launchReservationDispatched, StepDispatchDispatched
			}
			installLaunchStepFailureTrigger(t, fixture.store, terminal.stepState)
			if err := terminal.invoke(&fixture, claim); err == nil {
				t.Fatal("paired terminal transition succeeded through injected failure")
			}
			reservation := adversarialReservation(t, fixture.store, fixture.runID, request.StepID, claim.Attempt)
			step, err := fixture.store.GetStep(context.Background(), fixture.runID, request.StepID)
			if err != nil || reservation.State != wantReservationState || step.Status != StepStarted || step.DispatchState != wantDispatch {
				t.Fatalf("rolled back %s reservation=%+v step=%+v err=%v", terminal.name, reservation, step, err)
			}
		})
	}

	t.Run("expiry materialization", func(t *testing.T) {
		fixture := newLaunchReservationFixture(t, "atomic-expiry", "gsxmail")
		request := fixture.request(t, "step-atomic-expiry", 0, 1)
		admission, err := fixture.store.reserveLaunchModelStep(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		fixture.store.launchReservationNow = func(context.Context, launchEnvelopeQueryer) (time.Time, error) {
			return admission.Claim.LeaseExpiresAt, nil
		}
		installLaunchStepFailureTrigger(t, fixture.store, StepFailed)
		if _, err := fixture.store.reserveLaunchModelStep(context.Background(), fixture.request(t, "step-materializer", 0, 1)); err == nil {
			t.Fatal("expiry materialization succeeded through injected failure")
		}
		reservation := adversarialReservation(t, fixture.store, fixture.runID, request.StepID, admission.Claim.Attempt)
		step, err := fixture.store.GetStep(context.Background(), fixture.runID, request.StepID)
		if err != nil || reservation.State != launchReservationReserved || step.Status != StepStarted {
			t.Fatalf("rolled back expiry reservation=%+v step=%+v err=%v", reservation, step, err)
		}
	})
}

func TestLaunchModelReservation_BusyDeadlineRollsBackAndPoolReuses(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "launch.db")
	first, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	fixture := prepareLaunchReservationFixture(t, first, "session-busy-v22", "run-busy-v22", "gsxmail")
	second, err := New(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	lock, err := second.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if _, err := lock.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	request := fixture.request(t, "step-busy-v22", 0, 1)
	if admission, err := first.reserveLaunchModelStep(ctx, request); !errors.Is(err, context.DeadlineExceeded) || admission != (launchReservationAdmission{}) {
		t.Fatalf("busy admission=%+v err=%v", admission, err)
	}
	if _, err := lock.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	assertLaunchStepAndReservationCounts(t, first, fixture.runID, request.StepID, 0, 0)
	if _, err := first.reserveLaunchModelStep(context.Background(), request); err != nil {
		t.Fatalf("pool reuse after busy deadline: %v", err)
	}
}

func TestLaunchModelReservation_InvalidOrCanceledWritesNothing(t *testing.T) {
	fixture := newLaunchReservationFixture(t, "invalid", "gsxmail")
	request := fixture.request(t, "step-invalid", 0, 1)
	request.WireRequestDigest = strings.Repeat("A", 64)
	if _, err := fixture.store.reserveLaunchModelStep(context.Background(), request); !errors.Is(err, errLaunchReservationInvalid) {
		t.Fatalf("invalid wire digest error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request = fixture.request(t, "step-canceled", 0, 1)
	if _, err := fixture.store.reserveLaunchModelStep(ctx, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled reservation error=%v", err)
	}
	var count int
	if err := fixture.store.db.QueryRow(`SELECT COUNT(*) FROM execution_steps WHERE run_id=? AND step_id IN ('step-invalid','step-canceled')`, fixture.runID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid/canceled step rows=%d err=%v", count, err)
	}
}

func TestLaunchReservationOperations_RestoreCallerForeignKeyState(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "launch-fk.db")+"?_pragma=foreign_keys(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store, err := NewWithDB(db)
	if err != nil {
		t.Fatal(err)
	}
	ensureLaunchTestRun(t, store, "session-v22-fk", "run-v22-fk")
	envelope := launchTestEnvelope(t, "session-v22-fk", "run-v22-fk", "gsxmail",
		launchTestPrice(t, time.Now().UTC().Round(0).Add(-time.Second), time.Minute), "")
	if _, _, err := ensureLaunchTestEnvelope(context.Background(), store, envelope); err != nil {
		t.Fatal(err)
	}

	disablePoolForeignKeys(t, db)
	account, _, err := store.activateLaunchBudget(context.Background(), envelope.RunID)
	if err != nil {
		t.Fatal(err)
	}
	assertPoolForeignKeys(t, db, 0)

	disablePoolForeignKeys(t, db)
	window, _, err := store.beginLaunchTurn(context.Background(), envelope.RunID, "task-v22-fk", "turn-v22-fk")
	if err != nil {
		t.Fatal(err)
	}
	assertPoolForeignKeys(t, db, 0)

	fixture := launchReservationFixture{
		store: store, runID: envelope.RunID, session: envelope.SessionID,
		taskID: window.TaskID, turnID: window.TurnID, account: account, window: window,
	}
	disablePoolForeignKeys(t, db)
	if _, err := store.reserveLaunchModelStep(context.Background(), fixture.request(t, "step-v22-fk", 0, 1)); err != nil {
		t.Fatal(err)
	}
	assertPoolForeignKeys(t, db, 0)
}

func TestLaunchReservationMutationSurface_RemainsPackagePrivate(t *testing.T) {
	allowed := map[string]bool{
		"EnsureLaunchAdmission":       true,
		"GetLaunchEnvelope":           true,
		"GetHistoricalLaunchEnvelope": true,
	}
	typeOfStore := reflect.TypeOf((*SQLiteStore)(nil))
	for index := 0; index < typeOfStore.NumMethod(); index++ {
		method := typeOfStore.Method(index)
		if strings.Contains(method.Name, "Launch") && !allowed[method.Name] {
			t.Fatalf("raw launch mutation escaped public surface: %s", method.Name)
		}
	}
}

func newLaunchReservationFixture(t *testing.T, suffix, profileID string) launchReservationFixture {
	t.Helper()
	store, err := New(filepath.Join(t.TempDir(), "launch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close launch reservation store: %v", err)
		}
	})
	return prepareLaunchReservationFixture(t, store, "session-"+suffix, "run-"+suffix, profileID)
}

func prepareLaunchReservationFixture(t *testing.T, store *SQLiteStore, sessionID, runID, profileID string) launchReservationFixture {
	t.Helper()
	ensureLaunchTestRun(t, store, sessionID, runID)
	envelope := launchTestEnvelope(t, sessionID, runID, profileID,
		launchTestPrice(t, time.Now().UTC().Round(0).Add(-time.Second), 5*time.Minute), "")
	if _, _, err := ensureLaunchTestEnvelope(context.Background(), store, envelope); err != nil {
		t.Fatal(err)
	}
	account, replay, err := store.activateLaunchBudget(context.Background(), runID)
	if err != nil || replay {
		t.Fatalf("activate launch account replay=%v err=%v", replay, err)
	}
	taskID, turnID := "task-"+runID, "turn-"+runID
	window, replay, err := store.beginLaunchTurn(context.Background(), runID, taskID, turnID)
	if err != nil || replay {
		t.Fatalf("begin launch turn replay=%v err=%v", replay, err)
	}
	return launchReservationFixture{
		store: store, runID: runID, session: sessionID, taskID: taskID,
		turnID: turnID, account: account, window: window,
	}
}

func (fixture launchReservationFixture) request(t *testing.T, stepID string, inputTokens, outputTokens int64) launchReservationRequest {
	t.Helper()
	return launchReservationRequest{
		RunID: fixture.runID, TaskID: fixture.taskID, TurnID: fixture.turnID,
		StepID: stepID, Kind: "model", IdempotencyKey: "idempotency-" + stepID,
		InputDigest: launchTestDigest(t, "input:"+stepID), WireRequestDigest: launchTestDigest(t, "wire:"+stepID),
		InputTokens: inputTokens, OutputTokens: outputTokens,
	}
}

func endLaunchReservationRun(t *testing.T, store *SQLiteStore, runID, sessionID string) {
	t.Helper()
	endedAt := time.Now().UTC().Round(0)
	result, err := store.db.Exec(`
		UPDATE agent_runs SET ended_at=?
		WHERE run_id=? AND session_id=? AND ended_at IS NULL
	`, sqliteTimestamp(endedAt), runID, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireOneLaunchRow(result, "end launch test run"); err != nil {
		t.Fatal(err)
	}
}

func installLaunchStepFailureTrigger(t *testing.T, store *SQLiteStore, status string) {
	t.Helper()
	if status != StepFailed && status != StepCompleted && status != StepBlocked && status != StepDispatchDispatched {
		t.Fatalf("unsupported injected step status %q", status)
	}
	query := fmt.Sprintf(`
		CREATE TRIGGER fail_launch_step_projection
		BEFORE UPDATE OF status, dispatch_state ON execution_steps
		WHEN NEW.status = '%s' OR NEW.dispatch_state = '%s'
		BEGIN SELECT RAISE(ABORT, 'injected launch step projection failure'); END
	`, status, status)
	if _, err := store.db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func assertLaunchStepAndReservationCounts(t *testing.T, store *SQLiteStore, runID, stepID string, wantSteps, wantReservations int) {
	t.Helper()
	var steps, reservations int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_steps WHERE run_id=? AND step_id=?`, runID, stepID).Scan(&steps); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM launch_model_reservations WHERE run_id=? AND step_id=?`, runID, stepID).Scan(&reservations); err != nil {
		t.Fatal(err)
	}
	if steps != wantSteps || reservations != wantReservations {
		t.Fatalf("step rows=%d want=%d reservation rows=%d want=%d", steps, wantSteps, reservations, wantReservations)
	}
}
