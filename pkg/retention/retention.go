// Package retention cleans completed history as a unit, keeping the records
// needed to resume sessions and replay every retained run.
package retention

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"m31labs.dev/buckley/pkg/evidence"
	"m31labs.dev/buckley/pkg/storage"
)

var errPreviewComplete = errors.New("retention: preview complete")

type Policy struct {
	HistoryTTL           time.Duration
	MaxCompletedSessions int
	MaxCompletedRuns     int
	Evidence             evidence.RetentionPolicy
}

type Report struct {
	Sessions       int                   `json:"sessions"`
	Runs           int                   `json:"runs"`
	Pins           int64                 `json:"pins"`
	Evidence       int                   `json:"evidence"`
	Blobs          int                   `json:"blobs"`
	TemporaryFiles int                   `json:"temporary_files"`
	DryRun         bool                  `json:"dry_run"`
	Storage        storage.StorageBudget `json:"storage"`
}

// Cleaner uses the already-migrated shared ledger/evidence database. Sessions
// are optional because standalone goal workers use a separate ledger database.
type Cleaner struct {
	db          *sql.DB
	evidence    *evidence.SQLiteStore
	hasSessions bool
}

func New(db *sql.DB, store *evidence.SQLiteStore) (*Cleaner, error) {
	if db == nil || store == nil || store.DB() != db {
		return nil, fmt.Errorf("retention: ledger and evidence must share a database")
	}
	var hasSessions bool
	if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name = 'sessions' AND type = 'table')`).Scan(&hasSessions); err != nil {
		return nil, err
	}
	return &Cleaner{db: db, evidence: store, hasSessions: hasSessions}, nil
}

// Clean expires completed histories by age or count. Zero limits disable their
// corresponding rule. Protected histories can exceed the configured limits.
func (c *Cleaner) Clean(ctx context.Context, policy Policy, now time.Time, dryRun bool) (Report, error) {
	report := Report{DryRun: dryRun}
	if policy.HistoryTTL < 0 || policy.MaxCompletedSessions < 0 || policy.MaxCompletedRuns < 0 || policy.Evidence.EphemeralTTL < 0 || policy.Evidence.DurableTTL < 0 || policy.Evidence.OtherTTL < 0 {
		return report, fmt.Errorf("retention: limits must be nonnegative")
	}
	var err error
	report.Storage, err = storage.ReadStorageBudget(ctx, c.db)
	if err != nil {
		return report, err
	}
	if !dryRun {
		if err := storage.RecoverStorageBudgetWAL(ctx, c.db); err != nil {
			return report, err
		}
	}
	err = storage.WithSQLiteWriteTransaction(ctx, c.db, func(conn *sql.Conn) error {
		if c.hasSessions {
			if err := storage.NormalizeSessionCompletionTimes(ctx, conn); err != nil {
				return err
			}
		}
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = conn.ExecContext(cleanupCtx, `DROP TABLE IF EXISTS temp.retention_runs`)
			_, _ = conn.ExecContext(cleanupCtx, `DROP TABLE IF EXISTS temp.retention_sessions`)
		}()
		// TEMP tables keep arbitrarily large candidate sets out of Go's heap.
		for _, statement := range []string{
			`CREATE TEMP TABLE IF NOT EXISTS retention_sessions (session_id TEXT PRIMARY KEY)`,
			`CREATE TEMP TABLE IF NOT EXISTS retention_runs (run_id TEXT PRIMARY KEY, session_id TEXT NOT NULL)`,
			`DELETE FROM retention_sessions`, `DELETE FROM retention_runs`,
		} {
			if _, err := conn.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		cutoff := now.Add(-policy.HistoryTTL).UTC().Format(time.RFC3339Nano)
		if c.hasSessions {
			_, err := conn.ExecContext(ctx, `INSERT INTO retention_sessions
				WITH ranked AS (
					SELECT session_id, last_active, completed_at,
						ROW_NUMBER() OVER (ORDER BY julianday(COALESCE(completed_at, last_active)) DESC, session_id DESC) AS rank
					FROM sessions WHERE status = 'completed'
				)
				SELECT session_id FROM ranked AS s WHERE
					((? > 0 AND julianday(last_active) < julianday(?) AND julianday(COALESCE(completed_at, last_active)) < julianday(?)) OR (? > 0 AND rank > ?))
					AND NOT EXISTS (SELECT 1 FROM session_commands WHERE session_id = s.session_id AND state IN ('accepted','running','blocked','interrupted'))
					AND NOT EXISTS (SELECT 1 FROM session_effect_permits WHERE session_id = s.session_id AND state IN ('active','ambiguous'))
					AND NOT EXISTS (SELECT 1 FROM pending_approvals WHERE session_id = s.session_id AND status = 'pending')`,
				policy.HistoryTTL, cutoff, cutoff, policy.MaxCompletedSessions, policy.MaxCompletedSessions)
			if err != nil {
				return err
			}
		}
		sessionGuard := ""
		if c.hasSessions {
			sessionGuard = ` AND (NOT EXISTS (SELECT 1 FROM sessions WHERE session_id = r.session_id)
				OR r.session_id IN (SELECT session_id FROM retention_sessions))`
		}
		_, err := conn.ExecContext(ctx, `INSERT INTO retention_runs
			WITH ranked AS (
				SELECT run_id, session_id, ended_at,
					ROW_NUMBER() OVER (ORDER BY julianday(ended_at) DESC, run_id DESC) AS rank
				FROM agent_runs WHERE status IN ('completed','failed','cancelled') AND ended_at IS NOT NULL
			)
			SELECT run_id, session_id FROM ranked AS r WHERE
				((? > 0 AND julianday(ended_at) < julianday(?)) OR (? > 0 AND rank > ?)
				 OR session_id IN (SELECT session_id FROM retention_sessions))`+sessionGuard+`
				AND NOT EXISTS (SELECT 1 FROM execution_steps WHERE run_id = r.run_id AND status IN ('started','blocked'))
				AND NOT EXISTS (SELECT 1 FROM agent_mailbox WHERE run_id = r.run_id AND state IN ('queued','claimed'))
				AND NOT EXISTS (SELECT 1 FROM agent_run_attempts WHERE run_id = r.run_id AND state = 'attached' AND julianday(lease_expires_at) > julianday(?))
				AND NOT EXISTS (SELECT 1 FROM agent_claims WHERE run_id = r.run_id AND released_at IS NULL)
				AND NOT EXISTS (SELECT 1 FROM launch_model_reservations WHERE run_id = r.run_id AND state IN ('reserved','dispatched','ambiguous','breached'))`,
			policy.HistoryTTL, cutoff, policy.MaxCompletedRuns, policy.MaxCompletedRuns, now.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return err
		}
		// Keep a whole run tree if any member is still retained. This also
		// propagates protection through parents and children in other sessions.
		for {
			result, err := conn.ExecContext(ctx, `DELETE FROM retention_runs WHERE run_id IN (
				SELECT r.run_id FROM agent_runs AS r WHERE
					EXISTS (SELECT 1 FROM agent_runs AS parent WHERE parent.run_id = r.parent_run_id AND parent.run_id NOT IN (SELECT run_id FROM retention_runs))
					OR EXISTS (SELECT 1 FROM agent_runs AS child WHERE child.parent_run_id = r.run_id AND child.run_id NOT IN (SELECT run_id FROM retention_runs))
					OR EXISTS (SELECT 1 FROM context_receipts AS parent JOIN context_receipts AS child ON child.parent_receipt_id = parent.receipt_id
						WHERE parent.run_id = r.run_id AND COALESCE(child.run_id,'') NOT IN (SELECT run_id FROM retention_runs) AND COALESCE(child.session_id,'') NOT IN (SELECT session_id FROM retention_sessions))
					OR EXISTS (SELECT 1 FROM task_checkpoints AS parent JOIN task_checkpoints AS child ON child.parent_checkpoint_id = parent.checkpoint_id
						WHERE parent.run_id = r.run_id AND COALESCE(child.run_id,'') NOT IN (SELECT run_id FROM retention_runs) AND child.session_id NOT IN (SELECT session_id FROM retention_sessions)))`)
			if err != nil {
				return err
			}
			n, err := result.RowsAffected()
			if err != nil {
				return err
			}
			result, err = conn.ExecContext(ctx, `DELETE FROM retention_sessions WHERE EXISTS (
				SELECT 1 FROM agent_runs WHERE session_id = retention_sessions.session_id AND run_id NOT IN (SELECT run_id FROM retention_runs))
				OR EXISTS (SELECT 1 FROM context_receipts WHERE session_id = retention_sessions.session_id AND COALESCE(run_id,'') <> '' AND run_id NOT IN (SELECT run_id FROM retention_runs))
				OR EXISTS (SELECT 1 FROM task_checkpoints WHERE session_id = retention_sessions.session_id AND COALESCE(run_id,'') <> '' AND run_id NOT IN (SELECT run_id FROM retention_runs))
				OR EXISTS (SELECT 1 FROM context_receipts AS parent JOIN context_receipts AS child ON child.parent_receipt_id = parent.receipt_id
					WHERE parent.session_id = retention_sessions.session_id AND COALESCE(child.run_id,'') NOT IN (SELECT run_id FROM retention_runs) AND COALESCE(child.session_id,'') NOT IN (SELECT session_id FROM retention_sessions))
				OR EXISTS (SELECT 1 FROM task_checkpoints AS parent JOIN task_checkpoints AS child ON child.parent_checkpoint_id = parent.checkpoint_id
					WHERE parent.session_id = retention_sessions.session_id AND COALESCE(child.run_id,'') NOT IN (SELECT run_id FROM retention_runs) AND child.session_id NOT IN (SELECT session_id FROM retention_sessions))`)
			if err != nil {
				return err
			}
			m, err := result.RowsAffected()
			if err != nil {
				return err
			}
			n += m
			if c.hasSessions {
				result, err = conn.ExecContext(ctx, `DELETE FROM retention_runs WHERE EXISTS (
					SELECT 1 FROM agent_runs JOIN sessions USING(session_id)
					WHERE agent_runs.run_id = retention_runs.run_id AND session_id NOT IN (SELECT session_id FROM retention_sessions))`)
				if err != nil {
					return err
				}
				m, err := result.RowsAffected()
				if err != nil {
					return err
				}
				n += m
			}
			if n == 0 {
				break
			}
		}
		// A retained run must never lose its associated transcript, even when
		// another run in the same session has already completed.
		if _, err := conn.ExecContext(ctx, `DELETE FROM retention_sessions WHERE EXISTS (
			SELECT 1 FROM agent_runs WHERE session_id = retention_sessions.session_id AND run_id NOT IN (SELECT run_id FROM retention_runs))`); err != nil {
			return err
		}
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM retention_sessions`).Scan(&report.Sessions); err != nil {
			return err
		}
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM retention_runs`).Scan(&report.Runs); err != nil {
			return err
		}
		if dryRun {
			return errPreviewComplete
		}
		// Cross-package relationships without foreign keys need explicit
		// deletion; the run's own events, steps, mailbox, metrics and claims
		// cascade. Release run pins only in this same committed transaction.
		for _, statement := range []string{
			`DELETE FROM context_receipts WHERE run_id IN (SELECT run_id FROM retention_runs) OR session_id IN (SELECT session_id FROM retention_sessions)`,
			`DELETE FROM task_checkpoints WHERE run_id IN (SELECT run_id FROM retention_runs) OR session_id IN (SELECT session_id FROM retention_sessions)`,
			`DELETE FROM agent_runs WHERE run_id IN (SELECT run_id FROM retention_runs)`,
		} {
			if _, err := conn.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		result, err := conn.ExecContext(ctx, `DELETE FROM evidence_pins WHERE
			reason IN (SELECT 'run:' || run_id FROM retention_runs)
			OR reason IN (SELECT 'session:' || session_id FROM retention_sessions)`)
		if err != nil {
			return err
		}
		report.Pins, err = result.RowsAffected()
		if err != nil {
			return err
		}
		if c.hasSessions {
			for _, statement := range []string{
				`DELETE FROM ipc_events WHERE session_id IN (SELECT session_id FROM retention_sessions)`,
				`DELETE FROM sessions WHERE session_id IN (SELECT session_id FROM retention_sessions)`,
			} {
				if _, err := conn.ExecContext(ctx, statement); err != nil {
					return err
				}
			}
		}
		// Logical goal sessions can exist without a sessions row. Release
		// their association pins only after the last owning run has expired.
		sessionPinGuard := ""
		if c.hasSessions {
			sessionPinGuard = ` AND NOT EXISTS (SELECT 1 FROM sessions WHERE 'session:' || session_id = reason)`
		}
		result, err = conn.ExecContext(ctx, `DELETE FROM evidence_pins WHERE reason IN (
			SELECT 'session:' || session_id FROM retention_runs)
			AND NOT EXISTS (SELECT 1 FROM agent_runs WHERE 'session:' || session_id = reason)`+sessionPinGuard)
		if err != nil {
			return err
		}
		pins, err := result.RowsAffected()
		if err != nil {
			return err
		}
		report.Pins += pins
		return nil
	})
	if errors.Is(err, errPreviewComplete) {
		return report, nil
	}
	if err != nil {
		return Report{DryRun: dryRun}, fmt.Errorf("retention: prune history: %w", err)
	}
	report.Evidence, err = c.evidence.SweepCount(ctx, policy.Evidence, now)
	if err != nil {
		return report, err
	}
	report.Blobs, err = c.evidence.CleanupOrphanBlobCount(ctx)
	if err != nil {
		return report, err
	}
	report.TemporaryFiles, err = c.evidence.CleanupStaleTemps(ctx, now.Add(-24*time.Hour))
	if err != nil {
		return report, err
	}
	if err := storage.ReclaimSQLite(ctx, c.db, false); err != nil {
		return report, err
	}
	report.Storage, err = storage.ReadStorageBudget(ctx, c.db)
	return report, err
}
