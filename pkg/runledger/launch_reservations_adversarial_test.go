package runledger

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLaunchReservationAdversarial_TamperedBindingsFailClosed(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(*testing.T, *launchReservationAdversarialHarness)
	}{
		{
			name: "turn_deadline",
			tamper: func(t *testing.T, h *launchReservationAdversarialHarness) {
				adversarialExec(t, h.store, `DROP TRIGGER trg_launch_turn_windows_immutable`)
				adversarialExec(t, h.store, `
					UPDATE launch_turn_windows SET deadline_at = ?
					WHERE run_id = ? AND turn_id = ?
				`, sqliteTimestamp(h.window.DeadlineAt.Add(time.Second)), h.request.RunID, h.request.TurnID)
			},
		},
		{
			name: "account_limit",
			tamper: func(t *testing.T, h *launchReservationAdversarialHarness) {
				adversarialExec(t, h.store, `DROP TRIGGER trg_launch_budget_accounts_immutable`)
				adversarialExec(t, h.store, `
					UPDATE launch_budget_accounts
					SET model_requests_limit = model_requests_limit - 1
					WHERE run_id = ?
				`, h.request.RunID)
			},
		},
		{
			name: "profile_binding",
			tamper: func(t *testing.T, h *launchReservationAdversarialHarness) {
				adversarialExec(t, h.store, `DROP TRIGGER trg_launch_budget_accounts_immutable`)
				adversarialExec(t, h.store, `
					UPDATE launch_budget_accounts SET profile_digest = ? WHERE run_id = ?
				`, strings.Repeat("0", 64), h.request.RunID)
			},
		},
		{
			name: "envelope_binding",
			tamper: func(t *testing.T, h *launchReservationAdversarialHarness) {
				adversarialExec(t, h.store, `DROP TRIGGER trg_launch_budget_accounts_immutable`)
				adversarialExec(t, h.store, `
					UPDATE launch_budget_accounts SET envelope_digest = ? WHERE run_id = ?
				`, strings.Repeat("0", 64), h.request.RunID)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newLaunchReservationAdversarialHarness(t, "tamper-"+test.name)
			test.tamper(t, h)
			if _, err := h.store.reserveLaunchModelStep(h.ctx, h.request); !errors.Is(err, errLaunchReservationIntegrity) {
				t.Fatalf("reserve after tamper error = %v", err)
			}
			adversarialAssertReservationCount(t, h.store, h.request.RunID, 0)
		})
	}
}

func TestLaunchReservationAdversarial_ExactDuplicateCannotStealLeaseOrGeneration(t *testing.T) {
	h := newLaunchReservationAdversarialHarness(t, "duplicate")
	first := adversarialReserve(t, h.store, h.request)
	second := adversarialReserve(t, h.store, h.request)

	if first.Disposition != launchAdmissionReserved || second.Disposition != launchAdmissionAlreadyReserved {
		t.Fatalf("dispositions = %q, %q", first.Disposition, second.Disposition)
	}
	if !reflect.DeepEqual(second.Claim, first.Claim) {
		t.Fatalf("duplicate claim changed:\nfirst  = %+v\nsecond = %+v", first.Claim, second.Claim)
	}
	if second.Step.Attempt != first.Step.Attempt || second.Step.ClaimGeneration != first.Step.ClaimGeneration ||
		second.Claim.LeaseGeneration != first.Claim.LeaseGeneration ||
		!second.Claim.LeaseExpiresAt.Equal(first.Claim.LeaseExpiresAt) {
		t.Fatalf("duplicate stole ownership: first=%+v second=%+v", first, second)
	}
	adversarialAssertReservationCount(t, h.store, h.request.RunID, 1)
}

