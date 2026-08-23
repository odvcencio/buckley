package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/sessionexec"
	"m31labs.dev/buckley/pkg/telemetry"
)

const sessionExecSchemaSQL = `
CREATE TABLE IF NOT EXISTS session_commands (
    session_id TEXT NOT NULL,
    command_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    task_id TEXT NOT NULL CHECK(task_id = 'foreground'),
    turn_id TEXT NOT NULL,
    generation INTEGER NOT NULL DEFAULT 0 CHECK(generation >= 0),
    sequence INTEGER NOT NULL CHECK(sequence > 0 AND sequence <= 1000000000000),
    lane TEXT NOT NULL CHECK(lane IN ('work','control')),
    command_type TEXT NOT NULL,
    content TEXT NOT NULL,
    input_digest TEXT NOT NULL CHECK(length(input_digest) = 64),
    accepted_by TEXT NOT NULL,
    target_command_id TEXT,
    state TEXT NOT NULL CHECK(state IN ('accepted','running','succeeded','failed','blocked','interrupted','cancelled')),
    attempt INTEGER NOT NULL DEFAULT 0 CHECK(attempt >= 0 AND attempt <= 1000000),
    lease_generation INTEGER NOT NULL DEFAULT 0 CHECK(lease_generation >= 0 AND lease_generation <= 1000000),
    lease_owner TEXT,
    lease_expires_at_ms INTEGER,
    accepted_at_ms INTEGER NOT NULL CHECK(accepted_at_ms >= 0),
    started_at_ms INTEGER,
    heartbeat_at_ms INTEGER,
    completed_at_ms INTEGER,
    error_code TEXT,
    error_text TEXT,
    outcome_json TEXT,
    completion_digest TEXT,
    completed_by TEXT,
    completion_lease_generation INTEGER,
    PRIMARY KEY(session_id, command_id),
    UNIQUE(session_id, sequence),
    UNIQUE(run_id, turn_id),
    CHECK(length(CAST(session_id AS BLOB)) BETWEEN 1 AND 256),
    CHECK(length(CAST(command_id AS BLOB)) BETWEEN 1 AND 128),
    CHECK(length(CAST(run_id AS BLOB)) BETWEEN 1 AND 128),
    CHECK(length(CAST(turn_id AS BLOB)) BETWEEN 1 AND 192),
    CHECK(length(CAST(command_type AS BLOB)) BETWEEN 1 AND 32),
    CHECK(length(CAST(content AS BLOB)) <= 1048576),
    CHECK(length(CAST(accepted_by AS BLOB)) BETWEEN 1 AND 128),
    CHECK(target_command_id IS NULL OR length(CAST(target_command_id AS BLOB)) BETWEEN 1 AND 128),
    CHECK(lease_owner IS NULL OR length(CAST(lease_owner AS BLOB)) BETWEEN 1 AND 128),
    CHECK(error_code IS NULL OR length(CAST(error_code AS BLOB)) <= 64),
    CHECK(error_text IS NULL OR length(CAST(error_text AS BLOB)) <= 512),
    CHECK(outcome_json IS NULL OR length(CAST(outcome_json AS BLOB)) <= 32768),
    CHECK(completion_digest IS NULL OR length(completion_digest) = 64),
    CHECK(completed_by IS NULL OR length(CAST(completed_by AS BLOB)) BETWEEN 1 AND 128),
    CHECK(lease_expires_at_ms IS NULL OR lease_expires_at_ms >= 0),
    CHECK(started_at_ms IS NULL OR started_at_ms >= 0),
    CHECK(heartbeat_at_ms IS NULL OR heartbeat_at_ms >= 0),
    CHECK(completed_at_ms IS NULL OR completed_at_ms >= 0),
    CHECK(completion_lease_generation IS NULL OR completion_lease_generation > 0),
    CHECK((state = 'running' AND lease_owner IS NOT NULL AND lease_expires_at_ms IS NOT NULL)
       OR (state <> 'running' AND lease_owner IS NULL AND lease_expires_at_ms IS NULL)),
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_session_commands_ready
    ON session_commands(session_id, lane, state, sequence);
CREATE INDEX IF NOT EXISTS idx_session_commands_lease
    ON session_commands(state, lease_expires_at_ms);
CREATE UNIQUE INDEX IF NOT EXISTS idx_session_commands_one_running_lane
    ON session_commands(session_id, lane) WHERE state = 'running';
CREATE TABLE IF NOT EXISTS session_command_transcript (
    session_id TEXT NOT NULL,
    command_id TEXT NOT NULL,
    generation INTEGER NOT NULL CHECK(generation >= 0),
    ordinal INTEGER NOT NULL CHECK(ordinal >= 0 AND ordinal <= 1000000),
    message_id INTEGER UNIQUE,
    entry_json TEXT NOT NULL CHECK(length(CAST(entry_json AS BLOB)) BETWEEN 1 AND 8388608),
    entry_digest TEXT NOT NULL CHECK(length(entry_digest) = 64),
    PRIMARY KEY(session_id, command_id, generation, ordinal),
    FOREIGN KEY (session_id, command_id)
        REFERENCES session_commands(session_id, command_id) ON DELETE CASCADE,
    FOREIGN KEY (message_id) REFERENCES messages(id) ON DELETE SET NULL
);
CREATE INDEX IF NOT EXISTS idx_session_command_transcript_message
    ON session_command_transcript(message_id);
`

const sessionExecutionStateSchemaSQL = `
CREATE TABLE IF NOT EXISTS session_execution_state (
    session_id TEXT PRIMARY KEY,
    mode TEXT NOT NULL CHECK(mode IN ('headless','detached','adopted')),
    generation INTEGER NOT NULL DEFAULT 0 CHECK(generation >= 0 AND generation <= 1000000000000),
    reason_code TEXT,
    updated_at_ms INTEGER NOT NULL CHECK(updated_at_ms >= 0),
    CHECK(length(CAST(session_id AS BLOB)) BETWEEN 1 AND 256),
    CHECK(reason_code IS NULL OR length(CAST(reason_code AS BLOB)) <= 64),
    FOREIGN KEY (session_id) REFERENCES sessions(session_id) ON DELETE CASCADE
);
`

const sessionEffectPermitSchemaSQL = `
CREATE TABLE IF NOT EXISTS session_effect_permits (
    session_id TEXT NOT NULL,
    command_id TEXT NOT NULL,
    generation INTEGER NOT NULL CHECK(generation >= 0),
    effect_id TEXT NOT NULL,
    kind TEXT NOT NULL CHECK(kind IN ('model','tool')),
    lease_owner TEXT NOT NULL,
    lease_generation INTEGER NOT NULL CHECK(lease_generation > 0 AND lease_generation <= 1000000),
    state TEXT NOT NULL CHECK(state IN ('active','ambiguous','ended','resolved')),
    expires_at_ms INTEGER NOT NULL CHECK(expires_at_ms >= 0),
    created_at_ms INTEGER NOT NULL CHECK(created_at_ms >= 0),
    ambiguous_at_ms INTEGER CHECK(ambiguous_at_ms IS NULL OR ambiguous_at_ms >= 0),
    ended_at_ms INTEGER CHECK(ended_at_ms IS NULL OR ended_at_ms >= 0),
    resolved_at_ms INTEGER CHECK(resolved_at_ms IS NULL OR resolved_at_ms >= 0),
    resolved_by TEXT,
    resolution_reason TEXT,
    PRIMARY KEY(session_id, command_id, generation, effect_id),
    CHECK(length(CAST(session_id AS BLOB)) BETWEEN 1 AND 256),
    CHECK(length(CAST(command_id AS BLOB)) BETWEEN 1 AND 128),
    CHECK(length(CAST(effect_id AS BLOB)) BETWEEN 1 AND 512),
    CHECK(length(CAST(lease_owner AS BLOB)) BETWEEN 1 AND 128),
    CHECK(resolved_by IS NULL OR length(CAST(resolved_by AS BLOB)) BETWEEN 1 AND 128),
    CHECK(resolution_reason IS NULL OR length(CAST(resolution_reason AS BLOB)) BETWEEN 1 AND 512),
    CHECK((state = 'active' AND ambiguous_at_ms IS NULL AND ended_at_ms IS NULL
            AND resolved_at_ms IS NULL AND resolved_by IS NULL AND resolution_reason IS NULL)
       OR (state = 'ambiguous' AND ambiguous_at_ms IS NOT NULL AND ended_at_ms IS NULL
            AND resolved_at_ms IS NULL AND resolved_by IS NULL AND resolution_reason IS NULL)
       OR (state = 'ended' AND ended_at_ms IS NOT NULL
            AND resolved_at_ms IS NULL AND resolved_by IS NULL AND resolution_reason IS NULL)
       OR (state = 'resolved' AND ambiguous_at_ms IS NOT NULL AND ended_at_ms IS NULL
            AND resolved_at_ms IS NOT NULL AND resolved_by IS NOT NULL AND resolution_reason IS NOT NULL)),
    FOREIGN KEY (session_id, command_id)
        REFERENCES session_commands(session_id, command_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_session_effect_permits_active
    ON session_effect_permits(session_id, state, expires_at_ms, effect_id);
`

const sessionExecNowMillisSQL = `
CAST(strftime('%s','now') AS INTEGER) * 1000 +
CAST(substr(strftime('%f','now'), 4, 3) AS INTEGER)`

func ensureSessionExecSchema(db MigrationDB) error {
	if _, err := db.Exec(sessionExecSchemaSQL); err != nil {
		return fmt.Errorf("create session command journal: %w", err)
	}
	return nil
}

func ensureSessionExecutionStateSchema(db MigrationDB) error {
	if _, err := db.Exec(sessionExecutionStateSchemaSQL); err != nil {
		return fmt.Errorf("create session execution state: %w", err)
	}
	return nil
}

func ensureSessionEffectPermitSchema(db MigrationDB) error {
	if _, err := db.Exec(sessionEffectPermitSchemaSQL); err != nil {
		return fmt.Errorf("create session effect permits: %w", err)
	}
	return nil
}

type sessionExecConn struct {
	ctx  context.Context
	conn *sql.Conn
}

func (db *sessionExecConn) exec(query string, args ...any) (sql.Result, error) {
	return db.conn.ExecContext(db.ctx, query, args...)
}

func (db *sessionExecConn) query(query string, args ...any) (*sql.Rows, error) {
	return db.conn.QueryContext(db.ctx, query, args...)
}

func (db *sessionExecConn) queryRow(query string, args ...any) *sql.Row {
	return db.conn.QueryRowContext(db.ctx, query, args...)
}

func (s *Store) withSessionExecWrite(ctx context.Context, fn func(*sessionExecConn) error) error {
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	delay := 10 * time.Millisecond
	var lastErr error
	for attempt := 0; attempt < sqliteWALRetryLimit; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("acquire session command connection: %w", err)
		}
		if _, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err == nil {
			bound := &sessionExecConn{ctx: ctx, conn: conn}
			err = fn(bound)
			if err == nil {
				_, err = conn.ExecContext(ctx, `COMMIT`)
			}
			if err != nil {
				_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
			}
		}
		_ = conn.Close()
		if err == nil {
			return nil
		}
		lastErr = err
		if !IsSQLiteBusyError(err) {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < 250*time.Millisecond {
			delay *= 2
		}
	}
	return fmt.Errorf("session command transaction remained busy: %w", lastErr)
}

func sessionExecNowMillis(db *sessionExecConn) (int64, error) {
	var now int64
	if err := db.queryRow(`SELECT ` + sessionExecNowMillisSQL).Scan(&now); err != nil {
		return 0, fmt.Errorf("read database time: %w", err)
	}
	return now, nil
}

func sessionExecTime(ms int64) time.Time {
	return time.UnixMilli(ms).UTC()
}

func sessionExecTimePtr(ms sql.NullInt64) *time.Time {
	if !ms.Valid {
		return nil
	}
	value := sessionExecTime(ms.Int64)
	return &value
}

func sessionExecSessionExists(db *sessionExecConn, sessionID string) error {
	var exists int
	err := db.queryRow(`SELECT 1 FROM sessions WHERE session_id = ?`, sessionID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionexec.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read command session: %w", err)
	}
	return nil
}

