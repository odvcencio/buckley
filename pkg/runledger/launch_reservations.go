package runledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/launchadmission"
	"m31labs.dev/buckley/pkg/launchcontract"
	"m31labs.dev/buckley/pkg/storage"
)

const (
	launchPrepareLease = 30 * time.Second

	launchReservationReserved   = "reserved"
	launchReservationDispatched = "dispatched"
	launchReservationSettled    = "settled"
	launchReservationReleased   = "released"
	launchReservationAmbiguous  = "ambiguous"
	launchReservationBreached   = "breached"

	launchAdmissionReserved        = "reserved"
	launchAdmissionAlreadyReserved = "already_reserved"
	launchAdmissionReplayCompleted = "replay_completed"
	launchAdmissionReplayBlocked   = "replay_blocked"

	launchReasonPrepareExpired  = "prepare_lease_expired"
	launchReasonRequestExpired  = "request_deadline_expired"
	launchReasonReleased        = "predispatch_released"
	launchReasonAmbiguous       = "provider_outcome_ambiguous"
	launchReasonUsageOverrun    = "usage_overrun"
	launchReasonBudgetExhausted = "budget_exhausted"

	launchMaxEvidenceIDBytes = 256
	launchMaxReasonCodeBytes = 64
)

var (
	errLaunchAccountNotFound        = errors.New("runledger: launch budget account not found")
	errLaunchTurnNotFound           = errors.New("runledger: launch turn window not found")
	errLaunchReservationNotFound    = errors.New("runledger: launch model reservation not found")
	errLaunchReservationInvalid     = errors.New("runledger: launch model reservation is invalid")
	errLaunchReservationConflict    = errors.New("runledger: launch model reservation conflicts with durable state")
	errLaunchReservationStale       = errors.New("runledger: launch model reservation ownership is stale")
	errLaunchDispatchAlreadyDurable = errors.New("runledger: launch model dispatch is already durable")
	errLaunchBudgetExhausted        = errors.New("runledger: launch model budget is exhausted")
	errLaunchBudgetBreached         = errors.New("runledger: launch model budget was breached")
	errLaunchCapacity               = errors.New("runledger: launch model capacity is unavailable")
	errLaunchDeadline               = errors.New("runledger: launch model deadline expired")
	errLaunchRunEnded               = errors.New("runledger: launch run has ended")
	errLaunchReservationIntegrity   = errors.New("runledger: launch model reservation integrity violation")
)

// All types and mutation methods in this file are deliberately package-private.
// The later controller boundary must validate and seal the final provider
// request before exposing a production port that can mint these values.
type launchBudgetAccount struct {
	RunID                        string
	SessionID                    string
	ProfileID                    string
	ProfileVersion               string
	ProfileDigest                string
	EnvelopeDigest               string
	ModelRequests                int64
	InputTokens                  int64
	OutputTokens                 int64
	TotalTokens                  int64
	MaxOutputPerRequest          int64
	RequestTimeoutMS             int64
	TurnTimeoutMS                int64
	AbsoluteRunTimeoutMS         int64
	GlobalCapacity               int64
	PerRunParallelism            int64
	ProviderPostAttempts         int64
	ManagerAffordabilityAttempts int64
	RetryOwner                   string
	StartedAt                    time.Time
	RunDeadlineAt                time.Time
}

type launchTurnWindow struct {
	RunID          string
	SessionID      string
	TaskID         string
	TurnID         string
	ProfileDigest  string
	EnvelopeDigest string
	StartedAt      time.Time
	DeadlineAt     time.Time
}

type launchReservationRequest struct {
	RunID             string
	TaskID            string
	TurnID            string
	StepID            string
	Kind              string
	IdempotencyKey    string
	InputDigest       string
	WireRequestDigest string
	InputTokens       int64
	OutputTokens      int64
}

type launchReservationClaim struct {
	RunID           string
	TaskID          string
	TurnID          string
	StepID          string
	Attempt         int
	ClaimGeneration int
	LeaseGeneration int
	InputTokens     int64
	OutputTokens    int64
	TotalTokens     int64
	LeaseExpiresAt  time.Time
}

type launchReservationAdmission struct {
	Claim       launchReservationClaim
	Step        ExecutionStep
	Disposition string
}

type launchDispatchPermit struct {
	Claim             launchReservationClaim
	RequestDeadlineAt time.Time
}

type launchReservationSettlement struct {
	InputTokens        int64
	OutputTokens       int64
	TotalTokens        int64
	ResponseEvidenceID string
	OutputDigest       string
}

type launchModelReservation struct {
	launchReservationClaim
	SessionID             string
	Kind                  string
	IdempotencyKey        string
	InputDigest           string
	WireRequestDigest     string
	ProfileDigest         string
	EnvelopeDigest        string
	State                 string
	ReservedAt            time.Time
	RequestDeadlineAt     *time.Time
	DispatchedAt          *time.Time
	ActualInputTokens     *int64
	ActualOutputTokens    *int64
	ActualTotalTokens     *int64
	ResponseEvidenceID    string
	OutputDigest          string
	RequestEvidenceID     string
	RequestEvidenceDigest string
	TerminalReasonCode    string
	TerminalAt            *time.Time
}

func (s *SQLiteStore) activateLaunchBudget(ctx context.Context, runID string) (account launchBudgetAccount, replay bool, err error) {
	if err := validateLaunchReservationID("run_id", runID); err != nil {
		return launchBudgetAccount{}, false, err
	}
	err = s.withLaunchReservationWrite(ctx, func(ctx context.Context, conn *launchForeignKeyConn) error {
		sessionID, record, err := readLaunchReservationEnvelope(ctx, conn, runID)
		if err != nil {
			return err
		}
		existing, err := readLaunchBudgetAccount(ctx, conn, runID)
		if err == nil {
			if err := validateLaunchBudgetAccount(existing, record); err != nil {
				return err
			}
			account = existing
			replay = true
			return nil
		}
		if !errors.Is(err, errLaunchAccountNotFound) {
			return err
		}
		if err := requireLaunchRunActive(ctx, conn, runID, sessionID); err != nil {
			return err
		}
		now, err := s.launchReservationTime(ctx, conn)
		if err != nil {
			return err
		}
		if err := record.ValidateAt(now); err != nil {
			return fmt.Errorf("%w: launch price evidence is not fresh at activation", errLaunchReservationInvalid)
		}
		profile := record.Snapshot().Profile
		runTimeout, err := launchDurationFromMS(profile.Limits.AbsoluteRunTimeoutMS)
		if err != nil {
			return err
		}
		deadline := now.Add(runTimeout)
		if !deadline.After(now) {
			return fmt.Errorf("%w: absolute run deadline", errLaunchReservationInvalid)
		}
		_, err = conn.ExecContext(ctx, `
			INSERT INTO launch_budget_accounts (
				run_id, session_id, profile_id, profile_version, profile_digest,
				envelope_digest, model_requests_limit, input_tokens_limit,
				output_tokens_limit, total_tokens_limit, max_output_per_request,
				request_timeout_ms, turn_timeout_ms, absolute_run_timeout_ms,
				global_capacity, per_run_parallelism, provider_post_attempts,
				manager_affordability_attempts, retry_owner, started_at,
				run_deadline_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, runID, sessionID, profile.ID, profile.Schema, record.Snapshot().ProfileDigest,
			record.Digest(), profile.Limits.ModelRequests, profile.Limits.InputTokens,
			profile.Limits.OutputTokens, profile.Limits.TotalTokens,
			profile.Limits.MaxOutputPerRequest, profile.Limits.RequestTimeoutMS,
			profile.Limits.TurnTimeoutMS, profile.Limits.AbsoluteRunTimeoutMS,
			profile.Limits.GlobalCapacity, profile.Limits.PerRunParallelism,
			profile.ProviderPostAttempts, profile.ManagerAffordabilityAttempts,
			profile.RetryOwner, sqliteTimestamp(now), sqliteTimestamp(deadline))
		if err != nil {
			return fmt.Errorf("runledger: insert launch budget account: %w", err)
		}
		account, err = readLaunchBudgetAccount(ctx, conn, runID)
		if err != nil {
			return err
		}
		return validateLaunchBudgetAccount(account, record)
	})
	if err != nil {
		return launchBudgetAccount{}, false, err
	}
	return account, replay, nil
}

func (s *SQLiteStore) beginLaunchTurn(ctx context.Context, runID, taskID, turnID string) (window launchTurnWindow, replay bool, err error) {
	for name, value := range map[string]string{"run_id": runID, "task_id": taskID, "turn_id": turnID} {
		if err := validateLaunchReservationID(name, value); err != nil {
			return launchTurnWindow{}, false, err
		}
	}
	err = s.withLaunchReservationWrite(ctx, func(ctx context.Context, conn *launchForeignKeyConn) error {
		account, err := readAndValidateLaunchBudgetAccount(ctx, conn, runID)
		if err != nil {
			return err
		}
		existing, err := readLaunchTurnWindow(ctx, conn, runID, turnID)
		if err == nil {
			if existing.SessionID != account.SessionID || existing.TaskID != taskID || existing.ProfileDigest != account.ProfileDigest || existing.EnvelopeDigest != account.EnvelopeDigest {
				return fmt.Errorf("%w: turn identity drift", errLaunchReservationConflict)
			}
			if err := validateLaunchTurnWindow(existing, account); err != nil {
				return err
			}
			window = existing
			replay = true
			return nil
		}
		if !errors.Is(err, errLaunchTurnNotFound) {
			return err
		}
		if err := requireLaunchRunActive(ctx, conn, runID, account.SessionID); err != nil {
			return err
		}
		now, err := s.launchReservationTime(ctx, conn)
		if err != nil {
			return err
		}
		if !now.Before(account.RunDeadlineAt) {
			return errLaunchDeadline
		}
		turnTimeout, err := launchDurationFromMS(account.TurnTimeoutMS)
		if err != nil {
			return err
		}
		deadline := minLaunchTime(now.Add(turnTimeout), account.RunDeadlineAt)
		if !deadline.After(now) {
			return errLaunchDeadline
		}
		_, err = conn.ExecContext(ctx, `
			INSERT INTO launch_turn_windows (
				run_id, session_id, task_id, turn_id, profile_digest,
				envelope_digest, started_at, deadline_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, runID, account.SessionID, taskID, turnID, account.ProfileDigest,
			account.EnvelopeDigest, sqliteTimestamp(now), sqliteTimestamp(deadline))
		if err != nil {
			return fmt.Errorf("runledger: insert launch turn window: %w", err)
		}
		window, err = readLaunchTurnWindow(ctx, conn, runID, turnID)
		return err
	})
	if err != nil {
		return launchTurnWindow{}, false, err
	}
	return window, replay, nil
}

func (s *SQLiteStore) withLaunchReservationWrite(ctx context.Context, operation func(context.Context, *launchForeignKeyConn) error) error {
	if s == nil || s.db == nil {
		return errors.New("runledger: launch reservation journal is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return retryMailboxBusy(ctx, func() (err error) {
		conn, err := acquireLaunchForeignKeyConn(ctx, s.db)
		if err != nil {
			return err
		}
		began := false
		defer func() {
			if cleanupErr := closeLaunchForeignKeyConn(conn, began); cleanupErr != nil {
				err = errors.Join(err, cleanupErr)
			}
		}()
		if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
			return errors.Join(fmt.Errorf("runledger: begin launch reservation transaction: %w", err), conn.discard())
		}
		began = true
		if err := operation(ctx, conn); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return fmt.Errorf("runledger: commit launch reservation transaction: %w", err)
		}
		began = false
		return nil
	})
}

func (s *SQLiteStore) launchReservationTime(ctx context.Context, queryer launchEnvelopeQueryer) (time.Time, error) {
	if s != nil && s.launchReservationNow != nil {
		now, err := s.launchReservationNow(ctx, queryer)
		if err != nil {
			return time.Time{}, err
		}
		if !launchcontract.CanonicalTime(now) {
			return time.Time{}, fmt.Errorf("%w: database time", errLaunchReservationIntegrity)
		}
		return now, nil
	}
	return launchSQLiteTime(ctx, queryer)
}

