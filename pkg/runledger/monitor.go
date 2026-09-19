package runledger

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/agentcoord"
	"m31labs.dev/buckley/pkg/storage"
)

const (
	MonitorMaxExpiredMailboxClaims = 1024
	monitorBusySlice               = 25 * time.Millisecond
	monitorRetryLimit              = 8
)

var ErrMonitorUnavailable = errors.New("runledger: routine monitor is unavailable")

type monitorConn struct {
	ctx  context.Context
	conn *sql.Conn
}

func (c *monitorConn) exec(query string, args ...any) (sql.Result, error) {
	return c.conn.ExecContext(c.ctx, query, args...)
}

func (c *monitorConn) query(query string, args ...any) (*sql.Rows, error) {
	return c.conn.QueryContext(c.ctx, query, args...)
}

func (c *monitorConn) queryRow(query string, args ...any) *sql.Row {
	return c.conn.QueryRowContext(c.ctx, query, args...)
}

type monitorRun struct {
	status         agentcoord.RoutineStatus
	canonicalState agentcoord.RunState
	launchEvidence bool
	contractDigest string
	contractProof  string
}

func (s *SQLiteStore) GetRoutineStatus(ctx context.Context, sessionID, runID string) (agentcoord.RoutineStatus, error) {
	if err := agentcoord.ValidateMonitorIdentity(sessionID, runID); err != nil {
		return agentcoord.RoutineStatus{}, err
	}
	if s == nil || s.db == nil {
		return agentcoord.RoutineStatus{}, fmt.Errorf("%w", ErrMonitorUnavailable)
	}
	var (
		selected monitorRun
		observed time.Time
		result   agentcoord.RoutineStatus
	)
	err := s.withMonitorWriteTransaction(ctx, func(db *monitorConn, now time.Time) error {
		var err error
		selected, err = monitorReadOneRun(db, sessionID, runID)
		if err != nil {
			return err
		}
		observed = now
		return monitorMaterializeOperationalState(db, now, sessionID, []string{runID})
	})
	if err != nil {
		return agentcoord.RoutineStatus{}, err
	}
	err = s.withMonitorReadTransaction(ctx, func(db *monitorConn) error {
		run, err := monitorRevalidateRun(db, selected)
		if err != nil {
			return err
		}
		statuses, err := monitorProjectRoutines(db, observed, []monitorRun{run})
		if err != nil {
			return err
		}
		result = statuses[0]
		return nil
	})
	if err != nil {
		return agentcoord.RoutineStatus{}, err
	}
	return result, nil
}

func (s *SQLiteStore) ListRoutineStatuses(ctx context.Context, query agentcoord.RoutineQuery) (agentcoord.RoutineStatusPage, error) {
	return s.listRoutineStatuses(ctx, query, nil)
}

