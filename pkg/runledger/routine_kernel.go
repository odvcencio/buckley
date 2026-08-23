package runledger

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
)

const (
	RunContractMaxDigest     = 128
	RunContractMaxEvidenceID = 256
	maxFinalizationEvents    = 4
	maxFinalizationPayload   = 64 * 1024
	maxFinalizationRefs      = 256
)

var (
	ErrRunContractConflict = errors.New("runledger: run contract conflicts with durable identity")
	ErrRunContractInvalid  = errors.New("runledger: invalid run contract")
)

// RunContract binds a logical run identity to the immutable task input and
// its pinned evidence object. Process-attempt fields deliberately do not live
// here; they are reconstructed from agent_run_attempts.
type RunContract struct {
	RunID          string
	SessionID      string
	InputDigest    string
	TaskEvidenceID string
	CreatedAt      time.Time
}

// RoutineRunJournal is the narrow idempotent spawn/recovery extension used by
// the local coordinator. EnsureRunContract creates the run and its immutable
// input binding in one transaction, or validates an existing binding.
type RoutineRunJournal interface {
	EnsureRunContract(ctx context.Context, run AgentRun, inputDigest, taskEvidenceID string) (AgentRun, bool, error)
	GetRunContract(ctx context.Context, runID string) (RunContract, error)
}

// AttemptFinalization is the exact-generation terminal transaction. Events
// must use stable IDs so the whole operation is safely replayable.
type AttemptFinalization struct {
	SessionID       string
	RunID           string
	AttemptID       string
	LeaseGeneration int64
	Status          string
	EndedAt         time.Time
	Outcome         map[string]any
	ReleaseReason   string
	Events          []Event
}

// AttemptFinalizer atomically ends a run, detaches its exact process attempt,
// releases its claims, and appends its immutable terminal audit facts.
type AttemptFinalizer interface {
	FinalizeRunAttempt(ctx context.Context, finalization AttemptFinalization) error
}

var _ RoutineRunJournal = (*SQLiteStore)(nil)
var _ AttemptFinalizer = (*SQLiteStore)(nil)

