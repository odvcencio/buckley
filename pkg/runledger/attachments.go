package runledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"m31labs.dev/buckley/pkg/agentcoord"
)

const (
	AttachmentDefaultLease = 30 * time.Second
	AttachmentMaxLease     = 24 * time.Hour
	AttachmentMaxID        = 256
)

var (
	ErrAttachmentNotFound   = errors.New("runledger: attachment not found")
	ErrAttachmentExpired    = errors.New("runledger: attachment lease expired")
	ErrAttachmentStale      = errors.New("runledger: attachment generation is stale")
	ErrAttachmentOwner      = errors.New("runledger: attachment owner mismatch")
	ErrAttachmentConflict   = errors.New("runledger: attachment identity conflicts")
	ErrAttachmentRunMissing = errors.New("runledger: attachment run does not exist")
	ErrAttachmentSession    = errors.New("runledger: attachment session mismatch")
	ErrAttachmentTerminal   = errors.New("runledger: attachment run is terminal")
)

var _ agentcoord.AttachmentStore = (*SQLiteStore)(nil)

// Attach allocates a new process attempt and strictly increasing generation.
// A caller-provided AttemptID is idempotent while its lease remains current;
// an expired or detached attempt can never be resurrected under the same ID.
func (s *SQLiteStore) Attach(ctx context.Context, request agentcoord.AttachmentRequest) (agentcoord.AttachmentLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var lease agentcoord.AttachmentLease
	err := retryMailboxBusy(ctx, func() error {
		var attachErr error
		lease, attachErr = s.attachOnce(ctx, request)
		return attachErr
	})
	return lease, err
}