func TestLaunchReservationAdversarial_ConcurrentDispatchMintsOneFreshPermit(t *testing.T) {
	h := newLaunchReservationAdversarialHarness(t, "dispatch-race")
	admission := adversarialReserve(t, h.store, h.request)

	const contenders = 12
	type outcome struct {
		permit launchDispatchPermit
		err    error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, contenders)
	var wg sync.WaitGroup
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			permit, err := h.store.dispatchLaunchModelStep(h.ctx, admission.Claim, "request-evidence", h.request.WireRequestDigest)
			outcomes <- outcome{permit: permit, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(outcomes)

	fresh, alreadyDurable := 0, 0
	var minted launchDispatchPermit
	for result := range outcomes {
		switch {
		case result.err == nil:
			fresh++
			minted = result.permit
		case errors.Is(result.err, errLaunchDispatchAlreadyDurable):
			alreadyDurable++
			if result.permit != (launchDispatchPermit{}) {
				t.Fatalf("failed contender received authority: %+v", result.permit)
			}
		default:
			t.Fatalf("unexpected dispatch error: %v", result.err)
		}
	}
	if fresh != 1 || alreadyDurable != contenders-1 || minted.RequestDeadlineAt.IsZero() {
		t.Fatalf("fresh=%d already_durable=%d permit=%+v", fresh, alreadyDurable, minted)
	}
	if permit, err := h.store.dispatchLaunchModelStep(h.ctx, admission.Claim, "request-evidence", h.request.WireRequestDigest); !errors.Is(err, errLaunchDispatchAlreadyDurable) || permit != (launchDispatchPermit{}) {
		t.Fatalf("second provider authority = %+v, %v", permit, err)
	}
}

func TestLaunchReservationAdversarial_StaleClaimFencesAllMutations(t *testing.T) {
	mutations := []struct {
		name   string
		mutate func(launchReservationClaim) launchReservationClaim
	}{
		{
			name: "claim_generation",
			mutate: func(claim launchReservationClaim) launchReservationClaim {
				claim.ClaimGeneration++
				return claim
			},
		},
		{
			name: "lease_generation",
			mutate: func(claim launchReservationClaim) launchReservationClaim {
				claim.LeaseGeneration++
				return claim
			},
		},
		{
			name: "lease_deadline",
			mutate: func(claim launchReservationClaim) launchReservationClaim {
				claim.LeaseExpiresAt = claim.LeaseExpiresAt.Add(time.Nanosecond)
				return claim
			},
		},
	}
	operations := []struct {
		name      string
		dispatch  bool
		wantState string
		invoke    func(*launchReservationAdversarialHarness, launchReservationClaim) error
	}{
		{
			name:      "release",
			wantState: launchReservationReserved,
			invoke: func(h *launchReservationAdversarialHarness, claim launchReservationClaim) error {
				return h.store.releaseLaunchModelStep(h.ctx, claim, launchReasonReleased)
			},
		},
		{
			name:      "dispatch",
			wantState: launchReservationReserved,
			invoke: func(h *launchReservationAdversarialHarness, claim launchReservationClaim) error {
				_, err := h.store.dispatchLaunchModelStep(h.ctx, claim, "request-evidence", h.request.WireRequestDigest)
				return err
			},
		},
		{
			name:      "settle",
			dispatch:  true,
			wantState: launchReservationDispatched,
			invoke: func(h *launchReservationAdversarialHarness, claim launchReservationClaim) error {
				return h.store.settleLaunchModelStep(h.ctx, claim, launchReservationSettlement{
					InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
					ResponseEvidenceID: "response-evidence", OutputDigest: strings.Repeat("d", 64),
				})
			},
		},
		{
			name:      "block",
			dispatch:  true,
			wantState: launchReservationDispatched,
			invoke: func(h *launchReservationAdversarialHarness, claim launchReservationClaim) error {
				return h.store.blockLaunchModelStep(h.ctx, claim, launchReasonAmbiguous, "", "")
			},
		},
	}

	for _, operation := range operations {
		for _, mutation := range mutations {
			t.Run(operation.name+"/"+mutation.name, func(t *testing.T) {
				h := newLaunchReservationAdversarialHarness(t, "stale-"+operation.name+"-"+mutation.name)
				admission := adversarialReserve(t, h.store, h.request)
				claim := admission.Claim
				if operation.dispatch {
					claim = adversarialDispatch(t, h, claim).Claim
				}
				if err := operation.invoke(h, mutation.mutate(claim)); !errors.Is(err, errLaunchReservationStale) {
					t.Fatalf("stale mutation error = %v", err)
				}
				reservation := adversarialReservation(t, h.store, claim.RunID, claim.StepID, claim.Attempt)
				if reservation.State != operation.wantState {
					t.Fatalf("state after stale mutation = %q, want %q", reservation.State, operation.wantState)
				}
			})
		}
	}
}

func TestLaunchReservationAdversarial_DurableStepFenceBlocksAllMutations(t *testing.T) {
	operations := []struct {
		name      string
		dispatch  bool
		wantState string
		invoke    func(*launchReservationAdversarialHarness, launchReservationClaim) error
	}{
		{
			name:      "release",
			wantState: launchReservationReserved,
			invoke: func(h *launchReservationAdversarialHarness, claim launchReservationClaim) error {
				return h.store.releaseLaunchModelStep(h.ctx, claim, launchReasonReleased)
			},
		},
		{
			name:      "dispatch",
			wantState: launchReservationReserved,
			invoke: func(h *launchReservationAdversarialHarness, claim launchReservationClaim) error {
				_, err := h.store.dispatchLaunchModelStep(h.ctx, claim, "request-evidence", h.request.WireRequestDigest)
				return err
			},
		},
		{
			name:      "settle",
			dispatch:  true,
			wantState: launchReservationDispatched,
			invoke: func(h *launchReservationAdversarialHarness, claim launchReservationClaim) error {
				return h.store.settleLaunchModelStep(h.ctx, claim, launchReservationSettlement{
					InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
					ResponseEvidenceID: "response-evidence", OutputDigest: strings.Repeat("d", 64),
				})
			},
		},
		{
			name:      "block",
			dispatch:  true,
			wantState: launchReservationDispatched,
			invoke: func(h *launchReservationAdversarialHarness, claim launchReservationClaim) error {
				return h.store.blockLaunchModelStep(h.ctx, claim, launchReasonAmbiguous, "", "")
			},
		},
	}

	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			h := newLaunchReservationAdversarialHarness(t, "durable-fence-"+operation.name)
			admission := adversarialReserve(t, h.store, h.request)
			claim := admission.Claim
			if operation.dispatch {
				claim = adversarialDispatch(t, h, claim).Claim
			}
			adversarialExec(t, h.store, `DROP TRIGGER trg_launch_execution_steps_coupled_update`)
			adversarialExec(t, h.store, `
				UPDATE execution_steps SET claim_generation = claim_generation + 1
				WHERE run_id = ? AND step_id = ?
			`, claim.RunID, claim.StepID)
			if err := operation.invoke(h, claim); !errors.Is(err, errLaunchReservationStale) && !errors.Is(err, errLaunchReservationIntegrity) {
				t.Fatalf("durable fence error = %v", err)
			}
			reservation := adversarialReservation(t, h.store, claim.RunID, claim.StepID, claim.Attempt)
			if reservation.State != operation.wantState {
				t.Fatalf("state after durable fence = %q, want %q", reservation.State, operation.wantState)
			}
		})
	}
}

