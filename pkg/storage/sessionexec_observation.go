package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"m31labs.dev/buckley/pkg/sessionexec"
	modernsqlite "modernc.org/sqlite"
)

const sessionExecObservationSafeTextFunction = "buckley_sessionexec_safe_text_v1"

func init() {
	modernsqlite.MustRegisterDeterministicScalarFunction(
		sessionExecObservationSafeTextFunction,
		1,
		func(_ *modernsqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			if len(args) != 1 {
				return int64(0), nil
			}
			value, ok := args[0].(string)
			if !ok || !sessionExecObservationSafeText(value) {
				return int64(0), nil
			}
			return int64(1), nil
		},
	)
}

func sessionExecObservationSafeText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return false
		}
	}
	return true
}

const sessionExecObservationCommandColumns = sessionExecStoredCommandColumns + `,
heartbeat_at_ms, completed_at_ms, error_code, error_text, outcome_json`

const sessionExecObservationRecentSQL = `SELECT ` + sessionExecObservationCommandColumns + `
	FROM session_commands WHERE session_id = ?
	ORDER BY sequence DESC, command_id DESC LIMIT ?`

const sessionExecObservationOneSQL = `SELECT ` + sessionExecObservationCommandColumns + `
	FROM session_commands WHERE session_id = ? AND command_id = ?`

const sessionExecObservationBusySlice = 25 * time.Millisecond

type sessionExecObservationCommand struct {
	stored      sessionExecStoredCommand
	heartbeatAt sql.NullInt64
	completedAt sql.NullInt64
	errorCode   sql.NullString
	errorText   sql.NullString
	outcomeJSON sql.NullString
}

type sessionExecObservedCommand struct {
	status            sessionexec.CommandStatus
	leaseGeneration   int64
	leaseOwner        string
	leaseOwnerValid   bool
	leaseExpires      int64
	leaseExpiresValid bool
	terminalOrigin    sessionExecTerminalOrigin
}

type sessionExecTerminalOrigin uint8

const (
	sessionExecOriginLive sessionExecTerminalOrigin = iota
	sessionExecOriginComplete
	sessionExecOriginCancelPending
	sessionExecOriginQuiesce
	sessionExecOriginAmbiguity
)

// sessionExecObservationTrace is an internal test seam for proving bounded
// envelope authentication and transaction progress without wall-clock races.
// Its context key is package-private, so production callers cannot install it.
type sessionExecObservationTrace struct {
	envelopeLoaded func()
	aggregateRead  func()
}

type sessionExecObservationTraceKey struct{}

const sessionExecObservationAggregateSQL = `SELECT
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
	COALESCE(MIN(sequence), 0),
	COALESCE(MAX(sequence), 0),
	COUNT(sequence),
	COALESCE(SUM(CASE WHEN
		state NOT IN ('accepted','running','succeeded','failed','blocked','interrupted','cancelled')
		OR lane NOT IN ('work','control')
		OR NOT ((command_type IN ('input','queue','steer','model','slash') AND lane = 'work')
			OR (command_type IN ('interrupt','approval','pause','resume') AND lane = 'control'))
		OR task_id <> 'foreground'
		OR generation <> 0
		OR attempt < 0 OR attempt > ?4
		OR lease_generation < 0 OR lease_generation > ?4
		OR (completion_lease_generation IS NOT NULL AND
			(completion_lease_generation < 1 OR completion_lease_generation > ?4))
		OR attempt <> lease_generation
		OR (attempt = 0 AND started_at_ms IS NOT NULL)
		OR (attempt > 0 AND started_at_ms IS NULL)
		OR accepted_at_ms < 0
		OR (started_at_ms IS NOT NULL AND started_at_ms < accepted_at_ms)
		OR (heartbeat_at_ms IS NOT NULL AND
			(started_at_ms IS NULL OR heartbeat_at_ms < started_at_ms))
		OR (completed_at_ms IS NOT NULL AND
			(completed_at_ms < accepted_at_ms
				OR (started_at_ms IS NOT NULL AND completed_at_ms < started_at_ms)))
		OR (lease_owner IS NOT NULL AND
			(typeof(lease_owner) <> 'text' OR length(CAST(lease_owner AS BLOB)) NOT BETWEEN 1 AND ?5
				OR substr(lease_owner, 1, 1) NOT GLOB '[A-Za-z0-9]'
				OR lease_owner GLOB '*[^-A-Za-z0-9._:@+/]*'))
		OR (completed_by IS NOT NULL AND
			(typeof(completed_by) <> 'text' OR length(CAST(completed_by AS BLOB)) NOT BETWEEN 1 AND ?5
				OR substr(completed_by, 1, 1) NOT GLOB '[A-Za-z0-9]'
				OR completed_by GLOB '*[^-A-Za-z0-9._:@+/]*'))
		OR (error_code IS NOT NULL AND
			(typeof(error_code) <> 'text' OR length(CAST(error_code AS BLOB)) NOT BETWEEN 1 AND ?6
				OR substr(error_code, 1, 1) NOT GLOB '[A-Za-z0-9]'
				OR error_code GLOB '*[^A-Za-z0-9._:-]*'))
		OR (error_text IS NOT NULL AND
			(typeof(error_text) <> 'text' OR length(CAST(error_text AS BLOB)) NOT BETWEEN 1 AND ?7
				OR ` + sessionExecObservationSafeTextFunction + `(error_text) <> 1))
		OR (outcome_json IS NOT NULL AND
			(typeof(outcome_json) <> 'text' OR length(CAST(outcome_json AS BLOB)) NOT BETWEEN 2 AND ?8
				OR json_valid(outcome_json) <> 1))
		OR (completion_digest IS NOT NULL AND
			(length(completion_digest) <> 64 OR completion_digest <> lower(completion_digest)
				OR completion_digest GLOB '*[^0123456789abcdef]*'))
		OR ((completed_by IS NULL) <> (completion_lease_generation IS NULL))
		OR (target_command_id IS NOT NULL AND
			(command_type NOT IN ('steer','interrupt') OR NOT EXISTS (
				SELECT 1 FROM session_commands target
				WHERE target.session_id = c.session_id
					AND target.command_id = c.target_command_id
					AND target.lane = 'work' AND target.sequence < c.sequence
			)))
		OR (state = 'accepted' AND
			(lease_owner IS NOT NULL OR lease_expires_at_ms IS NOT NULL OR heartbeat_at_ms IS NOT NULL
				OR completed_at_ms IS NOT NULL OR error_code IS NOT NULL OR error_text IS NOT NULL
				OR outcome_json IS NOT NULL OR completion_digest IS NOT NULL OR completed_by IS NOT NULL
				OR completion_lease_generation IS NOT NULL))
		OR (state = 'running' AND
			(attempt < 1 OR lease_owner IS NULL OR lease_expires_at_ms IS NULL OR heartbeat_at_ms IS NULL
				OR lease_expires_at_ms <= heartbeat_at_ms OR completed_at_ms IS NOT NULL
				OR error_code IS NOT NULL OR error_text IS NOT NULL OR outcome_json IS NOT NULL
				OR completion_digest IS NOT NULL OR completed_by IS NOT NULL
				OR completion_lease_generation IS NOT NULL))
		OR (state IN ('succeeded','failed','blocked','interrupted','cancelled') AND
			(lease_owner IS NOT NULL OR lease_expires_at_ms IS NOT NULL OR heartbeat_at_ms IS NOT NULL
				OR completed_at_ms IS NULL OR outcome_json IS NULL))
		OR (completed_by IS NOT NULL AND
			(completion_digest IS NULL OR attempt < 1 OR completion_lease_generation <> lease_generation))
		OR (completed_by IS NULL AND state IN ('succeeded','failed','interrupted'))
		OR (completed_by IS NULL AND state = 'blocked' AND
			(completion_digest IS NOT NULL OR error_code IS NULL OR error_code <> 'ambiguous_effect' OR error_text IS NOT NULL
				OR outcome_json <> '{}' OR NOT EXISTS (
					SELECT 1 FROM session_effect_permits effect
					WHERE effect.session_id = c.session_id AND effect.command_id = c.command_id
						AND effect.generation = c.generation AND effect.ambiguous_at_ms IS NOT NULL
						AND effect.state IN ('ambiguous','ended','resolved')
				)))
		OR (completed_by IS NULL AND state = 'cancelled' AND
			(error_code IS NULL OR error_text IS NOT NULL OR outcome_json <> '{}'
				OR (completion_digest IS NULL AND NOT (?2 <> 'headless' AND error_code = ?3))))
		THEN 1 ELSE 0 END), 0)
	FROM session_commands c WHERE c.session_id = ?1`

