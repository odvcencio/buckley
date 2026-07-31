package runledger

import (
	"context"
	"database/sql"
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
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// DefaultRedactionVersion is applied to an Event when the caller does not
// set one explicitly.
const DefaultRedactionVersion = "runledger-redaction-v1"

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

	mu        sync.RWMutex
	liveSink  LiveSink
	ralphSink RalphSink
}

var _ Store = (*SQLiteStore)(nil)

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

	if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("runledger: enable WAL mode: %w", err)
	}

	if err := runMigrations(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("runledger: run migrations: %w", err)
	}

	return &SQLiteStore{db: db}, nil
}

// NewWithDB wraps an already-open *sql.DB, applying the run ledger schema
// migrations to it. It is intended for composing the run ledger onto a
// shared database connection (for example, one already holding
// pkg/evidence's schema, which enables the application-level evidence
// reference checks described on SQLiteStore) once wiring lands in a later
// PR. The caller owns the connection's lifecycle and pragma configuration.
func NewWithDB(db *sql.DB) (*SQLiteStore, error) {
	if db == nil {
		return nil, fmt.Errorf("runledger: db cannot be nil")
	}
	if err := runMigrations(db); err != nil {
		return nil, fmt.Errorf("runledger: run migrations: %w", err)
	}
	return &SQLiteStore{db: db}, nil
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
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ralphSink = sink
}

func (s *SQLiteStore) sinks() (LiveSink, RalphSink) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.liveSink, s.ralphSink
}

// StartRun implements Store.
func (s *SQLiteStore) StartRun(ctx context.Context, run AgentRun) (AgentRun, error) {
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
		UPDATE agent_runs SET status = ?, ended_at = ?, outcome_json = ? WHERE run_id = ?
	`, status, sqliteTimestamp(endedAt), outcomeJSON, runID)
	if err != nil {
		return fmt.Errorf("runledger: end run %s: %w", runID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("runledger: end run %s: %w", runID, err)
	}
	if n == 0 {
		return ErrNotFound
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

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, fmt.Errorf("runledger: begin append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var seq int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence), 0) + 1 FROM run_events WHERE run_id = ?`, event.RunID).Scan(&seq); err != nil {
		return Event{}, fmt.Errorf("runledger: compute sequence: %w", err)
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
		return Event{}, fmt.Errorf("runledger: insert event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Event{}, fmt.Errorf("runledger: commit append: %w", err)
	}
	event.Sequence = seq

	liveSink, ralphSink := s.sinks()
	notifyLiveSink(liveSink, event)

	var dualWriteErr error
	if ralphSink != nil {
		if err := ralphSink.WriteEvent(ctx, event); err != nil {
			dualWriteErr = fmt.Errorf("%w: %v", ErrRalphDualWriteFailed, err)
		}
	}

	return event, dualWriteErr
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
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_metric_samples (run_id, task_id, metric_name, metric_value, unit, dimensions_json, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, sample.RunID, nullableStr(sample.TaskID), sample.MetricName, sample.Value, nullableStr(sample.Unit),
		dimensionsJSON, sqliteTimestamp(sample.Timestamp))
	if err != nil {
		return AgentMetricSample{}, fmt.Errorf("runledger: insert metric sample: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return AgentMetricSample{}, fmt.Errorf("runledger: metric sample last insert id: %w", err)
	}
	sample.ID = id
	return sample, nil
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

func isBusyError(err error) bool {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) {
		code := sqliteErr.Code()
		return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
	}
	return false
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

// migration is a single, ordered, idempotent schema step, matching the
// pattern in pkg/storage/sqlite.go and pkg/evidence/store.go.
type migration struct {
	Version int
	Name    string
	Apply   func(db *sql.DB) error
}

var migrations = []migration{
	{1, "agent_runs", createAgentRunsTable},
	{2, "run_events", createRunEventsTable},
	{3, "context_receipts", createContextReceiptsTable},
	{4, "context_receipt_items", createContextReceiptItemsTable},
	{5, "context_usage", createContextUsageTable},
	{6, "task_checkpoints", createTaskCheckpointsTable},
	{7, "agent_metric_samples", createAgentMetricSamplesTable},
}

func createAgentRunsTable(db *sql.DB) error {
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

func createRunEventsTable(db *sql.DB) error {
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

func createContextReceiptsTable(db *sql.DB) error {
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

func createContextReceiptItemsTable(db *sql.DB) error {
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

func createContextUsageTable(db *sql.DB) error {
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

func createTaskCheckpointsTable(db *sql.DB) error {
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

func createAgentMetricSamplesTable(db *sql.DB) error {
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

// runMigrations applies pending schema migrations, tracked in
// runledger_schema_migrations (named distinctly from pkg/storage's and
// pkg/evidence's own migration-tracking tables so all three can safely
// share one SQLite file once wiring lands).
func runMigrations(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS runledger_schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create runledger_schema_migrations: %w", err)
	}

	var currentVersion int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM runledger_schema_migrations`).Scan(&currentVersion); err != nil {
		return fmt.Errorf("get schema version: %w", err)
	}

	for _, m := range migrations {
		if m.Version <= currentVersion {
			continue
		}
		if err := m.Apply(db); err != nil {
			return fmt.Errorf("migration %d (%s): %w", m.Version, m.Name, err)
		}
		if _, err := db.Exec(`INSERT INTO runledger_schema_migrations (version, name) VALUES (?, ?)`, m.Version, m.Name); err != nil {
			return fmt.Errorf("record migration %d: %w", m.Version, err)
		}
	}
	return nil
}