func (s *SQLiteStore) listRoutineStatuses(
	ctx context.Context,
	query agentcoord.RoutineQuery,
	beforeSnapshot func(),
) (agentcoord.RoutineStatusPage, error) {
	query, err := agentcoord.NormalizeRoutineQuery(query)
	if err != nil {
		return agentcoord.RoutineStatusPage{}, err
	}
	if s == nil || s.db == nil {
		return agentcoord.RoutineStatusPage{}, fmt.Errorf("%w", ErrMonitorUnavailable)
	}
	var (
		selected []monitorRun
		observed time.Time
		page     = agentcoord.RoutineStatusPage{Routines: make([]agentcoord.RoutineStatus, 0)}
	)
	err = s.withMonitorWriteTransaction(ctx, func(db *monitorConn, now time.Time) error {
		if query.ParentRunID != "" {
			if err := monitorValidateOwnedRun(db, query.SessionID, query.ParentRunID); err != nil {
				return err
			}
		}
		var err error
		selected, err = monitorListRuns(db, query)
		if err != nil {
			return err
		}
		if len(selected) > query.Limit {
			page.HasMore = true
			selected = selected[:query.Limit]
		}
		if len(selected) == 0 {
			return nil
		}
		observed = now
		ids := monitorRunIDs(selected)
		return monitorMaterializeOperationalState(db, now, query.SessionID, ids)
	})
	if err != nil {
		return agentcoord.RoutineStatusPage{}, err
	}
	if len(selected) == 0 {
		return page, nil
	}
	if beforeSnapshot != nil {
		beforeSnapshot()
	}
	err = s.withMonitorReadTransaction(ctx, func(db *monitorConn) error {
		runs, err := monitorRevalidateRuns(db, selected)
		if err != nil {
			return err
		}
		page.Routines, err = monitorProjectRoutines(db, observed, runs)
		if err != nil {
			return err
		}
		if page.HasMore {
			last := page.Routines[len(page.Routines)-1]
			page.Next, err = agentcoord.EncodeRoutineCursor(last.StartedAt, last.RunID)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return agentcoord.RoutineStatusPage{}, err
	}
	return page, nil
}

func (s *SQLiteStore) ListMailboxStatuses(ctx context.Context, query agentcoord.MailboxStatusQuery) (agentcoord.MailboxStatusPage, error) {
	return s.listMailboxStatuses(ctx, query, nil)
}

func (s *SQLiteStore) listMailboxStatuses(
	ctx context.Context,
	query agentcoord.MailboxStatusQuery,
	afterSnapshot func(),
) (agentcoord.MailboxStatusPage, error) {
	query, err := agentcoord.NormalizeMailboxStatusQuery(query)
	if err != nil {
		return agentcoord.MailboxStatusPage{}, err
	}
	if s == nil || s.db == nil {
		return agentcoord.MailboxStatusPage{}, fmt.Errorf("%w", ErrMonitorUnavailable)
	}
	var (
		selected monitorRun
		observed time.Time
		result   agentcoord.MailboxStatusPage
	)
	err = s.withMonitorWriteTransaction(ctx, func(db *monitorConn, now time.Time) error {
		var err error
		selected, err = monitorReadOneRun(db, query.SessionID, query.RunID)
		if err != nil {
			return err
		}
		observed = now
		if err := monitorMaterializeOperationalState(db, now, query.SessionID, []string{query.RunID}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return agentcoord.MailboxStatusPage{}, err
	}
	err = s.withMonitorReadTransaction(ctx, func(db *monitorConn) error {
		if _, err := monitorRevalidateRun(db, selected); err != nil {
			return err
		}
		if afterSnapshot != nil {
			afterSnapshot()
		}
		if _, err := monitorMailboxSummaries(db, observed, query.SessionID, []string{query.RunID}); err != nil {
			return err
		}
		page, err := monitorMailboxPage(db, query)
		if err != nil {
			return err
		}
		result = page
		return nil
	})
	if err != nil {
		return agentcoord.MailboxStatusPage{}, err
	}
	return result, nil
}

func (s *SQLiteStore) withMonitorWriteTransaction(ctx context.Context, fn func(*monitorConn, time.Time) error) error {
	return s.withMonitorTransaction(ctx, `BEGIN IMMEDIATE`, true, fn)
}

func (s *SQLiteStore) withMonitorReadTransaction(ctx context.Context, fn func(*monitorConn) error) error {
	return s.withMonitorTransaction(ctx, `BEGIN`, false, func(db *monitorConn, _ time.Time) error {
		return fn(db)
	})
}

func (s *SQLiteStore) withMonitorTransaction(
	ctx context.Context,
	beginStatement string,
	observeTime bool,
	fn func(*monitorConn, time.Time) error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	delay := 2 * time.Millisecond
	var lastErr error
	for attempt := 0; attempt < monitorRetryLimit; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		conn, err := s.db.Conn(ctx)
		if err != nil {
			return fmt.Errorf("runledger: acquire monitor connection: %w", err)
		}
		var originalBusy int
		if err = conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&originalBusy); err != nil {
			_ = conn.Close()
			return fmt.Errorf("runledger: read monitor busy timeout: %w", err)
		}
		busyWait := monitorBusySlice
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				_ = conn.Close()
				return context.DeadlineExceeded
			}
			if remaining < busyWait {
				busyWait = remaining
			}
		}
		busyMillis := busyWait.Milliseconds()
		if busyMillis < 1 {
			busyMillis = 1
		}
		began := false
		if _, err = conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA busy_timeout = %d`, busyMillis)); err == nil {
			_, err = conn.ExecContext(ctx, beginStatement)
			began = err == nil
		}
		if err == nil {
			db := &monitorConn{ctx: ctx, conn: conn}
			var now time.Time
			if observeTime {
				now, err = monitorSQLiteTime(db)
			}
			if err == nil {
				err = fn(db, now)
			}
			if err == nil {
				_, err = conn.ExecContext(ctx, `COMMIT`)
			}
		}
		discard := false
		if err != nil && began {
			if _, rollbackErr := conn.ExecContext(context.Background(), `ROLLBACK`); rollbackErr != nil {
				discard = true
			}
		}
		if _, restoreErr := conn.ExecContext(context.Background(), fmt.Sprintf(`PRAGMA busy_timeout = %d`, originalBusy)); restoreErr != nil {
			discard = true
			if err == nil {
				err = fmt.Errorf("runledger: restore monitor busy timeout: %w", restoreErr)
			}
		}
		if discard {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
		if err == nil {
			return nil
		}
		lastErr = err
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if !storage.IsSQLiteBusyError(err) {
			return err
		}
		if attempt == monitorRetryLimit-1 {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < 50*time.Millisecond {
			delay *= 2
		}
	}
	return fmt.Errorf("runledger: monitor remained busy: %w", lastErr)
}

func monitorSQLiteTime(db *monitorConn) (time.Time, error) {
	var raw string
	if err := db.queryRow(`SELECT strftime('%Y-%m-%dT%H:%M:%fZ', 'now')`).Scan(&raw); err != nil {
		return time.Time{}, fmt.Errorf("runledger: read monitor sqlite time: %w", err)
	}
	value, err := monitorParseTimestamp(raw)
	if err != nil {
		return time.Time{}, err
	}
	return value, nil
}

const monitorRunSelectColumns = `r.session_id, r.run_id, r.parent_run_id, r.task_id,
	r.agent_id, r.model_id, r.provider_id, r.backend, r.status, r.started_at, r.ended_at,
	contract.input_digest, contract.task_evidence_id,
	EXISTS (SELECT 1 FROM run_events event WHERE event.run_id = r.run_id AND event.event_type = 'subagent.spawned'),
	CASE WHEN r.parent_run_id IS NULL THEN 1 ELSE EXISTS (
		SELECT 1 FROM agent_runs parent WHERE parent.run_id = r.parent_run_id
			AND parent.session_id = r.session_id AND parent.run_id <> r.run_id
	) END`

const monitorRunContractJoin = `JOIN agent_run_contracts contract
	ON contract.run_id = r.run_id AND contract.session_id = r.session_id`

func monitorReadOneRun(db *monitorConn, sessionID, runID string) (monitorRun, error) {
	row := db.queryRow(`SELECT `+monitorRunSelectColumns+`
		FROM agent_runs r `+monitorRunContractJoin+`
		WHERE r.session_id = ? AND r.run_id = ?`, sessionID, runID)
	run, err := monitorScanRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return monitorRun{}, ErrNotFound
	}
	if err != nil {
		return monitorRun{}, err
	}
	return run, nil
}

func monitorListRuns(db *monitorConn, query agentcoord.RoutineQuery) ([]monitorRun, error) {
	clauses := []string{"r.session_id = ?"}
	args := []any{query.SessionID}
	if query.ParentRunID != "" {
		clauses = append(clauses, "r.parent_run_id = ?")
		args = append(args, query.ParentRunID)
	}
	if query.Before != "" {
		startedAt, runID, err := agentcoord.DecodeRoutineCursor(query.Before)
		if err != nil {
			return nil, err
		}
		epoch := startedAt.Unix()
		fraction := float64(startedAt.Nanosecond()) / float64(time.Second)
		clauses = append(clauses, `(`+runStartedAtEpochKey+` < ? OR (`+runStartedAtEpochKey+` = ? AND `+runStartedAtFractionKey+` < ?)
			OR (`+runStartedAtEpochKey+` = ? AND `+runStartedAtFractionKey+` = ? AND r.run_id < ?))`)
		args = append(args, epoch, epoch, fraction, epoch, fraction, runID)
	}
	args = append(args, query.Limit+1)
	rows, err := db.query(`SELECT `+monitorRunSelectColumns+`
		FROM agent_runs r `+monitorRunContractJoin+` WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY `+runStartedAtEpochKey+` DESC, `+runStartedAtFractionKey+` DESC, r.run_id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("runledger: list monitored routines: %w", err)
	}
	defer rows.Close()
	result := make([]monitorRun, 0, query.Limit+1)
	for rows.Next() {
		run, err := monitorScanRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("runledger: iterate monitored routines: %w", err)
	}
	return result, nil
}