func (s *Store) GetExecutionSnapshot(ctx context.Context, sessionID string, recentLimit int) (sessionexec.ExecutionSnapshot, error) {
	if err := sessionexec.ValidateSessionID(sessionID); err != nil {
		return sessionexec.ExecutionSnapshot{}, err
	}
	if err := sessionexec.ValidateRecentCommandStatusesLimit(recentLimit); err != nil {
		return sessionexec.ExecutionSnapshot{}, err
	}
	if s == nil || s.db == nil {
		return sessionexec.ExecutionSnapshot{}, ErrStoreClosed
	}

	var snapshot sessionexec.ExecutionSnapshot
	err := s.withSessionExecObservation(ctx, func(db *sessionExecConn) error {
		current := sessionexec.ExecutionSnapshot{
			SessionID: sessionID,
			Summary:   sessionexec.Summary{SessionID: sessionID},
		}
		if err := sessionExecSessionExists(db, sessionID); err != nil {
			return err
		}
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		current.ObservedAt = sessionExecTime(now)
		state, initialized, err := sessionExecObservationState(db, sessionID)
		if err != nil {
			return err
		}
		if !initialized {
			if err := sessionExecObservationRequireNoArtifacts(db, sessionID); err != nil {
				return err
			}
			snapshot = current
			return nil
		}
		current.Initialized = true
		current.ExecutionState = state
		if err := sessionExecMaterializeExpiredEffects(db, sessionID, now); err != nil {
			return err
		}

		summary, err := sessionExecObservationSummary(db, sessionID, state)
		if err != nil {
			return err
		}
		sessionExecObservationRecordAggregate(db)
		current.Summary = summary
		permits, err := sessionExecListEffectPermits(db, sessionID)
		if err != nil {
			return err
		}
		recent, err := sessionExecObservationRecentCommands(db, sessionID, recentLimit, state)
		if err != nil {
			return err
		}
		commandByID := make(map[string]sessionExecObservedCommand, len(recent)+len(permits))
		for _, command := range recent {
			commandByID[command.status.CommandID] = command
		}
		ownerIDs, err := sessionExecObservationMissingPermitOwners(permits, commandByID)
		if err != nil {
			return err
		}
		for _, commandID := range ownerIDs {
			command, err := sessionExecObservationOneCommand(db, sessionID, commandID, state)
			if errors.Is(err, sessionexec.ErrNotFound) {
				return sessionExecStoredConflict("observation effect command")
			}
			if err != nil {
				return err
			}
			commandByID[commandID] = command
		}
		if len(commandByID) > sessionexec.MaxRecentCommandStatuses+sessionexec.MaxEffectPermitsPerSession {
			return sessionExecStoredConflict("observation authentication union")
		}
		effectsByCommand, effectSummaries, overall, attention, err := sessionExecObservationProjectEffects(
			permits, commandByID, true,
		)
		if err != nil {
			return err
		}
		current.EffectSummary = overall
		if len(attention) > sessionexec.MaxAttentionEffects {
			current.AttentionEffectsTruncated = true
			attention = attention[:sessionexec.MaxAttentionEffects]
		}
		current.AttentionEffects = attention

		current.RecentCommands = sessionExecObservationStatuses(recent, effectsByCommand, effectSummaries)
		snapshot = current
		return nil
	})
	if err != nil {
		return sessionexec.ExecutionSnapshot{}, err
	}
	return snapshot, nil
}