func (s *SQLiteStore) EnsureRunContract(ctx context.Context, run AgentRun, inputDigest, taskEvidenceID string) (AgentRun, bool, error) {
	if s == nil || s.db == nil {
		return AgentRun{}, false, fmt.Errorf("runledger: run contract journal is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	run.RunID = strings.TrimSpace(run.RunID)
	run.SessionID = strings.TrimSpace(run.SessionID)
	inputDigest = strings.ToLower(strings.TrimSpace(inputDigest))
	taskEvidenceID = strings.TrimSpace(taskEvidenceID)
	if run.RunID == "" || run.SessionID == "" || inputDigest == "" || taskEvidenceID == "" {
		return AgentRun{}, false, fmt.Errorf("%w: run_id, session_id, input_digest, and task_evidence_id are required", ErrRunContractInvalid)
	}
	if reservedAgentIdentity(run.RunID) || reservedAgentIdentity(run.ParentRunID) {
		return AgentRun{}, false, fmt.Errorf("%w: run_id=%q parent_run_id=%q", ErrReservedAgentIdentity, run.RunID, strings.TrimSpace(run.ParentRunID))
	}
	if len(inputDigest) > RunContractMaxDigest || len(taskEvidenceID) > RunContractMaxEvidenceID {
		return AgentRun{}, false, fmt.Errorf("%w: digest or evidence identity exceeds its bound", ErrRunContractInvalid)
	}
	for field, value := range map[string]string{
		"run_id": run.RunID, "session_id": run.SessionID, "parent_run_id": run.ParentRunID,
		"task_id": run.TaskID, "agent_id": run.AgentID, "model_id": run.ModelID,
		"provider_id": run.ProviderID, "backend": run.Backend, "status": run.Status,
	} {
		if len(value) > AttachmentMaxID {
			return AgentRun{}, false, fmt.Errorf("%w: %s exceeds %d bytes", ErrRunContractInvalid, field, AttachmentMaxID)
		}
	}
	if run.Status == "" {
		run.Status = "queued"
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	budgetJSON, err := marshalJSONMap(run.Budget)
	if err != nil {
		return AgentRun{}, false, fmt.Errorf("runledger: marshal contract budget: %w", err)
	}
	outcomeJSON, err := marshalJSONMap(run.Outcome)
	if err != nil {
		return AgentRun{}, false, fmt.Errorf("runledger: marshal contract outcome: %w", err)
	}
	if len(nullableJSONText(budgetJSON)) > maxFinalizationPayload || len(nullableJSONText(outcomeJSON)) > maxFinalizationPayload {
		return AgentRun{}, false, fmt.Errorf("%w: run budget or outcome exceeds %d bytes", ErrRunContractInvalid, maxFinalizationPayload)
	}

	var result AgentRun
	created := false
	err = retryMailboxBusy(ctx, func() error {
		var ensureErr error
		result, created, ensureErr = s.ensureRunContractOnce(ctx, run, inputDigest, taskEvidenceID, budgetJSON, outcomeJSON)
		return ensureErr
	})
	return result, created, err
}

func (s *SQLiteStore) ensureRunContractOnce(ctx context.Context, run AgentRun, inputDigest, taskEvidenceID string, budgetJSON, outcomeJSON any) (AgentRun, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AgentRun{}, false, fmt.Errorf("runledger: begin run contract: %w", err)
	}
	defer tx.Rollback()

	existing, err := scanRun(tx.QueryRowContext(ctx, runSelectColumns+" WHERE run_id = ?", run.RunID))
	created := false
	if errors.Is(err, ErrNotFound) {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO agent_runs (
				run_id, session_id, parent_run_id, task_id, agent_id, model_id,
				provider_id, backend, status, started_at, ended_at, budget_json, outcome_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, run.RunID, run.SessionID, nullableStr(run.ParentRunID), nullableStr(run.TaskID), nullableStr(run.AgentID),
			nullableStr(run.ModelID), nullableStr(run.ProviderID), nullableStr(run.Backend), run.Status,
			sqliteTimestamp(run.StartedAt), nullableTime(run.EndedAt), budgetJSON, outcomeJSON)
		if err != nil {
			return AgentRun{}, false, fmt.Errorf("runledger: insert contracted run: %w", err)
		}
		existing = run
		created = true
	} else if err != nil {
		return AgentRun{}, false, err
	} else if !sameRunContractIdentity(existing, run) {
		return AgentRun{}, false, fmt.Errorf("%w: run %s", ErrRunContractConflict, run.RunID)
	}

	var contract RunContract
	var createdRaw string
	err = tx.QueryRowContext(ctx, `
		SELECT run_id, session_id, input_digest, task_evidence_id, created_at
		FROM agent_run_contracts WHERE run_id = ?
	`, run.RunID).Scan(&contract.RunID, &contract.SessionID, &contract.InputDigest, &contract.TaskEvidenceID, &createdRaw)
	if errors.Is(err, sql.ErrNoRows) {
		now, timeErr := sqliteDBTime(ctx, tx)
		if timeErr != nil {
			return AgentRun{}, false, timeErr
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agent_run_contracts (run_id, session_id, input_digest, task_evidence_id, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, run.RunID, run.SessionID, inputDigest, taskEvidenceID, sqliteTimestamp(now)); err != nil {
			return AgentRun{}, false, fmt.Errorf("runledger: insert run contract: %w", err)
		}
	} else if err != nil {
		return AgentRun{}, false, fmt.Errorf("runledger: read run contract: %w", err)
	} else if contract.SessionID != run.SessionID || contract.InputDigest != inputDigest || contract.TaskEvidenceID != taskEvidenceID {
		return AgentRun{}, false, fmt.Errorf("%w: run %s input binding changed", ErrRunContractConflict, run.RunID)
	}

	if err := tx.Commit(); err != nil {
		return AgentRun{}, false, fmt.Errorf("runledger: commit run contract: %w", err)
	}
	return existing, created, nil
}

func (s *SQLiteStore) GetRunContract(ctx context.Context, runID string) (RunContract, error) {
	if s == nil || s.db == nil {
		return RunContract{}, fmt.Errorf("runledger: run contract journal is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var contract RunContract
	var createdRaw string
	err := s.db.QueryRowContext(ctx, `
		SELECT run_id, session_id, input_digest, task_evidence_id, created_at
		FROM agent_run_contracts WHERE run_id = ?
	`, strings.TrimSpace(runID)).Scan(&contract.RunID, &contract.SessionID, &contract.InputDigest, &contract.TaskEvidenceID, &createdRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return RunContract{}, ErrNotFound
	}
	if err != nil {
		return RunContract{}, fmt.Errorf("runledger: read run contract: %w", err)
	}
	contract.CreatedAt = parseSQLiteTimestamp(createdRaw)
	return contract, nil
}

func sameRunContractIdentity(left, right AgentRun) bool {
	return left.RunID == right.RunID && left.SessionID == right.SessionID &&
		left.ParentRunID == right.ParentRunID && left.TaskID == right.TaskID &&
		left.AgentID == right.AgentID && left.ModelID == right.ModelID &&
		left.ProviderID == right.ProviderID && left.Backend == right.Backend
}

type preparedKernelEvent struct {
	event                            Event
	payload, evidenceIDs, receiptIDs any
}

func prepareKernelEvent(event Event, endedAt time.Time) (preparedKernelEvent, error) {
	if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.RunID) == "" || strings.TrimSpace(event.Type) == "" {
		return preparedKernelEvent{}, fmt.Errorf("runledger: finalization events require stable id, run_id, and type")
	}
	if event.SchemaVersion == "" {
		event.SchemaVersion = SchemaVersion
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = endedAt
	}
	if event.Redaction == "" {
		event.Redaction = DefaultRedactionVersion
	}
	if len(event.ID) > AttachmentMaxID || len(event.Type) > AttachmentMaxID || len(event.SessionID) > AttachmentMaxID || len(event.RunID) > AttachmentMaxID || len(event.ParentRunID) > AttachmentMaxID || len(event.TaskID) > AttachmentMaxID || len(event.AgentID) > AttachmentMaxID || len(event.ModelID) > AttachmentMaxID || len(event.ProviderID) > AttachmentMaxID || len(event.Backend) > AttachmentMaxID || len(event.SnapshotID) > AttachmentMaxID {
		return preparedKernelEvent{}, fmt.Errorf("runledger: finalization event identity exceeds %d bytes", AttachmentMaxID)
	}
	if len(event.EvidenceIDs) > maxFinalizationRefs || len(event.ReceiptIDs) > maxFinalizationRefs {
		return preparedKernelEvent{}, fmt.Errorf("runledger: finalization event references exceed %d items", maxFinalizationRefs)
	}
	payload, err := marshalJSONMap(event.Payload)
	if err != nil {
		return preparedKernelEvent{}, fmt.Errorf("runledger: marshal finalization payload: %w", err)
	}
	if len(nullableJSONText(payload)) > maxFinalizationPayload {
		return preparedKernelEvent{}, fmt.Errorf("runledger: finalization event payload exceeds %d bytes", maxFinalizationPayload)
	}
	evidenceIDs, err := marshalJSONStrings(event.EvidenceIDs)
	if err != nil {
		return preparedKernelEvent{}, fmt.Errorf("runledger: marshal finalization evidence: %w", err)
	}
	receiptIDs, err := marshalJSONStrings(event.ReceiptIDs)
	if err != nil {
		return preparedKernelEvent{}, fmt.Errorf("runledger: marshal finalization receipts: %w", err)
	}
	if len(nullableJSONText(evidenceIDs)) > maxFinalizationPayload || len(nullableJSONText(receiptIDs)) > maxFinalizationPayload {
		return preparedKernelEvent{}, fmt.Errorf("runledger: finalization event references exceed %d bytes", maxFinalizationPayload)
	}
	return preparedKernelEvent{event: event, payload: payload, evidenceIDs: evidenceIDs, receiptIDs: receiptIDs}, nil
}

func (s *SQLiteStore) FinalizeRunAttempt(ctx context.Context, finalization AttemptFinalization) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("runledger: attempt finalizer is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	finalization.SessionID = strings.TrimSpace(finalization.SessionID)
	finalization.RunID = strings.TrimSpace(finalization.RunID)
	finalization.AttemptID = strings.TrimSpace(finalization.AttemptID)
	finalization.Status = strings.TrimSpace(finalization.Status)
	finalization.ReleaseReason = boundedMailboxText(finalization.ReleaseReason, MailboxMaxError)
	if finalization.SessionID == "" || finalization.RunID == "" || finalization.AttemptID == "" || finalization.LeaseGeneration <= 0 || finalization.Status == "" {
		return fmt.Errorf("%w: exact run attachment and status are required", ErrAttachmentStale)
	}
	if len(finalization.SessionID) > AttachmentMaxID || len(finalization.RunID) > AttachmentMaxID || len(finalization.AttemptID) > AttachmentMaxID || len(finalization.Status) > AttachmentMaxID {
		return fmt.Errorf("runledger: finalization identity exceeds %d bytes", AttachmentMaxID)
	}
	if finalization.Status != strings.ToLower(finalization.Status) || !terminalAgentRunStatus(finalization.Status) {
		return fmt.Errorf("runledger: finalization status %q is not terminal", finalization.Status)
	}
	if len(finalization.Events) == 0 || len(finalization.Events) > maxFinalizationEvents {
		return fmt.Errorf("runledger: finalization requires between 1 and %d stable events", maxFinalizationEvents)
	}
	if finalization.EndedAt.IsZero() {
		finalization.EndedAt = time.Now().UTC()
	} else {
		finalization.EndedAt = finalization.EndedAt.UTC()
	}
	outcomeJSON, err := marshalJSONMap(finalization.Outcome)
	if err != nil {
		return fmt.Errorf("runledger: marshal finalization outcome: %w", err)
	}
	if len(nullableJSONText(outcomeJSON)) > maxFinalizationPayload {
		return fmt.Errorf("runledger: finalization outcome exceeds %d bytes", maxFinalizationPayload)
	}
	prepared := make([]preparedKernelEvent, 0, len(finalization.Events))
	for _, event := range finalization.Events {
		if event.RunID != finalization.RunID || (event.SessionID != "" && event.SessionID != finalization.SessionID) {
			return fmt.Errorf("runledger: finalization event identity conflicts with run attachment")
		}
		item, prepErr := prepareKernelEvent(event, finalization.EndedAt)
		if prepErr != nil {
			return prepErr
		}
		prepared = append(prepared, item)
	}

	var inserted []Event
	var ralphSink RalphSink
	err = func() error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.appendGate:
		}
		defer func() { s.appendGate <- struct{}{} }()
		_, ralphSink = s.sinks()
		return retryMailboxBusy(ctx, func() error {
			inserted = nil
			var finalizeErr error
			inserted, finalizeErr = s.finalizeRunAttemptOnce(ctx, finalization, outcomeJSON, prepared, ralphSink != nil)
			return finalizeErr
		})
	}()
	if err != nil {
		return err
	}
	liveSink, _ := s.sinks()
	for _, event := range inserted {
		notifyLiveSink(liveSink, event)
	}
	var deliveryErr error
	for _, event := range prepared {
		deliveryCtx, cancel := context.WithTimeout(ctx, mailboxBusyRetryWindow)
		err := s.deliverRalphOutbox(deliveryCtx, event.event.ID, ralphSink)
		cancel()
		if err != nil {
			deliveryErr = errors.Join(deliveryErr, err)
		}
	}
	return deliveryErr
}

func (s *SQLiteStore) finalizeRunAttemptOnce(ctx context.Context, finalization AttemptFinalization, outcomeJSON any, events []preparedKernelEvent, trackRalph bool) ([]Event, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("runledger: begin attempt finalization: %w", err)
	}
	defer tx.Rollback()
	lease, err := s.attachmentForMutation(ctx, tx, finalization.SessionID, finalization.RunID, finalization.AttemptID, finalization.LeaseGeneration)
	if err != nil {
		return nil, err
	}
	if err := validateAttachmentGeneration(tx, lease); err != nil {
		return nil, err
	}
	now, err := sqliteDBTime(ctx, tx)
	if err != nil {
		return nil, err
	}

	var existingStatus string
	var existingEnded, existingOutcome sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT status, ended_at, outcome_json FROM agent_runs
		WHERE run_id = ? AND session_id = ?
	`, finalization.RunID, finalization.SessionID).Scan(&existingStatus, &existingEnded, &existingOutcome)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("runledger: inspect finalizing run: %w", err)
	}
	if existingEnded.Valid {
		if existingStatus != finalization.Status || existingOutcome.String != nullableJSONText(outcomeJSON) {
			return nil, fmt.Errorf("runledger: run %s already ended as %s", finalization.RunID, existingStatus)
		}
		if lease.State == agentcoord.AttachmentExpired {
			return nil, ErrAttachmentExpired
		}
		if lease.State != agentcoord.AttachmentDetached {
			return nil, ErrAttachmentStale
		}
		for _, prepared := range events {
			if _, err := verifyPreparedEventTx(ctx, tx, prepared, trackRalph); err != nil {
				return nil, err
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("runledger: commit idempotent attempt finalization: %w", err)
		}
		return nil, nil
	}

	// The first terminal transition is owned only by the exact latest live
	// attempt at SQLite's time. A detached or expired process cannot turn a
	// nonterminal run into a terminal one or release its claims.
	if err := validateCurrentAttachment(tx, lease, now); err != nil {
		return nil, err
	}
	result, updateErr := tx.ExecContext(ctx, `
		UPDATE agent_runs SET status = ?, ended_at = ?, outcome_json = ?
		WHERE run_id = ? AND session_id = ? AND ended_at IS NULL
	`, finalization.Status, sqliteTimestamp(finalization.EndedAt), outcomeJSON, finalization.RunID, finalization.SessionID)
	if updateErr != nil {
		return nil, fmt.Errorf("runledger: finalize run %s: %w", finalization.RunID, updateErr)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		if rowsErr != nil {
			return nil, fmt.Errorf("runledger: inspect finalized run %s: %w", finalization.RunID, rowsErr)
		}
		return nil, fmt.Errorf("runledger: finalize run %s lost its lifecycle compare-and-swap", finalization.RunID)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE agent_claim_locks SET touched_at = ? WHERE lock_key = 'workspace'`, sqliteTimestamp(now)); err != nil {
		return nil, fmt.Errorf("runledger: lock terminal claims: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_claims SET released_at = ?, release_reason = ?
		WHERE run_id = ? AND released_at IS NULL
	`, sqliteTimestamp(finalization.EndedAt), finalization.ReleaseReason, finalization.RunID); err != nil {
		return nil, fmt.Errorf("runledger: release terminal claims: %w", err)
	}
	result, detachErr := tx.ExecContext(ctx, `
		UPDATE agent_run_attempts
		SET state = ?, detached_at = ?, detach_reason = ?
		WHERE session_id = ? AND run_id = ? AND attempt_id = ? AND lease_generation = ? AND state = ?
	`, agentcoord.AttachmentDetached, sqliteTimestamp(finalization.EndedAt), finalization.ReleaseReason,
		finalization.SessionID, finalization.RunID, finalization.AttemptID, finalization.LeaseGeneration, agentcoord.AttachmentAttached)
	if detachErr != nil {
		return nil, fmt.Errorf("runledger: detach terminal attempt: %w", detachErr)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		if rowsErr != nil {
			return nil, fmt.Errorf("runledger: inspect terminal detach: %w", rowsErr)
		}
		return nil, ErrAttachmentStale
	}

	inserted := make([]Event, 0, len(events))
	for _, prepared := range events {
		event, created, appendErr := appendPreparedEventTx(ctx, tx, prepared, trackRalph)
		if appendErr != nil {
			return nil, appendErr
		}
		if created {
			inserted = append(inserted, event)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("runledger: commit attempt finalization: %w", err)
	}
	return inserted, nil
}

func readPreparedEventTx(ctx context.Context, tx *sql.Tx, prepared preparedKernelEvent) (Event, bool, error) {
	event := prepared.event
	var (
		existingRunID, existingType, existingRedaction                                           string
		existingSequence                                                                         int64
		existingTaskID, existingAgentID, existingModelID, existingProviderID                     sql.NullString
		existingBackend, existingSnapshotID, existingPayload, existingEvidence, existingReceipts sql.NullString
	)
	err := tx.QueryRowContext(ctx, `
		SELECT run_id, sequence, event_type, task_id, agent_id, model_id,
		       provider_id, backend, snapshot_id, payload_json,
		       evidence_ids_json, receipt_ids_json, redaction_version
		FROM run_events WHERE event_id = ?
	`, event.ID).Scan(&existingRunID, &existingSequence, &existingType, &existingTaskID,
		&existingAgentID, &existingModelID, &existingProviderID, &existingBackend,
		&existingSnapshotID, &existingPayload, &existingEvidence, &existingReceipts, &existingRedaction)
	if err == nil {
		if existingRunID != event.RunID || existingType != event.Type || existingTaskID.String != event.TaskID ||
			existingAgentID.String != event.AgentID || existingModelID.String != event.ModelID ||
			existingProviderID.String != event.ProviderID || existingBackend.String != event.Backend ||
			existingSnapshotID.String != event.SnapshotID || existingPayload.String != nullableJSONText(prepared.payload) ||
			existingEvidence.String != nullableJSONText(prepared.evidenceIDs) || existingReceipts.String != nullableJSONText(prepared.receiptIDs) ||
			existingRedaction != event.Redaction {
			return Event{}, false, fmt.Errorf("runledger: event id %s conflicts with an existing immutable event", event.ID)
		}
		event.Sequence = existingSequence
		return event, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Event{}, false, fmt.Errorf("runledger: read event id %s: %w", event.ID, err)
	}
	return event, false, nil
}

func verifyPreparedEventTx(ctx context.Context, tx *sql.Tx, prepared preparedKernelEvent, trackRalph bool) (Event, error) {
	event, exists, err := readPreparedEventTx(ctx, tx, prepared)
	if err != nil {
		return Event{}, err
	}
	if !exists {
		return Event{}, fmt.Errorf("runledger: idempotent finalization event %s is missing", prepared.event.ID)
	}
	if err := enqueueRalphOutboxTx(ctx, tx, event.ID, trackRalph); err != nil {
		return Event{}, err
	}
	return event, nil
}

func appendPreparedEventTx(ctx context.Context, tx *sql.Tx, prepared preparedKernelEvent, trackRalph bool) (Event, bool, error) {
	event, exists, err := readPreparedEventTx(ctx, tx, prepared)
	if err != nil {
		return Event{}, false, err
	}
	if exists {
		if err := enqueueRalphOutboxTx(ctx, tx, event.ID, trackRalph); err != nil {
			return Event{}, false, err
		}
		return event, false, nil
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM run_events WHERE run_id = ?`, event.RunID).Scan(&sequence); err != nil {
		return Event{}, false, fmt.Errorf("runledger: compute finalization event sequence: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO run_events (
			event_id, run_id, sequence, event_type, timestamp, task_id, agent_id,
			model_id, provider_id, backend, snapshot_id, payload_json,
			evidence_ids_json, receipt_ids_json, redaction_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, event.ID, event.RunID, sequence, event.Type, sqliteTimestamp(event.Timestamp), nullableStr(event.TaskID), nullableStr(event.AgentID),
		nullableStr(event.ModelID), nullableStr(event.ProviderID), nullableStr(event.Backend), nullableStr(event.SnapshotID),
		prepared.payload, prepared.evidenceIDs, prepared.receiptIDs, event.Redaction)
	if err != nil {
		return Event{}, false, fmt.Errorf("runledger: insert finalization event: %w", err)
	}
	if err := enqueueRalphOutboxTx(ctx, tx, event.ID, trackRalph); err != nil {
		return Event{}, false, err
	}
	event.Sequence = sequence
	return event, true, nil
}