func (s *SQLiteStore) attachOnce(ctx context.Context, request agentcoord.AttachmentRequest) (agentcoord.AttachmentLease, error) {
	if s == nil || s.db == nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: attachment store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.RunID = strings.TrimSpace(request.RunID)
	request.ParentRunID = strings.TrimSpace(request.ParentRunID)
	request.TaskID = strings.TrimSpace(request.TaskID)
	request.TurnID = strings.TrimSpace(request.TurnID)
	request.AttemptID = strings.TrimSpace(request.AttemptID)
	if request.SessionID == "" || request.RunID == "" {
		return agentcoord.AttachmentLease{}, fmt.Errorf("%w: session_id and run_id are required", ErrAttachmentConflict)
	}
	if len(request.SessionID) > AttachmentMaxID || len(request.RunID) > AttachmentMaxID || len(request.ParentRunID) > AttachmentMaxID || len(request.TaskID) > AttachmentMaxID || len(request.TurnID) > AttachmentMaxID || len(request.AttemptID) > AttachmentMaxID {
		return agentcoord.AttachmentLease{}, fmt.Errorf("%w: attachment identity exceeds %d bytes", ErrAttachmentConflict, AttachmentMaxID)
	}
	duration := request.LeaseDuration
	if duration < 0 {
		return agentcoord.AttachmentLease{}, fmt.Errorf("%w: lease_duration cannot be negative", ErrAttachmentConflict)
	}
	if duration == 0 {
		duration = AttachmentDefaultLease
	}
	if duration > AttachmentMaxLease {
		duration = AttachmentMaxLease
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: begin attachment: %w", err)
	}
	defer tx.Rollback()
	if err := validateAttachableRun(ctx, tx, request); err != nil {
		return agentcoord.AttachmentLease{}, err
	}
	now, err := sqliteDBTime(ctx, tx)
	if err != nil {
		return agentcoord.AttachmentLease{}, err
	}
	// Expiration is materialized before generation allocation. It makes the
	// current-row invariant true even when no recovery loop has run recently.
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_run_attempts SET state = ?
		WHERE session_id = ? AND run_id = ? AND state = ? AND lease_expires_at <= ?
	`, agentcoord.AttachmentExpired, request.SessionID, request.RunID, agentcoord.AttachmentAttached, sqliteLeaseTimestamp(now)); err != nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: expire attachments: %w", err)
	}
	// A live lease is already the elected owner. Concurrent recovery callers
	// converge on it instead of creating overlapping active processes; a
	// replacement is permitted only after expiry or explicit detach.
	current, currentErr := scanAttachment(tx.QueryRowContext(ctx, `
		SELECT attempt_id, session_id, run_id, parent_run_id, task_id, turn_id,
			lease_generation, pid, state, attached_at, heartbeat_at,
			lease_expires_at, detached_at
		FROM agent_run_attempts
		WHERE session_id = ? AND run_id = ? AND state = ? AND lease_expires_at > ?
		ORDER BY lease_generation DESC LIMIT 1
	`, request.SessionID, request.RunID, agentcoord.AttachmentAttached, sqliteLeaseTimestamp(now)))
	if currentErr == nil {
		if request.AttemptID == "" || request.AttemptID == current.AttemptID {
			if err := tx.Commit(); err != nil {
				return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: commit elected attachment: %w", err)
			}
			return current, nil
		}
		return agentcoord.AttachmentLease{}, fmt.Errorf("%w: current attempt %s is still attached", ErrAttachmentConflict, current.AttemptID)
	}
	if !errors.Is(currentErr, sql.ErrNoRows) {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: inspect current attachment: %w", currentErr)
	}

	if request.AttemptID != "" {
		lease, lookupErr := scanAttachment(tx.QueryRowContext(ctx, `
			SELECT attempt_id, session_id, run_id, parent_run_id, task_id, turn_id,
				lease_generation, pid, state, attached_at, heartbeat_at,
				lease_expires_at, detached_at
			FROM agent_run_attempts WHERE session_id = ? AND run_id = ? AND attempt_id = ?
		`, request.SessionID, request.RunID, request.AttemptID))
		if lookupErr == nil {
			if lease.State == agentcoord.AttachmentAttached && lease.LeaseExpiresAt.After(now) {
				if err := tx.Commit(); err != nil {
					return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: commit idempotent attachment: %w", err)
				}
				return lease, nil
			}
			return agentcoord.AttachmentLease{}, fmt.Errorf("%w: attempt_id %s already used", ErrAttachmentConflict, request.AttemptID)
		}
		if !errors.Is(lookupErr, sql.ErrNoRows) {
			return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: inspect attachment identity: %w", lookupErr)
		}
	}
	if request.AttemptID == "" {
		request.AttemptID = "attempt_" + ulid.Make().String()
	}
	var generation int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(lease_generation), 0) + 1
		FROM agent_run_attempts WHERE session_id = ? AND run_id = ?
	`, request.SessionID, request.RunID).Scan(&generation); err != nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: allocate attachment generation: %w", err)
	}
	expires := now.Add(duration)
	lease := agentcoord.AttachmentLease{
		SessionID:       request.SessionID,
		RunID:           request.RunID,
		ParentRunID:     request.ParentRunID,
		TaskID:          request.TaskID,
		TurnID:          request.TurnID,
		AttemptID:       request.AttemptID,
		LeaseGeneration: generation,
		PID:             request.PID,
		State:           agentcoord.AttachmentAttached,
		AttachedAt:      now,
		HeartbeatAt:     now,
		LeaseExpiresAt:  expires,
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO agent_run_attempts (
			attempt_id, session_id, run_id, parent_run_id, task_id, turn_id,
			lease_generation, pid, state, attached_at, heartbeat_at, lease_expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, lease.AttemptID, lease.SessionID, lease.RunID, nullableStr(lease.ParentRunID), nullableStr(lease.TaskID), nullableStr(lease.TurnID), lease.LeaseGeneration, nullableInt(lease.PID), lease.State, sqliteTimestamp(lease.AttachedAt), sqliteTimestamp(lease.HeartbeatAt), sqliteLeaseTimestamp(lease.LeaseExpiresAt))
	if err != nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: insert attachment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: commit attachment: %w", err)
	}
	return lease, nil
}