func TestLaunchReservationAdversarial_ExpiredReservedRefundsAndFailsStep(t *testing.T) {
	h := newLaunchReservationAdversarialHarness(t, "expired-reserved")
	admission := adversarialReserve(t, h.store, h.request)
	adversarialForceLeaseExpired(t, h.store, admission.Claim)
	materializerRequest := h.request
	materializerRequest.StepID = "step-expiry-materializer"
	materializerRequest.IdempotencyKey = materializerRequest.StepID
	materializerRequest.InputDigest = strings.Repeat("a", 64)
	materializerRequest.WireRequestDigest = strings.Repeat("b", 64)
	materializer := adversarialReserve(t, h.store, materializerRequest)
	if err := h.store.releaseLaunchModelStep(h.ctx, materializer.Claim, launchReasonReleased); err != nil {
		t.Fatal(err)
	}

	if err := h.store.releaseLaunchModelStep(h.ctx, admission.Claim, launchReasonReleased); !errors.Is(err, errLaunchReservationStale) {
		t.Fatalf("expired release error = %v", err)
	}
	reservation := adversarialReservation(t, h.store, admission.Claim.RunID, admission.Claim.StepID, admission.Claim.Attempt)
	if reservation.State != launchReservationReleased || reservation.TerminalReasonCode != launchReasonPrepareExpired {
		t.Fatalf("expired reservation = %+v", reservation)
	}
	step, err := h.store.GetStep(h.ctx, admission.Claim.RunID, admission.Claim.StepID)
	if err != nil || step.Status != StepFailed || step.Error != launchReasonPrepareExpired {
		t.Fatalf("expired step = %+v, %v", step, err)
	}
	usage, active := adversarialUsageAndActive(t, h.store, admission.Claim.RunID)
	if usage != (launchBudgetUsage{}) || active != 0 {
		t.Fatalf("released usage=%+v active=%d", usage, active)
	}

	retry := adversarialReserve(t, h.store, h.request)
	if retry.Claim.Attempt != admission.Claim.Attempt+1 || retry.Claim.ClaimGeneration != admission.Claim.ClaimGeneration+1 {
		t.Fatalf("retry ownership = %+v after %+v", retry.Claim, admission.Claim)
	}
}

func TestLaunchReservationAdversarial_ExpiredDispatchBlocksChargesAndFreesCapacity(t *testing.T) {
	h := newLaunchReservationAdversarialHarness(t, "expired-dispatched")
	admission := adversarialReserve(t, h.store, h.request)
	permit := adversarialDispatch(t, h, admission.Claim)
	adversarialForceLeaseExpired(t, h.store, permit.Claim)
	materializerRequest := h.request
	materializerRequest.StepID = "step-expiry-materializer"
	materializerRequest.IdempotencyKey = materializerRequest.StepID
	materializerRequest.InputDigest = strings.Repeat("a", 64)
	materializerRequest.WireRequestDigest = strings.Repeat("b", 64)
	materializer := adversarialReserve(t, h.store, materializerRequest)
	if err := h.store.releaseLaunchModelStep(h.ctx, materializer.Claim, launchReasonReleased); err != nil {
		t.Fatal(err)
	}

	if err := h.store.blockLaunchModelStep(h.ctx, permit.Claim, launchReasonAmbiguous, "", ""); !errors.Is(err, errLaunchReservationConflict) {
		t.Fatalf("expired block error = %v", err)
	}
	reservation := adversarialReservation(t, h.store, permit.Claim.RunID, permit.Claim.StepID, permit.Claim.Attempt)
	if reservation.State != launchReservationAmbiguous || reservation.TerminalReasonCode != launchReasonRequestExpired {
		t.Fatalf("expired dispatched reservation = %+v", reservation)
	}
	step, err := h.store.GetStep(h.ctx, permit.Claim.RunID, permit.Claim.StepID)
	if err != nil || step.Status != StepBlocked || step.Error != launchReasonRequestExpired {
		t.Fatalf("expired dispatched step = %+v, %v", step, err)
	}
	usage, active := adversarialUsageAndActive(t, h.store, permit.Claim.RunID)
	if usage.Requests != 1 || usage.Input != h.request.InputTokens || usage.Output != h.request.OutputTokens ||
		usage.Total != h.request.InputTokens+h.request.OutputTokens || usage.Breached || active != 0 {
		t.Fatalf("ambiguous usage=%+v active=%d", usage, active)
	}

	next := h.request
	next.StepID = "step-after-ambiguous"
	next.IdempotencyKey = next.StepID
	next.InputDigest = strings.Repeat("a", 64)
	next.WireRequestDigest = strings.Repeat("b", 64)
	if admission := adversarialReserve(t, h.store, next); admission.Disposition != launchAdmissionReserved {
		t.Fatalf("capacity was not released: %+v", admission)
	}
}

func TestLaunchReservationAdversarial_TerminalReplaySurvivesTurnAndRunDeadline(t *testing.T) {
	tests := []struct {
		name            string
		terminalize     func(*testing.T, *launchReservationAdversarialHarness, launchReservationClaim)
		wantDisposition string
		wantStatus      string
	}{
		{
			name: "completed",
			terminalize: func(t *testing.T, h *launchReservationAdversarialHarness, claim launchReservationClaim) {
				if err := h.store.settleLaunchModelStep(h.ctx, claim, launchReservationSettlement{
					InputTokens: 25, OutputTokens: 10, TotalTokens: 35,
					ResponseEvidenceID: "response-evidence", OutputDigest: strings.Repeat("d", 64),
				}); err != nil {
					t.Fatal(err)
				}
			},
			wantDisposition: launchAdmissionReplayCompleted,
			wantStatus:      StepCompleted,
		},
		{
			name: "blocked",
			terminalize: func(t *testing.T, h *launchReservationAdversarialHarness, claim launchReservationClaim) {
				if err := h.store.blockLaunchModelStep(h.ctx, claim, launchReasonAmbiguous, "", ""); err != nil {
					t.Fatal(err)
				}
			},
			wantDisposition: launchAdmissionReplayBlocked,
			wantStatus:      StepBlocked,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newLaunchReservationAdversarialHarness(t, "terminal-replay-"+test.name)
			admission := adversarialReserve(t, h.store, h.request)
			permit := adversarialDispatch(t, h, admission.Claim)
			test.terminalize(t, h, permit.Claim)
			h.store.launchReservationNow = func(context.Context, launchEnvelopeQueryer) (time.Time, error) {
				return h.account.RunDeadlineAt.Add(time.Second), nil
			}

			replay := adversarialReserve(t, h.store, h.request)
			if replay.Disposition != test.wantDisposition || replay.Step.Status != test.wantStatus {
				t.Fatalf("terminal replay = %+v", replay)
			}
		})
	}
}