func monitorScanRun(scanner rowScanner) (monitorRun, error) {
	var (
		run                                           monitorRun
		parent, task, agent, model, provider, backend sql.NullString
		startedRaw                                    string
		endedRaw                                      sql.NullString
		launchEvidence, parentValid                   bool
		canonical                                     string
	)
	if err := scanner.Scan(
		&run.status.SessionID, &run.status.RunID, &parent, &task, &agent, &model,
		&provider, &backend, &canonical, &startedRaw, &endedRaw,
		&run.contractDigest, &run.contractProof, &launchEvidence, &parentValid,
	); err != nil {
		return monitorRun{}, err
	}
	for name, value := range map[string]sql.NullString{
		"parent run": parent, "task": task, "agent": agent,
		"model": model, "provider": provider, "backend": backend,
	} {
		if value.Valid && value.String == "" {
			return monitorRun{}, monitorIntegrity(name + " identity")
		}
	}
	if !parentValid {
		return monitorRun{}, monitorIntegrity("routine parent relationship")
	}
	if run.contractDigest == "" || len(run.contractDigest) > RunContractMaxDigest ||
		run.contractProof == "" || len(run.contractProof) > RunContractMaxEvidenceID {
		return monitorRun{}, monitorIntegrity("routine contract identity")
	}
	startedAt, err := monitorParseTimestamp(startedRaw)
	if err != nil {
		return monitorRun{}, err
	}
	run.status.ParentRunID = parent.String
	run.status.TaskID = task.String
	run.status.AgentID = agent.String
	run.status.ModelID = model.String
	run.status.ProviderID = provider.String
	run.status.Backend = backend.String
	run.status.StartedAt = startedAt
	run.launchEvidence = launchEvidence
	run.canonicalState = agentcoord.RunState(canonical)
	switch run.canonicalState {
	case agentcoord.RunQueued, agentcoord.RunRunning:
		if endedRaw.Valid {
			return monitorRun{}, monitorIntegrity("nonterminal routine timestamp")
		}
	case agentcoord.RunCompleted, agentcoord.RunFailed, agentcoord.RunCancelled, agentcoord.RunBlocked:
		if !endedRaw.Valid || endedRaw.String == "" {
			return monitorRun{}, monitorIntegrity("terminal routine timestamp")
		}
		endedAt, err := monitorParseTimestamp(endedRaw.String)
		if err != nil || endedAt.Before(startedAt) {
			return monitorRun{}, monitorIntegrity("terminal routine chronology")
		}
		run.status.FinishedAt = &endedAt
	default:
		return monitorRun{}, monitorIntegrity("routine state")
	}
	projection := run.status
	projection.State = run.canonicalState
	projection.Attempt = agentcoord.AttemptStatus{State: agentcoord.AttemptNone}
	if err := agentcoord.ValidateRoutineStatus(projection); err != nil {
		return monitorRun{}, err
	}
	return run, nil
}

func monitorValidateOwnedRun(db *monitorConn, sessionID, runID string) error {
	var found int
	if err := db.queryRow(`SELECT COUNT(*) FROM agent_runs WHERE session_id = ? AND run_id = ?`, sessionID, runID).Scan(&found); err != nil {
		return fmt.Errorf("runledger: validate monitored routine: %w", err)
	}
	if found != 1 {
		return ErrNotFound
	}
	return nil
}