func scanSessionExecutionState(row rowScanner) (sessionexec.ExecutionState, error) {
	var state sessionexec.ExecutionState
	var reason sql.NullString
	var updatedAt int64
	if err := row.Scan(&state.SessionID, &state.Mode, &state.Generation, &reason, &updatedAt); err != nil {
		return sessionexec.ExecutionState{}, err
	}
	if err := sessionexec.ValidateSessionID(state.SessionID); err != nil {
		return sessionexec.ExecutionState{}, sessionExecStoredConflict("execution state session")
	}
	if err := sessionexec.ValidateExecutionMode(state.Mode, true); err != nil ||
		state.Generation < 0 || state.Generation > sessionexec.MaxCommandSequence || updatedAt < 0 {
		return sessionexec.ExecutionState{}, sessionExecStoredConflict("execution state")
	}
	if reason.Valid {
		if err := sessionexec.ValidateErrorCode(reason.String); err != nil {
			return sessionexec.ExecutionState{}, sessionExecStoredConflict("execution state reason")
		}
		state.ReasonCode = reason.String
	}
	state.UpdatedAt = sessionExecTime(updatedAt)
	return state, nil
}

func sessionExecReadExecutionState(db *sessionExecConn, sessionID string) (sessionexec.ExecutionState, error) {
	state, err := scanSessionExecutionState(db.queryRow(`SELECT session_id, mode, generation, reason_code, updated_at_ms
		FROM session_execution_state WHERE session_id = ?`, sessionID))
	if errors.Is(err, sql.ErrNoRows) {
		return sessionexec.ExecutionState{}, sessionexec.ErrNotFound
	}
	if err != nil {
		return sessionexec.ExecutionState{}, fmt.Errorf("read session execution state: %w", err)
	}
	return state, nil
}

func sessionExecRequireHeadlessState(db *sessionExecConn, sessionID string, now int64) (sessionexec.ExecutionState, error) {
	if _, err := db.exec(`INSERT OR IGNORE INTO session_execution_state (
		session_id, mode, generation, reason_code, updated_at_ms
	) VALUES (?, ?, 0, NULL, ?)`, sessionID, sessionexec.ExecutionModeHeadless, now); err != nil {
		return sessionexec.ExecutionState{}, fmt.Errorf("initialize session execution state: %w", err)
	}
	state, err := sessionExecReadExecutionState(db, sessionID)
	if err != nil {
		return sessionexec.ExecutionState{}, err
	}
	if state.Mode != sessionexec.ExecutionModeHeadless {
		return sessionexec.ExecutionState{}, fmt.Errorf("%w: mode %s", sessionexec.ErrSessionQuiesced, state.Mode)
	}
	return state, nil
}

