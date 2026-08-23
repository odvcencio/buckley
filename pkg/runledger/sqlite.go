package runledger

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/storage"
)

// DefaultRedactionVersion is applied to an Event when the caller does not
// set one explicitly.
const DefaultRedactionVersion = "runledger-redaction-v1"

const (
	appendBusyRetryWindow = 5 * time.Second
	appendBusyBaseDelay   = 2 * time.Millisecond
	appendBusyMaxDelay    = 50 * time.Millisecond
	launchFKRestoreWindow = 2 * time.Second
)

// ErrRalphDualWriteFailed wraps a RalphSink error returned alongside a
// successfully persisted Event (section 14.1: Ralph dual-write is best
// effort and must never affect canonical durability).
var ErrRalphDualWriteFailed = errors.New("runledger: ralph dual-write failed")

// SQLiteStore is the SQLite-backed implementation of Store. It follows the
// same connection idiom as pkg/storage.Store and pkg/evidence.SQLiteStore
// (WAL mode, busy_timeout, foreign_keys, private file permissions,
// versioned migrations); see ADR 0001.
//
// context_receipts.bundle_evidence_id / manifest_evidence_id and
// task_checkpoints.markdown_evidence_id logically reference
// evidence_objects(evidence_id), a table owned by pkg/evidence. This store
// does not declare a SQL FOREIGN KEY for those columns, because
// pkg/runledger and pkg/evidence are independent foundation packages that
// may be tested and composed independently; a literal cross-package FK
// would create a hidden migration-order coupling. Referential integrity is
// instead enforced at the application level: CreateContextReceipt and
// CreateTaskCheckpoint look up the referenced evidence_id directly (see
// evidenceRowExists) and return ErrEvidenceNotFound if it is missing. The
// same reasoning applies to agent_runs.session_id, which the spec ties to
// pkg/storage's sessions table.
type SQLiteStore struct {
	db *sql.DB
	// launchReservationNow is a package-private deterministic test seam. A nil
	// value always samples SQLite time on the operation's pinned connection.
	launchReservationNow func(context.Context, launchEnvelopeQueryer) (time.Time, error)

	appendGate chan struct{}
	mu         sync.RWMutex
	liveSink   LiveSink
	ralphSink  RalphSink
}

var _ Store = (*SQLiteStore)(nil)
var _ StepJournal = (*SQLiteStore)(nil)
var _ StepEnumerator = (*SQLiteStore)(nil)
var _ BlockingStepJournal = (*SQLiteStore)(nil)
var _ DispatchStepJournal = (*SQLiteStore)(nil)
var _ FencedStepJournal = (*SQLiteStore)(nil)

// New creates a SQLiteStore backed by SQLite at dbPath, initializing WAL
// mode, foreign keys, and the run ledger schema.
func New(dbPath string) (*SQLiteStore, error) {
	filePath, onDisk := sqliteFilePathFromDSN(dbPath)
	if onDisk {
		if dir := filepath.Dir(filePath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return nil, fmt.Errorf("runledger: create database directory: %w", err)
			}
		}
		if err := ensurePrivateSQLiteFile(filePath); err != nil {
			return nil, err
		}
	}

	// _txlock=immediate acquires the write lock at BEGIN rather than at
	// the first write statement, avoiding the classic SQLite "two readers
	// racing to upgrade to a writer" SQLITE_BUSY, which busy_timeout alone
	// does not resolve. Append relies on this for correct sequence
	// assignment under concurrent writers.
	pragmas := "_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
	dsn := dbPath
	if strings.Contains(dsn, "?") {
		dsn += "&" + pragmas
	} else {
		dsn += "?" + pragmas
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("runledger: open database: %w", err)
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(0)

	if err := storage.EnableSQLiteWAL(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("runledger: enable WAL mode: %w", err)
	}

	if err := runMigrations(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("runledger: run migrations: %w", err)
	}

	return newSQLiteStore(db), nil
}

// NewWithDB wraps an already-open *sql.DB, applying the run ledger schema
// migrations to it. It is intended for composing the run ledger onto a
// shared database connection (for example, one already holding
// pkg/evidence's schema, which enables the application-level evidence
// reference checks described on SQLiteStore) once wiring lands in a later
// PR. The caller owns the connection's lifecycle and non-foreign-key pragma
// configuration. NewWithDB probes foreign-key support before migrations and
// restores the caller's setting; launch-envelope operations additionally
// enable and verify it on the exact pooled connection used for each operation.
func NewWithDB(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("runledger: db cannot be nil")
	}
	if err := enableAndVerifySQLiteForeignKeys(db); err != nil {
		return nil, err
	}
	if err := runMigrations(db); err != nil {
		return nil, fmt.Errorf("runledger: run migrations: %w", err)
	}
	return newSQLiteStore(db), nil
}

func enableAndVerifySQLiteForeignKeys(db *sql.DB) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := acquireLaunchForeignKeyConn(ctx, db)
	if err != nil {
		return err
	}
	return conn.Close()
}

type launchForeignKeyConn struct {
	*sql.Conn
	original int
}

// acquireLaunchForeignKeyConn pins one pooled connection and verifies the
// launch-envelope foreign-key invariant before returning it. Callers must keep
// all v21 work on this exact connection and close it afterward. Close restores
// the caller's original setting before the physical connection can reenter its
// pool.
func acquireLaunchForeignKeyConn(ctx context.Context, db *sql.DB) (*launchForeignKeyConn, error) {
	if db == nil {
		return nil, errors.New("runledger: SQLite database is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("runledger: acquire SQLite foreign-key connection: %w", err)
	}
	lease := &launchForeignKeyConn{Conn: conn}
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&lease.original); err != nil {
		return nil, errors.Join(fmt.Errorf("runledger: read SQLite foreign-key setting: %w", err), discardLaunchForeignKeyConn(conn))
	}
	if lease.original != 0 && lease.original != 1 {
		return nil, errors.Join(errors.New("runledger: SQLite foreign-key setting is invalid"), discardLaunchForeignKeyConn(conn))
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return nil, errors.Join(fmt.Errorf("runledger: enable SQLite foreign keys: %w", err), lease.Close())
	}
	var enabled int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return nil, errors.Join(fmt.Errorf("runledger: verify SQLite foreign keys: %w", err), lease.Close())
	}
	if enabled != 1 {
		return nil, errors.Join(errors.New("runledger: SQLite foreign-key enforcement is unavailable"), lease.Close())
	}
	return lease, nil
}