func TestLaunchReservationAdversarial_OptionalAmbiguousEvidenceIsValidated(t *testing.T) {
	h := newLaunchReservationAdversarialHarness(t, "optional-evidence")
	admission := adversarialReserve(t, h.store, h.request)
	permit := adversarialDispatch(t, h, admission.Claim)
	digest := strings.Repeat("d", 64)

	invalid := []struct {
		name       string
		evidenceID string
		digest     string
	}{
		{name: "missing_digest", evidenceID: "response-evidence"},
		{name: "missing_evidence_id", digest: digest},
		{name: "malformed_digest", evidenceID: "response-evidence", digest: "not-a-digest"},
		{name: "malformed_evidence_id", evidenceID: strings.Repeat("x", launchMaxEvidenceIDBytes+1), digest: digest},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if err := h.store.blockLaunchModelStep(h.ctx, permit.Claim, launchReasonAmbiguous, test.evidenceID, test.digest); !errors.Is(err, errLaunchReservationInvalid) {
				t.Fatalf("invalid optional evidence error = %v", err)
			}
		})
	}
	if reservation := adversarialReservation(t, h.store, permit.Claim.RunID, permit.Claim.StepID, permit.Claim.Attempt); reservation.State != launchReservationDispatched {
		t.Fatalf("invalid evidence mutated state: %+v", reservation)
	}
	if err := h.store.blockLaunchModelStep(h.ctx, permit.Claim, launchReasonAmbiguous, "", ""); err != nil {
		t.Fatalf("absent optional evidence: %v", err)
	}

	valid := newLaunchReservationAdversarialHarness(t, "valid-evidence")
	validAdmission := adversarialReserve(t, valid.store, valid.request)
	validPermit := adversarialDispatch(t, valid, validAdmission.Claim)
	if err := valid.store.blockLaunchModelStep(valid.ctx, validPermit.Claim, launchReasonAmbiguous, "response-evidence", digest); err != nil {
		t.Fatalf("valid optional evidence: %v", err)
	}
	reservation := adversarialReservation(t, valid.store, validPermit.Claim.RunID, validPermit.Claim.StepID, validPermit.Claim.Attempt)
	if reservation.ResponseEvidenceID != "response-evidence" || reservation.OutputDigest != digest {
		t.Fatalf("valid optional evidence was not bound: %+v", reservation)
	}
}