func (s *Store) Accept(ctx context.Context, request sessionexec.AcceptRequest) (sessionexec.Receipt, error) {
	if request.CommandID == "" {
		request.CommandID = sessionexec.NewCommandID()
	}
	request.Type = strings.ToLower(request.Type)
	if err := sessionexec.ValidateAcceptRequest(request, request.CommandID); err != nil {
		return sessionexec.Receipt{}, err
	}
	lane, err := sessionexec.LaneFor(request.Type)
	if err != nil {
		return sessionexec.Receipt{}, err
	}

	var receipt sessionexec.Receipt
	err = s.withSessionExecWrite(ctx, func(db *sessionExecConn) error {
		if err := sessionExecSessionExists(db, request.SessionID); err != nil {
			return err
		}
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		if _, err := sessionExecRequireHeadlessState(db, request.SessionID, now); err != nil {
			return err
		}

		stored, err := sessionExecLoadStoredCommand(db, request.SessionID, request.CommandID)
		if err == nil {
			if err := sessionExecValidateStoredAcceptance(stored); err != nil {
				return err
			}
			digest, digestErr := sessionexec.InputDigest(request, stored.command.Identity, lane, stored.target.String)
			if digestErr != nil {
				return digestErr
			}
			if digest != stored.command.InputDigest {
				return sessionexec.ErrIdempotencyConflict
			}
			receipt, err = sessionExecReadReceipt(db, request.SessionID, request.CommandID)
			if err == nil {
				receipt.Duplicate = true
			}
			return err
		}
		if !errors.Is(err, sessionexec.ErrNotFound) {
			return err
		}

		target := ""
		if request.Type == "steer" || request.Type == "interrupt" {
			err = db.queryRow(`SELECT command_id FROM session_commands
				WHERE session_id = ? AND lane = ? AND state = ? AND lease_expires_at_ms > ?
				ORDER BY sequence DESC LIMIT 1`, request.SessionID, sessionexec.LaneWork, sessionexec.StateRunning, now).
				Scan(&target)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("snapshot running work target: %w", err)
			}
			if target != "" {
				var signals int
				if err := db.queryRow(`SELECT COUNT(*) FROM session_commands
					WHERE session_id = ? AND target_command_id = ?
						AND command_type IN ('steer', 'interrupt')`, request.SessionID, target).Scan(&signals); err != nil {
					return fmt.Errorf("count command cancellation signals: %w", err)
				}
				if signals < 0 || signals >= sessionexec.MaxCancellationSignals {
					return fmt.Errorf("%w: target %s", sessionexec.ErrCancellationLimit, target)
				}
			}
		}
		var sequence int64
		if err := db.queryRow(`SELECT COALESCE(MAX(sequence), 0) + 1
			FROM session_commands WHERE session_id = ?`, request.SessionID).Scan(&sequence); err != nil {
			return fmt.Errorf("allocate command sequence: %w", err)
		}
		if sequence < 1 || sequence > sessionexec.MaxCommandSequence {
			return fmt.Errorf("%w: command sequence exhausted", sessionexec.ErrValidation)
		}
		identity := sessionexec.Identity{
			SessionID:  request.SessionID,
			RunID:      sessionexec.RunIDForSession(request.SessionID),
			TaskID:     sessionexec.ForegroundTaskID,
			CommandID:  request.CommandID,
			TurnID:     sessionexec.TurnID(request.CommandID, 0),
			Generation: 0,
			Sequence:   sequence,
		}
		digest, err := sessionexec.InputDigest(request, identity, lane, target)
		if err != nil {
			return err
		}
		_, err = db.exec(`INSERT INTO session_commands (
			session_id, command_id, run_id, task_id, turn_id, generation, sequence,
			lane, command_type, content, input_digest, accepted_by, target_command_id,
			state, accepted_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			identity.SessionID, identity.CommandID, identity.RunID, identity.TaskID,
			identity.TurnID, identity.Generation, identity.Sequence, lane, request.Type,
			request.Content, digest, request.AcceptedBy, nullIfEmpty(target),
			sessionexec.StateAccepted, now)
		if err != nil {
			return fmt.Errorf("insert session command: %w", err)
		}
		if _, err := db.exec(`UPDATE sessions SET last_active = ? WHERE session_id = ?`,
			sqliteTimestamp(sessionExecTime(now)), request.SessionID); err != nil {
			return fmt.Errorf("update command session activity: %w", err)
		}
		receipt = sessionexec.Receipt{
			Identity:        identity,
			Lane:            lane,
			State:           sessionexec.StateAccepted,
			TargetCommandID: target,
			AcceptedAt:      sessionExecTime(now),
		}
		return nil
	})
	return receipt, err
}

const sessionExecReceiptColumns = `
session_id, run_id, task_id, command_id, turn_id, generation, sequence,
lane, state, attempt, target_command_id, accepted_at_ms, started_at_ms, completed_at_ms,
error_code, error_text`

func sessionExecReadReceipt(db *sessionExecConn, sessionID, commandID string) (sessionexec.Receipt, error) {
	return scanSessionExecReceipt(db.queryRow(`SELECT `+sessionExecReceiptColumns+`
		FROM session_commands WHERE session_id = ? AND command_id = ?`, sessionID, commandID))
}

type rowScanner interface {
	Scan(...any) error
}

func scanSessionExecReceipt(row rowScanner) (sessionexec.Receipt, error) {
	var receipt sessionexec.Receipt
	var accepted int64
	var started, finished sql.NullInt64
	var targetCommandID, errorCode, errorText sql.NullString
	err := row.Scan(
		&receipt.SessionID, &receipt.RunID, &receipt.TaskID, &receipt.CommandID,
		&receipt.TurnID, &receipt.Generation, &receipt.Sequence, &receipt.Lane,
		&receipt.State, &receipt.Attempt, &targetCommandID, &accepted, &started, &finished,
		&errorCode, &errorText,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionexec.Receipt{}, sessionexec.ErrNotFound
	}
	if err != nil {
		return sessionexec.Receipt{}, err
	}
	receipt.AcceptedAt = sessionExecTime(accepted)
	receipt.TargetCommandID = targetCommandID.String
	receipt.StartedAt = sessionExecTimePtr(started)
	receipt.FinishedAt = sessionExecTimePtr(finished)
	receipt.ErrorCode = errorCode.String
	receipt.Error = errorText.String
	return receipt, nil
}

func (s *Store) Get(ctx context.Context, sessionID, commandID string) (sessionexec.Receipt, error) {
	if err := sessionexec.ValidateSessionID(sessionID); err != nil {
		return sessionexec.Receipt{}, err
	}
	if err := sessionexec.ValidateCommandID(commandID); err != nil {
		return sessionexec.Receipt{}, err
	}
	if s == nil || s.db == nil {
		return sessionexec.Receipt{}, ErrStoreClosed
	}
	row := s.db.QueryRowContext(ctx, `SELECT `+sessionExecReceiptColumns+`
		FROM session_commands WHERE session_id = ? AND command_id = ?`, sessionID, commandID)
	return scanSessionExecReceipt(row)
}

func (s *Store) List(ctx context.Context, query sessionexec.Query) ([]sessionexec.Receipt, error) {
	if err := sessionexec.ValidateSessionID(query.SessionID); err != nil {
		return nil, err
	}
	if query.AfterSequence < 0 {
		return nil, fmt.Errorf("%w: after sequence cannot be negative", sessionexec.ErrValidation)
	}
	if query.Limit == 0 {
		query.Limit = sessionexec.DefaultListLimit
	}
	if query.Limit < 1 || query.Limit > sessionexec.MaxListLimit {
		return nil, fmt.Errorf("%w: list limit out of range", sessionexec.ErrValidation)
	}
	if len(query.States) > 7 {
		return nil, fmt.Errorf("%w: too many states", sessionexec.ErrValidation)
	}
	if s == nil || s.db == nil {
		return nil, ErrStoreClosed
	}
	args := []any{query.SessionID, query.AfterSequence}
	statement := `SELECT ` + sessionExecReceiptColumns + ` FROM session_commands
		WHERE session_id = ? AND sequence > ?`
	seen := make(map[sessionexec.State]struct{}, len(query.States))
	if len(query.States) > 0 {
		placeholders := make([]string, 0, len(query.States))
		for _, state := range query.States {
			if !state.Valid() {
				return nil, fmt.Errorf("%w: invalid query state", sessionexec.ErrValidation)
			}
			if _, exists := seen[state]; exists {
				continue
			}
			seen[state] = struct{}{}
			placeholders = append(placeholders, "?")
			args = append(args, state)
		}
		statement += ` AND state IN (` + strings.Join(placeholders, ",") + `)`
	}
	statement += ` ORDER BY sequence ASC, command_id ASC LIMIT ?`
	args = append(args, query.Limit)
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, fmt.Errorf("list session commands: %w", err)
	}
	defer rows.Close()
	result := make([]sessionexec.Receipt, 0, query.Limit)
	for rows.Next() {
		receipt, err := scanSessionExecReceipt(rows)
		if err != nil {
			return nil, fmt.Errorf("scan session command: %w", err)
		}
		result = append(result, receipt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session commands: %w", err)
	}
	return result, nil
}

func (s *Store) Summary(ctx context.Context, sessionID string) (sessionexec.Summary, error) {
	if err := sessionexec.ValidateSessionID(sessionID); err != nil {
		return sessionexec.Summary{}, err
	}
	if s == nil || s.db == nil {
		return sessionexec.Summary{}, ErrStoreClosed
	}
	var result sessionexec.Summary
	result.SessionID = sessionID
	err := s.db.QueryRowContext(ctx, `SELECT
		COUNT(*),
		COALESCE(SUM(state = 'accepted'), 0),
		COALESCE(SUM(state = 'running'), 0),
		COALESCE(SUM(state = 'succeeded'), 0),
		COALESCE(SUM(state = 'failed'), 0),
		COALESCE(SUM(state = 'blocked'), 0),
		COALESCE(SUM(state = 'interrupted'), 0),
		COALESCE(SUM(state = 'cancelled'), 0),
		COALESCE(SUM(state = 'accepted' AND lane = 'work'), 0),
		COALESCE(SUM(state = 'accepted' AND lane = 'control'), 0),
		COALESCE(MAX(sequence), 0)
		FROM session_commands WHERE session_id = ?`, sessionID).Scan(
		&result.Total, &result.Accepted, &result.Running, &result.Succeeded,
		&result.Failed, &result.Blocked, &result.Interrupted, &result.Cancelled,
		&result.WorkPending, &result.ControlPending, &result.LastSequence,
	)
	if err != nil {
		return sessionexec.Summary{}, fmt.Errorf("summarize session commands: %w", err)
	}
	return result, nil
}

func (s *Store) GetExecutionState(ctx context.Context, sessionID string) (sessionexec.ExecutionState, error) {
	if err := sessionexec.ValidateSessionID(sessionID); err != nil {
		return sessionexec.ExecutionState{}, err
	}
	if s == nil || s.db == nil {
		return sessionexec.ExecutionState{}, ErrStoreClosed
	}
	state, readErr := scanSessionExecutionState(s.db.QueryRowContext(ctx, `SELECT
		session_id, mode, generation, reason_code, updated_at_ms
		FROM session_execution_state WHERE session_id = ?`, sessionID))
	if readErr == nil {
		return state, nil
	}
	if !errors.Is(readErr, sql.ErrNoRows) {
		return sessionexec.ExecutionState{}, fmt.Errorf("read session execution state: %w", readErr)
	}
	err := s.withSessionExecWrite(ctx, func(db *sessionExecConn) error {
		if err := sessionExecSessionExists(db, sessionID); err != nil {
			return err
		}
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		if _, err := db.exec(`INSERT OR IGNORE INTO session_execution_state (
			session_id, mode, generation, reason_code, updated_at_ms
		) VALUES (?, ?, 0, NULL, ?)`, sessionID, sessionexec.ExecutionModeHeadless, now); err != nil {
			return fmt.Errorf("initialize session execution state: %w", err)
		}
		state, err = sessionExecReadExecutionState(db, sessionID)
		return err
	})
	return state, err
}

func (s *Store) QuiesceSession(ctx context.Context, sessionID string, mode sessionexec.ExecutionMode, reasonCode string) (sessionexec.QuiesceResult, error) {
	if err := sessionexec.ValidateSessionID(sessionID); err != nil {
		return sessionexec.QuiesceResult{}, err
	}
	if err := sessionexec.ValidateExecutionMode(mode, false); err != nil {
		return sessionexec.QuiesceResult{}, err
	}
	reasonCode = strings.TrimSpace(reasonCode)
	if reasonCode == "" {
		if mode == sessionexec.ExecutionModeAdopted {
			reasonCode = "session_adopted"
		} else {
			reasonCode = "session_detached"
		}
	}
	if err := sessionexec.ValidateErrorCode(reasonCode); err != nil {
		return sessionexec.QuiesceResult{}, err
	}

	var result sessionexec.QuiesceResult
	err := s.withSessionExecWrite(ctx, func(db *sessionExecConn) error {
		if err := sessionExecSessionExists(db, sessionID); err != nil {
			return err
		}
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		state, err := sessionExecReadExecutionState(db, sessionID)
		switch {
		case errors.Is(err, sessionexec.ErrNotFound):
			if _, err := db.exec(`INSERT INTO session_execution_state (
				session_id, mode, generation, reason_code, updated_at_ms
			) VALUES (?, ?, 1, ?, ?)`, sessionID, mode, reasonCode, now); err != nil {
				return fmt.Errorf("insert quiesced session execution state: %w", err)
			}
		case err != nil:
			return err
		case state.Mode == mode:
			result.Duplicate = true
		case state.Mode != sessionexec.ExecutionModeHeadless:
			return fmt.Errorf("%w: mode %s", sessionexec.ErrSessionQuiesced, state.Mode)
		case state.Generation >= sessionexec.MaxCommandSequence:
			return fmt.Errorf("%w: execution generation exhausted", sessionexec.ErrValidation)
		default:
			changed, err := db.exec(`UPDATE session_execution_state SET
				mode = ?, generation = generation + 1, reason_code = ?, updated_at_ms = ?
				WHERE session_id = ? AND mode = ? AND generation = ?`,
				mode, reasonCode, now, sessionID, sessionexec.ExecutionModeHeadless, state.Generation)
			if err != nil {
				return fmt.Errorf("quiesce session execution state: %w", err)
			}
			rows, err := changed.RowsAffected()
			if err != nil {
				return fmt.Errorf("read session quiesce result: %w", err)
			}
			if rows != 1 {
				return sessionexec.ErrSessionQuiesced
			}
		}

		cancelled, err := db.exec(`UPDATE session_commands SET
			state = ?, completed_at_ms = ?, error_code = ?, error_text = NULL,
			outcome_json = '{}', completion_digest = NULL, completed_by = NULL,
			completion_lease_generation = NULL, lease_owner = NULL,
			lease_expires_at_ms = NULL, heartbeat_at_ms = NULL
			WHERE session_id = ? AND state IN (?, ?)`,
			sessionexec.StateCancelled, now, reasonCode, sessionID,
			sessionexec.StateAccepted, sessionexec.StateRunning)
		if err != nil {
			return fmt.Errorf("cancel quiesced session commands: %w", err)
		}
		result.Cancelled, err = cancelled.RowsAffected()
		if err != nil {
			return fmt.Errorf("read quiesced command count: %w", err)
		}
		if err := sessionExecMaterializeExpiredEffects(db, sessionID, now); err != nil {
			return err
		}
		result.State, err = sessionExecReadExecutionState(db, sessionID)
		return err
	})
	if err != nil {
		return result, err
	}
	drainCtx, cancel := context.WithTimeout(ctx, sessionexec.DefaultQuiesceDrainTimeout)
	defer cancel()
	if err := s.sessionExecDrainEffectPermits(drainCtx, sessionID); err != nil {
		return result, err
	}
	return result, nil
}

func (s *Store) sessionExecDrainEffectPermits(ctx context.Context, sessionID string) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	lastActive := -1
	for {
		active, err := s.sessionExecActiveEffectPermitCount(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("%w: inspect active effects: %v", sessionexec.ErrQuiescenceIncomplete, err)
		}
		lastActive = active
		if active == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %d blocking effect permit(s): %v", sessionexec.ErrQuiescenceIncomplete, lastActive, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (s *Store) sessionExecActiveEffectPermitCount(ctx context.Context, sessionID string) (int, error) {
	active := 0
	err := s.withSessionExecWrite(ctx, func(db *sessionExecConn) error {
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		if err := sessionExecMaterializeExpiredEffects(db, sessionID, now); err != nil {
			return err
		}
		if err := db.queryRow(`SELECT COUNT(*) FROM session_effect_permits
			WHERE session_id = ? AND state IN (?, ?)`, sessionID,
			sessionexec.EffectStateActive, sessionexec.EffectStateAmbiguous).Scan(&active); err != nil {
			return fmt.Errorf("count blocking session effect permits: %w", err)
		}
		if active < 0 || active > sessionexec.MaxActiveEffectPermits {
			return sessionexec.ErrEffectPermitConflict
		}
		return nil
	})
	return active, err
}

func (s *Store) CancellationRequested(ctx context.Context, sessionID, targetCommandID string) (bool, error) {
	if err := sessionexec.ValidateSessionID(sessionID); err != nil {
		return false, err
	}
	if err := sessionexec.ValidateCommandID(targetCommandID); err != nil {
		return false, err
	}
	if s == nil || s.db == nil {
		return false, ErrStoreClosed
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionExecStoredCommandColumns+`
		FROM session_commands
		WHERE session_id = ? AND target_command_id = ? AND command_type IN ('steer', 'interrupt')
		ORDER BY sequence ASC, command_id ASC LIMIT ?`,
		sessionID, targetCommandID, sessionexec.MaxCancellationSignals+1)
	if err != nil {
		return false, fmt.Errorf("list command cancellation signals: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		stored, err := scanSessionExecStoredCommand(rows)
		if err != nil {
			return false, fmt.Errorf("scan command cancellation signal: %w", err)
		}
		count++
		if count > sessionexec.MaxCancellationSignals {
			return false, fmt.Errorf("%w: cancellation signal limit exceeded", sessionexec.ErrValidation)
		}
		if err := sessionExecValidateStoredAcceptance(stored); err != nil {
			return false, err
		}
		if stored.target.String != targetCommandID ||
			(stored.command.Type != "steer" && stored.command.Type != "interrupt") {
			return false, sessionExecStoredConflict("cancellation signal")
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate command cancellation signals: %w", err)
	}
	return count > 0, nil
}

const sessionExecStoredCommandColumns = `
session_id, run_id, task_id, command_id, turn_id, generation, sequence,
lane, command_type, content, input_digest, accepted_by, target_command_id,
state, attempt, lease_generation, lease_owner, lease_expires_at_ms,
accepted_at_ms, started_at_ms, completion_digest, completed_by,
completion_lease_generation`

type sessionExecStoredCommand struct {
	command                  sessionexec.Command
	target                   sql.NullString
	state                    sessionexec.State
	attempt                  int
	leaseGeneration          int64
	leaseOwner               sql.NullString
	leaseExpires             sql.NullInt64
	acceptedAt               int64
	startedAt                sql.NullInt64
	completionDigest         sql.NullString
	completedBy              sql.NullString
	completedLeaseGeneration sql.NullInt64
}

func scanSessionExecStoredCommand(row rowScanner) (sessionExecStoredCommand, error) {
	var stored sessionExecStoredCommand
	err := row.Scan(
		&stored.command.SessionID, &stored.command.RunID, &stored.command.TaskID,
		&stored.command.CommandID, &stored.command.TurnID, &stored.command.Generation,
		&stored.command.Sequence, &stored.command.Lane, &stored.command.Type,
		&stored.command.Content, &stored.command.InputDigest, &stored.command.AcceptedBy,
		&stored.target, &stored.state, &stored.attempt, &stored.leaseGeneration,
		&stored.leaseOwner, &stored.leaseExpires, &stored.acceptedAt, &stored.startedAt,
		&stored.completionDigest, &stored.completedBy, &stored.completedLeaseGeneration,
	)
	return stored, err
}

func sessionExecLoadStoredCommand(db *sessionExecConn, sessionID, commandID string) (sessionExecStoredCommand, error) {
	stored, err := scanSessionExecStoredCommand(db.queryRow(`SELECT `+sessionExecStoredCommandColumns+`
		FROM session_commands WHERE session_id = ? AND command_id = ?`, sessionID, commandID))
	if errors.Is(err, sql.ErrNoRows) {
		return sessionExecStoredCommand{}, sessionexec.ErrNotFound
	}
	if err != nil {
		return sessionExecStoredCommand{}, fmt.Errorf("read stored session command: %w", err)
	}
	return stored, nil
}

func sessionExecStoredConflict(field string) error {
	return fmt.Errorf("%w: stored command %s mismatch", sessionexec.ErrIdempotencyConflict, field)
}

func sessionExecValidateStoredAcceptance(stored sessionExecStoredCommand) error {
	command := stored.command
	request := sessionexec.AcceptRequest{
		SessionID: command.SessionID, CommandID: command.CommandID, Type: command.Type,
		Content: command.Content, AcceptedBy: command.AcceptedBy,
	}
	if err := sessionexec.ValidateAcceptRequest(request, command.CommandID); err != nil {
		return sessionExecStoredConflict("acceptance envelope")
	}
	lane, err := sessionexec.LaneFor(command.Type)
	if err != nil || lane != command.Lane {
		return sessionExecStoredConflict("lane")
	}
	if command.RunID != sessionexec.RunIDForSession(command.SessionID) {
		return sessionExecStoredConflict("run identity")
	}
	if command.TaskID != sessionexec.ForegroundTaskID {
		return sessionExecStoredConflict("task identity")
	}
	if command.Generation != 0 || command.TurnID != sessionexec.TurnID(command.CommandID, command.Generation) {
		return sessionExecStoredConflict("turn identity")
	}
	if command.Sequence < 1 || command.Sequence > sessionexec.MaxCommandSequence {
		return sessionExecStoredConflict("sequence")
	}
	if stored.attempt < 0 || stored.attempt > sessionexec.MaxCommandAttempts ||
		stored.leaseGeneration < 0 || stored.leaseGeneration > int64(sessionexec.MaxCommandAttempts) {
		return sessionExecStoredConflict("attempt fence")
	}
	if stored.acceptedAt < 0 || (stored.startedAt.Valid && stored.startedAt.Int64 < 0) {
		return sessionExecStoredConflict("timestamp")
	}
	target := stored.target.String
	if command.Type != "steer" && command.Type != "interrupt" && target != "" {
		return sessionExecStoredConflict("target")
	}
	digest, err := sessionexec.InputDigest(request, command.Identity, command.Lane, target)
	if err != nil || digest != command.InputDigest {
		return sessionExecStoredConflict("input digest")
	}
	return nil
}

func sessionExecValidateStoredCommand(stored sessionExecStoredCommand) error {
	if err := sessionExecValidateStoredAcceptance(stored); err != nil {
		return err
	}
	command := stored.command
	switch stored.state {
	case sessionexec.StateAccepted:
		if stored.leaseOwner.Valid || stored.leaseExpires.Valid {
			return sessionExecStoredConflict("accepted lease")
		}
	case sessionexec.StateRunning:
		if stored.attempt == 0 || !stored.leaseOwner.Valid || !stored.leaseExpires.Valid || stored.leaseGeneration <= 0 {
			return sessionExecStoredConflict("running lease")
		}
		if err := sessionexec.ValidateLeaseRef(sessionexec.LeaseRef{
			SessionID: command.SessionID, CommandID: command.CommandID,
			Generation: command.Generation, Owner: stored.leaseOwner.String,
			LeaseGeneration: stored.leaseGeneration,
		}); err != nil {
			return sessionExecStoredConflict("running lease")
		}
	default:
		return sessionExecStoredConflict("claim state")
	}
	return nil
}

func sessionExecClaimTranscript(stored sessionExecStoredCommand) []sessionexec.TranscriptEntry {
	if stored.attempt == 0 {
		return nil
	}
	switch stored.command.Type {
	case "input", "queue", "steer":
		return []sessionexec.TranscriptEntry{{
			Ordinal: 0, Role: "user", Content: stored.command.Content, ContentType: "text",
		}}
	default:
		return nil
	}
}

func sessionExecLoadLaneRunning(db *sessionExecConn, sessionID string, lane sessionexec.Lane) (sessionExecStoredCommand, bool, error) {
	stored, err := scanSessionExecStoredCommand(db.queryRow(`SELECT `+sessionExecStoredCommandColumns+`
		FROM session_commands WHERE session_id = ? AND lane = ? AND state = ?
		ORDER BY sequence ASC LIMIT 1`, sessionID, lane, sessionexec.StateRunning))
	if errors.Is(err, sql.ErrNoRows) {
		return sessionExecStoredCommand{}, false, nil
	}
	if err != nil {
		return sessionExecStoredCommand{}, false, fmt.Errorf("read running lane command: %w", err)
	}
	return stored, true, nil
}

func sessionExecLoadNextAccepted(db *sessionExecConn, sessionID string, lane sessionexec.Lane) (sessionExecStoredCommand, error) {
	stored, err := scanSessionExecStoredCommand(db.queryRow(`SELECT `+sessionExecStoredCommandColumns+`
		FROM session_commands WHERE session_id = ? AND lane = ? AND state = ?
		ORDER BY sequence ASC, command_id ASC LIMIT 1`, sessionID, lane, sessionexec.StateAccepted))
	if errors.Is(err, sql.ErrNoRows) {
		return sessionExecStoredCommand{}, sessionexec.ErrNotFound
	}
	if err != nil {
		return sessionExecStoredCommand{}, fmt.Errorf("select next session command: %w", err)
	}
	return stored, nil
}

func (s *Store) ClaimNext(ctx context.Context, request sessionexec.ClaimRequest) (sessionexec.Command, error) {
	if err := sessionexec.ValidateClaimRequest(request); err != nil {
		return sessionexec.Command{}, err
	}
	var command sessionexec.Command
	var committedErr error
	err := s.withSessionExecWrite(ctx, func(db *sessionExecConn) error {
		if err := sessionExecSessionExists(db, request.SessionID); err != nil {
			return err
		}
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		if _, err := sessionExecRequireHeadlessState(db, request.SessionID, now); err != nil {
			return err
		}
		if err := sessionExecMaterializeExpiredEffects(db, request.SessionID, now); err != nil {
			return err
		}
		if err := sessionExecRequireNoSessionAmbiguousEffects(db, request.SessionID); err != nil {
			if errors.Is(err, sessionexec.ErrEffectAmbiguous) {
				committedErr = err
				return nil
			}
			return err
		}
		running, found, err := sessionExecLoadLaneRunning(db, request.SessionID, request.Lane)
		if err != nil {
			return err
		}
		if found {
			if err := sessionExecValidateStoredCommand(running); err != nil {
				return err
			}
			if err := sessionExecValidateMappings(db, running.command.Identity, sessionExecClaimTranscript(running)); err != nil {
				return err
			}
			if running.leaseExpires.Int64 > now {
				return sessionexec.ErrNotFound
			}
			if err := sessionExecRequireNoBlockingEffectPermits(db, sessionexec.LeaseRef{
				SessionID: running.command.SessionID, CommandID: running.command.CommandID,
				Generation: running.command.Generation, Owner: running.leaseOwner.String,
				LeaseGeneration: running.leaseGeneration,
			}); err != nil {
				return err
			}
			result, err := db.exec(`UPDATE session_commands SET
				state = ?, lease_owner = NULL, lease_expires_at_ms = NULL, heartbeat_at_ms = NULL
				WHERE session_id = ? AND command_id = ? AND generation = ? AND state = ?
					AND lease_owner = ? AND lease_generation = ? AND lease_expires_at_ms = ?`,
				sessionexec.StateAccepted, running.command.SessionID, running.command.CommandID,
				running.command.Generation, sessionexec.StateRunning, running.leaseOwner.String,
				running.leaseGeneration, running.leaseExpires.Int64)
			if err != nil {
				return fmt.Errorf("recover expired lane lease: %w", err)
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("read expired lane recovery result: %w", err)
			}
			if changed != 1 {
				return sessionexec.ErrLeaseStale
			}
		}

		stored, err := sessionExecLoadNextAccepted(db, request.SessionID, request.Lane)
		if err != nil {
			return err
		}
		if err := sessionExecValidateStoredCommand(stored); err != nil {
			return err
		}
		if err := sessionExecValidateMappings(db, stored.command.Identity, sessionExecClaimTranscript(stored)); err != nil {
			return err
		}
		if err := sessionExecRequireNoBlockingEffectPermits(db, sessionexec.LeaseRef{
			SessionID: stored.command.SessionID, CommandID: stored.command.CommandID,
			Generation: stored.command.Generation, Owner: request.Owner, LeaseGeneration: 1,
		}); err != nil {
			return err
		}
		if stored.attempt == sessionexec.MaxCommandAttempts || stored.leaseGeneration == int64(sessionexec.MaxCommandAttempts) {
			return fmt.Errorf("%w: command attempt limit reached", sessionexec.ErrValidation)
		}
		expires := now + request.LeaseDuration.Milliseconds()
		result, err := db.exec(`UPDATE session_commands SET
			state = ?, attempt = attempt + 1, lease_generation = lease_generation + 1,
			lease_owner = ?, lease_expires_at_ms = ?, heartbeat_at_ms = ?,
			started_at_ms = COALESCE(started_at_ms, ?)
			WHERE session_id = ? AND command_id = ? AND generation = ? AND state = ?
				AND attempt = ? AND lease_generation = ?`,
			sessionexec.StateRunning, request.Owner, expires, now, now,
			stored.command.SessionID, stored.command.CommandID, stored.command.Generation, sessionexec.StateAccepted,
			stored.attempt, stored.leaseGeneration)
		if err != nil {
			return fmt.Errorf("claim session command: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read command claim result: %w", err)
		}
		if changed != 1 {
			return sessionexec.ErrLeaseStale
		}

		command = stored.command
		command.TargetCommandID = stored.target.String
		command.State = sessionexec.StateRunning
		command.Attempt = stored.attempt + 1
		command.AcceptedAt = sessionExecTime(stored.acceptedAt)
		if stored.startedAt.Valid {
			command.StartedAt = sessionExecTimePtr(stored.startedAt)
		} else {
			startedNow := sessionExecTime(now)
			command.StartedAt = &startedNow
		}
		command.Lease = sessionexec.LeaseRef{
			SessionID: command.SessionID, CommandID: command.CommandID,
			Generation: command.Generation, Owner: request.Owner,
			LeaseGeneration: stored.leaseGeneration + 1,
			ExpiresAt:       sessionExecTime(expires),
		}

		if command.Type == "input" || command.Type == "queue" || command.Type == "steer" {
			entry := sessionexec.TranscriptEntry{
				Ordinal: 0, Role: "user", Content: command.Content, ContentType: "text",
			}
			inserted, err := sessionExecAppendTranscript(db, command.Identity, entry, now)
			if err != nil {
				return err
			}
			if inserted {
				if err := sessionExecUpdateMessageStats(db, command.SessionID, 1, entry.Tokens, now); err != nil {
					return err
				}
			}
		}
		command.NextTranscriptOrdinal = len(sessionExecClaimTranscript(sessionExecStoredCommand{
			command: command, attempt: command.Attempt,
		}))
		return nil
	})
	if err != nil {
		return command, err
	}
	return command, committedErr
}

func (s *Store) Heartbeat(ctx context.Context, ref sessionexec.LeaseRef, duration time.Duration) (sessionexec.LeaseRef, error) {
	if err := sessionexec.ValidateLeaseRef(ref); err != nil {
		return sessionexec.LeaseRef{}, err
	}
	if err := sessionexec.ValidateLeaseDuration(duration); err != nil {
		return sessionexec.LeaseRef{}, err
	}
	var updated sessionexec.LeaseRef
	var committedErr error
	err := s.withSessionExecWrite(ctx, func(db *sessionExecConn) error {
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		if _, err := sessionExecRequireHeadlessState(db, ref.SessionID, now); err != nil {
			return err
		}
		if err := sessionExecMaterializeExpiredEffects(db, ref.SessionID, now); err != nil {
			return err
		}
		if err := sessionExecRequireNoSessionAmbiguousEffects(db, ref.SessionID); err != nil {
			if errors.Is(err, sessionexec.ErrEffectAmbiguous) {
				committedErr = err
				return nil
			}
			return err
		}
		if err := sessionExecCheckLease(db, ref, now, true); err != nil {
			return err
		}
		var currentExpiry int64
		if err := db.queryRow(`SELECT lease_expires_at_ms FROM session_commands
			WHERE session_id = ? AND command_id = ?`, ref.SessionID, ref.CommandID).Scan(&currentExpiry); err != nil {
			return fmt.Errorf("read heartbeat lease expiry: %w", err)
		}
		active, err := sessionExecValidateHeartbeatEffects(db, ref, currentExpiry)
		if err != nil {
			return err
		}
		expires := now + duration.Milliseconds()
		result, err := db.exec(`UPDATE session_commands
			SET heartbeat_at_ms = ?, lease_expires_at_ms = ?
			WHERE session_id = ? AND command_id = ? AND generation = ? AND state = ?
				AND lease_owner = ? AND lease_generation = ? AND lease_expires_at_ms > ?`,
			now, expires, ref.SessionID, ref.CommandID, ref.Generation,
			sessionexec.StateRunning, ref.Owner, ref.LeaseGeneration, now)
		if err != nil {
			return fmt.Errorf("heartbeat session command: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read heartbeat result: %w", err)
		}
		if changed != 1 {
			return sessionexec.ErrLeaseStale
		}
		renewed, err := db.exec(`UPDATE session_effect_permits SET expires_at_ms = ?
			WHERE session_id = ? AND command_id = ? AND generation = ?
				AND lease_owner = ? AND lease_generation = ? AND state = ? AND expires_at_ms > ?`,
			expires, ref.SessionID, ref.CommandID, ref.Generation,
			ref.Owner, ref.LeaseGeneration, sessionexec.EffectStateActive, now)
		if err != nil {
			return fmt.Errorf("renew session effect permits: %w", err)
		}
		if changed, err := renewed.RowsAffected(); err != nil {
			return fmt.Errorf("read renewed effect permit count: %w", err)
		} else if int(changed) != active {
			return sessionexec.ErrEffectPermitConflict
		}
		updated = ref
		updated.ExpiresAt = sessionExecTime(expires)
		return nil
	})
	if err != nil {
		return updated, err
	}
	return updated, committedErr
}

func (s *Store) BeginEffect(ctx context.Context, request sessionexec.EffectRequest) (sessionexec.EffectPermit, error) {
	if err := sessionexec.ValidateEffectRequest(request); err != nil {
		return sessionexec.EffectPermit{}, err
	}
	if s == nil || s.db == nil {
		return sessionexec.EffectPermit{}, ErrStoreClosed
	}
	var permit sessionexec.EffectPermit
	var committedErr error
	err := s.withSessionExecWrite(ctx, func(db *sessionExecConn) error {
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		if _, err := sessionExecRequireHeadlessState(db, request.Lease.SessionID, now); err != nil {
			return err
		}
		if err := sessionExecMaterializeExpiredEffects(db, request.Lease.SessionID, now); err != nil {
			return err
		}
		if err := sessionExecRequireNoSessionAmbiguousEffects(db, request.Lease.SessionID); err != nil {
			if errors.Is(err, sessionexec.ErrEffectAmbiguous) {
				committedErr = err
				return nil
			}
			return err
		}

		existing, found, err := sessionExecReadEffectPermit(db, request.Lease, request.EffectID)
		if err != nil {
			return err
		}
		if found {
			if existing.Kind != request.Kind {
				return sessionexec.ErrEffectPermitConflict
			}
			leaseConflict := (existing.State == sessionexec.EffectStateActive || existing.State == sessionexec.EffectStateAmbiguous) &&
				!sessionExecEffectPermitMatches(existing, request)
			storedCommand, err := sessionExecLoadStoredCommand(db, request.Lease.SessionID, request.Lease.CommandID)
			if err != nil {
				return err
			}
			if err := sessionExecValidateStoredAcceptance(storedCommand); err != nil {
				return err
			}
			if !storedCommand.state.Terminal() {
				if err := sessionExecCheckLease(db, request.Lease, now, true); err != nil {
					return err
				}
			}
			if existing.State == sessionexec.EffectStateActive {
				changed, err := db.exec(`UPDATE session_effect_permits SET
					state = ?, ambiguous_at_ms = ?
					WHERE session_id = ? AND command_id = ? AND generation = ? AND effect_id = ? AND state = ?`,
					sessionexec.EffectStateAmbiguous, now, request.Lease.SessionID,
					request.Lease.CommandID, request.Lease.Generation, request.EffectID,
					sessionexec.EffectStateActive)
				if err != nil {
					return fmt.Errorf("mark duplicate effect ambiguous: %w", err)
				}
				rows, err := changed.RowsAffected()
				if err != nil || rows != 1 {
					return sessionexec.ErrEffectPermitConflict
				}
				existing.State = sessionexec.EffectStateAmbiguous
				ambiguousAt := sessionExecTime(now)
				existing.AmbiguousAt = &ambiguousAt
			}
			blockingPermit := existing
			blockingPermit.Lease = request.Lease
			if err := sessionExecFailCommandForAmbiguousEffect(db, blockingPermit, now); err != nil {
				return err
			}
			existing.Duplicate = true
			permit = existing
			committedErr = sessionexec.ErrEffectAmbiguous
			if leaseConflict {
				committedErr = errors.Join(sessionexec.ErrEffectAmbiguous, sessionexec.ErrEffectPermitConflict)
			}
			return nil
		}

		stored, err := sessionExecLoadStoredCommand(db, request.Lease.SessionID, request.Lease.CommandID)
		if err != nil {
			return err
		}
		if err := sessionExecValidateStoredAcceptance(stored); err != nil {
			return err
		}
		if err := sessionExecCheckLease(db, request.Lease, now, true); err != nil {
			return err
		}

		var active, total int
		if err := db.queryRow(`SELECT
			COALESCE(SUM(CASE WHEN state IN (?, ?) THEN 1 ELSE 0 END), 0), COUNT(*)
			FROM session_effect_permits WHERE session_id = ?`,
			sessionexec.EffectStateActive, sessionexec.EffectStateAmbiguous,
			request.Lease.SessionID).Scan(&active, &total); err != nil {
			return fmt.Errorf("count session effect permits: %w", err)
		}
		if active < 0 || total < active || active >= sessionexec.MaxActiveEffectPermits ||
			total >= sessionexec.MaxEffectPermitsPerSession {
			return sessionexec.ErrEffectPermitLimit
		}
		var leaseExpires int64
		if err := db.queryRow(`SELECT lease_expires_at_ms FROM session_commands
			WHERE session_id = ? AND command_id = ?`, request.Lease.SessionID, request.Lease.CommandID).
			Scan(&leaseExpires); err != nil {
			return fmt.Errorf("read effect command lease expiry: %w", err)
		}
		if leaseExpires <= now {
			return sessionexec.ErrLeaseExpired
		}
		if _, err := db.exec(`INSERT INTO session_effect_permits (
			session_id, command_id, generation, effect_id, kind,
			lease_owner, lease_generation, state, expires_at_ms, created_at_ms
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			request.Lease.SessionID, request.Lease.CommandID, request.Lease.Generation,
			request.EffectID, request.Kind, request.Lease.Owner,
			request.Lease.LeaseGeneration, sessionexec.EffectStateActive, leaseExpires, now); err != nil {
			return fmt.Errorf("insert session effect permit: %w", err)
		}
		permit = sessionexec.EffectPermit{
			EffectRequest: request,
			State:         sessionexec.EffectStateActive,
			ExpiresAt:     sessionExecTime(leaseExpires),
			CreatedAt:     sessionExecTime(now),
		}
		return nil
	})
	if err != nil {
		return sessionexec.EffectPermit{}, err
	}
	return permit, committedErr
}