func (s *SQLiteStore) guardGenericLaunchStepMutation(ctx context.Context, runID, stepID string) error {
	if s == nil || s.db == nil {
		return errors.New("runledger: execution step journal is unavailable")
	}
	var coupled int
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM launch_model_reservations
			WHERE run_id = ? AND step_id = ?
		)
	`, runID, stepID).Scan(&coupled); err != nil {
		return fmt.Errorf("runledger: inspect launch-coupled execution step: %w", err)
	}
	if coupled != 0 {
		return fmt.Errorf("%w: execution step %s is launch-reservation coupled", ErrStepTransitionConflict, stepID)
	}
	return nil
}

func readLaunchReservationEnvelope(ctx context.Context, conn *launchForeignKeyConn, runID string) (string, launchadmission.Record, error) {
	var sessionID string
	err := conn.QueryRowContext(ctx, `
		SELECT contract.session_id
		FROM agent_run_contracts AS contract
		JOIN agent_runs AS run
		  ON run.run_id = contract.run_id AND run.session_id = contract.session_id
		WHERE contract.run_id = ?
	`, runID).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", launchadmission.Record{}, ErrNotFound
	}
	if err != nil {
		return "", launchadmission.Record{}, fmt.Errorf("runledger: read launch reservation run ownership: %w", err)
	}
	record, err := readLaunchEnvelope(ctx, conn, sessionID, runID)
	if err != nil {
		return "", launchadmission.Record{}, err
	}
	return sessionID, record, nil
}

func readAndValidateLaunchBudgetAccount(ctx context.Context, conn *launchForeignKeyConn, runID string) (launchBudgetAccount, error) {
	_, record, err := readLaunchReservationEnvelope(ctx, conn, runID)
	if err != nil {
		return launchBudgetAccount{}, err
	}
	account, err := readLaunchBudgetAccount(ctx, conn, runID)
	if err != nil {
		return launchBudgetAccount{}, err
	}
	if err := validateLaunchBudgetAccount(account, record); err != nil {
		return launchBudgetAccount{}, err
	}
	return account, nil
}

// requireLaunchRunActive fences only operations that mint fresh provider
// authority. Durable replay and terminal cleanup deliberately do not call it.
func requireLaunchRunActive(ctx context.Context, queryer launchEnvelopeQueryer, runID, sessionID string) error {
	var active int
	err := queryer.QueryRowContext(ctx, `
		SELECT CASE WHEN run.ended_at IS NULL THEN 1 ELSE 0 END
		FROM agent_runs AS run
		JOIN agent_run_contracts AS contract
		  ON contract.run_id = run.run_id AND contract.session_id = run.session_id
		WHERE run.run_id = ? AND run.session_id = ?
	`, runID, sessionID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("runledger: inspect launch run lifecycle: %w", err)
	}
	if active != 1 {
		return errLaunchRunEnded
	}
	return nil
}

func readLaunchBudgetAccount(ctx context.Context, queryer launchEnvelopeQueryer, runID string) (launchBudgetAccount, error) {
	var account launchBudgetAccount
	var startedRaw, deadlineRaw string
	err := queryer.QueryRowContext(ctx, `
		SELECT run_id, session_id, profile_id, profile_version, profile_digest,
		       envelope_digest, model_requests_limit, input_tokens_limit,
		       output_tokens_limit, total_tokens_limit, max_output_per_request,
		       request_timeout_ms, turn_timeout_ms, absolute_run_timeout_ms,
		       global_capacity, per_run_parallelism, provider_post_attempts,
		       manager_affordability_attempts, retry_owner, started_at,
		       run_deadline_at
		FROM launch_budget_accounts WHERE run_id = ?
	`, runID).Scan(&account.RunID, &account.SessionID, &account.ProfileID,
		&account.ProfileVersion, &account.ProfileDigest, &account.EnvelopeDigest,
		&account.ModelRequests, &account.InputTokens, &account.OutputTokens,
		&account.TotalTokens, &account.MaxOutputPerRequest, &account.RequestTimeoutMS,
		&account.TurnTimeoutMS, &account.AbsoluteRunTimeoutMS,
		&account.GlobalCapacity, &account.PerRunParallelism,
		&account.ProviderPostAttempts, &account.ManagerAffordabilityAttempts,
		&account.RetryOwner, &startedRaw, &deadlineRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return launchBudgetAccount{}, errLaunchAccountNotFound
	}
	if err != nil {
		return launchBudgetAccount{}, fmt.Errorf("runledger: read launch budget account: %w", err)
	}
	account.StartedAt = parseSQLiteTimestamp(startedRaw)
	account.RunDeadlineAt = parseSQLiteTimestamp(deadlineRaw)
	return account, nil
}

func validateLaunchBudgetAccount(account launchBudgetAccount, record launchadmission.Record) error {
	snapshot := record.Snapshot()
	profile := snapshot.Profile
	if err := profile.Validate(); err != nil {
		return fmt.Errorf("%w: canonical profile", errLaunchReservationIntegrity)
	}
	wantDeadline, err := addLaunchMilliseconds(account.StartedAt, profile.Limits.AbsoluteRunTimeoutMS)
	if err != nil {
		return err
	}
	if account.RunID != snapshot.RunID || account.SessionID != snapshot.SessionID ||
		account.ProfileID != profile.ID || account.ProfileVersion != profile.Schema ||
		account.ProfileDigest != snapshot.ProfileDigest || account.EnvelopeDigest != snapshot.EnvelopeDigest ||
		account.ModelRequests != int64(profile.Limits.ModelRequests) ||
		account.InputTokens != profile.Limits.InputTokens || account.OutputTokens != profile.Limits.OutputTokens ||
		account.TotalTokens != profile.Limits.TotalTokens || account.MaxOutputPerRequest != profile.Limits.MaxOutputPerRequest ||
		account.RequestTimeoutMS != profile.Limits.RequestTimeoutMS || account.TurnTimeoutMS != profile.Limits.TurnTimeoutMS ||
		account.AbsoluteRunTimeoutMS != profile.Limits.AbsoluteRunTimeoutMS ||
		account.GlobalCapacity != int64(profile.Limits.GlobalCapacity) || account.PerRunParallelism != int64(profile.Limits.PerRunParallelism) ||
		account.ProviderPostAttempts != int64(profile.ProviderPostAttempts) ||
		account.ManagerAffordabilityAttempts != int64(profile.ManagerAffordabilityAttempts) ||
		account.RetryOwner != profile.RetryOwner || account.RetryOwner != launchcontract.RetryOwnerDapr ||
		account.ProviderPostAttempts != launchcontract.ProviderPostAttempts ||
		account.ManagerAffordabilityAttempts != launchcontract.ManagerAffordabilityAttempts ||
		!launchcontract.CanonicalTime(account.StartedAt) || !launchcontract.CanonicalTime(account.RunDeadlineAt) ||
		account.StartedAt.Before(record.CreatedAt()) || record.ValidateAt(account.StartedAt) != nil ||
		!account.RunDeadlineAt.Equal(wantDeadline) {
		return errLaunchReservationIntegrity
	}
	return nil
}

func readLaunchTurnWindow(ctx context.Context, queryer launchEnvelopeQueryer, runID, turnID string) (launchTurnWindow, error) {
	var window launchTurnWindow
	var startedRaw, deadlineRaw string
	err := queryer.QueryRowContext(ctx, `
		SELECT run_id, session_id, task_id, turn_id, profile_digest,
		       envelope_digest, started_at, deadline_at
		FROM launch_turn_windows WHERE run_id = ? AND turn_id = ?
	`, runID, turnID).Scan(&window.RunID, &window.SessionID, &window.TaskID,
		&window.TurnID, &window.ProfileDigest, &window.EnvelopeDigest,
		&startedRaw, &deadlineRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return launchTurnWindow{}, errLaunchTurnNotFound
	}
	if err != nil {
		return launchTurnWindow{}, fmt.Errorf("runledger: read launch turn window: %w", err)
	}
	window.StartedAt = parseSQLiteTimestamp(startedRaw)
	window.DeadlineAt = parseSQLiteTimestamp(deadlineRaw)
	if !launchcontract.CanonicalTime(window.StartedAt) || !launchcontract.CanonicalTime(window.DeadlineAt) || !window.DeadlineAt.After(window.StartedAt) {
		return launchTurnWindow{}, errLaunchReservationIntegrity
	}
	return window, nil
}

func validateLaunchTurnWindow(window launchTurnWindow, account launchBudgetAccount) error {
	turnTimeout, err := launchDurationFromMS(account.TurnTimeoutMS)
	if err != nil {
		return errLaunchReservationIntegrity
	}
	wantDeadline := minLaunchTime(window.StartedAt.Add(turnTimeout), account.RunDeadlineAt)
	if window.RunID != account.RunID || window.SessionID != account.SessionID ||
		window.ProfileDigest != account.ProfileDigest || window.EnvelopeDigest != account.EnvelopeDigest ||
		window.StartedAt.Before(account.StartedAt) || !window.StartedAt.Before(account.RunDeadlineAt) ||
		!window.DeadlineAt.Equal(wantDeadline) {
		return fmt.Errorf("%w: turn deadline binding", errLaunchReservationIntegrity)
	}
	return nil
}

func validateLaunchReservationID(name, value string) error {
	if err := agentcoord.ValidateMonitorIdentifier(name, value, true); err != nil {
		return fmt.Errorf("%w: %s", errLaunchReservationInvalid, name)
	}
	return nil
}

func validateLaunchDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func validateLaunchReasonCode(value string) error {
	if value == "" || len(value) > launchMaxReasonCodeBytes || strings.TrimSpace(value) != value {
		return errLaunchReservationInvalid
	}
	for _, ch := range value {
		if ch != '_' && ch != '-' && (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
			return errLaunchReservationInvalid
		}
	}
	return nil
}

func launchDurationFromMS(value int64) (time.Duration, error) {
	if value <= 0 || value > math.MaxInt64/int64(time.Millisecond) {
		return 0, fmt.Errorf("%w: duration", errLaunchReservationInvalid)
	}
	return time.Duration(value) * time.Millisecond, nil
}

func addLaunchMilliseconds(start time.Time, milliseconds int64) (time.Time, error) {
	duration, err := launchDurationFromMS(milliseconds)
	if err != nil || start.IsZero() {
		return time.Time{}, fmt.Errorf("%w: deadline", errLaunchReservationIntegrity)
	}
	deadline := start.Add(duration)
	if !deadline.After(start) {
		return time.Time{}, fmt.Errorf("%w: deadline", errLaunchReservationIntegrity)
	}
	return deadline, nil
}

func minLaunchTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

func checkedLaunchAdd(left, right int64) (int64, error) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, errLaunchReservationInvalid
	}
	return left + right, nil
}

func (s *SQLiteStore) reserveLaunchModelStep(ctx context.Context, request launchReservationRequest) (admission launchReservationAdmission, err error) {
	request, totalTokens, err := normalizeLaunchReservationRequest(request)
	if err != nil {
		return launchReservationAdmission{}, err
	}
	err = s.withLaunchReservationWrite(ctx, func(ctx context.Context, conn *launchForeignKeyConn) error {
		account, err := readAndValidateLaunchBudgetAccount(ctx, conn, request.RunID)
		if err != nil {
			return err
		}
		window, err := readLaunchTurnWindow(ctx, conn, request.RunID, request.TurnID)
		if err != nil {
			return err
		}
		if window.SessionID != account.SessionID || window.TaskID != request.TaskID ||
			window.ProfileDigest != account.ProfileDigest || window.EnvelopeDigest != account.EnvelopeDigest {
			return fmt.Errorf("%w: turn/account binding", errLaunchReservationIntegrity)
		}
		if err := validateLaunchTurnWindow(window, account); err != nil {
			return err
		}
		step, stepErr := readLaunchExecutionStep(ctx, conn, request.RunID, request.StepID)
		if stepErr == nil {
			if err := validateLaunchStepIdentity(step, request); err != nil {
				return err
			}
			reservation, reservationErr := readLaunchModelReservation(ctx, conn, step.RunID, step.StepID, step.Attempt)
			if reservationErr != nil {
				if errors.Is(reservationErr, errLaunchReservationNotFound) {
					return fmt.Errorf("%w: execution step has no reservation", errLaunchReservationIntegrity)
				}
				return reservationErr
			}
			if err := validateLaunchReservationIdentity(reservation, account, window, request, totalTokens); err != nil {
				return err
			}
			if err := validateCurrentLaunchReservationStepFence(step, reservation); err != nil {
				return err
			}
			switch step.Status {
			case StepCompleted:
				if reservation.State != launchReservationSettled && reservation.State != launchReservationBreached {
					return fmt.Errorf("%w: completed step reservation state", errLaunchReservationIntegrity)
				}
				if step.OutputEvidenceID != reservation.ResponseEvidenceID || step.OutputDigest != reservation.OutputDigest {
					return fmt.Errorf("%w: completed evidence binding", errLaunchReservationIntegrity)
				}
				admission = launchReservationAdmission{Claim: reservation.launchReservationClaim, Step: step, Disposition: launchAdmissionReplayCompleted}
				return nil
			case StepBlocked:
				if reservation.State != launchReservationAmbiguous || step.OutputEvidenceID != reservation.ResponseEvidenceID || step.OutputDigest != reservation.OutputDigest {
					return fmt.Errorf("%w: blocked reservation binding", errLaunchReservationIntegrity)
				}
				admission = launchReservationAdmission{Claim: reservation.launchReservationClaim, Step: step, Disposition: launchAdmissionReplayBlocked}
				return nil
			}
		} else if !errors.Is(stepErr, ErrStepNotFound) {
			return stepErr
		}

		now, err := s.launchReservationTime(ctx, conn)
		if err != nil {
			return err
		}
		if err := materializeExpiredLaunchReservations(ctx, conn, now); err != nil {
			return err
		}

		step, stepErr = readLaunchExecutionStep(ctx, conn, request.RunID, request.StepID)
		switch {
		case errors.Is(stepErr, ErrStepNotFound):
			if err := requireLaunchRunActive(ctx, conn, account.RunID, account.SessionID); err != nil {
				return err
			}
			if !now.Before(window.DeadlineAt) || !now.Before(account.RunDeadlineAt) {
				return errLaunchDeadline
			}
			if request.OutputTokens > account.MaxOutputPerRequest || request.InputTokens > account.InputTokens ||
				request.OutputTokens > account.OutputTokens || totalTokens > account.TotalTokens {
				return errLaunchBudgetExhausted
			}
			if err := checkLaunchAdmissionCapacity(ctx, conn, account, request.InputTokens, request.OutputTokens, totalTokens); err != nil {
				return err
			}
			step = ExecutionStep{
				RunID: request.RunID, TaskID: request.TaskID, StepID: request.StepID,
				Kind: request.Kind, IdempotencyKey: request.IdempotencyKey,
				Status: StepStarted, Attempt: 1, ClaimGeneration: 1,
				InputDigest: request.InputDigest, DispatchState: StepDispatchClaimed,
				StartedAt: now,
			}
			if err := insertLaunchExecutionStep(ctx, conn, step); err != nil {
				return err
			}
			reservation, err := insertLaunchModelReservation(ctx, conn, account, window, request, step, totalTokens, now)
			if err != nil {
				return err
			}
			admission = launchReservationAdmission{Claim: reservation.launchReservationClaim, Step: step, Disposition: launchAdmissionReserved}
			return nil
		case stepErr != nil:
			return stepErr
		}

		if err := validateLaunchStepIdentity(step, request); err != nil {
			return err
		}
		reservation, reservationErr := readLaunchModelReservation(ctx, conn, step.RunID, step.StepID, step.Attempt)
		if reservationErr != nil {
			if errors.Is(reservationErr, errLaunchReservationNotFound) {
				return fmt.Errorf("%w: execution step has no reservation", errLaunchReservationIntegrity)
			}
			return reservationErr
		}
		if err := validateLaunchReservationIdentity(reservation, account, window, request, totalTokens); err != nil {
			return err
		}
		if err := validateCurrentLaunchReservationStepFence(step, reservation); err != nil {
			return err
		}

		switch step.Status {
		case StepCompleted:
			if reservation.State != launchReservationSettled && reservation.State != launchReservationBreached {
				return fmt.Errorf("%w: completed step reservation state", errLaunchReservationIntegrity)
			}
			if step.OutputEvidenceID != reservation.ResponseEvidenceID || step.OutputDigest != reservation.OutputDigest {
				return fmt.Errorf("%w: completed evidence binding", errLaunchReservationIntegrity)
			}
			admission = launchReservationAdmission{Claim: reservation.launchReservationClaim, Step: step, Disposition: launchAdmissionReplayCompleted}
			return nil
		case StepBlocked:
			if reservation.State != launchReservationAmbiguous || step.OutputEvidenceID != reservation.ResponseEvidenceID || step.OutputDigest != reservation.OutputDigest {
				return fmt.Errorf("%w: blocked reservation binding", errLaunchReservationIntegrity)
			}
			admission = launchReservationAdmission{Claim: reservation.launchReservationClaim, Step: step, Disposition: launchAdmissionReplayBlocked}
			return nil
		case StepStarted:
			switch step.DispatchState {
			case StepDispatchDispatched:
				if reservation.State != launchReservationDispatched {
					return fmt.Errorf("%w: dispatched step reservation state", errLaunchReservationIntegrity)
				}
				return errLaunchDispatchAlreadyDurable
			case StepDispatchClaimed:
				if reservation.State != launchReservationReserved || reservation.ClaimGeneration != step.ClaimGeneration {
					return fmt.Errorf("%w: claimed step reservation state", errLaunchReservationIntegrity)
				}
				if !now.Before(reservation.LeaseExpiresAt) {
					return fmt.Errorf("%w: expired reservation was not materialized", errLaunchReservationIntegrity)
				}
				// An exact duplicate receives the same immutable claim. It cannot
				// steal or extend the live lease; dispatch's reserved->dispatched
				// CAS grants provider authority to exactly one contender.
				admission = launchReservationAdmission{Claim: reservation.launchReservationClaim, Step: step, Disposition: launchAdmissionAlreadyReserved}
				return nil
			default:
				return fmt.Errorf("%w: execution step dispatch state", errLaunchReservationIntegrity)
			}
		case StepFailed:
			if reservation.State != launchReservationReleased {
				return fmt.Errorf("%w: failed step reservation state", errLaunchReservationIntegrity)
			}
			if err := requireLaunchRunActive(ctx, conn, account.RunID, account.SessionID); err != nil {
				return err
			}
			if !now.Before(window.DeadlineAt) || !now.Before(account.RunDeadlineAt) {
				return errLaunchDeadline
			}
			if request.OutputTokens > account.MaxOutputPerRequest || request.InputTokens > account.InputTokens ||
				request.OutputTokens > account.OutputTokens || totalTokens > account.TotalTokens {
				return errLaunchBudgetExhausted
			}
			if err := checkLaunchAdmissionCapacity(ctx, conn, account, request.InputTokens, request.OutputTokens, totalTokens); err != nil {
				return err
			}
			oldAttempt, oldClaimGeneration := step.Attempt, step.ClaimGeneration
			newAttempt := oldAttempt + 1
			newClaimGeneration := oldClaimGeneration + 1
			if newAttempt <= oldAttempt || newClaimGeneration <= oldClaimGeneration {
				return errLaunchReservationIntegrity
			}
			step.Attempt = newAttempt
			step.ClaimGeneration = newClaimGeneration
			step.Status = StepStarted
			step.DispatchState = StepDispatchClaimed
			step.StartedAt = now
			step.CompletedAt = nil
			step.Error, step.OutputEvidenceID, step.OutputDigest = "", "", ""
			reservation, err := insertLaunchModelReservation(ctx, conn, account, window, request, step, totalTokens, now)
			if err != nil {
				return err
			}
			updated, err := conn.ExecContext(ctx, `
				UPDATE execution_steps
				SET status = ?, attempt = ?, claim_generation = ?, input_digest = ?,
				    output_digest = NULL, output_evidence_id = NULL, error_text = NULL,
				    dispatch_state = ?, started_at = ?, completed_at = NULL
				WHERE run_id = ? AND step_id = ? AND status = ? AND attempt = ?
				  AND claim_generation = ?
			`, StepStarted, newAttempt, newClaimGeneration, request.InputDigest,
				StepDispatchClaimed, sqliteTimestamp(now), step.RunID, step.StepID,
				StepFailed, oldAttempt, oldClaimGeneration)
			if err != nil {
				return fmt.Errorf("runledger: retry launch execution step: %w", err)
			}
			if err := requireOneLaunchRow(updated, "retry launch execution step"); err != nil {
				return err
			}
			admission = launchReservationAdmission{Claim: reservation.launchReservationClaim, Step: step, Disposition: launchAdmissionReserved}
			return nil
		default:
			return fmt.Errorf("%w: execution step status", errLaunchReservationIntegrity)
		}
	})
	if err != nil {
		return launchReservationAdmission{}, err
	}
	return admission, nil
}

func (s *SQLiteStore) dispatchLaunchModelStep(ctx context.Context, claim launchReservationClaim, requestEvidenceID, requestEvidenceDigest string) (permit launchDispatchPermit, err error) {
	if err := validateLaunchReservationClaim(claim); err != nil {
		return launchDispatchPermit{}, err
	}
	if err := validateLaunchEvidencePair(requestEvidenceID, requestEvidenceDigest, true); err != nil {
		return launchDispatchPermit{}, err
	}
	err = s.withLaunchReservationWrite(ctx, func(ctx context.Context, conn *launchForeignKeyConn) error {
		account, err := readAndValidateLaunchBudgetAccount(ctx, conn, claim.RunID)
		if err != nil {
			return err
		}
		now, err := s.launchReservationTime(ctx, conn)
		if err != nil {
			return err
		}
		if err := materializeExpiredLaunchReservations(ctx, conn, now); err != nil {
			return err
		}
		window, err := readLaunchTurnWindow(ctx, conn, claim.RunID, claim.TurnID)
		if err != nil {
			return err
		}
		if err := validateLaunchTurnWindow(window, account); err != nil {
			return err
		}
		reservation, err := readLaunchModelReservation(ctx, conn, claim.RunID, claim.StepID, claim.Attempt)
		if err != nil {
			return err
		}
		if reservation.SessionID != account.SessionID || reservation.TaskID != window.TaskID ||
			reservation.TurnID != window.TurnID || reservation.ProfileDigest != account.ProfileDigest ||
			reservation.EnvelopeDigest != account.EnvelopeDigest {
			return fmt.Errorf("%w: dispatch account binding", errLaunchReservationIntegrity)
		}
		if err := validateLaunchReservationAuthorityBounds(reservation, account, window); err != nil {
			return err
		}
		if err := requireLaunchClaimIdentity(reservation, claim); err != nil {
			return err
		}
		if reservation.WireRequestDigest != requestEvidenceDigest {
			return errLaunchReservationConflict
		}
		step, err := readLaunchExecutionStep(ctx, conn, claim.RunID, claim.StepID)
		if err != nil {
			return err
		}
		if err := validateCurrentLaunchReservationStepFence(step, reservation); err != nil {
			return err
		}
		if reservation.State == launchReservationDispatched {
			if reservation.RequestEvidenceID != requestEvidenceID || reservation.RequestEvidenceDigest != requestEvidenceDigest {
				return errLaunchReservationConflict
			}
			return errLaunchDispatchAlreadyDurable
		}
		if reservation.State != launchReservationReserved {
			return errLaunchReservationStale
		}
		if !reservation.LeaseExpiresAt.Equal(claim.LeaseExpiresAt) {
			return errLaunchReservationStale
		}
		if err := requireLaunchRunActive(ctx, conn, account.RunID, account.SessionID); err != nil {
			return err
		}
		if !now.Before(reservation.LeaseExpiresAt) || !now.Before(window.DeadlineAt) || !now.Before(account.RunDeadlineAt) {
			return errLaunchDeadline
		}
		usage, err := readLaunchBudgetUsage(ctx, conn, account)
		if err != nil {
			return err
		}
		if usage.Breached {
			return errLaunchBudgetBreached
		}
		if step.Status != StepStarted || step.DispatchState != StepDispatchClaimed ||
			step.Attempt != claim.Attempt || step.ClaimGeneration != claim.ClaimGeneration ||
			step.TaskID != claim.TaskID {
			return errLaunchReservationStale
		}
		requestTimeout, err := launchDurationFromMS(account.RequestTimeoutMS)
		if err != nil {
			return err
		}
		requestDeadline := minLaunchTime(now.Add(requestTimeout), minLaunchTime(window.DeadlineAt, account.RunDeadlineAt))
		if !requestDeadline.After(now) {
			return errLaunchDeadline
		}
		result, err := conn.ExecContext(ctx, `
			UPDATE launch_model_reservations
			SET state = ?, request_deadline_at = ?, dispatched_at = ?,
			    lease_expires_at = ?, request_evidence_id = ?,
			    request_evidence_digest = ?
			WHERE run_id = ? AND step_id = ? AND step_attempt = ?
			  AND step_claim_generation = ? AND lease_generation = ? AND state = ?
		`, launchReservationDispatched, sqliteTimestamp(requestDeadline), sqliteTimestamp(now),
			sqliteLeaseTimestamp(requestDeadline), requestEvidenceID, requestEvidenceDigest,
			claim.RunID, claim.StepID, claim.Attempt, claim.ClaimGeneration,
			claim.LeaseGeneration, launchReservationReserved)
		if err != nil {
			return fmt.Errorf("runledger: dispatch launch reservation: %w", err)
		}
		if err := requireOneLaunchRow(result, "dispatch launch reservation"); err != nil {
			return err
		}
		result, err = conn.ExecContext(ctx, `
			UPDATE execution_steps
			SET dispatch_state = ?
			WHERE run_id = ? AND step_id = ? AND status = ? AND attempt = ?
			  AND claim_generation = ? AND dispatch_state = ?
		`, StepDispatchDispatched, claim.RunID, claim.StepID, StepStarted,
			claim.Attempt, claim.ClaimGeneration, StepDispatchClaimed)
		if err != nil {
			return fmt.Errorf("runledger: dispatch launch execution step: %w", err)
		}
		if err := requireOneLaunchRow(result, "dispatch launch execution step"); err != nil {
			return err
		}
		claim.LeaseExpiresAt = requestDeadline
		permit = launchDispatchPermit{Claim: claim, RequestDeadlineAt: requestDeadline}
		return nil
	})
	if err != nil {
		return launchDispatchPermit{}, err
	}
	return permit, nil
}

func (s *SQLiteStore) releaseLaunchModelStep(ctx context.Context, claim launchReservationClaim, reasonCode string) (err error) {
	if err := validateLaunchReservationClaim(claim); err != nil {
		return err
	}
	if err := validateLaunchReasonCode(reasonCode); err != nil {
		return err
	}
	err = s.withLaunchReservationWrite(ctx, func(ctx context.Context, conn *launchForeignKeyConn) error {
		account, err := readAndValidateLaunchBudgetAccount(ctx, conn, claim.RunID)
		if err != nil {
			return err
		}
		window, err := readLaunchTurnWindow(ctx, conn, claim.RunID, claim.TurnID)
		if err != nil {
			return err
		}
		if err := validateLaunchTurnWindow(window, account); err != nil {
			return err
		}
		reservation, step, err := readLaunchClaimPair(ctx, conn, account, window, claim)
		if err != nil {
			return err
		}
		if err := requireExactLaunchClaim(reservation, claim); err != nil {
			return err
		}
		if reservation.State == launchReservationReleased {
			if reservation.TerminalReasonCode == reasonCode {
				return nil
			}
			return errLaunchReservationStale
		}
		if reservation.State != launchReservationReserved {
			return errLaunchReservationStale
		}
		now, err := s.launchReservationTime(ctx, conn)
		if err != nil {
			return err
		}
		if err := materializeExpiredLaunchReservations(ctx, conn, now); err != nil {
			return err
		}
		reservation, step, err = readLaunchClaimPair(ctx, conn, account, window, claim)
		if err != nil {
			return err
		}
		if err := requireExactLaunchClaim(reservation, claim); err != nil {
			return err
		}
		if reservation.State != launchReservationReserved || step.Status != StepStarted ||
			step.DispatchState != StepDispatchClaimed || step.Attempt != claim.Attempt ||
			step.ClaimGeneration != claim.ClaimGeneration {
			return errLaunchReservationStale
		}
		result, err := conn.ExecContext(ctx, `
			UPDATE launch_model_reservations
			SET state = ?, terminal_reason_code = ?, terminal_at = ?
			WHERE run_id = ? AND step_id = ? AND step_attempt = ?
			  AND step_claim_generation = ? AND lease_generation = ? AND state = ?
		`, launchReservationReleased, reasonCode, sqliteTimestamp(now), claim.RunID,
			claim.StepID, claim.Attempt, claim.ClaimGeneration, claim.LeaseGeneration,
			launchReservationReserved)
		if err != nil {
			return fmt.Errorf("runledger: release launch reservation: %w", err)
		}
		if err := requireOneLaunchRow(result, "release launch reservation"); err != nil {
			return err
		}
		result, err = conn.ExecContext(ctx, `
			UPDATE execution_steps
			SET status = ?, error_text = ?, completed_at = ?
			WHERE run_id = ? AND step_id = ? AND status = ? AND attempt = ?
			  AND claim_generation = ? AND dispatch_state = ?
		`, StepFailed, reasonCode, sqliteTimestamp(now), claim.RunID, claim.StepID,
			StepStarted, claim.Attempt, claim.ClaimGeneration, StepDispatchClaimed)
		if err != nil {
			return fmt.Errorf("runledger: fail released launch step: %w", err)
		}
		return requireOneLaunchRow(result, "fail released launch step")
	})
	return err
}

func (s *SQLiteStore) settleLaunchModelStep(ctx context.Context, claim launchReservationClaim, settlement launchReservationSettlement) (err error) {
	settlement, err = normalizeLaunchSettlement(settlement)
	if err != nil {
		return err
	}
	if err := validateLaunchReservationClaim(claim); err != nil {
		return err
	}
	var terminalErr error
	err = s.withLaunchReservationWrite(ctx, func(ctx context.Context, conn *launchForeignKeyConn) error {
		terminalErr = nil
		account, err := readAndValidateLaunchBudgetAccount(ctx, conn, claim.RunID)
		if err != nil {
			return err
		}
		window, err := readLaunchTurnWindow(ctx, conn, claim.RunID, claim.TurnID)
		if err != nil {
			return err
		}
		if err := validateLaunchTurnWindow(window, account); err != nil {
			return err
		}
		reservation, step, err := readLaunchClaimPair(ctx, conn, account, window, claim)
		if err != nil {
			return err
		}
		if err := requireExactLaunchClaim(reservation, claim); err != nil {
			return err
		}
		breached := settlement.InputTokens > reservation.InputTokens ||
			settlement.OutputTokens > reservation.OutputTokens || settlement.TotalTokens > reservation.TotalTokens
		state := launchReservationSettled
		reason := ""
		if breached {
			state = launchReservationBreached
			reason = launchReasonUsageOverrun
		}
		if reservation.State == launchReservationSettled || reservation.State == launchReservationBreached {
			if reservation.State != state || reservation.ActualInputTokens == nil || reservation.ActualOutputTokens == nil || reservation.ActualTotalTokens == nil ||
				*reservation.ActualInputTokens != settlement.InputTokens || *reservation.ActualOutputTokens != settlement.OutputTokens ||
				*reservation.ActualTotalTokens != settlement.TotalTokens || reservation.ResponseEvidenceID != settlement.ResponseEvidenceID ||
				reservation.OutputDigest != settlement.OutputDigest || step.Status != StepCompleted ||
				step.OutputEvidenceID != settlement.ResponseEvidenceID || step.OutputDigest != settlement.OutputDigest {
				return errLaunchReservationConflict
			}
			if breached {
				terminalErr = errLaunchBudgetBreached
			}
			return nil
		}
		if reservation.State != launchReservationDispatched {
			return errLaunchReservationStale
		}
		now, err := s.launchReservationTime(ctx, conn)
		if err != nil {
			return err
		}
		if err := materializeExpiredLaunchReservations(ctx, conn, now); err != nil {
			return err
		}
		reservation, step, err = readLaunchClaimPair(ctx, conn, account, window, claim)
		if err != nil {
			return err
		}
		if err := requireExactLaunchClaim(reservation, claim); err != nil {
			return err
		}
		if reservation.State != launchReservationDispatched || step.Status != StepStarted ||
			step.DispatchState != StepDispatchDispatched || step.Attempt != claim.Attempt ||
			step.ClaimGeneration != claim.ClaimGeneration {
			return errLaunchReservationStale
		}
		result, err := conn.ExecContext(ctx, `
			UPDATE launch_model_reservations
			SET state = ?, actual_input_tokens = ?, actual_output_tokens = ?,
			    actual_total_tokens = ?, response_evidence_id = ?, output_digest = ?,
			    terminal_reason_code = ?, terminal_at = ?
			WHERE run_id = ? AND step_id = ? AND step_attempt = ?
			  AND step_claim_generation = ? AND lease_generation = ? AND state = ?
		`, state, settlement.InputTokens, settlement.OutputTokens, settlement.TotalTokens,
			settlement.ResponseEvidenceID, settlement.OutputDigest, nullableStr(reason),
			sqliteTimestamp(now), claim.RunID, claim.StepID, claim.Attempt,
			claim.ClaimGeneration, claim.LeaseGeneration, launchReservationDispatched)
		if err != nil {
			return fmt.Errorf("runledger: settle launch reservation: %w", err)
		}
		if err := requireOneLaunchRow(result, "settle launch reservation"); err != nil {
			return err
		}
		result, err = conn.ExecContext(ctx, `
			UPDATE execution_steps
			SET status = ?, output_evidence_id = ?, output_digest = ?,
			    error_text = NULL, completed_at = ?
			WHERE run_id = ? AND step_id = ? AND status = ? AND attempt = ?
			  AND claim_generation = ? AND dispatch_state = ?
		`, StepCompleted, settlement.ResponseEvidenceID, settlement.OutputDigest,
			sqliteTimestamp(now), claim.RunID, claim.StepID, StepStarted,
			claim.Attempt, claim.ClaimGeneration, StepDispatchDispatched)
		if err != nil {
			return fmt.Errorf("runledger: complete settled launch step: %w", err)
		}
		if err := requireOneLaunchRow(result, "complete settled launch step"); err != nil {
			return err
		}
		if breached {
			terminalErr = errLaunchBudgetBreached
		}
		return nil
	})
	if err != nil {
		return err
	}
	return terminalErr
}

func (s *SQLiteStore) blockLaunchModelStep(ctx context.Context, claim launchReservationClaim, reasonCode, responseEvidenceID, outputDigest string) (err error) {
	if err := validateLaunchReservationClaim(claim); err != nil {
		return err
	}
	if err := validateLaunchReasonCode(reasonCode); err != nil {
		return err
	}
	if err := validateLaunchEvidencePair(responseEvidenceID, outputDigest, false); err != nil {
		return err
	}
	err = s.withLaunchReservationWrite(ctx, func(ctx context.Context, conn *launchForeignKeyConn) error {
		account, err := readAndValidateLaunchBudgetAccount(ctx, conn, claim.RunID)
		if err != nil {
			return err
		}
		window, err := readLaunchTurnWindow(ctx, conn, claim.RunID, claim.TurnID)
		if err != nil {
			return err
		}
		if err := validateLaunchTurnWindow(window, account); err != nil {
			return err
		}
		reservation, step, err := readLaunchClaimPair(ctx, conn, account, window, claim)
		if err != nil {
			return err
		}
		if err := requireExactLaunchClaim(reservation, claim); err != nil {
			return err
		}
		if reservation.State == launchReservationAmbiguous {
			if reservation.TerminalReasonCode == reasonCode && reservation.ResponseEvidenceID == responseEvidenceID &&
				reservation.OutputDigest == outputDigest {
				return nil
			}
			return errLaunchReservationConflict
		}
		if reservation.State != launchReservationDispatched {
			return errLaunchReservationStale
		}
		now, err := s.launchReservationTime(ctx, conn)
		if err != nil {
			return err
		}
		if err := materializeExpiredLaunchReservations(ctx, conn, now); err != nil {
			return err
		}
		reservation, step, err = readLaunchClaimPair(ctx, conn, account, window, claim)
		if err != nil {
			return err
		}
		if err := requireExactLaunchClaim(reservation, claim); err != nil {
			return err
		}
		if reservation.State != launchReservationDispatched || step.Status != StepStarted ||
			step.DispatchState != StepDispatchDispatched || step.Attempt != claim.Attempt ||
			step.ClaimGeneration != claim.ClaimGeneration {
			return errLaunchReservationStale
		}
		result, err := conn.ExecContext(ctx, `
			UPDATE launch_model_reservations
			SET state = ?, response_evidence_id = ?, output_digest = ?,
			    terminal_reason_code = ?, terminal_at = ?
			WHERE run_id = ? AND step_id = ? AND step_attempt = ?
			  AND step_claim_generation = ? AND lease_generation = ? AND state = ?
		`, launchReservationAmbiguous, nullableStr(responseEvidenceID), nullableStr(outputDigest),
			reasonCode, sqliteTimestamp(now), claim.RunID, claim.StepID, claim.Attempt,
			claim.ClaimGeneration, claim.LeaseGeneration, launchReservationDispatched)
		if err != nil {
			return fmt.Errorf("runledger: block launch reservation: %w", err)
		}
		if err := requireOneLaunchRow(result, "block launch reservation"); err != nil {
			return err
		}
		result, err = conn.ExecContext(ctx, `
			UPDATE execution_steps
			SET status = ?, output_evidence_id = ?, output_digest = ?,
			    error_text = ?, completed_at = ?
			WHERE run_id = ? AND step_id = ? AND status = ? AND attempt = ?
			  AND claim_generation = ? AND dispatch_state = ?
		`, StepBlocked, nullableStr(responseEvidenceID), nullableStr(outputDigest), reasonCode,
			sqliteTimestamp(now), claim.RunID, claim.StepID, StepStarted,
			claim.Attempt, claim.ClaimGeneration, StepDispatchDispatched)
		if err != nil {
			return fmt.Errorf("runledger: block launch execution step: %w", err)
		}
		return requireOneLaunchRow(result, "block launch execution step")
	})
	return err
}

func normalizeLaunchSettlement(settlement launchReservationSettlement) (launchReservationSettlement, error) {
	if err := validateLaunchEvidencePair(settlement.ResponseEvidenceID, settlement.OutputDigest, true); err != nil {
		return launchReservationSettlement{}, err
	}
	if settlement.InputTokens < 0 || settlement.OutputTokens < 0 || settlement.TotalTokens < 0 {
		return launchReservationSettlement{}, errLaunchReservationInvalid
	}
	sum, err := checkedLaunchAdd(settlement.InputTokens, settlement.OutputTokens)
	if err != nil {
		return launchReservationSettlement{}, errLaunchReservationInvalid
	}
	if settlement.TotalTokens < sum {
		settlement.TotalTokens = sum
	}
	return settlement, nil
}

func validateLaunchEvidencePair(evidenceID, digest string, required bool) error {
	if evidenceID == "" && digest == "" && !required {
		return nil
	}
	if evidenceID == "" || digest == "" || len(evidenceID) > launchMaxEvidenceIDBytes ||
		agentcoord.ValidateMonitorIdentifier("evidence_id", evidenceID, true) != nil || !validateLaunchDigest(digest) {
		return errLaunchReservationInvalid
	}
	return nil
}

func validateLaunchReservationClaim(claim launchReservationClaim) error {
	for name, value := range map[string]string{
		"run_id": claim.RunID, "task_id": claim.TaskID, "turn_id": claim.TurnID, "step_id": claim.StepID,
	} {
		if err := validateLaunchReservationID(name, value); err != nil {
			return err
		}
	}
	if claim.Attempt <= 0 || claim.ClaimGeneration <= 0 || claim.LeaseGeneration <= 0 ||
		claim.InputTokens < 0 || claim.OutputTokens <= 0 || claim.TotalTokens <= 0 ||
		!launchcontract.CanonicalTime(claim.LeaseExpiresAt) {
		return errLaunchReservationInvalid
	}
	total, err := checkedLaunchAdd(claim.InputTokens, claim.OutputTokens)
	if err != nil || total != claim.TotalTokens {
		return errLaunchReservationInvalid
	}
	return nil
}

func requireExactLaunchClaim(reservation launchModelReservation, claim launchReservationClaim) error {
	if err := requireLaunchClaimIdentity(reservation, claim); err != nil {
		return err
	}
	if !reservation.LeaseExpiresAt.Equal(claim.LeaseExpiresAt) {
		return errLaunchReservationStale
	}
	return nil
}

func requireLaunchClaimIdentity(reservation launchModelReservation, claim launchReservationClaim) error {
	if reservation.RunID != claim.RunID || reservation.TaskID != claim.TaskID ||
		reservation.TurnID != claim.TurnID || reservation.StepID != claim.StepID ||
		reservation.Attempt != claim.Attempt || reservation.ClaimGeneration != claim.ClaimGeneration ||
		reservation.LeaseGeneration != claim.LeaseGeneration || reservation.InputTokens != claim.InputTokens ||
		reservation.OutputTokens != claim.OutputTokens || reservation.TotalTokens != claim.TotalTokens {
		return errLaunchReservationStale
	}
	return nil
}

func readLaunchClaimPair(ctx context.Context, conn *launchForeignKeyConn, account launchBudgetAccount, window launchTurnWindow, claim launchReservationClaim) (launchModelReservation, ExecutionStep, error) {
	reservation, err := readLaunchModelReservation(ctx, conn, claim.RunID, claim.StepID, claim.Attempt)
	if err != nil {
		return launchModelReservation{}, ExecutionStep{}, err
	}
	if err := requireLaunchClaimIdentity(reservation, claim); err != nil {
		return launchModelReservation{}, ExecutionStep{}, err
	}
	if err := validateLaunchReservationAuthorityBounds(reservation, account, window); err != nil {
		return launchModelReservation{}, ExecutionStep{}, err
	}
	step, err := readLaunchExecutionStep(ctx, conn, claim.RunID, claim.StepID)
	if err != nil {
		return launchModelReservation{}, ExecutionStep{}, err
	}
	if err := validateCurrentLaunchReservationStepFence(step, reservation); err != nil {
		return launchModelReservation{}, ExecutionStep{}, err
	}
	return reservation, step, nil
}

func normalizeLaunchReservationRequest(request launchReservationRequest) (launchReservationRequest, int64, error) {
	if request.IdempotencyKey == "" {
		request.IdempotencyKey = request.StepID
	}
	for name, value := range map[string]string{
		"run_id": request.RunID, "task_id": request.TaskID, "turn_id": request.TurnID,
		"step_id": request.StepID, "idempotency_key": request.IdempotencyKey,
	} {
		if err := validateLaunchReservationID(name, value); err != nil {
			return launchReservationRequest{}, 0, err
		}
	}
	if request.Kind != "model" && request.Kind != "finalize" {
		return launchReservationRequest{}, 0, fmt.Errorf("%w: step kind", errLaunchReservationInvalid)
	}
	if !validateLaunchDigest(request.InputDigest) || !validateLaunchDigest(request.WireRequestDigest) || request.InputTokens < 0 || request.OutputTokens <= 0 {
		return launchReservationRequest{}, 0, errLaunchReservationInvalid
	}
	total, err := checkedLaunchAdd(request.InputTokens, request.OutputTokens)
	if err != nil || total <= 0 {
		return launchReservationRequest{}, 0, errLaunchReservationInvalid
	}
	return request, total, nil
}

func validateLaunchStepIdentity(step ExecutionStep, request launchReservationRequest) error {
	if step.RunID != request.RunID || step.TaskID != request.TaskID || step.StepID != request.StepID ||
		step.Kind != request.Kind || step.IdempotencyKey != request.IdempotencyKey || step.InputDigest != request.InputDigest {
		return errLaunchReservationConflict
	}
	if step.Attempt <= 0 || step.ClaimGeneration <= 0 || step.StartedAt.IsZero() {
		return errLaunchReservationIntegrity
	}
	return nil
}

func validateLaunchReservationIdentity(reservation launchModelReservation, account launchBudgetAccount, window launchTurnWindow, request launchReservationRequest, totalTokens int64) error {
	if reservation.RunID != request.RunID || reservation.SessionID != account.SessionID ||
		reservation.TaskID != request.TaskID || reservation.TurnID != request.TurnID ||
		reservation.StepID != request.StepID || reservation.Kind != request.Kind ||
		reservation.IdempotencyKey != request.IdempotencyKey || reservation.InputDigest != request.InputDigest ||
		reservation.WireRequestDigest != request.WireRequestDigest ||
		reservation.ProfileDigest != account.ProfileDigest || reservation.EnvelopeDigest != account.EnvelopeDigest ||
		reservation.InputTokens != request.InputTokens || reservation.OutputTokens != request.OutputTokens ||
		reservation.TotalTokens != totalTokens || window.RunID != request.RunID || window.TurnID != request.TurnID {
		return errLaunchReservationConflict
	}
	return validateLaunchReservationAuthorityBounds(reservation, account, window)
}

func validateLaunchReservationAuthorityBounds(reservation launchModelReservation, account launchBudgetAccount, window launchTurnWindow) error {
	if reservation.RunID != account.RunID || reservation.SessionID != account.SessionID ||
		reservation.TaskID != window.TaskID || reservation.TurnID != window.TurnID ||
		reservation.ProfileDigest != account.ProfileDigest || reservation.EnvelopeDigest != account.EnvelopeDigest ||
		window.RunID != account.RunID || window.SessionID != account.SessionID ||
		window.ProfileDigest != account.ProfileDigest || window.EnvelopeDigest != account.EnvelopeDigest ||
		reservation.ReservedAt.Before(window.StartedAt) || !reservation.ReservedAt.Before(window.DeadlineAt) ||
		!reservation.ReservedAt.Before(account.RunDeadlineAt) {
		return fmt.Errorf("%w: reservation authority binding", errLaunchReservationIntegrity)
	}
	wantPrepareDeadline := minLaunchTime(reservation.ReservedAt.Add(launchPrepareLease),
		minLaunchTime(window.DeadlineAt, account.RunDeadlineAt))
	switch reservation.State {
	case launchReservationReserved, launchReservationReleased:
		if !reservation.LeaseExpiresAt.Equal(wantPrepareDeadline) {
			return fmt.Errorf("%w: prepare lease derivation", errLaunchReservationIntegrity)
		}
	default:
		if reservation.DispatchedAt == nil || reservation.RequestDeadlineAt == nil ||
			!reservation.DispatchedAt.Before(wantPrepareDeadline) ||
			!reservation.DispatchedAt.Before(window.DeadlineAt) || !reservation.DispatchedAt.Before(account.RunDeadlineAt) {
			return fmt.Errorf("%w: dispatch deadline binding", errLaunchReservationIntegrity)
		}
		requestTimeout, err := launchDurationFromMS(account.RequestTimeoutMS)
		if err != nil {
			return errLaunchReservationIntegrity
		}
		wantRequestDeadline := minLaunchTime(reservation.DispatchedAt.Add(requestTimeout),
			minLaunchTime(window.DeadlineAt, account.RunDeadlineAt))
		if !reservation.RequestDeadlineAt.Equal(wantRequestDeadline) || !reservation.LeaseExpiresAt.Equal(wantRequestDeadline) {
			return fmt.Errorf("%w: request deadline derivation", errLaunchReservationIntegrity)
		}
	}
	return nil
}

func validateCurrentLaunchReservationStepFence(step ExecutionStep, reservation launchModelReservation) error {
	if step.RunID != reservation.RunID || step.TaskID != reservation.TaskID ||
		step.StepID != reservation.StepID || step.Kind != reservation.Kind ||
		step.IdempotencyKey != reservation.IdempotencyKey || step.InputDigest != reservation.InputDigest ||
		step.Attempt != reservation.Attempt || step.ClaimGeneration != reservation.ClaimGeneration ||
		!step.StartedAt.Equal(reservation.ReservedAt) {
		return fmt.Errorf("%w: execution step reservation fence", errLaunchReservationIntegrity)
	}
	terminalTimeMatches := step.CompletedAt != nil && reservation.TerminalAt != nil && step.CompletedAt.Equal(*reservation.TerminalAt)
	switch reservation.State {
	case launchReservationReserved:
		if step.Status != StepStarted || step.DispatchState != StepDispatchClaimed || step.CompletedAt != nil ||
			step.Error != "" || step.OutputEvidenceID != "" || step.OutputDigest != "" {
			return fmt.Errorf("%w: reserved execution step shape", errLaunchReservationIntegrity)
		}
	case launchReservationDispatched:
		if step.Status != StepStarted || step.DispatchState != StepDispatchDispatched || step.CompletedAt != nil ||
			step.Error != "" || step.OutputEvidenceID != "" || step.OutputDigest != "" {
			return fmt.Errorf("%w: dispatched execution step shape", errLaunchReservationIntegrity)
		}
	case launchReservationSettled, launchReservationBreached:
		if step.Status != StepCompleted || step.DispatchState != StepDispatchDispatched || !terminalTimeMatches ||
			step.Error != "" || step.OutputEvidenceID != reservation.ResponseEvidenceID || step.OutputDigest != reservation.OutputDigest {
			return fmt.Errorf("%w: completed execution step shape", errLaunchReservationIntegrity)
		}
	case launchReservationAmbiguous:
		if step.Status != StepBlocked || step.DispatchState != StepDispatchDispatched || !terminalTimeMatches ||
			step.Error != reservation.TerminalReasonCode || step.OutputEvidenceID != reservation.ResponseEvidenceID || step.OutputDigest != reservation.OutputDigest {
			return fmt.Errorf("%w: blocked execution step shape", errLaunchReservationIntegrity)
		}
	case launchReservationReleased:
		if step.Status != StepFailed || step.DispatchState != StepDispatchClaimed || !terminalTimeMatches ||
			step.Error != reservation.TerminalReasonCode || step.OutputEvidenceID != "" || step.OutputDigest != "" {
			return fmt.Errorf("%w: released execution step shape", errLaunchReservationIntegrity)
		}
	default:
		return errLaunchReservationIntegrity
	}
	return nil
}

func insertLaunchExecutionStep(ctx context.Context, conn *launchForeignKeyConn, step ExecutionStep) error {
	_, err := conn.ExecContext(ctx, `
		INSERT INTO execution_steps (
			run_id, task_id, step_id, kind, idempotency_key, status, attempt,
			claim_generation, input_digest, dispatch_state, started_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, step.RunID, step.TaskID, step.StepID, step.Kind, step.IdempotencyKey,
		step.Status, step.Attempt, step.ClaimGeneration, step.InputDigest,
		step.DispatchState, sqliteTimestamp(step.StartedAt))
	if err != nil {
		return fmt.Errorf("runledger: insert launch execution step: %w", err)
	}
	return nil
}