func (c *launchForeignKeyConn) Close() error {
	if c == nil || c.Conn == nil {
		return nil
	}
	conn := c.Conn
	c.Conn = nil
	ctx, cancel := context.WithTimeout(context.Background(), launchFKRestoreWindow)
	defer cancel()
	setting := "OFF"
	if c.original == 1 {
		setting = "ON"
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = `+setting); err != nil {
		return errors.Join(fmt.Errorf("runledger: restore SQLite foreign-key setting: %w", err), discardLaunchForeignKeyConn(conn))
	}
	var restored int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&restored); err != nil {
		return errors.Join(fmt.Errorf("runledger: verify restored SQLite foreign-key setting: %w", err), discardLaunchForeignKeyConn(conn))
	}
	if restored != c.original {
		return errors.Join(errors.New("runledger: restored SQLite foreign-key setting does not match"), discardLaunchForeignKeyConn(conn))
	}
	return conn.Close()
}

func (c *launchForeignKeyConn) discard() error {
	if c == nil || c.Conn == nil {
		return nil
	}
	conn := c.Conn
	c.Conn = nil
	return discardLaunchForeignKeyConn(conn)
}

func closeLaunchForeignKeyConn(conn *launchForeignKeyConn, rollback bool) error {
	if conn == nil || conn.Conn == nil {
		return nil
	}
	if rollback {
		ctx, cancel := context.WithTimeout(context.Background(), launchFKRestoreWindow)
		_, err := conn.ExecContext(ctx, `ROLLBACK`)
		cancel()
		if err != nil {
			return errors.Join(fmt.Errorf("runledger: rollback launch admission transaction: %w", err), conn.discard())
		}
	}
	return conn.Close()
}

func discardLaunchForeignKeyConn(conn *sql.Conn) error {
	if conn == nil {
		return nil
	}
	rawErr := conn.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(rawErr, driver.ErrBadConn) {
		rawErr = nil
	}
	return errors.Join(rawErr, conn.Close())
}

func newSQLiteStore(db *sql.DB) *SQLiteStore {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return &SQLiteStore{db: db, appendGate: gate}
}

// Close closes the underlying database connection opened by New. It must
// not be called on a store constructed with NewWithDB.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// DB returns the underlying database connection, primarily for tests and
// for composing pkg/evidence onto the same database file.
func (s *SQLiteStore) DB() *sql.DB {
	return s.db
}

// SetLiveSink implements Store.
func (s *SQLiteStore) SetLiveSink(sink LiveSink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.liveSink = sink
}

// SetRalphSink implements Store.
func (s *SQLiteStore) SetRalphSink(sink RalphSink) {
	if s == nil {
		return
	}
	<-s.appendGate
	s.mu.Lock()
	s.ralphSink = sink
	s.mu.Unlock()
	s.appendGate <- struct{}{}
	if sink != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.drainRalphOutbox(ctx, sink, 64)
	}
}

func (s *SQLiteStore) sinks() (LiveSink, RalphSink) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.liveSink, s.ralphSink
}

// StartRun implements Store.
func (s *SQLiteStore) StartRun(ctx context.Context, run AgentRun) (AgentRun, error) {
	if reservedAgentIdentity(run.RunID) || reservedAgentIdentity(run.ParentRunID) {
		return AgentRun{}, fmt.Errorf("%w: run_id=%q parent_run_id=%q", ErrReservedAgentIdentity, strings.TrimSpace(run.RunID), strings.TrimSpace(run.ParentRunID))
	}
	if run.RunID == "" {
		run.RunID = "run_" + ulid.Make().String()
	}
	if run.SessionID == "" {
		return AgentRun{}, fmt.Errorf("runledger: session_id is required")
	}
	if run.Status == "" {
		run.Status = "running"
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}

	budgetJSON, err := marshalJSONMap(run.Budget)
	if err != nil {
		return AgentRun{}, fmt.Errorf("runledger: marshal budget: %w", err)
	}
	outcomeJSON, err := marshalJSONMap(run.Outcome)
	if err != nil {
		return AgentRun{}, fmt.Errorf("runledger: marshal outcome: %w", err)
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO agent_runs (
			run_id, session_id, parent_run_id, task_id, agent_id, model_id,
			provider_id, backend, status, started_at, ended_at, budget_json, outcome_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, run.RunID, run.SessionID, nullableStr(run.ParentRunID), nullableStr(run.TaskID), nullableStr(run.AgentID),
		nullableStr(run.ModelID), nullableStr(run.ProviderID), nullableStr(run.Backend), run.Status,
		sqliteTimestamp(run.StartedAt), nullableTime(run.EndedAt), budgetJSON, outcomeJSON)
	if err != nil {
		return AgentRun{}, fmt.Errorf("runledger: insert run: %w", err)
	}
	return run, nil
}

// EndRun implements Store.
func (s *SQLiteStore) EndRun(ctx context.Context, runID, status string, endedAt time.Time, outcome map[string]any) error {
	if status == "" {
		return fmt.Errorf("runledger: status is required")
	}
	outcomeJSON, err := marshalJSONMap(outcome)
	if err != nil {
		return fmt.Errorf("runledger: marshal outcome: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE agent_runs SET status = ?, ended_at = ?, outcome_json = ?
		WHERE run_id = ? AND ended_at IS NULL
	`, status, sqliteTimestamp(endedAt), outcomeJSON, runID)
	if err != nil {
		return fmt.Errorf("runledger: end run %s: %w", runID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("runledger: end run %s: %w", runID, err)
	}
	if n == 0 {
		existing, getErr := s.GetRun(ctx, runID)
		if getErr != nil {
			return getErr
		}
		if existing.EndedAt != nil && existing.Status == status {
			return nil
		}
		if existing.EndedAt != nil {
			return fmt.Errorf("runledger: run %s already ended as %s", runID, existing.Status)
		}
		return fmt.Errorf("runledger: end run %s did not update its lifecycle", runID)
	}
	return nil
}

// Append implements Store. It assigns the event's sequence number
// authoritatively; any caller-supplied Sequence is overwritten, since
// strict per-run ordering can only be guaranteed by the store itself under
// concurrent writers.
func (s *SQLiteStore) Append(ctx context.Context, event Event) (Event, error) {
	if event.RunID == "" {
		return Event{}, fmt.Errorf("runledger: run_id is required")
	}
	if event.Type == "" {
		return Event{}, fmt.Errorf("runledger: type is required")
	}
	if event.ID == "" {
		event.ID = NewEventID()
	}
	if event.SchemaVersion == "" {
		event.SchemaVersion = SchemaVersion
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.Redaction == "" {
		event.Redaction = DefaultRedactionVersion
	}

	payloadJSON, err := marshalJSONMap(event.Payload)
	if err != nil {
		return Event{}, fmt.Errorf("runledger: marshal payload: %w", err)
	}
	evidenceIDsJSON, err := marshalJSONStrings(event.EvidenceIDs)
	if err != nil {
		return Event{}, fmt.Errorf("runledger: marshal evidence_ids: %w", err)
	}
	receiptIDsJSON, err := marshalJSONStrings(event.ReceiptIDs)
	if err != nil {
		return Event{}, fmt.Errorf("runledger: marshal receipt_ids: %w", err)
	}

	liveSink, _ := s.sinks()
	event, inserted, ralphSink, err := s.appendWithGate(ctx, event, payloadJSON, evidenceIDsJSON, receiptIDsJSON)
	if err != nil {
		return Event{}, err
	}
	if inserted {
		notifyLiveSink(liveSink, event)
	}
	if err := s.deliverRalphOutbox(ctx, event.ID, ralphSink); err != nil {
		return event, err
	}
	return event, nil
}

func (s *SQLiteStore) appendWithGate(ctx context.Context, event Event, payloadJSON, evidenceIDsJSON, receiptIDsJSON any) (Event, bool, RalphSink, error) {
	// SQLite permits only one writer. Serializing this store's appenders avoids
	// wasteful read-to-write upgrade collisions on externally owned connections,
	// while the retry below still covers writers using another store wrapper.
	select {
	case <-ctx.Done():
		return Event{}, false, nil, fmt.Errorf("runledger: append interrupted: %w", ctx.Err())
	case <-s.appendGate:
	}
	defer func() { s.appendGate <- struct{}{} }()
	_, ralphSink := s.sinks()
	appended, inserted, err := s.appendWithBusyRetry(ctx, event, payloadJSON, evidenceIDsJSON, receiptIDsJSON, ralphSink != nil)
	return appended, inserted, ralphSink, err
}

func (s *SQLiteStore) appendWithBusyRetry(ctx context.Context, event Event, payloadJSON, evidenceIDsJSON, receiptIDsJSON any, trackRalph bool) (Event, bool, error) {
	retryCtx, cancel := context.WithTimeout(ctx, appendBusyRetryWindow)
	defer cancel()

	var lastBusyErr error
	for attempt := 0; ; attempt++ {
		appended, inserted, err := s.appendOnce(retryCtx, event, payloadJSON, evidenceIDsJSON, receiptIDsJSON, trackRalph)
		if err == nil {
			return appended, inserted, nil
		}
		if !storage.IsSQLiteBusyError(err) {
			if ctx.Err() != nil {
				return Event{}, false, fmt.Errorf("runledger: append retry interrupted: %w", ctx.Err())
			}
			if retryCtx.Err() != nil && lastBusyErr != nil {
				return Event{}, false, fmt.Errorf("runledger: append busy retry exhausted after %s: %w", appendBusyRetryWindow, lastBusyErr)
			}
			return Event{}, false, err
		}
		lastBusyErr = err
		if retryCtx.Err() != nil {
			if ctx.Err() != nil {
				return Event{}, false, fmt.Errorf("runledger: append retry interrupted: %w", ctx.Err())
			}
			return Event{}, false, fmt.Errorf("runledger: append busy retry exhausted after %s: %w", appendBusyRetryWindow, lastBusyErr)
		}

		delay := appendBusyMaxDelay
		if attempt < 5 {
			delay = appendBusyBaseDelay << attempt
		}
		if delay > appendBusyMaxDelay {
			delay = appendBusyMaxDelay
		}
		if event.ID != "" {
			delay += time.Duration(event.ID[len(event.ID)-1]%7) * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Event{}, false, fmt.Errorf("runledger: append retry interrupted: %w", ctx.Err())
		case <-retryCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return Event{}, false, fmt.Errorf("runledger: append retry interrupted: %w", ctx.Err())
			}
			return Event{}, false, fmt.Errorf("runledger: append busy retry exhausted after %s: %w", appendBusyRetryWindow, lastBusyErr)
		case <-timer.C:
		}
	}
}

func (s *SQLiteStore) appendOnce(ctx context.Context, event Event, payloadJSON, evidenceIDsJSON, receiptIDsJSON any, trackRalph bool) (Event, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, false, fmt.Errorf("runledger: begin append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		existingRunID      string
		existingSequence   int64
		existingType       string
		existingTaskID     sql.NullString
		existingAgentID    sql.NullString
		existingModelID    sql.NullString
		existingProviderID sql.NullString
		existingBackend    sql.NullString
		existingSnapshotID sql.NullString
		existingPayload    sql.NullString
		existingEvidence   sql.NullString
		existingReceipts   sql.NullString
		existingRedaction  string
	)
	err = tx.QueryRowContext(ctx, `
		SELECT run_id, sequence, event_type, task_id, agent_id, model_id,
		       provider_id, backend, snapshot_id, payload_json,
		       evidence_ids_json, receipt_ids_json, redaction_version
		FROM run_events WHERE event_id = ?
	`, event.ID).Scan(
		&existingRunID, &existingSequence, &existingType, &existingTaskID,
		&existingAgentID, &existingModelID, &existingProviderID,
		&existingBackend, &existingSnapshotID, &existingPayload,
		&existingEvidence, &existingReceipts, &existingRedaction,
	)
	if err == nil {
		if existingRunID != event.RunID || existingType != event.Type ||
			existingTaskID.String != event.TaskID || existingAgentID.String != event.AgentID ||
			existingModelID.String != event.ModelID || existingProviderID.String != event.ProviderID ||
			existingBackend.String != event.Backend || existingSnapshotID.String != event.SnapshotID ||
			existingPayload.String != nullableJSONText(payloadJSON) || existingEvidence.String != nullableJSONText(evidenceIDsJSON) ||
			existingReceipts.String != nullableJSONText(receiptIDsJSON) || existingRedaction != event.Redaction {
			return Event{}, false, fmt.Errorf("runledger: event id %s conflicts with an existing immutable event", event.ID)
		}
		if err := enqueueRalphOutboxTx(ctx, tx, event.ID, trackRalph); err != nil {
			return Event{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return Event{}, false, fmt.Errorf("runledger: commit idempotent append: %w", err)
		}
		event.Sequence = existingSequence
		return event, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Event{}, false, fmt.Errorf("runledger: read event id %s: %w", event.ID, err)
	}

	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM run_events WHERE run_id = ?`, event.RunID).Scan(&seq); err != nil {
		return Event{}, false, fmt.Errorf("runledger: compute sequence: %w", err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO run_events (
			event_id, run_id, sequence, event_type, timestamp, task_id, agent_id,
			model_id, provider_id, backend, snapshot_id, payload_json,
			evidence_ids_json, receipt_ids_json, redaction_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, event.ID, event.RunID, seq, event.Type, sqliteTimestamp(event.Timestamp), nullableStr(event.TaskID),
		nullableStr(event.AgentID), nullableStr(event.ModelID), nullableStr(event.ProviderID), nullableStr(event.Backend),
		nullableStr(event.SnapshotID), payloadJSON, evidenceIDsJSON, receiptIDsJSON, event.Redaction)
	if err != nil {
		return Event{}, false, fmt.Errorf("runledger: insert event: %w", err)
	}
	if err := enqueueRalphOutboxTx(ctx, tx, event.ID, trackRalph); err != nil {
		return Event{}, false, err
	}

	if err := tx.Commit(); err != nil {
		return Event{}, false, fmt.Errorf("runledger: commit append: %w", err)
	}
	event.Sequence = seq
	return event, true, nil
}

// BeginStep implements StepJournal. Completed and blocked logical steps are
// immutable from the replay perspective: callers receive them with replay=true
// instead of executing the side effect again. Failed steps advance their
// attempt count while retaining the stable StepID and idempotency key; an
// existing started attempt remains owned and fails closed as in progress.
func (s *SQLiteStore) BeginStep(ctx context.Context, step ExecutionStep) (ExecutionStep, bool, error) {
	if step.IdempotencyKey == "" {
		step.IdempotencyKey = step.StepID
	}
	if err := validateExecutionStep(step); err != nil {
		return ExecutionStep{}, false, err
	}
	if err := s.guardGenericLaunchStepMutation(ctx, step.RunID, step.StepID); err != nil {
		return ExecutionStep{}, false, err
	}
	if step.Attempt <= 0 {
		step.Attempt = 1
	}
	if step.StartedAt.IsZero() {
		step.StartedAt = time.Now().UTC()
	}

	res, err := s.db.ExecContext(ctx, `
		INSERT INTO execution_steps (
			run_id, task_id, step_id, kind, idempotency_key, status, attempt,
			claim_generation, input_digest, dispatch_state, started_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, step_id) DO NOTHING
	`, step.RunID, nullableStr(step.TaskID), step.StepID, step.Kind, step.IdempotencyKey,
		StepStarted, step.Attempt, 1, nullableStr(step.InputDigest), StepDispatchClaimed, sqliteTimestamp(step.StartedAt))
	if err != nil {
		return ExecutionStep{}, false, fmt.Errorf("runledger: insert execution step %s: %w", step.StepID, err)
	}
	inserted, err := res.RowsAffected()
	if err != nil {
		return ExecutionStep{}, false, fmt.Errorf("runledger: inspect inserted execution step %s: %w", step.StepID, err)
	}
	if inserted == 1 {
		step.Status = StepStarted
		step.ClaimGeneration = 1
		step.DispatchState = StepDispatchClaimed
		return step, false, nil
	}

	for {
		existing, err := s.GetStep(ctx, step.RunID, step.StepID)
		if err != nil {
			return ExecutionStep{}, false, fmt.Errorf("runledger: read execution step %s: %w", step.StepID, err)
		}
		if err := s.guardGenericLaunchStepMutation(ctx, step.RunID, step.StepID); err != nil {
			return ExecutionStep{}, false, err
		}
		if existing.IdempotencyKey != step.IdempotencyKey {
			return ExecutionStep{}, false, fmt.Errorf("runledger: execution step %s idempotency key changed", step.StepID)
		}
		if existing.Kind != step.Kind {
			return ExecutionStep{}, false, fmt.Errorf("runledger: execution step %s kind changed", step.StepID)
		}
		if existing.InputDigest != "" && step.InputDigest != "" && existing.InputDigest != step.InputDigest {
			return ExecutionStep{}, false, fmt.Errorf("runledger: execution step %s input digest changed", step.StepID)
		}
		if existing.InputDigest != "" && strings.TrimSpace(step.InputDigest) == "" {
			return ExecutionStep{}, false, fmt.Errorf("runledger: execution step %s input digest is required to resume", step.StepID)
		}
		switch existing.Status {
		case StepCompleted, StepBlocked:
			return existing, true, nil
		case StepStarted:
			return existing, false, RecoveryErrorForStep(existing)
		case StepFailed:
			if strings.TrimSpace(step.InputDigest) == "" {
				return ExecutionStep{}, false, fmt.Errorf("runledger: execution step %s input digest is required to retry a failed step", step.StepID)
			}
			res, err := s.db.ExecContext(ctx, `
				UPDATE execution_steps
				SET status = ?, attempt = attempt + 1, claim_generation = claim_generation + 1,
				    input_digest = ?, output_digest = NULL,
				    output_evidence_id = NULL, error_text = NULL, dispatch_state = ?,
				    started_at = ?, completed_at = NULL
				WHERE run_id = ? AND step_id = ? AND status = ? AND attempt = ? AND claim_generation = ?
			`, StepStarted, nullableStr(step.InputDigest), StepDispatchClaimed, sqliteTimestamp(step.StartedAt),
				existing.RunID, existing.StepID, StepFailed, existing.Attempt, existing.ClaimGeneration)
			if err != nil {
				return ExecutionStep{}, false, fmt.Errorf("runledger: restart execution step %s: %w", step.StepID, err)
			}
			updated, err := res.RowsAffected()
			if err != nil {
				return ExecutionStep{}, false, fmt.Errorf("runledger: inspect restarted execution step %s: %w", step.StepID, err)
			}
			if updated == 0 {
				continue
			}
			existing.Attempt++
			existing.ClaimGeneration++
			existing.Status = StepStarted
			existing.InputDigest = step.InputDigest
			existing.OutputDigest = ""
			existing.OutputEvidenceID = ""
			existing.Error = ""
			existing.DispatchState = StepDispatchClaimed
			existing.StartedAt = step.StartedAt
			existing.CompletedAt = nil
			return existing, false, nil
		default:
			return ExecutionStep{}, false, fmt.Errorf("runledger: execution step %s has unknown status %q", step.StepID, existing.Status)
		}
	}
}

// CompleteStep implements StepJournal.
func (s *SQLiteStore) CompleteStep(ctx context.Context, runID, stepID, outputEvidenceID, outputDigest string, completedAt time.Time) error {
	step, err := s.GetStep(ctx, runID, stepID)
	if err != nil {
		return err
	}
	if step.Attempt != 1 || step.ClaimGeneration != 1 {
		return fmt.Errorf("%w: step %s is attempt %d claim %d", ErrStepAttemptRequired, stepID, step.Attempt, step.ClaimGeneration)
	}
	return s.CompleteStepAttempt(ctx, step, outputEvidenceID, outputDigest, completedAt)
}

// FailStep implements StepJournal.
func (s *SQLiteStore) FailStep(ctx context.Context, runID, stepID, failure string, completedAt time.Time) error {
	step, err := s.GetStep(ctx, runID, stepID)
	if err != nil {
		return err
	}
	if step.Attempt != 1 || step.ClaimGeneration != 1 {
		return fmt.Errorf("%w: step %s is attempt %d claim %d", ErrStepAttemptRequired, stepID, step.Attempt, step.ClaimGeneration)
	}
	return s.FailStepAttempt(ctx, step, failure, completedAt)
}

// CompleteStepAttempt implements BlockingStepJournal.
func (s *SQLiteStore) CompleteStepAttempt(ctx context.Context, step ExecutionStep, outputEvidenceID, outputDigest string, completedAt time.Time) error {
	return s.transitionStep(ctx, step, StepCompleted, "", outputEvidenceID, outputDigest, completedAt)
}

// FailStepAttempt implements BlockingStepJournal.
func (s *SQLiteStore) FailStepAttempt(ctx context.Context, step ExecutionStep, failure string, completedAt time.Time) error {
	return s.transitionStep(ctx, step, StepFailed, failure, "", "", completedAt)
}

// BlockStep implements BlockingStepJournal.
func (s *SQLiteStore) BlockStep(ctx context.Context, step ExecutionStep, failure, outputEvidenceID, outputDigest string, completedAt time.Time) error {
	if strings.TrimSpace(failure) == "" {
		return fmt.Errorf("runledger: failure is required to block an execution step")
	}
	return s.transitionStep(ctx, step, StepBlocked, failure, outputEvidenceID, outputDigest, completedAt)
}

// MarkStepDispatched implements DispatchStepJournal.
func (s *SQLiteStore) MarkStepDispatched(ctx context.Context, step ExecutionStep, dispatchedAt time.Time) error {
	if step.RunID == "" || step.StepID == "" {
		return fmt.Errorf("runledger: run_id and step_id are required to mark an execution step dispatched")
	}
	if step.Attempt <= 0 {
		return fmt.Errorf("runledger: a positive attempt is required for execution step %s", step.StepID)
	}
	if err := s.guardGenericLaunchStepMutation(ctx, step.RunID, step.StepID); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE execution_steps
		SET dispatch_state = ?
		WHERE run_id = ? AND step_id = ? AND status = ? AND attempt = ? AND claim_generation = ?
		  AND dispatch_state = ?
	`, StepDispatchDispatched, step.RunID, step.StepID, StepStarted, step.Attempt, step.ClaimGeneration, StepDispatchClaimed)
	if err != nil {
		return fmt.Errorf("runledger: mark execution step %s dispatched: %w", step.StepID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("runledger: inspect execution step %s dispatch mark: %w", step.StepID, err)
	}
	if n == 1 {
		return nil
	}
	if err := s.guardGenericLaunchStepMutation(ctx, step.RunID, step.StepID); err != nil {
		return err
	}
	existing, err := s.GetStep(ctx, step.RunID, step.StepID)
	if err != nil {
		return err
	}
	if existing.Status == StepStarted && existing.Attempt == step.Attempt && existing.ClaimGeneration == step.ClaimGeneration && existing.DispatchState == StepDispatchDispatched {
		return nil
	}
	return fmt.Errorf("%w: step %s attempt %d is %s attempt %d with dispatch state %q", ErrStepTransitionConflict, step.StepID, step.Attempt, existing.Status, existing.Attempt, existing.DispatchState)
}

// ReclaimStep implements FencedStepJournal.
func (s *SQLiteStore) ReclaimStep(ctx context.Context, step ExecutionStep, reclaimedAt time.Time) (ExecutionStep, error) {
	if step.RunID == "" || step.StepID == "" || step.Attempt <= 0 || step.ClaimGeneration <= 0 {
		return ExecutionStep{}, fmt.Errorf("runledger: complete step identity is required to reclaim %s", step.StepID)
	}
	if err := s.guardGenericLaunchStepMutation(ctx, step.RunID, step.StepID); err != nil {
		return ExecutionStep{}, err
	}
	if reclaimedAt.IsZero() {
		reclaimedAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE execution_steps
		SET claim_generation = claim_generation + 1, started_at = ?
		WHERE run_id = ? AND step_id = ? AND status = ? AND attempt = ?
		  AND claim_generation = ? AND dispatch_state = ?
	`, sqliteTimestamp(reclaimedAt), step.RunID, step.StepID, StepStarted, step.Attempt, step.ClaimGeneration, StepDispatchClaimed)
	if err != nil {
		return ExecutionStep{}, fmt.Errorf("runledger: reclaim execution step %s: %w", step.StepID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return ExecutionStep{}, fmt.Errorf("runledger: inspect execution step %s reclaim: %w", step.StepID, err)
	}
	if n != 1 {
		if err := s.guardGenericLaunchStepMutation(ctx, step.RunID, step.StepID); err != nil {
			return ExecutionStep{}, err
		}
		existing, loadErr := s.GetStep(ctx, step.RunID, step.StepID)
		if loadErr != nil {
			return ExecutionStep{}, loadErr
		}
		return ExecutionStep{}, fmt.Errorf("%w: step %s attempt %d claim %d is %s attempt %d claim %d with dispatch state %q", ErrStepTransitionConflict, step.StepID, step.Attempt, step.ClaimGeneration, existing.Status, existing.Attempt, existing.ClaimGeneration, existing.DispatchState)
	}
	step.ClaimGeneration++
	step.StartedAt = reclaimedAt
	return step, nil
}

func (s *SQLiteStore) transitionStep(ctx context.Context, step ExecutionStep, status, failure, outputEvidenceID, outputDigest string, completedAt time.Time) error {
	if step.RunID == "" || step.StepID == "" {
		return fmt.Errorf("runledger: run_id and step_id are required for an execution step transition")
	}
	if step.Attempt <= 0 {
		return fmt.Errorf("runledger: a positive attempt is required for execution step %s", step.StepID)
	}
	if err := s.guardGenericLaunchStepMutation(ctx, step.RunID, step.StepID); err != nil {
		return err
	}
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	query := `
		UPDATE execution_steps
		SET status = ?, output_evidence_id = ?, output_digest = ?, error_text = ?, completed_at = ?
		WHERE run_id = ? AND step_id = ? AND status = ? AND attempt = ? AND claim_generation = ?`
	args := []any{status, nullableStr(outputEvidenceID), nullableStr(outputDigest), nullableStr(failure), sqliteTimestamp(completedAt),
		step.RunID, step.StepID, StepStarted, step.Attempt, step.ClaimGeneration}
	if status == StepFailed {
		query += ` AND dispatch_state = ?`
		args = append(args, StepDispatchClaimed)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("runledger: transition execution step %s to %s: %w", step.StepID, status, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("runledger: inspect execution step %s transition to %s: %w", step.StepID, status, err)
	}
	if n == 1 {
		return nil
	}
	if err := s.guardGenericLaunchStepMutation(ctx, step.RunID, step.StepID); err != nil {
		return err
	}
	existing, err := s.GetStep(ctx, step.RunID, step.StepID)
	if err != nil {
		return err
	}
	if existing.Status == status && existing.Attempt == step.Attempt && existing.ClaimGeneration == step.ClaimGeneration && existing.Error == failure && existing.OutputEvidenceID == outputEvidenceID && existing.OutputDigest == outputDigest {
		return nil
	}
	return fmt.Errorf("%w: step %s attempt %d is %s attempt %d", ErrStepTransitionConflict, step.StepID, step.Attempt, existing.Status, existing.Attempt)
}

// GetStep implements StepJournal.
func (s *SQLiteStore) GetStep(ctx context.Context, runID, stepID string) (ExecutionStep, error) {
	if runID == "" || stepID == "" {
		return ExecutionStep{}, fmt.Errorf("runledger: run_id and step_id are required to read an execution step")
	}
	return scanExecutionStep(s.db.QueryRowContext(ctx, `
		SELECT run_id, task_id, step_id, kind, idempotency_key, status, attempt,
		       claim_generation, input_digest, output_digest, output_evidence_id, error_text, dispatch_state,
		       started_at, completed_at
		FROM execution_steps WHERE run_id = ? AND step_id = ?
	`, runID, stepID))
}

// ListSteps implements StepEnumerator. Results are stable by logical step ID
// so replay reports and tests do not depend on SQLite's row order.
func (s *SQLiteStore) ListSteps(ctx context.Context, runID string) ([]ExecutionStep, error) {
	if strings.TrimSpace(runID) == "" {
		return nil, fmt.Errorf("runledger: run_id is required to list execution steps")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT run_id, task_id, step_id, kind, idempotency_key, status, attempt,
		       claim_generation, input_digest, output_digest, output_evidence_id, error_text, dispatch_state,
		       started_at, completed_at
		FROM execution_steps WHERE run_id = ? ORDER BY step_id
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("runledger: list execution steps: %w", err)
	}
	defer rows.Close()
	var steps []ExecutionStep
	for rows.Next() {
		step, scanErr := scanExecutionStep(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("runledger: iterate execution steps: %w", err)
	}
	return steps, nil
}

type executionStepScanner interface {
	Scan(dest ...any) error
}

func scanExecutionStep(row executionStepScanner) (ExecutionStep, error) {
	var (
		step           ExecutionStep
		taskID         sql.NullString
		idempotency    sql.NullString
		inputDigest    sql.NullString
		outputDigest   sql.NullString
		outputEvidence sql.NullString
		failure        sql.NullString
		dispatchState  sql.NullString
		startedAtRaw   string
		completedRaw   sql.NullString
	)
	err := row.Scan(&step.RunID, &taskID, &step.StepID, &step.Kind, &idempotency, &step.Status, &step.Attempt, &step.ClaimGeneration,
		&inputDigest, &outputDigest, &outputEvidence, &failure, &dispatchState, &startedAtRaw, &completedRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutionStep{}, ErrStepNotFound
	}
	if err != nil {
		return ExecutionStep{}, fmt.Errorf("runledger: scan execution step: %w", err)
	}
	step.TaskID = taskID.String
	step.IdempotencyKey = idempotency.String
	step.InputDigest = inputDigest.String
	step.OutputDigest = outputDigest.String
	step.OutputEvidenceID = outputEvidence.String
	step.Error = failure.String
	step.DispatchState = dispatchState.String
	step.StartedAt = parseSQLiteTimestamp(startedAtRaw)
	if completedRaw.Valid && completedRaw.String != "" {
		completed := parseSQLiteTimestamp(completedRaw.String)
		step.CompletedAt = &completed
	}
	return step, nil
}

// notifyLiveSink calls sink.Notify, recovering from any panic so a broken
// live sink can never affect the durable write that already committed.
func notifyLiveSink(sink LiveSink, event Event) {
	if sink == nil {
		return
	}
	defer func() { _ = recover() }()
	sink.Notify(event)
}

// CreateContextReceipt implements Store.
func (s *SQLiteStore) CreateContextReceipt(ctx context.Context, receipt ContextReceipt, items []ContextReceiptItem) (ContextReceipt, error) {
	if receipt.ReceiptID == "" {
		receipt.ReceiptID = "ctx_" + ulid.Make().String()
	}
	if receipt.CreatedAt.IsZero() {
		receipt.CreatedAt = time.Now().UTC()
	}

	for _, evidenceID := range []string{receipt.BundleEvidenceID, receipt.ManifestEvidenceID} {
		if evidenceID == "" {
			continue
		}
		exists, err := evidenceRowExists(ctx, s.db, evidenceID)
		if err != nil {
			return ContextReceipt{}, err
		}
		if !exists {
			return ContextReceipt{}, fmt.Errorf("%w: %s", ErrEvidenceNotFound, evidenceID)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ContextReceipt{}, fmt.Errorf("runledger: begin create receipt: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO context_receipts (
			receipt_id, parent_receipt_id, session_id, run_id, task_id, snapshot_id,
			request_digest, policy_version, budget_tokens, estimated_tokens, candidate_tokens,
			bundle_evidence_id, manifest_evidence_id, bundle_sha256, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, receipt.ReceiptID, nullableStr(receipt.ParentReceiptID), nullableStr(receipt.SessionID), nullableStr(receipt.RunID),
		nullableStr(receipt.TaskID), receipt.SnapshotID, receipt.RequestDigest, receipt.PolicyVersion, receipt.BudgetTokens,
		receipt.EstimatedTokens, receipt.CandidateTokens, receipt.BundleEvidenceID, receipt.ManifestEvidenceID,
		receipt.BundleSHA256, sqliteTimestamp(receipt.CreatedAt))
	if err != nil {
		return ContextReceipt{}, fmt.Errorf("runledger: insert receipt: %w", err)
	}

	for i, item := range items {
		reasonsJSON, err := marshalReasons(item.Reasons)
		if err != nil {
			return ContextReceipt{}, fmt.Errorf("runledger: marshal reasons: %w", err)
		}
		required := 0
		if item.Required {
			required = 1
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO context_receipt_items (
				receipt_id, ordinal, item_id, entity_id, path, section, projection_mode,
				start_line, end_line, content_sha256, raw_tokens, projected_tokens, score,
				reasons_json, required
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, receipt.ReceiptID, i, item.ItemID, nullableStr(item.EntityID), nullableStr(item.Path), item.Section, item.Mode,
			nullableInt(item.StartLine), nullableInt(item.EndLine), item.ContentSHA256, nullableInt(item.RawTokens),
			nullableInt(item.ProjectedTokens), nullableInt(item.Score), reasonsJSON, required)
		if err != nil {
			return ContextReceipt{}, fmt.Errorf("runledger: insert receipt item %d: %w", i, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return ContextReceipt{}, fmt.Errorf("runledger: commit create receipt: %w", err)
	}
	return receipt, nil
}

// GetContextReceipt implements Store.
func (s *SQLiteStore) GetContextReceipt(ctx context.Context, receiptID string) (ContextReceipt, []ContextReceiptItem, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT receipt_id, parent_receipt_id, session_id, run_id, task_id, snapshot_id,
		       request_digest, policy_version, budget_tokens, estimated_tokens, candidate_tokens,
		       bundle_evidence_id, manifest_evidence_id, bundle_sha256, created_at
		FROM context_receipts WHERE receipt_id = ?
	`, receiptID)

	var (
		receipt         ContextReceipt
		parentReceiptID sql.NullString
		sessionID       sql.NullString
		runID           sql.NullString
		taskID          sql.NullString
		createdAtRaw    string
	)
	err := row.Scan(&receipt.ReceiptID, &parentReceiptID, &sessionID, &runID, &taskID, &receipt.SnapshotID,
		&receipt.RequestDigest, &receipt.PolicyVersion, &receipt.BudgetTokens, &receipt.EstimatedTokens,
		&receipt.CandidateTokens, &receipt.BundleEvidenceID, &receipt.ManifestEvidenceID, &receipt.BundleSHA256, &createdAtRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return ContextReceipt{}, nil, ErrNotFound
	}
	if err != nil {
		return ContextReceipt{}, nil, fmt.Errorf("runledger: scan receipt: %w", err)
	}
	receipt.ParentReceiptID = parentReceiptID.String
	receipt.SessionID = sessionID.String
	receipt.RunID = runID.String
	receipt.TaskID = taskID.String
	receipt.CreatedAt = parseSQLiteTimestamp(createdAtRaw)

	rows, err := s.db.QueryContext(ctx, `
		SELECT ordinal, item_id, entity_id, path, section, projection_mode, start_line, end_line,
		       content_sha256, raw_tokens, projected_tokens, score, reasons_json, required
		FROM context_receipt_items WHERE receipt_id = ? ORDER BY ordinal ASC
	`, receiptID)
	if err != nil {
		return ContextReceipt{}, nil, fmt.Errorf("runledger: list receipt items: %w", err)
	}
	defer rows.Close()

	var items []ContextReceiptItem
	for rows.Next() {
		var (
			item         ContextReceiptItem
			entityID     sql.NullString
			path         sql.NullString
			startLine    sql.NullInt64
			endLine      sql.NullInt64
			rawTokens    sql.NullInt64
			projTokens   sql.NullInt64
			score        sql.NullInt64
			reasonsJSON  sql.NullString
			requiredFlag int
		)
		if err := rows.Scan(&item.Ordinal, &item.ItemID, &entityID, &path, &item.Section, &item.Mode,
			&startLine, &endLine, &item.ContentSHA256, &rawTokens, &projTokens, &score, &reasonsJSON, &requiredFlag); err != nil {
			return ContextReceipt{}, nil, fmt.Errorf("runledger: scan receipt item: %w", err)
		}
		item.EntityID = entityID.String
		item.Path = path.String
		item.StartLine = int(startLine.Int64)
		item.EndLine = int(endLine.Int64)
		item.RawTokens = int(rawTokens.Int64)
		item.ProjectedTokens = int(projTokens.Int64)
		item.Score = int(score.Int64)
		item.Required = requiredFlag != 0
		reasons, err := unmarshalReasons(reasonsJSON.String)
		if err != nil {
			return ContextReceipt{}, nil, fmt.Errorf("runledger: unmarshal reasons: %w", err)
		}
		item.Reasons = reasons
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ContextReceipt{}, nil, err
	}
	return receipt, items, nil
}

// RecordContextUsage implements Store.
func (s *SQLiteStore) RecordContextUsage(ctx context.Context, usage ContextUsage) (ContextUsage, error) {
	if usage.CreatedAt.IsZero() {
		usage.CreatedAt = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO context_usage (
			receipt_id, model_id, provider_id, backend, estimated_prompt_tokens,
			actual_prompt_tokens, cached_prompt_tokens, completion_tokens, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, usage.ReceiptID, nullableStr(usage.ModelID), nullableStr(usage.ProviderID), nullableStr(usage.Backend),
		nullableInt(usage.EstimatedPromptTokens), nullableInt(usage.ActualPromptTokens), nullableInt(usage.CachedPromptTokens),
		nullableInt(usage.CompletionTokens), sqliteTimestamp(usage.CreatedAt))
	if err != nil {
		return ContextUsage{}, fmt.Errorf("runledger: insert usage: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return ContextUsage{}, fmt.Errorf("runledger: usage last insert id: %w", err)
	}
	usage.ID = id
	return usage, nil
}

// CreateTaskCheckpoint implements Store. If checkpoint.Version is zero, the
// next version for TaskID is assigned automatically.
func (s *SQLiteStore) CreateTaskCheckpoint(ctx context.Context, checkpoint TaskCheckpoint) (TaskCheckpoint, error) {
	if checkpoint.TaskID == "" {
		return TaskCheckpoint{}, fmt.Errorf("runledger: task_id is required")
	}
	if checkpoint.MarkdownEvidenceID == "" {
		return TaskCheckpoint{}, fmt.Errorf("runledger: markdown_evidence_id is required")
	}
	exists, err := evidenceRowExists(ctx, s.db, checkpoint.MarkdownEvidenceID)
	if err != nil {
		return TaskCheckpoint{}, err
	}
	if !exists {
		return TaskCheckpoint{}, fmt.Errorf("%w: %s", ErrEvidenceNotFound, checkpoint.MarkdownEvidenceID)
	}

	if checkpoint.CheckpointID == "" {
		checkpoint.CheckpointID = "cp_" + ulid.Make().String()
	}
	if checkpoint.CreatedAt.IsZero() {
		checkpoint.CreatedAt = time.Now().UTC()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskCheckpoint{}, fmt.Errorf("runledger: begin create checkpoint: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if checkpoint.Version == 0 {
		var next int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM task_checkpoints WHERE task_id = ?`, checkpoint.TaskID).Scan(&next); err != nil {
			return TaskCheckpoint{}, fmt.Errorf("runledger: compute checkpoint version: %w", err)
		}
		checkpoint.Version = int(next)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO task_checkpoints (
			checkpoint_id, parent_checkpoint_id, task_id, session_id, run_id, version,
			status, snapshot_id, reason, state_json, markdown_evidence_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, checkpoint.CheckpointID, nullableStr(checkpoint.ParentCheckpointID), checkpoint.TaskID, checkpoint.SessionID,
		nullableStr(checkpoint.RunID), checkpoint.Version, checkpoint.Status, nullableStr(checkpoint.SnapshotID),
		checkpoint.Reason, checkpoint.StateJSON, checkpoint.MarkdownEvidenceID, sqliteTimestamp(checkpoint.CreatedAt))
	if err != nil {
		return TaskCheckpoint{}, fmt.Errorf("runledger: insert checkpoint: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return TaskCheckpoint{}, fmt.Errorf("runledger: commit create checkpoint: %w", err)
	}
	return checkpoint, nil
}

// LatestTaskCheckpoint implements Store.
func (s *SQLiteStore) LatestTaskCheckpoint(ctx context.Context, taskID string) (TaskCheckpoint, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT checkpoint_id, parent_checkpoint_id, task_id, session_id, run_id, version,
		       status, snapshot_id, reason, state_json, markdown_evidence_id, created_at
		FROM task_checkpoints WHERE task_id = ? ORDER BY version DESC LIMIT 1
	`, taskID)

	var (
		cp                 TaskCheckpoint
		parentCheckpointID sql.NullString
		runID              sql.NullString
		snapshotID         sql.NullString
		createdAtRaw       string
	)
	err := row.Scan(&cp.CheckpointID, &parentCheckpointID, &cp.TaskID, &cp.SessionID, &runID, &cp.Version,
		&cp.Status, &snapshotID, &cp.Reason, &cp.StateJSON, &cp.MarkdownEvidenceID, &createdAtRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskCheckpoint{}, ErrNotFound
	}
	if err != nil {
		return TaskCheckpoint{}, fmt.Errorf("runledger: scan checkpoint: %w", err)
	}
	cp.ParentCheckpointID = parentCheckpointID.String
	cp.RunID = runID.String
	cp.SnapshotID = snapshotID.String
	cp.CreatedAt = parseSQLiteTimestamp(createdAtRaw)
	return cp, nil
}

// RecordMetricSample implements Store.
func (s *SQLiteStore) RecordMetricSample(ctx context.Context, sample AgentMetricSample) (AgentMetricSample, error) {
	if sample.Timestamp.IsZero() {
		sample.Timestamp = time.Now().UTC()
	}
	dimensionsJSON, err := marshalJSONMap(sample.Dimensions)
	if err != nil {
		return AgentMetricSample{}, fmt.Errorf("runledger: marshal dimensions: %w", err)
	}
	statement := `
		INSERT INTO agent_metric_samples (run_id, task_id, idempotency_key, metric_name, metric_value, unit, dimensions_json, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	if strings.TrimSpace(sample.IdempotencyKey) != "" {
		statement = `
			INSERT INTO agent_metric_samples (run_id, task_id, idempotency_key, metric_name, metric_value, unit, dimensions_json, timestamp)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(run_id, idempotency_key)
			WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''
			DO NOTHING`
	}
	res, err := s.db.ExecContext(ctx, statement, sample.RunID, nullableStr(sample.TaskID), nullableStr(sample.IdempotencyKey), sample.MetricName, sample.Value, nullableStr(sample.Unit),
		dimensionsJSON, sqliteTimestamp(sample.Timestamp))
	if err != nil {
		return AgentMetricSample{}, fmt.Errorf("runledger: insert metric sample: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return AgentMetricSample{}, fmt.Errorf("runledger: metric sample rows affected: %w", err)
	}
	if affected == 0 && strings.TrimSpace(sample.IdempotencyKey) != "" {
		var (
			existingTask       sql.NullString
			existingMetric     string
			existingValue      float64
			existingUnit       sql.NullString
			existingDimensions sql.NullString
		)
		if err := s.db.QueryRowContext(ctx, `
			SELECT id, task_id, metric_name, metric_value, unit, dimensions_json
			FROM agent_metric_samples WHERE run_id = ? AND idempotency_key = ?
		`, sample.RunID, sample.IdempotencyKey).Scan(
			&sample.ID, &existingTask, &existingMetric, &existingValue,
			&existingUnit, &existingDimensions,
		); err != nil {
			return AgentMetricSample{}, fmt.Errorf("runledger: read idempotent metric sample: %w", err)
		}
		if existingTask.String != sample.TaskID || existingMetric != sample.MetricName ||
			existingValue != sample.Value || existingUnit.String != sample.Unit ||
			existingDimensions.String != nullableJSONText(dimensionsJSON) {
			return AgentMetricSample{}, fmt.Errorf("runledger: metric idempotency key %s conflicts with an existing immutable sample", sample.IdempotencyKey)
		}
		return sample, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return AgentMetricSample{}, fmt.Errorf("runledger: metric sample last insert id: %w", err)
	}
	sample.ID = id
	return sample, nil
}

// SumMetric implements Store.
func (s *SQLiteStore) SumMetric(ctx context.Context, runID, metricName string) (float64, error) {
	var total float64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(metric_value), 0) FROM agent_metric_samples
		WHERE run_id = ? AND metric_name = ?
	`, runID, metricName).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("runledger: sum metric %s: %w", metricName, err)
	}
	return total, nil
}

// SumMetricByTask implements Store.
func (s *SQLiteStore) SumMetricByTask(ctx context.Context, runID, metricName string) (map[string]float64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(task_id, ''), COALESCE(SUM(metric_value), 0)
		FROM agent_metric_samples
		WHERE run_id = ? AND metric_name = ?
		GROUP BY task_id
	`, runID, metricName)
	if err != nil {
		return nil, fmt.Errorf("runledger: sum metric %s by task: %w", metricName, err)
	}
	defer rows.Close()

	out := map[string]float64{}
	for rows.Next() {
		var taskID string
		var total float64
		if err := rows.Scan(&taskID, &total); err != nil {
			return nil, fmt.Errorf("runledger: scan metric sum: %w", err)
		}
		out[taskID] = total
	}
	return out, rows.Err()
}

// evidenceRowExists checks for evidence_id in evidence_objects, the table
// owned by pkg/evidence. If that table does not exist in db (the run ledger
// is being used standalone, without pkg/evidence composed onto the same
// connection via NewWithDB), the check is skipped rather than failed; the
// caller is responsible for composing both stores onto one *sql.DB when it
// wants this enforced.
func evidenceRowExists(ctx context.Context, db *sql.DB, evidenceID string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence_objects WHERE evidence_id = ?`, evidenceID).Scan(&count)
	if err != nil {
		if isNoSuchTableError(err) {
			return true, nil
		}
		return false, fmt.Errorf("runledger: check evidence reference: %w", err)
	}
	return count > 0, nil
}

func isNoSuchTableError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

func marshalReasons(reasons []Reason) (any, error) {
	if len(reasons) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(reasons)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

func unmarshalReasons(raw string) ([]Reason, error) {
	if raw == "" {
		return nil, nil
	}
	var reasons []Reason
	if err := json.Unmarshal([]byte(raw), &reasons); err != nil {
		return nil, err
	}
	return reasons, nil
}

func marshalJSONMap(m map[string]any) (any, error) {
	if len(m) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

func nullableJSONText(value any) string {
	if value == nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

func unmarshalJSONMap(raw string) (map[string]any, error) {
	if raw == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func marshalJSONStrings(values []string) (any, error) {
	if len(values) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	return string(raw), nil
}

func unmarshalJSONStrings(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	return values, nil
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func nullableTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return sqliteTimestamp(*t)
}

func sqliteTimestamp(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func sqliteLeaseTimestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func parseSQLiteTimestamp(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02 15:04:05.999999999-07:00", raw); err == nil {
		return t
	}
	return time.Time{}
}

func sqliteFilePathFromDSN(dsn string) (string, bool) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" || dsn == ":memory:" {
		return "", false
	}
	if strings.HasPrefix(dsn, "file:") {
		u, err := url.Parse(dsn)
		if err != nil || !strings.EqualFold(strings.TrimSpace(u.Scheme), "file") {
			return "", false
		}
		path := strings.TrimSpace(u.Path)
		if path == "" {
			path = strings.TrimSpace(u.Opaque)
		}
		if path == "" || path == ":memory:" {
			return "", false
		}
		return path, true
	}
	if strings.Contains(dsn, "://") {
		return "", false
	}
	return dsn, true
}

func ensurePrivateSQLiteFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("runledger: db path cannot be empty")
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("runledger: stat db path: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return fmt.Errorf("runledger: create db file: %w", err)
	}
	return f.Close()
}

// migrations is the ordered list of schema steps applied via storage.Migrate.
var migrations = []storage.SQLiteMigration{
	{Version: 1, Name: "agent_runs", Apply: createAgentRunsTable},
	{Version: 2, Name: "run_events", Apply: createRunEventsTable},
	{Version: 3, Name: "context_receipts", Apply: createContextReceiptsTable},
	{Version: 4, Name: "context_receipt_items", Apply: createContextReceiptItemsTable},
	{Version: 5, Name: "context_usage", Apply: createContextUsageTable},
	{Version: 6, Name: "task_checkpoints", Apply: createTaskCheckpointsTable},
	{Version: 7, Name: "agent_metric_samples", Apply: createAgentMetricSamplesTable},
	{Version: 8, Name: "execution_steps", Apply: createExecutionStepsTable},
	{Version: 9, Name: "agent_claims", Apply: createAgentClaimsTable},
	{Version: 10, Name: "metric_idempotency", Apply: addMetricIdempotency},
	{Version: 11, Name: "execution_step_dispatch_state", Apply: addExecutionStepDispatchState},
	{Version: 12, Name: "execution_step_claim_generation", Apply: addExecutionStepClaimGeneration},
	{Version: 13, Name: "agent_mailbox", Apply: createAgentMailboxTable},
	{Version: 14, Name: "agent_run_attempts", Apply: createAgentRunAttemptsTable},
	{Version: 15, Name: "agent_run_contracts", Apply: createAgentRunContractsTable},
	{Version: 16, Name: "run_event_ralph_outbox", Apply: createRunEventRalphOutboxTable},
	{Version: 17, Name: "operational_lease_timestamp_normalization", Apply: normalizeOperationalLeaseTimestamps},
	{Version: 18, Name: "agent_mailbox_envelope_digest", Apply: addAgentMailboxEnvelopeDigest},
	{Version: 19, Name: "agent_mailbox_envelope_digest_v2", Apply: refreshAgentMailboxEnvelopeDigests},
	{Version: 20, Name: "agent_run_monitor_indexes", Apply: addAgentRunMonitorIndexes},
	{Version: 21, Name: "launch_envelopes", Apply: createLaunchEnvelopesTable},
	{Version: 22, Name: "launch_model_reservations", Apply: createLaunchReservationTables},
}

func addAgentRunMonitorIndexes(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE INDEX IF NOT EXISTS idx_agent_runs_monitor_session
			ON agent_runs(session_id, ` + runStartedAtEpochKey + `, ` + runStartedAtFractionKey + `, run_id);
		CREATE INDEX IF NOT EXISTS idx_agent_runs_monitor_parent
			ON agent_runs(session_id, parent_run_id, ` + runStartedAtEpochKey + `, ` + runStartedAtFractionKey + `, run_id);
		DROP INDEX IF EXISTS idx_agent_mailbox_lease;
		CREATE INDEX idx_agent_mailbox_lease
			ON agent_mailbox(session_id, run_id, state, lease_expires_at);
	`)
	if err != nil {
		return fmt.Errorf("create agent run monitor indexes: %w", err)
	}
	return nil
}

func createAgentRunsTable(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_runs (
			run_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			parent_run_id TEXT,
			task_id TEXT,
			agent_id TEXT,
			model_id TEXT,
			provider_id TEXT,
			backend TEXT,
			status TEXT NOT NULL,
			started_at TIMESTAMP NOT NULL,
			ended_at TIMESTAMP,
			budget_json TEXT,
			outcome_json TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_agent_runs_session ON agent_runs(session_id, started_at);
		CREATE INDEX IF NOT EXISTS idx_agent_runs_task ON agent_runs(task_id, started_at);
		CREATE INDEX IF NOT EXISTS idx_agent_runs_parent ON agent_runs(parent_run_id);
	`)
	if err != nil {
		return fmt.Errorf("create agent_runs: %w", err)
	}
	return nil
}

func createRunEventsTable(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS run_events (
			event_id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			sequence INTEGER NOT NULL,
			event_type TEXT NOT NULL,
			timestamp TIMESTAMP NOT NULL,
			task_id TEXT,
			agent_id TEXT,
			model_id TEXT,
			provider_id TEXT,
			backend TEXT,
			snapshot_id TEXT,
			payload_json TEXT,
			evidence_ids_json TEXT,
			receipt_ids_json TEXT,
			redaction_version TEXT NOT NULL,
			UNIQUE(run_id, sequence),
			FOREIGN KEY(run_id) REFERENCES agent_runs(run_id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_run_events_type ON run_events(event_type, timestamp);
		CREATE INDEX IF NOT EXISTS idx_run_events_run ON run_events(run_id, sequence);
		CREATE INDEX IF NOT EXISTS idx_run_events_task ON run_events(task_id, timestamp);
	`)
	if err != nil {
		return fmt.Errorf("create run_events: %w", err)
	}
	return nil
}

func createContextReceiptsTable(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS context_receipts (
			receipt_id TEXT PRIMARY KEY,
			parent_receipt_id TEXT,
			session_id TEXT,
			run_id TEXT,
			task_id TEXT,
			snapshot_id TEXT NOT NULL,
			request_digest TEXT NOT NULL,
			policy_version TEXT NOT NULL,
			budget_tokens INTEGER NOT NULL,
			estimated_tokens INTEGER NOT NULL,
			candidate_tokens INTEGER NOT NULL,
			bundle_evidence_id TEXT NOT NULL,
			manifest_evidence_id TEXT NOT NULL,
			bundle_sha256 TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_context_receipts_task ON context_receipts(task_id, created_at);
	`)
	if err != nil {
		return fmt.Errorf("create context_receipts: %w", err)
	}
	return nil
}

func createContextReceiptItemsTable(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS context_receipt_items (
			receipt_id TEXT NOT NULL,
			ordinal INTEGER NOT NULL,
			item_id TEXT NOT NULL,
			entity_id TEXT,
			path TEXT,
			section TEXT NOT NULL,
			projection_mode TEXT NOT NULL,
			start_line INTEGER,
			end_line INTEGER,
			content_sha256 TEXT NOT NULL,
			raw_tokens INTEGER,
			projected_tokens INTEGER,
			score INTEGER,
			reasons_json TEXT,
			required INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY(receipt_id, ordinal),
			FOREIGN KEY(receipt_id) REFERENCES context_receipts(receipt_id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_context_items_entity ON context_receipt_items(entity_id, path);
	`)
	if err != nil {
		return fmt.Errorf("create context_receipt_items: %w", err)
	}
	return nil
}

func createContextUsageTable(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS context_usage (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			receipt_id TEXT NOT NULL,
			model_id TEXT,
			provider_id TEXT,
			backend TEXT,
			estimated_prompt_tokens INTEGER,
			actual_prompt_tokens INTEGER,
			cached_prompt_tokens INTEGER,
			completion_tokens INTEGER,
			created_at TIMESTAMP NOT NULL,
			FOREIGN KEY(receipt_id) REFERENCES context_receipts(receipt_id) ON DELETE CASCADE
		);
	`)
	if err != nil {
		return fmt.Errorf("create context_usage: %w", err)
	}
	return nil
}

func createTaskCheckpointsTable(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS task_checkpoints (
			checkpoint_id TEXT PRIMARY KEY,
			parent_checkpoint_id TEXT,
			task_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			run_id TEXT,
			version INTEGER NOT NULL,
			status TEXT NOT NULL,
			snapshot_id TEXT,
			reason TEXT NOT NULL,
			state_json TEXT NOT NULL,
			markdown_evidence_id TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			UNIQUE(task_id, version)
		);
		CREATE INDEX IF NOT EXISTS idx_task_checkpoints_latest ON task_checkpoints(task_id, version DESC);
	`)
	if err != nil {
		return fmt.Errorf("create task_checkpoints: %w", err)
	}
	return nil
}

func createAgentMetricSamplesTable(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_metric_samples (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			run_id TEXT NOT NULL,
			task_id TEXT,
			metric_name TEXT NOT NULL,
			metric_value REAL NOT NULL,
			unit TEXT,
			dimensions_json TEXT,
			timestamp TIMESTAMP NOT NULL,
			FOREIGN KEY(run_id) REFERENCES agent_runs(run_id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_agent_metrics_name ON agent_metric_samples(metric_name, timestamp);
	`)
	if err != nil {
		return fmt.Errorf("create agent_metric_samples: %w", err)
	}
	return nil
}

func addMetricIdempotency(db storage.MigrationDB) error {
	exists, err := sqliteColumnExists(db, "agent_metric_samples", "idempotency_key")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := db.Exec(`ALTER TABLE agent_metric_samples ADD COLUMN idempotency_key TEXT`); err != nil {
			return fmt.Errorf("add metric idempotency key: %w", err)
		}
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_metrics_idempotency ON agent_metric_samples(run_id, idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''`); err != nil {
		return fmt.Errorf("create metric idempotency index: %w", err)
	}
	return nil
}

func createExecutionStepsTable(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS execution_steps (
			run_id TEXT NOT NULL,
			task_id TEXT,
			step_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			idempotency_key TEXT NOT NULL,
			status TEXT NOT NULL,
			attempt INTEGER NOT NULL,
			claim_generation INTEGER NOT NULL DEFAULT 1,
			input_digest TEXT,
			output_digest TEXT,
			output_evidence_id TEXT,
			error_text TEXT,
			dispatch_state TEXT,
			started_at TIMESTAMP NOT NULL,
			completed_at TIMESTAMP,
			PRIMARY KEY(run_id, step_id),
			UNIQUE(run_id, idempotency_key),
			FOREIGN KEY(run_id) REFERENCES agent_runs(run_id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_execution_steps_task ON execution_steps(run_id, task_id, started_at);
		CREATE INDEX IF NOT EXISTS idx_execution_steps_status ON execution_steps(run_id, status, started_at);
	`)
	if err != nil {
		return fmt.Errorf("create execution_steps: %w", err)
	}
	return nil
}

func addExecutionStepDispatchState(db storage.MigrationDB) error {
	exists, err := sqliteColumnExists(db, "execution_steps", "dispatch_state")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := db.Exec(`ALTER TABLE execution_steps ADD COLUMN dispatch_state TEXT`); err != nil {
			return fmt.Errorf("add execution step dispatch state: %w", err)
		}
	}
	return nil
}

func addExecutionStepClaimGeneration(db storage.MigrationDB) error {
	exists, err := sqliteColumnExists(db, "execution_steps", "claim_generation")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := db.Exec(`ALTER TABLE execution_steps ADD COLUMN claim_generation INTEGER NOT NULL DEFAULT 1`); err != nil {
			return fmt.Errorf("add execution step claim generation: %w", err)
		}
	}
	return nil
}

func createAgentMailboxTable(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_runs_identity
			ON agent_runs(run_id, session_id);
		CREATE TABLE IF NOT EXISTS agent_mailbox (
			message_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			run_id TEXT NOT NULL,
			parent_run_id TEXT,
			task_id TEXT,
			turn_id TEXT,
			idempotency_key TEXT NOT NULL,
			correlation_id TEXT,
			causation_id TEXT,
			attempt_id TEXT,
			lease_generation INTEGER NOT NULL DEFAULT 0,
			source_attempt_id TEXT,
			source_lease_generation INTEGER NOT NULL DEFAULT 0,
			sequence INTEGER NOT NULL,
			schema_version TEXT NOT NULL,
			from_id TEXT,
			to_id TEXT NOT NULL,
			kind TEXT NOT NULL,
			content_ref TEXT NOT NULL,
			content_digest TEXT NOT NULL,
			envelope_digest TEXT NOT NULL,
			media_type TEXT NOT NULL,
			byte_count INTEGER NOT NULL,
			preview TEXT,
			state TEXT NOT NULL,
			lease_owner TEXT,
			lease_expires_at TIMESTAMP,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			last_error TEXT,
			created_at TIMESTAMP NOT NULL,
			claimed_at TIMESTAMP,
			processed_at TIMESTAMP,
			dead_lettered_at TIMESTAMP,
			CHECK (sequence > 0),
			CHECK (lease_generation >= 0),
			CHECK (source_lease_generation >= 0),
			CHECK (length(envelope_digest) = 64),
			CHECK (byte_count >= 0),
			CHECK (state IN ('queued', 'claimed', 'processed', 'dead_letter')),
			UNIQUE(session_id, run_id, idempotency_key),
			UNIQUE(session_id, run_id, sequence),
			FOREIGN KEY(run_id, session_id)
				REFERENCES agent_runs(run_id, session_id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_agent_mailbox_ready
			ON agent_mailbox(session_id, run_id, state, sequence);
		CREATE INDEX IF NOT EXISTS idx_agent_mailbox_lease
			ON agent_mailbox(session_id, run_id, state, lease_expires_at);
		CREATE INDEX IF NOT EXISTS idx_agent_mailbox_identity
			ON agent_mailbox(session_id, run_id, message_id);
	`)
	if err != nil {
		return fmt.Errorf("create agent_mailbox: %w", err)
	}
	return nil
}

func createAgentRunAttemptsTable(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_runs_identity
			ON agent_runs(run_id, session_id);
		CREATE TABLE IF NOT EXISTS agent_run_attempts (
			attempt_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			run_id TEXT NOT NULL,
			parent_run_id TEXT,
			task_id TEXT,
			turn_id TEXT,
			lease_generation INTEGER NOT NULL,
			pid INTEGER,
			state TEXT NOT NULL,
			attached_at TIMESTAMP NOT NULL,
			heartbeat_at TIMESTAMP NOT NULL,
			lease_expires_at TIMESTAMP NOT NULL,
			detached_at TIMESTAMP,
			detach_reason TEXT,
			CHECK (lease_generation > 0),
			CHECK (state IN ('attached', 'detached', 'expired')),
			UNIQUE(session_id, run_id, lease_generation),
			UNIQUE(session_id, run_id, attempt_id),
			FOREIGN KEY(run_id, session_id)
				REFERENCES agent_runs(run_id, session_id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_agent_attempts_current
			ON agent_run_attempts(session_id, run_id, state, lease_generation DESC);
		CREATE INDEX IF NOT EXISTS idx_agent_attempts_expiry
			ON agent_run_attempts(state, lease_expires_at);
	`)
	if err != nil {
		return fmt.Errorf("create agent_run_attempts: %w", err)
	}
	return nil
}

func createAgentRunContractsTable(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_run_contracts (
			run_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			input_digest TEXT NOT NULL,
			task_evidence_id TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL,
			CHECK (length(input_digest) BETWEEN 1 AND 128),
			CHECK (length(task_evidence_id) BETWEEN 1 AND 256),
			FOREIGN KEY(run_id, session_id)
				REFERENCES agent_runs(run_id, session_id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_agent_run_contracts_session
			ON agent_run_contracts(session_id, run_id);
	`)
	if err != nil {
		return fmt.Errorf("create agent_run_contracts: %w", err)
	}
	return nil
}

func createRunEventRalphOutboxTable(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS run_event_ralph_outbox (
			event_id TEXT PRIMARY KEY,
			state TEXT NOT NULL,
			delivery_owner TEXT,
			lease_expires_at TIMESTAMP,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL,
			delivered_at TIMESTAMP,
			CHECK (state IN ('pending', 'delivering', 'failed', 'delivered')),
			CHECK (attempt_count >= 0),
			FOREIGN KEY(event_id) REFERENCES run_events(event_id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_run_event_ralph_ready
			ON run_event_ralph_outbox(state, lease_expires_at, updated_at);
	`)
	if err != nil {
		return fmt.Errorf("create run_event_ralph_outbox: %w", err)
	}
	return nil
}

func normalizeOperationalLeaseTimestamps(db storage.MigrationDB) error {
	targets := []struct {
		table, key string
	}{
		{table: "agent_mailbox", key: "message_id"},
		{table: "agent_run_attempts", key: "attempt_id"},
		{table: "run_event_ralph_outbox", key: "event_id"},
	}
	for _, target := range targets {
		rows, err := db.Query(`SELECT ` + target.key + `, lease_expires_at FROM ` + target.table + ` WHERE lease_expires_at IS NOT NULL`)
		if err != nil {
			return fmt.Errorf("normalize %s lease timestamps: %w", target.table, err)
		}
		type leaseRow struct{ id, raw string }
		var leases []leaseRow
		for rows.Next() {
			var lease leaseRow
			if err := rows.Scan(&lease.id, &lease.raw); err != nil {
				rows.Close()
				return fmt.Errorf("scan %s lease timestamp: %w", target.table, err)
			}
			leases = append(leases, lease)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return fmt.Errorf("iterate %s lease timestamps: %w", target.table, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close %s lease timestamps: %w", target.table, err)
		}
		for _, lease := range leases {
			parsed := parseSQLiteTimestamp(lease.raw)
			if parsed.IsZero() {
				return fmt.Errorf("normalize %s lease timestamp %q: invalid timestamp", target.table, lease.raw)
			}
			if _, err := db.Exec(`UPDATE `+target.table+` SET lease_expires_at = ? WHERE `+target.key+` = ?`, sqliteLeaseTimestamp(parsed), lease.id); err != nil {
				return fmt.Errorf("update %s lease timestamp: %w", target.table, err)
			}
		}
	}
	return nil
}

func addAgentMailboxEnvelopeDigest(db storage.MigrationDB) error {
	exists, err := sqliteColumnExists(db, "agent_mailbox", "envelope_digest")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := db.Exec(`ALTER TABLE agent_mailbox ADD COLUMN envelope_digest TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add agent mailbox envelope digest: %w", err)
		}
	}
	if err := backfillAgentMailboxEnvelopeDigests(db, `WHERE envelope_digest = ''`); err != nil {
		return err
	}
	if _, err := db.Exec(`
		CREATE TRIGGER IF NOT EXISTS trg_agent_mailbox_envelope_digest_insert
		BEFORE INSERT ON agent_mailbox
		WHEN length(NEW.envelope_digest) <> 64
		BEGIN
			SELECT RAISE(ABORT, 'agent_mailbox envelope_digest must be 64 bytes');
		END;
		CREATE TRIGGER IF NOT EXISTS trg_agent_mailbox_envelope_digest_update
		BEFORE UPDATE OF envelope_digest ON agent_mailbox
		WHEN length(NEW.envelope_digest) <> 64
		BEGIN
			SELECT RAISE(ABORT, 'agent_mailbox envelope_digest must be 64 bytes');
		END;
	`); err != nil {
		return fmt.Errorf("constrain agent mailbox envelope digest: %w", err)
	}
	return nil
}

func refreshAgentMailboxEnvelopeDigests(db storage.MigrationDB) error {
	return backfillAgentMailboxEnvelopeDigests(db, "")
}

func backfillAgentMailboxEnvelopeDigests(db storage.MigrationDB, where string) error {
	rows, err := db.Query(`
		SELECT message_id, schema_version, session_id, run_id, parent_run_id,
			task_id, turn_id, idempotency_key, correlation_id, causation_id,
			attempt_id, lease_generation, source_attempt_id, source_lease_generation,
			sequence, from_id, to_id, kind, content_ref, content_digest,
			envelope_digest, media_type, byte_count, preview, state,
			lease_owner, lease_expires_at, attempt_count, last_error, created_at,
			claimed_at, processed_at, dead_lettered_at
		FROM agent_mailbox ` + where)
	if err != nil {
		return fmt.Errorf("list mailbox envelope digest backfill: %w", err)
	}
	var messages []agentcoord.Message
	for rows.Next() {
		message, scanErr := scanMailboxMessage(rows)
		if scanErr != nil {
			rows.Close()
			return fmt.Errorf("scan mailbox envelope digest backfill: %w", scanErr)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate mailbox envelope digest backfill: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close mailbox envelope digest backfill: %w", err)
	}
	for _, message := range messages {
		digest, err := mailboxEnvelopeDigest(message)
		if err != nil {
			return err
		}
		if _, err := db.Exec(`UPDATE agent_mailbox SET envelope_digest = ? WHERE message_id = ?`, digest, message.ID); err != nil {
			return fmt.Errorf("backfill mailbox envelope digest: %w", err)
		}
	}
	return nil
}

func sqliteColumnExists(db storage.MigrationDB, table, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, fmt.Errorf("inspect %s columns: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, fmt.Errorf("scan %s columns: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate %s columns: %w", table, err)
	}
	return false, nil
}

func createAgentClaimsTable(db storage.MigrationDB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS agent_claim_locks (
			lock_key TEXT PRIMARY KEY,
			touched_at TIMESTAMP NOT NULL
		);
		INSERT OR IGNORE INTO agent_claim_locks (lock_key, touched_at)
		VALUES ('workspace', CURRENT_TIMESTAMP);

		CREATE TABLE IF NOT EXISTS agent_claims (
			claim_id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			resource TEXT NOT NULL,
			acquired_at TIMESTAMP NOT NULL,
			released_at TIMESTAMP,
			release_reason TEXT,
			FOREIGN KEY(run_id) REFERENCES agent_runs(run_id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_agent_claims_active
			ON agent_claims(resource, run_id) WHERE released_at IS NULL;
		CREATE INDEX IF NOT EXISTS idx_agent_claims_run
			ON agent_claims(run_id, acquired_at);
	`)
	if err != nil {
		return fmt.Errorf("create agent_claims: %w", err)
	}
	return nil
}

// runMigrations applies pending schema migrations via the shared
// storage.Migrate runner, tracked in runledger_schema_migrations (named
// distinctly from pkg/storage's and pkg/evidence's own migration-tracking
// tables so all three can safely share one SQLite file once wiring lands).
func runMigrations(db *sql.DB) error {
	return storage.MigrateSQLite(db, "runledger_schema_migrations", migrations)
}