func (s *Store) ListCommandStatuses(ctx context.Context, query sessionexec.CommandStatusQuery) (sessionexec.CommandStatusPage, error) {
	query, err := sessionexec.NormalizeCommandStatusQuery(query)
	if err != nil {
		return sessionexec.CommandStatusPage{}, err
	}
	if s == nil || s.db == nil {
		return sessionexec.CommandStatusPage{}, ErrStoreClosed
	}

	var page sessionexec.CommandStatusPage
	err = s.withSessionExecObservation(ctx, func(db *sessionExecConn) error {
		current := sessionexec.CommandStatusPage{
			Commands: make([]sessionexec.CommandStatus, 0),
			Next:     query.AfterSequence,
		}
		if err := sessionExecSessionExists(db, query.SessionID); err != nil {
			return err
		}
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		execution, initialized, err := sessionExecObservationState(db, query.SessionID)
		if err != nil {
			return err
		}
		if !initialized {
			if err := sessionExecObservationRequireNoArtifacts(db, query.SessionID); err != nil {
				return err
			}
			page = current
			return nil
		}
		if err := sessionExecMaterializeExpiredEffects(db, query.SessionID, now); err != nil {
			return err
		}

		commands, err := sessionExecObservationCommandPage(db, query, execution)
		if err != nil {
			return err
		}
		if len(commands) > query.Limit {
			current.HasMore = true
			commands = commands[:query.Limit]
		}
		if len(commands) == 0 {
			page = current
			return nil
		}
		permits, err := sessionExecListEffectPermits(db, query.SessionID)
		if err != nil {
			return err
		}
		commandByID := make(map[string]sessionExecObservedCommand, len(commands))
		for _, command := range commands {
			commandByID[command.status.CommandID] = command
		}
		effectsByCommand, effectSummaries, _, _, err := sessionExecObservationProjectEffects(
			permits, commandByID, false,
		)
		if err != nil {
			return err
		}
		current.Commands = sessionExecObservationStatuses(commands, effectsByCommand, effectSummaries)
		current.Next = current.Commands[len(current.Commands)-1].Sequence
		page = current
		return nil
	})
	if err != nil {
		return sessionexec.CommandStatusPage{}, err
	}
	return page, nil
}

func (s *Store) GetCommandStatus(ctx context.Context, sessionID, commandID string) (sessionexec.CommandStatus, error) {
	if err := sessionexec.ValidateSessionID(sessionID); err != nil {
		return sessionexec.CommandStatus{}, err
	}
	if err := sessionexec.ValidateCommandID(commandID); err != nil {
		return sessionexec.CommandStatus{}, err
	}
	if s == nil || s.db == nil {
		return sessionexec.CommandStatus{}, ErrStoreClosed
	}

	var status sessionexec.CommandStatus
	err := s.withSessionExecObservation(ctx, func(db *sessionExecConn) error {
		if err := sessionExecSessionExists(db, sessionID); err != nil {
			return err
		}
		now, err := sessionExecNowMillis(db)
		if err != nil {
			return err
		}
		execution, initialized, err := sessionExecObservationState(db, sessionID)
		if err != nil {
			return err
		}
		if !initialized {
			if err := sessionExecObservationRequireNoArtifacts(db, sessionID); err != nil {
				return err
			}
			return sessionexec.ErrNotFound
		}
		if err := sessionExecMaterializeExpiredEffects(db, sessionID, now); err != nil {
			return err
		}
		command, err := sessionExecObservationOneCommand(db, sessionID, commandID, execution)
		if err != nil {
			return err
		}
		permits, err := sessionExecListEffectPermits(db, sessionID)
		if err != nil {
			return err
		}
		commandByID := map[string]sessionExecObservedCommand{commandID: command}
		effectsByCommand, effectSummaries, _, _, err := sessionExecObservationProjectEffects(
			permits, commandByID, false,
		)
		if err != nil {
			return err
		}
		status = sessionExecObservationStatuses([]sessionExecObservedCommand{command}, effectsByCommand, effectSummaries)[0]
		return nil
	})
	if err != nil {
		return sessionexec.CommandStatus{}, err
	}
	return status, nil
}