func insertLaunchModelReservation(ctx context.Context, conn *launchForeignKeyConn, account launchBudgetAccount, window launchTurnWindow, request launchReservationRequest, step ExecutionStep, totalTokens int64, now time.Time) (launchModelReservation, error) {
	prepareDeadline := minLaunchTime(now.Add(launchPrepareLease), minLaunchTime(window.DeadlineAt, account.RunDeadlineAt))
	if !prepareDeadline.After(now) {
		return launchModelReservation{}, errLaunchDeadline
	}
	_, err := conn.ExecContext(ctx, `
		INSERT INTO launch_model_reservations (
			run_id, session_id, task_id, turn_id, step_id, kind,
			idempotency_key, input_digest, wire_request_digest, profile_digest, envelope_digest,
			step_attempt, step_claim_generation, lease_generation, state,
			reserved_input_tokens, reserved_output_tokens, reserved_total_tokens,
			reserved_at, lease_expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, request.RunID, account.SessionID, request.TaskID, request.TurnID,
		request.StepID, request.Kind, request.IdempotencyKey, request.InputDigest, request.WireRequestDigest,
		account.ProfileDigest, account.EnvelopeDigest, step.Attempt,
		step.ClaimGeneration, 1, launchReservationReserved, request.InputTokens,
		request.OutputTokens, totalTokens, sqliteTimestamp(now),
		sqliteLeaseTimestamp(prepareDeadline))
	if err != nil {
		return launchModelReservation{}, fmt.Errorf("runledger: insert launch model reservation: %w", err)
	}
	return readLaunchModelReservation(ctx, conn, request.RunID, request.StepID, step.Attempt)
}

func readLaunchExecutionStep(ctx context.Context, queryer launchEnvelopeQueryer, runID, stepID string) (ExecutionStep, error) {
	return scanExecutionStep(queryer.QueryRowContext(ctx, `
		SELECT run_id, task_id, step_id, kind, idempotency_key, status, attempt,
		       claim_generation, input_digest, output_digest, output_evidence_id,
		       error_text, dispatch_state, started_at, completed_at
		FROM execution_steps WHERE run_id = ? AND step_id = ?
	`, runID, stepID))
}

func requireOneLaunchRow(result sql.Result, operation string) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("runledger: inspect %s: %w", operation, err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: %s changed %d rows", errLaunchReservationStale, operation, rows)
	}
	return nil
}

type launchBudgetUsage struct {
	Requests int64
	Input    int64
	Output   int64
	Total    int64
	Breached bool
}

func checkLaunchAdmissionCapacity(ctx context.Context, conn *launchForeignKeyConn, account launchBudgetAccount, inputTokens, outputTokens, totalTokens int64) error {
	global, err := countBoundedActiveLaunchReservations(ctx, conn, "", account.GlobalCapacity)
	if err != nil {
		return err
	}
	if global >= account.GlobalCapacity {
		return errLaunchCapacity
	}
	perRun, err := countBoundedActiveLaunchReservations(ctx, conn, account.RunID, account.PerRunParallelism)
	if err != nil {
		return err
	}
	if perRun >= account.PerRunParallelism {
		return errLaunchCapacity
	}
	usage, err := readLaunchBudgetUsage(ctx, conn, account)
	if err != nil {
		return err
	}
	if usage.Breached {
		return errLaunchBudgetBreached
	}
	requests, err := checkedLaunchAdd(usage.Requests, 1)
	if err != nil {
		return errLaunchReservationIntegrity
	}
	input, err := checkedLaunchAdd(usage.Input, inputTokens)
	if err != nil {
		return errLaunchReservationIntegrity
	}
	output, err := checkedLaunchAdd(usage.Output, outputTokens)
	if err != nil {
		return errLaunchReservationIntegrity
	}
	total, err := checkedLaunchAdd(usage.Total, totalTokens)
	if err != nil {
		return errLaunchReservationIntegrity
	}
	if requests > account.ModelRequests || input > account.InputTokens || output > account.OutputTokens || total > account.TotalTokens {
		return errLaunchBudgetExhausted
	}
	return nil
}

func countBoundedActiveLaunchReservations(ctx context.Context, conn *launchForeignKeyConn, runID string, limit int64) (int64, error) {
	if limit <= 0 || limit >= math.MaxInt64 {
		return 0, errLaunchReservationIntegrity
	}
	query := `SELECT run_id FROM launch_model_reservations WHERE state IN ('reserved','dispatched')`
	args := []any{}
	if runID != "" {
		query += ` AND run_id = ?`
		args = append(args, runID)
	}
	query += ` LIMIT ?`
	args = append(args, limit+1)
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("runledger: count active launch reservations: %w", err)
	}
	defer rows.Close()
	var count int64
	for rows.Next() {
		var ignored string
		if err := rows.Scan(&ignored); err != nil {
			return 0, fmt.Errorf("runledger: scan active launch reservation: %w", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("runledger: iterate active launch reservations: %w", err)
	}
	if count > limit {
		return 0, errLaunchReservationIntegrity
	}
	return count, nil
}

func readLaunchBudgetUsage(ctx context.Context, conn *launchForeignKeyConn, account launchBudgetAccount) (launchBudgetUsage, error) {
	if err := validateLaunchReservationStepBindings(ctx, conn, account.RunID); err != nil {
		return launchBudgetUsage{}, err
	}
	rowLimit, err := checkedLaunchAdd(account.ModelRequests, 1)
	if err != nil {
		return launchBudgetUsage{}, errLaunchReservationIntegrity
	}
	rows, err := conn.QueryContext(ctx, `
		SELECT run_id, session_id, task_id, turn_id, step_id, kind,
		       idempotency_key, input_digest, wire_request_digest, profile_digest,
		       envelope_digest, step_attempt, step_claim_generation,
		       lease_generation, state, reserved_input_tokens,
		       reserved_output_tokens, reserved_total_tokens,
		       actual_input_tokens, actual_output_tokens, actual_total_tokens,
		       reserved_at, lease_expires_at, request_deadline_at, dispatched_at,
		       request_evidence_id, request_evidence_digest,
		       response_evidence_id, output_digest, terminal_reason_code, terminal_at
		FROM launch_model_reservations
		WHERE run_id = ? AND state <> ?
		ORDER BY step_id, step_attempt
		LIMIT ?
	`, account.RunID, launchReservationReleased, rowLimit)
	if err != nil {
		return launchBudgetUsage{}, fmt.Errorf("runledger: read launch budget usage: %w", err)
	}
	var reservations []launchModelReservation
	for rows.Next() {
		reservation, err := scanLaunchModelReservation(rows)
		if err != nil {
			rows.Close()
			return launchBudgetUsage{}, err
		}
		reservations = append(reservations, reservation)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return launchBudgetUsage{}, fmt.Errorf("runledger: iterate launch budget usage: %w", err)
	}
	if err := rows.Close(); err != nil {
		return launchBudgetUsage{}, fmt.Errorf("runledger: close launch budget usage: %w", err)
	}
	var usage launchBudgetUsage
	for _, reservation := range reservations {
		window, err := readLaunchTurnWindow(ctx, conn, reservation.RunID, reservation.TurnID)
		if err != nil {
			return launchBudgetUsage{}, err
		}
		if err := validateLaunchTurnWindow(window, account); err != nil {
			return launchBudgetUsage{}, err
		}
		if err := validateLaunchReservationAuthorityBounds(reservation, account, window); err != nil {
			return launchBudgetUsage{}, err
		}
		step, err := readLaunchExecutionStep(ctx, conn, reservation.RunID, reservation.StepID)
		if err != nil {
			return launchBudgetUsage{}, err
		}
		if err := validateCurrentLaunchReservationStepFence(step, reservation); err != nil {
			return launchBudgetUsage{}, err
		}
		usage.Requests++
		input, output, total := reservation.InputTokens, reservation.OutputTokens, reservation.TotalTokens
		switch reservation.State {
		case launchReservationReserved, launchReservationDispatched, launchReservationAmbiguous:
			if reservation.ActualInputTokens != nil || reservation.ActualOutputTokens != nil || reservation.ActualTotalTokens != nil {
				return launchBudgetUsage{}, errLaunchReservationIntegrity
			}
		case launchReservationSettled:
			if reservation.ActualInputTokens == nil || reservation.ActualOutputTokens == nil || reservation.ActualTotalTokens == nil {
				return launchBudgetUsage{}, errLaunchReservationIntegrity
			}
			input, output, total = *reservation.ActualInputTokens, *reservation.ActualOutputTokens, *reservation.ActualTotalTokens
		case launchReservationBreached:
			if reservation.ActualInputTokens == nil || reservation.ActualOutputTokens == nil || reservation.ActualTotalTokens == nil {
				return launchBudgetUsage{}, errLaunchReservationIntegrity
			}
			input = max(reservation.InputTokens, *reservation.ActualInputTokens)
			output = max(reservation.OutputTokens, *reservation.ActualOutputTokens)
			total = max(reservation.TotalTokens, *reservation.ActualTotalTokens)
			usage.Breached = true
		default:
			return launchBudgetUsage{}, errLaunchReservationIntegrity
		}
		var addErr error
		if usage.Input, addErr = checkedLaunchAdd(usage.Input, input); addErr != nil {
			return launchBudgetUsage{}, errLaunchReservationIntegrity
		}
		if usage.Output, addErr = checkedLaunchAdd(usage.Output, output); addErr != nil {
			return launchBudgetUsage{}, errLaunchReservationIntegrity
		}
		if usage.Total, addErr = checkedLaunchAdd(usage.Total, total); addErr != nil {
			return launchBudgetUsage{}, errLaunchReservationIntegrity
		}
	}
	if usage.Requests > account.ModelRequests {
		return launchBudgetUsage{}, errLaunchReservationIntegrity
	}
	return usage, nil
}

func validateLaunchReservationStepBindings(ctx context.Context, conn *launchForeignKeyConn, runID string) error {
	var invalid int
	err := conn.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM launch_model_reservations AS reservation
			LEFT JOIN execution_steps AS step
			  ON step.run_id = reservation.run_id AND step.step_id = reservation.step_id
			WHERE reservation.run_id = ? AND (
				step.run_id IS NULL OR
				reservation.task_id IS NOT step.task_id OR
				reservation.kind IS NOT step.kind OR
				reservation.idempotency_key IS NOT step.idempotency_key OR
				reservation.input_digest IS NOT step.input_digest OR
				reservation.step_attempt > step.attempt OR
				(reservation.step_attempt < step.attempt AND reservation.state <> 'released') OR
				(reservation.step_attempt = step.attempt AND (
					reservation.step_claim_generation <> step.claim_generation OR
					reservation.reserved_at IS NOT step.started_at OR NOT (
						(reservation.state = 'reserved' AND step.status = 'started' AND step.dispatch_state = 'claimed'
						 AND step.completed_at IS NULL AND step.output_evidence_id IS NULL AND step.output_digest IS NULL AND step.error_text IS NULL) OR
						(reservation.state = 'dispatched' AND step.status = 'started' AND step.dispatch_state = 'dispatched'
						 AND step.completed_at IS NULL AND step.output_evidence_id IS NULL AND step.output_digest IS NULL AND step.error_text IS NULL) OR
						(reservation.state IN ('settled','breached') AND step.status = 'completed' AND
						 step.dispatch_state = 'dispatched' AND step.completed_at IS reservation.terminal_at AND
						 step.output_evidence_id IS reservation.response_evidence_id AND step.output_digest IS reservation.output_digest
						 AND step.error_text IS NULL) OR
						(reservation.state = 'ambiguous' AND step.status = 'blocked' AND
						 step.dispatch_state = 'dispatched' AND step.completed_at IS reservation.terminal_at AND
						 step.output_evidence_id IS reservation.response_evidence_id AND step.output_digest IS reservation.output_digest
						 AND step.error_text IS reservation.terminal_reason_code) OR
						(reservation.state = 'released' AND step.status = 'failed' AND step.dispatch_state = 'claimed'
						 AND step.completed_at IS reservation.terminal_at AND step.output_evidence_id IS NULL
						 AND step.output_digest IS NULL AND step.error_text IS reservation.terminal_reason_code)
					)
				))
			)
			LIMIT 1
		)
	`, runID).Scan(&invalid)
	if err != nil {
		return fmt.Errorf("runledger: validate launch reservation step bindings: %w", err)
	}
	if invalid != 0 {
		return errLaunchReservationIntegrity
	}
	return nil
}