func TestLaunchReservationAdversarial_ImmutableRowsRejectDeleteAndSameStateMutation(t *testing.T) {
	h := newLaunchReservationAdversarialHarness(t, "immutable-rows")
	admission := adversarialReserve(t, h.store, h.request)

	for _, mutation := range []struct {
		name  string
		query string
		args  []any
	}{
		{name: "delete_account", query: `DELETE FROM launch_budget_accounts WHERE run_id = ?`, args: []any{h.request.RunID}},
		{name: "delete_turn", query: `DELETE FROM launch_turn_windows WHERE run_id = ? AND turn_id = ?`, args: []any{h.request.RunID, h.request.TurnID}},
		{name: "delete_reservation", query: `DELETE FROM launch_model_reservations WHERE run_id = ? AND step_id = ?`, args: []any{h.request.RunID, h.request.StepID}},
		{name: "reserved_same_state", query: `UPDATE launch_model_reservations SET lease_expires_at = lease_expires_at WHERE run_id = ? AND step_id = ?`, args: []any{h.request.RunID, h.request.StepID}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			if _, err := h.store.db.ExecContext(h.ctx, mutation.query, mutation.args...); err == nil {
				t.Fatal("immutable mutation succeeded")
			}
		})
	}

	permit := adversarialDispatch(t, h, admission.Claim)
	if _, err := h.store.db.ExecContext(h.ctx, `
		UPDATE launch_model_reservations SET request_deadline_at = request_deadline_at
		WHERE run_id = ? AND step_id = ?
	`, h.request.RunID, h.request.StepID); err == nil {
		t.Fatal("dispatched same-state mutation succeeded")
	}
	if err := h.store.settleLaunchModelStep(h.ctx, permit.Claim, launchReservationSettlement{
		InputTokens: 25, OutputTokens: 10, TotalTokens: 35,
		ResponseEvidenceID: "response-evidence", OutputDigest: strings.Repeat("d", 64),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.db.ExecContext(h.ctx, `
		UPDATE launch_model_reservations SET actual_input_tokens = actual_input_tokens
		WHERE run_id = ? AND step_id = ?
	`, h.request.RunID, h.request.StepID); err == nil {
		t.Fatal("terminal same-state mutation succeeded")
	}
}

func TestLaunchReservationAdversarial_ForgedReleasedRefundFailsStepBinding(t *testing.T) {
	h := newLaunchReservationAdversarialHarness(t, "forged-release")
	admission := adversarialReserve(t, h.store, h.request)
	permit := adversarialDispatch(t, h, admission.Claim)
	if err := h.store.settleLaunchModelStep(h.ctx, permit.Claim, launchReservationSettlement{
		InputTokens: h.request.InputTokens, OutputTokens: h.request.OutputTokens,
		ResponseEvidenceID: "response-evidence", OutputDigest: strings.Repeat("d", 64),
	}); err != nil {
		t.Fatal(err)
	}
	adversarialExec(t, h.store, `DROP TRIGGER trg_launch_model_reservations_transition`)
	adversarialExec(t, h.store, `DROP TRIGGER trg_launch_model_reservations_same_state_immutable`)
	adversarialExec(t, h.store, `
		UPDATE launch_model_reservations
		SET state = 'released', actual_input_tokens = NULL,
		    actual_output_tokens = NULL, actual_total_tokens = NULL,
		    request_deadline_at = NULL, dispatched_at = NULL,
		    request_evidence_id = NULL, request_evidence_digest = NULL,
		    response_evidence_id = NULL, output_digest = NULL,
		    terminal_reason_code = ?, terminal_at = reserved_at
		WHERE run_id = ? AND step_id = ?
	`, launchReasonReleased, h.request.RunID, h.request.StepID)

	next := h.request
	next.StepID = "step-after-forged-release"
	next.IdempotencyKey = next.StepID
	next.InputDigest = strings.Repeat("a", 64)
	next.WireRequestDigest = strings.Repeat("b", 64)
	if _, err := h.store.reserveLaunchModelStep(h.ctx, next); !errors.Is(err, errLaunchReservationIntegrity) {
		t.Fatalf("forged released refund error=%v", err)
	}
	adversarialAssertReservationCount(t, h.store, h.request.RunID, 1)
}

func TestLaunchReservationAdversarial_GenericStepMutatorsCannotCrossLaunchBoundary(t *testing.T) {
	h := newLaunchReservationAdversarialHarness(t, "generic-step-guard")
	admission := adversarialReserve(t, h.store, h.request)
	step := admission.Step
	now := time.Now().UTC().Round(0)

	mutations := []struct {
		name   string
		invoke func() error
	}{
		{name: "begin", invoke: func() error {
			_, _, err := h.store.BeginStep(h.ctx, step)
			return err
		}},
		{name: "mark_dispatched", invoke: func() error { return h.store.MarkStepDispatched(h.ctx, step, now) }},
		{name: "reclaim", invoke: func() error {
			_, err := h.store.ReclaimStep(h.ctx, step, now)
			return err
		}},
		{name: "complete", invoke: func() error {
			return h.store.CompleteStepAttempt(h.ctx, step, "generic-output", strings.Repeat("d", 64), now)
		}},
		{name: "fail", invoke: func() error { return h.store.FailStepAttempt(h.ctx, step, "generic_failure", now) }},
		{name: "block", invoke: func() error { return h.store.BlockStep(h.ctx, step, "generic_block", "", "", now) }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			if err := mutation.invoke(); !errors.Is(err, ErrStepTransitionConflict) {
				t.Fatalf("generic mutation error=%v", err)
			}
		})
	}

	permit := adversarialDispatch(t, h, admission.Claim)
	step.DispatchState = StepDispatchDispatched
	if err := h.store.MarkStepDispatched(h.ctx, step, now); !errors.Is(err, ErrStepTransitionConflict) {
		t.Fatalf("generic idempotent dispatch error=%v", err)
	}
	if duplicate, err := h.store.dispatchLaunchModelStep(h.ctx, admission.Claim, "request-evidence", h.request.WireRequestDigest); !errors.Is(err, errLaunchDispatchAlreadyDurable) || duplicate != (launchDispatchPermit{}) {
		t.Fatalf("launch duplicate dispatch=%+v err=%v", duplicate, err)
	}
	if reservation := adversarialReservation(t, h.store, permit.Claim.RunID, permit.Claim.StepID, permit.Claim.Attempt); reservation.State != launchReservationDispatched {
		t.Fatalf("generic mutation changed launch state: %+v", reservation)
	}
}

func TestLaunchReservationAdversarial_TerminalReplayRequiresExactPairedFence(t *testing.T) {
	tests := []struct {
		name        string
		terminalize func(*testing.T, *launchReservationAdversarialHarness, launchReservationClaim)
		replay      func(*launchReservationAdversarialHarness, launchReservationClaim) error
	}{
		{
			name: "released",
			terminalize: func(t *testing.T, h *launchReservationAdversarialHarness, claim launchReservationClaim) {
				if err := h.store.releaseLaunchModelStep(h.ctx, claim, launchReasonReleased); err != nil {
					t.Fatal(err)
				}
			},
			replay: func(h *launchReservationAdversarialHarness, claim launchReservationClaim) error {
				return h.store.releaseLaunchModelStep(h.ctx, claim, launchReasonReleased)
			},
		},
		{
			name: "settled",
			terminalize: func(t *testing.T, h *launchReservationAdversarialHarness, claim launchReservationClaim) {
				if err := h.store.settleLaunchModelStep(h.ctx, claim, launchReservationSettlement{
					InputTokens: h.request.InputTokens, OutputTokens: h.request.OutputTokens,
					ResponseEvidenceID: "response-evidence", OutputDigest: strings.Repeat("d", 64),
				}); err != nil {
					t.Fatal(err)
				}
			},
			replay: func(h *launchReservationAdversarialHarness, claim launchReservationClaim) error {
				return h.store.settleLaunchModelStep(h.ctx, claim, launchReservationSettlement{
					InputTokens: h.request.InputTokens, OutputTokens: h.request.OutputTokens,
					ResponseEvidenceID: "response-evidence", OutputDigest: strings.Repeat("d", 64),
				})
			},
		},
		{
			name: "ambiguous",
			terminalize: func(t *testing.T, h *launchReservationAdversarialHarness, claim launchReservationClaim) {
				if err := h.store.blockLaunchModelStep(h.ctx, claim, launchReasonAmbiguous, "response-evidence", strings.Repeat("d", 64)); err != nil {
					t.Fatal(err)
				}
			},
			replay: func(h *launchReservationAdversarialHarness, claim launchReservationClaim) error {
				return h.store.blockLaunchModelStep(h.ctx, claim, launchReasonAmbiguous, "response-evidence", strings.Repeat("d", 64))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := newLaunchReservationAdversarialHarness(t, "terminal-fence-"+test.name)
			admission := adversarialReserve(t, h.store, h.request)
			claim := admission.Claim
			if test.name != "released" {
				claim = adversarialDispatch(t, h, claim).Claim
			}
			test.terminalize(t, h, claim)
			adversarialExec(t, h.store, `DROP TRIGGER trg_launch_execution_steps_coupled_update`)
			adversarialExec(t, h.store, `
				UPDATE execution_steps SET claim_generation = claim_generation + 1
				WHERE run_id = ? AND step_id = ?
			`, claim.RunID, claim.StepID)
			if _, err := h.store.reserveLaunchModelStep(h.ctx, h.request); !errors.Is(err, errLaunchReservationIntegrity) {
				t.Fatalf("terminal reserve replay error=%v", err)
			}
			if err := test.replay(h, claim); !errors.Is(err, errLaunchReservationIntegrity) {
				t.Fatalf("terminal operation replay error=%v", err)
			}
		})
	}
}