func (s *Store) withSessionExecObservation(ctx context.Context, fn func(*sessionExecConn) error) error {
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
			return fmt.Errorf("acquire session observation connection: %w", err)
		}
		busyWait := sessionExecObservationBusySlice
		if deadline, ok := ctx.Deadline(); ok {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				_ = conn.Close()
				if ctxErr := ctx.Err(); ctxErr != nil {
					return ctxErr
				}
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
			_, err = conn.ExecContext(ctx, `BEGIN IMMEDIATE`)
			began = err == nil
		}
		if err == nil {
			bound := &sessionExecConn{ctx: ctx, conn: conn}
			err = fn(bound)
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
		_, restoreErr := conn.ExecContext(context.Background(), fmt.Sprintf(`PRAGMA busy_timeout = %d`, sqliteWALBusyTimeout.Milliseconds()))
		if restoreErr != nil {
			discard = true
			if err == nil {
				err = fmt.Errorf("restore session observation busy timeout: %w", restoreErr)
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
		if !IsSQLiteBusyError(err) {
			return err
		}
		if attempt == sqliteWALRetryLimit-1 {
			break
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
	return fmt.Errorf("session observation transaction remained busy: %w", lastErr)
}

func sessionExecObservationState(db *sessionExecConn, sessionID string) (sessionexec.ExecutionState, bool, error) {
	state, err := sessionExecReadExecutionState(db, sessionID)
	if errors.Is(err, sessionexec.ErrNotFound) {
		return sessionexec.ExecutionState{}, false, nil
	}
	if err != nil {
		return sessionexec.ExecutionState{}, false, err
	}
	if state.SessionID != sessionID ||
		(state.Mode == sessionexec.ExecutionModeHeadless && (state.Generation != 0 || state.ReasonCode != "")) ||
		(state.Mode != sessionexec.ExecutionModeHeadless && (state.Generation < 1 || state.ReasonCode == "")) {
		return sessionexec.ExecutionState{}, false, sessionExecStoredConflict("observation execution state")
	}
	return state, true, nil
}

func sessionExecObservationRequireNoArtifacts(db *sessionExecConn, sessionID string) error {
	var commands, effects, transcript int
	if err := db.queryRow(`SELECT
		(SELECT COUNT(*) FROM session_commands WHERE session_id = ?),
		(SELECT COUNT(*) FROM session_effect_permits WHERE session_id = ?),
		(SELECT COUNT(*) FROM session_command_transcript WHERE session_id = ?)`,
		sessionID, sessionID, sessionID).Scan(&commands, &effects, &transcript); err != nil {
		return fmt.Errorf("inspect uninitialized session execution artifacts: %w", err)
	}
	if commands != 0 || effects != 0 || transcript != 0 {
		return sessionExecStoredConflict("uninitialized observation artifacts")
	}
	return nil
}

func scanSessionExecObservationCommand(row rowScanner) (sessionExecObservationCommand, error) {
	var command sessionExecObservationCommand
	stored := &command.stored
	err := row.Scan(
		&stored.command.SessionID, &stored.command.RunID, &stored.command.TaskID,
		&stored.command.CommandID, &stored.command.TurnID, &stored.command.Generation,
		&stored.command.Sequence, &stored.command.Lane, &stored.command.Type,
		&stored.command.Content, &stored.command.InputDigest, &stored.command.AcceptedBy,
		&stored.target, &stored.state, &stored.attempt, &stored.leaseGeneration,
		&stored.leaseOwner, &stored.leaseExpires, &stored.acceptedAt, &stored.startedAt,
		&stored.completionDigest, &stored.completedBy, &stored.completedLeaseGeneration,
		&command.heartbeatAt, &command.completedAt, &command.errorCode,
		&command.errorText, &command.outcomeJSON,
	)
	return command, err
}

func sessionExecValidateObservationCommand(command sessionExecObservationCommand, execution sessionexec.ExecutionState) error {
	stored := command.stored
	if err := sessionExecValidateStoredAcceptance(stored); err != nil {
		return err
	}
	if stored.command.Type != strings.ToLower(strings.TrimSpace(stored.command.Type)) || !stored.state.Valid() {
		return sessionExecStoredConflict("observation command vocabulary")
	}
	if (stored.startedAt.Valid && stored.startedAt.Int64 < stored.acceptedAt) ||
		(command.completedAt.Valid && (command.completedAt.Int64 < stored.acceptedAt ||
			(stored.startedAt.Valid && command.completedAt.Int64 < stored.startedAt.Int64))) {
		return sessionExecStoredConflict("observation command timestamp")
	}
	if (stored.attempt == 0 && stored.startedAt.Valid) || (stored.attempt > 0 && !stored.startedAt.Valid) {
		return sessionExecStoredConflict("observation command attempt timestamp")
	}
	if int64(stored.attempt) != stored.leaseGeneration {
		return sessionExecStoredConflict("observation command attempt fence")
	}
	if command.errorCode.Valid {
		if command.errorCode.String == "" || sessionexec.ValidateErrorCode(command.errorCode.String) != nil {
			return sessionExecStoredConflict("observation command error code")
		}
	}
	if command.errorText.Valid && (command.errorText.String == "" || !sessionExecObservationSafeText(command.errorText.String) ||
		len(command.errorText.String) > sessionexec.MaxErrorTextBytes) {
		return sessionExecStoredConflict("observation command error text")
	}
	if stored.target.Valid && stored.target.String == "" {
		return sessionExecStoredConflict("observation command target")
	}
	if stored.completedBy.Valid != stored.completedLeaseGeneration.Valid {
		return sessionExecStoredConflict("observation command completion fence")
	}
	if stored.completedLeaseGeneration.Valid && (stored.completedLeaseGeneration.Int64 <= 0 ||
		stored.completedLeaseGeneration.Int64 > int64(sessionexec.MaxCommandAttempts) ||
		stored.completedLeaseGeneration.Int64 != stored.leaseGeneration) {
		return sessionExecStoredConflict("observation command completion fence")
	}
	if stored.completedBy.Valid {
		if !stored.completionDigest.Valid || sessionexec.ValidateLeaseRef(sessionexec.LeaseRef{
			SessionID: stored.command.SessionID, CommandID: stored.command.CommandID,
			Generation: stored.command.Generation, Owner: stored.completedBy.String,
			LeaseGeneration: stored.completedLeaseGeneration.Int64,
		}) != nil {
			return sessionExecStoredConflict("observation command completion fence")
		}
	}
	if stored.completionDigest.Valid {
		digest, err := hex.DecodeString(stored.completionDigest.String)
		if err != nil || len(digest) != 32 || stored.completionDigest.String != strings.ToLower(stored.completionDigest.String) {
			return sessionExecStoredConflict("observation command completion digest")
		}
	}

	switch stored.state {
	case sessionexec.StateAccepted:
		if stored.leaseOwner.Valid || stored.leaseExpires.Valid || command.heartbeatAt.Valid || command.completedAt.Valid ||
			command.errorCode.Valid || command.errorText.Valid || command.outcomeJSON.Valid || stored.completionDigest.Valid ||
			stored.completedBy.Valid || stored.completedLeaseGeneration.Valid {
			return sessionExecStoredConflict("observation accepted command fields")
		}
	case sessionexec.StateRunning:
		if err := sessionExecValidateStoredCommand(stored); err != nil {
			return err
		}
		if !command.heartbeatAt.Valid || command.heartbeatAt.Int64 < stored.acceptedAt ||
			command.heartbeatAt.Int64 < stored.startedAt.Int64 ||
			stored.leaseExpires.Int64 <= command.heartbeatAt.Int64 || command.completedAt.Valid ||
			command.errorCode.Valid || command.errorText.Valid || command.outcomeJSON.Valid || stored.completionDigest.Valid ||
			stored.completedBy.Valid || stored.completedLeaseGeneration.Valid {
			return sessionExecStoredConflict("observation running command fields")
		}
	default:
		if stored.leaseOwner.Valid || stored.leaseExpires.Valid || command.heartbeatAt.Valid || !command.completedAt.Valid ||
			!command.outcomeJSON.Valid || command.outcomeJSON.String == "" || !json.Valid([]byte(command.outcomeJSON.String)) {
			return sessionExecStoredConflict("observation terminal command fields")
		}
		if stored.state != sessionexec.StateCancelled && stored.attempt == 0 {
			return sessionExecStoredConflict("observation terminal command attempt")
		}
		var outcome sessionexec.Outcome
		if err := json.Unmarshal([]byte(command.outcomeJSON.String), &outcome); err != nil {
			return sessionExecStoredConflict("observation terminal outcome")
		}
		canonical, err := sessionexec.NormalizeCompletion(sessionexec.Completion{
			State: stored.state, ErrorCode: command.errorCode.String,
			Error: command.errorText.String, Outcome: outcome,
		})
		if err != nil {
			return sessionExecStoredConflict("observation terminal outcome")
		}
		encoded, err := marshalSessionExecOutcome(canonical.Outcome)
		if err != nil || encoded != command.outcomeJSON.String {
			return sessionExecStoredConflict("observation terminal outcome")
		}
		switch {
		case stored.completedBy.Valid:
			if !stored.completionDigest.Valid || stored.attempt < 1 {
				return sessionExecStoredConflict("observation complete origin")
			}
		case stored.state == sessionexec.StateBlocked:
			if stored.completionDigest.Valid || !command.errorCode.Valid || command.errorCode.String != "ambiguous_effect" ||
				command.errorText.Valid || command.outcomeJSON.String != "{}" {
				return sessionExecStoredConflict("observation ambiguity origin")
			}
		case stored.state == sessionexec.StateCancelled && stored.completionDigest.Valid:
			if !command.errorCode.Valid || command.errorCode.String == "" || command.errorText.Valid || command.outcomeJSON.String != "{}" {
				return sessionExecStoredConflict("observation cancellation origin")
			}
			_, _, digest, err := sessionexec.CompletionDigest(sessionexec.Completion{
				State: sessionexec.StateCancelled, ErrorCode: command.errorCode.String,
			}, nil, 0)
			if err != nil || digest != stored.completionDigest.String {
				return sessionExecStoredConflict("observation cancellation digest")
			}
		case stored.state == sessionexec.StateCancelled:
			if execution.Mode == sessionexec.ExecutionModeHeadless || execution.ReasonCode == "" ||
				!command.errorCode.Valid || command.errorCode.String != execution.ReasonCode ||
				command.errorText.Valid || command.outcomeJSON.String != "{}" {
				return sessionExecStoredConflict("observation quiesce origin")
			}
		default:
			return sessionExecStoredConflict("observation terminal origin")
		}
	}
	return nil
}

func sessionExecObservationTerminalOrigin(command sessionExecObservationCommand) (sessionExecTerminalOrigin, error) {
	stored := command.stored
	switch {
	case stored.state == sessionexec.StateAccepted || stored.state == sessionexec.StateRunning:
		return sessionExecOriginLive, nil
	case stored.completedBy.Valid:
		return sessionExecOriginComplete, nil
	case stored.state == sessionexec.StateBlocked:
		return sessionExecOriginAmbiguity, nil
	case stored.state == sessionexec.StateCancelled && stored.completionDigest.Valid:
		return sessionExecOriginCancelPending, nil
	case stored.state == sessionexec.StateCancelled:
		return sessionExecOriginQuiesce, nil
	default:
		return sessionExecOriginLive, sessionExecStoredConflict("observation terminal origin")
	}
}

func sessionExecSafeObservedCommand(command *sessionExecObservationCommand, origin sessionExecTerminalOrigin) sessionExecObservedCommand {
	stored := &command.stored
	result := sessionExecObservedCommand{
		status: sessionexec.CommandStatus{
			Identity:        stored.command.Identity,
			Type:            stored.command.Type,
			Lane:            stored.command.Lane,
			State:           stored.state,
			Attempt:         stored.attempt,
			TargetCommandID: stored.target.String,
			AcceptedAt:      sessionExecTime(stored.acceptedAt),
			StartedAt:       sessionExecTimePtr(stored.startedAt),
			FinishedAt:      sessionExecTimePtr(command.completedAt),
			ErrorCode:       command.errorCode.String,
		},
		leaseGeneration:   stored.leaseGeneration,
		leaseOwner:        stored.leaseOwner.String,
		leaseOwnerValid:   stored.leaseOwner.Valid,
		leaseExpires:      stored.leaseExpires.Int64,
		leaseExpiresValid: stored.leaseExpires.Valid,
		terminalOrigin:    origin,
	}
	stored.command.Content = ""
	stored.command.InputDigest = ""
	stored.command.AcceptedBy = ""
	stored.completionDigest = sql.NullString{}
	stored.completedBy = sql.NullString{}
	command.errorText = sql.NullString{}
	command.outcomeJSON = sql.NullString{}
	return result
}

func sessionExecObservationSummary(
	db *sessionExecConn,
	sessionID string,
	execution sessionexec.ExecutionState,
) (sessionexec.Summary, error) {
	values := make([]int64, 14)
	args := make([]any, len(values))
	for index := range values {
		args[index] = &values[index]
	}
	if err := db.queryRow(
		sessionExecObservationAggregateSQL,
		sessionID,
		execution.Mode,
		execution.ReasonCode,
		sessionexec.MaxCommandAttempts,
		sessionexec.MaxLeaseOwnerBytes,
		sessionexec.MaxErrorCodeBytes,
		sessionexec.MaxErrorTextBytes,
		sessionexec.MaxOutcomeJSONBytes,
	).Scan(args...); err != nil {
		return sessionexec.Summary{}, fmt.Errorf("summarize observed session commands: %w", err)
	}
	total := values[0]
	stateSum := values[1] + values[2] + values[3] + values[4] + values[5] + values[6] + values[7]
	if total < 0 || total > sessionexec.MaxCommandSequence || stateSum != total || values[8] > values[1] ||
		values[9] > values[1] || values[8]+values[9] != values[1] || values[12] != total || values[13] != 0 ||
		(total == 0 && (values[10] != 0 || values[11] != 0)) ||
		(total > 0 && (values[10] != 1 || values[11] != total)) {
		return sessionexec.Summary{}, sessionExecStoredConflict("observation command aggregate")
	}
	return sessionexec.Summary{
		SessionID: sessionID, Total: int(total), Accepted: int(values[1]), Running: int(values[2]),
		Succeeded: int(values[3]), Failed: int(values[4]), Blocked: int(values[5]),
		Interrupted: int(values[6]), Cancelled: int(values[7]), WorkPending: int(values[8]),
		ControlPending: int(values[9]), LastSequence: values[11],
	}, nil
}

func sessionExecObservationRecentCommands(
	db *sessionExecConn,
	sessionID string,
	limit int,
	execution sessionexec.ExecutionState,
) ([]sessionExecObservedCommand, error) {
	if limit == 0 {
		return nil, nil
	}
	rows, err := db.query(sessionExecObservationRecentSQL, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent observed session commands: %w", err)
	}
	commands, err := sessionExecObservationScanCommands(db, rows, execution)
	if err != nil {
		return nil, err
	}
	for left, right := 0, len(commands)-1; left < right; left, right = left+1, right-1 {
		commands[left], commands[right] = commands[right], commands[left]
	}
	if err := sessionExecObservationValidateTargets(db, commands); err != nil {
		return nil, err
	}
	return commands, nil
}

func sessionExecObservationMissingPermitOwners(
	permits []sessionexec.EffectPermit,
	loaded map[string]sessionExecObservedCommand,
) ([]string, error) {
	owners := make(map[string]struct{}, len(permits))
	for _, permit := range permits {
		if _, exists := loaded[permit.Lease.CommandID]; !exists {
			owners[permit.Lease.CommandID] = struct{}{}
		}
	}
	if len(loaded)+len(owners) > sessionexec.MaxRecentCommandStatuses+sessionexec.MaxEffectPermitsPerSession {
		return nil, sessionExecStoredConflict("observation authentication union")
	}
	result := make([]string, 0, len(owners))
	for commandID := range owners {
		result = append(result, commandID)
	}
	sort.Strings(result)
	return result, nil
}

func sessionExecObservationCommandPage(
	db *sessionExecConn,
	query sessionexec.CommandStatusQuery,
	execution sessionexec.ExecutionState,
) ([]sessionExecObservedCommand, error) {
	statement := `SELECT ` + sessionExecObservationCommandColumns + `
		FROM session_commands WHERE session_id = ? AND sequence > ?`
	args := []any{query.SessionID, query.AfterSequence}
	if len(query.States) > 0 {
		placeholders := make([]string, len(query.States))
		for i, state := range query.States {
			placeholders[i] = "?"
			args = append(args, state)
		}
		statement += ` AND state IN (` + strings.Join(placeholders, ",") + `)`
	}
	statement += ` ORDER BY sequence ASC, command_id ASC LIMIT ?`
	args = append(args, query.Limit+1)
	rows, err := db.query(statement, args...)
	if err != nil {
		return nil, fmt.Errorf("list observed session commands: %w", err)
	}
	commands, err := sessionExecObservationScanCommands(db, rows, execution)
	if err != nil {
		return nil, err
	}
	if err := sessionExecObservationValidateTargets(db, commands); err != nil {
		return nil, err
	}
	return commands, nil
}

func sessionExecObservationOneCommand(
	db *sessionExecConn,
	sessionID, commandID string,
	execution sessionexec.ExecutionState,
) (sessionExecObservedCommand, error) {
	command, err := scanSessionExecObservationCommand(db.queryRow(sessionExecObservationOneSQL, sessionID, commandID))
	if errors.Is(err, sql.ErrNoRows) {
		return sessionExecObservedCommand{}, sessionexec.ErrNotFound
	}
	if err != nil {
		return sessionExecObservedCommand{}, fmt.Errorf("read observed session command: %w", err)
	}
	if err := sessionExecValidateObservationCommand(command, execution); err != nil {
		return sessionExecObservedCommand{}, err
	}
	origin, err := sessionExecObservationTerminalOrigin(command)
	if err != nil {
		return sessionExecObservedCommand{}, err
	}
	sessionExecObservationRecordEnvelope(db)
	observed := sessionExecSafeObservedCommand(&command, origin)
	if err := sessionExecObservationValidateTargets(db, []sessionExecObservedCommand{observed}); err != nil {
		return sessionExecObservedCommand{}, err
	}
	return observed, nil
}

func sessionExecObservationValidateTargets(db *sessionExecConn, commands []sessionExecObservedCommand) error {
	for _, command := range commands {
		if command.status.TargetCommandID == "" {
			continue
		}
		var lane sessionexec.Lane
		var sequence int64
		err := db.queryRow(`SELECT lane, sequence FROM session_commands
			WHERE session_id = ? AND command_id = ?`,
			command.status.SessionID, command.status.TargetCommandID).Scan(&lane, &sequence)
		if errors.Is(err, sql.ErrNoRows) {
			return sessionExecStoredConflict("observation command target")
		}
		if err != nil {
			return fmt.Errorf("read observed command target: %w", err)
		}
		if lane != sessionexec.LaneWork || sequence < 1 || sequence >= command.status.Sequence {
			return sessionExecStoredConflict("observation command target")
		}
	}
	return nil
}

func sessionExecObservationScanCommands(
	db *sessionExecConn,
	rows *sql.Rows,
	execution sessionexec.ExecutionState,
) ([]sessionExecObservedCommand, error) {
	defer rows.Close()
	commands := make([]sessionExecObservedCommand, 0)
	for rows.Next() {
		command, err := scanSessionExecObservationCommand(rows)
		if err != nil {
			return nil, fmt.Errorf("scan observed session command: %w", err)
		}
		if err := sessionExecValidateObservationCommand(command, execution); err != nil {
			return nil, err
		}
		origin, err := sessionExecObservationTerminalOrigin(command)
		if err != nil {
			return nil, err
		}
		sessionExecObservationRecordEnvelope(db)
		commands = append(commands, sessionExecSafeObservedCommand(&command, origin))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate observed session commands: %w", err)
	}
	return commands, nil
}

func sessionExecObservationRecordEnvelope(db *sessionExecConn) {
	if db == nil || db.ctx == nil {
		return
	}
	trace, _ := db.ctx.Value(sessionExecObservationTraceKey{}).(*sessionExecObservationTrace)
	if trace != nil && trace.envelopeLoaded != nil {
		trace.envelopeLoaded()
	}
}

func sessionExecObservationRecordAggregate(db *sessionExecConn) {
	if db == nil || db.ctx == nil {
		return
	}
	trace, _ := db.ctx.Value(sessionExecObservationTraceKey{}).(*sessionExecObservationTrace)
	if trace != nil && trace.aggregateRead != nil {
		trace.aggregateRead()
	}
}

func sessionExecObservationProjectEffects(
	permits []sessionexec.EffectPermit,
	commands map[string]sessionExecObservedCommand,
	requireEveryCommand bool,
) (map[string][]sessionexec.EffectStatus, map[string]sessionexec.EffectSummary, sessionexec.EffectSummary, []sessionexec.EffectStatus, error) {
	type fence struct {
		owner           string
		leaseGeneration int64
		set             bool
	}
	type relation struct {
		ambiguity fence
		quiesce   fence
	}
	type selectedEffect struct {
		permit  sessionexec.EffectPermit
		command sessionExecObservedCommand
		status  sessionexec.EffectStatus
	}
	addFence := func(current *fence, permit sessionexec.EffectPermit) error {
		if !current.set {
			current.owner = permit.Lease.Owner
			current.leaseGeneration = permit.Lease.LeaseGeneration
			current.set = true
			return nil
		}
		if current.owner != permit.Lease.Owner || current.leaseGeneration != permit.Lease.LeaseGeneration {
			return sessionExecStoredConflict("observation effect terminal fence")
		}
		return nil
	}

	relations := make(map[string]relation, len(commands))
	selected := make([]selectedEffect, 0, len(permits))
	for _, permit := range permits {
		command, ok := commands[permit.Lease.CommandID]
		if !ok {
			if requireEveryCommand {
				return nil, nil, sessionexec.EffectSummary{}, nil, sessionExecStoredConflict("observation effect command")
			}
			continue
		}
		if permit.Lease.SessionID != command.status.SessionID ||
			permit.Lease.Generation != command.status.Generation ||
			permit.Lease.LeaseGeneration > command.leaseGeneration {
			return nil, nil, sessionexec.EffectSummary{}, nil, sessionExecStoredConflict("observation effect identity")
		}
		status, err := sessionExecObservationEffectStatus(permit)
		if err != nil {
			return nil, nil, sessionexec.EffectSummary{}, nil, err
		}
		current := relations[permit.Lease.CommandID]
		retainsAmbiguity := permit.AmbiguousAt != nil &&
			(permit.State == sessionexec.EffectStateAmbiguous || permit.State == sessionexec.EffectStateEnded ||
				permit.State == sessionexec.EffectStateResolved)
		if command.terminalOrigin == sessionExecOriginAmbiguity && retainsAmbiguity &&
			permit.Lease.LeaseGeneration == command.leaseGeneration {
			if err := addFence(&current.ambiguity, permit); err != nil {
				return nil, nil, sessionexec.EffectSummary{}, nil, err
			}
		}
		if command.terminalOrigin == sessionExecOriginQuiesce &&
			(permit.State == sessionexec.EffectStateActive || permit.State == sessionexec.EffectStateAmbiguous) {
			if permit.Lease.LeaseGeneration != command.leaseGeneration {
				return nil, nil, sessionexec.EffectSummary{}, nil, sessionExecStoredConflict("observation quiesce effect fence")
			}
			if err := addFence(&current.quiesce, permit); err != nil {
				return nil, nil, sessionexec.EffectSummary{}, nil, err
			}
		}
		relations[permit.Lease.CommandID] = current
		selected = append(selected, selectedEffect{permit: permit, command: command, status: status})
	}
	for commandID, command := range commands {
		if command.terminalOrigin == sessionExecOriginAmbiguity && !relations[commandID].ambiguity.set {
			return nil, nil, sessionexec.EffectSummary{}, nil, sessionExecStoredConflict("observation ambiguity proof")
		}
	}

	effectsByCommand := make(map[string][]sessionexec.EffectStatus, len(commands))
	summaries := make(map[string]sessionexec.EffectSummary, len(commands))
	var overall sessionexec.EffectSummary
	attention := make([]sessionexec.EffectStatus, 0)
	for _, effect := range selected {
		permit := effect.permit
		command := effect.command
		blocking := permit.State == sessionexec.EffectStateActive || permit.State == sessionexec.EffectStateAmbiguous
		switch command.terminalOrigin {
		case sessionExecOriginComplete:
			return nil, nil, sessionexec.EffectSummary{}, nil, sessionExecStoredConflict("observation completed command effect")
		case sessionExecOriginCancelPending:
			if permit.State != sessionexec.EffectStateEnded || permit.AmbiguousAt != nil {
				return nil, nil, sessionexec.EffectSummary{}, nil, sessionExecStoredConflict("observation cancelled effect history")
			}
		case sessionExecOriginQuiesce:
			if blocking {
				current := relations[permit.Lease.CommandID].quiesce
				if !current.set || current.owner != permit.Lease.Owner ||
					current.leaseGeneration != permit.Lease.LeaseGeneration {
					return nil, nil, sessionexec.EffectSummary{}, nil, sessionExecStoredConflict("observation quiesce effect fence")
				}
			}
		case sessionExecOriginAmbiguity:
			if blocking {
				current := relations[permit.Lease.CommandID].ambiguity
				if !current.set || current.owner != permit.Lease.Owner ||
					current.leaseGeneration != permit.Lease.LeaseGeneration {
					return nil, nil, sessionexec.EffectSummary{}, nil, sessionExecStoredConflict("observation ambiguity effect fence")
				}
			}
		case sessionExecOriginLive:
			switch permit.State {
			case sessionexec.EffectStateActive:
				if command.status.State != sessionexec.StateRunning || !command.leaseOwnerValid ||
					command.leaseOwner != permit.Lease.Owner || command.leaseGeneration != permit.Lease.LeaseGeneration ||
					!command.leaseExpiresValid || command.leaseExpires != permit.ExpiresAt.UnixMilli() {
					return nil, nil, sessionexec.EffectSummary{}, nil, sessionExecStoredConflict("observation active effect lease")
				}
			case sessionexec.EffectStateAmbiguous:
				return nil, nil, sessionexec.EffectSummary{}, nil, sessionExecStoredConflict("observation live ambiguous effect")
			case sessionexec.EffectStateEnded:
				if permit.Lease.LeaseGeneration == command.leaseGeneration && permit.AmbiguousAt != nil {
					return nil, nil, sessionexec.EffectSummary{}, nil, sessionExecStoredConflict("observation live ended ambiguity")
				}
			case sessionexec.EffectStateResolved:
				if permit.Lease.LeaseGeneration == command.leaseGeneration {
					return nil, nil, sessionexec.EffectSummary{}, nil, sessionExecStoredConflict("observation live resolved effect")
				}
			}
		default:
			return nil, nil, sessionexec.EffectSummary{}, nil, sessionExecStoredConflict("observation command origin")
		}
		status := effect.status
		summary := summaries[permit.Lease.CommandID]
		if err := sessionExecObservationAddEffectSummary(&summary, status.State); err != nil {
			return nil, nil, sessionexec.EffectSummary{}, nil, err
		}
		summaries[permit.Lease.CommandID] = summary
		if len(effectsByCommand[permit.Lease.CommandID]) < sessionexec.MaxCommandStatusEffects {
			effectsByCommand[permit.Lease.CommandID] = append(effectsByCommand[permit.Lease.CommandID], status)
		}
		if requireEveryCommand {
			if err := sessionExecObservationAddEffectSummary(&overall, status.State); err != nil {
				return nil, nil, sessionexec.EffectSummary{}, nil, err
			}
			if status.State == sessionexec.EffectStateActive || status.State == sessionexec.EffectStateAmbiguous {
				attention = append(attention, status)
			}
		}
	}
	sort.Slice(attention, func(i, j int) bool {
		if !attention[i].CreatedAt.Equal(attention[j].CreatedAt) {
			return attention[i].CreatedAt.Before(attention[j].CreatedAt)
		}
		if attention[i].CommandID != attention[j].CommandID {
			return attention[i].CommandID < attention[j].CommandID
		}
		if attention[i].CommandGeneration != attention[j].CommandGeneration {
			return attention[i].CommandGeneration < attention[j].CommandGeneration
		}
		return attention[i].EffectID < attention[j].EffectID
	})
	return effectsByCommand, summaries, overall, attention, nil
}

func sessionExecObservationEffectStatus(permit sessionexec.EffectPermit) (sessionexec.EffectStatus, error) {
	return sessionexec.EffectStatus{
		SessionID:         permit.Lease.SessionID,
		CommandID:         permit.Lease.CommandID,
		CommandGeneration: permit.Lease.Generation,
		EffectID:          permit.EffectID,
		Kind:              permit.Kind,
		State:             permit.State,
		CreatedAt:         permit.CreatedAt,
		ExpiresAt:         permit.ExpiresAt,
		AmbiguousAt:       permit.AmbiguousAt,
		EndedAt:           permit.EndedAt,
		ResolvedAt:        permit.ResolvedAt,
	}, nil
}

func sessionExecObservationStatuses(
	commands []sessionExecObservedCommand,
	effects map[string][]sessionexec.EffectStatus,
	summaries map[string]sessionexec.EffectSummary,
) []sessionexec.CommandStatus {
	statuses := make([]sessionexec.CommandStatus, 0, len(commands))
	for _, command := range commands {
		status := command.status
		status.EffectSummary = summaries[status.CommandID]
		status.Effects = effects[status.CommandID]
		status.EffectsTruncated = status.EffectSummary.Total > len(status.Effects)
		statuses = append(statuses, status)
	}
	return statuses
}

func sessionExecObservationAddEffectSummary(summary *sessionexec.EffectSummary, state sessionexec.EffectState) error {
	summary.Total++
	switch state {
	case sessionexec.EffectStateActive:
		summary.Active++
	case sessionexec.EffectStateAmbiguous:
		summary.Ambiguous++
	case sessionexec.EffectStateEnded:
		summary.Ended++
	case sessionexec.EffectStateResolved:
		summary.Resolved++
	default:
		return sessionExecStoredConflict("observation effect state")
	}
	return nil
}

var _ sessionexec.MonitorReader = (*Store)(nil)