func readLaunchModelReservation(ctx context.Context, queryer launchEnvelopeQueryer, runID, stepID string, attempt int) (launchModelReservation, error) {
	return scanLaunchModelReservation(queryer.QueryRowContext(ctx, `
		SELECT run_id, session_id, task_id, turn_id, step_id, kind,
		       idempotency_key, input_digest, wire_request_digest, profile_digest,
		       envelope_digest, step_attempt, step_claim_generation,
		       lease_generation, state, reserved_input_tokens,
		       reserved_output_tokens, reserved_total_tokens,
		       actual_input_tokens, actual_output_tokens, actual_total_tokens,
		       reserved_at, lease_expires_at, request_deadline_at, dispatched_at,
		       request_evidence_id, request_evidence_digest,
		       response_evidence_id, output_digest, terminal_reason_code, terminal_at
		FROM launch_model_reservations
		WHERE run_id = ? AND step_id = ? AND step_attempt = ?
	`, runID, stepID, attempt))
}

func scanLaunchModelReservation(scanner executionStepScanner) (launchModelReservation, error) {
	var reservation launchModelReservation
	var actualInput, actualOutput, actualTotal sql.NullInt64
	var reservedRaw, leaseRaw string
	var requestDeadlineRaw, dispatchedRaw, terminalRaw sql.NullString
	var requestEvidenceID, requestEvidenceDigest, responseEvidenceID, outputDigest, reason sql.NullString
	err := scanner.Scan(
		&reservation.RunID, &reservation.SessionID, &reservation.TaskID,
		&reservation.TurnID, &reservation.StepID, &reservation.Kind,
		&reservation.IdempotencyKey, &reservation.InputDigest,
		&reservation.WireRequestDigest, &reservation.ProfileDigest,
		&reservation.EnvelopeDigest, &reservation.Attempt,
		&reservation.ClaimGeneration, &reservation.LeaseGeneration,
		&reservation.State, &reservation.InputTokens, &reservation.OutputTokens,
		&reservation.TotalTokens, &actualInput, &actualOutput, &actualTotal,
		&reservedRaw, &leaseRaw, &requestDeadlineRaw, &dispatchedRaw,
		&requestEvidenceID, &requestEvidenceDigest, &responseEvidenceID,
		&outputDigest, &reason, &terminalRaw,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return launchModelReservation{}, errLaunchReservationNotFound
	}
	if err != nil {
		return launchModelReservation{}, fmt.Errorf("runledger: scan launch model reservation: %w", err)
	}
	reservation.ReservedAt = parseSQLiteTimestamp(reservedRaw)
	reservation.LeaseExpiresAt = parseSQLiteTimestamp(leaseRaw)
	if requestDeadlineRaw.Valid {
		value := parseSQLiteTimestamp(requestDeadlineRaw.String)
		reservation.RequestDeadlineAt = &value
	}
	if dispatchedRaw.Valid {
		value := parseSQLiteTimestamp(dispatchedRaw.String)
		reservation.DispatchedAt = &value
	}
	if actualInput.Valid {
		value := actualInput.Int64
		reservation.ActualInputTokens = &value
	}
	if actualOutput.Valid {
		value := actualOutput.Int64
		reservation.ActualOutputTokens = &value
	}
	if actualTotal.Valid {
		value := actualTotal.Int64
		reservation.ActualTotalTokens = &value
	}
	reservation.RequestEvidenceID = requestEvidenceID.String
	reservation.RequestEvidenceDigest = requestEvidenceDigest.String
	reservation.ResponseEvidenceID = responseEvidenceID.String
	reservation.OutputDigest = outputDigest.String
	reservation.TerminalReasonCode = reason.String
	if terminalRaw.Valid {
		value := parseSQLiteTimestamp(terminalRaw.String)
		reservation.TerminalAt = &value
	}
	if err := validateStoredLaunchReservation(reservation); err != nil {
		return launchModelReservation{}, err
	}
	return reservation, nil
}

