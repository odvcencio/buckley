package runledger

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/storage"
)

const (
	// MailboxDefaultLease is intentionally short enough that a dead local
	// process becomes resumable without operator intervention.
	MailboxDefaultLease    = 30 * time.Second
	MailboxMaxLease        = 24 * time.Hour
	MailboxDefaultLimit    = 256
	MailboxMaxLimit        = 1000
	MailboxMaxStates       = 4
	MailboxMaxPreview      = 512
	MailboxMaxIdentifier   = 256
	MailboxMaxDigest       = 128
	MailboxMaxError        = 1024
	MailboxMaxContent      = 8 * 1024 * 1024
	mailboxMessageIDDomain = "m31.agent.message-id.v1"
)

var (
	ErrMailboxNotFound            = errors.New("runledger: mailbox message not found")
	ErrMailboxIdempotencyConflict = errors.New("runledger: mailbox idempotency digest conflict")
	ErrMailboxClaimUnavailable    = errors.New("runledger: mailbox message is not claimable")
	ErrMailboxClaimOwner          = errors.New("runledger: mailbox claim owner mismatch")
	ErrMailboxLeaseExpired        = errors.New("runledger: mailbox claim lease expired")
	ErrMailboxNotClaimed          = errors.New("runledger: mailbox message is not claimed")
	ErrMailboxAlreadyProcessed    = errors.New("runledger: mailbox message is already processed")
	ErrMailboxInvalid             = errors.New("runledger: invalid mailbox message")
	ErrMailboxReservedProvenance  = errors.New("runledger: reserved mailbox provenance requires trusted injection")
	ErrReservedAgentIdentity      = errors.New("runledger: reserved agent identity")
)

var _ agentcoord.MailboxStore = (*SQLiteStore)(nil)
var _ agentcoord.OperatorMailboxStore = (*SQLiteStore)(nil)

// Enqueue implements agentcoord.MailboxStore. It persists only a bounded
// envelope; the body is expected to live in evidence and is represented by a
// content reference plus digest.
func (s *SQLiteStore) Enqueue(ctx context.Context, message agentcoord.Message) (agentcoord.Message, bool, error) {
	return s.enqueue(ctx, message, false)
}

// EnqueueOperatorSteer is the explicit trusted operator-injection adapter.
// Reserved provenance is assigned here rather than accepted from its caller.
func (s *SQLiteStore) EnqueueOperatorSteer(ctx context.Context, message agentcoord.Message) (agentcoord.Message, bool, error) {
	if strings.TrimSpace(message.SourceAttemptID) != "" || message.SourceLeaseGeneration != 0 {
		return agentcoord.Message{}, false, fmt.Errorf("%w: operator steering cannot carry source attachment provenance", ErrMailboxInvalid)
	}
	message.From = agentcoord.OperatorIdentity
	message.Kind = agentcoord.OperatorSteerKind
	return s.enqueue(ctx, message, true)
}

func (s *SQLiteStore) enqueue(ctx context.Context, message agentcoord.Message, trustedOperator bool) (agentcoord.Message, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var (
		result  agentcoord.Message
		created bool
	)
	err := retryMailboxBusy(ctx, func() error {
		var err error
		result, created, err = s.enqueueOnce(ctx, message, trustedOperator)
		return err
	})
	return result, created, err
}