func (s *Store) EndEffect(ctx context.Context, permit sessionexec.EffectPermit) error {
	if err := sessionexec.ValidateEffectPermit(permit); err != nil {
		return err
	}
	if s == nil || s.db == nil {
		return ErrStoreClosed
	}
	return s.withSessionExecWrite(ctx, func(db *sessionExecConn) error {
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		if err := sessionExecMaterializeExpiredEffects(db, permit.Lease.SessionID, now); err != nil {
			return err
		}
		stored, found, err := sessionExecReadEffectPermit(db, permit.Lease, permit.EffectID)
		if err != nil {
			return err
		}
		if !found {
			storedCommand, loadErr := sessionExecLoadStoredCommand(db, permit.Lease.SessionID, permit.Lease.CommandID)
			if loadErr == nil && storedCommand.state.Terminal() &&
				storedCommand.command.Generation == permit.Lease.Generation &&
				storedCommand.completedBy.Valid && storedCommand.completedBy.String == permit.Lease.Owner &&
				storedCommand.completedLeaseGeneration.Valid &&
				storedCommand.completedLeaseGeneration.Int64 == permit.Lease.LeaseGeneration {
				return nil
			}
			return sessionexec.ErrNotFound
		}
		if !sessionExecEffectPermitMatches(stored, permit.EffectRequest) {
			return sessionexec.ErrEffectPermitConflict
		}
		switch stored.State {
		case sessionexec.EffectStateEnded:
			return nil
		case sessionexec.EffectStateActive, sessionexec.EffectStateAmbiguous:
		case sessionexec.EffectStateResolved:
			return sessionexec.ErrEffectPermitConflict
		default:
			return sessionexec.ErrEffectPermitConflict
		}
		result, err := db.exec(`UPDATE session_effect_permits SET state = ?, ended_at_ms = ?
			WHERE session_id = ? AND command_id = ? AND generation = ? AND effect_id = ?
				AND kind = ? AND lease_owner = ? AND lease_generation = ? AND state IN (?, ?)`,
			sessionexec.EffectStateEnded, now,
			permit.Lease.SessionID, permit.Lease.CommandID, permit.Lease.Generation,
			permit.EffectID, permit.Kind, permit.Lease.Owner, permit.Lease.LeaseGeneration,
			sessionexec.EffectStateActive, sessionexec.EffectStateAmbiguous)
		if err != nil {
			return fmt.Errorf("end session effect permit: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read session effect permit end result: %w", err)
		}
		if changed != 1 {
			return sessionexec.ErrEffectPermitConflict
		}
		return nil
	})
}