func monitorProjectRoutines(db *monitorConn, now time.Time, runs []monitorRun) ([]agentcoord.RoutineStatus, error) {
	ids := monitorRunIDs(runs)
	attempts, err := monitorAttemptStatuses(db, now, runs[0].status.SessionID, ids)
	if err != nil {
		return nil, err
	}
	mailboxes, err := monitorMailboxSummaries(db, now, runs[0].status.SessionID, ids)
	if err != nil {
		return nil, err
	}
	statuses := make([]agentcoord.RoutineStatus, 0, len(runs))
	for _, run := range runs {
		status := run.status
		status.Attempt = attempts[status.RunID]
		status.Mailbox = mailboxes[status.RunID]
		if run.canonicalState.Terminal() {
			if status.Attempt.State == agentcoord.AttemptAttached {
				return nil, monitorIntegrity("terminal routine has an attached attempt")
			}
			status.State = run.canonicalState
		} else {
			switch {
			case status.Attempt.State == agentcoord.AttemptAttached:
				status.State = agentcoord.RunRunning
			case status.Attempt.Number > 0 || run.launchEvidence:
				status.State = agentcoord.RunResumable
			case run.canonicalState == agentcoord.RunQueued:
				status.State = agentcoord.RunQueued
			default:
				return nil, monitorIntegrity("running routine lacks launch ownership evidence")
			}
		}
		if err := agentcoord.ValidateRoutineStatus(status); err != nil {
			return nil, err
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func monitorRunIDs(runs []monitorRun) []string {
	ids := make([]string, len(runs))
	for index := range runs {
		ids[index] = runs[index].status.RunID
	}
	return ids
}

func monitorRevalidateRuns(db *monitorConn, selected []monitorRun) ([]monitorRun, error) {
	result := make([]monitorRun, 0, len(selected))
	for _, expected := range selected {
		actual, err := monitorReadOneRun(db, expected.status.SessionID, expected.status.RunID)
		if errors.Is(err, ErrNotFound) {
			return nil, agentcoord.ErrMonitorConflict
		}
		if err != nil {
			return nil, err
		}
		if !monitorSameRun(expected, actual) {
			return nil, agentcoord.ErrMonitorConflict
		}
		result = append(result, actual)
	}
	return result, nil
}

func monitorRevalidateRun(db *monitorConn, selected monitorRun) (monitorRun, error) {
	runs, err := monitorRevalidateRuns(db, []monitorRun{selected})
	if err != nil {
		return monitorRun{}, err
	}
	return runs[0], nil
}

func monitorSameRun(left, right monitorRun) bool {
	return left.status.SessionID == right.status.SessionID &&
		left.status.RunID == right.status.RunID &&
		left.status.ParentRunID == right.status.ParentRunID &&
		left.status.TaskID == right.status.TaskID &&
		left.status.AgentID == right.status.AgentID &&
		left.status.ModelID == right.status.ModelID &&
		left.status.ProviderID == right.status.ProviderID &&
		left.status.Backend == right.status.Backend &&
		left.status.StartedAt.Equal(right.status.StartedAt) &&
		left.contractDigest == right.contractDigest &&
		left.contractProof == right.contractProof
}

func monitorMaterializeOperationalState(db *monitorConn, now time.Time, sessionID string, runIDs []string) error {
	if len(runIDs) == 0 || len(runIDs) > agentcoord.MaxRoutineStatusLimit {
		return monitorIntegrity("routine materialization selection")
	}
	placeholders := monitorPlaceholders(len(runIDs))
	baseArgs := make([]any, 0, len(runIDs)+2)
	baseArgs = append(baseArgs, sessionID)
	for _, runID := range runIDs {
		baseArgs = append(baseArgs, runID)
	}
	attachmentArgs := append(append([]any(nil), baseArgs...), agentcoord.AttachmentAttached, len(runIDs)+1)
	attachmentRows, err := db.query(`SELECT run_id, lease_expires_at FROM agent_run_attempts
		WHERE session_id = ? AND run_id IN (`+placeholders+`) AND state = ?
		ORDER BY run_id ASC, lease_generation DESC LIMIT ?`, attachmentArgs...)
	if err != nil {
		return fmt.Errorf("runledger: preflight monitored attachments: %w", err)
	}
	seenAttachments := make(map[string]struct{}, len(runIDs))
	expiredAttachments := 0
	for attachmentRows.Next() {
		var runID, expiresRaw string
		if err := attachmentRows.Scan(&runID, &expiresRaw); err != nil {
			_ = attachmentRows.Close()
			return fmt.Errorf("runledger: scan monitored attachment preflight: %w", err)
		}
		if _, duplicate := seenAttachments[runID]; duplicate {
			_ = attachmentRows.Close()
			return monitorIntegrity("multiple attached attempts")
		}
		seenAttachments[runID] = struct{}{}
		expiresAt, err := monitorParseTimestamp(expiresRaw)
		if err != nil {
			_ = attachmentRows.Close()
			return err
		}
		if !expiresAt.After(now) {
			expiredAttachments++
		}
	}
	if err := attachmentRows.Err(); err != nil {
		_ = attachmentRows.Close()
		return fmt.Errorf("runledger: iterate monitored attachment preflight: %w", err)
	}
	if err := attachmentRows.Close(); err != nil {
		return fmt.Errorf("runledger: close monitored attachment preflight: %w", err)
	}
	mailboxExpiryArgs := append(append([]any(nil), baseArgs...),
		agentcoord.MessageClaimed, sqliteLeaseTimestamp(now), MonitorMaxExpiredMailboxClaims+1)
	var expiredMailbox int
	if err := db.queryRow(monitorExpiredMailboxCapacityQuery(placeholders), mailboxExpiryArgs...).Scan(&expiredMailbox); err != nil {
		return fmt.Errorf("runledger: count monitored mailbox expiry: %w", err)
	}
	if expiredMailbox > MonitorMaxExpiredMailboxClaims {
		return fmt.Errorf("%w: %d expired mailbox claims exceed %d", agentcoord.ErrMonitorCapacity, expiredMailbox, MonitorMaxExpiredMailboxClaims)
	}
	if expiredAttachments > 0 {
		updateArgs := []any{agentcoord.AttachmentExpired}
		updateArgs = append(updateArgs, baseArgs...)
		updateArgs = append(updateArgs, agentcoord.AttachmentAttached, sqliteLeaseTimestamp(now))
		if _, err := db.exec(`UPDATE agent_run_attempts SET state = ?
			WHERE session_id = ? AND run_id IN (`+placeholders+`) AND state = ? AND lease_expires_at <= ?`, updateArgs...); err != nil {
			return fmt.Errorf("runledger: materialize monitored attachment expiry: %w", err)
		}
	}
	if expiredMailbox > 0 {
		updateArgs := []any{agentcoord.MessageQueued}
		updateArgs = append(updateArgs, baseArgs...)
		updateArgs = append(updateArgs, agentcoord.MessageClaimed, sqliteLeaseTimestamp(now))
		if _, err := db.exec(`UPDATE agent_mailbox SET state = ?, lease_owner = NULL,
			lease_expires_at = NULL, attempt_id = NULL, lease_generation = 0,
			last_error = COALESCE(last_error, 'claim lease expired')
			WHERE session_id = ? AND run_id IN (`+placeholders+`) AND state = ?
				AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?`, updateArgs...); err != nil {
			return fmt.Errorf("runledger: materialize monitored mailbox expiry: %w", err)
		}
	}
	return nil
}

func monitorAttemptStatuses(
	db *monitorConn,
	now time.Time,
	sessionID string,
	runIDs []string,
) (map[string]agentcoord.AttemptStatus, error) {
	result := make(map[string]agentcoord.AttemptStatus, len(runIDs))
	for _, runID := range runIDs {
		result[runID] = agentcoord.AttemptStatus{State: agentcoord.AttemptNone}
	}
	placeholders := monitorPlaceholders(len(runIDs))
	args := make([]any, 0, len(runIDs)+2)
	args = append(args, sessionID)
	for _, runID := range runIDs {
		args = append(args, runID)
	}
	args = append(args, sqliteLeaseTimestamp(now))
	rows, err := db.query(`WITH stats AS (
		SELECT attempt.run_id, COUNT(*) AS attempt_count,
			MAX(attempt.lease_generation) AS maximum_generation,
			SUM(attempt.state = 'attached') AS attached_count,
			SUM(CASE WHEN
				attempt.attempt_id = '' OR length(CAST(attempt.attempt_id AS BLOB)) > ?
				OR attempt.lease_generation < 1
				OR NOT (attempt.parent_run_id IS run.parent_run_id)
				OR NOT (attempt.task_id IS run.task_id)
				OR (attempt.turn_id IS NOT NULL AND (attempt.turn_id = '' OR length(CAST(attempt.turn_id AS BLOB)) > ?))
				OR (attempt.pid IS NOT NULL AND attempt.pid <= 0)
				OR attempt.state NOT IN ('attached','detached','expired')
				OR strftime('%s', attempt.attached_at) IS NULL
				OR strftime('%s', attempt.heartbeat_at) IS NULL
				OR strftime('%s', attempt.lease_expires_at) IS NULL
				OR julianday(attempt.heartbeat_at) < julianday(attempt.attached_at)
				OR julianday(attempt.lease_expires_at) <= julianday(attempt.heartbeat_at)
				OR (attempt.state = 'attached' AND (attempt.detached_at IS NOT NULL OR attempt.lease_expires_at <= ?))
				OR (attempt.state = 'expired' AND attempt.detached_at IS NOT NULL)
				OR (attempt.state = 'detached' AND (attempt.detached_at IS NULL
					OR strftime('%s', attempt.detached_at) IS NULL
					OR julianday(attempt.detached_at) < julianday(attempt.heartbeat_at)))
			THEN 1 ELSE 0 END) AS invalid_count
		FROM agent_run_attempts attempt
		JOIN agent_runs run ON run.run_id = attempt.run_id AND run.session_id = attempt.session_id
		WHERE attempt.session_id = ? AND attempt.run_id IN (`+placeholders+`)
		GROUP BY attempt.run_id
	)
	SELECT stats.run_id, stats.attempt_count, stats.maximum_generation,
		stats.attached_count, stats.invalid_count,
		latest.parent_run_id, latest.task_id, latest.turn_id, latest.lease_generation,
		latest.state, latest.attached_at, latest.heartbeat_at,
		latest.lease_expires_at, latest.detached_at
	FROM stats
	JOIN agent_run_attempts latest ON latest.session_id = ? AND latest.run_id = stats.run_id
		AND latest.lease_generation = stats.maximum_generation
	ORDER BY stats.run_id ASC`, append([]any{
		AttachmentMaxID, AttachmentMaxID, sqliteLeaseTimestamp(now),
	}, append(args[:len(args)-1], sessionID)...)...)
	if err != nil {
		return nil, fmt.Errorf("runledger: read monitored attempts: %w", err)
	}
	defer rows.Close()
	seen := make(map[string]struct{}, len(runIDs))
	for rows.Next() {
		var (
			runID                                             string
			count, maximum, attachedCount, invalidCount       int64
			parent, task, turn, detachedRaw                   sql.NullString
			latestGeneration                                  int64
			state, attachedRaw, heartbeatRaw, leaseExpiresRaw string
		)
		if err := rows.Scan(
			&runID, &count, &maximum, &attachedCount, &invalidCount,
			&parent, &task, &turn, &latestGeneration, &state,
			&attachedRaw, &heartbeatRaw, &leaseExpiresRaw, &detachedRaw,
		); err != nil {
			return nil, fmt.Errorf("runledger: scan monitored attempt: %w", err)
		}
		if _, duplicate := seen[runID]; duplicate || count < 1 || count != maximum ||
			latestGeneration != maximum || attachedCount > 1 || invalidCount != 0 {
			return nil, monitorIntegrity("attempt history")
		}
		seen[runID] = struct{}{}
		attachedAt, err := monitorParseTimestamp(attachedRaw)
		if err != nil {
			return nil, err
		}
		heartbeatAt, err := monitorParseTimestamp(heartbeatRaw)
		if err != nil {
			return nil, err
		}
		leaseExpiresAt, err := monitorParseTimestamp(leaseExpiresRaw)
		if err != nil {
			return nil, err
		}
		attempt := agentcoord.AttemptStatus{
			Number: int(count), AttachedAt: &attachedAt,
			HeartbeatAt: &heartbeatAt, LeaseExpiresAt: &leaseExpiresAt,
		}
		switch state {
		case agentcoord.AttachmentAttached:
			if !leaseExpiresAt.After(now) {
				return nil, monitorIntegrity("attached attempt freshness")
			}
			attempt.State = agentcoord.AttemptAttached
		case agentcoord.AttachmentExpired:
			attempt.State = agentcoord.AttemptExpired
		case agentcoord.AttachmentDetached:
			attempt.State = agentcoord.AttemptDetached
			if !detachedRaw.Valid || detachedRaw.String == "" {
				return nil, monitorIntegrity("detached attempt timestamp")
			}
			detachedAt, err := monitorParseTimestamp(detachedRaw.String)
			if err != nil {
				return nil, err
			}
			attempt.DetachedAt = &detachedAt
		default:
			return nil, monitorIntegrity("attempt state")
		}
		if err := agentcoord.ValidateAttemptStatus(attempt); err != nil {
			return nil, err
		}
		result[runID] = attempt
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("runledger: iterate monitored attempts: %w", err)
	}
	return result, nil
}

func monitorExpiredMailboxCapacityQuery(placeholders string) string {
	return `SELECT COUNT(*) FROM (
		SELECT 1 FROM agent_mailbox
		WHERE session_id = ? AND run_id IN (` + placeholders + `) AND state = ?
			AND lease_expires_at IS NOT NULL AND lease_expires_at <= ?
		LIMIT ?
	)`
}

func monitorMailboxSummaries(
	db *monitorConn,
	now time.Time,
	sessionID string,
	runIDs []string,
) (map[string]agentcoord.MailboxSummary, error) {
	result := make(map[string]agentcoord.MailboxSummary, len(runIDs))
	for _, runID := range runIDs {
		result[runID] = agentcoord.MailboxSummary{}
	}
	placeholders := monitorPlaceholders(len(runIDs))
	args := []any{
		MailboxMaxIdentifier, MailboxMaxIdentifier, MailboxMaxIdentifier,
		MailboxMaxContent, MailboxMaxIdentifier, MailboxMaxIdentifier,
		MailboxMaxIdentifier, MailboxMaxIdentifier, MailboxMaxIdentifier,
		MailboxMaxIdentifier, sqliteLeaseTimestamp(now), sessionID,
	}
	for _, runID := range runIDs {
		args = append(args, runID)
	}
	rows, err := db.query(`SELECT mailbox.run_id, COUNT(*),
		SUM(mailbox.state = 'queued'), SUM(mailbox.state = 'claimed'),
		SUM(mailbox.state = 'processed'), SUM(mailbox.state = 'dead_letter'),
		COALESCE(MIN(mailbox.sequence), 0), COALESCE(MAX(mailbox.sequence), 0),
		SUM(CASE WHEN
			mailbox.message_id = '' OR length(CAST(mailbox.message_id AS BLOB)) > ?
			OR mailbox.schema_version <> 'm31.agent.message.v1'
			OR mailbox.sequence < 1
			OR mailbox.to_id <> mailbox.run_id OR length(CAST(mailbox.to_id AS BLOB)) > ?
			OR mailbox.kind = '' OR length(CAST(mailbox.kind AS BLOB)) > ?
			OR mailbox.byte_count < 0 OR mailbox.byte_count > ?
			OR (mailbox.parent_run_id IS NOT NULL AND NOT (mailbox.parent_run_id IS target.parent_run_id))
			OR (mailbox.task_id IS NOT NULL AND NOT (mailbox.task_id IS target.task_id))
			OR (mailbox.parent_run_id IS NOT NULL AND (mailbox.parent_run_id = '' OR length(CAST(mailbox.parent_run_id AS BLOB)) > ?))
			OR (mailbox.task_id IS NOT NULL AND (mailbox.task_id = '' OR length(CAST(mailbox.task_id AS BLOB)) > ?))
			OR mailbox.from_id IS NULL OR mailbox.from_id = '' OR length(CAST(mailbox.from_id AS BLOB)) > ?
			OR (mailbox.lease_owner IS NOT NULL AND (mailbox.lease_owner = '' OR length(CAST(mailbox.lease_owner AS BLOB)) > ?))
			OR (mailbox.attempt_id IS NOT NULL AND (mailbox.attempt_id = '' OR length(CAST(mailbox.attempt_id AS BLOB)) > ?))
			OR (mailbox.source_attempt_id IS NOT NULL AND (mailbox.source_attempt_id = '' OR length(CAST(mailbox.source_attempt_id AS BLOB)) > ?))
			OR strftime('%s', mailbox.created_at) IS NULL
			OR mailbox.attempt_count < 0
			OR mailbox.state NOT IN ('queued','claimed','processed','dead_letter')
			OR (mailbox.state = 'queued' AND
				(mailbox.lease_owner IS NOT NULL OR mailbox.lease_expires_at IS NOT NULL
					OR mailbox.attempt_id IS NOT NULL OR mailbox.lease_generation <> 0
					OR mailbox.processed_at IS NOT NULL OR mailbox.dead_lettered_at IS NOT NULL))
			OR (mailbox.state = 'claimed' AND
				(mailbox.lease_owner IS NULL OR mailbox.lease_owner = ''
					OR mailbox.lease_expires_at IS NULL OR mailbox.attempt_id IS NULL OR mailbox.attempt_id = ''
					OR mailbox.lease_generation < 1 OR mailbox.claimed_at IS NULL OR mailbox.attempt_count < 1
					OR mailbox.lease_expires_at <= ? OR mailbox.processed_at IS NOT NULL OR mailbox.dead_lettered_at IS NOT NULL
					OR NOT EXISTS (SELECT 1 FROM agent_run_attempts delivery
						WHERE delivery.session_id = mailbox.session_id AND delivery.run_id = mailbox.run_id
							AND delivery.attempt_id = mailbox.attempt_id
							AND delivery.lease_generation = mailbox.lease_generation)))
			OR (mailbox.state IN ('processed','dead_letter') AND
				(mailbox.lease_owner IS NULL OR mailbox.lease_owner = ''
					OR mailbox.lease_expires_at IS NULL OR mailbox.attempt_id IS NULL OR mailbox.attempt_id = ''
					OR mailbox.lease_generation < 1 OR mailbox.claimed_at IS NULL OR mailbox.attempt_count < 1
					OR NOT EXISTS (SELECT 1 FROM agent_run_attempts delivery
						WHERE delivery.session_id = mailbox.session_id AND delivery.run_id = mailbox.run_id
							AND delivery.attempt_id = mailbox.attempt_id
							AND delivery.lease_generation = mailbox.lease_generation)))
			OR (mailbox.state = 'processed' AND (mailbox.processed_at IS NULL OR mailbox.dead_lettered_at IS NOT NULL))
			OR (mailbox.state = 'dead_letter' AND (mailbox.dead_lettered_at IS NULL OR mailbox.processed_at IS NOT NULL))
			OR (mailbox.claimed_at IS NOT NULL AND
				(strftime('%s', mailbox.claimed_at) IS NULL OR julianday(mailbox.claimed_at) < julianday(mailbox.created_at)))
			OR (mailbox.lease_expires_at IS NOT NULL AND
				(strftime('%s', mailbox.lease_expires_at) IS NULL OR mailbox.claimed_at IS NULL
					OR julianday(mailbox.lease_expires_at) <= julianday(mailbox.claimed_at)))
			OR (mailbox.processed_at IS NOT NULL AND
				(strftime('%s', mailbox.processed_at) IS NULL OR julianday(mailbox.processed_at) < julianday(mailbox.created_at)
					OR (mailbox.claimed_at IS NOT NULL AND julianday(mailbox.processed_at) < julianday(mailbox.claimed_at))
					OR (mailbox.lease_expires_at IS NOT NULL AND julianday(mailbox.processed_at) >= julianday(mailbox.lease_expires_at))))
			OR (mailbox.dead_lettered_at IS NOT NULL AND
				(strftime('%s', mailbox.dead_lettered_at) IS NULL OR julianday(mailbox.dead_lettered_at) < julianday(mailbox.created_at)
					OR (mailbox.claimed_at IS NOT NULL AND julianday(mailbox.dead_lettered_at) < julianday(mailbox.claimed_at))
					OR (mailbox.lease_expires_at IS NOT NULL AND julianday(mailbox.dead_lettered_at) >= julianday(mailbox.lease_expires_at))))
			OR (lower(COALESCE(mailbox.from_id, '')) = 'operator' OR lower(mailbox.kind) = 'steer') AND NOT (
				mailbox.from_id = 'operator' AND mailbox.kind = 'steer'
				AND mailbox.source_attempt_id IS NULL AND mailbox.source_lease_generation = 0)
			OR (mailbox.from_id = 'operator' AND mailbox.kind = 'steer') AND NOT (
				mailbox.source_attempt_id IS NULL AND mailbox.source_lease_generation = 0)
			OR (mailbox.from_id <> 'operator' AND mailbox.kind <> 'steer') AND (
				source.run_id IS NULL OR source.session_id <> mailbox.session_id
				OR NOT ((source.parent_run_id IS NOT NULL AND source.parent_run_id = mailbox.run_id)
					OR (target.parent_run_id IS NOT NULL AND target.parent_run_id = source.run_id))
				OR mailbox.source_attempt_id IS NULL OR mailbox.source_attempt_id = '' OR mailbox.source_lease_generation < 1
				OR NOT EXISTS (SELECT 1 FROM agent_run_attempts source_attempt
					WHERE source_attempt.session_id = mailbox.session_id AND source_attempt.run_id = source.run_id
						AND source_attempt.attempt_id = mailbox.source_attempt_id
						AND source_attempt.lease_generation = mailbox.source_lease_generation))
			THEN 1 ELSE 0 END)
	FROM agent_mailbox mailbox
	JOIN agent_runs target ON target.run_id = mailbox.run_id AND target.session_id = mailbox.session_id
	LEFT JOIN agent_runs source ON source.run_id = mailbox.from_id
	WHERE mailbox.session_id = ? AND mailbox.run_id IN (`+placeholders+`)
	GROUP BY mailbox.run_id ORDER BY mailbox.run_id ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("runledger: summarize monitored mailboxes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var runID string
		var total, queued, claimed, processed, dead, minimum, maximum, invalid int64
		if err := rows.Scan(&runID, &total, &queued, &claimed, &processed, &dead, &minimum, &maximum, &invalid); err != nil {
			return nil, fmt.Errorf("runledger: scan monitored mailbox summary: %w", err)
		}
		if invalid != 0 || total < 1 || minimum != 1 || maximum != total ||
			queued+claimed+processed+dead != total || total > agentcoord.MaxMonitorSequence {
			return nil, monitorIntegrity("mailbox history")
		}
		summary := agentcoord.MailboxSummary{
			Queued: int(queued), Claimed: int(claimed), Processed: int(processed),
			DeadLetter: int(dead), LastSequence: maximum,
		}
		if err := agentcoord.ValidateMailboxSummary(summary); err != nil {
			return nil, err
		}
		result[runID] = summary
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("runledger: iterate monitored mailbox summaries: %w", err)
	}
	return result, nil
}

func monitorMailboxPage(db *monitorConn, query agentcoord.MailboxStatusQuery) (agentcoord.MailboxStatusPage, error) {
	clauses := []string{"mailbox.session_id = ?", "mailbox.run_id = ?", "mailbox.sequence > ?"}
	args := []any{query.SessionID, query.RunID, query.AfterSequence}
	if len(query.States) > 0 {
		states := make([]string, len(query.States))
		for index, state := range query.States {
			states[index] = "?"
			args = append(args, state)
		}
		clauses = append(clauses, "mailbox.state IN ("+strings.Join(states, ",")+")")
	}
	args = append(args, query.Limit+1)
	rows, err := db.query(`SELECT mailbox.session_id, mailbox.run_id, mailbox.message_id,
		mailbox.from_id, mailbox.kind, mailbox.state, mailbox.sequence, mailbox.byte_count,
		mailbox.created_at, mailbox.processed_at, mailbox.dead_lettered_at,
		target.parent_run_id, source.parent_run_id, source.session_id
	FROM agent_mailbox mailbox
	JOIN agent_runs target ON target.run_id = mailbox.run_id AND target.session_id = mailbox.session_id
	LEFT JOIN agent_runs source ON source.run_id = mailbox.from_id
	WHERE `+strings.Join(clauses, " AND ")+`
	ORDER BY mailbox.sequence ASC, mailbox.message_id ASC LIMIT ?`, args...)
	if err != nil {
		return agentcoord.MailboxStatusPage{}, fmt.Errorf("runledger: list monitored mailbox: %w", err)
	}
	defer rows.Close()
	page := agentcoord.MailboxStatusPage{
		Messages: make([]agentcoord.MailboxStatus, 0, query.Limit),
		Next:     query.AfterSequence,
	}
	for rows.Next() {
		var (
			status                                    agentcoord.MailboxStatus
			from, processedRaw, deadRaw               sql.NullString
			targetParent, sourceParent, sourceSession sql.NullString
			createdRaw, storedState                   string
		)
		if err := rows.Scan(
			&status.SessionID, &status.RunID, &status.MessageID, &from, &status.Kind,
			&storedState, &status.Sequence, &status.ByteCount, &createdRaw,
			&processedRaw, &deadRaw, &targetParent, &sourceParent, &sourceSession,
		); err != nil {
			return agentcoord.MailboxStatusPage{}, fmt.Errorf("runledger: scan monitored mailbox: %w", err)
		}
		status.State = agentcoord.MailboxState(storedState)
		createdAt, err := monitorParseTimestamp(createdRaw)
		if err != nil {
			return agentcoord.MailboxStatusPage{}, err
		}
		status.CreatedAt = createdAt
		if processedRaw.Valid {
			processedAt, err := monitorParseTimestamp(processedRaw.String)
			if err != nil {
				return agentcoord.MailboxStatusPage{}, err
			}
			status.ProcessedAt = &processedAt
		}
		if deadRaw.Valid {
			deadAt, err := monitorParseTimestamp(deadRaw.String)
			if err != nil {
				return agentcoord.MailboxStatusPage{}, err
			}
			status.DeadLetteredAt = &deadAt
		}
		switch {
		case from.Valid && from.String == agentcoord.OperatorIdentity && status.Kind == agentcoord.OperatorSteerKind:
			status.Direction = agentcoord.MailboxFromOperator
		case from.Valid && targetParent.Valid && targetParent.String == from.String &&
			sourceSession.Valid && sourceSession.String == status.SessionID:
			status.Direction = agentcoord.MailboxFromParent
			status.PeerRunID = from.String
		case from.Valid && sourceParent.Valid && sourceParent.String == status.RunID &&
			sourceSession.Valid && sourceSession.String == status.SessionID:
			status.Direction = agentcoord.MailboxFromChild
			status.PeerRunID = from.String
		default:
			return agentcoord.MailboxStatusPage{}, monitorIntegrity("mailbox peer relationship")
		}
		if err := agentcoord.ValidateMailboxStatus(status); err != nil {
			return agentcoord.MailboxStatusPage{}, err
		}
		page.Messages = append(page.Messages, status)
	}
	if err := rows.Err(); err != nil {
		return agentcoord.MailboxStatusPage{}, fmt.Errorf("runledger: iterate monitored mailbox: %w", err)
	}
	if len(page.Messages) > query.Limit {
		page.HasMore = true
		page.Messages = page.Messages[:query.Limit]
	}
	if len(page.Messages) > 0 {
		page.Next = page.Messages[len(page.Messages)-1].Sequence
	}
	return page, nil
}

func monitorPlaceholders(count int) string {
	values := make([]string, count)
	for index := range values {
		values[index] = "?"
	}
	return strings.Join(values, ",")
}

func monitorParseTimestamp(raw string) (time.Time, error) {
	if raw == "" || strings.TrimSpace(raw) != raw {
		return time.Time{}, monitorIntegrity("timestamp")
	}
	formats := []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999-07:00"}
	for _, format := range formats {
		value, err := time.Parse(format, raw)
		if err == nil && !value.IsZero() {
			return value.UTC(), nil
		}
	}
	return time.Time{}, monitorIntegrity("timestamp")
}

func monitorIntegrity(field string) error {
	return fmt.Errorf("%w: %s", agentcoord.ErrMonitorIntegrity, field)
}

var _ agentcoord.MonitorReader = (*SQLiteStore)(nil)