// Current returns the highest-generation non-expired attachment for a logical
// run. Older attached rows are historical but are never current owners.
func (s *SQLiteStore) Current(ctx context.Context, sessionID, runID string) (agentcoord.AttachmentLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var lease agentcoord.AttachmentLease
	err := retryMailboxBusy(ctx, func() error {
		var currentErr error
		lease, currentErr = s.currentOnce(ctx, sessionID, runID)
		return currentErr
	})
	return lease, err
}

func (s *SQLiteStore) currentOnce(ctx context.Context, sessionID, runID string) (agentcoord.AttachmentLease, error) {
	if s == nil || s.db == nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: attachment store is unavailable")
	}
	sessionID, runID = strings.TrimSpace(sessionID), strings.TrimSpace(runID)
	if sessionID == "" || runID == "" {
		return agentcoord.AttachmentLease{}, fmt.Errorf("%w: session_id and run_id are required", ErrAttachmentConflict)
	}
	if err := validateAttachmentIdentifiers(sessionID, runID); err != nil {
		return agentcoord.AttachmentLease{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: begin current attachment: %w", err)
	}
	defer tx.Rollback()
	now, err := sqliteDBTime(ctx, tx)
	if err != nil {
		return agentcoord.AttachmentLease{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_run_attempts SET state = ?
		WHERE session_id = ? AND run_id = ? AND state = ? AND lease_expires_at <= ?
	`, agentcoord.AttachmentExpired, sessionID, runID, agentcoord.AttachmentAttached, sqliteLeaseTimestamp(now)); err != nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: expire current attachment: %w", err)
	}
	lease, err := scanAttachment(tx.QueryRowContext(ctx, `
		SELECT attempt_id, session_id, run_id, parent_run_id, task_id, turn_id,
			lease_generation, pid, state, attached_at, heartbeat_at,
			lease_expires_at, detached_at
		FROM agent_run_attempts
		WHERE session_id = ? AND run_id = ? AND state = ? AND lease_expires_at > ?
		ORDER BY lease_generation DESC LIMIT 1
	`, sessionID, runID, agentcoord.AttachmentAttached, sqliteLeaseTimestamp(now)))
	if errors.Is(err, sql.ErrNoRows) {
		var state string
		stateErr := tx.QueryRowContext(ctx, `
			SELECT state FROM agent_run_attempts WHERE session_id = ? AND run_id = ?
			ORDER BY lease_generation DESC LIMIT 1
		`, sessionID, runID).Scan(&state)
		if stateErr == nil && state == agentcoord.AttachmentExpired {
			if err := tx.Commit(); err != nil {
				return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: commit expired attachment: %w", err)
			}
			return agentcoord.AttachmentLease{}, ErrAttachmentExpired
		}
		return agentcoord.AttachmentLease{}, ErrAttachmentNotFound
	}
	if err != nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: read current attachment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: commit current attachment: %w", err)
	}
	return lease, nil
}

// Heartbeat renews only the exact current attempt and generation.
func (s *SQLiteStore) Heartbeat(ctx context.Context, request agentcoord.AttachmentHeartbeatRequest) (agentcoord.AttachmentLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var lease agentcoord.AttachmentLease
	err := retryMailboxBusy(ctx, func() error {
		var heartbeatErr error
		lease, heartbeatErr = s.heartbeatOnce(ctx, request)
		return heartbeatErr
	})
	if normalized, canceled := normalizeAttachmentHeartbeatContextError(ctx, err); canceled {
		return agentcoord.AttachmentLease{}, normalized
	}
	return lease, err
}

func normalizeAttachmentHeartbeatContextError(ctx context.Context, err error) (error, bool) {
	if err == nil || ctx == nil || ctx.Err() == nil || !errors.Is(err, sql.ErrTxDone) {
		return err, false
	}
	if errors.Is(err, ErrAttachmentStale) || errors.Is(err, ErrAttachmentExpired) || errors.Is(err, ErrAttachmentTerminal) {
		return err, false
	}
	return ctx.Err(), true
}

func (s *SQLiteStore) heartbeatOnce(ctx context.Context, request agentcoord.AttachmentHeartbeatRequest) (agentcoord.AttachmentLease, error) {
	if s == nil || s.db == nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: attachment store is unavailable")
	}
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.RunID = strings.TrimSpace(request.RunID)
	request.AttemptID = strings.TrimSpace(request.AttemptID)
	if err := validateAttachmentFence(request.SessionID, request.RunID, request.AttemptID, request.LeaseGeneration); err != nil {
		return agentcoord.AttachmentLease{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: begin attachment heartbeat: %w", err)
	}
	defer tx.Rollback()
	lease, err := s.attachmentForMutation(ctx, tx, request.SessionID, request.RunID, request.AttemptID, request.LeaseGeneration)
	if err != nil {
		return agentcoord.AttachmentLease{}, err
	}
	now, err := sqliteDBTime(ctx, tx)
	if err != nil {
		return agentcoord.AttachmentLease{}, err
	}
	if err := validateCurrentAttachment(tx, lease, now); err != nil {
		return agentcoord.AttachmentLease{}, err
	}
	if err := validateActiveRun(ctx, tx, request.SessionID, request.RunID); err != nil {
		return agentcoord.AttachmentLease{}, err
	}
	duration := request.LeaseDuration
	if duration < 0 {
		return agentcoord.AttachmentLease{}, fmt.Errorf("%w: lease_duration cannot be negative", ErrAttachmentConflict)
	}
	if duration == 0 {
		duration = AttachmentDefaultLease
	}
	if duration > AttachmentMaxLease {
		duration = AttachmentMaxLease
	}
	lease.HeartbeatAt = now
	lease.LeaseExpiresAt = now.Add(duration)
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_run_attempts SET heartbeat_at = ?, lease_expires_at = ?
		WHERE session_id = ? AND run_id = ? AND attempt_id = ? AND lease_generation = ? AND state = ?
	`, sqliteTimestamp(lease.HeartbeatAt), sqliteLeaseTimestamp(lease.LeaseExpiresAt), lease.SessionID, lease.RunID, lease.AttemptID, lease.LeaseGeneration, agentcoord.AttachmentAttached); err != nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: heartbeat attachment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: commit attachment heartbeat: %w", err)
	}
	return lease, nil
}