func (s *Store) ResolveAmbiguousEffect(ctx context.Context, request sessionexec.EffectResolutionRequest) (sessionexec.EffectPermit, error) {
	if err := sessionexec.ValidateEffectResolutionRequest(request); err != nil {
		return sessionexec.EffectPermit{}, err
	}
	if s == nil || s.db == nil {
		return sessionexec.EffectPermit{}, ErrStoreClosed
	}
	var resolved sessionexec.EffectPermit
	err := s.withSessionExecWrite(ctx, func(db *sessionExecConn) error {
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		state, err := sessionExecReadExecutionState(db, request.SessionID)
		if err != nil {
			return err
		}
		if state.Mode == sessionexec.ExecutionModeHeadless {
			return sessionexec.ErrEffectPermitConflict
		}
		if err := sessionExecMaterializeExpiredEffects(db, request.SessionID, now); err != nil {
			return err
		}
		lease := sessionexec.LeaseRef{
			SessionID: request.SessionID, CommandID: request.CommandID, Generation: request.Generation,
		}
		permit, found, err := sessionExecReadEffectPermit(db, lease, request.EffectID)
		if err != nil {
			return err
		}
		if !found {
			return sessionexec.ErrNotFound
		}
		if permit.State == sessionexec.EffectStateResolved {
			if permit.ResolvedBy != request.Actor || permit.ResolutionReason != request.Reason {
				return sessionexec.ErrEffectPermitConflict
			}
			permit.Duplicate = true
			resolved = permit
			return nil
		}
		if permit.State != sessionexec.EffectStateAmbiguous || permit.ExpiresAt.UnixMilli() > now {
			return sessionexec.ErrEffectPermitConflict
		}
		stored, err := sessionExecLoadStoredCommand(db, request.SessionID, request.CommandID)
		if err != nil {
			return err
		}
		if err := sessionExecValidateStoredAcceptance(stored); err != nil {
			return err
		}
		if stored.command.Generation != request.Generation ||
			(stored.state != sessionexec.StateBlocked && stored.state != sessionexec.StateCancelled) {
			return sessionexec.ErrEffectPermitConflict
		}
		result, err := db.exec(`UPDATE session_effect_permits SET
			state = ?, resolved_at_ms = ?, resolved_by = ?, resolution_reason = ?
			WHERE session_id = ? AND command_id = ? AND generation = ? AND effect_id = ?
				AND state = ? AND expires_at_ms <= ?`,
			sessionexec.EffectStateResolved, now, request.Actor, nullIfEmpty(request.Reason),
			request.SessionID, request.CommandID, request.Generation, request.EffectID,
			sessionexec.EffectStateAmbiguous, now)
		if err != nil {
			return fmt.Errorf("resolve ambiguous effect: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return sessionexec.ErrEffectPermitConflict
		}
		resolved, found, err = sessionExecReadEffectPermit(db, lease, request.EffectID)
		if err != nil {
			return err
		}
		if !found || resolved.State != sessionexec.EffectStateResolved {
			return sessionexec.ErrEffectPermitConflict
		}
		return nil
	})
	return resolved, err
}

const sessionExecEffectPermitColumns = `session_id, command_id, generation, effect_id, kind,
	lease_owner, lease_generation, state, expires_at_ms, created_at_ms,
	ambiguous_at_ms, ended_at_ms, resolved_at_ms, resolved_by, resolution_reason`

func sessionExecReadEffectPermit(db *sessionExecConn, lease sessionexec.LeaseRef, effectID string) (sessionexec.EffectPermit, bool, error) {
	permit, err := scanSessionExecEffectPermit(db.queryRow(`SELECT `+sessionExecEffectPermitColumns+`
		FROM session_effect_permits
		WHERE session_id = ? AND command_id = ? AND generation = ? AND effect_id = ?`,
		lease.SessionID, lease.CommandID, lease.Generation, effectID))
	if errors.Is(err, sql.ErrNoRows) {
		return sessionexec.EffectPermit{}, false, nil
	}
	if err != nil {
		return sessionexec.EffectPermit{}, false, fmt.Errorf("read session effect permit: %w", err)
	}
	return permit, true, nil
}

func scanSessionExecEffectPermit(row rowScanner) (sessionexec.EffectPermit, error) {
	var permit sessionexec.EffectPermit
	var expires, created int64
	var ambiguous, ended, resolved sql.NullInt64
	var resolvedBy, resolutionReason sql.NullString
	if err := row.Scan(
		&permit.Lease.SessionID, &permit.Lease.CommandID, &permit.Lease.Generation,
		&permit.EffectID, &permit.Kind, &permit.Lease.Owner, &permit.Lease.LeaseGeneration,
		&permit.State, &expires, &created, &ambiguous, &ended, &resolved,
		&resolvedBy, &resolutionReason,
	); err != nil {
		return sessionexec.EffectPermit{}, err
	}
	permit.ExpiresAt = sessionExecTime(expires)
	permit.Lease.ExpiresAt = permit.ExpiresAt
	permit.CreatedAt = sessionExecTime(created)
	permit.AmbiguousAt = sessionExecTimePtr(ambiguous)
	permit.EndedAt = sessionExecTimePtr(ended)
	permit.ResolvedAt = sessionExecTimePtr(resolved)
	permit.ResolvedBy = resolvedBy.String
	permit.ResolutionReason = resolutionReason.String
	if expires < 0 || created < 0 || expires < created || sessionexec.ValidateEffectPermit(permit) != nil {
		return sessionexec.EffectPermit{}, sessionexec.ErrEffectPermitConflict
	}
	return permit, nil
}

func sessionExecEffectPermitMatches(permit sessionexec.EffectPermit, request sessionexec.EffectRequest) bool {
	return permit.EffectID == request.EffectID && permit.Kind == request.Kind &&
		permit.Lease.SessionID == request.Lease.SessionID &&
		permit.Lease.CommandID == request.Lease.CommandID &&
		permit.Lease.Generation == request.Lease.Generation &&
		permit.Lease.Owner == request.Lease.Owner &&
		permit.Lease.LeaseGeneration == request.Lease.LeaseGeneration
}

func sessionExecListEffectPermits(db *sessionExecConn, sessionID string) ([]sessionexec.EffectPermit, error) {
	rows, err := db.query(`SELECT `+sessionExecEffectPermitColumns+`
		FROM session_effect_permits
		WHERE session_id = ? ORDER BY command_id, generation, effect_id LIMIT ?`,
		sessionID, sessionexec.MaxEffectPermitsPerSession+1)
	if err != nil {
		return nil, fmt.Errorf("list session effect permits: %w", err)
	}
	defer rows.Close()
	permits := make([]sessionexec.EffectPermit, 0)
	for rows.Next() {
		permit, err := scanSessionExecEffectPermit(rows)
		if err != nil {
			return nil, sessionexec.ErrEffectPermitConflict
		}
		permits = append(permits, permit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session effect permits: %w", err)
	}
	if len(permits) > sessionexec.MaxEffectPermitsPerSession {
		return nil, sessionexec.ErrEffectPermitConflict
	}
	blocking := 0
	for _, permit := range permits {
		if permit.State == sessionexec.EffectStateActive || permit.State == sessionexec.EffectStateAmbiguous {
			blocking++
		}
	}
	if blocking > sessionexec.MaxActiveEffectPermits {
		return nil, sessionexec.ErrEffectPermitConflict
	}
	return permits, nil
}

func sessionExecMaterializeExpiredEffects(db *sessionExecConn, sessionID string, now int64) error {
	permits, err := sessionExecListEffectPermits(db, sessionID)
	if err != nil {
		return err
	}
	for _, permit := range permits {
		if permit.State != sessionexec.EffectStateActive || permit.ExpiresAt.UnixMilli() > now {
			continue
		}
		changed, err := db.exec(`UPDATE session_effect_permits SET state = ?, ambiguous_at_ms = ?
			WHERE session_id = ? AND command_id = ? AND generation = ? AND effect_id = ?
				AND state = ? AND expires_at_ms = ?`,
			sessionexec.EffectStateAmbiguous, now, permit.Lease.SessionID,
			permit.Lease.CommandID, permit.Lease.Generation, permit.EffectID,
			sessionexec.EffectStateActive, permit.ExpiresAt.UnixMilli())
		if err != nil {
			return fmt.Errorf("materialize expired effect permit: %w", err)
		}
		rows, err := changed.RowsAffected()
		if err != nil || rows != 1 {
			return sessionexec.ErrEffectPermitConflict
		}
		permit.State = sessionexec.EffectStateAmbiguous
		ambiguousAt := sessionExecTime(now)
		permit.AmbiguousAt = &ambiguousAt
		if err := sessionExecFailCommandForAmbiguousEffect(db, permit, now); err != nil {
			return err
		}
	}
	return nil
}

func sessionExecFailCommandForAmbiguousEffect(db *sessionExecConn, permit sessionexec.EffectPermit, now int64) error {
	stored, err := sessionExecLoadStoredCommand(db, permit.Lease.SessionID, permit.Lease.CommandID)
	if err != nil {
		return err
	}
	if err := sessionExecValidateStoredAcceptance(stored); err != nil {
		return err
	}
	if stored.command.Generation != permit.Lease.Generation {
		return sessionexec.ErrEffectPermitConflict
	}
	if stored.state.Terminal() {
		return nil
	}
	if stored.state == sessionexec.StateRunning && (!stored.leaseOwner.Valid ||
		stored.leaseOwner.String != permit.Lease.Owner || stored.leaseGeneration != permit.Lease.LeaseGeneration) {
		return sessionexec.ErrEffectPermitConflict
	}
	if stored.state != sessionexec.StateRunning && stored.state != sessionexec.StateAccepted {
		return sessionexec.ErrEffectPermitConflict
	}
	result, err := db.exec(`UPDATE session_commands SET
		state = ?, completed_at_ms = ?, error_code = ?, error_text = NULL,
		outcome_json = '{}', completion_digest = NULL, completed_by = NULL,
		completion_lease_generation = NULL, lease_owner = NULL,
		lease_expires_at_ms = NULL, heartbeat_at_ms = NULL
		WHERE session_id = ? AND command_id = ? AND generation = ? AND state IN (?, ?)`,
		sessionexec.StateBlocked, now, "ambiguous_effect", permit.Lease.SessionID,
		permit.Lease.CommandID, permit.Lease.Generation,
		sessionexec.StateAccepted, sessionexec.StateRunning)
	if err != nil {
		return fmt.Errorf("block command for ambiguous effect: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return sessionexec.ErrEffectPermitConflict
	}
	return nil
}

func sessionExecCommandEffectPermits(db *sessionExecConn, ref sessionexec.LeaseRef) ([]sessionexec.EffectPermit, error) {
	rows, err := db.query(`SELECT `+sessionExecEffectPermitColumns+`
		FROM session_effect_permits
		WHERE session_id = ? AND command_id = ? AND generation = ?
		ORDER BY effect_id LIMIT ?`, ref.SessionID, ref.CommandID, ref.Generation,
		sessionexec.MaxEffectPermitsPerSession+1)
	if err != nil {
		return nil, fmt.Errorf("list command effect permits: %w", err)
	}
	defer rows.Close()
	permits := make([]sessionexec.EffectPermit, 0)
	for rows.Next() {
		permit, err := scanSessionExecEffectPermit(rows)
		if err != nil {
			return nil, sessionexec.ErrEffectPermitConflict
		}
		permits = append(permits, permit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate command effect permits: %w", err)
	}
	if len(permits) > sessionexec.MaxEffectPermitsPerSession {
		return nil, sessionexec.ErrEffectPermitConflict
	}
	return permits, nil
}

func sessionExecRequireNoBlockingEffectPermits(db *sessionExecConn, ref sessionexec.LeaseRef) error {
	permits, err := sessionExecCommandEffectPermits(db, ref)
	if err != nil {
		return err
	}
	for _, permit := range permits {
		if permit.State == sessionexec.EffectStateActive || permit.State == sessionexec.EffectStateAmbiguous {
			return sessionexec.ErrEffectAmbiguous
		}
	}
	return nil
}

func sessionExecRequireNoSessionAmbiguousEffects(db *sessionExecConn, sessionID string) error {
	permits, err := sessionExecListEffectPermits(db, sessionID)
	if err != nil {
		return err
	}
	for _, permit := range permits {
		if permit.State == sessionexec.EffectStateAmbiguous {
			return sessionexec.ErrEffectAmbiguous
		}
	}
	return nil
}

func sessionExecValidateHeartbeatEffects(db *sessionExecConn, ref sessionexec.LeaseRef, leaseExpiry int64) (int, error) {
	permits, err := sessionExecCommandEffectPermits(db, ref)
	if err != nil {
		return 0, err
	}
	active := 0
	for _, permit := range permits {
		switch permit.State {
		case sessionexec.EffectStateActive:
			if !sessionExecEffectPermitMatches(permit, sessionexec.EffectRequest{
				Lease: ref, EffectID: permit.EffectID, Kind: permit.Kind,
			}) || permit.ExpiresAt.UnixMilli() != leaseExpiry {
				return 0, sessionexec.ErrEffectPermitConflict
			}
			active++
		case sessionexec.EffectStateAmbiguous:
			return 0, sessionexec.ErrEffectAmbiguous
		}
	}
	return active, nil
}

func sessionExecCleanupFinishedEffects(db *sessionExecConn, ref sessionexec.LeaseRef) error {
	permits, err := sessionExecCommandEffectPermits(db, ref)
	if err != nil {
		return err
	}
	finished := 0
	for _, permit := range permits {
		switch permit.State {
		case sessionexec.EffectStateActive, sessionexec.EffectStateAmbiguous:
			return sessionexec.ErrEffectAmbiguous
		case sessionexec.EffectStateEnded, sessionexec.EffectStateResolved:
			finished++
		default:
			return sessionexec.ErrEffectPermitConflict
		}
	}
	if finished == 0 {
		return nil
	}
	result, err := db.exec(`DELETE FROM session_effect_permits
		WHERE session_id = ? AND command_id = ? AND generation = ? AND state IN (?, ?)`,
		ref.SessionID, ref.CommandID, ref.Generation,
		sessionexec.EffectStateEnded, sessionexec.EffectStateResolved)
	if err != nil {
		return fmt.Errorf("cleanup completed effect permits: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read completed effect cleanup result: %w", err)
	}
	if int(changed) != finished {
		return sessionexec.ErrEffectPermitConflict
	}
	return nil
}

func (s *Store) Release(ctx context.Context, ref sessionexec.LeaseRef) (sessionexec.Receipt, error) {
	if err := sessionexec.ValidateLeaseRef(ref); err != nil {
		return sessionexec.Receipt{}, err
	}
	var receipt sessionexec.Receipt
	var committedErr error
	err := s.withSessionExecWrite(ctx, func(db *sessionExecConn) error {
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		if err := sessionExecMaterializeExpiredEffects(db, ref.SessionID, now); err != nil {
			return err
		}
		if err := sessionExecRequireNoSessionAmbiguousEffects(db, ref.SessionID); err != nil {
			if errors.Is(err, sessionexec.ErrEffectAmbiguous) {
				committedErr = err
				return nil
			}
			return err
		}
		if err := sessionExecCheckLease(db, ref, now, false); err != nil {
			return err
		}
		if err := sessionExecRequireNoBlockingEffectPermits(db, ref); err != nil {
			return err
		}
		result, err := db.exec(`UPDATE session_commands
			SET state = ?, lease_owner = NULL, lease_expires_at_ms = NULL, heartbeat_at_ms = NULL
			WHERE session_id = ? AND command_id = ? AND generation = ? AND state = ?
				AND lease_owner = ? AND lease_generation = ?`,
			sessionexec.StateAccepted, ref.SessionID, ref.CommandID, ref.Generation,
			sessionexec.StateRunning, ref.Owner, ref.LeaseGeneration)
		if err != nil {
			return fmt.Errorf("release session command: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read release result: %w", err)
		}
		if changed != 1 {
			return sessionexec.ErrLeaseStale
		}
		receipt, err = sessionExecReadReceipt(db, ref.SessionID, ref.CommandID)
		return err
	})
	if err != nil {
		return receipt, err
	}
	return receipt, committedErr
}

func (s *Store) RecoverExpired(ctx context.Context, sessionID string) (int, error) {
	if err := sessionexec.ValidateSessionID(sessionID); err != nil {
		return 0, err
	}
	count := 0
	var committedErr error
	err := s.withSessionExecWrite(ctx, func(db *sessionExecConn) error {
		count = 0
		if err := sessionExecSessionExists(db, sessionID); err != nil {
			return err
		}
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		if _, err := sessionExecRequireHeadlessState(db, sessionID, now); err != nil {
			return err
		}
		if err := sessionExecMaterializeExpiredEffects(db, sessionID, now); err != nil {
			return err
		}
		if err := sessionExecRequireNoSessionAmbiguousEffects(db, sessionID); err != nil {
			if errors.Is(err, sessionexec.ErrEffectAmbiguous) {
				committedErr = err
				return nil
			}
			return err
		}
		rows, err := db.query(`SELECT `+sessionExecStoredCommandColumns+`
			FROM session_commands WHERE session_id = ? AND state = ? AND lease_expires_at_ms <= ?
			ORDER BY lane ASC, sequence ASC LIMIT 3`, sessionID, sessionexec.StateRunning, now)
		if err != nil {
			return fmt.Errorf("list expired session commands: %w", err)
		}
		expired := make([]sessionExecStoredCommand, 0, 2)
		for rows.Next() {
			stored, err := scanSessionExecStoredCommand(rows)
			if err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan expired session command: %w", err)
			}
			expired = append(expired, stored)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate expired session commands: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close expired session commands: %w", err)
		}
		if len(expired) > 2 {
			return sessionExecStoredConflict("running lane cardinality")
		}
		for _, stored := range expired {
			if err := sessionExecValidateStoredCommand(stored); err != nil {
				return err
			}
			if err := sessionExecValidateMappings(db, stored.command.Identity, sessionExecClaimTranscript(stored)); err != nil {
				return err
			}
			if err := sessionExecRequireNoBlockingEffectPermits(db, sessionexec.LeaseRef{
				SessionID: stored.command.SessionID, CommandID: stored.command.CommandID,
				Generation: stored.command.Generation, Owner: stored.leaseOwner.String,
				LeaseGeneration: stored.leaseGeneration,
			}); err != nil {
				return err
			}
		}
		for _, stored := range expired {
			result, err := db.exec(`UPDATE session_commands SET
				state = ?, lease_owner = NULL, lease_expires_at_ms = NULL, heartbeat_at_ms = NULL
				WHERE session_id = ? AND command_id = ? AND generation = ? AND state = ?
					AND lease_owner = ? AND lease_generation = ? AND lease_expires_at_ms = ?`,
				sessionexec.StateAccepted, stored.command.SessionID, stored.command.CommandID,
				stored.command.Generation, sessionexec.StateRunning, stored.leaseOwner.String,
				stored.leaseGeneration, stored.leaseExpires.Int64)
			if err != nil {
				return fmt.Errorf("recover expired session command: %w", err)
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("read expired recovery result: %w", err)
			}
			if changed != 1 {
				return sessionexec.ErrLeaseStale
			}
			count++
		}
		return nil
	})
	if err != nil {
		return count, err
	}
	return count, committedErr
}

func sessionExecCheckLease(db *sessionExecConn, ref sessionexec.LeaseRef, now int64, requireUnexpired bool) error {
	var state sessionexec.State
	var generation int
	var owner sql.NullString
	var leaseGeneration int64
	var expires sql.NullInt64
	err := db.queryRow(`SELECT state, generation, lease_owner, lease_generation, lease_expires_at_ms
		FROM session_commands WHERE session_id = ? AND command_id = ?`, ref.SessionID, ref.CommandID).
		Scan(&state, &generation, &owner, &leaseGeneration, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionexec.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read session command lease: %w", err)
	}
	if state != sessionexec.StateRunning || generation != ref.Generation ||
		!owner.Valid || owner.String != ref.Owner || leaseGeneration != ref.LeaseGeneration || !expires.Valid {
		return sessionexec.ErrLeaseStale
	}
	if requireUnexpired && expires.Int64 <= now {
		return sessionexec.ErrLeaseExpired
	}
	return nil
}

type sessionExecTranscriptMapping struct {
	entry     sessionexec.TranscriptEntry
	payload   string
	digest    string
	messageID sql.NullInt64
}

func sessionExecReadTranscriptMapping(db *sessionExecConn, identity sessionexec.Identity, ordinal int) (sessionExecTranscriptMapping, bool, error) {
	var mapping sessionExecTranscriptMapping
	var payloadBytes, digestBytes int64
	err := db.queryRow(`SELECT length(CAST(entry_json AS BLOB)), length(CAST(entry_digest AS BLOB)), message_id
		FROM session_command_transcript
		WHERE session_id = ? AND command_id = ? AND generation = ? AND ordinal = ?`,
		identity.SessionID, identity.CommandID, identity.Generation, ordinal).Scan(
		&payloadBytes, &digestBytes, &mapping.messageID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionExecTranscriptMapping{}, false, nil
	}
	if err != nil {
		return sessionExecTranscriptMapping{}, false, fmt.Errorf("read transcript mapping: %w", err)
	}
	if payloadBytes < 1 || payloadBytes > sessionexec.MaxTranscriptEntryJSONBytes || digestBytes != 64 {
		return sessionExecTranscriptMapping{}, false, sessionexec.ErrTranscriptConflict
	}
	if err := db.queryRow(`SELECT entry_json, entry_digest FROM session_command_transcript
		WHERE session_id = ? AND command_id = ? AND generation = ? AND ordinal = ?`,
		identity.SessionID, identity.CommandID, identity.Generation, ordinal).Scan(
		&mapping.payload, &mapping.digest,
	); err != nil {
		return sessionExecTranscriptMapping{}, false, fmt.Errorf("load transcript mapping payload: %w", err)
	}
	entry, digest, err := sessionexec.DecodeTranscriptEntryPayload(mapping.payload)
	if err != nil || entry.Ordinal != ordinal || digest != mapping.digest {
		return sessionExecTranscriptMapping{}, false, sessionexec.ErrTranscriptConflict
	}
	mapping.entry = entry
	if mapping.messageID.Valid {
		actual, err := sessionExecReadMappedMessage(db, mapping.messageID.Int64, ordinal)
		if err != nil {
			return sessionExecTranscriptMapping{}, false, err
		}
		_, actualPayload, actualDigest, err := sessionexec.TranscriptEntryPayload(actual)
		if err != nil || actualPayload != mapping.payload || actualDigest != mapping.digest {
			return sessionExecTranscriptMapping{}, false, sessionexec.ErrTranscriptConflict
		}
	}
	return mapping, true, nil
}

func sessionExecReadMappedMessage(db *sessionExecConn, messageID int64, ordinal int) (sessionexec.TranscriptEntry, error) {
	var entry sessionexec.TranscriptEntry
	var contentJSON, toolCalls, toolCallID, name, reasoning, reasoningDetails sql.NullString
	var roleBytes, contentBytes, contentJSONBytes, contentTypeBytes int64
	var toolCallsBytes, toolCallIDBytes, nameBytes, reasoningBytes, reasoningDetailsBytes int64
	err := db.queryRow(`SELECT
		length(CAST(role AS BLOB)), length(CAST(content AS BLOB)),
		COALESCE(length(CAST(content_json AS BLOB)), 0), length(CAST(content_type AS BLOB)),
		COALESCE(length(CAST(tool_calls AS BLOB)), 0), COALESCE(length(CAST(tool_call_id AS BLOB)), 0),
		COALESCE(length(CAST(name AS BLOB)), 0), COALESCE(length(CAST(reasoning AS BLOB)), 0),
		COALESCE(length(CAST(reasoning_details AS BLOB)), 0)
		FROM messages WHERE id = ?`, messageID).Scan(
		&roleBytes, &contentBytes, &contentJSONBytes, &contentTypeBytes, &toolCallsBytes,
		&toolCallIDBytes, &nameBytes, &reasoningBytes, &reasoningDetailsBytes,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionexec.TranscriptEntry{}, sessionexec.ErrTranscriptConflict
	}
	if err != nil {
		return sessionexec.TranscriptEntry{}, fmt.Errorf("read mapped transcript message bounds: %w", err)
	}
	if roleBytes < 1 || roleBytes > 16 || contentTypeBytes < 1 || contentTypeBytes > sessionexec.MaxCommandTypeBytes ||
		contentBytes > sessionexec.MaxTranscriptEntryBytes || contentJSONBytes > sessionexec.MaxTranscriptEntryBytes ||
		toolCallsBytes > sessionexec.MaxTranscriptEntryBytes || reasoningBytes > sessionexec.MaxTranscriptEntryBytes ||
		reasoningDetailsBytes > sessionexec.MaxTranscriptEntryBytes || toolCallIDBytes > sessionexec.MaxReferenceIDBytes ||
		nameBytes > sessionexec.MaxReferenceIDBytes ||
		contentBytes+contentJSONBytes+toolCallsBytes+reasoningBytes+reasoningDetailsBytes > sessionexec.MaxTranscriptTotalBytes {
		return sessionexec.TranscriptEntry{}, sessionexec.ErrTranscriptConflict
	}
	err = db.queryRow(`SELECT role, content, content_json, content_type, tool_calls,
		tool_call_id, name, reasoning, reasoning_details, tokens, is_summary, is_truncated
		FROM messages WHERE id = ?`, messageID).Scan(
		&entry.Role, &entry.Content, &contentJSON, &entry.ContentType, &toolCalls,
		&toolCallID, &name, &reasoning, &reasoningDetails, &entry.Tokens,
		&entry.IsSummary, &entry.IsTruncated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return sessionexec.TranscriptEntry{}, sessionexec.ErrTranscriptConflict
	}
	if err != nil {
		return sessionexec.TranscriptEntry{}, fmt.Errorf("read mapped transcript message: %w", err)
	}
	entry.Ordinal = ordinal
	entry.ContentJSON = contentJSON.String
	entry.ToolCalls = toolCalls.String
	entry.ToolCallID = toolCallID.String
	entry.Name = name.String
	entry.Reasoning = reasoning.String
	entry.ReasoningDetails = reasoningDetails.String
	return entry, nil
}

func sessionExecAppendTranscript(db *sessionExecConn, identity sessionexec.Identity, entry sessionexec.TranscriptEntry, now int64) (bool, error) {
	entry, payload, digest, err := sessionexec.TranscriptEntryPayload(entry)
	if err != nil {
		return false, err
	}
	mapping, exists, err := sessionExecReadTranscriptMapping(db, identity, entry.Ordinal)
	if err != nil {
		return false, err
	}
	if exists {
		if mapping.payload != payload || mapping.digest != digest {
			return false, sessionexec.ErrTranscriptConflict
		}
		return false, nil
	}
	result, err := db.exec(`INSERT INTO messages (
		session_id, role, content, content_json, content_type, tool_calls,
		tool_call_id, name, reasoning, reasoning_details, timestamp, tokens,
		is_summary, is_truncated
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		identity.SessionID, entry.Role, entry.Content, nullIfEmpty(entry.ContentJSON),
		entry.ContentType, nullIfEmpty(entry.ToolCalls), nullIfEmpty(entry.ToolCallID),
		nullIfEmpty(entry.Name), nullIfEmpty(entry.Reasoning), nullIfEmpty(entry.ReasoningDetails),
		sqliteTimestamp(sessionExecTime(now)), entry.Tokens, entry.IsSummary, entry.IsTruncated)
	if err != nil {
		return false, fmt.Errorf("insert command transcript message: %w", err)
	}
	messageID, err := result.LastInsertId()
	if err != nil {
		return false, fmt.Errorf("read command transcript message id: %w", err)
	}
	if _, err := db.exec(`INSERT INTO session_command_transcript (
		session_id, command_id, generation, ordinal, message_id, entry_json, entry_digest
	) VALUES (?, ?, ?, ?, ?, ?, ?)`, identity.SessionID, identity.CommandID,
		identity.Generation, entry.Ordinal, messageID, payload, digest); err != nil {
		return false, fmt.Errorf("insert command transcript mapping: %w", err)
	}
	return true, nil
}

func sessionExecValidateMappings(db *sessionExecConn, identity sessionexec.Identity, expected []sessionexec.TranscriptEntry) error {
	rows, err := db.query(`SELECT ordinal FROM session_command_transcript
		WHERE session_id = ? AND command_id = ? AND generation = ? ORDER BY ordinal ASC LIMIT ?`,
		identity.SessionID, identity.CommandID, identity.Generation, len(expected)+1)
	if err != nil {
		return fmt.Errorf("list transcript mappings: %w", err)
	}
	ordinals := make([]int, 0, len(expected)+1)
	for rows.Next() {
		var ordinal int
		if err := rows.Scan(&ordinal); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan transcript mapping ordinal: %w", err)
		}
		ordinals = append(ordinals, ordinal)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate transcript mapping ordinals: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close transcript mapping ordinals: %w", err)
	}
	if len(ordinals) != len(expected) {
		return sessionexec.ErrTranscriptConflict
	}
	for index, entry := range expected {
		if ordinals[index] != entry.Ordinal {
			return sessionexec.ErrTranscriptConflict
		}
		mapping, exists, err := sessionExecReadTranscriptMapping(db, identity, entry.Ordinal)
		if err != nil {
			return err
		}
		_, payload, digest, err := sessionexec.TranscriptEntryPayload(entry)
		if err != nil {
			return err
		}
		if !exists || mapping.payload != payload || mapping.digest != digest {
			return sessionexec.ErrTranscriptConflict
		}
	}
	return nil
}

func sessionExecUpdateMessageStats(db *sessionExecConn, sessionID string, count int64, tokens int64, now int64) error {
	if count < 0 || tokens < 0 || count > sessionexec.MaxSessionMessageCount || tokens > sessionexec.MaxCompletionTokens {
		return fmt.Errorf("%w: transcript statistic delta out of range", sessionexec.ErrValidation)
	}
	var currentCount, currentTokens int64
	if err := db.queryRow(`SELECT message_count, total_tokens FROM sessions WHERE session_id = ?`, sessionID).
		Scan(&currentCount, &currentTokens); errors.Is(err, sql.ErrNoRows) {
		return sessionexec.ErrNotFound
	} else if err != nil {
		return fmt.Errorf("read command transcript stats: %w", err)
	}
	if currentCount < 0 || currentCount > sessionexec.MaxSessionMessageCount ||
		currentTokens < 0 || currentTokens > sessionexec.MaxSessionTotalTokens ||
		count > sessionexec.MaxSessionMessageCount-currentCount ||
		tokens > sessionexec.MaxSessionTotalTokens-currentTokens {
		return fmt.Errorf("%w: session transcript statistics out of range", sessionexec.ErrValidation)
	}
	nextCount := currentCount + count
	nextTokens := currentTokens + tokens
	result, err := db.exec(`UPDATE sessions SET
		message_count = ?, total_tokens = ?, last_active = ?
		WHERE session_id = ? AND message_count = ? AND total_tokens = ?`,
		nextCount, nextTokens, sqliteTimestamp(sessionExecTime(now)), sessionID, currentCount, currentTokens)
	if err != nil {
		return fmt.Errorf("update command transcript stats: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read command transcript stats result: %w", err)
	}
	if changed != 1 {
		return sessionexec.ErrNotFound
	}
	return nil
}

func (s *Store) Complete(ctx context.Context, ref sessionexec.LeaseRef, completion sessionexec.Completion, entries []sessionexec.TranscriptEntry) (sessionexec.Receipt, error) {
	if err := sessionexec.ValidateLeaseRef(ref); err != nil {
		return sessionexec.Receipt{}, err
	}
	completion, err := sanitizeSessionExecCompletion(completion)
	if err != nil {
		return sessionexec.Receipt{}, err
	}
	preflightOrdinal := 0
	if len(entries) > 0 {
		preflightOrdinal = entries[0].Ordinal
	}
	entries, err = sessionexec.ValidateTranscriptEntries(entries, preflightOrdinal)
	if err != nil {
		return sessionexec.Receipt{}, err
	}
	var receipt sessionexec.Receipt
	var committedErr error
	err = s.withSessionExecWrite(ctx, func(db *sessionExecConn) error {
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		if err := sessionExecMaterializeExpiredEffects(db, ref.SessionID, now); err != nil {
			return err
		}
		if err := sessionExecRequireNoSessionAmbiguousEffects(db, ref.SessionID); err != nil {
			if errors.Is(err, sessionexec.ErrEffectAmbiguous) {
				committedErr = err
				return nil
			}
			return err
		}
		stored, err := sessionExecLoadStoredCommand(db, ref.SessionID, ref.CommandID)
		if err != nil {
			return err
		}
		if err := sessionExecValidateStoredAcceptance(stored); err != nil {
			return err
		}
		if err := sessionExecRequireNoBlockingEffectPermits(db, ref); err != nil {
			return err
		}
		if stored.state.Terminal() {
			nextOrdinal := 0
			if len(entries) > 0 {
				nextOrdinal = entries[0].Ordinal
			}
			_, _, digest, digestErr := sessionexec.CompletionDigest(completion, entries, nextOrdinal)
			if digestErr != nil {
				return digestErr
			}
			if stored.completionDigest.Valid && stored.completionDigest.String == digest &&
				stored.completedBy.Valid && stored.completedBy.String == ref.Owner &&
				stored.completedLeaseGeneration.Valid && stored.completedLeaseGeneration.Int64 == ref.LeaseGeneration &&
				stored.command.Generation == ref.Generation {
				expected := append(sessionExecClaimTranscript(stored), entries...)
				if err := sessionExecValidateMappings(db, stored.command.Identity, expected); err != nil {
					return err
				}
				if err := sessionExecCleanupFinishedEffects(db, ref); err != nil {
					return err
				}
				receipt, err = sessionExecReadReceipt(db, ref.SessionID, ref.CommandID)
				if err == nil {
					receipt.Duplicate = true
				}
				return err
			}
			return sessionexec.ErrTerminalConflict
		}
		if stored.state != sessionexec.StateRunning || stored.command.Generation != ref.Generation ||
			!stored.leaseOwner.Valid || stored.leaseOwner.String != ref.Owner ||
			stored.leaseGeneration != ref.LeaseGeneration || !stored.leaseExpires.Valid {
			return sessionexec.ErrLeaseStale
		}
		if stored.leaseExpires.Int64 <= now {
			return sessionexec.ErrLeaseExpired
		}
		prefix := sessionExecClaimTranscript(stored)
		if err := sessionExecValidateMappings(db, stored.command.Identity, prefix); err != nil {
			return err
		}
		nextOrdinal := len(prefix)
		completion, entries, digest, err := sessionexec.CompletionDigest(completion, entries, nextOrdinal)
		if err != nil {
			return err
		}
		var newMessages int64
		var newTokens int64
		for _, entry := range entries {
			inserted, err := sessionExecAppendTranscript(db, stored.command.Identity, entry, now)
			if err != nil {
				return err
			}
			if inserted {
				newMessages++
				newTokens += entry.Tokens
			}
		}
		if newMessages > 0 {
			if err := sessionExecUpdateMessageStats(db, ref.SessionID, newMessages, newTokens, now); err != nil {
				return err
			}
		}
		outcome, err := marshalSessionExecOutcome(completion.Outcome)
		if err != nil {
			return err
		}
		result, err := db.exec(`UPDATE session_commands SET
			state = ?, completed_at_ms = ?, error_code = ?, error_text = ?,
			outcome_json = ?, completion_digest = ?, completed_by = ?,
			completion_lease_generation = ?, lease_owner = NULL,
			lease_expires_at_ms = NULL, heartbeat_at_ms = NULL
			WHERE session_id = ? AND command_id = ? AND generation = ? AND state = ?
				AND lease_owner = ? AND lease_generation = ? AND lease_expires_at_ms > ?`,
			completion.State, now, nullIfEmpty(completion.ErrorCode), nullIfEmpty(completion.Error),
			outcome, digest, ref.Owner, ref.LeaseGeneration,
			ref.SessionID, ref.CommandID, ref.Generation, sessionexec.StateRunning,
			ref.Owner, ref.LeaseGeneration, now)
		if err != nil {
			return fmt.Errorf("complete session command: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read command completion result: %w", err)
		}
		if changed != 1 {
			return sessionexec.ErrLeaseStale
		}
		if err := sessionExecCleanupFinishedEffects(db, ref); err != nil {
			return err
		}
		receipt, err = sessionExecReadReceipt(db, ref.SessionID, ref.CommandID)
		return err
	})
	if err != nil {
		return receipt, err
	}
	return receipt, committedErr
}

func (s *Store) CancelPending(ctx context.Context, sessionID, reasonCode string) (int, error) {
	if err := sessionexec.ValidateSessionID(sessionID); err != nil {
		return 0, err
	}
	if reasonCode == "" {
		reasonCode = "cancelled"
	}
	if err := sessionexec.ValidateErrorCode(reasonCode); err != nil {
		return 0, err
	}
	count := 0
	err := s.withSessionExecWrite(ctx, func(db *sessionExecConn) error {
		count = 0
		if err := sessionExecSessionExists(db, sessionID); err != nil {
			return err
		}
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		rows, err := db.query(`SELECT command_id FROM session_commands
			WHERE session_id = ? AND state = ? ORDER BY sequence ASC, command_id ASC LIMIT ?`,
			sessionID, sessionexec.StateAccepted, sessionexec.MaxCancelBatch)
		if err != nil {
			return fmt.Errorf("select pending commands for cancellation: %w", err)
		}
		commandIDs := make([]string, 0, sessionexec.MaxCancelBatch)
		for rows.Next() {
			var commandID string
			if err := rows.Scan(&commandID); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan pending command for cancellation: %w", err)
			}
			commandIDs = append(commandIDs, commandID)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate pending commands for cancellation: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close pending command rows: %w", err)
		}
		completion := sessionexec.Completion{State: sessionexec.StateCancelled, ErrorCode: reasonCode}
		_, _, digest, err := sessionexec.CompletionDigest(completion, nil, 0)
		if err != nil {
			return err
		}
		outcome, err := marshalSessionExecOutcome(sessionexec.Outcome{})
		if err != nil {
			return err
		}
		for _, commandID := range commandIDs {
			result, err := db.exec(`UPDATE session_commands SET
				state = ?, completed_at_ms = ?, error_code = ?, error_text = NULL,
				outcome_json = ?, completion_digest = ?, completed_by = NULL,
				completion_lease_generation = NULL
				WHERE session_id = ? AND command_id = ? AND state = ?`,
				sessionexec.StateCancelled, now, reasonCode, outcome, digest,
				sessionID, commandID, sessionexec.StateAccepted)
			if err != nil {
				return fmt.Errorf("cancel pending session command: %w", err)
			}
			changed, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("read pending cancellation result: %w", err)
			}
			count += int(changed)
		}
		return nil
	})
	return count, err
}

func marshalSessionExecOutcome(outcome sessionexec.Outcome) (string, error) {
	encoded, err := json.Marshal(outcome)
	if err != nil {
		return "", fmt.Errorf("encode command outcome: %w", err)
	}
	if len(encoded) > sessionexec.MaxOutcomeJSONBytes {
		return "", fmt.Errorf("%w: encoded outcome too large", sessionexec.ErrValidation)
	}
	return string(encoded), nil
}

func sanitizeSessionExecCompletion(value sessionexec.Completion) (sessionexec.Completion, error) {
	canonical, err := sessionexec.NormalizeCompletion(value)
	if err != nil {
		return sessionexec.Completion{}, err
	}
	canonical.Error = telemetry.SanitizeText(canonical.Error, sessionexec.MaxErrorTextBytes)
	return sessionexec.NormalizeCompletion(canonical)
}