func TestLaunchReservationAdversarial_InsertOrReplaceCannotResetCoupledState(t *testing.T) {
	h := newLaunchReservationAdversarialHarness(t, "replace-guards")
	adversarialReserve(t, h.store, h.request)
	mutations := []struct {
		name  string
		query string
		args  []any
	}{
		{name: "agent_run", query: `INSERT OR REPLACE INTO agent_runs SELECT * FROM agent_runs WHERE run_id=?`, args: []any{h.request.RunID}},
		{name: "run_contract", query: `INSERT OR REPLACE INTO agent_run_contracts SELECT * FROM agent_run_contracts WHERE run_id=?`, args: []any{h.request.RunID}},
		{name: "envelope", query: `INSERT OR REPLACE INTO launch_envelopes SELECT * FROM launch_envelopes WHERE run_id=?`, args: []any{h.request.RunID}},
		{name: "account", query: `INSERT OR REPLACE INTO launch_budget_accounts SELECT * FROM launch_budget_accounts WHERE run_id=?`, args: []any{h.request.RunID}},
		{name: "turn", query: `INSERT OR REPLACE INTO launch_turn_windows SELECT * FROM launch_turn_windows WHERE run_id=? AND turn_id=?`, args: []any{h.request.RunID, h.request.TurnID}},
		{name: "reservation", query: `INSERT OR REPLACE INTO launch_model_reservations SELECT * FROM launch_model_reservations WHERE run_id=? AND step_id=?`, args: []any{h.request.RunID, h.request.StepID}},
		{name: "reservation_active_attempt", query: `
			INSERT OR REPLACE INTO launch_model_reservations (
				run_id, session_id, task_id, turn_id, step_id, kind, idempotency_key,
				input_digest, wire_request_digest, profile_digest, envelope_digest,
				step_attempt, step_claim_generation, lease_generation, state,
				reserved_input_tokens, reserved_output_tokens, reserved_total_tokens,
				reserved_at, lease_expires_at
			)
			SELECT run_id, session_id, task_id, turn_id, step_id, kind, idempotency_key,
				input_digest, wire_request_digest, profile_digest, envelope_digest,
				step_attempt + 1, step_claim_generation + 1, lease_generation, state,
				reserved_input_tokens, reserved_output_tokens, reserved_total_tokens,
				reserved_at, lease_expires_at
			FROM launch_model_reservations WHERE run_id=? AND step_id=?
		`, args: []any{h.request.RunID, h.request.StepID}},
		{name: "execution_step_pk", query: `INSERT OR REPLACE INTO execution_steps SELECT * FROM execution_steps WHERE run_id=? AND step_id=?`, args: []any{h.request.RunID, h.request.StepID}},
		{name: "execution_step_idempotency", query: `
			INSERT OR REPLACE INTO execution_steps (
				run_id, task_id, step_id, kind, idempotency_key, status, attempt,
				claim_generation, input_digest, output_digest, output_evidence_id,
				error_text, dispatch_state, started_at, completed_at
			)
			SELECT run_id, task_id, 'replacement-step', kind, idempotency_key, status, attempt,
				claim_generation, input_digest, output_digest, output_evidence_id,
				error_text, dispatch_state, started_at, completed_at
			FROM execution_steps WHERE run_id=? AND step_id=?
		`, args: []any{h.request.RunID, h.request.StepID}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			if _, err := h.store.db.ExecContext(h.ctx, mutation.query, mutation.args...); err == nil {
				t.Fatal("INSERT OR REPLACE reset succeeded")
			}
		})
	}
	adversarialAssertReservationCount(t, h.store, h.request.RunID, 1)
	if _, _, err := h.store.activateLaunchBudget(h.ctx, h.request.RunID); err != nil {
		t.Fatalf("account was damaged by replacement attempts: %v", err)
	}
}