// Detach closes only the exact current attempt and generation.
func (s *SQLiteStore) Detach(ctx context.Context, request agentcoord.AttachmentDetachRequest) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("runledger: attachment store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.RunID = strings.TrimSpace(request.RunID)
	request.AttemptID = strings.TrimSpace(request.AttemptID)
	if err := validateAttachmentFence(request.SessionID, request.RunID, request.AttemptID, request.LeaseGeneration); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("runledger: begin attachment detach: %w", err)
	}
	defer tx.Rollback()
	lease, err := s.attachmentForMutation(ctx, tx, strings.TrimSpace(request.SessionID), strings.TrimSpace(request.RunID), strings.TrimSpace(request.AttemptID), request.LeaseGeneration)
	if err != nil {
		return err
	}
	now, err := sqliteDBTime(ctx, tx)
	if err != nil {
		return err
	}
	if lease.State == agentcoord.AttachmentDetached {
		if err := validateAttachmentGeneration(tx, lease); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := validateCurrentAttachment(tx, lease, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_run_attempts SET state = ?, detached_at = ?, detach_reason = ?
		WHERE session_id = ? AND run_id = ? AND attempt_id = ? AND lease_generation = ? AND state = ?
	`, agentcoord.AttachmentDetached, sqliteTimestamp(now), boundedMailboxText(request.Reason, MailboxMaxError), lease.SessionID, lease.RunID, lease.AttemptID, lease.LeaseGeneration, agentcoord.AttachmentAttached); err != nil {
		return fmt.Errorf("runledger: detach attachment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("runledger: commit attachment detach: %w", err)
	}
	return nil
}

// ValidateAttachment is useful to coordinator adapters that must fence a
// lifecycle callback before writing a terminal run transition.
func (s *SQLiteStore) ValidateAttachment(ctx context.Context, lease agentcoord.AttachmentLease) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("runledger: attachment store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateAttachmentFence(strings.TrimSpace(lease.SessionID), strings.TrimSpace(lease.RunID), strings.TrimSpace(lease.AttemptID), lease.LeaseGeneration); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("runledger: begin attachment validation: %w", err)
	}
	defer tx.Rollback()
	stored, err := s.attachmentForMutation(ctx, tx, lease.SessionID, lease.RunID, lease.AttemptID, lease.LeaseGeneration)
	if err != nil {
		return err
	}
	now, err := sqliteDBTime(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateCurrentAttachment(tx, stored, now); err != nil {
		return err
	}
	return tx.Commit()
}

// EndRunFenced is retained as a compatibility wrapper. New coordinators use
// FinalizeRunAttempt directly so claim cleanup and their full terminal audit
// event set share the transaction.
func (s *SQLiteStore) EndRunFenced(ctx context.Context, runID, status string, endedAt time.Time, outcome map[string]any, sessionID, attemptID string, generation int64) error {
	runID, sessionID, attemptID = strings.TrimSpace(runID), strings.TrimSpace(sessionID), strings.TrimSpace(attemptID)
	if runID == "" || sessionID == "" || attemptID == "" || generation <= 0 {
		return fmt.Errorf("%w: exact run attachment is required", ErrAttachmentStale)
	}
	status = strings.TrimSpace(status)
	if status == "" {
		return fmt.Errorf("runledger: status is required")
	}
	eventType := EventSubagentFailed
	if status == string(agentcoord.RunCompleted) {
		eventType = EventSubagentCompleted
	} else if status == string(agentcoord.RunCancelled) {
		eventType = EventSubagentCancelled
	}
	return s.FinalizeRunAttempt(ctx, AttemptFinalization{
		SessionID:       sessionID,
		RunID:           runID,
		AttemptID:       attemptID,
		LeaseGeneration: generation,
		Status:          status,
		EndedAt:         endedAt,
		Outcome:         outcome,
		ReleaseReason:   "run terminal",
		Events: []Event{{
			ID:        StableEventID("runledger.end_run_fenced", runID, attemptID, fmt.Sprint(generation), status),
			Type:      eventType,
			SessionID: sessionID,
			RunID:     runID,
			Payload:   map[string]any{"state": status},
		}},
	})
}

func (s *SQLiteStore) AttachRun(ctx context.Context, request agentcoord.AttachmentRequest) (agentcoord.AttachmentLease, error) {
	return s.Attach(ctx, request)
}

func (s *SQLiteStore) CurrentAttachment(ctx context.Context, sessionID, runID string) (agentcoord.AttachmentLease, error) {
	return s.Current(ctx, sessionID, runID)
}

func (s *SQLiteStore) HeartbeatAttachment(ctx context.Context, request agentcoord.AttachmentHeartbeatRequest) (agentcoord.AttachmentLease, error) {
	return s.Heartbeat(ctx, request)
}

func (s *SQLiteStore) DetachAttachment(ctx context.Context, request agentcoord.AttachmentDetachRequest) error {
	return s.Detach(ctx, request)
}

func (s *SQLiteStore) attachmentForMutation(ctx context.Context, tx *sql.Tx, sessionID, runID, attemptID string, generation int64) (agentcoord.AttachmentLease, error) {
	if err := validateAttachmentFence(strings.TrimSpace(sessionID), strings.TrimSpace(runID), strings.TrimSpace(attemptID), generation); err != nil {
		return agentcoord.AttachmentLease{}, err
	}
	lease, err := scanAttachment(tx.QueryRowContext(ctx, `
		SELECT attempt_id, session_id, run_id, parent_run_id, task_id, turn_id,
			lease_generation, pid, state, attached_at, heartbeat_at,
			lease_expires_at, detached_at
		FROM agent_run_attempts
		WHERE session_id = ? AND run_id = ? AND attempt_id = ? AND lease_generation = ?
	`, sessionID, runID, attemptID, generation))
	if errors.Is(err, sql.ErrNoRows) {
		return agentcoord.AttachmentLease{}, ErrAttachmentStale
	}
	if err != nil {
		return agentcoord.AttachmentLease{}, fmt.Errorf("runledger: read attachment owner: %w", err)
	}
	return lease, nil
}

func validateAttachmentFence(sessionID, runID, attemptID string, generation int64) error {
	if sessionID == "" || runID == "" || attemptID == "" || generation <= 0 {
		return fmt.Errorf("%w: exact session, run, attempt, and positive generation are required", ErrAttachmentStale)
	}
	return validateAttachmentIdentifiers(sessionID, runID, attemptID)
}

func validateAttachmentIdentifiers(values ...string) error {
	for _, value := range values {
		if len(strings.TrimSpace(value)) > AttachmentMaxID {
			return fmt.Errorf("%w: attachment identity exceeds %d bytes", ErrAttachmentConflict, AttachmentMaxID)
		}
	}
	return nil
}

func validateAttachmentGeneration(tx *sql.Tx, lease agentcoord.AttachmentLease) error {
	var current int64
	if err := tx.QueryRow(`
		SELECT COALESCE(MAX(lease_generation), 0) FROM agent_run_attempts
		WHERE session_id = ? AND run_id = ?
	`, lease.SessionID, lease.RunID).Scan(&current); err != nil {
		return fmt.Errorf("runledger: read attachment generation: %w", err)
	}
	if current != lease.LeaseGeneration {
		return ErrAttachmentStale
	}
	return nil
}

func validateCurrentAttachment(tx *sql.Tx, lease agentcoord.AttachmentLease, now time.Time) error {
	if err := validateAttachmentGeneration(tx, lease); err != nil {
		return err
	}
	if lease.State != agentcoord.AttachmentAttached {
		if lease.State == agentcoord.AttachmentExpired {
			return ErrAttachmentExpired
		}
		return ErrAttachmentStale
	}
	if !lease.LeaseExpiresAt.After(now) {
		_, _ = tx.Exec(`UPDATE agent_run_attempts SET state = ? WHERE attempt_id = ? AND state = ?`, agentcoord.AttachmentExpired, lease.AttemptID, agentcoord.AttachmentAttached)
		return ErrAttachmentExpired
	}
	return nil
}

// mailboxAttachmentFence links operational delivery to the same ownership
// table. A fenced mutation is authorized only by the exact current attempt;
// an arbitrary nonzero generation cannot stand in for a missing attachment.
func mailboxAttachmentFence(ctx context.Context, tx *sql.Tx, sessionID, runID, attemptID string, generation int64, now time.Time) error {
	var maximum int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(lease_generation), 0)
		FROM agent_run_attempts WHERE session_id = ? AND run_id = ?
	`, strings.TrimSpace(sessionID), strings.TrimSpace(runID)).Scan(&maximum); err != nil {
		return fmt.Errorf("runledger: read mailbox attachment fence: %w", err)
	}
	if maximum == 0 {
		return ErrAttachmentStale
	}
	if strings.TrimSpace(attemptID) == "" || generation != maximum {
		return ErrAttachmentStale
	}
	var state string
	var expiresRaw string
	if err := tx.QueryRowContext(ctx, `
		SELECT state, lease_expires_at FROM agent_run_attempts
		WHERE session_id = ? AND run_id = ? AND attempt_id = ? AND lease_generation = ?
	`, strings.TrimSpace(sessionID), strings.TrimSpace(runID), strings.TrimSpace(attemptID), generation).Scan(&state, &expiresRaw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrAttachmentStale
		}
		return fmt.Errorf("runledger: inspect mailbox attachment fence: %w", err)
	}
	if state != agentcoord.AttachmentAttached {
		if state == agentcoord.AttachmentExpired {
			return ErrAttachmentExpired
		}
		return ErrAttachmentStale
	}
	if expires := parseSQLiteTimestamp(expiresRaw); !expires.After(now) {
		_, _ = tx.ExecContext(ctx, `UPDATE agent_run_attempts SET state = ? WHERE session_id = ? AND run_id = ? AND attempt_id = ? AND lease_generation = ?`, agentcoord.AttachmentExpired, sessionID, runID, attemptID, generation)
		return ErrAttachmentExpired
	}
	return nil
}