func validateStoredLaunchReservation(reservation launchModelReservation) error {
	for name, value := range map[string]string{
		"run_id": reservation.RunID, "session_id": reservation.SessionID,
		"task_id": reservation.TaskID, "turn_id": reservation.TurnID,
		"step_id": reservation.StepID, "idempotency_key": reservation.IdempotencyKey,
	} {
		if validateLaunchReservationID(name, value) != nil {
			return errLaunchReservationIntegrity
		}
	}
	if (reservation.Kind != "model" && reservation.Kind != "finalize") ||
		!validateLaunchDigest(reservation.InputDigest) || !validateLaunchDigest(reservation.WireRequestDigest) ||
		!validateLaunchDigest(reservation.ProfileDigest) || !validateLaunchDigest(reservation.EnvelopeDigest) ||
		reservation.Attempt <= 0 || reservation.ClaimGeneration <= 0 || reservation.LeaseGeneration <= 0 ||
		reservation.InputTokens < 0 || reservation.OutputTokens <= 0 || reservation.TotalTokens <= 0 ||
		!launchcontract.CanonicalTime(reservation.ReservedAt) || !launchcontract.CanonicalTime(reservation.LeaseExpiresAt) ||
		!reservation.LeaseExpiresAt.After(reservation.ReservedAt) {
		return errLaunchReservationIntegrity
	}
	total, err := checkedLaunchAdd(reservation.InputTokens, reservation.OutputTokens)
	if err != nil || total != reservation.TotalTokens {
		return errLaunchReservationIntegrity
	}
	requestEvidenceValid := validateLaunchEvidencePair(reservation.RequestEvidenceID, reservation.RequestEvidenceDigest, true) == nil &&
		reservation.RequestEvidenceDigest == reservation.WireRequestDigest
	responseEvidenceValid := validateLaunchEvidencePair(reservation.ResponseEvidenceID, reservation.OutputDigest, true) == nil
	if (reservation.ResponseEvidenceID == "") != (reservation.OutputDigest == "") ||
		(reservation.RequestEvidenceID == "") != (reservation.RequestEvidenceDigest == "") {
		return errLaunchReservationIntegrity
	}
	actualPresent := reservation.ActualInputTokens != nil && reservation.ActualOutputTokens != nil && reservation.ActualTotalTokens != nil
	if (reservation.ActualInputTokens == nil) != (reservation.ActualOutputTokens == nil) ||
		(reservation.ActualInputTokens == nil) != (reservation.ActualTotalTokens == nil) {
		return errLaunchReservationIntegrity
	}
	if actualPresent && (*reservation.ActualInputTokens < 0 || *reservation.ActualOutputTokens < 0 || *reservation.ActualTotalTokens < 0) {
		return errLaunchReservationIntegrity
	}
	if actualPresent {
		actualSum, err := checkedLaunchAdd(*reservation.ActualInputTokens, *reservation.ActualOutputTokens)
		if err != nil || *reservation.ActualTotalTokens < actualSum {
			return errLaunchReservationIntegrity
		}
	}
	if reservation.RequestDeadlineAt != nil && (!launchcontract.CanonicalTime(*reservation.RequestDeadlineAt) || !reservation.RequestDeadlineAt.After(reservation.ReservedAt)) {
		return errLaunchReservationIntegrity
	}
	if reservation.DispatchedAt != nil && (!launchcontract.CanonicalTime(*reservation.DispatchedAt) || reservation.DispatchedAt.Before(reservation.ReservedAt)) {
		return errLaunchReservationIntegrity
	}
	if reservation.TerminalAt != nil && (!launchcontract.CanonicalTime(*reservation.TerminalAt) || reservation.TerminalAt.Before(reservation.ReservedAt)) {
		return errLaunchReservationIntegrity
	}
	if reservation.RequestDeadlineAt != nil && (!reservation.LeaseExpiresAt.Equal(*reservation.RequestDeadlineAt) ||
		reservation.DispatchedAt == nil || reservation.DispatchedAt.After(*reservation.RequestDeadlineAt)) {
		return errLaunchReservationIntegrity
	}
	if reservation.TerminalAt != nil && reservation.DispatchedAt != nil && reservation.TerminalAt.Before(*reservation.DispatchedAt) {
		return errLaunchReservationIntegrity
	}
	if reservation.TerminalReasonCode != "" && validateLaunchReasonCode(reservation.TerminalReasonCode) != nil {
		return errLaunchReservationIntegrity
	}
	switch reservation.State {
	case launchReservationReserved:
		if reservation.RequestDeadlineAt != nil || reservation.DispatchedAt != nil || reservation.TerminalAt != nil ||
			reservation.TerminalReasonCode != "" || actualPresent || reservation.RequestEvidenceID != "" || reservation.ResponseEvidenceID != "" {
			return errLaunchReservationIntegrity
		}
	case launchReservationDispatched:
		if reservation.RequestDeadlineAt == nil || reservation.DispatchedAt == nil || reservation.TerminalAt != nil ||
			reservation.TerminalReasonCode != "" || actualPresent || !requestEvidenceValid || reservation.ResponseEvidenceID != "" {
			return errLaunchReservationIntegrity
		}
	case launchReservationSettled, launchReservationBreached:
		if reservation.RequestDeadlineAt == nil || reservation.DispatchedAt == nil || reservation.TerminalAt == nil ||
			!actualPresent || !requestEvidenceValid || !responseEvidenceValid {
			return errLaunchReservationIntegrity
		}
		if reservation.State == launchReservationSettled && reservation.TerminalReasonCode != "" {
			return errLaunchReservationIntegrity
		}
		if reservation.State == launchReservationBreached && reservation.TerminalReasonCode != launchReasonUsageOverrun {
			return errLaunchReservationIntegrity
		}
	case launchReservationReleased:
		if reservation.RequestDeadlineAt != nil || reservation.DispatchedAt != nil || reservation.TerminalAt == nil ||
			reservation.TerminalReasonCode == "" || actualPresent || reservation.RequestEvidenceID != "" || reservation.ResponseEvidenceID != "" {
			return errLaunchReservationIntegrity
		}
	case launchReservationAmbiguous:
		if reservation.RequestDeadlineAt == nil || reservation.DispatchedAt == nil || reservation.TerminalAt == nil ||
			reservation.TerminalReasonCode == "" || actualPresent || !requestEvidenceValid {
			return errLaunchReservationIntegrity
		}
		if reservation.ResponseEvidenceID != "" && !responseEvidenceValid {
			return errLaunchReservationIntegrity
		}
	default:
		return errLaunchReservationIntegrity
	}
	return nil
}

