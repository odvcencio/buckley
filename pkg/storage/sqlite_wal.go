package storage

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	sqlite3 "modernc.org/sqlite/lib"
)

const (
	sqliteWALBusyTimeout = 5 * time.Second
	sqliteWALRetryLimit  = 8
)

// EnableSQLiteWAL negotiates WAL mode on one dedicated connection before
// callers acquire migration ownership. Concurrent process-like openers may
// race on the journal-mode write; busy_timeout plus bounded backoff lets the
// winner finish without holding a connection across BEGIN IMMEDIATE.
func EnableSQLiteWAL(db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("sqlite WAL database is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), sqliteWALBusyTimeout*time.Duration(sqliteWALRetryLimit))
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire SQLite WAL connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, fmt.Sprintf(`PRAGMA busy_timeout = %d`, sqliteWALBusyTimeout.Milliseconds())); err != nil {
		return fmt.Errorf("configure SQLite WAL busy timeout: %w", err)
	}

	delay := 10 * time.Millisecond
	var lastErr error
	for attempt := 1; attempt <= sqliteWALRetryLimit; attempt++ {
		var mode string
		err := conn.QueryRowContext(ctx, `PRAGMA journal_mode = WAL`).Scan(&mode)
		mode = strings.ToLower(strings.TrimSpace(mode))
		if err == nil && (mode == "wal" || mode == "memory") {
			return nil
		}
		if err != nil {
			lastErr = err
			if !IsSQLiteBusyError(err) {
				return fmt.Errorf("enable SQLite WAL mode: %w", err)
			}
		} else {
			lastErr = fmt.Errorf("SQLite returned journal mode %q", mode)
		}
		if attempt == sqliteWALRetryLimit {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("enable SQLite WAL mode: %w", ctx.Err())
		case <-timer.C:
		}
		if delay < 250*time.Millisecond {
			delay *= 2
		}
	}
	return fmt.Errorf("enable SQLite WAL mode after %d attempts: %w", sqliteWALRetryLimit, lastErr)
}

type sqliteCodeError interface {
	error
	Code() int
}

const (
	sqliteErrorTraversalMaxDepth = 64
	sqliteErrorTraversalMaxNodes = 1024
)

// IsSQLiteBusyError reports whether any error in err's unwrap tree has a BUSY
// or LOCKED primary SQLite result code.
func IsSQLiteBusyError(root error) bool {
	if root == nil {
		return false
	}
	type errorNode struct {
		err   error
		depth int
	}
	queue := make([]errorNode, 0, 8)
	queue = append(queue, errorNode{err: root})
	for head := 0; head < len(queue) && head < sqliteErrorTraversalMaxNodes; head++ {
		node := queue[head]
		if node.err == nil || node.depth > sqliteErrorTraversalMaxDepth {
			continue
		}
		if sqliteErr, ok := node.err.(sqliteCodeError); ok && isSQLiteBusyCode(sqliteErr.Code()) {
			return true
		}
		if node.depth == sqliteErrorTraversalMaxDepth {
			continue
		}
		switch wrapped := node.err.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range wrapped.Unwrap() {
				if child != nil && len(queue) < sqliteErrorTraversalMaxNodes {
					queue = append(queue, errorNode{err: child, depth: node.depth + 1})
				}
			}
		case interface{ Unwrap() error }:
			if child := wrapped.Unwrap(); child != nil && len(queue) < sqliteErrorTraversalMaxNodes {
				queue = append(queue, errorNode{err: child, depth: node.depth + 1})
			}
		}
	}
	return false
}

func isSQLiteBusyCode(code int) bool {
	primaryCode := code & 0xff
	return primaryCode == sqlite3.SQLITE_BUSY || primaryCode == sqlite3.SQLITE_LOCKED
}