func (s *SQLiteStore) enqueueOnce(ctx context.Context, message agentcoord.Message, trustedOperator bool) (agentcoord.Message, bool, error) {
	if s == nil || s.db == nil {
		return agentcoord.Message{}, false, fmt.Errorf("runledger: mailbox store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	normalized, err := normalizeMailboxMessage(message)
	if err != nil {
		return agentcoord.Message{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agentcoord.Message{}, false, fmt.Errorf("runledger: begin mailbox enqueue: %w", err)
	}
	defer tx.Rollback()
	if err := validateMailboxEnqueueTarget(ctx, tx, normalized); err != nil {
		return agentcoord.Message{}, false, err
	}
	now, err := sqliteDBTime(ctx, tx)
	if err != nil {
		return agentcoord.Message{}, false, err
	}

	var existing agentcoord.Message
	existing, err = scanMailboxMessage(tx.QueryRowContext(ctx, `
		SELECT message_id, schema_version, session_id, run_id, parent_run_id,
			task_id, turn_id, idempotency_key, correlation_id, causation_id,
			attempt_id, lease_generation, source_attempt_id, source_lease_generation,
			sequence, from_id, to_id, kind,
			content_ref, content_digest, envelope_digest, media_type, byte_count, preview, state,
			lease_owner, lease_expires_at, attempt_count, last_error, created_at,
			claimed_at, processed_at, dead_lettered_at
		FROM agent_mailbox
		WHERE session_id = ? AND run_id = ? AND idempotency_key = ?
	`, normalized.SessionID, normalized.RunID, normalized.IdempotencyKey))
	if err == nil {
		if existing.EnvelopeDigest != normalized.EnvelopeDigest {
			return agentcoord.Message{}, false, fmt.Errorf("%w: session=%s run=%s key=%s", ErrMailboxIdempotencyConflict, normalized.SessionID, normalized.RunID, normalized.IdempotencyKey)
		}
		if err := validateMailboxEnqueueAuthority(ctx, tx, normalized, now, trustedOperator); err != nil {
			return agentcoord.Message{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return agentcoord.Message{}, false, fmt.Errorf("runledger: commit duplicate mailbox enqueue: %w", err)
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return agentcoord.Message{}, false, fmt.Errorf("runledger: inspect mailbox idempotency: %w", err)
	}
	if err := validateMailboxEnqueueAuthority(ctx, tx, normalized, now, trustedOperator); err != nil {
		return agentcoord.Message{}, false, err
	}

	normalized.CreatedAt = now
	normalized.State = agentcoord.MessageQueued
	normalized.Delivery = agentcoord.MessageQueued
	var sequence int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(sequence), 0) + 1
		FROM agent_mailbox
		WHERE session_id = ? AND run_id = ?
	`, normalized.SessionID, normalized.RunID).Scan(&sequence); err != nil {
		return agentcoord.Message{}, false, fmt.Errorf("runledger: allocate mailbox sequence: %w", err)
	}
	normalized.Sequence = sequence
	_, err = tx.ExecContext(ctx, `
		INSERT INTO agent_mailbox (
			message_id, session_id, run_id, parent_run_id, task_id, turn_id,
			idempotency_key, correlation_id, causation_id, attempt_id,
			lease_generation, source_attempt_id, source_lease_generation,
			sequence, schema_version, from_id, to_id, kind,
			content_ref, content_digest, envelope_digest, media_type, byte_count, preview, state,
			attempt_count, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, normalized.MessageID, normalized.SessionID, normalized.RunID,
		nullableStr(normalized.ParentRunID), nullableStr(normalized.TaskID), nullableStr(normalized.TurnID),
		normalized.IdempotencyKey, nullableStr(normalized.CorrelationID), nullableStr(normalized.CausationID),
		nullableStr(normalized.AttemptID), normalized.LeaseGeneration,
		nullableStr(normalized.SourceAttemptID), normalized.SourceLeaseGeneration, normalized.Sequence,
		normalized.Version, nullableStr(normalized.From), normalized.To, normalized.Kind,
		normalized.ContentRef, normalized.ContentDigest, normalized.EnvelopeDigest, normalized.MediaType, normalized.ByteCount,
		normalized.Preview, agentcoord.MessageQueued, 0, sqliteTimestamp(normalized.CreatedAt))
	if err != nil {
		return agentcoord.Message{}, false, fmt.Errorf("runledger: insert mailbox message: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return agentcoord.Message{}, false, fmt.Errorf("runledger: commit mailbox enqueue: %w", err)
	}
	return normalized, true, nil
}

func validateMailboxEnqueueAuthority(ctx context.Context, tx *sql.Tx, message agentcoord.Message, now time.Time, trustedOperator bool) error {
	if message.AttemptID != "" || message.LeaseGeneration > 0 {
		if err := mailboxAttachmentFence(ctx, tx, message.SessionID, message.RunID, message.AttemptID, message.LeaseGeneration, now); err != nil {
			return err
		}
	}
	if trustedOperator {
		if message.From != agentcoord.OperatorIdentity || message.Kind != agentcoord.OperatorSteerKind || message.SourceAttemptID != "" || message.SourceLeaseGeneration != 0 {
			return fmt.Errorf("%w: malformed trusted operator steering envelope", ErrMailboxInvalid)
		}
		return nil
	}
	if reservedMailboxProvenance(message.From, message.Kind) {
		return ErrMailboxReservedProvenance
	}
	return validateMailboxSource(ctx, tx, message, now)
}

// Claim implements at-least-once mailbox delivery. Expired claims are made
// eligible in the same transaction before deterministic sequence ordering is
// evaluated.
func (s *SQLiteStore) Claim(ctx context.Context, request agentcoord.MailboxClaimRequest) ([]agentcoord.Message, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var result []agentcoord.Message
	err := retryMailboxBusy(ctx, func() error {
		var err error
		result, err = s.claimOnce(ctx, request)
		return err
	})
	return result, err
}

func (s *SQLiteStore) claimOnce(ctx context.Context, request agentcoord.MailboxClaimRequest) ([]agentcoord.Message, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("runledger: mailbox store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.RunID = strings.TrimSpace(request.RunID)
	request.Owner = firstNonEmptyMailbox(request.Owner, request.ConsumerID, request.LeaseOwner)
	request.MessageID = strings.TrimSpace(request.MessageID)
	request.AttemptID = strings.TrimSpace(request.AttemptID)
	if request.SessionID == "" || request.RunID == "" || request.Owner == "" || request.AttemptID == "" || request.LeaseGeneration <= 0 {
		return nil, fmt.Errorf("%w: session_id, run_id, owner, attempt_id, and positive lease_generation are required", ErrMailboxInvalid)
	}
	if err := validateMailboxIdentifiers(map[string]string{
		"session_id": request.SessionID, "run_id": request.RunID, "message_id": request.MessageID,
		"owner": request.Owner, "attempt_id": request.AttemptID,
	}); err != nil {
		return nil, err
	}
	duration := request.LeaseDuration
	if duration < 0 {
		return nil, fmt.Errorf("%w: lease_duration cannot be negative", ErrMailboxInvalid)
	}
	if duration == 0 {
		duration = MailboxDefaultLease
	}
	if duration > MailboxMaxLease {
		duration = MailboxMaxLease
	}
	limit := request.Limit
	if limit < 0 {
		return nil, fmt.Errorf("%w: limit cannot be negative", ErrMailboxInvalid)
	}
	if limit == 0 {
		limit = MailboxDefaultLimit
	}
	if limit > MailboxMaxLimit {
		limit = MailboxMaxLimit
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("runledger: begin mailbox claim: %w", err)
	}
	defer tx.Rollback()
	if err := validateMailboxRun(ctx, tx, request.SessionID, request.RunID); err != nil {
		return nil, err
	}
	now, err := sqliteDBTime(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := mailboxAttachmentFence(ctx, tx, request.SessionID, request.RunID, request.AttemptID, request.LeaseGeneration, now); err != nil {
		return nil, err
	}
	// Redelivery is a state transition, not a read-time interpretation. This
	// makes a lost worker visible to every subsequent store instance.
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_mailbox
		SET state = ?, lease_owner = NULL, lease_expires_at = NULL,
			attempt_id = NULL, lease_generation = 0,
			last_error = COALESCE(last_error, 'claim lease expired')
		WHERE session_id = ? AND run_id = ? AND state = ?
		  AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?
	`, agentcoord.MessageQueued, request.SessionID, request.RunID, agentcoord.MessageClaimed, sqliteLeaseTimestamp(now)); err != nil {
		return nil, fmt.Errorf("runledger: expire mailbox claims: %w", err)
	}

	query := `
		SELECT message_id FROM agent_mailbox
		WHERE session_id = ? AND run_id = ? AND state = ?`
	args := []any{request.SessionID, request.RunID, agentcoord.MessageQueued}
	if request.MessageID != "" {
		query += " AND message_id = ?"
		args = append(args, request.MessageID)
	}
	query += " ORDER BY sequence ASC, message_id ASC LIMIT ?"
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("runledger: select mailbox claims: %w", err)
	}
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("runledger: scan mailbox claim: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("runledger: iterate mailbox claims: %w", err)
	}
	rows.Close()
	if len(ids) == 0 {
		if request.MessageID != "" {
			var state string
			err := tx.QueryRowContext(ctx, `SELECT state FROM agent_mailbox WHERE session_id = ? AND run_id = ? AND message_id = ?`, request.SessionID, request.RunID, request.MessageID).Scan(&state)
			if errors.Is(err, sql.ErrNoRows) {
				return nil, fmt.Errorf("%w: %s", ErrMailboxNotFound, request.MessageID)
			}
			if err != nil {
				return nil, fmt.Errorf("runledger: inspect unclaimable mailbox message: %w", err)
			}
			return nil, fmt.Errorf("%w: %s is %s", ErrMailboxClaimUnavailable, request.MessageID, state)
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("runledger: commit empty mailbox claim: %w", err)
		}
		return nil, nil
	}

	expires := now.Add(duration)
	claimed := make([]agentcoord.Message, 0, len(ids))
	for _, id := range ids {
		res, err := tx.ExecContext(ctx, `
			UPDATE agent_mailbox
			SET state = ?, lease_owner = ?, lease_expires_at = ?,
				attempt_id = ?, lease_generation = ?, attempt_count = attempt_count + 1,
				claimed_at = ?, last_error = NULL
			WHERE session_id = ? AND run_id = ? AND message_id = ? AND state = ?
		`, agentcoord.MessageClaimed, request.Owner, sqliteLeaseTimestamp(expires), nullableStr(request.AttemptID),
			request.LeaseGeneration, sqliteTimestamp(now), request.SessionID, request.RunID, id, agentcoord.MessageQueued)
		if err != nil {
			return nil, fmt.Errorf("runledger: claim mailbox message %s: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil || n != 1 {
			continue
		}
		message, err := scanMailboxMessage(tx.QueryRowContext(ctx, `
			SELECT message_id, schema_version, session_id, run_id, parent_run_id,
				task_id, turn_id, idempotency_key, correlation_id, causation_id,
				attempt_id, lease_generation, source_attempt_id, source_lease_generation,
				sequence, from_id, to_id, kind,
				content_ref, content_digest, envelope_digest, media_type, byte_count, preview, state,
				lease_owner, lease_expires_at, attempt_count, last_error, created_at,
				claimed_at, processed_at, dead_lettered_at
			FROM agent_mailbox WHERE message_id = ?
		`, id))
		if err != nil {
			return nil, fmt.Errorf("runledger: read claimed mailbox message %s: %w", id, err)
		}
		claimed = append(claimed, message)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("runledger: commit mailbox claim: %w", err)
	}
	return claimed, nil
}

// Ack implements the fenced compare-and-swap terminal transition.
func (s *SQLiteStore) Ack(ctx context.Context, request agentcoord.MailboxAckRequest) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("runledger: mailbox store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request.Owner = firstNonEmptyMailbox(request.Owner, request.LeaseOwner)
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.RunID = strings.TrimSpace(request.RunID)
	request.MessageID = strings.TrimSpace(request.MessageID)
	request.AttemptID = strings.TrimSpace(request.AttemptID)
	if request.SessionID == "" || request.RunID == "" || request.MessageID == "" || request.Owner == "" || request.AttemptID == "" || request.LeaseGeneration <= 0 {
		return fmt.Errorf("%w: session_id, run_id, message_id, owner, attempt_id, and positive lease_generation are required", ErrMailboxInvalid)
	}
	if err := validateMailboxIdentifiers(map[string]string{
		"session_id": request.SessionID, "run_id": request.RunID, "message_id": request.MessageID,
		"owner": request.Owner, "attempt_id": request.AttemptID,
	}); err != nil {
		return err
	}
	row, tx, err := s.mailboxMutationRow(ctx, request.SessionID, request.RunID, request.MessageID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now, err := sqliteDBTime(ctx, tx)
	if err != nil {
		return err
	}
	if err := mailboxAttachmentFence(ctx, tx, request.SessionID, request.RunID, request.AttemptID, request.LeaseGeneration, now); err != nil {
		return err
	}
	if err := validateMailboxClaim(row, request, now); err != nil {
		if errors.Is(err, ErrMailboxAlreadyProcessed) {
			return tx.Commit()
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_mailbox SET state = ?, processed_at = ?
		WHERE session_id = ? AND run_id = ? AND message_id = ?
	`, agentcoord.MessageProcessed, sqliteTimestamp(now), strings.TrimSpace(request.SessionID), strings.TrimSpace(request.RunID), strings.TrimSpace(request.MessageID)); err != nil {
		return fmt.Errorf("runledger: acknowledge mailbox message: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("runledger: commit mailbox acknowledgement: %w", err)
	}
	return nil
}

// Nack requeues a claimed message or moves it to dead_letter.
func (s *SQLiteStore) Nack(ctx context.Context, request agentcoord.MailboxNackRequest) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("runledger: mailbox store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	request.Owner = firstNonEmptyMailbox(request.Owner, request.LeaseOwner)
	request.SessionID = strings.TrimSpace(request.SessionID)
	request.RunID = strings.TrimSpace(request.RunID)
	request.MessageID = strings.TrimSpace(request.MessageID)
	request.AttemptID = strings.TrimSpace(request.AttemptID)
	if request.SessionID == "" || request.RunID == "" || request.MessageID == "" || request.Owner == "" || request.AttemptID == "" || request.LeaseGeneration <= 0 {
		return fmt.Errorf("%w: session_id, run_id, message_id, owner, attempt_id, and positive lease_generation are required", ErrMailboxInvalid)
	}
	if err := validateMailboxIdentifiers(map[string]string{
		"session_id": request.SessionID, "run_id": request.RunID, "message_id": request.MessageID,
		"owner": request.Owner, "attempt_id": request.AttemptID,
	}); err != nil {
		return err
	}
	row, tx, err := s.mailboxMutationRow(ctx, request.SessionID, request.RunID, request.MessageID)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now, err := sqliteDBTime(ctx, tx)
	if err != nil {
		return err
	}
	if err := mailboxAttachmentFence(ctx, tx, request.SessionID, request.RunID, request.AttemptID, request.LeaseGeneration, now); err != nil {
		return err
	}
	if err := validateMailboxClaim(row, request.MailboxAckRequest, now); err != nil {
		return err
	}
	reason := boundedMailboxText(request.Reason, MailboxMaxError)
	state := agentcoord.MessageQueued
	if request.DeadLetter {
		state = agentcoord.MessageDeadLetter
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE agent_mailbox
		SET state = ?, lease_owner = CASE WHEN ? = ? THEN lease_owner ELSE NULL END,
			lease_expires_at = CASE WHEN ? = ? THEN lease_expires_at ELSE NULL END,
			attempt_id = CASE WHEN ? = ? THEN attempt_id ELSE NULL END,
			lease_generation = CASE WHEN ? = ? THEN lease_generation ELSE 0 END,
			last_error = ?, dead_lettered_at = CASE WHEN ? = ? THEN ? ELSE NULL END
		WHERE session_id = ? AND run_id = ? AND message_id = ?
	`, state, state, agentcoord.MessageDeadLetter, state, agentcoord.MessageDeadLetter,
		state, agentcoord.MessageDeadLetter, state, agentcoord.MessageDeadLetter, reason,
		state, agentcoord.MessageDeadLetter, sqliteTimestamp(now), strings.TrimSpace(request.SessionID), strings.TrimSpace(request.RunID), strings.TrimSpace(request.MessageID))
	if err != nil {
		return fmt.Errorf("runledger: negatively acknowledge mailbox message: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("runledger: commit mailbox negative acknowledgement: %w", err)
	}
	return nil
}

// List implements deterministic mailbox reads. The content body remains in
// evidence; Content is populated from the bounded preview for compatibility.
func (s *SQLiteStore) List(ctx context.Context, query agentcoord.MailboxQuery) ([]agentcoord.Message, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("runledger: mailbox store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	query.SessionID = strings.TrimSpace(query.SessionID)
	query.RunID = strings.TrimSpace(query.RunID)
	query.MessageID = strings.TrimSpace(query.MessageID)
	if query.SessionID == "" || query.RunID == "" {
		return nil, fmt.Errorf("%w: session_id and run_id are required", ErrMailboxInvalid)
	}
	if err := validateMailboxIdentifiers(map[string]string{
		"session_id": query.SessionID, "run_id": query.RunID, "message_id": query.MessageID,
	}); err != nil {
		return nil, err
	}
	if len(query.States) > MailboxMaxStates {
		return nil, fmt.Errorf("%w: states exceeds %d items", ErrMailboxInvalid, MailboxMaxStates)
	}
	if err := validateMailboxRun(ctx, s.db, query.SessionID, query.RunID); err != nil {
		return nil, err
	}
	limit := query.Limit
	if limit <= 0 {
		limit = MailboxDefaultLimit
	}
	if limit > MailboxMaxLimit {
		limit = MailboxMaxLimit
	}
	clauses := []string{"session_id = ?", "run_id = ?"}
	args := []any{query.SessionID, query.RunID}
	if query.MessageID != "" {
		clauses = append(clauses, "message_id = ?")
		args = append(args, query.MessageID)
	}
	if len(query.States) > 0 {
		states := make([]string, 0, len(query.States))
		for _, state := range query.States {
			state = strings.TrimSpace(state)
			if state == "" {
				continue
			}
			if !validMailboxState(state) {
				return nil, fmt.Errorf("%w: unknown mailbox state %q", ErrMailboxInvalid, state)
			}
			states = append(states, state)
		}
		if len(states) > 0 {
			placeholders := make([]string, len(states))
			for i, state := range states {
				placeholders[i] = "?"
				args = append(args, state)
			}
			clauses = append(clauses, "state IN ("+strings.Join(placeholders, ",")+")")
		}
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT message_id, schema_version, session_id, run_id, parent_run_id,
			task_id, turn_id, idempotency_key, correlation_id, causation_id,
			attempt_id, lease_generation, source_attempt_id, source_lease_generation,
			sequence, from_id, to_id, kind,
			content_ref, content_digest, envelope_digest, media_type, byte_count, preview, state,
			lease_owner, lease_expires_at, attempt_count, last_error, created_at,
			claimed_at, processed_at, dead_lettered_at
		FROM agent_mailbox WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY sequence ASC, message_id ASC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("runledger: list mailbox messages: %w", err)
	}
	defer rows.Close()
	messages := make([]agentcoord.Message, 0)
	for rows.Next() {
		message, err := scanMailboxMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("runledger: scan mailbox message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("runledger: iterate mailbox messages: %w", err)
	}
	return messages, nil
}

// Expire makes claimed messages eligible for redelivery. It is safe to call
// repeatedly and is useful to a recovery loop that does not claim first.
func (s *SQLiteStore) Expire(ctx context.Context, sessionID, runID string, now time.Time) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("runledger: mailbox store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID, runID = strings.TrimSpace(sessionID), strings.TrimSpace(runID)
	if sessionID == "" || runID == "" {
		return 0, fmt.Errorf("%w: session_id and run_id are required", ErrMailboxInvalid)
	}
	if err := validateMailboxIdentifiers(map[string]string{"session_id": sessionID, "run_id": runID}); err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("runledger: begin mailbox expiry: %w", err)
	}
	defer tx.Rollback()
	if err := validateMailboxRun(ctx, tx, sessionID, runID); err != nil {
		return 0, err
	}
	if now.IsZero() {
		now, err = sqliteDBTime(ctx, tx)
		if err != nil {
			return 0, err
		}
	} else {
		now = now.UTC()
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE agent_mailbox SET state = ?, lease_owner = NULL,
			lease_expires_at = NULL, attempt_id = NULL, lease_generation = 0,
			last_error = COALESCE(last_error, 'claim lease expired')
		WHERE session_id = ? AND run_id = ? AND state = ?
		  AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?
	`, agentcoord.MessageQueued, sessionID, runID, agentcoord.MessageClaimed, sqliteLeaseTimestamp(now))
	if err != nil {
		return 0, fmt.Errorf("runledger: expire mailbox messages: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("runledger: count expired mailbox messages: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("runledger: commit mailbox expiry: %w", err)
	}
	return int(count), nil
}

// GetMailboxMessage is a narrow read helper for recovery and tests.
func (s *SQLiteStore) GetMailboxMessage(ctx context.Context, sessionID, runID, messageID string) (agentcoord.Message, error) {
	messages, err := s.List(ctx, agentcoord.MailboxQuery{SessionID: sessionID, RunID: runID, MessageID: messageID, Limit: 1})
	if err != nil {
		return agentcoord.Message{}, err
	}
	if len(messages) == 0 {
		return agentcoord.Message{}, fmt.Errorf("%w: %s", ErrMailboxNotFound, strings.TrimSpace(messageID))
	}
	return messages[0], nil
}

// EnqueueMessage, ClaimMessages, AckMessage, and NackMessage are descriptive
// aliases for callers that prefer not to confuse an operational mailbox with
// the Coordinator domain port.
func (s *SQLiteStore) EnqueueMessage(ctx context.Context, message agentcoord.Message) (agentcoord.Message, bool, error) {
	return s.Enqueue(ctx, message)
}

func (s *SQLiteStore) ClaimMessages(ctx context.Context, request agentcoord.MailboxClaimRequest) ([]agentcoord.Message, error) {
	return s.Claim(ctx, request)
}

func (s *SQLiteStore) ListMessages(ctx context.Context, query agentcoord.MailboxQuery) ([]agentcoord.Message, error) {
	return s.List(ctx, query)
}

func (s *SQLiteStore) AckMessage(ctx context.Context, request agentcoord.MailboxAckRequest) error {
	return s.Ack(ctx, request)
}

func (s *SQLiteStore) NackMessage(ctx context.Context, request agentcoord.MailboxNackRequest) error {
	return s.Nack(ctx, request)
}

func (s *SQLiteStore) DeadLetter(ctx context.Context, request agentcoord.MailboxNackRequest) error {
	request.DeadLetter = true
	return s.Nack(ctx, request)
}

func (s *SQLiteStore) ExpireClaims(ctx context.Context, sessionID, runID string, now time.Time) (int, error) {
	return s.Expire(ctx, sessionID, runID, now)
}

func normalizeMailboxMessage(message agentcoord.Message) (agentcoord.Message, error) {
	rawContent := message.Content
	message.Version = strings.TrimSpace(message.Version)
	if message.Version == "" {
		message.Version = agentcoord.MessageSchemaVersion
	}
	if message.Version != agentcoord.MessageSchemaVersion {
		return agentcoord.Message{}, fmt.Errorf("%w: unsupported schema_version", ErrMailboxInvalid)
	}
	message.ID = strings.TrimSpace(message.ID)
	message.MessageID = strings.TrimSpace(message.MessageID)
	if message.ID != "" && message.MessageID != "" && message.ID != message.MessageID {
		return agentcoord.Message{}, fmt.Errorf("%w: id and message_id conflict", ErrMailboxInvalid)
	}
	if message.MessageID == "" {
		message.MessageID = message.ID
	}
	message.IdempotencyKey = strings.TrimSpace(message.IdempotencyKey)
	message.SessionID = strings.TrimSpace(message.SessionID)
	message.RunID = strings.TrimSpace(message.RunID)
	message.ParentRunID = strings.TrimSpace(message.ParentRunID)
	message.TaskID = strings.TrimSpace(message.TaskID)
	message.TurnID = strings.TrimSpace(message.TurnID)
	message.CorrelationID = strings.TrimSpace(message.CorrelationID)
	message.CausationID = strings.TrimSpace(message.CausationID)
	message.AttemptID = strings.TrimSpace(message.AttemptID)
	message.SourceAttemptID = strings.TrimSpace(message.SourceAttemptID)
	message.From = strings.TrimSpace(message.From)
	message.To = strings.TrimSpace(message.To)
	message.Kind = strings.TrimSpace(message.Kind)
	message.ContentRef = strings.TrimSpace(message.ContentRef)
	message.ContentDigest = strings.ToLower(strings.TrimSpace(message.ContentDigest))
	message.MediaType = strings.TrimSpace(message.MediaType)
	message.Preview = CanonicalMailboxPreview(message.Preview, rawContent)
	message.Content = ""
	if message.SessionID == "" || message.RunID == "" || message.To == "" || message.Kind == "" {
		return agentcoord.Message{}, fmt.Errorf("%w: session_id, run_id, to, and kind are required", ErrMailboxInvalid)
	}
	if message.To != message.RunID {
		return agentcoord.Message{}, fmt.Errorf("%w: to must equal target run_id", ErrMailboxInvalid)
	}
	if err := validateMailboxIdentifiers(map[string]string{
		"schema_version": message.Version, "message_id": message.MessageID, "idempotency_key": message.IdempotencyKey,
		"session_id": message.SessionID, "run_id": message.RunID,
		"parent_run_id": message.ParentRunID, "task_id": message.TaskID,
		"turn_id": message.TurnID, "correlation_id": message.CorrelationID,
		"causation_id": message.CausationID, "attempt_id": message.AttemptID,
		"source_attempt_id": message.SourceAttemptID,
		"from":              message.From, "to": message.To, "kind": message.Kind,
		"content_ref": message.ContentRef, "media_type": message.MediaType,
	}); err != nil {
		return agentcoord.Message{}, err
	}
	if message.MessageID == "" {
		if message.IdempotencyKey == "" {
			return agentcoord.Message{}, fmt.Errorf("%w: idempotency_key is required when message_id is omitted", ErrMailboxInvalid)
		}
		message.MessageID = deterministicMailboxMessageID(message.SessionID, message.RunID, message.IdempotencyKey)
	}
	message.ID = message.MessageID
	if message.IdempotencyKey == "" {
		message.IdempotencyKey = message.MessageID
	}
	if message.ContentRef == "" {
		return agentcoord.Message{}, fmt.Errorf("%w: content_ref is required", ErrMailboxInvalid)
	}
	if len(rawContent) > MailboxMaxContent {
		return agentcoord.Message{}, fmt.Errorf("%w: content exceeds %d bytes", ErrMailboxInvalid, MailboxMaxContent)
	}
	if message.ContentDigest == "" {
		digest := sha256.Sum256([]byte(rawContent))
		message.ContentDigest = hex.EncodeToString(digest[:])
	}
	if len(message.ContentDigest) > MailboxMaxDigest {
		return agentcoord.Message{}, fmt.Errorf("%w: content_digest exceeds %d bytes", ErrMailboxInvalid, MailboxMaxDigest)
	}
	if message.ByteCount < 0 {
		return agentcoord.Message{}, fmt.Errorf("%w: byte_count cannot be negative", ErrMailboxInvalid)
	}
	if message.LeaseGeneration < 0 || message.SourceLeaseGeneration < 0 {
		return agentcoord.Message{}, fmt.Errorf("%w: lease generations cannot be negative", ErrMailboxInvalid)
	}
	if message.ByteCount > MailboxMaxContent {
		return agentcoord.Message{}, fmt.Errorf("%w: byte_count exceeds %d bytes", ErrMailboxInvalid, MailboxMaxContent)
	}
	if message.ByteCount == 0 && rawContent != "" {
		message.ByteCount = int64(len(rawContent))
	}
	if message.MediaType == "" {
		message.MediaType = "application/octet-stream"
	}
	for field, value := range map[string]string{
		"schema_version": message.Version, "message_id": message.MessageID, "idempotency_key": message.IdempotencyKey,
		"session_id": message.SessionID, "run_id": message.RunID,
		"parent_run_id": message.ParentRunID, "task_id": message.TaskID,
		"turn_id": message.TurnID, "correlation_id": message.CorrelationID,
		"causation_id": message.CausationID, "attempt_id": message.AttemptID,
		"source_attempt_id": message.SourceAttemptID,
		"from":              message.From, "to": message.To, "kind": message.Kind,
		"content_ref": message.ContentRef, "media_type": message.MediaType,
	} {
		if err := validateMailboxIdentifiers(map[string]string{field: value}); err != nil {
			return agentcoord.Message{}, err
		}
	}
	envelopeDigest, err := mailboxEnvelopeDigest(message)
	if err != nil {
		return agentcoord.Message{}, err
	}
	message.EnvelopeDigest = envelopeDigest
	return message, nil
}

func deterministicMailboxMessageID(sessionID, runID, idempotencyKey string) string {
	canonical := struct {
		Domain, SessionID, RunID, IdempotencyKey string
	}{
		Domain: mailboxMessageIDDomain, SessionID: strings.TrimSpace(sessionID),
		RunID: strings.TrimSpace(runID), IdempotencyKey: strings.TrimSpace(idempotencyKey),
	}
	body, _ := json.Marshal(canonical)
	digest := sha256.Sum256(body)
	return "msg_" + hex.EncodeToString(digest[:])
}

// CanonicalMailboxPreview applies the same fallback, redaction, trimming, and
// byte bound used by durable storage. Coordinator adapters use it before
// deriving an implicit idempotency key so the key and stored envelope cannot
// disagree about an omitted preview.
func CanonicalMailboxPreview(preview, content string) string {
	return boundedMailboxText(string(evidence.Redact([]byte(firstNonEmptyMailbox(preview, content)))), MailboxMaxPreview)
}

func mailboxEnvelopeDigest(message agentcoord.Message) (string, error) {
	canonical := struct {
		Version, MessageID, SessionID, RunID, ParentRunID, TaskID, TurnID string
		IdempotencyKey, CorrelationID, CausationID                        string
		SourceAttemptID                                                   string
		SourceLeaseGeneration                                             int64
		From, To, Kind                                                    string
		ContentRef, ContentDigest, MediaType, Preview                     string
		ByteCount                                                         int64
	}{
		Version: message.Version, MessageID: message.MessageID, SessionID: message.SessionID, RunID: message.RunID,
		ParentRunID: message.ParentRunID, TaskID: message.TaskID, TurnID: message.TurnID,
		IdempotencyKey: message.IdempotencyKey, CorrelationID: message.CorrelationID, CausationID: message.CausationID,
		SourceAttemptID: message.SourceAttemptID, SourceLeaseGeneration: message.SourceLeaseGeneration,
		From: message.From, To: message.To, Kind: message.Kind,
		ContentRef: message.ContentRef, ContentDigest: message.ContentDigest, MediaType: message.MediaType,
		Preview: message.Preview, ByteCount: message.ByteCount,
	}
	body, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize mailbox envelope: %v", ErrMailboxInvalid, err)
	}
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:]), nil
}

type mailboxRow struct {
	message                                            agentcoord.Message
	leaseExpires, claimedAt, processedAt, deadLettered sql.NullString
}

type mailboxScanner interface {
	Scan(dest ...any) error
}

func scanMailboxMessage(scanner mailboxScanner) (agentcoord.Message, error) {
	var (
		message                                                               agentcoord.Message
		parentRun, task, turn, correlation, causation, attempt, sourceAttempt sql.NullString
		from, contentRef, preview, owner, lastError                           sql.NullString
		leaseExpires, created, claimedAt, processedAt, deadLettered           sql.NullString
	)
	if err := scanner.Scan(
		&message.MessageID, &message.Version, &message.SessionID, &message.RunID,
		&parentRun, &task, &turn, &message.IdempotencyKey, &correlation, &causation,
		&attempt, &message.LeaseGeneration, &sourceAttempt, &message.SourceLeaseGeneration,
		&message.Sequence, &from, &message.To,
		&message.Kind, &contentRef, &message.ContentDigest, &message.EnvelopeDigest, &message.MediaType,
		&message.ByteCount, &preview, &message.State, &owner, &leaseExpires,
		&message.AttemptCount, &lastError, &created, &claimedAt, &processedAt,
		&deadLettered,
	); err != nil {
		return agentcoord.Message{}, err
	}
	message.ID = message.MessageID
	message.ParentRunID = parentRun.String
	message.TaskID = task.String
	message.TurnID = turn.String
	message.CorrelationID = correlation.String
	message.CausationID = causation.String
	message.AttemptID = attempt.String
	message.SourceAttemptID = sourceAttempt.String
	message.From = from.String
	message.ContentRef = contentRef.String
	message.Preview = preview.String
	message.Content = message.Preview
	message.LeaseOwner = owner.String
	message.LastError = lastError.String
	message.Delivery = message.State
	message.CreatedAt = parseSQLiteTimestamp(created.String)
	message.LeasedUntil = parseSQLiteTimestamp(leaseExpires.String)
	message.ProcessedAt = parseSQLiteTimestamp(processedAt.String)
	message.DeadLetteredAt = parseSQLiteTimestamp(deadLettered.String)
	return message, nil
}

func (s *SQLiteStore) mailboxMutationRow(ctx context.Context, sessionID, runID, messageID string) (agentcoord.Message, *sql.Tx, error) {
	sessionID, runID, messageID = strings.TrimSpace(sessionID), strings.TrimSpace(runID), strings.TrimSpace(messageID)
	if sessionID == "" || runID == "" || messageID == "" {
		return agentcoord.Message{}, nil, fmt.Errorf("%w: session_id, run_id, and message_id are required", ErrMailboxInvalid)
	}
	if err := validateMailboxIdentifiers(map[string]string{"session_id": sessionID, "run_id": runID, "message_id": messageID}); err != nil {
		return agentcoord.Message{}, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return agentcoord.Message{}, nil, fmt.Errorf("runledger: begin mailbox mutation: %w", err)
	}
	row, err := scanMailboxMessage(tx.QueryRowContext(ctx, `
		SELECT message_id, schema_version, session_id, run_id, parent_run_id,
			task_id, turn_id, idempotency_key, correlation_id, causation_id,
			attempt_id, lease_generation, source_attempt_id, source_lease_generation,
			sequence, from_id, to_id, kind,
			content_ref, content_digest, envelope_digest, media_type, byte_count, preview, state,
			lease_owner, lease_expires_at, attempt_count, last_error, created_at,
			claimed_at, processed_at, dead_lettered_at
		FROM agent_mailbox WHERE session_id = ? AND run_id = ? AND message_id = ?
	`, sessionID, runID, messageID))
	if errors.Is(err, sql.ErrNoRows) {
		tx.Rollback()
		return agentcoord.Message{}, nil, fmt.Errorf("%w: %s", ErrMailboxNotFound, messageID)
	}
	if err != nil {
		tx.Rollback()
		return agentcoord.Message{}, nil, fmt.Errorf("runledger: read mailbox mutation: %w", err)
	}
	return row, tx, nil
}

func validateMailboxClaim(message agentcoord.Message, request agentcoord.MailboxAckRequest, now time.Time) error {
	if message.State == agentcoord.MessageProcessed {
		if message.LeaseOwner == strings.TrimSpace(request.Owner) && message.LeaseGeneration == request.LeaseGeneration && exactMailboxAttempt(message.AttemptID, request.AttemptID) {
			return ErrMailboxAlreadyProcessed
		}
		return ErrMailboxClaimOwner
	}
	if message.State != agentcoord.MessageClaimed {
		return ErrMailboxNotClaimed
	}
	if message.LeaseOwner != strings.TrimSpace(request.Owner) || message.LeaseGeneration != request.LeaseGeneration || !exactMailboxAttempt(message.AttemptID, request.AttemptID) {
		return ErrMailboxClaimOwner
	}
	if !message.LeasedUntil.IsZero() && !now.Before(message.LeasedUntil) {
		return ErrMailboxLeaseExpired
	}
	return nil
}

func exactMailboxAttempt(stored, requested string) bool {
	stored, requested = strings.TrimSpace(stored), strings.TrimSpace(requested)
	if stored == "" {
		return requested == ""
	}
	return requested != "" && stored == requested
}

func validateMailboxIdentifiers(fields map[string]string) error {
	for field, value := range fields {
		if err := agentcoord.ValidateMonitorIdentifier(field, value, false); err != nil {
			return fmt.Errorf("%w: %s is malformed", ErrMailboxInvalid, field)
		}
	}
	return nil
}

func validMailboxState(state string) bool {
	switch state {
	case agentcoord.MessageQueued, agentcoord.MessageClaimed, agentcoord.MessageProcessed, agentcoord.MessageDeadLetter:
		return true
	default:
		return false
	}
}

type mailboxRunQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func validateMailboxRun(ctx context.Context, queryer mailboxRunQueryer, sessionID, runID string) error {
	var storedSession string
	err := queryer.QueryRowContext(ctx, `SELECT session_id FROM agent_runs WHERE run_id = ?`, strings.TrimSpace(runID)).Scan(&storedSession)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: run does not exist", ErrMailboxInvalid)
	}
	if err != nil {
		return fmt.Errorf("runledger: inspect mailbox run: %w", err)
	}
	if storedSession != strings.TrimSpace(sessionID) {
		return fmt.Errorf("%w: session does not own run", ErrMailboxInvalid)
	}
	return nil
}

func validateMailboxEnqueueTarget(ctx context.Context, queryer mailboxRunQueryer, message agentcoord.Message) error {
	var (
		storedSession string
		parent, task  sql.NullString
	)
	err := queryer.QueryRowContext(ctx, `SELECT session_id, parent_run_id, task_id
		FROM agent_runs WHERE run_id = ?`, message.RunID).Scan(&storedSession, &parent, &task)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: run does not exist", ErrMailboxInvalid)
	}
	if err != nil {
		return fmt.Errorf("runledger: inspect mailbox enqueue target: %w", err)
	}
	if storedSession != message.SessionID {
		return fmt.Errorf("%w: session does not own run", ErrMailboxInvalid)
	}
	if message.ParentRunID != "" && message.ParentRunID != parent.String {
		return fmt.Errorf("%w: parent_run_id does not match target", ErrMailboxInvalid)
	}
	if message.TaskID != "" && message.TaskID != task.String {
		return fmt.Errorf("%w: task_id does not match target", ErrMailboxInvalid)
	}
	return nil
}

func validateMailboxSource(ctx context.Context, tx *sql.Tx, message agentcoord.Message, now time.Time) error {
	sourceRunID := strings.TrimSpace(message.From)
	if sourceRunID == "" {
		return fmt.Errorf("%w: source run is required", ErrMailboxInvalid)
	}
	var sourceSession string
	var parentRunID sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT session_id, parent_run_id FROM agent_runs WHERE run_id = ?`, sourceRunID).Scan(&sourceSession, &parentRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: source run does not exist", ErrMailboxInvalid)
	}
	if err != nil {
		return fmt.Errorf("runledger: inspect mailbox source run: %w", err)
	}
	if sourceSession != message.SessionID {
		return fmt.Errorf("%w: source run belongs to a different session", ErrMailboxInvalid)
	}
	childToParent := parentRunID.Valid && strings.TrimSpace(parentRunID.String) == message.RunID
	var targetParent sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT parent_run_id FROM agent_runs WHERE run_id = ? AND session_id = ?`, message.RunID, message.SessionID).Scan(&targetParent); err != nil {
		return fmt.Errorf("runledger: inspect mailbox target relationship: %w", err)
	}
	parentToChild := targetParent.Valid && strings.TrimSpace(targetParent.String) == sourceRunID
	if !childToParent && !parentToChild {
		return fmt.Errorf("%w: source and target are not a recorded parent-child pair", ErrMailboxInvalid)
	}
	if message.SourceAttemptID == "" || message.SourceLeaseGeneration <= 0 {
		return fmt.Errorf("%w: run source requires its exact attachment fence", ErrMailboxInvalid)
	}
	return mailboxAttachmentFence(ctx, tx, sourceSession, sourceRunID, message.SourceAttemptID, message.SourceLeaseGeneration, now)
}

func reservedMailboxProvenance(from, kind string) bool {
	return strings.EqualFold(strings.TrimSpace(from), agentcoord.OperatorIdentity) ||
		strings.EqualFold(strings.TrimSpace(kind), agentcoord.OperatorSteerKind)
}

func reservedAgentIdentity(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), agentcoord.OperatorIdentity)
}

func sqliteDBTime(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`).Scan(&raw); err != nil {
		return time.Time{}, fmt.Errorf("runledger: read sqlite time: %w", err)
	}
	return parseSQLiteTimestamp(raw), nil
}

func boundedMailboxText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func firstNonEmptyMailbox(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

const mailboxBusyRetryWindow = 5 * time.Second

func retryMailboxBusy(ctx context.Context, operation func() error) error {
	deadline := time.Now().Add(mailboxBusyRetryWindow)
	for attempt := 0; ; attempt++ {
		err := operation()
		if err == nil || !storage.IsSQLiteBusyError(err) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("runledger: mailbox busy retry exhausted after %s: %w", mailboxBusyRetryWindow, err)
		}
		delay := 2 * time.Millisecond * time.Duration(1<<minMailboxRetryShift(attempt))
		if delay > 50*time.Millisecond {
			delay = 50 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func minMailboxRetryShift(attempt int) int {
	if attempt < 0 {
		return 0
	}
	if attempt > 5 {
		return 5
	}
	return attempt
}