func materializeExpiredLaunchReservations(ctx context.Context, conn *launchForeignKeyConn, now time.Time) error {
	if _, err := countBoundedActiveLaunchReservations(ctx, conn, "", launchcontract.GlobalCapacity); err != nil {
		return err
	}
	rows, err := conn.QueryContext(ctx, `
		SELECT run_id, session_id, task_id, turn_id, step_id, kind,
		       idempotency_key, input_digest, wire_request_digest, profile_digest,
		       envelope_digest, step_attempt, step_claim_generation,
		       lease_generation, state, reserved_input_tokens,
		       reserved_output_tokens, reserved_total_tokens,
		       actual_input_tokens, actual_output_tokens, actual_total_tokens,
		       reserved_at, lease_expires_at, request_deadline_at, dispatched_at,
		       request_evidence_id, request_evidence_digest,
		       response_evidence_id, output_digest, terminal_reason_code, terminal_at
		FROM launch_model_reservations
		WHERE state IN ('reserved','dispatched') AND lease_expires_at <= ?
		ORDER BY lease_expires_at, run_id, step_id
		LIMIT ?
	`, sqliteLeaseTimestamp(now), launchcontract.GlobalCapacity)
	if err != nil {
		return fmt.Errorf("runledger: list expired launch reservations: %w", err)
	}
	var expired []launchModelReservation
	for rows.Next() {
		reservation, scanErr := scanLaunchModelReservation(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		expired = append(expired, reservation)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("runledger: iterate expired launch reservations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("runledger: close expired launch reservations: %w", err)
	}
	for _, reservation := range expired {
		account, err := readAndValidateLaunchBudgetAccount(ctx, conn, reservation.RunID)
		if err != nil {
			return err
		}
		window, err := readLaunchTurnWindow(ctx, conn, reservation.RunID, reservation.TurnID)
		if err != nil {
			return err
		}
		if err := validateLaunchTurnWindow(window, account); err != nil {
			return err
		}
		if err := validateLaunchReservationAuthorityBounds(reservation, account, window); err != nil {
			return err
		}
		step, err := readLaunchExecutionStep(ctx, conn, reservation.RunID, reservation.StepID)
		if err != nil {
			return err
		}
		if err := validateCurrentLaunchReservationStepFence(step, reservation); err != nil {
			return err
		}
		if step.Status != StepStarted || step.Attempt != reservation.Attempt || step.ClaimGeneration != reservation.ClaimGeneration {
			return fmt.Errorf("%w: expired reservation step fence", errLaunchReservationIntegrity)
		}
		reason := launchReasonPrepareExpired
		stepStatus := StepFailed
		wantDispatch := StepDispatchClaimed
		newState := launchReservationReleased
		if reservation.State == launchReservationDispatched {
			reason = launchReasonRequestExpired
			stepStatus = StepBlocked
			wantDispatch = StepDispatchDispatched
			newState = launchReservationAmbiguous
		}
		if step.DispatchState != wantDispatch {
			return fmt.Errorf("%w: expired reservation dispatch fence", errLaunchReservationIntegrity)
		}
		result, err := conn.ExecContext(ctx, `
			UPDATE launch_model_reservations
			SET state = ?, terminal_reason_code = ?, terminal_at = ?
			WHERE run_id = ? AND step_id = ? AND step_attempt = ?
			  AND step_claim_generation = ? AND lease_generation = ? AND state = ?
		`, newState, reason, sqliteTimestamp(now), reservation.RunID,
			reservation.StepID, reservation.Attempt, reservation.ClaimGeneration,
			reservation.LeaseGeneration, reservation.State)
		if err != nil {
			return fmt.Errorf("runledger: expire launch reservation: %w", err)
		}
		if err := requireOneLaunchRow(result, "expire launch reservation"); err != nil {
			return err
		}
		result, err = conn.ExecContext(ctx, `
			UPDATE execution_steps
			SET status = ?, error_text = ?, completed_at = ?
			WHERE run_id = ? AND step_id = ? AND status = ? AND attempt = ?
			  AND claim_generation = ? AND dispatch_state = ?
		`, stepStatus, reason, sqliteTimestamp(now), reservation.RunID,
			reservation.StepID, StepStarted, reservation.Attempt,
			reservation.ClaimGeneration, wantDispatch)
		if err != nil {
			return fmt.Errorf("runledger: terminalize expired launch step: %w", err)
		}
		if err := requireOneLaunchRow(result, "terminalize expired launch step"); err != nil {
			return err
		}
	}
	return nil
}

func createLaunchReservationTables(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS launch_budget_accounts (
			run_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			profile_id TEXT NOT NULL,
			profile_version TEXT NOT NULL,
			profile_digest TEXT NOT NULL,
			envelope_digest TEXT NOT NULL,
			model_requests_limit INTEGER NOT NULL,
			input_tokens_limit INTEGER NOT NULL,
			output_tokens_limit INTEGER NOT NULL,
			total_tokens_limit INTEGER NOT NULL,
			max_output_per_request INTEGER NOT NULL,
			request_timeout_ms INTEGER NOT NULL,
			turn_timeout_ms INTEGER NOT NULL,
			absolute_run_timeout_ms INTEGER NOT NULL,
			global_capacity INTEGER NOT NULL,
			per_run_parallelism INTEGER NOT NULL,
			provider_post_attempts INTEGER NOT NULL,
			manager_affordability_attempts INTEGER NOT NULL,
			retry_owner TEXT NOT NULL,
			started_at TIMESTAMP NOT NULL,
			run_deadline_at TIMESTAMP NOT NULL,
			CHECK (model_requests_limit > 0),
			CHECK (input_tokens_limit > 0),
			CHECK (output_tokens_limit > 0),
			CHECK (total_tokens_limit > 0),
			CHECK (max_output_per_request > 0),
			CHECK (request_timeout_ms > 0),
			CHECK (turn_timeout_ms > 0),
			CHECK (absolute_run_timeout_ms > 0),
			CHECK (global_capacity > 0),
			CHECK (per_run_parallelism > 0),
			CHECK (provider_post_attempts = 1),
			CHECK (manager_affordability_attempts = 1),
			CHECK (retry_owner = 'dapr'),
			UNIQUE (run_id, session_id),
			FOREIGN KEY(run_id, session_id)
				REFERENCES launch_envelopes(run_id, session_id) ON DELETE CASCADE
		);
		CREATE TABLE IF NOT EXISTS launch_turn_windows (
			run_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			profile_digest TEXT NOT NULL,
			envelope_digest TEXT NOT NULL,
			started_at TIMESTAMP NOT NULL,
			deadline_at TIMESTAMP NOT NULL,
			PRIMARY KEY(run_id, turn_id),
			UNIQUE(run_id, session_id, turn_id),
			FOREIGN KEY(run_id, session_id)
				REFERENCES launch_budget_accounts(run_id, session_id) ON DELETE CASCADE
		);
		CREATE TABLE IF NOT EXISTS launch_model_reservations (
			run_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			task_id TEXT NOT NULL,
			turn_id TEXT NOT NULL,
			step_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			input_digest TEXT NOT NULL,
			wire_request_digest TEXT NOT NULL,
			profile_digest TEXT NOT NULL,
			envelope_digest TEXT NOT NULL,
			step_attempt INTEGER NOT NULL,
			step_claim_generation INTEGER NOT NULL,
			lease_generation INTEGER NOT NULL,
			state TEXT NOT NULL,
			reserved_input_tokens INTEGER NOT NULL,
			reserved_output_tokens INTEGER NOT NULL,
			reserved_total_tokens INTEGER NOT NULL,
			actual_input_tokens INTEGER,
			actual_output_tokens INTEGER,
			actual_total_tokens INTEGER,
			reserved_at TIMESTAMP NOT NULL,
			lease_expires_at TIMESTAMP NOT NULL,
			request_deadline_at TIMESTAMP,
			dispatched_at TIMESTAMP,
			request_evidence_id TEXT,
			request_evidence_digest TEXT,
			response_evidence_id TEXT,
			output_digest TEXT,
			terminal_reason_code TEXT,
			terminal_at TIMESTAMP,
			PRIMARY KEY(run_id, step_id, step_attempt),
			CHECK (step_attempt > 0),
			CHECK (step_claim_generation > 0),
			CHECK (lease_generation > 0),
			CHECK (state IN ('reserved','dispatched','settled','released','ambiguous','breached')),
			CHECK (reserved_input_tokens >= 0),
			CHECK (reserved_output_tokens > 0),
			CHECK (reserved_total_tokens > 0),
			CHECK ((actual_input_tokens IS NULL) = (actual_output_tokens IS NULL)),
			CHECK ((actual_input_tokens IS NULL) = (actual_total_tokens IS NULL)),
			UNIQUE(run_id, session_id, step_id, step_attempt),
			FOREIGN KEY(run_id, session_id)
				REFERENCES launch_budget_accounts(run_id, session_id) ON DELETE CASCADE,
			FOREIGN KEY(run_id, session_id, turn_id)
				REFERENCES launch_turn_windows(run_id, session_id, turn_id) ON DELETE CASCADE,
			FOREIGN KEY(run_id, step_id)
				REFERENCES execution_steps(run_id, step_id) ON DELETE CASCADE
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_launch_model_reservations_active_step
			ON launch_model_reservations(run_id, step_id)
			WHERE state IN ('reserved','dispatched');
		CREATE INDEX IF NOT EXISTS idx_launch_model_reservations_active_global
			ON launch_model_reservations(state, lease_expires_at, run_id, step_id)
			WHERE state IN ('reserved','dispatched');
		CREATE INDEX IF NOT EXISTS idx_launch_model_reservations_run_budget
			ON launch_model_reservations(run_id, state, step_attempt);
		CREATE TRIGGER IF NOT EXISTS trg_launch_budget_accounts_immutable
		BEFORE UPDATE ON launch_budget_accounts
		BEGIN
			SELECT RAISE(ABORT, 'launch budget account is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_agent_runs_coupled_replace
		BEFORE INSERT ON agent_runs
		WHEN EXISTS (SELECT 1 FROM agent_runs WHERE run_id = NEW.run_id)
		 AND EXISTS (SELECT 1 FROM launch_envelopes WHERE run_id = NEW.run_id)
		BEGIN
			SELECT RAISE(ABORT, 'launch run is coupled');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_agent_runs_coupled_update_replace
		BEFORE UPDATE OF run_id, session_id ON agent_runs
		WHEN (OLD.run_id IS NOT NEW.run_id OR OLD.session_id IS NOT NEW.session_id)
		 AND (
			EXISTS (SELECT 1 FROM launch_envelopes WHERE run_id = OLD.run_id) OR
			EXISTS (SELECT 1 FROM launch_envelopes WHERE run_id = NEW.run_id)
		 )
		BEGIN
			SELECT RAISE(ABORT, 'launch run identity is coupled');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_run_contracts_coupled_replace
		BEFORE INSERT ON agent_run_contracts
		WHEN EXISTS (SELECT 1 FROM agent_run_contracts WHERE run_id = NEW.run_id)
		 AND EXISTS (SELECT 1 FROM launch_envelopes WHERE run_id = NEW.run_id)
		BEGIN
			SELECT RAISE(ABORT, 'launch run contract is coupled');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_run_contracts_coupled_update_replace
		BEFORE UPDATE OF run_id, session_id ON agent_run_contracts
		WHEN (OLD.run_id IS NOT NEW.run_id OR OLD.session_id IS NOT NEW.session_id)
		 AND (
			EXISTS (SELECT 1 FROM launch_envelopes WHERE run_id = OLD.run_id) OR
			EXISTS (SELECT 1 FROM launch_envelopes WHERE run_id = NEW.run_id)
		 )
		BEGIN
			SELECT RAISE(ABORT, 'launch run contract identity is coupled');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_envelopes_duplicate_immutable
		BEFORE INSERT ON launch_envelopes
		WHEN EXISTS (SELECT 1 FROM launch_envelopes WHERE run_id = NEW.run_id)
		BEGIN
			SELECT RAISE(ABORT, 'launch envelope is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_budget_accounts_duplicate_immutable
		BEFORE INSERT ON launch_budget_accounts
		WHEN EXISTS (SELECT 1 FROM launch_budget_accounts WHERE run_id = NEW.run_id)
		BEGIN
			SELECT RAISE(ABORT, 'launch budget account is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_budget_accounts_delete_immutable
		BEFORE DELETE ON launch_budget_accounts
		WHEN EXISTS (
			SELECT 1 FROM launch_envelopes
			WHERE run_id = OLD.run_id AND session_id = OLD.session_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch budget account is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_turn_windows_immutable
		BEFORE UPDATE ON launch_turn_windows
		BEGIN
			SELECT RAISE(ABORT, 'launch turn window is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_turn_windows_duplicate_immutable
		BEFORE INSERT ON launch_turn_windows
		WHEN EXISTS (
			SELECT 1 FROM launch_turn_windows
			WHERE run_id = NEW.run_id AND turn_id = NEW.turn_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch turn window is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_turn_windows_delete_immutable
		BEFORE DELETE ON launch_turn_windows
		WHEN EXISTS (
			SELECT 1 FROM launch_budget_accounts
			WHERE run_id = OLD.run_id AND session_id = OLD.session_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch turn window is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_model_reservations_identity_immutable
		BEFORE UPDATE OF run_id, session_id, task_id, turn_id, step_id, kind,
			idempotency_key, input_digest, wire_request_digest, profile_digest, envelope_digest,
			step_attempt, step_claim_generation, lease_generation,
			reserved_input_tokens, reserved_output_tokens,
			reserved_total_tokens, reserved_at
		ON launch_model_reservations
		BEGIN
			SELECT RAISE(ABORT, 'launch model reservation identity is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_model_reservations_duplicate_immutable
		BEFORE INSERT ON launch_model_reservations
		WHEN EXISTS (
			SELECT 1 FROM launch_model_reservations
			WHERE run_id = NEW.run_id AND step_id = NEW.step_id
			  AND step_attempt = NEW.step_attempt
		) OR (
			NEW.state IN ('reserved','dispatched') AND EXISTS (
				SELECT 1 FROM launch_model_reservations
				WHERE run_id = NEW.run_id AND step_id = NEW.step_id
				  AND state IN ('reserved','dispatched')
			)
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch model reservation is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_model_reservations_transition
		BEFORE UPDATE OF state ON launch_model_reservations
		WHEN NOT (
			(OLD.state = 'reserved' AND NEW.state IN ('dispatched','released')) OR
			(OLD.state = 'dispatched' AND NEW.state IN ('settled','ambiguous','breached'))
		)
		BEGIN
			SELECT RAISE(ABORT, 'invalid launch model reservation transition');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_model_reservations_same_state_immutable
		BEFORE UPDATE ON launch_model_reservations
		WHEN OLD.state = NEW.state
		BEGIN
			SELECT RAISE(ABORT, 'launch model reservation state is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_model_reservations_delete_immutable
		BEFORE DELETE ON launch_model_reservations
		WHEN EXISTS (
			SELECT 1 FROM launch_budget_accounts
			WHERE run_id = OLD.run_id AND session_id = OLD.session_id
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch model reservation is immutable');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_execution_steps_coupled_update
		BEFORE UPDATE ON execution_steps
		WHEN EXISTS (
			SELECT 1 FROM launch_model_reservations
			WHERE run_id = OLD.run_id AND step_id = OLD.step_id
		) AND NOT EXISTS (
			SELECT 1 FROM launch_model_reservations AS reservation
			WHERE reservation.run_id = NEW.run_id AND reservation.step_id = NEW.step_id
			  AND reservation.task_id IS NEW.task_id
			  AND reservation.kind = NEW.kind
			  AND reservation.idempotency_key = NEW.idempotency_key
			  AND reservation.input_digest IS NEW.input_digest
			  AND reservation.step_attempt = NEW.attempt
			  AND reservation.step_claim_generation = NEW.claim_generation
			  AND reservation.reserved_at = NEW.started_at
			  AND (
				(reservation.state = 'reserved' AND NEW.status = 'started'
				 AND NEW.dispatch_state = 'claimed' AND NEW.completed_at IS NULL
				 AND NEW.output_evidence_id IS NULL AND NEW.output_digest IS NULL AND NEW.error_text IS NULL) OR
				(reservation.state = 'dispatched' AND NEW.status = 'started'
				 AND NEW.dispatch_state = 'dispatched' AND NEW.completed_at IS NULL
				 AND NEW.output_evidence_id IS NULL AND NEW.output_digest IS NULL AND NEW.error_text IS NULL) OR
				(reservation.state IN ('settled','breached') AND NEW.status = 'completed'
				 AND NEW.dispatch_state = 'dispatched' AND NEW.completed_at IS reservation.terminal_at
				 AND NEW.output_evidence_id IS reservation.response_evidence_id
				 AND NEW.output_digest IS reservation.output_digest AND NEW.error_text IS NULL) OR
				(reservation.state = 'ambiguous' AND NEW.status = 'blocked'
				 AND NEW.dispatch_state = 'dispatched' AND NEW.completed_at IS reservation.terminal_at
				 AND NEW.output_evidence_id IS reservation.response_evidence_id
				 AND NEW.output_digest IS reservation.output_digest
				 AND NEW.error_text IS reservation.terminal_reason_code) OR
				(reservation.state = 'released' AND NEW.status = 'failed'
				 AND NEW.dispatch_state = 'claimed' AND NEW.completed_at IS reservation.terminal_at
				 AND NEW.output_evidence_id IS NULL AND NEW.output_digest IS NULL
				 AND NEW.error_text IS reservation.terminal_reason_code)
			  )
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch execution step mutation is not paired');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_execution_steps_coupled_update_replace
		BEFORE UPDATE OF run_id, step_id, idempotency_key ON execution_steps
		WHEN EXISTS (
			SELECT 1
			FROM launch_model_reservations AS reservation
			JOIN execution_steps AS existing
			  ON existing.run_id = reservation.run_id AND existing.step_id = reservation.step_id
			WHERE existing.run_id = NEW.run_id
			  AND (existing.step_id = NEW.step_id OR existing.idempotency_key = NEW.idempotency_key)
			  AND NOT (existing.run_id = OLD.run_id AND existing.step_id = OLD.step_id)
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch execution step replacement target is coupled');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_execution_steps_coupled_delete
		BEFORE DELETE ON execution_steps
		WHEN EXISTS (
			SELECT 1 FROM launch_model_reservations
			WHERE run_id = OLD.run_id AND step_id = OLD.step_id
		) AND EXISTS (SELECT 1 FROM agent_runs WHERE run_id = OLD.run_id)
		BEGIN
			SELECT RAISE(ABORT, 'launch execution step is coupled');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_launch_execution_steps_coupled_replace
		BEFORE INSERT ON execution_steps
		WHEN EXISTS (
			SELECT 1
			FROM launch_model_reservations AS reservation
			JOIN execution_steps AS existing
			  ON existing.run_id = reservation.run_id AND existing.step_id = reservation.step_id
			WHERE existing.run_id = NEW.run_id
			  AND (existing.step_id = NEW.step_id OR existing.idempotency_key = NEW.idempotency_key)
		)
		BEGIN
			SELECT RAISE(ABORT, 'launch execution step is coupled');
		END;
	`)
	if err != nil {
		return fmt.Errorf("create launch reservation tables: %w", err)
	}
	return nil
}