func TestLaunchReservationAdversarial_UpdateOrReplaceCannotResetCoupledState(t *testing.T) {
	h := newLaunchReservationAdversarialHarness(t, "update-replace-guards")
	adversarialReserve(t, h.store, h.request)
	const runSource = "run-update-replace-source"
	if _, err := h.store.db.Exec(`
		INSERT INTO agent_runs (run_id, session_id, status, started_at)
		VALUES (?, ?, 'queued', ?)
	`, runSource, "session-update-replace-source", sqliteTimestamp(h.account.StartedAt)); err != nil {
		t.Fatal(err)
	}
	ensureLaunchTestRun(t, h.store, "session-update-replace-contract", "run-update-replace-contract")
	for _, stepID := range []string{"source-step-pk", "source-step-idempotency"} {
		if _, err := h.store.db.Exec(`
			INSERT INTO execution_steps (
				run_id, task_id, step_id, kind, idempotency_key, status, attempt,
				claim_generation, input_digest, dispatch_state, started_at
			) VALUES (?, ?, ?, 'model', ?, 'started', 1, 1, ?, 'claimed', ?)
		`, h.request.RunID, h.request.TaskID, stepID, "idempotency-"+stepID,
			strings.Repeat("a", 64), sqliteTimestamp(h.window.StartedAt)); err != nil {
			t.Fatal(err)
		}
	}
	mutations := []struct {
		name  string
		query string
		args  []any
	}{
		{name: "agent_run", query: `
			UPDATE OR REPLACE agent_runs SET run_id=?, session_id=? WHERE run_id=?
		`, args: []any{h.request.RunID, h.account.SessionID, runSource}},
		{name: "run_contract", query: `
			UPDATE OR REPLACE agent_run_contracts SET run_id=?, session_id=? WHERE run_id=?
		`, args: []any{h.request.RunID, h.account.SessionID, "run-update-replace-contract"}},
		{name: "execution_step_pk", query: `
			UPDATE OR REPLACE execution_steps SET step_id=?, idempotency_key=?
			WHERE run_id=? AND step_id=?
		`, args: []any{h.request.StepID, "idempotency-source-step-pk", h.request.RunID, "source-step-pk"}},
		{name: "execution_step_idempotency", query: `
			UPDATE OR REPLACE execution_steps SET idempotency_key=?
			WHERE run_id=? AND step_id=?
		`, args: []any{h.request.IdempotencyKey, h.request.RunID, "source-step-idempotency"}},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			if _, err := h.store.db.ExecContext(h.ctx, mutation.query, mutation.args...); err == nil {
				t.Fatal("UPDATE OR REPLACE reset succeeded")
			}
		})
	}
	adversarialAssertReservationCount(t, h.store, h.request.RunID, 1)
	if _, _, err := h.store.activateLaunchBudget(h.ctx, h.request.RunID); err != nil {
		t.Fatalf("account was damaged by update replacement attempts: %v", err)
	}
}

func TestLaunchReservationAdversarial_DerivedRequestDeadlineCannotDrift(t *testing.T) {
	h := newLaunchReservationAdversarialHarness(t, "deadline-derivation")
	admission := adversarialReserve(t, h.store, h.request)
	permit := adversarialDispatch(t, h, admission.Claim)
	adversarialExec(t, h.store, `DROP TRIGGER trg_launch_model_reservations_same_state_immutable`)
	adversarialExec(t, h.store, `
		UPDATE launch_model_reservations
		SET request_deadline_at = ?, lease_expires_at = ?
		WHERE run_id = ? AND step_id = ? AND step_attempt = ?
	`, sqliteLeaseTimestamp(h.window.DeadlineAt), sqliteLeaseTimestamp(h.window.DeadlineAt),
		permit.Claim.RunID, permit.Claim.StepID, permit.Claim.Attempt)
	if _, err := h.store.dispatchLaunchModelStep(h.ctx, admission.Claim, "request-evidence", h.request.WireRequestDigest); !errors.Is(err, errLaunchReservationIntegrity) {
		t.Fatalf("forged request deadline error=%v", err)
	}
	if _, err := h.store.reserveLaunchModelStep(h.ctx, h.request); !errors.Is(err, errLaunchReservationIntegrity) {
		t.Fatalf("forged request deadline reserve error=%v", err)
	}
}

func TestLaunchReservationAdversarial_DispatchCannotBeginAtOrAfterPrepareDeadline(t *testing.T) {
	for _, offset := range []time.Duration{0, time.Nanosecond} {
		offset := offset
		t.Run(offset.String(), func(t *testing.T) {
			h := newLaunchReservationAdversarialHarness(t, "dispatch-after-prepare-"+strings.ReplaceAll(offset.String(), ".", "-"))
			admission := adversarialReserve(t, h.store, h.request)
			adversarialDispatch(t, h, admission.Claim)
			adversarialExec(t, h.store, `DROP TRIGGER trg_launch_model_reservations_same_state_immutable`)
			dispatchedAt := admission.Claim.LeaseExpiresAt.Add(offset)
			requestTimeout, err := launchDurationFromMS(h.account.RequestTimeoutMS)
			if err != nil {
				t.Fatal(err)
			}
			requestDeadline := minLaunchTime(dispatchedAt.Add(requestTimeout), minLaunchTime(h.window.DeadlineAt, h.account.RunDeadlineAt))
			adversarialExec(t, h.store, `
				UPDATE launch_model_reservations
				SET dispatched_at=?, request_deadline_at=?, lease_expires_at=?
				WHERE run_id=? AND step_id=? AND step_attempt=?
			`, sqliteTimestamp(dispatchedAt), sqliteTimestamp(requestDeadline), sqliteLeaseTimestamp(requestDeadline),
				admission.Claim.RunID, admission.Claim.StepID, admission.Claim.Attempt)
			if _, err := h.store.dispatchLaunchModelStep(h.ctx, admission.Claim, "request-evidence", h.request.WireRequestDigest); !errors.Is(err, errLaunchReservationIntegrity) {
				t.Fatalf("forged dispatch at prepare deadline offset %v error=%v", offset, err)
			}
		})
	}
}

