package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// WithSQLiteWriteTransaction serializes database and filesystem changes with
// other writers, including stores opened by another process. All SQL in fn must
// use conn; using the pool can deadlock when it has only one connection.
func WithSQLiteWriteTransaction(ctx context.Context, db *sql.DB, fn func(conn *sql.Conn) error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin SQLite write: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(cleanup, "ROLLBACK")
	}()
	if err := fn(conn); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, "COMMIT")
	committed = err == nil
	return err
}

// EnableSQLiteIncrementalVacuum enables bounded reclamation for new databases.
// Existing databases with auto_vacuum=NONE need an explicit VACUUM to adopt it.
func EnableSQLiteIncrementalVacuum(db *sql.DB) error {
	_, err := db.Exec("PRAGMA auto_vacuum = INCREMENTAL")
	return err
}

// ReclaimSQLite merges obsolete search-index entries and frees unused pages.
// A legacy database can adopt incremental reclamation with fullVacuum=true.
func ReclaimSQLite(ctx context.Context, db *sql.DB, fullVacuum bool) error {
	if err := RecoverStorageBudgetWAL(ctx, db); err != nil {
		return err
	}
	if err := truncateSQLiteWAL(ctx, db); err != nil {
		return err
	}
	if err := reclaimMessagesFTS(ctx, db); err != nil {
		return err
	}
	if fullVacuum {
		if _, err := db.ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, "VACUUM"); err != nil {
			return fmt.Errorf("vacuum SQLite: %w", err)
		}
	} else {
		// Consume the result to let SQLite finish all requested page moves.
		rows, err := db.QueryContext(ctx, "PRAGMA incremental_vacuum(2048)")
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
	}
	return truncateSQLiteWAL(ctx, db)
}

func truncateSQLiteWAL(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var busyTimeout int
	if err := conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA busy_timeout = 0"); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(cleanup, fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeout))
	}()
	// A reader or writer may defer truncation until the next pass. Never wait
	// for foreground work just to trim the WAL's high-water allocation.
	var busy, pages, checkpointed int
	return conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &pages, &checkpointed)
}

func reclaimMessagesFTS(ctx context.Context, db *sql.DB) error {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name = 'messages_fts' AND type = 'table')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return nil
	}
	return WithSQLiteWriteTransaction(ctx, db, func(conn *sql.Conn) error {
		var before, after int64
		if err := conn.QueryRowContext(ctx, "SELECT total_changes()").Scan(&before); err != nil {
			return err
		}
		// Continue an unfinished merge before forcing a new one. Repeated
		// negative budgets would restart it whenever foreground work adds a
		// segment. Each pass writes roughly 128 FTS pages (about 512 KiB).
		if _, err := conn.ExecContext(ctx, `INSERT INTO messages_fts(messages_fts, rank) VALUES('merge', 128)`); err != nil {
			return fmt.Errorf("merge message search index: %w", err)
		}
		if err := conn.QueryRowContext(ctx, "SELECT total_changes()").Scan(&after); err != nil {
			return err
		}
		// FTS5 reports fewer than two changes when the merge did no work.
		if after-before < 2 {
			if _, err := conn.ExecContext(ctx, `INSERT INTO messages_fts(messages_fts, rank) VALUES('merge', -128)`); err != nil {
				return fmt.Errorf("start message search index merge: %w", err)
			}
		}
		return nil
	})
}

// NormalizeSessionCompletionTimes repairs time.Time values written by older
// versions in a format SQLite cannot compare for history retention.
func NormalizeSessionCompletionTimes(ctx context.Context, db *sql.Conn) error {
	var cursor int64
	for {
		rows, err := db.QueryContext(ctx, `SELECT rowid, CAST(completed_at AS TEXT) FROM sessions
			WHERE rowid > ? AND completed_at IS NOT NULL AND julianday(completed_at) IS NULL
			ORDER BY rowid LIMIT 256`, cursor)
		if err != nil {
			return err
		}
		var batch []struct {
			id  int64
			raw string
		}
		for rows.Next() {
			var item struct {
				id  int64
				raw string
			}
			if err := rows.Scan(&item.id, &item.raw); err != nil {
				_ = rows.Close()
				return err
			}
			batch = append(batch, item)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		for _, item := range batch {
			cursor = item.id
			parsed := parseSQLiteTimestamp(item.raw)
			if parsed.IsZero() {
				continue
			}
			if _, err := db.ExecContext(ctx, `UPDATE sessions SET completed_at = ?
				WHERE rowid = ? AND completed_at = ?`, sqliteTimestamp(parsed), item.id, item.raw); err != nil {
				return err
			}
		}
	}
}