func scanAttachment(scanner mailboxScanner) (agentcoord.AttachmentLease, error) {
	var (
		lease                                  agentcoord.AttachmentLease
		parentRun, task, turn                  sql.NullString
		attached, heartbeat, expires, detached sql.NullString
		pid                                    sql.NullInt64
	)
	if err := scanner.Scan(&lease.AttemptID, &lease.SessionID, &lease.RunID, &parentRun, &task, &turn, &lease.LeaseGeneration, &pid, &lease.State, &attached, &heartbeat, &expires, &detached); err != nil {
		return agentcoord.AttachmentLease{}, err
	}
	lease.ParentRunID = parentRun.String
	lease.TaskID = task.String
	lease.TurnID = turn.String
	lease.PID = int(pid.Int64)
	lease.AttachedAt = parseSQLiteTimestamp(attached.String)
	lease.HeartbeatAt = parseSQLiteTimestamp(heartbeat.String)
	lease.LeaseExpiresAt = parseSQLiteTimestamp(expires.String)
	if detached.Valid {
		value := parseSQLiteTimestamp(detached.String)
		lease.DetachedAt = value
	}
	return lease, nil
}

func validateAttachableRun(ctx context.Context, tx *sql.Tx, request agentcoord.AttachmentRequest) error {
	var (
		sessionID, status            string
		parentRunID, taskID, endedAt sql.NullString
	)
	err := tx.QueryRowContext(ctx, `
		SELECT session_id, parent_run_id, task_id, status, ended_at
		FROM agent_runs WHERE run_id = ?
	`, request.RunID).Scan(&sessionID, &parentRunID, &taskID, &status, &endedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAttachmentRunMissing
	}
	if err != nil {
		return fmt.Errorf("runledger: inspect attachment run: %w", err)
	}
	if sessionID != request.SessionID {
		return ErrAttachmentSession
	}
	if endedAt.Valid || terminalAgentRunStatus(status) {
		return ErrAttachmentTerminal
	}
	if request.ParentRunID != "" && request.ParentRunID != parentRunID.String {
		return fmt.Errorf("%w: parent_run_id changed", ErrAttachmentConflict)
	}
	if request.TaskID != "" && request.TaskID != taskID.String {
		return fmt.Errorf("%w: task_id changed", ErrAttachmentConflict)
	}
	return nil
}

func validateActiveRun(ctx context.Context, tx *sql.Tx, sessionID, runID string) error {
	var storedSession, status string
	var endedAt sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT session_id, status, ended_at FROM agent_runs WHERE run_id = ?`, runID).Scan(&storedSession, &status, &endedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrAttachmentRunMissing
	}
	if err != nil {
		return fmt.Errorf("runledger: inspect active attachment run: %w", err)
	}
	if storedSession != sessionID {
		return ErrAttachmentSession
	}
	if endedAt.Valid || terminalAgentRunStatus(status) {
		return ErrAttachmentTerminal
	}
	return nil
}

func terminalAgentRunStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled", "blocked":
		return true
	default:
		return false
	}
}