func TestLaunchReservationAdversarial_TurnCannotPredateAccount(t *testing.T) {
	h := newLaunchReservationAdversarialHarness(t, "turn-predates-account")
	adversarialExec(t, h.store, `DROP TRIGGER trg_launch_turn_windows_immutable`)
	startedAt := h.account.StartedAt.Add(-time.Second)
	turnTimeout, err := launchDurationFromMS(h.account.TurnTimeoutMS)
	if err != nil {
		t.Fatal(err)
	}
	deadline := minLaunchTime(startedAt.Add(turnTimeout), h.account.RunDeadlineAt)
	adversarialExec(t, h.store, `
		UPDATE launch_turn_windows SET started_at=?, deadline_at=?
		WHERE run_id=? AND turn_id=?
	`, sqliteTimestamp(startedAt), sqliteTimestamp(deadline), h.request.RunID, h.request.TurnID)
	if _, err := h.store.reserveLaunchModelStep(h.ctx, h.request); !errors.Is(err, errLaunchReservationIntegrity) {
		t.Fatalf("pre-account turn error=%v", err)
	}
}

type launchReservationAdversarialHarness struct {
	ctx     context.Context
	store   *SQLiteStore
	account launchBudgetAccount
	window  launchTurnWindow
	request launchReservationRequest
}

func newLaunchReservationAdversarialHarness(t *testing.T, suffix string) *launchReservationAdversarialHarness {
	t.Helper()
	store, err := New(filepath.Join(t.TempDir(), "launch-reservations.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close launch reservation store: %v", err)
		}
	})
	ctx := context.Background()
	sessionID := "session-" + suffix
	runID := "run-" + suffix
	taskID := "task-" + suffix
	turnID := "turn-" + suffix
	ensureLaunchTestRun(t, store, sessionID, runID)
	price := launchTestPrice(t, time.Now().UTC().Round(0).Add(-time.Second), 5*time.Minute)
	envelope := launchTestEnvelope(t, sessionID, runID, "gsxmail", price, "")
	if _, _, err := ensureLaunchTestEnvelope(ctx, store, envelope); err != nil {
		t.Fatal(err)
	}
	account, replay, err := store.activateLaunchBudget(ctx, runID)
	if err != nil || replay {
		t.Fatalf("activate launch budget = %+v, replay=%v, err=%v", account, replay, err)
	}
	window, replay, err := store.beginLaunchTurn(ctx, runID, taskID, turnID)
	if err != nil || replay {
		t.Fatalf("begin launch turn = %+v, replay=%v, err=%v", window, replay, err)
	}
	return &launchReservationAdversarialHarness{
		ctx: ctx, store: store, account: account, window: window,
		request: launchReservationRequest{
			RunID: runID, TaskID: taskID, TurnID: turnID, StepID: "step-" + suffix,
			Kind: "model", IdempotencyKey: "step-" + suffix,
			InputDigest: strings.Repeat("1", 64), WireRequestDigest: strings.Repeat("2", 64),
			InputTokens: 100, OutputTokens: 200,
		},
	}
}

func adversarialReserve(t *testing.T, store *SQLiteStore, request launchReservationRequest) launchReservationAdmission {
	t.Helper()
	admission, err := store.reserveLaunchModelStep(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	return admission
}

func adversarialDispatch(t *testing.T, h *launchReservationAdversarialHarness, claim launchReservationClaim) launchDispatchPermit {
	t.Helper()
	permit, err := h.store.dispatchLaunchModelStep(h.ctx, claim, "request-evidence", h.request.WireRequestDigest)
	if err != nil {
		t.Fatal(err)
	}
	return permit
}

func adversarialReservation(t *testing.T, store *SQLiteStore, runID, stepID string, attempt int) launchModelReservation {
	t.Helper()
	reservation, err := readLaunchModelReservation(context.Background(), store.db, runID, stepID, attempt)
	if err != nil {
		t.Fatal(err)
	}
	return reservation
}

func adversarialExec(t *testing.T, store *SQLiteStore, query string, args ...any) {
	t.Helper()
	result, err := store.db.ExecContext(context.Background(), query, args...)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(strings.TrimSpace(query), "UPDATE") {
		rows, err := result.RowsAffected()
		if err != nil {
			t.Fatal(err)
		}
		if rows != 1 {
			t.Fatalf("tamper changed %d rows", rows)
		}
	}
}

func adversarialAssertReservationCount(t *testing.T, store *SQLiteStore, runID string, want int) {
	t.Helper()
	var got int
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM launch_model_reservations WHERE run_id = ?
	`, runID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("reservation count = %d, want %d", got, want)
	}
}

func adversarialForceLeaseExpired(t *testing.T, store *SQLiteStore, claim launchReservationClaim) {
	t.Helper()
	store.launchReservationNow = func(context.Context, launchEnvelopeQueryer) (time.Time, error) {
		return claim.LeaseExpiresAt, nil
	}
}

func adversarialUsageAndActive(t *testing.T, store *SQLiteStore, runID string) (launchBudgetUsage, int64) {
	t.Helper()
	var usage launchBudgetUsage
	var active int64
	err := store.withLaunchReservationWrite(context.Background(), func(ctx context.Context, conn *launchForeignKeyConn) error {
		account, err := readAndValidateLaunchBudgetAccount(ctx, conn, runID)
		if err != nil {
			return err
		}
		usage, err = readLaunchBudgetUsage(ctx, conn, account)
		if err != nil {
			return err
		}
		active, err = countBoundedActiveLaunchReservations(ctx, conn, runID, account.PerRunParallelism)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return usage, active
}
